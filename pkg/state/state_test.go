package state_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/state"
)

const testCampfire = "abc123"

// makeMsg is a test helper that constructs a MessageRecord.
func makeMsg(id string, tags []string, payload interface{}, antecedents []string, ts int64) store.MessageRecord {
	p, _ := json.Marshal(payload)
	return store.MessageRecord{
		ID:          id,
		CampfireID:  testCampfire,
		Sender:      "testsender",
		Payload:     p,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   ts,
	}
}

func now() int64 { return time.Now().UnixNano() }

func TestDerive_Create(t *testing.T) {
	msgs := []store.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id":       "ready-t01",
			"title":    "Test item",
			"type":     "task",
			"for":      "baron@3dl.dev",
			"priority": "p1",
		}, nil, now()),
	}

	items := state.Derive(testCampfire, msgs)
	item, ok := items["ready-t01"]
	if !ok {
		t.Fatal("expected item ready-t01 to exist")
	}
	if item.Title != "Test item" {
		t.Errorf("expected title 'Test item', got %q", item.Title)
	}
	if item.Status != state.StatusInbox {
		t.Errorf("expected status inbox, got %q", item.Status)
	}
	if item.Priority != "p1" {
		t.Errorf("expected priority p1, got %q", item.Priority)
	}
	if item.For != "baron@3dl.dev" {
		t.Errorf("expected for baron@3dl.dev, got %q", item.For)
	}
	if item.MsgID != "msg-create-1" {
		t.Errorf("expected msg_id msg-create-1, got %q", item.MsgID)
	}
}

func TestDerive_StatusTransition(t *testing.T) {
	ts := now()
	msgs := []store.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-status-1", []string{"work:status", "work:status:active"}, map[string]interface{}{
			"target": "msg-create-1",
			"to":     "active",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusActive {
		t.Errorf("expected active, got %q", item.Status)
	}
}

func TestDerive_Claim(t *testing.T) {
	ts := now()
	msgs := []store.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-claim-1", []string{"work:claim"}, map[string]interface{}{
			"target": "msg-create-1",
		}, []string{"msg-create-1"}, ts+1000),
	}
	msgs[1].Sender = "agent-pubkey-hex"

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusActive {
		t.Errorf("expected active after claim, got %q", item.Status)
	}
	if item.By != "agent-pubkey-hex" {
		t.Errorf("expected by=agent-pubkey-hex, got %q", item.By)
	}
}

func TestDerive_Close(t *testing.T) {
	ts := now()
	msgs := []store.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-close-1", []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target":     "msg-create-1",
			"resolution": "done",
			"reason":     "finished",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusDone {
		t.Errorf("expected done, got %q", item.Status)
	}
	if !state.IsTerminal(item) {
		t.Error("expected terminal item")
	}
}

func TestDerive_Block(t *testing.T) {
	ts := now()
	msgs := []store.MessageRecord{
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
		}, nil, ts+200),
	}

	items := state.Derive(testCampfire, msgs)
	t02 := items["ready-t02"]
	if t02 == nil {
		t.Fatal("ready-t02 not found")
	}
	if t02.Status != state.StatusBlocked {
		t.Errorf("expected blocked, got %q", t02.Status)
	}
	if len(t02.BlockedBy) == 0 || t02.BlockedBy[0] != "ready-t01" {
		t.Errorf("expected BlockedBy=[ready-t01], got %v", t02.BlockedBy)
	}
}

func TestDerive_BlockImplicitUnblockOnClose(t *testing.T) {
	ts := now()
	msgs := []store.MessageRecord{
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
		}, nil, ts+200),
		// Close the blocker — should implicitly unblock t02.
		makeMsg("msg-close-t01", []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target":     "msg-t01",
			"resolution": "done",
		}, []string{"msg-t01"}, ts+300),
	}

	items := state.Derive(testCampfire, msgs)
	t02 := items["ready-t02"]
	if t02 == nil {
		t.Fatal("ready-t02 not found")
	}
	if t02.Status == state.StatusBlocked {
		t.Errorf("expected t02 to be unblocked after blocker closed, got %q", t02.Status)
	}
}

func TestDerive_Waiting(t *testing.T) {
	ts := now()
	msgs := []store.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-status-1", []string{"work:status", "work:status:waiting"}, map[string]interface{}{
			"target":       "msg-create-1",
			"to":           "waiting",
			"waiting_on":   "vendor quote",
			"waiting_type": "vendor",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusWaiting {
		t.Errorf("expected waiting, got %q", item.Status)
	}
	if item.WaitingOn != "vendor quote" {
		t.Errorf("expected WaitingOn='vendor quote', got %q", item.WaitingOn)
	}
	if item.WaitingType != "vendor" {
		t.Errorf("expected WaitingType=vendor, got %q", item.WaitingType)
	}
	if item.WaitingSince == "" {
		t.Error("expected WaitingSince to be set")
	}
}

func TestDerive_ETAFromPriority(t *testing.T) {
	ts := time.Now().UnixNano()
	msgs := []store.MessageRecord{
		makeMsg("msg-p0", []string{"work:create"}, map[string]interface{}{
			"id": "ready-p0", "title": "P0", "type": "task",
			"for": "baron@3dl.dev", "priority": "p0",
		}, nil, ts),
		makeMsg("msg-p3", []string{"work:create"}, map[string]interface{}{
			"id": "ready-p3", "title": "P3", "type": "task",
			"for": "baron@3dl.dev", "priority": "p3",
		}, nil, ts),
	}

	items := state.Derive(testCampfire, msgs)

	p0 := items["ready-p0"]
	if p0 == nil {
		t.Fatal("ready-p0 not found")
	}
	if p0.ETA == "" {
		t.Error("expected ETA to be set for p0")
	}

	p3 := items["ready-p3"]
	if p3 == nil {
		t.Fatal("ready-p3 not found")
	}
	if p3.ETA == "" {
		t.Error("expected ETA to be set for p3")
	}

	// p0 eta should be before p3 eta (roughly).
	etaP0, err0 := time.Parse(time.RFC3339, p0.ETA)
	etaP3, err3 := time.Parse(time.RFC3339, p3.ETA)
	if err0 != nil || err3 != nil {
		t.Fatalf("ETA parse error: %v %v", err0, err3)
	}
	if !etaP0.Before(etaP3) {
		t.Errorf("expected p0 ETA (%s) before p3 ETA (%s)", p0.ETA, p3.ETA)
	}
}

func TestDerive_MultipleItems(t *testing.T) {
	ts := now()
	msgs := []store.MessageRecord{
		makeMsg("msg-a", []string{"work:create"}, map[string]interface{}{
			"id": "ready-a", "title": "A", "type": "task",
			"for": "a@test.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-b", []string{"work:create"}, map[string]interface{}{
			"id": "ready-b", "title": "B", "type": "decision",
			"for": "b@test.dev", "priority": "p0",
		}, nil, ts+100),
	}

	items := state.Derive(testCampfire, msgs)
	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items["ready-a"] == nil || items["ready-b"] == nil {
		t.Error("expected both items to exist")
	}
}
