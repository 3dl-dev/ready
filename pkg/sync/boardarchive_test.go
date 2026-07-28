package sync

// ready-a9b: `rd board archive` / `rd board unarchive` — the wire-format half.
// See cmd/rd/board_archive_test.go for the CLI-command half (ownership guard,
// read-modify-write against the local log, refusal on a missing board).
//
// The done conditions this file proves:
//  1. BuildBoardEvent's "archived" tag is additive and toggled purely by
//     BoardSpec.Archived — nothing else about the event shape changes.
//  2. IsBoardArchived reads it back correctly (nil-safe, absent-tag-safe).
//  3. BoardSpecFromEvent is the exact inverse of BuildBoardEvent for the
//     fields a board mutation must carry forward unchanged (title,
//     maintainers) — the read-modify-write primitive `rd board archive` uses
//     so archiving a board never drops its title or demotes its maintainers.
//  4. WinningBoardEvent picks the correct definition under §4.5's tie-break
//     (greatest created_at, then lowest event id) and NEVER lets a forged or
//     wrong-coordinate event win — this is what makes the archived marker
//     trustworthy against a relay that serves stale or hostile events.

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

func TestBuildBoardEvent_ArchivedTag(t *testing.T) {
	k := testKey(t)

	notArchived, err := BuildBoardEvent(k, BoardSpec{BoardD: "proj", Title: "Proj", Maintainers: []string{k.PubKeyHex()}}, 1700000000)
	if err != nil {
		t.Fatalf("build (not archived): %v", err)
	}
	if _, ok := findTag(notArchived.Tags, "archived"); ok {
		t.Fatalf("BoardSpec.Archived=false emitted an archived tag: %v", notArchived.Tags)
	}
	if IsBoardArchived(notArchived) {
		t.Fatalf("IsBoardArchived true for a plain board event")
	}

	archived, err := BuildBoardEvent(k, BoardSpec{BoardD: "proj", Title: "Proj", Maintainers: []string{k.PubKeyHex()}, Archived: true}, 1700000001)
	if err != nil {
		t.Fatalf("build (archived): %v", err)
	}
	v, ok := findTag(archived.Tags, "archived")
	if !ok || v != ArchivedTagValue {
		t.Fatalf("archived tag = %q, ok=%v, want %q", v, ok, ArchivedTagValue)
	}
	if !IsBoardArchived(archived) {
		t.Fatalf("IsBoardArchived false for an event carrying the archived tag")
	}

	// Every OTHER tag is unaffected: d, title and p are still exactly what a
	// plain board carries. Archiving must be additive, never a replacement of
	// the board's existing tag set.
	for _, name := range []string{"d", "title", "p"} {
		plain, _ := findTag(notArchived.Tags, name)
		arch, _ := findTag(archived.Tags, name)
		if plain != arch {
			t.Errorf("tag %q differs between archived/plain: plain=%q archived=%q", name, plain, arch)
		}
	}
}

func TestIsBoardArchived_NilAndAbsentSafe(t *testing.T) {
	if IsBoardArchived(nil) {
		t.Fatal("IsBoardArchived(nil) = true, want false")
	}
	k := testKey(t)
	e, err := BuildBoardEvent(k, BoardSpec{BoardD: "x", Title: "x"}, 1700000000)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if IsBoardArchived(e) {
		t.Fatal("a board event with no archived tag reports archived")
	}
}

// TestBoardSpecFromEvent_RoundTrip proves BoardSpecFromEvent is the exact
// inverse BuildBoardEvent needs for a read-modify-write mutation: build a
// spec with a title and TWO maintainers, sign it, reconstruct the spec from
// the signed event, and require every field to survive — including BOTH
// maintainers (a mutation that dropped a second maintainer would silently
// demote them, exactly the failure mode board-fold-spec.md §16.6 warns a
// board republish can cause).
func TestBoardSpecFromEvent_RoundTrip(t *testing.T) {
	k := testKey(t)
	other := testKey(t).PubKeyHex()
	want := BoardSpec{
		BoardD:      "multi-maintainer-board",
		Title:       "Multi Maintainer Board",
		Maintainers: []string{k.PubKeyHex(), other},
		Archived:    false,
	}
	e, err := BuildBoardEvent(k, want, 1700000000)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	got := BoardSpecFromEvent(e)
	if got.BoardD != want.BoardD || got.Title != want.Title || got.Archived != want.Archived {
		t.Fatalf("BoardSpecFromEvent = %+v, want %+v", got, want)
	}
	if len(got.Maintainers) != 2 || got.Maintainers[0] != want.Maintainers[0] || got.Maintainers[1] != want.Maintainers[1] {
		t.Fatalf("BoardSpecFromEvent maintainers = %v, want %v", got.Maintainers, want.Maintainers)
	}

	// Now flip Archived and prove the round-trip still holds — this is the
	// EXACT sequence `rd board archive` runs: read the current spec, flip one
	// field, republish.
	want.Archived = true
	e2, err := BuildBoardEvent(k, BoardSpecFromEvent(e).cloneWith(true), 1700000001)
	if err != nil {
		t.Fatalf("build archived republish: %v", err)
	}
	got2 := BoardSpecFromEvent(e2)
	if !got2.Archived {
		t.Fatal("republished spec lost Archived=true")
	}
	if got2.Title != want.Title || len(got2.Maintainers) != 2 {
		t.Fatalf("republish dropped title/maintainers: %+v", got2)
	}
}

// cloneWith is a tiny test-local helper mirroring exactly what
// runBoardArchiveToggle does: spec := BoardSpecFromEvent(winner); spec.Archived = archived.
func (s BoardSpec) cloneWith(archived bool) BoardSpec {
	s.Archived = archived
	return s
}

func TestWinningBoardEvent_LatestWinsTieBreakAndVerification(t *testing.T) {
	k := testKey(t)
	coord := BoardCoord(k.PubKeyHex(), "board-x")

	older, err := BuildBoardEvent(k, BoardSpec{BoardD: "board-x", Title: "Old"}, 1700000000)
	if err != nil {
		t.Fatalf("build older: %v", err)
	}
	newer, err := BuildBoardEvent(k, BoardSpec{BoardD: "board-x", Title: "New", Archived: true}, 1700000010)
	if err != nil {
		t.Fatalf("build newer: %v", err)
	}
	// A DIFFERENT board's event must never win coord's contest.
	other, err := BuildBoardEvent(k, BoardSpec{BoardD: "board-y", Title: "Other"}, 1700000020)
	if err != nil {
		t.Fatalf("build other: %v", err)
	}

	// Order independence: the later event (by created_at) wins regardless of
	// which array position it occupies (§4.5 is a pure function of the event
	// set, never of append/merge order).
	for _, events := range [][]*nostr.Event{
		{older, newer, other},
		{newer, older, other},
		{other, older, newer},
	} {
		win, ok := WinningBoardEvent(events, coord)
		if !ok {
			t.Fatalf("WinningBoardEvent: not found among %d events", len(events))
		}
		if win.ID != newer.ID {
			t.Fatalf("winner = %q (title %s), want newer %q", win.ID, findTagOrEmpty(win, "title"), newer.ID)
		}
		if !IsBoardArchived(win) {
			t.Fatal("winner should carry the archived marker (it's the newer definition)")
		}
	}

	t.Run("same created_at ties to the lowest event id", func(t *testing.T) {
		// Two independently-signed events for the same coordinate at the SAME
		// second: whichever has the lexicographically lower id must win,
		// matching newerThan/NIP-33 and independent of array order.
		a, aerr := BuildBoardEvent(k, BoardSpec{BoardD: "board-tie", Title: "A"}, 1700000000)
		if aerr != nil {
			t.Fatalf("build a: %v", aerr)
		}
		b, berr := BuildBoardEvent(k, BoardSpec{BoardD: "board-tie", Title: "B"}, 1700000000)
		if berr != nil {
			t.Fatalf("build b: %v", berr)
		}
		if a.ID == b.ID {
			t.Skip("degenerate: two builds produced the identical id, cannot exercise the tie-break")
		}
		want := a
		if b.ID < a.ID {
			want = b
		}
		tieCoord := BoardCoord(k.PubKeyHex(), "board-tie")
		for _, events := range [][]*nostr.Event{{a, b}, {b, a}} {
			win, ok := WinningBoardEvent(events, tieCoord)
			if !ok || win.ID != want.ID {
				t.Fatalf("tie-break winner = %v (ok=%v), want %s", win, ok, want.ID)
			}
		}
	})

	t.Run("a forged event never wins", func(t *testing.T) {
		forged := *newer
		forged.Sig = "00" + forged.Sig[2:]
		win, ok := WinningBoardEvent([]*nostr.Event{older, &forged}, coord)
		if !ok || win.ID != older.ID {
			t.Fatalf("forged (later) event won over the older-but-genuine one: win=%v ok=%v", win, ok)
		}
	})

	t.Run("nil and not-found are safe", func(t *testing.T) {
		if win, ok := WinningBoardEvent(nil, coord); ok || win != nil {
			t.Fatalf("empty input: win=%v ok=%v, want nil,false", win, ok)
		}
		if win, ok := WinningBoardEvent([]*nostr.Event{nil, other}, coord); ok || win != nil {
			t.Fatalf("only-foreign-coordinate input: win=%v ok=%v, want nil,false", win, ok)
		}
	})
}

func findTagOrEmpty(e *nostr.Event, name string) string {
	v, _ := findTag(e.Tags, name)
	return v
}
