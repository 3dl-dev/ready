// Deterministic replay proofs for BP-3 (ready-1dc): revocation takes effect at
// PROJECTION, prospectively; the never-revokes board-maintainer union bug is gone;
// the board is pinned so a foreign-'a' card cannot self-escalate.
//
// Every event here is REAL: built + schnorr-signed via the wire builders and
// re-verified inside ProjectItems. Assertions check projected item STATE and
// history — not err==nil. Design: docs/design/nostr-identity-model.md §3/§4.
//
// The four required proofs (item ready-1dc):
//
//	(a) TestBP3_BoardRepublishRevokesMaintainer   — union bug gone (latest board wins)
//	(b) TestBP3_RevokeDropsFutureEvents           — prospective: future events drop
//	(c) TestBP3_CompletedItemDoesNotReopen         — prospective: past work preserved
//	(d) TestBP3_ForeignBoardCardRejected           — pinning kills parallel-board self-escalation
//
// plus one non-vacuous test that the level>=2 grant FOLD actually confers status
// authority (item DONE #2: "status-authority Maintainers from {level>=2}"):
//
//	(e) TestBP3_GrantedMaintainerGetsStatusAuthority
package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// mustCard (BoardD "ready", so a card's "a" coordinate is "30301:<k>:ready") is
// declared in negentropy_test.go and reused here.

// mustBoardAt builds+signs a 30301 board with an explicit created_at so successive
// republishes have distinct latest-wins order (mustBoard hardcodes ts=1).
func mustBoardAt(t *testing.T, author *nostr.Key, boardD string, ts int64, maintainers ...string) *nostr.Event {
	t.Helper()
	e, err := BuildBoardEvent(author, BoardSpec{BoardD: boardD, Title: boardD, Maintainers: maintainers}, ts)
	if err != nil {
		t.Fatalf("build board@%d: %v", ts, err)
	}
	return e
}

// (a) The pre-BP-3 code UNIONED the "p" tags of ALL historical board events, so a
// maintainer named once stayed a maintainer forever — the board could never be
// republished to REVOKE authority. BP-3 keeps only the NEWEST board per coordinate.
// Here board v1 (t=100) names M a maintainer; board v2 (t=200) drops M. With BOTH
// boards in the event set, M's later "done" MUST be non-authoritative — the item
// keeps the author's "active". Under the union bug M would win and the item would be
// "done"; that is the regression this test locks out.
func TestBP3_BoardRepublishRevokesMaintainer(t *testing.T) {
	owner := testKey(t)
	m := testKey(t)
	if owner.PubKeyHex() == m.PubKeyHex() {
		t.Fatal("keys collided")
	}
	const itemID = "ready-bp3-a"

	boardV1 := mustBoardAt(t, owner, "ready", 100, owner.PubKeyHex(), m.PubKeyHex())
	boardV2 := mustBoardAt(t, owner, "ready", 200, owner.PubKeyHex()) // M dropped
	card := mustCard(t, owner, itemID, state.StatusActive, 1000)
	ownerStatus := mustStatus(t, owner, itemID, state.StatusActive, card.ID, 1000)
	mDone := mustStatus(t, m, itemID, state.StatusDone, card.ID, 2000)

	trust := map[string]bool{owner.PubKeyHex(): true, m.PubKeyHex(): true}
	events := []*nostr.Event{boardV1, boardV2, card, ownerStatus, mDone}
	items := ProjectItems(events, ProjectOptions{Trusted: trust})

	it := items[itemID]
	if it == nil {
		t.Fatal("item missing from projection")
	}
	if it.Status != state.StatusActive {
		t.Fatalf("union-never-revokes bug: republished board dropped M, but M's 'done' still won: got %q want %q", it.Status, state.StatusActive)
	}
	for _, h := range it.History {
		if h.ChangedBy == m.PubKeyHex() {
			t.Fatalf("revoked maintainer M leaked into history: %+v", h)
		}
	}

	// CONTROL: with ONLY board v1 (M still a maintainer), M's done IS authoritative.
	// Proves the drop is caused by the republish, not by M being intrinsically weak.
	itemsV1 := ProjectItems([]*nostr.Event{boardV1, card, ownerStatus, mDone}, ProjectOptions{Trusted: trust})
	if got := itemsV1[itemID].Status; got != state.StatusDone {
		t.Fatalf("control: M is a maintainer under board v1, its done must win: got %q want %q", got, state.StatusDone)
	}
}

// (b) A role=revoked grant time-bounds the key's read-trust (authoritative-until).
// An event created AT/AFTER the revoke effective time is dropped at projection. Here
// the owner repudiates itself from T=1500 (owner-key rotation / compromise, design
// §3); its "done" at t=2000 (>= 1500) drops, so the item keeps its pre-revoke
// "active". A control WITHOUT the revoke shows the done would otherwise win — proving
// the revoke is causal.
func TestBP3_RevokeDropsFutureEvents(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	pin := BoardCoord(ba, "ready")
	const itemID = "ready-bp3-b"

	card := mustCard(t, owner, itemID, state.StatusActive, 1000)
	active := mustStatus(t, owner, itemID, state.StatusActive, card.ID, 1000)
	done := mustStatus(t, owner, itemID, state.StatusDone, card.ID, 2000) // post-revoke
	// Owner repudiates its own key from T=1500 (prospective boundary before the
	// suspected exposure). from=1500, published at created_at=1600.
	revoke := grant(t, owner, ba, ba, RoleRevoked, 1500, 1600)

	trust := map[string]bool{ba: true}

	// With the revoke: done@2000 (>=1500) is dropped, item stays active. card@1000 and
	// active@1000 (<1500) survive — the past is NOT erased.
	withRevoke := ProjectItems([]*nostr.Event{card, active, done, revoke}, ProjectOptions{Trusted: trust, PinnedBoard: pin})
	it := withRevoke[itemID]
	if it == nil {
		t.Fatal("item vanished — past events (card/active) must survive a prospective revoke")
	}
	if it.Status != state.StatusActive {
		t.Fatalf("post-revoke 'done' was NOT dropped from read-trust: got %q want %q", it.Status, state.StatusActive)
	}

	// CONTROL: no revoke -> done@2000 wins.
	noRevoke := ProjectItems([]*nostr.Event{card, active, done}, ProjectOptions{Trusted: trust, PinnedBoard: pin})
	if got := noRevoke[itemID].Status; got != state.StatusDone {
		t.Fatalf("control: without the revoke the done must win: got %q want %q", got, state.StatusDone)
	}
}

// (c) Prospective revocation preserves history: a COMPLETED item does NOT reopen when
// its past author is later revoked. The owner completes the item at t=2000, then is
// revoked from T=5000; a later reopen attempt at t=6000 (>= 5000) is dropped, while
// the earlier "done" (< 5000) is retained. The item stays done. A current-snapshot
// revoke would erase ALL the owner's events and lose the item entirely — the bug A1
// rules out.
func TestBP3_CompletedItemDoesNotReopen(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	pin := BoardCoord(ba, "ready")
	const itemID = "ready-bp3-c"

	card := mustCard(t, owner, itemID, state.StatusActive, 1000)
	active := mustStatus(t, owner, itemID, state.StatusActive, card.ID, 1000)
	done := mustStatus(t, owner, itemID, state.StatusDone, card.ID, 2000)     // past, kept
	reopen := mustStatus(t, owner, itemID, state.StatusActive, card.ID, 6000) // post-revoke, dropped
	revoke := grant(t, owner, ba, ba, RoleRevoked, 5000, 5100)

	trust := map[string]bool{ba: true}
	items := ProjectItems([]*nostr.Event{card, active, done, reopen, revoke}, ProjectOptions{Trusted: trust, PinnedBoard: pin})

	it := items[itemID]
	if it == nil {
		t.Fatal("completed item vanished — prospective revoke must preserve past events")
	}
	if it.Status != state.StatusDone {
		t.Fatalf("completed item REOPENED after its past author was revoked: got %q want %q", it.Status, state.StatusDone)
	}
	// The past done must still be present in history; the post-revoke reopen must not.
	sawDone := false
	for _, h := range it.History {
		if h.Timestamp == "" {
			continue
		}
		if h.ToStatus == state.StatusDone {
			sawDone = true
		}
	}
	if !sawDone {
		t.Fatal("the pre-revoke 'done' transition was erased from history (not prospective)")
	}
	if n := len(it.History); n != 2 { // active@1000, done@2000 — reopen@6000 dropped
		t.Fatalf("expected exactly 2 retained transitions (active, done), got %d: %+v", n, it.History)
	}
}

// (d) With a pinned board, a card whose "a" coordinate is not the pinned board is
// REJECTED — the parallel-board self-escalation path (a relay-admitted key forks its
// own 30301, self-grants maintainer, publishes cards under its own "a"). Here the
// attacker X is fully read-trusted yet its card on its OWN board is dropped, while the
// owner's card on the pinned board projects normally.
func TestBP3_ForeignBoardCardRejected(t *testing.T) {
	owner := testKey(t)
	attacker := testKey(t)
	if owner.PubKeyHex() == attacker.PubKeyHex() {
		t.Fatal("keys collided")
	}
	pin := BoardCoord(owner.PubKeyHex(), "ready")
	const legitID = "ready-bp3-d-legit"
	const evilID = "ready-bp3-d-evil"

	legitCard := mustCard(t, owner, legitID, state.StatusActive, 1000)
	legitStatus := mustStatus(t, owner, legitID, state.StatusActive, legitCard.ID, 1000)
	// Attacker builds a card on its OWN board coordinate "30301:<attacker>:ready".
	evilCard := mustCard(t, attacker, evilID, state.StatusActive, 1000)
	evilStatus := mustStatus(t, attacker, evilID, state.StatusActive, evilCard.ID, 1000)

	// Both keys pass READ-trust (attacker is relay-admitted); the pin is the only
	// thing standing between the attacker and a self-owned parallel board.
	trust := map[string]bool{owner.PubKeyHex(): true, attacker.PubKeyHex(): true}
	events := []*nostr.Event{legitCard, legitStatus, evilCard, evilStatus}
	items := ProjectItems(events, ProjectOptions{Trusted: trust, PinnedBoard: pin})

	if items[legitID] == nil {
		t.Fatal("owner's card on the pinned board was rejected")
	}
	if items[evilID] != nil {
		t.Fatalf("foreign-board card was NOT rejected: parallel-board self-escalation is open (%+v)", items[evilID])
	}

	// CONTROL: WITHOUT a pin, the foreign card projects (pre-BP-3 behaviour) — proving
	// the rejection is the pin's doing, not some incidental drop.
	unpinned := ProjectItems(events, ProjectOptions{Trusted: trust})
	if unpinned[evilID] == nil {
		t.Fatal("control: without a pin the foreign card should project (isolates the pin as the cause)")
	}
}

// (e) The level>=2 grant FOLD confers status authority (item DONE #2: status-authority
// Maintainers derived from {level>=2} via DeriveLevels). A key granted role=maintainer
// by the board owner can author an authoritative status transition on the OWNER's card;
// without the grant the same transition is non-authoritative. This keeps the fold
// non-vacuous and is the projection-side dependency BP-5's `rd grant maintainer` needs.
func TestBP3_GrantedMaintainerGetsStatusAuthority(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	agent := testKey(t)
	pin := BoardCoord(ba, "ready")
	const itemID = "ready-bp3-e"

	card := mustCard(t, owner, itemID, state.StatusActive, 1000)
	ownerStatus := mustStatus(t, owner, itemID, state.StatusActive, card.ID, 1000)
	agentDone := mustStatus(t, agent, itemID, state.StatusDone, card.ID, 2000)
	grantMaint := grant(t, owner, ba, agent.PubKeyHex(), RoleMaintainer, 0, 100)

	trust := map[string]bool{ba: true, agent.PubKeyHex(): true}

	// WITH the maintainer grant: the agent's done is authoritative on the owner's card.
	granted := ProjectItems([]*nostr.Event{card, ownerStatus, agentDone, grantMaint}, ProjectOptions{Trusted: trust, PinnedBoard: pin})
	if got := granted[itemID].Status; got != state.StatusDone {
		t.Fatalf("granted maintainer's status must be authoritative: got %q want %q", got, state.StatusDone)
	}

	// WITHOUT the grant: the agent is a bare trusted key, not a maintainer -> its done
	// is dropped, the item stays active.
	ungranted := ProjectItems([]*nostr.Event{card, ownerStatus, agentDone}, ProjectOptions{Trusted: trust, PinnedBoard: pin})
	if got := ungranted[itemID].Status; got != state.StatusActive {
		t.Fatalf("without a maintainer grant the agent must NOT be authoritative: got %q want %q", got, state.StatusActive)
	}
}
