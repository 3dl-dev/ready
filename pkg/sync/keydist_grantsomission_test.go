package sync

// §11.13a WITNESSES (ready-9a6) — the Go port of the browser hardening ready-daf
// shipped in web/board/src/lib/confidentiality.ts, witnessed there by
// web/board/src/main.grantsomission.test.ts. Separate file for the same reason the
// browser gave it one: this is the confidentiality DECISION layer, not keydist's
// grant→key unwrap, and it is falsifiable on its own.
//
// THE SHAPE THESE PIN SHUT. §11.13 derives the board cutover as a MINIMUM over the
// owner CEK grants the reader was GIVEN. Omission can only ever REMOVE grants, so
// the derived instant is always >= the truth, and the fail-open case is exactly
// "strictly greater": every plaintext card authored between the true cutover and
// the derived one satisfies §11.4's grandfather clause and folds in CLEAR.
//
// IT IS NOT ONLY AN ATTACK. kind 39301 is addressable with d=<boardD>:<grantee>,
// so a NIP-01-conformant relay retains only each grantee's NEWEST grant: after a
// rotation the epoch-1 grants are legitimately gone, and a local log whose sync
// only ever saw that answer — a fresh `rd join`, a clone, a lossy relay — derives
// the ROTATION's instant as the cutover. gwFixture below is exactly that board.

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

const gwT0 = int64(1_800_000_000)

// gwFixture is one confidential board's full history, from which each test serves
// a SUBSET — which is all a relay, or a partial sync, ever gives a reader:
//
//	t0-100  : owner-signed PLAINTEXT card    — genuinely pre-cutover, grandfatherable
//	t0      : owner self-grant, epoch 1      — the TRUE cutover
//	t0+100  : sealed card, epoch 1           — the witness that survives grant omission
//	t0+300  : owner-signed PLAINTEXT card    — the leak §11.4 must never grandfather
//	t0+600  : owner self-grant, epoch 2      — the rotation
//
// Both plaintext cards are OWNER-signed and admitted by the trust gate on purpose:
// a quarantine that only happened to drop them for being untrusted would prove
// nothing about the confidentiality gate.
type gwFixture struct {
	owner      *nostr.Key
	boardD     string
	coord      string
	cek1, cek2 [32]byte
	board      *nostr.Event
	grant1     *nostr.Event // epoch 1 @ t0     — what an omitting/rotated answer drops
	grant2     *nostr.Event // epoch 2 @ t0+600 — the rotation
	sealed1    *nostr.Event // sealed under epoch 1 @ t0+100
	preClear   *nostr.Event // plaintext @ t0-100
	leak       *nostr.Event // plaintext @ t0+300
}

func gwNewFixture(t *testing.T) *gwFixture {
	t.Helper()
	f := &gwFixture{owner: kdKey(t), boardD: "ready"}
	f.coord = BoardCoord(f.owner.PubKeyHex(), f.boardD)
	var err error
	if f.cek1, err = MintKey(); err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	if f.cek2, err = MintKey(); err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	f.board, err = BuildBoardEvent(f.owner, BoardSpec{
		BoardD: f.boardD, Title: f.boardD, Maintainers: []string{f.owner.PubKeyHex()},
	}, gwT0-1000)
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	f.grant1 = f.selfGrant(t, f.cek1, 1, gwT0)
	f.grant2 = f.selfGrant(t, f.cek2, 2, gwT0+600)
	f.sealed1 = kdCard(t, f.owner, "ready-gw-sealed", "sealed", "sealed body", f.boardD,
		&Envelope{CEK: f.cek1, Epoch: 1}, gwT0+100)
	f.preClear = kdCard(t, f.owner, "ready-gw-old", "old plaintext", "clear body", f.boardD, nil, gwT0-100)
	f.leak = kdCard(t, f.owner, "ready-gw-leak", "LEAKED IN CLEAR", "clear body", f.boardD, nil, gwT0+300)
	return f
}

// selfGrant mints the owner's own CEK-bearing grant for an epoch — the event that
// establishes the cutover, and the one an addressable relay slot replaces on a
// rotation.
func (f *gwFixture) selfGrant(t *testing.T, cek [32]byte, epoch int, at int64) *nostr.Event {
	t.Helper()
	return kdGrant(t, f.owner, RoleGrantSpec{
		BoardD: f.boardD, BoardAuthor: f.owner.PubKeyHex(), Grantee: f.owner.PubKeyHex(),
		Role:       RoleOwner,
		WrappedCEK: kdWrap(t, f.owner, f.owner.PubKeyHex(), cek), CEKEpoch: epoch,
	}, at)
}

// project folds the served events as the OWNER — the reader who holds the newest
// grant — with ONE derived keyring wired into BOTH ProjectOptions slots, exactly as
// cmd/rd/nostr.go's nostrProjectAllItems does in production.
func (f *gwFixture) project(t *testing.T, served []*nostr.Event) (map[string]*state.Item, *BoardKeyring) {
	t.Helper()
	kr := DeriveBoardKeyring(served, f.owner, f.owner.PubKeyHex(), f.boardD)
	return ProjectItems(served, ProjectOptions{
		Trusted:         map[string]bool{f.owner.PubKeyHex(): true},
		PinnedBoard:     f.coord,
		Decryptor:       kr,
		EncryptedBoards: kr,
	}), kr
}

// TestGrantsWithheldQuarantinesGrandfatheredPlaintext is ready-9a6's done
// condition: a log with the epoch-1 grants withheld (the post-rotation answer) plus
// an owner-signed plaintext card between the true cutover and the rotation, folded
// through ProjectItems, QUARANTINES that card instead of grandfathering it.
//
// The complete-log arm is the control: it proves the fixture's own §11.13 answer is
// right when nothing is missing (the pre-cutover card folds, the leak does not), so
// the difference in the omitted arm is the omission and nothing else.
func TestGrantsWithheldQuarantinesGrandfatheredPlaintext(t *testing.T) {
	f := gwNewFixture(t)

	// CONTROL — the whole history is served, so §11.13's derived cutover IS the truth.
	full := []*nostr.Event{f.board, f.grant1, f.sealed1, f.preClear, f.leak, f.grant2}
	items, kr := f.project(t, full)
	if cut, ok := kr.Cutover(f.coord); !ok || cut != gwT0 {
		t.Fatalf("complete answer: cutover = %d (ok=%v), want %d", cut, ok, gwT0)
	}
	if items["ready-gw-old"] == nil {
		t.Fatal("complete answer: genuinely pre-cutover plaintext card was not grandfathered")
	}
	if items["ready-gw-leak"] != nil {
		t.Fatal("complete answer: post-cutover plaintext card folded — the §11.4 gate is not even running")
	}

	// THE OMISSION — only the rotation's grants survived the addressable slot.
	served := []*nostr.Event{f.board, f.sealed1, f.preClear, f.leak, f.grant2}
	items, kr = f.project(t, served)
	if cut, ok := kr.Cutover(f.coord); !ok || cut != 0 {
		t.Fatalf("withheld answer: cutover = %d (ok=%v), want the fail-closed shape (0, true) — "+
			"§11.13 alone derives %d here and grandfathers everything before it", cut, ok, gwT0+600)
	}
	if it := items["ready-gw-leak"]; it != nil {
		t.Fatalf("FAIL-OPEN: plaintext card authored after the true cutover folded in clear: title=%q", it.Title)
	}
	// Fail-closed costs visibility, and says so: the genuinely pre-cutover card is
	// withheld too, because a cutover this reader cannot trust grandfathers NOTHING.
	if items["ready-gw-old"] != nil {
		t.Fatal("withheld answer: a contradicted cutover must grandfather nothing at all")
	}
	// Quarantine drops what is NOT a well-formed sealed envelope, never the sealed
	// cards themselves — the guard withholds cleartext, it does not hide the board.
	if items["ready-gw-sealed"] == nil {
		t.Fatal("withheld answer: a well-formed sealed card was quarantined")
	}
}

// TestGrantsWithheldTimeWitnessAlone isolates WITNESS A (time). The omitted grant
// is replaced by a LATER grant at the SAME epoch — `rd grant` adding a member to a
// board that never rotated — so the epoch floor stays 1 and witness B is silent by
// construction. Only "a verified sealed card older than the derived cutover" is
// left to catch it, and what it catches is real: every plaintext card between the
// owner's original grant and the new member's would otherwise be grandfathered.
func TestGrantsWithheldTimeWitnessAlone(t *testing.T) {
	f := gwNewFixture(t)
	member := kdKey(t)
	lateSameEpoch := kdGrant(t, f.owner, RoleGrantSpec{
		BoardD: f.boardD, BoardAuthor: f.owner.PubKeyHex(), Grantee: member.PubKeyHex(),
		Role:       RoleContributor,
		WrappedCEK: kdWrap(t, f.owner, member.PubKeyHex(), f.cek1), CEKEpoch: 1,
	}, gwT0+600)

	served := []*nostr.Event{f.board, f.sealed1, f.leak, lateSameEpoch}
	items, kr := f.project(t, served)
	if floor := kr.epochFloor[f.coord]; floor != 1 {
		t.Fatalf("epoch floor = %d, want 1 — witness B must be silent for this case to isolate A", floor)
	}
	if cut, ok := kr.Cutover(f.coord); !ok || cut != 0 {
		t.Fatalf("cutover = %d (ok=%v), want (0,true): the sealed card at %d predates the derived %d",
			cut, ok, gwT0+100, gwT0+600)
	}
	if it := items["ready-gw-leak"]; it != nil {
		t.Fatalf("FAIL-OPEN: %q folded in clear on a same-epoch omission", it.Title)
	}
}

// TestGrantsWithheldEpochWitnessAlone isolates WITNESS B (epoch) — the case witness
// A cannot see. A stale writer that missed the rotation seals under the OLD epoch
// and publishes AFTER the manufactured cutover, so its card is NEWER than the
// derived instant and raises no alarm by time; its cek_epoch is nonetheless BELOW
// every served grant's, which proves that epoch's grant was never served.
func TestGrantsWithheldEpochWitnessAlone(t *testing.T) {
	f := gwNewFixture(t)
	stale := kdCard(t, f.owner, "ready-gw-stale", "stale seal", "body", f.boardD,
		&Envelope{CEK: f.cek1, Epoch: 1}, gwT0+900) // AFTER the derived cutover t0+600

	served := []*nostr.Event{f.board, f.leak, f.grant2, stale}
	items, kr := f.project(t, served)
	if cut, ok := kr.Cutover(f.coord); !ok || cut != 0 {
		t.Fatalf("cutover = %d (ok=%v), want (0,true): epoch 1 is below the served floor 2", cut, ok)
	}
	if it := items["ready-gw-leak"]; it != nil {
		t.Fatalf("FAIL-OPEN: %q folded in clear while a below-floor epoch proved a grant was missing", it.Title)
	}
}

// TestGrantsWithheldIgnoresHigherEpochAndUnverified pins the two ways the guard
// must STAY QUIET, because a guard that fires on everything withholds every board
// unconditionally and pins nothing:
//
//   - AN EPOCH ABOVE the served grants is deliberately NOT a contradiction. It does
//     prove a grant is missing, but a missing LATER grant cannot move a MINIMUM, so
//     the cutover still stands and quarantining would cost visibility for no
//     security gain (the browser fixture's conf-004 is this same shape).
//   - AN UNVERIFIABLE sealed event witnesses nothing, in either direction. The
//     event here is re-stamped after signing, which breaks its id and signature;
//     believed, it would fire BOTH witnesses at once.
func TestGrantsWithheldIgnoresHigherEpochAndUnverified(t *testing.T) {
	f := gwNewFixture(t)
	high := kdCard(t, f.owner, "ready-gw-high", "sealed high", "body", f.boardD,
		&Envelope{CEK: f.cek2, Epoch: 9}, gwT0+300)
	tampered := kdCard(t, f.owner, "ready-gw-tampered", "sealed", "body", f.boardD,
		&Envelope{CEK: f.cek1, Epoch: 1}, gwT0+100)
	tampered.CreatedAt = gwT0 - 500 // id/sig no longer cover this
	if tampered.Verify() == nil {
		t.Fatal("fixture: the re-stamped event still verifies — it cannot test the verify gate")
	}

	served := []*nostr.Event{f.board, f.grant1, f.preClear, high, tampered}
	items, kr := f.project(t, served)
	if cut, ok := kr.Cutover(f.coord); !ok || cut != gwT0 {
		t.Fatalf("cutover = %d (ok=%v), want the derived %d intact: neither a higher epoch nor an "+
			"unverifiable event is a contradiction", cut, ok, gwT0)
	}
	if items["ready-gw-old"] == nil {
		t.Fatal("a genuinely pre-cutover plaintext card must still be grandfathered here")
	}
}
