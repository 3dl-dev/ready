package main

// ready-a9b: filterArchivedBoards is the READ side of `rd board archive` at
// the portfolio-gather layer — the thing that makes `rd board`'s printed
// count converge once a board's owner marks it archived (this item's live
// audit trail: "ALL 47" before, "ALL 15" after, for the owner's real 32
// junk/cancelled/duplicate boards). This file proves the pure filtering
// contract with local-log-only fixtures (no relay reachable in this
// deterministic env — see boardTestEnv's doc), so it needs nothing from the
// network to fail if the exclusion logic regresses.

import (
	"context"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

func TestFilterArchivedBoards_DropsArchivedKeepsPlainAndKeepsUnknown(t *testing.T) {
	owner, _, _, dir := boardTestEnv(t)
	self := owner.PubKeyHex()

	plain, err := rdSync.BuildBoardEvent(owner, rdSync.BoardSpec{BoardD: "plain-board", Title: "Plain"}, 1700000000)
	if err != nil {
		t.Fatalf("build plain: %v", err)
	}
	archived, err := rdSync.BuildBoardEvent(owner, rdSync.BoardSpec{BoardD: "archived-board", Title: "Archived", Archived: true}, 1700000000)
	if err != nil {
		t.Fatalf("build archived: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{plain, archived}); err != nil {
		t.Fatalf("AppendUnique: %v", err)
	}

	plainCoord := rdSync.BoardCoord(self, "plain-board")
	archivedCoord := rdSync.BoardCoord(self, "archived-board")
	// "unknown-board" has NO kind-30301 event anywhere this read can see — a
	// coordinate the portfolio gather derived from a role-grant whose board
	// definition this machine has never fetched. Archiving is a discovery
	// nicety, not a security boundary (see filterArchivedBoards's doc), so an
	// UNRESOLVABLE coordinate must stay IN the result, not be dropped.
	unknownCoord := rdSync.BoardCoord(self, "unknown-board")

	in := []string{plainCoord, archivedCoord, unknownCoord}
	got := filterArchivedBoards(context.Background(), dir, in, nil)

	want := map[string]bool{plainCoord: true, unknownCoord: true}
	if len(got) != len(want) {
		t.Fatalf("filterArchivedBoards = %v, want exactly %v", got, want)
	}
	for _, c := range got {
		if !want[c] {
			t.Errorf("unexpected coordinate survived the filter: %s", c)
		}
		if c == archivedCoord {
			t.Errorf("archived coordinate was NOT dropped: %s", c)
		}
	}
}

func TestFilterArchivedBoards_EmptyInputIsEmptyOutput(t *testing.T) {
	_, _, _, dir := boardTestEnv(t)
	got := filterArchivedBoards(context.Background(), dir, nil, nil)
	if len(got) != 0 {
		t.Fatalf("filterArchivedBoards(nil) = %v, want empty", got)
	}
}

// TestFilterArchivedBoards_LatestWinsAcrossRepublish proves the filter tracks
// the CURRENT state, not the first-seen one: seed an archived definition,
// then a LATER plain (unarchived) republish for the same coordinate — the
// exact shape `rd board unarchive` produces — and require the coordinate to
// survive the filter.
func TestFilterArchivedBoards_LatestWinsAcrossRepublish(t *testing.T) {
	owner, _, _, dir := boardTestEnv(t)
	self := owner.PubKeyHex()

	archivedFirst, err := rdSync.BuildBoardEvent(owner, rdSync.BoardSpec{BoardD: "was-archived", Title: "T", Archived: true}, 1700000000)
	if err != nil {
		t.Fatalf("build archived: %v", err)
	}
	revivedLater, err := rdSync.BuildBoardEvent(owner, rdSync.BoardSpec{BoardD: "was-archived", Title: "T"}, 1700000010)
	if err != nil {
		t.Fatalf("build revived: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{archivedFirst, revivedLater}); err != nil {
		t.Fatalf("AppendUnique: %v", err)
	}

	coord := rdSync.BoardCoord(self, "was-archived")
	got := filterArchivedBoards(context.Background(), dir, []string{coord}, nil)
	if len(got) != 1 || got[0] != coord {
		t.Fatalf("filterArchivedBoards after unarchive = %v, want [%s]", got, coord)
	}
}
