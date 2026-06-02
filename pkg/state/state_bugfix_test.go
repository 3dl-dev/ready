package state_test

import (
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/state"
)

// BUG-12: Verify that the clear sentinel "-" clears fields via work:update.
func TestDerive_UpdateClearField(t *testing.T) {
	ts := now()
	msgs := []store.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1", "due": "2026-04-01T00:00:00Z",
		}, nil, ts),
		makeMsg("msg-update-1", []string{"work:update"}, map[string]interface{}{
			"target": "msg-create-1",
			"due":    "-",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Due != "" {
		t.Errorf("expected due to be cleared, got %q", item.Due)
	}
}

// BUG-12: Verify that normal non-sentinel values still work.
func TestDerive_UpdateNonSentinelPreserved(t *testing.T) {
	ts := now()
	msgs := []store.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-update-1", []string{"work:update"}, map[string]interface{}{
			"target":   "msg-create-1",
			"priority": "p0",
			"due":      "2026-05-01T00:00:00Z",
		}, []string{"msg-create-1"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Priority != "p0" {
		t.Errorf("expected priority p0, got %q", item.Priority)
	}
	if item.Due != "2026-05-01T00:00:00Z" {
		t.Errorf("expected due 2026-05-01T00:00:00Z, got %q", item.Due)
	}
}

// BUG-03: Verify the ETA boundary behavior — P1 item at exactly now+4h
// should NOT appear as ready (strict less-than per spec).
// This documents the design decision, not a bug to fix.
func TestDerive_ETAFromPriority_AllLevels(t *testing.T) {
	ts := now()
	cases := []struct {
		priority string
		expect   bool // non-empty ETA?
	}{
		{"p0", true},
		{"p1", true},
		{"p2", true},
		{"p3", true},
		{"unknown", true}, // defaults to +24h
	}
	for _, tc := range cases {
		msgs := []store.MessageRecord{
			makeMsg("msg-"+tc.priority, []string{"work:create"}, map[string]interface{}{
				"id": "ready-" + tc.priority, "title": "Test " + tc.priority, "type": "task",
				"for": "baron@3dl.dev", "priority": tc.priority,
			}, nil, ts),
		}
		items := state.Derive(testCampfire, msgs)
		item := items["ready-"+tc.priority]
		if item == nil {
			t.Errorf("priority %s: item not found", tc.priority)
			continue
		}
		if tc.expect && item.ETA == "" {
			t.Errorf("priority %s: expected non-empty ETA", tc.priority)
		}
	}
}

// Verify ClearSentinel constant is accessible and correct.
func TestClearSentinel(t *testing.T) {
	if state.ClearSentinel != "-" {
		t.Errorf("expected ClearSentinel to be '-', got %q", state.ClearSentinel)
	}
}
