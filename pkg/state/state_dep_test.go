package state_test

import (
	"testing"

	msgrec "github.com/3dl-dev/ready/pkg/msgrec"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestDerive_UnblockRemovesEdge verifies that a work:unblock message targeting
// a specific work:block message ID removes the block edge via blockMsgIndex.
func TestDerive_UnblockRemovesEdge(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-t01", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Blocker", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-t02", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t02", "title": "Blocked", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
		}, nil, ts+100),
		// Wire the block edge. The block message ID is "msg-block-1".
		makeMsg("msg-block-1", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "ready-t01",
			"blocked_id":  "ready-t02",
			"blocker_msg": "msg-t01",
			"blocked_msg": "msg-t02",
		}, []string{"msg-t01", "msg-t02"}, ts+200),
		// Explicitly unblock by targeting the work:block message ID.
		makeMsg("msg-unblock-1", []string{"work:unblock"}, map[string]interface{}{
			"target": "msg-block-1",
			"reason": "Dependency no longer needed",
		}, []string{"msg-block-1"}, ts+300),
	}

	items := state.Derive(testCampfire, msgs)

	t02 := items["ready-t02"]
	if t02 == nil {
		t.Fatal("ready-t02 not found")
	}
	// After explicit unblock, t02 should no longer be blocked.
	if t02.Status == state.StatusBlocked {
		t.Errorf("expected t02 to be unblocked after work:unblock, got %q", t02.Status)
	}
	if len(t02.BlockedBy) > 0 {
		t.Errorf("expected BlockedBy to be empty after unblock, got %v", t02.BlockedBy)
	}
}

// TestDerive_UnblockByAntecedent verifies that work:unblock resolves the block
// message via antecedents when the target field is empty.
func TestDerive_UnblockByAntecedent(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-t01", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Blocker", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-t02", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t02", "title": "Blocked", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
		}, nil, ts+100),
		makeMsg("msg-block-1", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "ready-t01",
			"blocked_id":  "ready-t02",
			"blocker_msg": "msg-t01",
			"blocked_msg": "msg-t02",
		}, []string{"msg-t01", "msg-t02"}, ts+200),
		// Unblock with empty target — should resolve via first antecedent.
		makeMsg("msg-unblock-1", []string{"work:unblock"}, map[string]interface{}{
			"target": "", // empty — fall back to antecedent
			"reason": "No longer needed",
		}, []string{"msg-block-1"}, ts+300),
	}

	items := state.Derive(testCampfire, msgs)
	t02 := items["ready-t02"]
	if t02 == nil {
		t.Fatal("ready-t02 not found")
	}
	if t02.Status == state.StatusBlocked {
		t.Errorf("expected t02 unblocked via antecedent resolution, got %q", t02.Status)
	}
}

// TestDerive_UnblockDoesNotAffectOtherEdges verifies that unblocking one edge
// does not disturb other block edges in the same campfire.
func TestDerive_UnblockDoesNotAffectOtherEdges(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-t01", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Blocker A", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-t02", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t02", "title": "Blocked by A", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
		}, nil, ts+100),
		makeMsg("msg-t03", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t03", "title": "Also blocked by A", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
		}, nil, ts+200),
		// Two separate block edges from t01.
		makeMsg("msg-block-a", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "ready-t01",
			"blocked_id":  "ready-t02",
			"blocker_msg": "msg-t01",
			"blocked_msg": "msg-t02",
		}, []string{"msg-t01", "msg-t02"}, ts+300),
		makeMsg("msg-block-b", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "ready-t01",
			"blocked_id":  "ready-t03",
			"blocker_msg": "msg-t01",
			"blocked_msg": "msg-t03",
		}, []string{"msg-t01", "msg-t03"}, ts+400),
		// Remove only the first block edge.
		makeMsg("msg-unblock-a", []string{"work:unblock"}, map[string]interface{}{
			"target": "msg-block-a",
		}, []string{"msg-block-a"}, ts+500),
	}

	items := state.Derive(testCampfire, msgs)

	t02 := items["ready-t02"]
	if t02 == nil {
		t.Fatal("ready-t02 not found")
	}
	t03 := items["ready-t03"]
	if t03 == nil {
		t.Fatal("ready-t03 not found")
	}

	// t02 should be unblocked.
	if t02.Status == state.StatusBlocked {
		t.Errorf("expected t02 unblocked, got %q", t02.Status)
	}
	// t03 should still be blocked (different edge, not removed).
	if t03.Status != state.StatusBlocked {
		t.Errorf("expected t03 still blocked, got %q", t03.Status)
	}
}

// TestDerive_DepTreeChain verifies that a 3-item dependency chain produces
// correct BlockedBy/Blocks relationships across all three items.
func TestDerive_DepTreeChain(t *testing.T) {
	ts := now()
	// Chain: t01 blocks t02 blocks t03
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-t01", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Step 1", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-t02", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t02", "title": "Step 2", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts+100),
		makeMsg("msg-t03", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t03", "title": "Step 3", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts+200),
		// t01 blocks t02.
		makeMsg("msg-block-12", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "ready-t01",
			"blocked_id":  "ready-t02",
			"blocker_msg": "msg-t01",
			"blocked_msg": "msg-t02",
		}, []string{"msg-t01", "msg-t02"}, ts+300),
		// t02 blocks t03.
		makeMsg("msg-block-23", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "ready-t02",
			"blocked_id":  "ready-t03",
			"blocker_msg": "msg-t02",
			"blocked_msg": "msg-t03",
		}, []string{"msg-t02", "msg-t03"}, ts+400),
	}

	items := state.Derive(testCampfire, msgs)

	t01 := items["ready-t01"]
	t02 := items["ready-t02"]
	t03 := items["ready-t03"]
	if t01 == nil || t02 == nil || t03 == nil {
		t.Fatal("one or more items not found")
	}

	// t01: not blocked, blocks t02.
	if t01.Status == state.StatusBlocked {
		t.Errorf("t01 should not be blocked, got %q", t01.Status)
	}
	if len(t01.Blocks) == 0 || t01.Blocks[0] != "ready-t02" {
		t.Errorf("expected t01.Blocks=[ready-t02], got %v", t01.Blocks)
	}
	if len(t01.BlockedBy) > 0 {
		t.Errorf("expected t01.BlockedBy=[], got %v", t01.BlockedBy)
	}

	// t02: blocked by t01, blocks t03.
	if t02.Status != state.StatusBlocked {
		t.Errorf("expected t02 blocked, got %q", t02.Status)
	}
	if len(t02.BlockedBy) == 0 || t02.BlockedBy[0] != "ready-t01" {
		t.Errorf("expected t02.BlockedBy=[ready-t01], got %v", t02.BlockedBy)
	}
	if len(t02.Blocks) == 0 || t02.Blocks[0] != "ready-t03" {
		t.Errorf("expected t02.Blocks=[ready-t03], got %v", t02.Blocks)
	}

	// t03: blocked by t02.
	if t03.Status != state.StatusBlocked {
		t.Errorf("expected t03 blocked, got %q", t03.Status)
	}
	if len(t03.BlockedBy) == 0 || t03.BlockedBy[0] != "ready-t02" {
		t.Errorf("expected t03.BlockedBy=[ready-t02], got %v", t03.BlockedBy)
	}
}

// TestDerive_ImplicitUnblockCleansIndex verifies that when a blocker closes,
// the blockMsgIndex is cleaned up so a subsequent work:unblock targeting the
// same block message is a no-op (not a double-removal that corrupts state).
func TestDerive_ImplicitUnblockCleansIndex(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-t01", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Blocker", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-t02", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t02", "title": "Blocked", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
		}, nil, ts+100),
		makeMsg("msg-block-1", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "ready-t01",
			"blocked_id":  "ready-t02",
			"blocker_msg": "msg-t01",
			"blocked_msg": "msg-t02",
		}, []string{"msg-t01", "msg-t02"}, ts+200),
		// Blocker closes first (implicit unblock).
		makeMsg("msg-close-t01", []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target":     "msg-t01",
			"resolution": "done",
		}, []string{"msg-t01"}, ts+300),
		// Explicit unblock for the same block message — should be harmless.
		makeMsg("msg-unblock-1", []string{"work:unblock"}, map[string]interface{}{
			"target": "msg-block-1",
			"reason": "Explicit removal after close",
		}, []string{"msg-block-1"}, ts+400),
	}

	items := state.Derive(testCampfire, msgs)
	t02 := items["ready-t02"]
	if t02 == nil {
		t.Fatal("ready-t02 not found")
	}
	// t02 should be unblocked regardless.
	if t02.Status == state.StatusBlocked {
		t.Errorf("expected t02 unblocked, got %q", t02.Status)
	}
	// State should not be corrupted — t02 should still exist.
	if t02.Title != "Blocked" {
		t.Errorf("expected title 'Blocked', got %q", t02.Title)
	}
}
