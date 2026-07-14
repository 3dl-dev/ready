// Deterministic unit tests for ready-d53: the read-side web-of-trust gate.
//
// nostr's Event.Verify() proves an event is internally consistent (id + schnorr
// sig) but NOT that its author is AUTHORIZED to write — any generated key produces
// events that Verify. These tests prove that at PROJECTION (replay) time, rd drops
// every event whose author is not in the trusted allowlist, so an untrusted key
// can never influence projected work-item state — even a forged event that already
// sits in the local log (defence in depth with the ingestion gate in reconcile()).
//
// The live-relay half (an untrusted event published to a real strfry relay is
// dropped at INGESTION and never poisons the local authoritative log) is proven by
// TestLiveRelay_TrustGate in nostr_live_relay_test.go (env-gated RD_NOSTR_LIVE_RELAY=1)
// and by scripts/demo_nostr_trust.sh.
package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestProjection_TrustGate_DropsUntrustedTakeover is the core proof. A TRUSTED
// author creates an item. An UNTRUSTED key then publishes, for the SAME item id, a
// LATER card (higher created_at, so it would win the latest-wins projection) that
// flips the title/priority AND a matching done status event. Because a winning
// card's author is treated as the item author for status authority, an ungated
// projection would let the attacker both rewrite the card AND close the item — a
// full state takeover. With the trust gate the untrusted events are dropped and the
// item still reflects only the trusted author's state.
func TestProjection_TrustGate_DropsUntrustedTakeover(t *testing.T) {
	trusted := testKey(t)
	attacker := testKey(t)
	if trusted.PubKeyHex() == attacker.PubKeyHex() {
		t.Fatal("test keys collided")
	}
	itemID := "ready-d53-takeover"

	// Trusted author: create + claim the item (active, p1).
	tc, err := BuildCardEvent(trusted, CardSpec{ItemID: itemID, Title: "legit", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready"}, 1700000000)
	if err != nil {
		t.Fatalf("trusted card: %v", err)
	}
	ts, err := BuildStatusEvent(trusted, itemID, state.StatusActive, tc.ID, "", 1700000000)
	if err != nil {
		t.Fatalf("trusted status: %v", err)
	}

	// Attacker: LATER forged card (would win latest-wins) + forged done status.
	ac, err := BuildCardEvent(attacker, CardSpec{ItemID: itemID, Title: "HIJACKED", Status: state.StatusDone, Priority: "p0", Type: "task", BoardD: "ready"}, 1700009999)
	if err != nil {
		t.Fatalf("attacker card: %v", err)
	}
	as, err := BuildStatusEvent(attacker, itemID, state.StatusDone, ac.ID, "seized", 1700009999)
	if err != nil {
		t.Fatalf("attacker status: %v", err)
	}

	events := []*nostr.Event{tc, ts, ac, as}
	trustSet := map[string]bool{trusted.PubKeyHex(): true}

	// --- GATED: attacker events dropped, trusted state preserved. ---
	gated := ProjectItems(events, ProjectOptions{Maintainers: trustSet, Trusted: trustSet})
	it, ok := gated[itemID]
	if !ok {
		t.Fatal("trusted item vanished under the gate")
	}
	if it.Title != "legit" {
		t.Errorf("title = %q, want %q — untrusted card influenced projected state", it.Title, "legit")
	}
	if it.Priority != "p1" {
		t.Errorf("priority = %q, want p1 — untrusted card influenced projected state", it.Priority)
	}
	if it.Status != state.StatusActive {
		t.Errorf("status = %q, want active — untrusted done status was applied (takeover not prevented)", it.Status)
	}
	if it.MsgID != tc.ID {
		t.Errorf("winning card id = %q, want the trusted card %q", it.MsgID, tc.ID)
	}
	// The forged done status must not leak into the audit trail either.
	for _, h := range it.History {
		if h.ChangedBy == attacker.PubKeyHex() {
			t.Errorf("attacker %s appears in projected history: %+v", attacker.PubKeyHex(), h)
		}
	}

	// --- UNGATED (Trusted=nil) documents the vulnerability the gate closes. ---
	ungated := ProjectItems(events, ProjectOptions{Maintainers: trustSet})
	if ung := ungated[itemID]; ung == nil || ung.Title != "HIJACKED" {
		t.Fatalf("expected the ungated projection to be taken over (title HIJACKED) to prove the gate is load-bearing; got %+v", ung)
	}
}

// TestProjection_TrustGate_AppliesTrustedEvent proves the gate is not over-broad:
// a trusted-author (self) item projects normally, and an admitted maintainer's
// events are applied when that maintainer is in the trust set.
func TestProjection_TrustGate_AppliesTrustedEvent(t *testing.T) {
	self := testKey(t)
	maintainer := testKey(t)
	itemID := "ready-d53-apply"

	// Self creates the item.
	c0, err := BuildCardEvent(self, CardSpec{ItemID: itemID, Title: "v1", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready"}, 1700000000)
	if err != nil {
		t.Fatalf("self card: %v", err)
	}
	s0, err := BuildStatusEvent(self, itemID, state.StatusActive, c0.ID, "", 1700000000)
	if err != nil {
		t.Fatalf("self status: %v", err)
	}
	// An ADMITTED maintainer closes it with a reason.
	c1, err := BuildCardEvent(self, CardSpec{ItemID: itemID, Title: "v1", Status: state.StatusDone, Priority: "p1", Type: "task", BoardD: "ready"}, 1700000100)
	if err != nil {
		t.Fatalf("close card: %v", err)
	}
	ms, err := BuildStatusEvent(maintainer, itemID, state.StatusDone, c1.ID, "closed by maintainer", 1700000100)
	if err != nil {
		t.Fatalf("maintainer status: %v", err)
	}

	events := []*nostr.Event{c0, s0, c1, ms}
	// Trust set admits BOTH self and the maintainer.
	trustSet := map[string]bool{self.PubKeyHex(): true, maintainer.PubKeyHex(): true}
	items := ProjectItems(events, ProjectOptions{Maintainers: trustSet, Trusted: trustSet})
	it, ok := items[itemID]
	if !ok {
		t.Fatal("item not projected")
	}
	if it.Status != state.StatusDone {
		t.Errorf("status = %q, want done — the admitted maintainer's close was not applied", it.Status)
	}
	var sawClose bool
	for _, h := range it.History {
		if h.Note == "closed by maintainer" && h.ChangedBy == maintainer.PubKeyHex() {
			sawClose = true
		}
	}
	if !sawClose {
		t.Errorf("admitted maintainer's close-with-reason missing from history: %+v", it.History)
	}
}

// TestProjection_TrustGate_DropsUntrustedNewItem proves an untrusted key cannot
// even CREATE a phantom item: a card authored solely by an untrusted key projects
// to nothing under the gate.
func TestProjection_TrustGate_DropsUntrustedNewItem(t *testing.T) {
	trusted := testKey(t)
	attacker := testKey(t)
	itemID := "ready-d53-phantom"

	ac, err := BuildCardEvent(attacker, CardSpec{ItemID: itemID, Title: "phantom", Status: state.StatusActive, Priority: "p0", Type: "task", BoardD: "ready"}, 1700000000)
	if err != nil {
		t.Fatalf("attacker card: %v", err)
	}
	trustSet := map[string]bool{trusted.PubKeyHex(): true}
	items := ProjectItems([]*nostr.Event{ac}, ProjectOptions{Maintainers: trustSet, Trusted: trustSet})
	if _, ok := items[itemID]; ok {
		t.Fatalf("untrusted key created a phantom item %q under the trust gate", itemID)
	}
}
