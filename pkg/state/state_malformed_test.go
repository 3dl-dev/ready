package state_test

import (
	"encoding/json"
	"testing"

	msgrec "github.com/campfire-net/ready/pkg/msgrec"
	"github.com/campfire-net/ready/pkg/state"
)

// makeMsgRaw constructs a MessageRecord with a raw byte payload (no json.Marshal).
func makeMsgRaw(id string, tags []string, payload []byte, antecedents []string, ts int64) msgrec.MessageRecord {
	return msgrec.MessageRecord{
		ID:          id,
		CampfireID:  testCampfire,
		Sender:      "testsender",
		Payload:     payload,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   ts,
	}
}

// validCreate returns a well-formed work:create MessageRecord.
func validCreate(msgID, itemID string, ts int64) msgrec.MessageRecord {
	return makeMsg(msgID, []string{"work:create"}, map[string]interface{}{
		"id": itemID, "title": "Valid item", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)
}

// TestDerive_MalformedCreatePayload: work:create with bad JSON is silently skipped.
// A valid item in the same batch is unaffected.
func TestDerive_MalformedCreatePayload(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsgRaw("msg-bad", []string{"work:create"}, []byte("not json"), nil, ts),
		validCreate("msg-good", "ready-good-1", ts+100),
	}

	items := state.Derive(testCampfire, msgs)
	if len(items) != 1 {
		t.Fatalf("expected 1 item (bad create skipped), got %d", len(items))
	}
	if items["ready-good-1"] == nil {
		t.Error("expected ready-good-1 to exist")
	}
}

// TestDerive_CreateEmptyID: work:create with empty id is silently skipped.
func TestDerive_CreateEmptyID(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-empty-id", []string{"work:create"}, map[string]interface{}{
			"id": "", "title": "No ID", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		validCreate("msg-good", "ready-good-2", ts+100),
	}

	items := state.Derive(testCampfire, msgs)
	if len(items) != 1 {
		t.Fatalf("expected 1 item (empty-id create skipped), got %d", len(items))
	}
	if items["ready-good-2"] == nil {
		t.Error("expected ready-good-2 to exist")
	}
}

// TestDerive_CloseUnknownResolution: work:close with an unrecognised resolution
// defaults to StatusDone. This is a deliberate design decision — the test
// documents it.
func TestDerive_CloseUnknownResolution(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-c", "ready-close-3", ts),
		makeMsg("msg-close", []string{"work:close"}, map[string]interface{}{
			"target":     "msg-c",
			"resolution": "completed", // not "done" / "cancelled" / "failed"
			"reason":     "all good",
		}, []string{"msg-c"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-close-3"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusDone {
		t.Errorf("expected StatusDone for unknown resolution, got %q", item.Status)
	}
}

// TestDerive_StatusOrphanedTarget: work:status with a target that does not
// appear in msgIndex and has no matching antecedents is silently skipped.
func TestDerive_StatusOrphanedTarget(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-good", "ready-good-4", ts),
		makeMsg("msg-status-orphan", []string{"work:status"}, map[string]interface{}{
			"target": "msg-does-not-exist",
			"to":     "active",
		}, nil, ts+500),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-good-4"]
	if item == nil {
		t.Fatal("ready-good-4 not found")
	}
	// Status must remain inbox — the orphaned status message had no effect.
	if item.Status != state.StatusInbox {
		t.Errorf("expected StatusInbox, got %q", item.Status)
	}
}

// TestDerive_BlockEmptyIDs: work:block with empty blocker_id or blocked_id is
// silently skipped — no block edge is registered.
func TestDerive_BlockEmptyIDs(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-a", "ready-blk-a", ts),
		validCreate("msg-b", "ready-blk-b", ts+100),
		// blocker_id is empty
		makeMsg("msg-block-empty-blocker", []string{"work:block"}, map[string]interface{}{
			"blocker_id": "",
			"blocked_id": "ready-blk-b",
		}, nil, ts+200),
		// blocked_id is empty
		makeMsg("msg-block-empty-blocked", []string{"work:block"}, map[string]interface{}{
			"blocker_id": "ready-blk-a",
			"blocked_id": "",
		}, nil, ts+300),
	}

	items := state.Derive(testCampfire, msgs)
	blkB := items["ready-blk-b"]
	if blkB == nil {
		t.Fatal("ready-blk-b not found")
	}
	if blkB.Status == state.StatusBlocked {
		t.Error("expected ready-blk-b to NOT be blocked (empty-ID block edges should be skipped)")
	}
	blkA := items["ready-blk-a"]
	if blkA == nil {
		t.Fatal("ready-blk-a not found")
	}
	if len(blkA.Blocks) != 0 {
		t.Errorf("expected ready-blk-a.Blocks to be empty, got %v", blkA.Blocks)
	}
}

// TestDerive_GateResolveUnknownGate: work:gate-resolve targeting a gate message
// ID not in gateMsgIndex is silently skipped; item state is unchanged.
func TestDerive_GateResolveUnknownGate(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-item", "ready-gate-5", ts),
		// Send a gate so the item enters waiting state.
		makeMsg("msg-gate", []string{"work:gate"}, map[string]interface{}{
			"target":      "msg-item",
			"gate_type":   "design",
			"description": "needs design review",
		}, []string{"msg-item"}, ts+500),
		// Try to resolve a gate message that doesn't exist.
		makeMsg("msg-gate-resolve-bad", []string{"work:gate-resolve"}, map[string]interface{}{
			"target":     "msg-nonexistent-gate",
			"resolution": "approved",
		}, nil, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-gate-5"]
	if item == nil {
		t.Fatal("ready-gate-5 not found")
	}
	// Gate was set, bad resolve was ignored — item should still be waiting.
	if item.Status != state.StatusWaiting {
		t.Errorf("expected StatusWaiting (bad gate-resolve silently skipped), got %q", item.Status)
	}
	if item.GateMsgID != "msg-gate" {
		t.Errorf("expected GateMsgID=msg-gate, got %q", item.GateMsgID)
	}
}

// TestDerive_MixedValidAndInvalid: a sequence of 5 messages mixing valid and
// malformed messages. Malformed messages must not corrupt valid state.
//
// Sequence:
//  1. valid work:create  → ready-mix-a created (inbox)
//  2. malformed work:create (bad JSON) → skipped
//  3. valid work:status  → ready-mix-a transitions to active
//  4. malformed work:close (bad JSON) → skipped
//  5. valid work:close   → ready-mix-a transitions to done
func TestDerive_MixedValidAndInvalid(t *testing.T) {
	ts := now()

	p, _ := json.Marshal(map[string]interface{}{
		"target":     "msg-mix-create",
		"resolution": "done",
		"reason":     "finished",
	})
	_ = p // used below

	msgs := []msgrec.MessageRecord{
		// 1. Valid create
		validCreate("msg-mix-create", "ready-mix-a", ts),
		// 2. Malformed create
		makeMsgRaw("msg-mix-bad-create", []string{"work:create"}, []byte("{invalid"), nil, ts+100),
		// 3. Valid status → active
		makeMsg("msg-mix-status", []string{"work:status"}, map[string]interface{}{
			"target": "msg-mix-create",
			"to":     "active",
		}, []string{"msg-mix-create"}, ts+200),
		// 4. Malformed close
		makeMsgRaw("msg-mix-bad-close", []string{"work:close"}, []byte("not json at all"), nil, ts+300),
		// 5. Valid close
		makeMsg("msg-mix-close", []string{"work:close"}, map[string]interface{}{
			"target":     "msg-mix-create",
			"resolution": "done",
			"reason":     "finished",
		}, []string{"msg-mix-create"}, ts+400),
	}

	items := state.Derive(testCampfire, msgs)

	// Only ready-mix-a should exist (the bad create was skipped).
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	item := items["ready-mix-a"]
	if item == nil {
		t.Fatal("ready-mix-a not found")
	}
	if item.Status != state.StatusDone {
		t.Errorf("expected StatusDone after valid close, got %q", item.Status)
	}
}

// TestDerive_MalformedStatusPayload: work:status with bad JSON is silently skipped.
func TestDerive_MalformedStatusPayload(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-s", "ready-status-bad", ts),
		makeMsgRaw("msg-status-bad", []string{"work:status"}, []byte("not json"), []string{"msg-s"}, ts+100),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-status-bad"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusInbox {
		t.Errorf("expected StatusInbox (bad status skipped), got %q", item.Status)
	}
}

// TestDerive_MalformedClaimPayload: work:claim with bad JSON is silently skipped.
func TestDerive_MalformedClaimPayload(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-cl", "ready-claim-bad", ts),
		makeMsgRaw("msg-claim-bad", []string{"work:claim"}, []byte("{bad"), []string{"msg-cl"}, ts+100),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-claim-bad"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusInbox {
		t.Errorf("expected StatusInbox (bad claim skipped), got %q", item.Status)
	}
	if item.By != "" {
		t.Errorf("expected By to be empty (bad claim skipped), got %q", item.By)
	}
}

// TestDerive_MalformedDelegatePayload: work:delegate with bad JSON is silently skipped.
func TestDerive_MalformedDelegatePayload(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-del-create", []string{"work:create"}, map[string]interface{}{
			"id": "ready-delegate-bad", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "by": "original-agent", "priority": "p1",
		}, nil, ts),
		makeMsgRaw("msg-delegate-bad", []string{"work:delegate"}, []byte("not json"), []string{"msg-del-create"}, ts+100),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-delegate-bad"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.By != "original-agent" {
		t.Errorf("expected By=original-agent (bad delegate skipped), got %q", item.By)
	}
}

// TestDerive_MalformedUpdatePayload: work:update with bad JSON is silently skipped.
func TestDerive_MalformedUpdatePayload(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-upd-create", []string{"work:create"}, map[string]interface{}{
			"id": "ready-update-bad", "title": "Original title", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsgRaw("msg-update-bad", []string{"work:update"}, []byte("{not valid json"), []string{"msg-upd-create"}, ts+100),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-update-bad"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Title != "Original title" {
		t.Errorf("expected Title='Original title' (bad update skipped), got %q", item.Title)
	}
}

// TestDerive_MalformedBlockPayload: work:block with bad JSON is silently skipped.
func TestDerive_MalformedBlockPayload(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-blk-a", "ready-blk-bad-a", ts),
		validCreate("msg-blk-b", "ready-blk-bad-b", ts+100),
		makeMsgRaw("msg-block-bad", []string{"work:block"}, []byte("!!!"), nil, ts+200),
	}

	items := state.Derive(testCampfire, msgs)
	blkB := items["ready-blk-bad-b"]
	if blkB == nil {
		t.Fatal("ready-blk-bad-b not found")
	}
	if blkB.Status == state.StatusBlocked {
		t.Error("expected ready-blk-bad-b to NOT be blocked (bad block payload skipped)")
	}
}

// TestDerive_MalformedUnblockPayload: work:unblock with bad JSON is silently skipped.
// The block edge remains in place.
func TestDerive_MalformedUnblockPayload(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-ub-a", "ready-unblk-a", ts),
		validCreate("msg-ub-b", "ready-unblk-b", ts+100),
		makeMsg("msg-block-ok", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "ready-unblk-a",
			"blocked_id":  "ready-unblk-b",
			"blocker_msg": "msg-ub-a",
			"blocked_msg": "msg-ub-b",
		}, nil, ts+200),
		// Malformed unblock — should be skipped, block edge stays.
		makeMsgRaw("msg-unblock-bad", []string{"work:unblock"}, []byte("bad json"), []string{"msg-block-ok"}, ts+300),
	}

	items := state.Derive(testCampfire, msgs)
	blkB := items["ready-unblk-b"]
	if blkB == nil {
		t.Fatal("ready-unblk-b not found")
	}
	// Block edge should still be active because unblock was malformed.
	if blkB.Status != state.StatusBlocked {
		t.Errorf("expected StatusBlocked (bad unblock skipped, edge stays), got %q", blkB.Status)
	}
}

// TestDerive_MalformedGatePayload: work:gate with bad JSON is silently skipped.
func TestDerive_MalformedGatePayload(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-gate-item", "ready-gate-bad", ts),
		makeMsgRaw("msg-gate-bad", []string{"work:gate"}, []byte("{malformed"), []string{"msg-gate-item"}, ts+100),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-gate-bad"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Gate was malformed — item should remain inbox, not waiting.
	if item.Status != state.StatusInbox {
		t.Errorf("expected StatusInbox (bad gate skipped), got %q", item.Status)
	}
	if item.GateMsgID != "" {
		t.Errorf("expected GateMsgID to be empty, got %q", item.GateMsgID)
	}
}

// TestDerive_MalformedGateResolvePayload: work:gate-resolve with bad JSON is
// silently skipped; gate state is unchanged.
func TestDerive_MalformedGateResolvePayload(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		validCreate("msg-gr-item", "ready-gr-bad", ts),
		makeMsg("msg-gr-gate", []string{"work:gate"}, map[string]interface{}{
			"target":      "msg-gr-item",
			"gate_type":   "review",
			"description": "needs review",
		}, []string{"msg-gr-item"}, ts+500),
		makeMsgRaw("msg-gr-resolve-bad", []string{"work:gate-resolve"}, []byte("not json"), []string{"msg-gr-gate"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-gr-bad"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Gate-resolve was malformed — item should still be waiting with gate open.
	if item.Status != state.StatusWaiting {
		t.Errorf("expected StatusWaiting (bad gate-resolve skipped), got %q", item.Status)
	}
	if item.GateMsgID != "msg-gr-gate" {
		t.Errorf("expected GateMsgID=msg-gr-gate, got %q", item.GateMsgID)
	}
}

// TestDerive_NonStandardStatusValues: non-standard status values (e.g. "in_progress",
// "exited", "in_progres") are applied as-is. Derive is a replay engine — it does
// not validate enums. This test documents the design decision from the session log
// where bd idioms produced non-standard status values 180+ times.
func TestDerive_NonStandardStatusValues(t *testing.T) {
	ts := now()
	cases := []struct {
		name   string
		status string
	}{
		{"bd in_progress idiom", "in_progress"},
		{"exited status", "exited"},
		{"in_progres typo", "in_progres"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			msgID := "msg-ns-" + tc.status
			itemID := "ready-ns-" + tc.status
			msgs := []msgrec.MessageRecord{
				makeMsg(msgID, []string{"work:create"}, map[string]interface{}{
					"id": itemID, "title": "NS test", "type": "task",
					"for": "baron@3dl.dev", "priority": "p2",
				}, nil, ts),
				makeMsg(msgID+"-status", []string{"work:status"}, map[string]interface{}{
					"target": msgID,
					"to":     tc.status,
				}, []string{msgID}, ts+100),
				// A valid second item to confirm it's unaffected.
				validCreate(msgID+"-good", itemID+"-good", ts+50),
			}

			items := state.Derive(testCampfire, msgs)
			item := items[itemID]
			if item == nil {
				t.Fatalf("item %s not found", itemID)
			}
			if item.Status != tc.status {
				t.Errorf("expected status %q applied as-is, got %q", tc.status, item.Status)
			}
			// Verify the sibling item is unaffected.
			sibling := items[itemID+"-good"]
			if sibling == nil {
				t.Errorf("sibling item %s-good not found", itemID)
			}
		})
	}
}
