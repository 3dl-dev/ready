// Live-relay proof for ready-7ec: rd nostr sync must be BOARD-scoped when a
// board is pinned, not author-scoped-over-the-whole-portfolio, and the fix must
// not silently drop status events (the escalation this item resolved).
package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/state"
)

// TestLiveRelay_BoardScopedStatusConvergence is the ready-7ec ground-source
// proof, run against a LIVE self-hosted strfry relay:
//
//  1. Machine A creates item X on board BD, then closes it with a reason
//     (a status event) — the exact write path publishItemStatusChangeNostr uses
//     (Publisher.PublishItem + Publisher.PublishStatusChange).
//  2. Machine A ALSO creates an UNRELATED item Y on a DIFFERENT board BD-other.
//  3. Machine B starts with an EMPTY log and negentropy-syncs using
//     BoardSyncFilter(boardCoord-of-BD, nil) — the exact filter `rd nostr sync`
//     now builds when a board is pinned (cmd/rd/nostr.go nostrSyncCmd).
//  4. Assert: B's log contains X's CARD *and* its STATUS event (the close, with
//     its reason) — before ready-7ec, the board-scoped filter matched only the
//     card, because status events carried no board coordinate. Assert Y (a
//     different board) is ABSENT — the filter is scoped, not portfolio-wide.
//  5. Machine C runs the identical board-scoped sync independently and converges
//     on the IDENTICAL projected item for X (same status, same history/reason) —
//     two machines agreeing on the board-scoped readiness set, not the whole
//     portfolio.
//
// Gated behind RD_NOSTR_LIVE_RELAY=1 so `go test ./...` stays green with no
// relay reachable.
func TestLiveRelay_BoardScopedStatusConvergence(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live board-scope proof")
	}
	relay := liveRelayURL(t)
	t.Logf("live relay: %s", relay)

	k := liveRelayKey(t)
	run := time.Now().UnixNano()
	boardD := fmt.Sprintf("ready-7ec-live-%d", run)
	boardCoord := BoardCoord(k.PubKeyHex(), boardD)
	otherBoardD := boardD + "-other"
	itemX := fmt.Sprintf("ready-7ec-live-%d-x", run)
	itemY := fmt.Sprintf("ready-7ec-live-%d-y", run)

	dir := t.TempDir()
	pubA := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, "A", NostrLogFile)),
		WriteRelays: []string{relay},
		PendingPath: filepath.Join(dir, "A", NostrPendingFile),
	}
	board := BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{k.PubKeyHex()}}
	otherBoard := BoardSpec{BoardD: otherBoardD, Title: otherBoardD, Maintainers: []string{k.PubKeyHex()}}

	ctx := context.Background()
	now := time.Now().Unix()

	// 1. Create X on the pinned board, then close it with a reason (status event).
	cardX := CardSpec{ItemID: itemX, Title: "x", Status: state.StatusActive, Type: "task", BoardD: boardD}
	resCreate, err := pubA.PublishItem(ctx, &board, cardX, now)
	if err != nil {
		t.Fatalf("A create X: %v", err)
	}
	for _, ev := range resCreate.Events {
		if !ev.AnyRelay {
			t.Fatalf("create event kind %d not accepted by relay: acks=%+v", ev.Kind, ev.Acks)
		}
	}
	cardX.Status = state.StatusDone
	resClose, err := pubA.PublishStatusChange(ctx, cardX, "shipped via ready-7ec proof", now+5)
	if err != nil {
		t.Fatalf("A close X: %v", err)
	}
	for _, ev := range resClose.Events {
		if !ev.AnyRelay {
			t.Fatalf("close event kind %d not accepted by relay: acks=%+v", ev.Kind, ev.Acks)
		}
	}

	// 2. Unrelated item Y on a DIFFERENT board — must NOT leak into a BD-scoped sync.
	cardY := CardSpec{ItemID: itemY, Title: "y", Status: state.StatusActive, Type: "task", BoardD: otherBoardD}
	resY, err := pubA.PublishItem(ctx, &otherBoard, cardY, now+1)
	if err != nil {
		t.Fatalf("A create Y (other board): %v", err)
	}
	for _, ev := range resY.Events {
		if !ev.AnyRelay {
			t.Fatalf("Y event kind %d not accepted by relay: acks=%+v", ev.Kind, ev.Acks)
		}
	}

	time.Sleep(1 * time.Second)

	trusted := map[string]bool{k.PubKeyHex(): true}
	filter := BoardSyncFilter(boardCoord, nil)

	// 3+4. Machine B: empty log, board-scoped negentropy sync.
	logB := NewNostrLog(filepath.Join(dir, "B", NostrLogFile))
	rB, err := NegentropySync(ctx, relay, logB, filter, trusted, 30*time.Second)
	if err != nil {
		t.Fatalf("B board-scoped sync: %v", err)
	}
	t.Logf("B sync: need=%d downloaded=%d", rB.Need, rB.Downloaded)

	eventsB, err := logB.ReadAll()
	if err != nil {
		t.Fatalf("B read log: %v", err)
	}
	itemsB := ProjectItems(eventsB, ProjectOptions{Trusted: trusted, PinnedBoard: boardCoord})

	gotX, ok := itemsB[itemX]
	if !ok {
		t.Fatalf("BUG: board-scoped sync did not pull item X's card at all")
	}
	if gotX.Status != state.StatusDone {
		t.Errorf("BUG (the ready-7ec regression): item X's status is %q, want %q — the board-scoped sync missed the CLOSE status event (it carried no board coordinate pre-fix)", gotX.Status, state.StatusDone)
	}
	foundReason := false
	for _, h := range gotX.History {
		if h.Note == "shipped via ready-7ec proof" {
			foundReason = true
		}
	}
	if !foundReason {
		t.Errorf("BUG: item X's close-reason history entry is missing — the board-scoped filter dropped the status event that carries it")
	}
	if _, leaked := itemsB[itemY]; leaked {
		t.Errorf("BUG: board-scoped sync leaked item Y from a DIFFERENT board coordinate (%s) — the filter is not actually scoped", otherBoardD)
	}

	// 5. Machine C: independent empty log, identical board-scoped sync -> converges.
	logC := NewNostrLog(filepath.Join(dir, "C", NostrLogFile))
	rC, err := NegentropySync(ctx, relay, logC, filter, trusted, 30*time.Second)
	if err != nil {
		t.Fatalf("C board-scoped sync: %v", err)
	}
	t.Logf("C sync: need=%d downloaded=%d", rC.Need, rC.Downloaded)

	eventsC, err := logC.ReadAll()
	if err != nil {
		t.Fatalf("C read log: %v", err)
	}
	itemsC := ProjectItems(eventsC, ProjectOptions{Trusted: trusted, PinnedBoard: boardCoord})
	gotXc, ok := itemsC[itemX]
	if !ok {
		t.Fatalf("C: item X missing after board-scoped sync")
	}
	if gotXc.Status != gotX.Status || len(gotXc.History) != len(gotX.History) {
		t.Errorf("two-machine divergence on the BOARD-scoped set: B status=%q history=%d, C status=%q history=%d",
			gotX.Status, len(gotX.History), gotXc.Status, len(gotXc.History))
	}
	if _, leaked := itemsC[itemY]; leaked {
		t.Errorf("BUG: machine C's board-scoped sync also leaked item Y from a different board")
	}

	t.Logf("PROVEN: board-scoped negentropy filter (BoardSyncFilter(boardCoord, nil)) matched item X's card AND its close-status event across two independent machines (B, C), and excluded an unrelated different-board item Y")
}
