// Deterministic proofs for ready-b57: status-authority is SEPARATE from read-trust,
// and 'by' provenance is bound to the signer's authority.
//
// READ-TRUST (ProjectOptions.Trusted) decides who may ENTER projection at all — the
// full web-of-trust allowlist. STATUS-AUTHORITY decides who may author an
// AUTHORITATIVE status transition on a given item: the item AUTHOR or a declared
// BOARD MAINTAINER (30301 "p" tags), NOT the whole trust set. Conflating them (the
// pre-b57 prod wiring passed the entire trust set as Maintainers) let any admitted
// key close/reopen ANY item and forge its 'by' history. These tests hold the two
// gates apart.
package sync

import (
	"testing"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
)

// board authored by author with the given maintainer p-tags.
func mustBoard(t *testing.T, author *nostr.Key, boardD string, maintainers ...string) *nostr.Event {
	t.Helper()
	e, err := BuildBoardEvent(author, BoardSpec{BoardD: boardD, Title: boardD, Maintainers: maintainers}, 1)
	if err != nil {
		t.Fatalf("build board: %v", err)
	}
	return e
}

func mustStatus(t *testing.T, k *nostr.Key, itemID, status, cardID string, ts int64) *nostr.Event {
	t.Helper()
	e, err := BuildStatusEvent(k, itemID, status, cardID, "", ts)
	if err != nil {
		t.Fatalf("build status: %v", err)
	}
	return e
}

// TestStatusAuthority_DropsTrustedNonMaintainer is ready-b57 proof (b): a key that is
// READ-TRUSTED (admitted to the log) but is NOT the item author and NOT a board
// maintainer CANNOT author an authoritative status transition on another item's card.
// Its later "done" is ignored; the item keeps the author's status, and the intruder's
// transition never enters the history.
func TestStatusAuthority_DropsTrustedNonMaintainer(t *testing.T) {
	author := testKey(t)
	intruder := testKey(t)
	if author.PubKeyHex() == intruder.PubKeyHex() {
		t.Fatal("keys collided")
	}
	const itemID = "ready-b57-auth"

	// Board declares only the author as maintainer.
	board := mustBoard(t, author, "ready", author.PubKeyHex())
	card, err := BuildCardEvent(author, CardSpec{ItemID: itemID, Title: "legit", Status: state.StatusActive, Type: "task", BoardD: "ready"}, 1000)
	if err != nil {
		t.Fatalf("card: %v", err)
	}
	authorStatus := mustStatus(t, author, itemID, state.StatusActive, card.ID, 1000)
	// Intruder is trusted-to-read but NOT a maintainer; publishes a LATER done.
	intruderStatus := mustStatus(t, intruder, itemID, state.StatusDone, card.ID, 2000)

	events := []*nostr.Event{board, card, authorStatus, intruderStatus}
	// Both keys pass READ-trust (both admitted); Maintainers left nil so authority is
	// board-derived (author only).
	trust := map[string]bool{author.PubKeyHex(): true, intruder.PubKeyHex(): true}
	items := ProjectItems(events, ProjectOptions{Trusted: trust})

	it := items[itemID]
	if it == nil {
		t.Fatal("item missing from projection")
	}
	if it.Status != state.StatusActive {
		t.Fatalf("intruder (trusted non-maintainer) seized status: got %q want %q", it.Status, state.StatusActive)
	}
	for _, h := range it.History {
		if h.ToStatus == state.StatusDone || h.ChangedBy == intruder.PubKeyHex() {
			t.Fatalf("intruder transition leaked into history: %+v", h)
		}
	}

	// Control: promote the intruder to a DECLARED board maintainer -> its status is
	// now authoritative (this is the legitimate multi-machine path; the board author
	// co-signs authority via a "p" tag). Proves the gate keys on maintainer status,
	// not on the intruder's identity per se.
	boardWithBoth := mustBoard(t, author, "ready", author.PubKeyHex(), intruder.PubKeyHex())
	events2 := []*nostr.Event{boardWithBoth, card, authorStatus, intruderStatus}
	items2 := ProjectItems(events2, ProjectOptions{Trusted: trust})
	if got := items2[itemID].Status; got != state.StatusDone {
		t.Fatalf("declared board maintainer must be authoritative: got %q want %q", got, state.StatusDone)
	}
}

// TestByProvenance_SpoofIgnoredFromNonMaintainer is ready-b57 proof (c): the
// rd-extension "by" tag REWRITES audit provenance, so it is honored ONLY from a
// signer authorized to rewrite it (a board maintainer running the migration). A
// signer that is authoritative-for-status but is NOT a board maintainer (here: a bare
// item author, no board event) cannot attribute its transition to an arbitrary third
// party — the "by" tag is ignored and ChangedBy falls back to the signer pubkey.
func TestByProvenance_SpoofIgnoredFromNonMaintainer(t *testing.T) {
	author := testKey(t)
	const itemID = "ready-b57-by"
	const spoofed = "victim@example.com"

	// No board event => author is authoritative for status (author == signer) but is
	// NOT a board maintainer. The status event carries a spoofed "by".
	card, err := BuildCardEvent(author, CardSpec{ItemID: itemID, Title: "t", Status: state.StatusActive, Type: "task", BoardD: "ready"}, 1000)
	if err != nil {
		t.Fatalf("card: %v", err)
	}
	spoofStatus, err := BuildHistoricalStatusEvent(author, itemID, state.StatusActive, spoofed, "", 1000)
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	trust := map[string]bool{author.PubKeyHex(): true}
	items := ProjectItems([]*nostr.Event{card, spoofStatus}, ProjectOptions{Trusted: trust})
	it := items[itemID]
	if it == nil || len(it.History) == 0 {
		t.Fatal("expected a history entry")
	}
	for _, h := range it.History {
		if h.ChangedBy == spoofed {
			t.Fatalf("spoofed 'by' honored from a non-maintainer signer: ChangedBy=%q", h.ChangedBy)
		}
	}
	if last := it.History[len(it.History)-1]; last.ChangedBy != author.PubKeyHex() {
		t.Fatalf("ChangedBy must fall back to signer: got %q want %q", last.ChangedBy, author.PubKeyHex())
	}
}

// TestByProvenance_HonoredFromBoardMaintainer is the ready-b57 companion proof: the
// migration path is UNCHANGED. When the signer IS the board maintainer (the entity
// that runs the migration), the "by" tag is honored so the ORIGINAL campfire actor
// survives item-for-item — exactly the ready-d65 provenance guarantee.
func TestByProvenance_HonoredFromBoardMaintainer(t *testing.T) {
	author := testKey(t)
	const itemID = "ready-b57-by-ok"
	const original = "baron@campfire"

	board := mustBoard(t, author, "ready", author.PubKeyHex())
	card, err := BuildCardEvent(author, CardSpec{ItemID: itemID, Title: "t", Status: state.StatusActive, Type: "task", BoardD: "ready"}, 1000)
	if err != nil {
		t.Fatalf("card: %v", err)
	}
	migStatus, err := BuildHistoricalStatusEvent(author, itemID, state.StatusActive, original, "created", 1000)
	if err != nil {
		t.Fatalf("status: %v", err)
	}

	trust := map[string]bool{author.PubKeyHex(): true}
	items := ProjectItems([]*nostr.Event{board, card, migStatus}, ProjectOptions{Trusted: trust})
	it := items[itemID]
	if it == nil || len(it.History) == 0 {
		t.Fatal("expected a history entry")
	}
	if got := it.History[len(it.History)-1].ChangedBy; got != original {
		t.Fatalf("board-maintainer 'by' must be honored (migration provenance): got %q want %q", got, original)
	}
}
