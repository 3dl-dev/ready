package sync

// ready-fce REWORK — the write-path guard.
//
// The original guard (live_relay_key_test.go's requireIsolatedBoardD) was only a
// convention helper: a live-relay test had to remember to call it, and a new test
// (or any other caller) that skipped it published straight to the reserved
// production board coordinate with no abort. A veracity adversary proved this by
// adding a probe that called Publisher.PublishItemWithReason directly with
// BoardD: "ready" and signed 4 real events (kinds 30301, 30302, 1630, 1621)
// carrying the production coordinate — no guard fired.
//
// The tests below exercise the REAL write path (Publisher.PublishItemWithReason /
// PublishStatusChange / PublishEvents / PublishEventsUnique / PublishBoard) the
// same way that adversary probe did — not a bare call to a string-compare helper
// — and assert on the return value AND the resulting log contents. No network is
// used (WriteRelays is nil/unset throughout), so these run unconditionally in
// `go test ./...`, not gated behind RD_NOSTR_LIVE_RELAY. The guard fires before
// any relay dial or log append regardless of relay reachability.
import (
	"context"
	"path/filepath"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

func newGuardTestPublisher(t *testing.T) (*Publisher, *NostrLog, *nostr.Key) {
	t.Helper()
	k := testKey(t)
	log := NewNostrLog(filepath.Join(t.TempDir(), "nostr-log.jsonl"))
	return &Publisher{Key: k, Log: log}, log, k // Production left false (zero value)
}

// TestGuard_PublishItemWithReason_RefusesReservedBoard reproduces the adversary's
// exact bypass: PublishItemWithReason called DIRECTLY (no helper in the way) with
// BoardD equal to the reserved production coordinate, on a Publisher NOT marked
// Production. The write must be refused, and — because the guard runs before
// Log.Append — the local log must end up with ZERO events: the batch (board,
// card, issue, status — the same 4 kinds the adversary's probe signed) never
// becomes durable at all, not even locally.
func TestGuard_PublishItemWithReason_RefusesReservedBoard(t *testing.T) {
	pub, log, k := newGuardTestPublisher(t)
	board := &BoardSpec{BoardD: reservedProductionBoardD, Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{
		ItemID: "ready-266-probe-repro", Title: "adversary repro", Status: state.StatusActive,
		Priority: "p3", Type: "task", BoardD: reservedProductionBoardD,
	}
	_, err := pub.PublishItemWithReason(context.Background(), board, card, "", 1_700_000_000)
	if err == nil {
		t.Fatal("PublishItemWithReason must refuse the reserved production board coordinate when Production is false")
	}
	events, rerr := log.ReadAll()
	if rerr != nil {
		t.Fatalf("read log: %v", rerr)
	}
	if len(events) != 0 {
		t.Fatalf("guard fired too late: %d event(s) reached the local log (board+card+issue+status must never become durable): %+v", len(events), events)
	}
}

// TestGuard_PublishStatusChange_RefusesReservedBoard covers the second entry
// point the item calls out explicitly (PublishStatusChange), independent of
// PublishItemWithReason.
func TestGuard_PublishStatusChange_RefusesReservedBoard(t *testing.T) {
	pub, log, _ := newGuardTestPublisher(t)
	card := CardSpec{
		ItemID: "ready-status-repro", Title: "status repro", Status: state.StatusDone,
		Priority: "p2", Type: "task", BoardD: reservedProductionBoardD,
	}
	_, err := pub.PublishStatusChange(context.Background(), card, "closing", 1_700_000_000)
	if err == nil {
		t.Fatal("PublishStatusChange must refuse the reserved production board coordinate when Production is false")
	}
	if events, _ := log.ReadAll(); len(events) != 0 {
		t.Fatalf("guard fired too late: %d event(s) reached the local log", len(events))
	}
}

// TestGuard_PublishEvents_RefusesReservedBoard proves the guard is not
// spec-shape-specific: a caller that builds its own event via BuildCardEvent
// (exactly how scripts/relay-policy/probe/main.go used to, and exactly how any
// future caller might) and hands it to the low-level PublishEvents is refused
// too — the check runs on the ACTUAL built/signed event's tags, not on a
// CardSpec/BoardSpec field a caller could spell differently.
func TestGuard_PublishEvents_RefusesReservedBoard(t *testing.T) {
	pub, log, k := newGuardTestPublisher(t)
	card := CardSpec{ItemID: "ready-raw-repro", Title: "raw event", Status: state.StatusActive, BoardD: reservedProductionBoardD}
	ev, err := BuildCardEvent(k, card, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); err == nil {
		t.Fatal("PublishEvents must refuse an event addressing the reserved production board coordinate when Production is false")
	}
	if events, _ := log.ReadAll(); len(events) != 0 {
		t.Fatalf("guard fired too late: %d event(s) reached the local log", len(events))
	}
}

// TestGuard_PublishEventsUnique_RefusesReservedBoard covers the migration entry
// point (ready-d65), which bypasses publishEvents and calls Log.AppendUnique
// directly.
func TestGuard_PublishEventsUnique_RefusesReservedBoard(t *testing.T) {
	pub, log, k := newGuardTestPublisher(t)
	card := CardSpec{ItemID: "ready-unique-repro", Title: "unique event", Status: state.StatusActive, BoardD: reservedProductionBoardD}
	ev, err := BuildCardEvent(k, card, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	if _, _, err := pub.PublishEventsUnique(context.Background(), []*nostr.Event{ev}); err == nil {
		t.Fatal("PublishEventsUnique must refuse an event addressing the reserved production board coordinate when Production is false")
	}
	if events, _ := log.ReadAll(); len(events) != 0 {
		t.Fatalf("guard fired too late: %d event(s) reached the local log", len(events))
	}
}

// TestGuard_PublishBoard_RefusesReservedBoard covers the board-level republish
// path (ready-866/ready-615): even when a reserved-coordinate event is ALREADY
// durable in the local log (simulated here by appending directly, bypassing the
// guarded entry points, the way a stale/pre-guard log might), PublishBoard must
// still refuse to relay-publish it when the Publisher isn't marked Production.
func TestGuard_PublishBoard_RefusesReservedBoard(t *testing.T) {
	pub, log, k := newGuardTestPublisher(t)
	board := BoardSpec{BoardD: reservedProductionBoardD, Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	be, err := BuildBoardEvent(k, board, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	// Bypass the guard to seed the log directly — proving PublishBoard's OWN
	// check (on the scoped republish set), not just publishEvents'.
	if err := log.Append(be); err != nil {
		t.Fatalf("seed log: %v", err)
	}
	coord := BoardCoord(k.PubKeyHex(), reservedProductionBoardD)
	if _, err := pub.PublishBoard(context.Background(), coord); err == nil {
		t.Fatal("PublishBoard must refuse to republish the reserved production board coordinate when Production is false")
	}
}

// TestGuard_Production_AllowsReservedBoard is the control the CONSTRAINT
// requires: the real rd CLI's own writes to the reserved board must obviously
// keep working. A Publisher explicitly marked Production=true (exactly what
// nostrPublisher() / follow.go's Publisher{} literals set) succeeds at the SAME
// write TestGuard_PublishItemWithReason_RefusesReservedBoard proves fails.
func TestGuard_Production_AllowsReservedBoard(t *testing.T) {
	k := testKey(t)
	log := NewNostrLog(filepath.Join(t.TempDir(), "nostr-log.jsonl"))
	pub := &Publisher{Key: k, Log: log, Production: true}
	board := &BoardSpec{BoardD: reservedProductionBoardD, Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{
		ItemID: "ready-prod-ok", Title: "real rd write", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: reservedProductionBoardD,
	}
	if _, err := pub.PublishItemWithReason(context.Background(), board, card, "", 1_700_000_000); err != nil {
		t.Fatalf("Production=true Publisher must be able to write the reserved production board coordinate: %v", err)
	}
	events, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("Production=true write produced no durable events")
	}
}

// TestGuard_AllowsIsolatedBoard is the OTHER control: a Publisher NOT marked
// Production, writing to an ISOLATED (non-reserved) board D-tag, must succeed —
// proving the guard is scoped to the one reserved coordinate, not a blanket
// refusal that would make every ordinary test author mark Production=true out of
// habit (which would defeat the fail-closed default).
func TestGuard_AllowsIsolatedBoard(t *testing.T) {
	pub, log, k := newGuardTestPublisher(t)
	board := &BoardSpec{BoardD: "some-other-board", Title: "other", Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{
		ItemID: "ready-iso-ok", Title: "isolated write", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: "some-other-board",
	}
	if _, err := pub.PublishItemWithReason(context.Background(), board, card, "", 1_700_000_000); err != nil {
		t.Fatalf("write to an isolated board D-tag must succeed: %v", err)
	}
	if events, _ := log.ReadAll(); len(events) == 0 {
		t.Fatal("isolated write produced no durable events")
	}
}

// TestGuard_HitsReservedBoard_StatusEventBoardTag proves the guard catches the
// board-membership "a" tag BuildStatusEventWithIssueRoot adds to a status event
// (ready-7ec) — the SECOND "a" tag, not just the card's own coordinate — so a
// caller that only publishes a bare status event (no card, no board event) for
// the reserved board is still caught.
func TestGuard_HitsReservedBoard_StatusEventBoardTag(t *testing.T) {
	k := testKey(t)
	boardCoord := BoardCoord(k.PubKeyHex(), reservedProductionBoardD)
	se, err := BuildStatusEventWithIssueRoot(k, "ready-status-only", state.StatusDone, "", "", boardCoord, "", 1_700_000_000, nil)
	if err != nil {
		t.Fatalf("BuildStatusEventWithIssueRoot: %v", err)
	}
	if !hitsReservedBoard(se) {
		t.Fatalf("hitsReservedBoard did not catch a bare status event's board-membership \"a\" tag: %+v", se.Tags)
	}
}
