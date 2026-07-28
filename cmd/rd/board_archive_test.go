package main

// ready-a9b: `rd board archive` / `rd board unarchive` — the CLI-command half.
// See pkg/sync/boardarchive_test.go for the wire-format half (the tag itself,
// IsBoardArchived, BoardSpecFromEvent, WinningBoardEvent).
//
// These are deterministic tests: boardTestEnv configures NO relay (see its own
// doc), so runBoardArchiveToggle's relay-read loop is a no-op and every
// assertion here is against the LOCAL authoritative log — the durability
// guarantee board-fold-spec.md §16.1 requires independent of any relay. The
// live-relay read-back proof (wss://relay.3dl.network) is this item's audit
// trail, not a unit test — a unit test cannot depend on a real network relay
// being reachable in CI.
//
// Done conditions this file proves:
//   - archiving toggles ONLY the archived marker; title and BOTH maintainers
//     of a multi-maintainer board survive the republish unchanged
//     (TestBoardArchiveCmd_RoundTrip).
//   - unarchive is the exact inverse (same test, second half).
//   - only the board's OWNER may archive it — a coordinate naming a different
//     pubkey is refused and appends NOTHING to the log
//     (TestBoardArchiveCmd_RefusesForeignOwnerCoordinate).
//   - archiving a boardD with no existing kind-30301 definition is refused,
//     not silently invented (TestBoardArchiveCmd_NoExistingBoard_Errors).
//   - re-archiving an already-archived board is a NOTE, not an error, and
//     still republishes (idempotent-by-outcome, not a no-op)
//     (TestBoardArchiveCmd_AlreadyArchived_StillRepublishes).

import (
	"bytes"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// seedBoard appends a kind-30301 event for boardD, signed by owner, with the
// given title/maintainers, directly to dir's local log — bypassing the CLI so
// the test sets up "a board that already exists" without depending on the
// very command under test.
func seedBoard(t *testing.T, dir string, owner *nostr.Key, boardD, title string, maintainers []string, createdAt int64) *nostr.Event {
	t.Helper()
	e, err := rdSync.BuildBoardEvent(owner, rdSync.BoardSpec{BoardD: boardD, Title: title, Maintainers: maintainers}, createdAt)
	if err != nil {
		t.Fatalf("seedBoard %s: BuildBoardEvent: %v", boardD, err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{e}); err != nil {
		t.Fatalf("seedBoard %s: AppendUnique: %v", boardD, err)
	}
	return e
}

// winningBoardInLog reads dir's local log and returns the winning kind-30301
// event for coord — the same read-modify-write view runBoardArchiveToggle
// itself resolves from, used here purely to VERIFY the outcome.
func winningBoardInLog(t *testing.T, dir, coord string) *nostr.Event {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	win, ok := rdSync.WinningBoardEvent(events, coord)
	if !ok {
		t.Fatalf("no winning board event for %s among %d log events", coord, len(events))
	}
	return win
}

func TestBoardArchiveCmd_RoundTrip(t *testing.T) {
	owner, _, _, dir := boardTestEnv(t)
	self := owner.PubKeyHex()
	other := testCLIKey(t).PubKeyHex()

	const boardD = "archive-target"
	seedBoard(t, dir, owner, boardD, "Archive Target", []string{self, other}, 1700000000)
	coord := rdSync.BoardCoord(self, boardD)

	// --- archive -----------------------------------------------------------
	var errBuf bytes.Buffer
	boardArchiveCmd.SetErr(&errBuf)
	t.Cleanup(func() { boardArchiveCmd.SetErr(nil) })
	if err := boardArchiveCmd.RunE(boardArchiveCmd, []string{boardD}); err != nil {
		t.Fatalf("rd board archive %s: %v (stderr=%s)", boardD, err, errBuf.String())
	}

	win := winningBoardInLog(t, dir, coord)
	if !rdSync.IsBoardArchived(win) {
		t.Fatalf("winning event after archive is not marked archived: tags=%v", win.Tags)
	}
	assertBoardSpecUnchanged(t, win, "Archive Target", []string{self, other})

	// --- unarchive: the exact inverse ---------------------------------------
	errBuf.Reset()
	boardUnarchiveCmd.SetErr(&errBuf)
	t.Cleanup(func() { boardUnarchiveCmd.SetErr(nil) })
	if err := boardUnarchiveCmd.RunE(boardUnarchiveCmd, []string{boardD}); err != nil {
		t.Fatalf("rd board unarchive %s: %v (stderr=%s)", boardD, err, errBuf.String())
	}

	win2 := winningBoardInLog(t, dir, coord)
	if rdSync.IsBoardArchived(win2) {
		t.Fatalf("winning event after unarchive is still marked archived: tags=%v", win2.Tags)
	}
	assertBoardSpecUnchanged(t, win2, "Archive Target", []string{self, other})

	// The unarchive event must be a LATER, DISTINCT event — not a no-op that
	// left the archived one standing.
	if win2.ID == win.ID {
		t.Fatal("unarchive produced the same event id as archive — no new event was published")
	}
	if win2.CreatedAt <= win.CreatedAt {
		t.Fatalf("unarchive created_at %d is not after archive's %d", win2.CreatedAt, win.CreatedAt)
	}
}

func assertBoardSpecUnchanged(t *testing.T, e *nostr.Event, wantTitle string, wantMaintainers []string) {
	t.Helper()
	spec := rdSync.BoardSpecFromEvent(e)
	if spec.Title != wantTitle {
		t.Errorf("title = %q, want %q", spec.Title, wantTitle)
	}
	if len(spec.Maintainers) != len(wantMaintainers) {
		t.Fatalf("maintainers = %v, want %v", spec.Maintainers, wantMaintainers)
	}
	for i, m := range wantMaintainers {
		if spec.Maintainers[i] != m {
			t.Errorf("maintainer[%d] = %q, want %q", i, spec.Maintainers[i], m)
		}
	}
}

// TestBoardArchiveCmd_RefusesForeignOwnerCoordinate proves board-fold-spec.md
// §16.6 ("the board event is written only by its owner") holds for archiving:
// naming a coordinate whose owner is NOT this signing key must be refused,
// and — the part a mere error-string assertion would miss — must append
// NOTHING to the local log for that foreign coordinate.
func TestBoardArchiveCmd_RefusesForeignOwnerCoordinate(t *testing.T) {
	_, _, _, dir := boardTestEnv(t)
	strangerOwner := testCLIKey(t)
	foreignCoord := rdSync.BoardCoord(strangerOwner.PubKeyHex(), "not-mine")

	before, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll before: %v", err)
	}

	var errBuf bytes.Buffer
	boardArchiveCmd.SetErr(&errBuf)
	t.Cleanup(func() { boardArchiveCmd.SetErr(nil) })
	err = boardArchiveCmd.RunE(boardArchiveCmd, []string{foreignCoord})
	if err == nil {
		t.Fatal("archiving a foreign-owner coordinate succeeded, want a refusal")
	}

	after, err2 := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err2 != nil {
		t.Fatalf("ReadAll after: %v", err2)
	}
	if len(after) != len(before) {
		t.Fatalf("refused archive still appended to the log: before=%d after=%d", len(before), len(after))
	}
}

// TestBoardArchiveCmd_NoExistingBoard_Errors proves archiving never INVENTS a
// board definition: a boardD with no prior kind-30301 event anywhere (local
// log or — in this deterministic env — the zero configured relays) must
// refuse rather than publish a bare "archived, no title" event.
func TestBoardArchiveCmd_NoExistingBoard_Errors(t *testing.T) {
	_, _, _, _ = boardTestEnv(t)

	var errBuf bytes.Buffer
	boardArchiveCmd.SetErr(&errBuf)
	t.Cleanup(func() { boardArchiveCmd.SetErr(nil) })
	err := boardArchiveCmd.RunE(boardArchiveCmd, []string{"never-published"})
	if err == nil {
		t.Fatal("archiving a boardD with no existing definition succeeded, want a refusal")
	}
}

// TestBoardArchiveCmd_AlreadyArchived_StillRepublishes proves a second
// `rd board archive` on an already-archived board is NOT an error (the owner
// re-running the command must not be punished for it) and NOT a no-op either
// — it republishes a fresh event, which is what lets every relay converge
// even if an earlier publish only reached some of them.
func TestBoardArchiveCmd_AlreadyArchived_StillRepublishes(t *testing.T) {
	owner, _, _, dir := boardTestEnv(t)
	const boardD = "double-archive"
	seedBoard(t, dir, owner, boardD, "Double Archive", []string{owner.PubKeyHex()}, 1700000000)
	coord := rdSync.BoardCoord(owner.PubKeyHex(), boardD)

	for i := 0; i < 2; i++ {
		var errBuf bytes.Buffer
		boardArchiveCmd.SetErr(&errBuf)
		if err := boardArchiveCmd.RunE(boardArchiveCmd, []string{boardD}); err != nil {
			t.Fatalf("archive call %d: %v (stderr=%s)", i+1, err, errBuf.String())
		}
		boardArchiveCmd.SetErr(nil)
		if i == 1 && !bytes.Contains(errBuf.Bytes(), []byte("already archived")) {
			t.Errorf("second archive call (already archived) did not note it: stderr=%q", errBuf.String())
		}
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	distinctBoardEvents := 0
	for _, e := range events {
		if e.Kind == rdSync.KindBoard {
			distinctBoardEvents++
		}
	}
	// seed + archive#1 + archive#2 = 3 distinct kind-30301 events in the log.
	if distinctBoardEvents != 3 {
		t.Fatalf("distinct kind-30301 events in log = %d, want 3 (seed + two archive republishes)", distinctBoardEvents)
	}
	win := winningBoardInLog(t, dir, coord)
	if !rdSync.IsBoardArchived(win) {
		t.Fatal("winning event after double-archive is not archived")
	}
}

// testCLIKey generates a fresh throwaway signing key for use as "some other
// pubkey" in a test — never persisted, never the actor identity.
func testCLIKey(t *testing.T) *nostr.Key {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k
}
