package sync

// ready-207: DiscoverLiveBoards / InventoryBoardCards must reproduce the
// 2026-07-29T04:19Z measurement's method exactly — kind-only / #a-only relay
// queries, owner narrowed CLIENT-SIDE, never a relay-side "authors" filter,
// against a relay that models the deployed relay's measured author-index
// defect (storeRelay.underReturnAuthors, ready-d84/ready-5c5). A test that
// only proved the happy-path counts, without also proving the query shape
// survives an authors-hostile relay, would not prove the one property this
// item's DONE CONDITION depends on.

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

func TestDiscoverLiveBoards_OwnerNarrowedAndArchivedDropped(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen owner key: %v", err)
	}
	other, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen other key: %v", err)
	}
	base := time.Now().Unix() - 10000

	live, err := BuildBoardEvent(owner, BoardSpec{BoardD: "live1", Title: "live1"}, base)
	if err != nil {
		t.Fatalf("live board: %v", err)
	}
	// A second, later republish of an already-archived board: the archived
	// marker must survive latest-wins, not just first-seen.
	archivedV1, err := BuildBoardEvent(owner, BoardSpec{BoardD: "gone", Title: "gone"}, base+1)
	if err != nil {
		t.Fatalf("gone v1: %v", err)
	}
	archivedV2, err := BuildBoardEvent(owner, BoardSpec{BoardD: "gone", Title: "gone", Archived: true}, base+2)
	if err != nil {
		t.Fatalf("gone v2: %v", err)
	}
	// Someone else's board on the same relay: must never surface for owner.
	foreign, err := BuildBoardEvent(other, BoardSpec{BoardD: "live1", Title: "not owner's"}, base+1)
	if err != nil {
		t.Fatalf("foreign board: %v", err)
	}

	// Both serve orders, for the reason given on runInventoryDedupCase: under
	// newest-first the archived V2 arrives first and a first-wins dedup looks
	// identical to latest-wins. Under oldest-first, a first-wins dedup resurrects
	// the un-archived V1 and reports a dead board as live — which is the failure
	// this assertion is for.
	for _, oldestFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("oldestFirst=%v", oldestFirst), func(t *testing.T) {
			relay := newStoreRelay(t)
			relay.underReturnAuthors = true // the deployed relay's measured defect
			relay.serveOldestFirst = oldestFirst
			for _, e := range []*nostr.Event{live, archivedV2, foreign} {
				relay.putRaw(e)
			}
			// The superseded, NOT-archived version stays on the wire alongside V2
			// (putRaw would have replaced it, leaving nothing to choose between).
			relay.putDup(archivedV1)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			got, err := DiscoverLiveBoards(ctx, relay.url, owner.PubKeyHex())
			if err != nil {
				t.Fatalf("DiscoverLiveBoards: %v", err)
			}
			if len(got) != 1 {
				t.Fatalf("got %d live boards, want exactly 1 (archived + foreign must be excluded): %+v", len(got), got)
			}
			if got[0].D != "live1" || got[0].Coord != BoardCoord(owner.PubKeyHex(), "live1") {
				t.Fatalf("got board %+v, want the owner's live1", got[0])
			}
		})
	}
}

func TestDiscoverLiveBoards_AuthorsFilterWouldHaveMissedIt(t *testing.T) {
	// A red-first control: the SAME fixture, queried the OLD banned way
	// (kinds + authors), against the SAME authors-hostile relay, must return
	// nothing — proving underReturnAuthors is actually wired into this
	// fixture's match() and that DiscoverLiveBoards' win in the test above
	// isn't a fixture no-op.
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	e, err := BuildBoardEvent(owner, BoardSpec{BoardD: "live1", Title: "live1"}, time.Now().Unix()-100)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	relay := newStoreRelay(t)
	relay.underReturnAuthors = true
	relay.putRaw(e)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := nostr.FetchMany(ctx, relay.url, map[string]any{
		"kinds":   []int{KindBoard},
		"authors": []string{owner.PubKeyHex()},
		"limit":   auditPageLimit,
	})
	if err != nil {
		t.Fatalf("authors-filtered fetch: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("authors-filtered fetch against an authors-hostile relay returned %d events, want 0 — the control fixture is not modelling the defect", len(got))
	}
}

func TestInventoryBoardCards_CountsPlaintextAndSealedAndMeasuresWireSize(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	boardD := "invboard"
	boardCoord := BoardCoord(owner.PubKeyHex(), boardD)
	base := time.Now().Unix() - 10000

	be, err := BuildBoardEvent(owner, BoardSpec{BoardD: boardD, Title: boardD}, base)
	if err != nil {
		t.Fatalf("board event: %v", err)
	}

	var plain []*nostr.Event
	for i := 0; i < 3; i++ {
		ce, err := BuildCardEvent(owner, CardSpec{
			ItemID: fmt.Sprintf("plain-%d", i), Title: "readable title", Status: state.StatusActive,
			Priority: "p2", Type: "task", BoardD: boardD,
		}, base+int64(i)+1)
		if err != nil {
			t.Fatalf("plain card %d: %v", i, err)
		}
		plain = append(plain, ce)
	}

	env := testEnvelope(1, false)
	var sealed []*nostr.Event
	for i := 0; i < 2; i++ {
		ce, err := BuildCardEvent(owner, CardSpec{
			ItemID: fmt.Sprintf("sealed-%d", i), Title: "secret title", Status: state.StatusActive,
			Priority: "p2", Type: "task", BoardD: boardD, Enc: env,
		}, base+int64(i)+100)
		if err != nil {
			t.Fatalf("sealed card %d: %v", i, err)
		}
		sealed = append(sealed, ce)
	}

	// A stale superseded version of one plaintext card: latest-wins must pick
	// the newer event, not double-count the coordinate.
	staleDup, err := BuildCardEvent(owner, CardSpec{
		ItemID: "plain-0", Title: "OLD readable title", Status: state.StatusActive,
		Priority: "p2", Type: "task", BoardD: boardD,
	}, base) // older than plain[0]
	if err != nil {
		t.Fatalf("stale dup: %v", err)
	}

	// BOTH SERVE ORDERS, and the answer must be identical.
	//
	// Newest-first is the relay convention, and under it a first-wins dedup and a
	// latest-wins dedup are INDISTINGUISHABLE — the newer event arrives first either
	// way. That is not a property of the client, it is a favour from the server, and
	// this inventory reads an untrusted public relay. Oldest-first is what separates
	// "we dedup by created_at" from "we take whatever came first".
	for _, oldestFirst := range []bool{false, true} {
		t.Run(fmt.Sprintf("oldestFirst=%v", oldestFirst), func(t *testing.T) {
			runInventoryDedupCase(t, oldestFirst, be, plain, sealed, staleDup, boardD, boardCoord)
		})
	}
}

// runInventoryDedupCase is the body of the counting/dedup assertion, run once per
// relay serve order.
func runInventoryDedupCase(t *testing.T, oldestFirst bool, be *nostr.Event, plain, sealed []*nostr.Event, staleDup *nostr.Event, boardD, boardCoord string) {
	t.Helper()
	relay := newStoreRelay(t)
	relay.serveOldestFirst = oldestFirst
	relay.putRaw(be)
	for _, e := range plain {
		relay.putRaw(e)
	}
	for _, e := range sealed {
		relay.putRaw(e)
	}
	// BOTH versions of plain-0's coordinate are on the wire at once (putDup, not
	// putRaw — putRaw would replace one with the other and leave nothing to
	// choose between). The inventory must return the NEWER of the two and count
	// the coordinate ONCE.
	relay.putDup(staleDup)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, totals, err := InventoryBoardCards(ctx, relay.url, boardD, boardCoord)
	if err != nil {
		t.Fatalf("InventoryBoardCards: %v", err)
	}

	if totals.Cards != 5 || totals.Plaintext != 3 || totals.Sealed != 2 {
		t.Fatalf("totals = %+v, want cards=5 plaintext=3 sealed=2", totals)
	}
	if len(rows) != 5 {
		t.Fatalf("got %d rows, want 5", len(rows))
	}

	byItem := map[string]CardCoordRow{}
	for _, r := range rows {
		byItem[r.ItemID] = r
	}
	got0, ok := byItem["plain-0"]
	if !ok {
		t.Fatalf("plain-0 missing from rows: %+v", rows)
	}
	if got0.Sealed {
		t.Fatalf("plain-0 reported Sealed=true")
	}
	if got0.EventID != plain[0].ID {
		t.Fatalf("plain-0 EventID = %s, want the NEWER event %s (latest-wins over the stale duplicate %s)", got0.EventID, plain[0].ID, staleDup.ID)
	}
	if got0.CreatedAt != plain[0].CreatedAt {
		t.Fatalf("plain-0 CreatedAt = %d, want %d", got0.CreatedAt, plain[0].CreatedAt)
	}
	for i := 0; i < 2; i++ {
		row, ok := byItem[fmt.Sprintf("sealed-%d", i)]
		if !ok {
			t.Fatalf("sealed-%d missing from rows", i)
		}
		if !row.Sealed {
			t.Fatalf("sealed-%d reported Sealed=false", i)
		}
		if row.Kind != KindCard {
			t.Fatalf("sealed-%d Kind = %d, want %d", i, row.Kind, KindCard)
		}
		wantN, werr := marshaledEventSize(sealed[i])
		if werr != nil {
			t.Fatalf("marshaledEventSize: %v", werr)
		}
		if row.WireBytes != wantN {
			t.Fatalf("sealed-%d WireBytes = %d, want %d (exact json.Marshal size of the winning event)", i, row.WireBytes, wantN)
		}
		if row.BoardCoord != boardCoord || row.Board != boardD {
			t.Fatalf("sealed-%d Board/BoardCoord = %q/%q, want %q/%q", i, row.Board, row.BoardCoord, boardD, boardCoord)
		}
	}
}

func TestInventoryBoardCards_ScopedToOneBoardEvenWithAnotherBoardOnTheSameRelay(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	base := time.Now().Unix() - 10000

	aD, bD := "boarda", "boardb"
	aCoord := BoardCoord(owner.PubKeyHex(), aD)
	bCoord := BoardCoord(owner.PubKeyHex(), bD)

	aCard, err := BuildCardEvent(owner, CardSpec{ItemID: "a-1", Title: "t", Status: state.StatusActive, Priority: "p2", Type: "task", BoardD: aD}, base+1)
	if err != nil {
		t.Fatalf("a card: %v", err)
	}
	bCard, err := BuildCardEvent(owner, CardSpec{ItemID: "b-1", Title: "t", Status: state.StatusActive, Priority: "p2", Type: "task", BoardD: bD}, base+1)
	if err != nil {
		t.Fatalf("b card: %v", err)
	}

	// Once against a relay that honours "#a", once against one that ignores it and
	// over-returns. A filter is a REQUEST to an untrusted server, not a guarantee:
	// only the second case can distinguish the inventory's own scope check from the
	// relay having done the work for it.
	for _, honoursFilter := range []bool{true, false} {
		t.Run(fmt.Sprintf("relayHonoursTagFilter=%v", honoursFilter), func(t *testing.T) {
			relay := newStoreRelay(t)
			relay.ignoreTagFilters = !honoursFilter
			relay.putRaw(aCard)
			relay.putRaw(bCard)

			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			rows, totals, err := InventoryBoardCards(ctx, relay.url, aD, aCoord)
			if err != nil {
				t.Fatalf("InventoryBoardCards: %v", err)
			}
			if totals.Cards != 1 || len(rows) != 1 || rows[0].ItemID != "a-1" {
				t.Fatalf("board a inventory = totals %+v rows %+v, want exactly a-1 (board b's card must not leak in, whatever the relay chooses to return)", totals, rows)
			}
			if rows[0].BoardCoord != aCoord || rows[0].BoardCoord == bCoord {
				t.Fatalf("row bound to %q, want board a's coordinate %q", rows[0].BoardCoord, aCoord)
			}
		})
	}
}
