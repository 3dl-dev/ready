package state_test

import (
	"testing"

	msgrec "github.com/3dl-dev/ready/pkg/msgrec"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestDerive_Delegate verifies that a work:delegate message sets the By field.
// Replays real msgrec.MessageRecord slices per test requirements.
func TestDerive_Delegate(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-delegate-1", []string{"work:delegate", "work:by:alice@3dl.dev"}, map[string]interface{}{
			"target": "msg-create-1",
			"to":     "alice@3dl.dev",
			"reason": "Alice owns this domain",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.By != "alice@3dl.dev" {
		t.Errorf("expected by=alice@3dl.dev after delegate, got %q", item.By)
	}
	// Delegate does not change status — item remains inbox.
	if item.Status != state.StatusInbox {
		t.Errorf("expected status inbox after delegate (no claim yet), got %q", item.Status)
	}
}

// TestDerive_DelegateThenClaim verifies that claim after delegate sets sender as by
// and transitions to active.
func TestDerive_DelegateThenClaim(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-delegate-1", []string{"work:delegate", "work:by:alice@3dl.dev"}, map[string]interface{}{
			"target": "msg-create-1",
			"to":     "alice@3dl.dev",
		}, []string{"msg-create-1"}, ts+1000),
		makeMsg("msg-claim-1", []string{"work:claim"}, map[string]interface{}{
			"target": "msg-create-1",
			"reason": "Accepting delegation",
		}, []string{"msg-create-1"}, ts+2000),
	}
	msgs[2].Sender = "alice-pubkey-hex"

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Claim sets by=sender (overriding the delegate's to field).
	if item.By != "alice-pubkey-hex" {
		t.Errorf("expected by=alice-pubkey-hex after claim, got %q", item.By)
	}
	if item.Status != state.StatusActive {
		t.Errorf("expected status active after claim, got %q", item.Status)
	}
}

// TestDerive_UpdateTitle verifies that work:update can change an item's title.
func TestDerive_UpdateTitle(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Original Title", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
		}, nil, ts),
		makeMsg("msg-update-1", []string{"work:update"}, map[string]interface{}{
			"target": "msg-create-1",
			"title":  "Updated Title",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Title != "Updated Title" {
		t.Errorf("expected title='Updated Title', got %q", item.Title)
	}
}

// TestDerive_UpdateLevel verifies that work:update can change an item's level.
func TestDerive_UpdateLevel(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
			"level": "epic",
		}, nil, ts),
		makeMsg("msg-update-1", []string{"work:update"}, map[string]interface{}{
			"target": "msg-create-1",
			"level":  "task",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Level != "task" {
		t.Errorf("expected level=task after update, got %q", item.Level)
	}
}

// TestDerive_UpdatePriorityAndETA verifies that work:update can change priority and ETA.
// This is the primary use case from the done condition:
//   rd update <id> --priority p0 --eta 2026-04-01
func TestDerive_UpdatePriorityAndETA(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p3",
		}, nil, ts),
		makeMsg("msg-update-1", []string{"work:update"}, map[string]interface{}{
			"target":   "msg-create-1",
			"priority": "p0",
			"eta":      "2026-04-01T00:00:00Z",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Priority != "p0" {
		t.Errorf("expected priority=p0, got %q", item.Priority)
	}
	if item.ETA != "2026-04-01T00:00:00Z" {
		t.Errorf("expected ETA=2026-04-01T00:00:00Z, got %q", item.ETA)
	}
}

// TestDerive_UpdatePreservesUnchangedFields verifies that work:update only changes
// the specified fields and leaves others intact.
func TestDerive_UpdatePreservesUnchangedFields(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Original", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
			"context": "Original context",
		}, nil, ts),
		makeMsg("msg-update-1", []string{"work:update"}, map[string]interface{}{
			"target":   "msg-create-1",
			"priority": "p0",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Priority changed.
	if item.Priority != "p0" {
		t.Errorf("expected priority=p0, got %q", item.Priority)
	}
	// Title and context unchanged.
	if item.Title != "Original" {
		t.Errorf("expected title unchanged='Original', got %q", item.Title)
	}
	if item.Context != "Original context" {
		t.Errorf("expected context unchanged, got %q", item.Context)
	}
	// For unchanged.
	if item.For != "baron@3dl.dev" {
		t.Errorf("expected for unchanged=baron@3dl.dev, got %q", item.For)
	}
}

// TestDerive_ClaimWithAntecedents verifies that claim resolves via antecedents
// when target is not in payload (fallback path in state derivation).
func TestDerive_ClaimWithAntecedents(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Claim with empty target field — should resolve via antecedents.
		makeMsg("msg-claim-1", []string{"work:claim"}, map[string]interface{}{
			"target": "", // empty — fallback to antecedents
		}, []string{"msg-create-1"}, ts+1000),
	}
	msgs[1].Sender = "agent-abc"

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusActive {
		t.Errorf("expected active, got %q", item.Status)
	}
	if item.By != "agent-abc" {
		t.Errorf("expected by=agent-abc, got %q", item.By)
	}
}
