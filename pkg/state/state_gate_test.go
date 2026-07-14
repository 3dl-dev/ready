package state_test

import (
	"testing"

	msgrec "github.com/3dl-dev/ready/pkg/msgrec"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestDerive_Gate verifies that work:gate transitions item to waiting
// with waiting_type=gate and sets GateMsgID.
func TestDerive_Gate(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-gate-1", []string{"work:gate", "work:gate-type:design"}, map[string]interface{}{
			"target":      "msg-create-1",
			"gate_type":   "design",
			"description": "Confirm approach before implementing",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusWaiting {
		t.Errorf("expected waiting after gate, got %q", item.Status)
	}
	if item.WaitingType != "gate" {
		t.Errorf("expected waiting_type=gate, got %q", item.WaitingType)
	}
	if item.WaitingOn != "Confirm approach before implementing" {
		t.Errorf("expected WaitingOn to be description, got %q", item.WaitingOn)
	}
	if item.WaitingSince == "" {
		t.Error("expected WaitingSince to be set after gate")
	}
	if item.GateMsgID != "msg-gate-1" {
		t.Errorf("expected GateMsgID=msg-gate-1, got %q", item.GateMsgID)
	}
}

// TestDerive_GateWithoutResolveKeepsWaiting verifies that a gate without
// a corresponding gate-resolve leaves the item in waiting status.
func TestDerive_GateWithoutResolveKeepsWaiting(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-gate-1", []string{"work:gate", "work:gate-type:review"}, map[string]interface{}{
			"target":    "msg-create-1",
			"gate_type": "review",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusWaiting {
		t.Errorf("expected item to remain waiting without resolve, got %q", item.Status)
	}
	if item.GateMsgID == "" {
		t.Error("expected GateMsgID to still be set (gate unresolved)")
	}
}

// TestDerive_GateApproved verifies that work:gate-resolve with resolution=approved
// transitions the item back to active and clears gate fields.
func TestDerive_GateApproved(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-gate-1", []string{"work:gate", "work:gate-type:design"}, map[string]interface{}{
			"target":    "msg-create-1",
			"gate_type": "design",
		}, []string{"msg-create-1"}, ts+1000),
		makeMsg("msg-resolve-1", []string{"work:gate-resolve", "work:resolution:approved"}, map[string]interface{}{
			"target":     "msg-gate-1",
			"resolution": "approved",
			"reason":     "Looks good",
		}, []string{"msg-gate-1"}, ts+2000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusActive {
		t.Errorf("expected active after approval, got %q", item.Status)
	}
	if item.GateMsgID != "" {
		t.Errorf("expected GateMsgID cleared after approval, got %q", item.GateMsgID)
	}
	if item.WaitingType != "" {
		t.Errorf("expected WaitingType cleared after approval, got %q", item.WaitingType)
	}
	if item.WaitingOn != "" {
		t.Errorf("expected WaitingOn cleared after approval, got %q", item.WaitingOn)
	}
	if item.WaitingSince != "" {
		t.Errorf("expected WaitingSince cleared after approval, got %q", item.WaitingSince)
	}
}

// TestDerive_GateRejected verifies that work:gate-resolve with resolution=rejected
// keeps the item in waiting status with GateMsgID still set.
func TestDerive_GateRejected(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-gate-1", []string{"work:gate", "work:gate-type:scope"}, map[string]interface{}{
			"target":    "msg-create-1",
			"gate_type": "scope",
		}, []string{"msg-create-1"}, ts+1000),
		makeMsg("msg-resolve-1", []string{"work:gate-resolve", "work:resolution:rejected"}, map[string]interface{}{
			"target":     "msg-gate-1",
			"resolution": "rejected",
			"reason":     "Scope too broad",
		}, []string{"msg-gate-1"}, ts+2000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	// rejected: item remains waiting, gate still open.
	if item.Status != state.StatusWaiting {
		t.Errorf("expected waiting after rejection, got %q", item.Status)
	}
	if item.GateMsgID == "" {
		t.Error("expected GateMsgID still set after rejection (gate not cleared)")
	}
}

// TestDerive_GateResolveViaAntecedents verifies that gate-resolve can find
// its target gate message via antecedents when the target field is empty.
func TestDerive_GateResolveViaAntecedents(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-gate-1", []string{"work:gate", "work:gate-type:human"}, map[string]interface{}{
			"target":    "msg-create-1",
			"gate_type": "human",
		}, []string{"msg-create-1"}, ts+1000),
		// gate-resolve with empty target — falls back to antecedents.
		makeMsg("msg-resolve-1", []string{"work:gate-resolve", "work:resolution:approved"}, map[string]interface{}{
			"target":     "", // empty — resolve via antecedents
			"resolution": "approved",
		}, []string{"msg-gate-1"}, ts+2000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusActive {
		t.Errorf("expected active after approval via antecedents, got %q", item.Status)
	}
}
