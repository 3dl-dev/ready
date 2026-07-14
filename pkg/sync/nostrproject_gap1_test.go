// Deterministic projection proofs for GAP-1 (ready-7c1): the client read-trust
// membership set is fed by DeriveLevels (grant-derived), NOT only by hand-maintained
// Config.TrustedPubkeys. "One signed source feeds everything."
//
// Every event is REAL: built + schnorr-signed via the wire builders and re-verified
// inside ProjectItems. Assertions check projected item STATE — not err==nil. The
// three required proofs (item ready-7c1):
//
//	(a) TestGAP1_GrantedContributorAdmittedWithoutConfig — an owner-granted contributor
//	    ABSENT from Trusted/rd.json has its events ADMITTED + projected (was dropped).
//	(b) TestGAP1_UngrantedForeignKeyStillDropped — fail-closed: an ungranted foreign key
//	    (on the pinned board, so the pin does NOT reject it) is STILL dropped by read-trust.
//	(c) TestGAP1_RevokeDropsReadTrustProspectivelyNoConfig — a role=revoked grant drops
//	    the key from read-trust for FUTURE events (prospective) WITHOUT touching config;
//	    the completed item does not reopen (BONUS: ready-213 / NOTE-A on the read side).
package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// cardOnBoard builds+signs a 30302 card whose board-membership "a" coordinate is the
// OWNER's board (BoardAuthor=owner), so an AGENT-signed card is accepted by the pin
// (BP-4 decoupling) and the ONLY thing that can drop it is read-trust.
func cardOnBoard(t *testing.T, signer *nostr.Key, owner, itemID, status string, ts int64) *nostr.Event {
	t.Helper()
	e, err := BuildCardEvent(signer, CardSpec{
		ItemID: itemID, Title: itemID, Status: status, Type: "task",
		BoardD: testBoardD, BoardAuthor: owner,
	}, ts)
	if err != nil {
		t.Fatalf("build card: %v", err)
	}
	return e
}

// (a) An owner-signed contributor grant admits that contributor's own events at
// projection EVEN THOUGH the contributor is absent from the Trusted set (rd.json).
// Before GAP-1 read-trust was fed ONLY by Config.TrustedPubkeys, so the grant alone
// was insufficient and the contributor's card was dropped — the item never appeared.
func TestGAP1_GrantedContributorAdmittedWithoutConfig(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	contrib := testKey(t)
	pin := BoardCoord(ba, testBoardD)
	const itemID = "ready-gap1-a"

	grantContrib := grant(t, owner, ba, contrib.PubKeyHex(), RoleContributor, 0, 100)
	card := cardOnBoard(t, contrib, ba, itemID, state.StatusActive, 1000)
	status := mustStatus(t, contrib, itemID, state.StatusActive, card.ID, 1000)

	// Trusted contains ONLY the owner — the contributor is NOT in rd.json.
	trust := map[string]bool{ba: true}

	// WITH the grant: the contributor's events are admitted by grant-derived read-trust.
	withGrant := ProjectItems([]*nostr.Event{grantContrib, card, status}, ProjectOptions{Trusted: trust, PinnedBoard: pin})
	it := withGrant[itemID]
	if it == nil {
		t.Fatal("granted contributor's item was NOT projected — grant-derived read-trust did not admit it (GAP-1 not fixed)")
	}
	if it.Status != state.StatusActive {
		t.Fatalf("item status = %q, want active", it.Status)
	}

	// CONTROL: WITHOUT the grant, the same contributor (still absent from Trusted) is
	// dropped and the item never appears — isolating the grant as the cause of admission.
	noGrant := ProjectItems([]*nostr.Event{card, status}, ProjectOptions{Trusted: trust, PinnedBoard: pin})
	if noGrant[itemID] != nil {
		t.Fatalf("control: an UNGRANTED key absent from Trusted must be dropped, but the item projected: %+v", noGrant[itemID])
	}
}

// (b) Fail-closed: an ungranted foreign key publishing a card ON THE PINNED BOARD
// (so BP-3's board pin does not reject it) is STILL dropped by read-trust. This proves
// GAP-1 did not weaken the gate — only owner-rooted grants + self + config admit.
func TestGAP1_UngrantedForeignKeyStillDropped(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	foreign := testKey(t) // never granted, not in Trusted
	pin := BoardCoord(ba, testBoardD)
	const itemID = "ready-gap1-b"

	// The foreign card is on the OWNER's board coordinate (a == pin), so the pin passes
	// it — the ONLY gate left is read-trust.
	card := cardOnBoard(t, foreign, ba, itemID, state.StatusActive, 1000)
	status := mustStatus(t, foreign, itemID, state.StatusActive, card.ID, 1000)

	trust := map[string]bool{ba: true} // owner only; foreign absent, ungranted.
	items := ProjectItems([]*nostr.Event{card, status}, ProjectOptions{Trusted: trust, PinnedBoard: pin})
	if items[itemID] != nil {
		t.Fatalf("fail-closed violated: an ungranted foreign key's item was projected: %+v", items[itemID])
	}

	// CONTROL: the SAME foreign key, once owner-granted, IS admitted — proving the drop
	// is the missing grant, not something incidental about the key/card.
	g := grant(t, owner, ba, foreign.PubKeyHex(), RoleContributor, 0, 100)
	granted := ProjectItems([]*nostr.Event{g, card, status}, ProjectOptions{Trusted: trust, PinnedBoard: pin})
	if granted[itemID] == nil {
		t.Fatal("control: once granted, the key's item must project (isolates the missing grant as the cause)")
	}
}

// (c) A role=revoked grant drops the key from read-trust for FUTURE events without any
// config change (the contributor is never in Trusted), while its PAST authoritative
// events are preserved — a completed item does NOT reopen. This is the read-side
// revocation the BONUS (ready-213 / NOTE-A) asks for: revoke via a signed grant, not
// by stripping rd.json.
func TestGAP1_RevokeDropsReadTrustProspectivelyNoConfig(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	agent := testKey(t) // granted then revoked; NEVER in Trusted/rd.json
	pin := BoardCoord(ba, testBoardD)
	const itemID = "ready-gap1-c"

	grantAgent := grant(t, owner, ba, agent.PubKeyHex(), RoleContributor, 0, 100)
	card := cardOnBoard(t, agent, ba, itemID, state.StatusActive, 1000)
	active := mustStatus(t, agent, itemID, state.StatusActive, card.ID, 1000)
	done := mustStatus(t, agent, itemID, state.StatusDone, card.ID, 2000)     // past, kept
	reopen := mustStatus(t, agent, itemID, state.StatusActive, card.ID, 6000) // post-revoke, dropped
	revoke := grant(t, owner, ba, agent.PubKeyHex(), RoleRevoked, 5000, 5100) // from=5000

	trust := map[string]bool{ba: true} // owner only — the agent is NOT in config.

	items := ProjectItems([]*nostr.Event{grantAgent, card, active, done, reopen, revoke},
		ProjectOptions{Trusted: trust, PinnedBoard: pin})
	it := items[itemID]
	if it == nil {
		t.Fatal("the agent's completed item vanished — prospective revoke must preserve its PAST events (config was never touched)")
	}
	if it.Status != state.StatusDone {
		t.Fatalf("completed item reopened after a read-side revoke: got %q want done", it.Status)
	}
	// The post-revoke reopen@6000 must NOT be in history; the past done@2000 must be.
	sawDone, sawReopen := false, false
	for _, h := range it.History {
		if h.ToStatus == state.StatusDone {
			sawDone = true
		}
		if h.Timestamp != "" && h.ToStatus == state.StatusActive && h.FromStatus == state.StatusDone {
			sawReopen = true
		}
	}
	if !sawDone {
		t.Error("the pre-revoke 'done' was erased — revoke was not prospective")
	}
	if sawReopen {
		t.Error("the post-revoke reopen was applied — read-side revoke did not drop the future event")
	}
}
