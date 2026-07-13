// Deterministic unit tests for the self-mint invite substrate (ready-ce0):
// the grant-presence liveness gate (InviteGrantValid) and the single-use
// claim-nonce binding enforced at grant derivation (ClaimGrantee / deriveGrants).
// Every event is REAL (schnorr-signed + re-verified); no network, no clock.
package sync

import (
	"testing"

	"github.com/campfire-net/ready/pkg/nostr"
)

// claimGrant builds+signs a 39301 grant that consumes an invite claim-nonce.
func claimGrant(t *testing.T, signer *nostr.Key, boardAuthor, grantee, role, claim string, createdAt int64) *nostr.Event {
	t.Helper()
	e, err := BuildRoleGrantEvent(signer, RoleGrantSpec{
		BoardD:      testBoardD,
		BoardAuthor: boardAuthor,
		Grantee:     grantee,
		Role:        role,
		Claim:       claim,
	}, createdAt)
	if err != nil {
		t.Fatalf("BuildRoleGrantEvent(claim): %v", err)
	}
	return e
}

// TestInviteGrantValid_OwnerRootedContributor: a real owner-signed contributor
// grant makes the self-minted key valid; an ungranted key and a foreign-board grant
// do not.
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

// TestInviteGrantValid_SelfGrantIgnored proves a self-signed grant by the minted key
// itself (not owner-rooted) does not validate it — the escalation cap ignores it.
func TestInviteGrantValid_SelfGrantIgnored(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	minted := testKey(t)

	selfGrant := grant(t, minted, ba, minted.PubKeyHex(), RoleContributor, 0, 1000)
	if InviteGrantValid([]*nostr.Event{selfGrant}, ba, testBoardD, minted.PubKeyHex()) {
		t.Error("a self-signed (non-owner-rooted) grant must not validate the key")
	}
}

// TestClaimSingleUse_OneClaimNonceOneGrantee is the ready-ce0 security-property (c)
// proof at the derivation seam: two DIFFERENT self-minted keys both present the SAME
// claim-nonce, and the owner (accidentally, e.g. a leaked claim) signs a grant for
// EACH. Derivation binds the claim to the FIRST grantee only; the SECOND grant is
// ignored — the second key is NOT admitted. Single-use is REAL and owner-enforced,
// not a relay marker.
func TestClaimSingleUse_OneClaimNonceOneGrantee(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	first := testKey(t)
	second := testKey(t)
	const claim = "claim-nonce-xyz"

	// The owner signs both grants; the FIRST (older created_at) binds the claim.
	events := []*nostr.Event{
		claimGrant(t, owner, ba, first.PubKeyHex(), RoleContributor, claim, 1000),
		claimGrant(t, owner, ba, second.PubKeyHex(), RoleContributor, claim, 1001),
	}

	levels, _ := DeriveLevels(events, ba, testBoardD)
	if lvl, ok := levels[first.PubKeyHex()]; !ok || lvl != LevelContributor {
		t.Errorf("first grantee must be admitted at contributor (claim binds to it), got lvl=%d ok=%v", lvl, ok)
	}
	if _, ok := levels[second.PubKeyHex()]; ok {
		t.Error("second grantee reusing the SAME claim-nonce must NOT be admitted (single-use)")
	}

	// ClaimGrantee reports the binding for the owner's client-side fail-fast check.
	bound, ok := ClaimGrantee(events, ba, testBoardD, claim)
	if !ok || bound != first.PubKeyHex() {
		t.Errorf("ClaimGrantee = (%s,%v), want first grantee bound", bound, ok)
	}
	if _, ok := ClaimGrantee(events, ba, testBoardD, "unused-nonce"); ok {
		t.Error("an unused claim-nonce must report no binding")
	}
}

// TestClaimSingleUse_SameGranteeMayReclaim: the SAME grantee re-granting under its own
// claim (e.g. a later revoke of that key) is NOT a single-use violation — the guard
// fires only on a grantee MISMATCH.
func TestClaimSingleUse_SameGranteeMayReclaim(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	g := testKey(t)
	const claim = "claim-nonce-reuse"

	events := []*nostr.Event{
		claimGrant(t, owner, ba, g.PubKeyHex(), RoleContributor, claim, 1000),
		claimGrant(t, owner, ba, g.PubKeyHex(), RoleRevoked, claim, 1001),
	}
	levels, _ := DeriveLevels(events, ba, testBoardD)
	if lvl, ok := levels[g.PubKeyHex()]; !ok || lvl != LevelRevoked {
		t.Errorf("same-grantee re-grant should apply latest (revoked), got lvl=%d ok=%v", lvl, ok)
	}
}
