package state_test

// Integration tests: views produce correct results from JSONL-derived state.
//
// These tests verify the full pipeline: write JSONL → DeriveFromJSONL → Apply view filter.
// They ensure that view predicates work correctly on JSONL-derived Items, not just
// on Items derived from store.MessageRecord directly.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/3dl-dev/ready/pkg/views"
)

const viewJSONLCampfire = "aaaa1111bbbb2222cccc3333dddd4444eeee5555ffff6666aaaa1111bbbb2222"

// buildViewJSONL writes a set of mutRec records to a temp file for view tests.
func buildViewJSONL(t *testing.T, records []mutRec) string {
	t.Helper()
	return writeMutJSONL(t, records)
}

func mkViewMut(msgID, itemID, title string, ts int64, priority string, eta string) mutRec {
	p := map[string]interface{}{
		"id": itemID, "title": title, "type": "task",
		"for": "baron@3dl.dev", "priority": priority,
	}
	if eta != "" {
		p["eta"] = eta
	}
	b, _ := json.Marshal(p)
	return mutRec{
		MsgID:     msgID,
		CampfireID: viewJSONLCampfire,
		Timestamp: ts,
		Operation: "work:create",
		Payload:   json.RawMessage(b),
		Tags:      []string{"work:create"},
		Sender:    "testsender",
	}
}

// TestViewReadyFilter_FromJSONL verifies the ready view works on JSONL state.
// An item with ETA in the past should appear in ready; a terminal item should not.
func TestViewReadyFilter_FromJSONL(t *testing.T) {
	base := time.Now().UnixNano()
	past := time.Now().Add(-1 * time.Hour).UTC().Format(time.RFC3339)
	future := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)

	records := []mutRec{
		mkViewMut("msg-vr1", "ready-vr1", "Ready Item (past ETA)", base, "p0", past),
		mkViewMut("msg-vr2", "ready-vr2", "Not Ready (future ETA)", base+1, "p3", future),
		mkViewMut("msg-vr3", "ready-vr3", "To Be Closed", base+2, "p1", past),
	}
	// Close vr3
	closeP, _ := json.Marshal(map[string]interface{}{
		"target": "msg-vr3", "resolution": "done", "reason": "closed",
	})
	records = append(records, mutRec{
		MsgID: "msg-vr3-close", CampfireID: viewJSONLCampfire, Timestamp: base + 3,
		Operation: "work:close", Payload: json.RawMessage(closeP),
		Tags: []string{"work:close"}, Sender: "testsender",
		Antecedents: []string{"msg-vr3"},
	})

	path := buildViewJSONL(t, records)
	items, err := state.DeriveFromJSONLWithCampfire(path, viewJSONLCampfire)
	if err != nil {
		t.Fatalf("DeriveFromJSONLWithCampfire error: %v", err)
	}

	var itemSlice []*state.Item
	for _, item := range items {
		itemSlice = append(itemSlice, item)
	}

	readyItems := views.Apply(itemSlice, views.ReadyFilter())

	// ready-vr1 should be in ready (past ETA, not terminal, not blocked)
	foundVR1 := false
	foundVR2 := false
	for _, item := range readyItems {
		if item.ID == "ready-vr1" {
			foundVR1 = true
		}
		// ready-vr3 should NOT appear (terminal/done)
		if item.ID == "ready-vr3" {
			t.Error("ready-vr3 (closed) should not appear in ready view")
		}
		// ready-vr2 SHOULD appear (ETA is for sorting, not filtering)
		if item.ID == "ready-vr2" {
			foundVR2 = true
		}
	}
	if !foundVR1 {
		t.Error("ready-vr1 (past ETA, open) should appear in ready view")
	}
	if !foundVR2 {
		t.Error("ready-vr2 (future ETA, open) should appear in ready view (ETA is for sorting only)")
	}
}

// TestViewWorkFilter_FromJSONL verifies the work view shows only active items.
func TestViewWorkFilter_FromJSONL(t *testing.T) {
	base := time.Now().UnixNano()

	records := []mutRec{
		mkViewMut("msg-vw1", "ready-vw1", "Active Item", base, "p1", ""),
		mkViewMut("msg-vw2", "ready-vw2", "Inbox Item", base+1, "p1", ""),
	}
	// Claim vw1 → active
	claimP, _ := json.Marshal(map[string]interface{}{"target": "msg-vw1"})
	records = append(records, mutRec{
		MsgID: "msg-vw1-claim", CampfireID: viewJSONLCampfire, Timestamp: base + 2,
		Operation: "work:claim", Payload: json.RawMessage(claimP),
		Tags: []string{"work:claim"}, Sender: "agent-key",
		Antecedents: []string{"msg-vw1"},
	})

	path := buildViewJSONL(t, records)
	items, err := state.DeriveFromJSONLWithCampfire(path, viewJSONLCampfire)
	if err != nil {
		t.Fatalf("DeriveFromJSONLWithCampfire error: %v", err)
	}

	var itemSlice []*state.Item
	for _, item := range items {
		itemSlice = append(itemSlice, item)
	}

	workItems := views.Apply(itemSlice, views.WorkFilter())

	foundActive := false
	for _, item := range workItems {
		if item.ID == "ready-vw1" {
			foundActive = true
			if item.Status != state.StatusActive {
				t.Errorf("ready-vw1 status: got %q, want active", item.Status)
			}
		}
		if item.ID == "ready-vw2" {
			t.Error("ready-vw2 (inbox) should not appear in work view")
		}
	}
	if !foundActive {
		t.Error("ready-vw1 (active/claimed) should appear in work view")
	}
}

// TestViewMyWorkFilter_FromJSONL verifies the my-work view filters by 'by' field.
func TestViewMyWorkFilter_FromJSONL(t *testing.T) {
	base := time.Now().UnixNano()
	myKey := "my-agent-pubkey-hex"

	records := []mutRec{
		mkViewMut("msg-mw1", "ready-mw1", "My Work Item", base, "p1", ""),
		mkViewMut("msg-mw2", "ready-mw2", "Other's Work Item", base+1, "p1", ""),
	}
	// Assign mw1 to myKey
	delegateP, _ := json.Marshal(map[string]interface{}{
		"target": "msg-mw1", "to": myKey, "from": "baron@3dl.dev",
	})
	records = append(records, mutRec{
		MsgID: "msg-mw1-delegate", CampfireID: viewJSONLCampfire, Timestamp: base + 2,
		Operation: "work:delegate", Payload: json.RawMessage(delegateP),
		Tags: []string{"work:delegate"}, Sender: "baron@3dl.dev",
		Antecedents: []string{"msg-mw1"},
	})

	path := buildViewJSONL(t, records)
	items, err := state.DeriveFromJSONLWithCampfire(path, viewJSONLCampfire)
	if err != nil {
		t.Fatalf("DeriveFromJSONLWithCampfire error: %v", err)
	}

	var itemSlice []*state.Item
	for _, item := range items {
		itemSlice = append(itemSlice, item)
	}

	myItems := views.Apply(itemSlice, views.MyWorkFilter(myKey))

	if len(myItems) != 1 {
		t.Errorf("expected 1 item in my-work, got %d", len(myItems))
	}
	if len(myItems) > 0 && myItems[0].ID != "ready-mw1" {
		t.Errorf("expected ready-mw1 in my-work, got %q", myItems[0].ID)
	}
}
