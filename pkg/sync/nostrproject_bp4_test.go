// BP-4 (ready-295) reconciliation proofs: an owner and a named AGENT on one host sign
// with DISTINCT keys and are attributed distinctly, AND an agent-signed card carries
// the OWNER's board coordinate so it PROJECTS under the BP-3 pin set to that board.
//
// Every event is REAL — built + schnorr-signed via the wire builders and re-verified
// inside ProjectItems. Assertions check projected STATE / attribution and the pin
// accept/reject boundary, never err==nil. Design: docs/design/nostr-identity-model.md
// §2/§4 (the CRITICAL card-authorship vs board-membership decoupling).
//
// Proofs (item ready-295):
//
//	(b) TestBP4_TwoActorsDistinctPubkeys        — two actors -> events with DIFFERENT pubkeys
//	(c)/(e) TestBP4_AgentCardProjectsUnderOwnerPin — agent card w/ owner coord projects; ChangedBy = agent
//	control TestBP4_AgentCardWithOwnCoordRejectedByPin — the naive (signer-coord) card is REJECTED
package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// agentCard builds+signs a 30302 card whose board-MEMBERSHIP coordinate points at
// boardAuthor (the owner) while the card is AUTHORED (signed) by signer — the BP-4
// decoupling. Empty boardAuthor reproduces the pre-BP-4 signer-coord behaviour.
func agentCard(t *testing.T, signer *nostr.Key, boardAuthor, itemID, status string, ts int64) *nostr.Event {
	t.Helper()
	e, err := BuildCardEvent(signer, CardSpec{
		ItemID:      itemID,
		Title:       itemID,
		Status:      status,
		Type:        "task",
		BoardD:      "ready",
		BoardAuthor: boardAuthor,
	}, ts)
	if err != nil {
		t.Fatalf("build agent card: %v", err)
	}
	return e
}

// (b) Two DURABLE actors on one host sign events with DIFFERENT pubkeys — the
// structural precondition the epic exists to answer ("which actor acted?").
func TestBP4_TwoActorsDistinctPubkeys(t *testing.T) {
	owner := testKey(t)
	agent := testKey(t)
	if owner.PubKeyHex() == agent.PubKeyHex() {
		t.Fatal("owner and agent keys collided")
	}

	ownerCard := agentCard(t, owner, "", "ready-bp4-o", state.StatusActive, 1000)
	agentEvt := agentCard(t, agent, owner.PubKeyHex(), "ready-bp4-a", state.StatusActive, 1000)

	if ownerCard.PubKey != owner.PubKeyHex() {
		t.Fatalf("owner card authored by %s, want %s", ownerCard.PubKey, owner.PubKeyHex())
	}
	if agentEvt.PubKey != agent.PubKeyHex() {
		t.Fatalf("agent card authored by %s, want %s", agentEvt.PubKey, agent.PubKeyHex())
	}
	if ownerCard.PubKey == agentEvt.PubKey {
		t.Fatal("events from two actors share a pubkey — attribution is NOT distinct")
	}
}

// (e) + (c) THE reconciliation with BP-3's pin: a card SIGNED BY THE AGENT but carrying
// the OWNER's board coordinate is a member of the owner's board, so the pin (set to the
// owner's coordinate) ACCEPTS it and the item projects. And because per-actor keys make
// authorship cryptographic, the projected history attributes the transition to the ACTING
// (agent) pubkey — ChangedBy = the signer, distinct from the owner.
func TestBP4_AgentCardProjectsUnderOwnerPin(t *testing.T) {
	owner := testKey(t)
	agent := testKey(t)
	const itemID = "ready-bp4-e"

	pin := BoardCoord(owner.PubKeyHex(), "ready") // 30301:<owner>:ready

	// Agent authors BOTH the card and its status. Board MEMBERSHIP = owner coord.
	card := agentCard(t, agent, owner.PubKeyHex(), itemID, state.StatusActive, 1000)
	if got, _ := findTag(card.Tags, "a"); got != pin {
		t.Fatalf("agent card 'a' = %q, want owner pin %q (board-membership must be the owner coord)", got, pin)
	}
	agentStatus := mustStatus(t, agent, itemID, state.StatusActive, card.ID, 1000)

	// Owner's board (pinned coordinate) provides the authority chain seed.
	board := mustBoard(t, owner, "ready")

	trust := map[string]bool{owner.PubKeyHex(): true, agent.PubKeyHex(): true}
	events := []*nostr.Event{board, card, agentStatus}
	items := ProjectItems(events, ProjectOptions{Trusted: trust, PinnedBoard: pin})

	it := items[itemID]
	if it == nil {
		t.Fatal("agent-signed card was REJECTED under the owner pin — BP-4 reconciliation broken")
	}
	if it.Status != state.StatusActive {
		t.Fatalf("projected status = %q, want %q", it.Status, state.StatusActive)
	}
	if len(it.History) != 1 {
		t.Fatalf("history len = %d, want 1", len(it.History))
	}
	// (c): the acting key is the AGENT, and per-actor keys make that cryptographically true.
	if it.History[0].ChangedBy != agent.PubKeyHex() {
		t.Fatalf("ChangedBy = %q, want the ACTING agent pubkey %q", it.History[0].ChangedBy, agent.PubKeyHex())
	}
	if it.History[0].ChangedBy == owner.PubKeyHex() {
		t.Fatal("agent transition mis-attributed to the OWNER")
	}
}

// CONTROL (why the naive BP-4 attempt failed): the SAME agent card WITHOUT the owner
// coordinate — i.e. carrying the agent's OWN board coordinate (30301:<agent>:ready), the
// pre-fix behaviour — is REJECTED by the pin. This locks in that decoupling authorship
// from membership is load-bearing: without it every agent write silently vanishes.
func TestBP4_AgentCardWithOwnCoordRejectedByPin(t *testing.T) {
	owner := testKey(t)
	agent := testKey(t)
	const itemID = "ready-bp4-ctl"

	pin := BoardCoord(owner.PubKeyHex(), "ready")

	// BoardAuthor empty => 'a' = signer's own coord (30301:<agent>:ready) != pin.
	card := agentCard(t, agent, "", itemID, state.StatusActive, 1000)
	if got, _ := findTag(card.Tags, "a"); got == pin {
		t.Fatal("test setup wrong: naive card should carry the AGENT coord, not the owner pin")
	}
	agentStatus := mustStatus(t, agent, itemID, state.StatusActive, card.ID, 1000)
	board := mustBoard(t, owner, "ready")

	trust := map[string]bool{owner.PubKeyHex(): true, agent.PubKeyHex(): true}
	events := []*nostr.Event{board, card, agentStatus}
	items := ProjectItems(events, ProjectOptions{Trusted: trust, PinnedBoard: pin})

	if _, ok := items[itemID]; ok {
		t.Fatal("a card bound to the AGENT's own board coord was accepted under the owner pin — the parallel-board path is open")
	}
}
