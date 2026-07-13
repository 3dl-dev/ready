// Deterministic unit tests for the mint-and-ship invite substrate (ready-a49):
// the one-use kind-39303 nonce marker (BuildInviteConsumedEvent /
// InviteNonceConsumed) and the grant-presence gate (InviteGrantValid). Every event
// is REAL (schnorr-signed + re-verified); no network, no clock.
package sync

import (
	"testing"

	"github.com/campfire-net/ready/pkg/nostr"
)

// TestInviteConsumed_RoundTrip: a built marker consumes exactly its own nonce.
func TestInviteConsumed_RoundTrip(t *testing.T) {
	minted := testKey(t)
	owner := testKey(t)
	board := BoardCoord(owner.PubKeyHex(), testBoardD)

	ev, err := BuildInviteConsumedEvent(minted, "nonce-abc", board, 5000)
	if err != nil {
		t.Fatalf("BuildInviteConsumedEvent: %v", err)
	}
	if err := ev.Verify(); err != nil {
		t.Fatalf("marker does not verify: %v", err)
	}
	if ev.Kind != KindInviteConsumed {
		t.Errorf("marker kind = %d, want %d", ev.Kind, KindInviteConsumed)
	}

	if !InviteNonceConsumed([]*nostr.Event{ev}, "nonce-abc") {
		t.Error("InviteNonceConsumed should report the nonce consumed")
	}
	if InviteNonceConsumed([]*nostr.Event{ev}, "different-nonce") {
		t.Error("a marker for one nonce must NOT consume a different nonce")
	}
	if InviteNonceConsumed(nil, "nonce-abc") {
		t.Error("empty medium must report not-consumed")
	}
	if InviteNonceConsumed([]*nostr.Event{ev}, "") {
		t.Error("empty nonce must never match")
	}
}

// TestInviteConsumed_TamperedMarkerIgnored: a marker whose signed fields were
// altered after signing fails Verify and must NOT consume the nonce.
func TestInviteConsumed_TamperedMarkerIgnored(t *testing.T) {
	minted := testKey(t)
	board := BoardCoord(minted.PubKeyHex(), testBoardD)
	ev, err := BuildInviteConsumedEvent(minted, "nonce-xyz", board, 5000)
	if err != nil {
		t.Fatalf("BuildInviteConsumedEvent: %v", err)
	}
	// Tamper: rewrite the nonce tag WITHOUT re-signing. Verify must reject it, so
	// it cannot consume the (now-mismatched) id, nor the original nonce.
	ev.Tags[0][1] = "nonce-forged"
	if InviteNonceConsumed([]*nostr.Event{ev}, "nonce-forged") {
		t.Error("a tampered (unsigned) marker must not consume a nonce")
	}
}

// TestInviteGrantValid_OwnerRootedContributor: a real owner-signed contributor
// grant makes the minted key valid; an ungranted key and a foreign-board grant do
// not.
func TestInviteGrantValid_OwnerRootedContributor(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	minted := testKey(t)
	ungranted := testKey(t)

	grantEv := grant(t, owner, ba, minted.PubKeyHex(), RoleContributor, 0, 1000)
	events := []*nostr.Event{grantEv}

	if !InviteGrantValid(events, ba, testBoardD, minted.PubKeyHex()) {
		t.Error("owner-signed contributor grant should make the minted key valid")
	}
	if InviteGrantValid(events, ba, testBoardD, ungranted.PubKeyHex()) {
		t.Error("an ungranted key must be rejected (fail-closed)")
	}
	// Same events, but check against a DIFFERENT board d — the grant is bound to
	// testBoardD, so it must not satisfy a foreign board.
	if InviteGrantValid(events, ba, "other-board", minted.PubKeyHex()) {
		t.Error("a grant bound to one board must not validate on another (cross-board bleed)")
	}
	// No grant at all → invalid.
	if InviteGrantValid(nil, ba, testBoardD, minted.PubKeyHex()) {
		t.Error("empty event set must be fail-closed invalid")
	}
}

// TestInviteGrantValid_ForgedGrantIgnored: a grant "signed" by a non-owner for a
// maintainer/owner escalation is capped out; here we prove a self-signed grant by
// the minted key itself (not owner-rooted) does not validate it.
func TestInviteGrantValid_SelfGrantIgnored(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	minted := testKey(t)

	// Minted key tries to grant ITSELF contributor — not owner-rooted, so
	// DeriveLevels' escalation cap ignores it (a non-author, non-maintainer signer
	// may grant nothing).
	selfGrant := grant(t, minted, ba, minted.PubKeyHex(), RoleContributor, 0, 1000)
	if InviteGrantValid([]*nostr.Event{selfGrant}, ba, testBoardD, minted.PubKeyHex()) {
		t.Error("a self-signed (non-owner-rooted) grant must not validate the key")
	}
}
