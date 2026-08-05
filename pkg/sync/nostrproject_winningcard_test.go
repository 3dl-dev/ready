package sync

// WinningCardEvent (ready-a43) answers "which kind-30302 event does a relay serve at
// this coordinate right now" — the question a re-seal must answer before it can
// supersede a plaintext card. The failure modes it has to be pinned against are all
// silent: pick the wrong event and the re-seal publishes a replacement that never
// evicts anything, while every local signal reports success.

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

func buildTestCard(t *testing.T, k *nostr.Key, boardAuthor, boardD, itemID, title string, createdAt int64) *nostr.Event {
	t.Helper()
	e, err := BuildCardEvent(k, CardSpec{
		ItemID: itemID, Title: title, BoardD: boardD, BoardAuthor: boardAuthor, Status: "inbox",
	}, createdAt)
	if err != nil {
		t.Fatalf("BuildCardEvent(%s @%d): %v", itemID, createdAt, err)
	}
	return e
}

func TestWinningCardEvent_LatestWinsTieBreakAuthorAndVerification(t *testing.T) {
	owner := testKey(t)
	boardD := "board-x"
	coord := BoardCoord(owner.PubKeyHex(), boardD)

	older := buildTestCard(t, owner, owner.PubKeyHex(), boardD, "x-1", "old", 1700000000)
	newer := buildTestCard(t, owner, owner.PubKeyHex(), boardD, "x-1", "new", 1700000010)
	otherItem := buildTestCard(t, owner, owner.PubKeyHex(), boardD, "x-2", "other item", 1700000020)
	otherBoard := buildTestCard(t, owner, owner.PubKeyHex(), "board-y", "x-1", "other board", 1700000030)

	// Order independence: latest-wins is a pure function of the event SET, never of
	// append/merge/fetch order (§4.5) — the property that makes two machines project
	// the same winner.
	for _, events := range [][]*nostr.Event{
		{older, newer, otherItem, otherBoard},
		{otherBoard, newer, older, otherItem},
		{newer, otherItem, otherBoard, older},
	} {
		win, ok := WinningCardEvent(events, coord, "x-1")
		if !ok {
			t.Fatalf("no winner among %d events", len(events))
		}
		if win.ID != newer.ID {
			t.Fatalf("winner = %s, want the later card %s (a card for another item or another board must never win this contest)", win.ID, newer.ID)
		}
	}

	t.Run("same created_at ties to the lowest event id", func(t *testing.T) {
		// The tie-break must match newerThan/NIP-33 exactly: a re-seal that ties with
		// the card it means to supersede is decided by this rule, and losing it is a
		// silent no-op (ready-500).
		a := buildTestCard(t, owner, owner.PubKeyHex(), boardD, "x-tie", "A", 1700000000)
		b := buildTestCard(t, owner, owner.PubKeyHex(), boardD, "x-tie", "B", 1700000000)
		if a.ID == b.ID {
			t.Skip("degenerate: identical ids, cannot exercise the tie-break")
		}
		want := a
		if b.ID < a.ID {
			want = b
		}
		for _, events := range [][]*nostr.Event{{a, b}, {b, a}} {
			win, ok := WinningCardEvent(events, coord, "x-tie")
			if !ok || win.ID != want.ID {
				t.Fatalf("tie-break winner = %v (ok=%v), want %s", win, ok, want.ID)
			}
		}
	})

	t.Run("a forged event never wins", func(t *testing.T) {
		forged := *newer
		// tamperSigHex XORs the first byte, so the signature ALWAYS changes.
		// The previous form, `"00" + Sig[2:]`, was a silent no-op on the 1-in-256
		// signatures that already begin with 00: the "forged" event was then
		// byte-identical to the genuine one, so it SHOULD win on created_at and
		// this subtest failed as a false alarm. Same defect, same fix as
		// boardarchive_test.go's — see tamperSigHex's own comment there.
		forged.Sig = tamperSigHex(t, newer.Sig)
		if forged.Sig == newer.Sig {
			t.Fatalf("test fixture bug: tampered sig equals the genuine one (%s), nothing was forged", newer.Sig)
		}
		win, ok := WinningCardEvent([]*nostr.Event{older, &forged}, coord, "x-1")
		if !ok || win.ID != older.ID {
			t.Fatalf("a forged later event won: win=%v ok=%v — a relay answer is untrusted input", win, ok)
		}
	})

	t.Run("a later card by another author wins, and is reported as such", func(t *testing.T) {
		// The contract's sharpest edge. kind-30302 is addressable on (kind, EVENT
		// AUTHOR, d), so a contributor's card for the same item sits in its OWN relay
		// slot — but it is the one the fold resolves and the one being read. Returning
		// the owner's older card here instead would let a re-sealer "successfully"
		// replace a slot nobody reads while the contributor's plaintext copy stays
		// served. So the cross-author winner must come back, PubKey and all, for the
		// caller to refuse on (cmd/rd errCardForeignAuthor).
		contributor := testKey(t)
		theirs := buildTestCard(t, contributor, owner.PubKeyHex(), boardD, "x-1", "contributor card", 1700009999)

		for _, events := range [][]*nostr.Event{{older, newer, theirs}, {theirs, newer, older}} {
			win, ok := WinningCardEvent(events, coord, "x-1")
			if !ok {
				t.Fatal("no winner")
			}
			if win.ID != theirs.ID {
				t.Fatalf("winner = %s, want the contributor's later card %s — hiding it would let a re-seal report success while the read copy stays plaintext", win.ID, theirs.ID)
			}
			if win.PubKey != contributor.PubKeyHex() {
				t.Fatalf("winner PubKey = %s, want the contributor %s (the caller decides on this field)", win.PubKey, contributor.PubKeyHex())
			}
		}
	})

	t.Run("nil and not-found are safe", func(t *testing.T) {
		if win, ok := WinningCardEvent(nil, coord, "x-1"); ok || win != nil {
			t.Fatalf("empty input: win=%v ok=%v, want nil,false", win, ok)
		}
		if win, ok := WinningCardEvent([]*nostr.Event{nil, otherItem}, coord, "x-1"); ok || win != nil {
			t.Fatalf("no card for this item: win=%v ok=%v, want nil,false", win, ok)
		}
	})
}
