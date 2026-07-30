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
// It is a CONTROLLED experiment: a board of a size this file chose, on a relay it
// publishes to. The field counterpart lives at the bottom of this file —
// TestLiveRelay_FreshCloneOfTheProductionBoardIsWhole reads the real 3dl board off
// wss://relay.3dl.network, the relay/board pair the truncation was first measured
// on and the one ready-bec's done condition names. Neither subsumes the other: a
// change in 3dl's clamping is invisible to this test, and a board this test can
// characterise is not available there.
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
// +60 clears it while keeping the publish cheap and the walk short: measured
// pages=3 — the 500-record window, the 60 below it, and the closing window that
// finds the cursor cannot move (the walk terminates on the CURSOR, never on a
// window's size; see NegentropySync).
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

// ---------------------------------------------------------------------------
// The named measurement: the REAL board on the REAL relay the defect was found on
// ---------------------------------------------------------------------------

// prodRelayURL / prodBoardCoord name the exact relay and board ready-bec's done
// condition speaks about: "a fresh clone of the 3dl board projects all 536
// cards". Every other proof in this package runs against a board it published
// itself, which is what makes them controlled — and is also why none of them
// would have caught a regression against THIS relay and THIS board, the pair the
// truncation was actually measured on (an unpaged sync merged 481 events and
// projected 201 items of a 536-card board, twice, silently).
//
// The board coordinate is this repo's own pinned board (.ready/config.json
// "board"), whose D-tag is the reserved production one. This test only ever READS
// it: the local log starts empty so the walk has nothing to upload, and
// GuardedPublish is called with production=false, which refuses any event
// addressing that coordinate before a relay is dialled (guardReservedBoard). The
// Uploaded assertion below pins that read-only property rather than assuming it.
//
// Both are overridable so the proof can be pointed at another deployment.
func prodRelayURL() string {
	if r := os.Getenv("RD_NOSTR_PROD_RELAY_URL"); r != "" {
		return r
	}
	return "wss://relay.3dl.network"
}

func prodBoardCoord() string {
	if c := os.Getenv("RD_NOSTR_PROD_BOARD_COORD"); c != "" {
		return c
	}
	return "30301:a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25:ready"
}

// prodBoardCardFloor is the card count measured on that board on 2026-07-30 and
// written into ready-bec's done condition. It is asserted as a FLOOR, not an
// equality: rd's boards only gain cards, so a hard 536 would go red the next time
// anyone files an item, and a flaky proof proves nothing. The floor still fails
// loudly on the defect it exists for — the truncated sync projected 201.
const prodBoardCardFloor = 536

// pagedCoordWalk is the INDEPENDENT ground truth: a plain NIP-01 REQ walk over an
// `#a` coordinate filter, paged on `until` at limit >= 500, run with none of the
// code under test. It deliberately shares nothing with NegentropySync — different
// protocol (REQ/EOSE, not NIP-77), its own filter construction, its own cursor —
// so "the sync got everything" is checked against a second opinion rather than
// against itself.
//
// No `authors` filter appears here or anywhere in this test: it silently
// under-returns on relay.3dl.network (ready-5c5 measured 42 of 56 boards), which
// would confound a measurement whose entire point is counting.
func pagedCoordWalk(ctx context.Context, t *testing.T, relay string, base map[string]any) map[string]*nostr.Event {
	t.Helper()
	seen := map[string]*nostr.Event{}
	var until int64
	bounded := false
	for page := 1; page <= MaxSyncPages; page++ {
		f := map[string]any{"limit": SyncPageLimit}
		for k, v := range base {
			f[k] = v
		}
		if bounded {
			f["until"] = until
		}
		fctx, cancel := context.WithTimeout(ctx, 90*time.Second)
		evs, err := nostr.FetchMany(fctx, relay, f)
		cancel()
		if err != nil {
			t.Fatalf("ground-truth walk page %d (until=%d): %v", page, until, err)
		}
		oldest := int64(1<<62 - 1)
		fresh := 0
		for _, e := range evs {
			if e == nil {
				continue
			}
			if e.CreatedAt < oldest {
				oldest = e.CreatedAt
			}
			if _, dup := seen[e.ID]; !dup {
				seen[e.ID] = e
				fresh++
			}
		}
		t.Logf("ground truth page %d: until=%d served=%d new=%d cumulative=%d", page, until, len(evs), fresh, len(seen))
		if len(evs) == 0 {
			break
		}
		if bounded && oldest >= until {
			break
		}
		until = oldest
		bounded = true
	}
	return seen
}

// cardIDs returns the distinct rd item ids (30302 "d" tags) among a set of events
// — the unit ready-bec's done condition counts.
func cardIDs(events map[string]*nostr.Event) map[string]bool {
	out := map[string]bool{}
	for _, e := range events {
		if e.Kind == KindCard {
			if d := tagValue(e, "d"); d != "" {
				out[d] = true
			}
		}
	}
	return out
}

// TestLiveRelay_FreshCloneOfTheProductionBoardIsWhole takes the measurement
// ready-bec's done condition NAMES and nothing on this branch had taken: a fresh
// clone of the 3dl board, from wss://relay.3dl.network, holding every event and
// projecting every card.
//
// The sibling proof (TestLiveRelay_OneQueryIsBoundedAndTheWalkPagesPastIt) is the
// controlled experiment — a board of a size this repo chose, on a relay whose cap
// this repo can characterise. This one is the FIELD measurement, and the two catch
// different regressions: the controlled proof would stay green if
// relay.3dl.network changed its cap, its clamping behaviour, or its handling of a
// 1800-event coordinate, because it never touches it. The done condition names
// this relay and this board precisely because that is where the truncation was
// observed.
//
// Read-only by construction: empty local log (nothing to upload) plus
// production=false (GuardedPublish refuses the reserved production coordinate
// before dialling). Uploaded==0 is asserted, not assumed.
func TestLiveRelay_FreshCloneOfTheProductionBoardIsWhole(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 to run the live production-board fresh-clone proof")
	}
	relay := prodRelayURL()
	coord := prodBoardCoord()
	t.Logf("live production relay: %s  board: %s", relay, coord)

	ctx := context.Background()
	filter := BoardSyncFilter(coord, nil)

	// (0) GROUND TRUTH FIRST, and deliberately first: the board is live, so
	// anything published between this walk and the sync can only make the sync
	// hold MORE. Every assertion below is therefore monotone-safe.
	truth := pagedCoordWalk(ctx, t, relay, filter)
	truthCards := cardIDs(truth)
	t.Logf("GROUND TRUTH (independent paged #a REQ walk): %d events, %d distinct cards", len(truth), len(truthCards))
	if len(truth) <= SyncPageLimit {
		t.Fatalf("the board on %s holds %d events, which is not more than one window (%d) — this proof cannot measure paging against it. Point RD_NOSTR_PROD_BOARD_COORD at a board bigger than %d.",
			relay, len(truth), SyncPageLimit, SyncPageLimit)
	}
	if len(truthCards) < prodBoardCardFloor {
		t.Fatalf("the independent walk found %d cards on %s, below the %d measured into ready-bec's done condition — either the walk regressed or the board lost cards",
			len(truthCards), coord, prodBoardCardFloor)
	}

	// (1) THE TRUNCATION, on this relay, with the filter rd's first window ships.
	// An empty local set makes `need` exactly what one query was willing to give.
	rctx, rcancel := context.WithTimeout(ctx, 90*time.Second)
	one, err := nostr.NegentropyReconcile(rctx, relay, syncWindowFilter(filter, 0, false), nil)
	rcancel()
	if err != nil {
		t.Fatalf("single-window reconcile: %v", err)
	}
	t.Logf("MEASURED: ONE exchange at limit=%d reconciled %d of the %d events on this board", SyncPageLimit, len(one.Need), len(truth))
	if len(one.Need) >= len(truth) {
		t.Fatalf("one query answered with all %d events — %s no longer truncates, so this proof measures nothing", len(truth), relay)
	}

	// (2) THE FRESH CLONE. Trust gate disabled (nil) on purpose: this measures the
	// PAGING walk, and a trust set would make the count a function of which keys
	// hold grants on the live board. The gate is proven separately, against a
	// controlled board, by TestLiveRelay_NegentropyDownloadTrustGate.
	log := NewNostrLog(filepath.Join(t.TempDir(), NostrLogFile))
	res, err := NegentropySync(ctx, relay, log, filter, nil, 180*time.Second, false)
	if err != nil {
		t.Fatalf("fresh-clone sync: %v", err)
	}
	t.Logf("FRESH CLONE: need=%d downloaded=%d uploaded=%d pages=%d", res.Need, res.Downloaded, res.Uploaded, res.Pages)
	if res.Uploaded != 0 {
		t.Fatalf("this proof must be READ-ONLY against the production board, it uploaded %d events", res.Uploaded)
	}
	if res.Pages < 2 {
		t.Fatalf("a %d-event board behind a %d-record window must take more than one window, walk used pages=%d",
			len(truth), len(one.Need), res.Pages)
	}

	// (3) THE CLONE HOLDS EVERYTHING THE INDEPENDENT WALK FOUND. This is the
	// assertion the truncation fails: the measured defect merged 481 of them.
	stored, err := log.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	have := map[string]bool{}
	for _, e := range stored {
		if e != nil {
			have[e.ID] = true
		}
	}
	var missing []string
	for id := range truth {
		if !have[id] {
			missing = append(missing, id)
		}
	}
	if len(missing) != 0 {
		t.Fatalf("fresh clone is SHORT: it holds %d of the %d events the independent walk found on %s — %d missing, first %v",
			len(have), len(truth), relay, len(missing), missing[:min(5, len(missing))])
	}

	// (4) AND IT PROJECTS EVERY CARD — the done condition's own words.
	items := ProjectItems(stored, ProjectOptions{})
	var unprojected []string
	for id := range truthCards {
		if items[id] == nil {
			unprojected = append(unprojected, id)
		}
	}
	if len(unprojected) != 0 {
		t.Fatalf("fresh clone projected %d items but %d of the %d cards on the board are missing, first %v",
			len(items), len(unprojected), len(truthCards), unprojected[:min(5, len(unprojected))])
	}
	if len(items) < prodBoardCardFloor {
		t.Fatalf("fresh clone projected %d items, below the %d ready-bec's done condition names (the truncated sync projected 201)",
			len(items), prodBoardCardFloor)
	}
	t.Logf("PROVEN against %s: one query gives %d of %d events; the paged walk gives all %d over %d windows and the fresh clone projects %d items (>= the %d named in ready-bec)",
		relay, len(one.Need), len(truth), len(truth), res.Pages, len(items), prodBoardCardFloor)
}
