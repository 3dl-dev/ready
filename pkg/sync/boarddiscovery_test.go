package sync

import (
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// board builds a signed 30301 board event with d=boardD authored by k.
func boardEvent(t *testing.T, k *nostr.Key, boardD string) *nostr.Event {
	t.Helper()
	e, err := BuildBoardEvent(k, BoardSpec{BoardD: boardD, Title: boardD}, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildBoardEvent %s: %v", boardD, err)
	}
	return e
}

// TestDiscoverOwnerBoards_EnumeratesEverySignedBoardOfOwner is the discovery core
// behind `rd follow`: from a relay snapshot it returns EVERY board the owner
// published (deduped, sorted) and nothing else.
func TestDiscoverOwnerBoards_EnumeratesEverySignedBoardOfOwner(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	other, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey other: %v", err)
	}

	events := []*nostr.Event{
		boardEvent(t, owner, "alpha"),
		boardEvent(t, owner, "beta"),
		boardEvent(t, owner, "gamma"),
		boardEvent(t, owner, "alpha"), // duplicate d — must collapse
		boardEvent(t, other, "delta"), // foreign owner — must be excluded
	}

	got := DiscoverOwnerBoards(events, []string{owner.PubKeyHex()}, "")
	want := []string{
		BoardCoord(owner.PubKeyHex(), "alpha"),
		BoardCoord(owner.PubKeyHex(), "beta"),
		BoardCoord(owner.PubKeyHex(), "gamma"),
	}
	if len(got) != len(want) {
		t.Fatalf("DiscoverOwnerBoards = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("board[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestDiscoverOwnerBoards_SingleBoardFilter restricts discovery to one d.
func TestDiscoverOwnerBoards_SingleBoardFilter(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	events := []*nostr.Event{
		boardEvent(t, owner, "alpha"),
		boardEvent(t, owner, "beta"),
	}
	got := DiscoverOwnerBoards(events, []string{owner.PubKeyHex()}, "beta")
	if len(got) != 1 || got[0] != BoardCoord(owner.PubKeyHex(), "beta") {
		t.Fatalf("single-board filter = %v, want [%s]", got, BoardCoord(owner.PubKeyHex(), "beta"))
	}
}

// TestDiscoverOwnerBoards_DropsForgedBoardEvent proves a relay-served 30301 whose
// signature does not verify never mints a coordinate.
func TestDiscoverOwnerBoards_DropsForgedBoardEvent(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	good := boardEvent(t, owner, "alpha")
	forged := boardEvent(t, owner, "evil")
	forged.Sig = "00" + forged.Sig[2:] // corrupt the signature

	got := DiscoverOwnerBoards([]*nostr.Event{good, forged}, []string{owner.PubKeyHex()}, "")
	if len(got) != 1 || got[0] != BoardCoord(owner.PubKeyHex(), "alpha") {
		t.Fatalf("discovery admitted a forged board: got %v", got)
	}
}
