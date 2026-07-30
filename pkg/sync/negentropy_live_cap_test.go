// The LIVE measurement of the relay window the paging walk exists to defeat
// (ready-bec).
//
// WHY THIS FILE EXISTS SEPARATELY FROM negentropy_paging_test.go. The defect is
// a RELAY behaviour: one query reconciles at most a bounded number of records and
// nothing in NIP-77 says whether that bound was reached. cappedRelay (the
// in-process fake in negentropy_paging_test.go) MODELS that behaviour so the walk
// can be driven deterministically at a chosen bound — but a model is worth only
// what its agreement with the modelled thing is worth, and until this file the
// only evidence that a real relay behaves that way lived as prose in a commit
// message. A regression reintroducing the truncation would have been caught by
// the fake and by nothing that touched a real relay.
//
// So this test measures the three cappedRelay behaviours against a real strfry,
// with rd's own shipped board filter, and then proves the walk recovers a whole
// board across them:
//
//	one query answers at most `limit` records   (the truncation)
//	it picks the NEWEST ones                    (so `until` can walk backwards)
//	it honours `until`                          (so the walk terminates)
//	NegentropySync -> the whole board, pages>1  (the fix)
//
// Gated behind RD_NOSTR_LIVE_RELAY=1, like every other live proof in this
// package (ready-5fd made that gate honest: these proofs RUN, they do not skip).
package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// liveCapBoardSize is how many events this proof puts on the relay. It must
// EXCEED the window rd asks for (SyncPageLimit), or "the relay truncated" and
// "the board is small" are indistinguishable and nothing here measures anything.
// +60 clears it while keeping the publish cheap and the walk at a readable two
// windows.
const liveCapBoardSize = SyncPageLimit + 60

// TestLiveRelay_OneQueryIsBoundedAndTheWalkPagesPastIt is the item's live proof,
// and the check on the fake that stands in for the relay everywhere else.
//
// It publishes liveCapBoardSize distinct cards to an isolated, per-run board on a
// REAL relay — a board of known size, which is what makes every count below
// meaningful — and then measures:
//
//  1. THE BOUND IS REAL. One NIP-77 exchange carrying the filter rd's walk
//     actually ships (limit=SyncPageLimit, no cursor) reconciles exactly
//     SyncPageLimit of the liveCapBoardSize events, reports no error, and gives
//     the client no way to tell that from a board of exactly that size. That is
//     the silent truncation, measured rather than asserted.
//  2. IT IS THE NEWEST SLICE, AND `until` MOVES IT. The bound window holds the
//     newest records; re-asking with `until` at that window's floor returns the
//     older ones. These are the two properties the `until` walk stands on and the
//     two cappedRelay reproduces.
//  3. THE WALK DEFEATS IT. A fresh empty log syncing the same board downloads
//     every event over more than one window, projects every card, and converges:
//     the second sync moves zero event bytes.
//
// It also RECORDS, without asserting, what the same exchange returns with no
// limit at all — the relay's own default bound, which is configuration this repo
// does not own and which differs per relay (see SyncPageLimit's doc for the
// wss://relay.3dl.network measurement). The walk must not depend on it, and this
// test does not.
//
// No `authors` filter is used anywhere: it under-returns on relay.3dl.network
// (ready-5c5), which would confound a measurement whose entire point is counting.
func TestLiveRelay_OneQueryIsBoundedAndTheWalkPagesPastIt(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 to run the live relay-window measurement")
	}
	relay := liveRelayURL(t)
	k := liveRelayKey(t) // stable author: this proof reads its own writes back
	boardD := liveTestBoardD(t)
	boardCoord := BoardCoord(k.PubKeyHex(), boardD)
	t.Logf("live relay: %s  board: %s", relay, boardCoord)

	// A board of a KNOWN size, one event per second so the `until` cursor has
	// somewhere to step. Signed directly rather than through Publisher because
	// this proof needs exact control of created_at and of how many events exist.
	base := time.Now().Unix() - int64(liveCapBoardSize) - 10
	events := pagingBoard(t, k, boardCoord, liveCapBoardSize, base)
	newest := events[len(events)-1].CreatedAt

	ctx := context.Background()
	pctx, pcancel := context.WithTimeout(ctx, 5*time.Minute)
	acks, err := GuardedPublishMany(pctx, relay, events, false)
	pcancel()
	if err != nil {
		t.Fatalf("publish %d events: %v", liveCapBoardSize, err)
	}
	for i, a := range acks {
		if a.Err != nil {
			t.Fatalf("publish event %d (%s): %v", i, a.EventID, a.Err)
		}
		if !a.Accepted {
			t.Fatalf("relay refused event %d (%s): %q — this proof needs the whole board on the relay", i, a.EventID, a.Message)
		}
	}
	t.Logf("published %d distinct cards (created_at %d..%d)", len(events), base, newest)

	filter := BoardSyncFilter(boardCoord, nil)
	reconcile := func(f map[string]any) []string {
		t.Helper()
		rctx, rcancel := context.WithTimeout(ctx, 60*time.Second)
		r, err := nostr.NegentropyReconcile(rctx, relay, f, nil)
		rcancel()
		if err != nil {
			t.Fatalf("reconcile %v: %v", f, err)
		}
		return r.Need
	}

	// Context, not an assertion: what the relay volunteers when the client names
	// no window at all. This is per-relay configuration rd neither owns nor can
	// read back, and the walk is built so it does not matter.
	t.Logf("CONTEXT: one exchange with NO limit reconciled %d of %d events on %s",
		len(reconcile(filter)), liveCapBoardSize, relay)

	// (1) THE BOUND, measured with the filter the shipped walk builds for its
	// first window. An empty local set makes `need` exactly the record count the
	// relay was willing to reconcile.
	first := syncWindowFilter(filter, 0, false)
	need := reconcile(first)
	t.Logf("MEASURED: one exchange at limit=%d reconciled %d of %d events", SyncPageLimit, len(need), liveCapBoardSize)
	if len(need) != SyncPageLimit {
		t.Fatalf("a single exchange asking for limit=%d returned %d records of a %d-event board — cappedRelay models the relay as answering exactly the limit, and it does not. Re-derive the model against %s",
			SyncPageLimit, len(need), liveCapBoardSize, relay)
	}
	if len(need) >= liveCapBoardSize {
		t.Fatalf("one query answered with the whole %d-event board — nothing here measures a bounded window", liveCapBoardSize)
	}

	// (2) That slice is the NEWEST records, and `until` moves the window to the
	// older ones. Both are load-bearing: newest-first is why walking `until`
	// backwards terminates, and an ignored `until` would make the walk re-ask the
	// same window forever (which MaxSyncPages would then abort).
	served := map[string]bool{}
	for _, id := range need {
		served[id] = true
	}
	oldestServed := int64(1 << 62)
	for _, e := range events {
		if served[e.ID] && e.CreatedAt < oldestServed {
			oldestServed = e.CreatedAt
		}
	}
	if oldestServed != newest-int64(SyncPageLimit)+1 {
		t.Fatalf("the bounded window is not the NEWEST %d records: its floor is created_at=%d, want %d",
			SyncPageLimit, oldestServed, newest-int64(SyncPageLimit)+1)
	}
	older := reconcile(syncWindowFilter(filter, oldestServed-1, true))
	t.Logf("MEASURED: the same query with until=%d returned %d older records", oldestServed-1, len(older))
	if len(older) != liveCapBoardSize-SyncPageLimit {
		t.Fatalf("until=%d must return the %d records below the first window, got %d — `until` is not bounding the relay's window",
			oldestServed-1, liveCapBoardSize-SyncPageLimit, len(older))
	}
	for _, id := range older {
		if served[id] {
			t.Fatalf("the until-bounded window re-served event %s from the first window — the cursor does not exclude", id)
		}
	}

	// (3) THE WALK. A fresh clone: empty log, same board, paged sync.
	log := NewNostrLog(filepath.Join(t.TempDir(), NostrLogFile))
	trusted := map[string]bool{k.PubKeyHex(): true}
	res, err := NegentropySync(ctx, relay, log, filter, trusted, 120*time.Second, false)
	if err != nil {
		t.Fatalf("paged sync: %v", err)
	}
	t.Logf("PAGED: need=%d downloaded=%d pages=%d (one window stops at %d)",
		res.Need, res.Downloaded, res.Pages, SyncPageLimit)
	if res.Pages < 2 {
		t.Fatalf("a %d-event board at a %d-record window must take more than one window, walk used pages=%d",
			liveCapBoardSize, SyncPageLimit, res.Pages)
	}
	if res.Downloaded != liveCapBoardSize {
		t.Fatalf("a fresh clone must download the WHOLE board: got downloaded=%d of %d (need=%d pages=%d)",
			res.Downloaded, liveCapBoardSize, res.Need, res.Pages)
	}

	// The substance of the done condition: the fresh clone PROJECTS every card,
	// not merely holds every event.
	items := ProjectItems(mustReadAll(t, log), ProjectOptions{Trusted: trusted})
	var missing []string
	for i := 0; i < liveCapBoardSize; i++ {
		id := fmt.Sprintf("ready-pg%04d", i)
		if items[id] == nil {
			missing = append(missing, id)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("fresh clone projected %d of %d cards — %d missing, first %v",
			len(items), liveCapBoardSize, len(missing), missing[:min(5, len(missing))])
	}

	// And it CONVERGES: syncing again moves nothing. Pre-fix a re-sync moved
	// nothing too — while short — which is exactly why silence was the defect.
	res2, err := NegentropySync(ctx, relay, log, filter, trusted, 120*time.Second, false)
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res2.Downloaded != 0 || res2.Uploaded != 0 || res2.EventBytesDownloaded != 0 || res2.EventBytesUploaded != 0 {
		t.Fatalf("converged re-sync must move zero event bytes, got downloaded=%d uploaded=%d down=%dB up=%dB",
			res2.Downloaded, res2.Uploaded, res2.EventBytesDownloaded, res2.EventBytesUploaded)
	}
	t.Logf("PROVEN against %s: one query is bounded at %d newest records and says nothing; `until` walks past it; the fresh clone fetched all %d over %d windows and the re-sync moved 0 event bytes",
		relay, SyncPageLimit, liveCapBoardSize, res.Pages)
}
