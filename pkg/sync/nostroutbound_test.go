package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// TestEventBelongsToBoard is the ready-54e done condition for duplication (2):
// EventBelongsToBoard is EXPORTED so cmd/rd/follow.go can call this single
// implementation instead of reimplementing the any-"a"-tag membership check
// (cmd/rd's now-deleted eventBelongsToFollowedBoard). This test exercises the
// three cases that logic must get right, directly against the exported symbol.
func TestEventBelongsToBoard(t *testing.T) {
	k := testKey(t)
	owner := k.PubKeyHex()
	coord := BoardCoord(owner, "team")

	// The board's own 30301 event IS a member of its own coordinate.
	boardEvent := &nostr.Event{Kind: KindBoard, Tags: [][]string{{"d", "team"}}, PubKey: owner}
	if !EventBelongsToBoard(boardEvent, coord) {
		t.Errorf("board's own 30301 event should belong to its own coordinate")
	}

	// A card carrying a single "a" tag equal to coord belongs.
	card := &nostr.Event{Kind: KindCard, Tags: [][]string{{"a", coord}}}
	if !EventBelongsToBoard(card, coord) {
		t.Errorf("card with a single matching \"a\" tag should belong")
	}

	// A NIP-34 status event carries its OWN item coordinate as the FIRST "a" tag
	// and the board coordinate as a SECOND, additive "a" tag — EVERY "a" tag must
	// be checked, not just the first (ready-7ec).
	status := &nostr.Event{Kind: KindStatusOpen, Tags: [][]string{
		{"a", coord + ":item-1"},
		{"a", coord},
	}}
	if !EventBelongsToBoard(status, coord) {
		t.Errorf("status event's SECOND \"a\" tag matching coord should still belong")
	}

	// An event for a different board does not belong.
	other := &nostr.Event{Kind: KindCard, Tags: [][]string{{"a", BoardCoord(owner, "other")}}}
	if EventBelongsToBoard(other, coord) {
		t.Errorf("event scoped to a different board coordinate should NOT belong")
	}
}
