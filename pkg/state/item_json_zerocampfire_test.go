package state

import (
	"encoding/json"
	"testing"
)

// TestItemJSON_NostrNative_NoCampfireID locks the ready-c0b fix / ready-327
// zero-campfire invariant: the Item DTO that backs `rd list --json` and
// `rd show --json` must NOT emit a "campfire_id" field on a nostr-native item
// (where CampfireID is always ""). Before the omitempty fix it emitted
// "campfire_id": "", leaking the campfire term onto the shipped nostr surface.
func TestItemJSON_NostrNative_NoCampfireID(t *testing.T) {
	nostrItem := Item{
		ID:    "ready-abc",
		MsgID: "d4e5f6a7b8c9", // a nostr event id on a nostr-native project
		Title: "a nostr-native item",
		Type:  "task",
		// CampfireID intentionally empty — nostr-native items never carry one.
	}
	b, err := json.Marshal(nostrItem)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := m["campfire_id"]; ok {
		t.Errorf("nostr-native Item JSON must not contain campfire_id; got: %s", b)
	}
	// msg_id (the event-id operation handle) is legitimate and must remain.
	if _, ok := m["msg_id"]; !ok {
		t.Errorf("Item JSON should still carry msg_id (the event-id handle); got: %s", b)
	}
}

// TestItemJSON_Legacy_KeepsCampfireID confirms omitempty didn't drop the field
// for a legacy campfire-era item that genuinely lives in a campfire — those
// still round-trip their CampfireID.
func TestItemJSON_Legacy_KeepsCampfireID(t *testing.T) {
	legacy := Item{
		ID:         "rudi-xyz",
		MsgID:      "11112222",
		CampfireID: "fcb8730a45ca",
		Title:      "a legacy campfire item",
		Type:       "task",
	}
	b, err := json.Marshal(legacy)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if m["campfire_id"] != "fcb8730a45ca" {
		t.Errorf("legacy Item JSON should keep its campfire_id; got: %s", b)
	}
}
