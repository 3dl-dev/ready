package main

// init_test.go — coverage for ready-cbc: `rd init` never leaves a confidential
// board un-bootstrapped.
//
// THE GAP THIS CLOSES: boardConfidentialEnvelope (confidential.go) used to mint
// the board's epoch-1 CEK+LTK and publish the owner self-grant only on the
// owner's FIRST WRITE. The browser cannot perform that mint itself — wrapping a
// CEK to the owner's own key is a NIP-44 ENCRYPT, which needs ECDH over the
// secret key the page never holds (web/board/src/lib/keyunwrap.ts's
// Nip44Provider deliberately has no `encrypt`). So an owner who ran `rd init`
// and opened the board page before ever writing from the CLI stayed read-only
// in the browser, even though ready-191 already taught the page how to SEAL a
// write once it holds a CEK.
//
// THE FIX: `rd init` mints and publishes the owner self-grant itself, right
// after the board event, so a confidential board is bootstrapped from birth —
// before any `rd create`/`rd claim`/etc ever runs.

import (
	"testing"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// TestInit_ConfidentialBoard_EagerlyBootstrapsOwnerSelfGrant is this item's
// literal done condition at the log level: immediately after `rd init` on a
// confidential (default) board, with NO other write of any kind, the local
// authoritative log already carries the owner's kind-39301 CEK+LTK self-grant
// at epoch 1 — the exact event a browser needs to fetch and nip44.decrypt to
// derive the board's write key. Before this fix, only the 30301 board event
// existed at this point; the self-grant did not appear until the owner's first
// CLI write.
func TestInit_ConfidentialBoard_EagerlyBootstrapsOwnerSelfGrant(t *testing.T) {
	dir := isolatedProject(t)

	if err := initNostr(dir, "proj", "", false /* public */, nil, false, true); err != nil {
		t.Fatalf("initNostr: %v", err)
	}

	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	var boardEvents, grantEvents int
	for _, e := range events {
		switch e.Kind {
		case rdSync.KindBoard:
			boardEvents++
		case rdSync.KindRoleGrant:
			grantEvents++
			if e.PubKey != owner {
				t.Errorf("self-grant signed by %q, want owner %q", e.PubKey, owner)
			}
		default:
			t.Errorf("unexpected event kind %d in the log — init should write ONLY the board event and the owner self-grant, nothing else", e.Kind)
		}
	}
	if boardEvents != 1 {
		t.Fatalf("board events = %d, want exactly 1", boardEvents)
	}
	if grantEvents != 1 {
		t.Fatalf("owner self-grant events = %d, want exactly 1 — `rd init` must publish the CEK self-grant eagerly, before any write", grantEvents)
	}

	// The self-grant must actually be OPENABLE by the owner's own key (what a
	// browser holding a NIP-07 signer for this identity would do via
	// nip44.decrypt) and must yield epoch 1 — the epoch every subsequent write
	// seals under.
	kr := rdSync.DeriveBoardKeyring(events, k, owner, boardD)
	epoch, _, ok := kr.CurrentEpoch(coord)
	if !ok {
		t.Fatalf("owner cannot derive a CEK from its own post-init log — the self-grant is unreadable")
	}
	if epoch != 1 {
		t.Errorf("epoch = %d, want 1 (the board's first cutover)", epoch)
	}
	if _, ok := kr.LTK(coord); !ok {
		t.Errorf("owner holds no LTK after init — label tokenization would be unavailable from birth")
	}
}

// TestInit_PublicBoard_NoSelfGrant guards against gold-plating the fix onto
// --public boards: a public board seals nothing, so it must mint no CEK/LTK
// and publish no self-grant. Only the board event should exist after init.
func TestInit_PublicBoard_NoSelfGrant(t *testing.T) {
	dir := isolatedProject(t)

	if err := initNostr(dir, "proj", "", true /* public */, nil, false, true); err != nil {
		t.Fatalf("initNostr: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("events after init --public = %d, want exactly 1 (the board event only, no self-grant)", len(events))
	}
	if events[0].Kind != rdSync.KindBoard {
		t.Fatalf("the one event after init --public has kind %d, want the board event (%d)", events[0].Kind, rdSync.KindBoard)
	}
}

// TestInit_ConfidentialBoard_FirstWriteReusesTheEagerGrant proves the eager
// bootstrap actually REPLACES the old first-write mint rather than merely
// preceding it: boardConfidentialEnvelope, called exactly as the owner's first
// `rd create` would call it, must find the envelope init already published and
// must NOT mint (and publish) a second epoch-1 self-grant. Two competing
// epoch-1 keys on the same board would silently split every card's readability
// depending on which grant a given reader's log happened to retain.
func TestInit_ConfidentialBoard_FirstWriteReusesTheEagerGrant(t *testing.T) {
	dir := isolatedProject(t)

	if err := initNostr(dir, "proj", "", false, nil, false, true); err != nil {
		t.Fatalf("initNostr: %v", err)
	}

	before, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll (before): %v", err)
	}

	pub, ok, err := nostrPublisher()
	if err != nil {
		t.Fatalf("nostrPublisher: %v", err)
	}
	if !ok {
		t.Fatalf("nostrPublisher: no project resolved")
	}
	owner := pub.Key.PubKeyHex()
	boardD := projectPrefix(dir)

	// This is exactly what setCardEnvelope calls on the owner's first write.
	env, err := boardConfidentialEnvelope(dir, pub, owner, boardD)
	if err != nil {
		t.Fatalf("boardConfidentialEnvelope: %v", err)
	}
	if env == nil {
		t.Fatalf("boardConfidentialEnvelope returned nil (plaintext) for a confidential board")
	}
	if env.Epoch != 1 {
		t.Errorf("Epoch = %d, want 1 (the eager grant's epoch, not a freshly minted one)", env.Epoch)
	}

	after, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll (after): %v", err)
	}
	if len(after) != len(before) {
		t.Fatalf("log grew from %d to %d events calling boardConfidentialEnvelope on the owner's first write — "+
			"it minted a SECOND epoch-1 self-grant instead of reusing the one `rd init` published", len(before), len(after))
	}

	// The CEK resolved on this "first write" must be the SAME one a reader of
	// the post-init log alone already derives — i.e. init's grant is the one
	// and only source of truth, not a coincidence of two independently-minted
	// epoch-1 keys.
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	coord := rdSync.BoardCoord(owner, boardD)
	kr := rdSync.DeriveBoardKeyring(before, k, owner, boardD)
	_, cekFromInit, ok := kr.CurrentEpoch(coord)
	if !ok {
		t.Fatalf("could not derive the post-init CEK independently")
	}
	if cekFromInit != env.CEK {
		t.Errorf("boardConfidentialEnvelope's CEK diverges from the CEK derivable from init's own self-grant — two different keys exist for epoch 1")
	}
}
