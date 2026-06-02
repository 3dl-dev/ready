package state_test

// Tests for DeriveFromJSONL / DeriveFromJSONLWithCampfire.
//
// Design: write MutationRecord JSONL to a temp file, call DeriveFromJSONL,
// assert that the derived state matches what Derive() produces for the same
// sequence. This validates the conversion path (MutationRecord→MessageRecord)
// without duplicating the replay logic tests (which live in state_test.go).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/jsonl"
	"github.com/campfire-net/ready/pkg/state"
)

const jsonlTestCampfire = "deadbeef1234567890abcdef1234567890abcdef1234567890abcdef12345678"

// mutRec is a minimal local struct matching jsonl.MutationRecord JSON fields.
// We duplicate the struct here (rather than importing jsonl.MutationRecord) to
// keep the test package self-contained. jsonl is imported only for WorkTagPrefix.
type mutRec struct {
	MsgID       string          `json:"msg_id"`
	CampfireID  string          `json:"campfire_id"`
	Timestamp   int64           `json:"timestamp"`
	Operation   string          `json:"operation"`
	Payload     json.RawMessage `json:"payload"`
	Tags        []string        `json:"tags"`
	Sender      string          `json:"sender"`
	Antecedents []string        `json:"antecedents,omitempty"`
}

// writeMutJSONL writes records to a temp JSONL file and returns its path.
func writeMutJSONL(t *testing.T, records []mutRec) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "mutations.jsonl")
	f, err := os.Create(path)
	if err != nil {
		t.Fatalf("creating JSONL file: %v", err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			t.Fatalf("encoding record: %v", err)
		}
	}
	return path
}

// payload marshals v to json.RawMessage.
func payload(v interface{}) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// mutFromMsg converts a store.MessageRecord to a mutRec for test round-tripping.
func mutFromMsg(m store.MessageRecord) mutRec {
	op := ""
	for _, t := range m.Tags {
		if strings.HasPrefix(t, jsonl.WorkTagPrefix) && len(t) > len(jsonl.WorkTagPrefix) {
			op = t
			break
		}
	}
	return mutRec{
		MsgID:       m.ID,
		CampfireID:  m.CampfireID,
		Timestamp:   m.Timestamp,
		Operation:   op,
		Payload:     json.RawMessage(m.Payload),
		Tags:        m.Tags,
		Sender:      m.Sender,
		Antecedents: m.Antecedents,
	}
}

// TestDeriveFromJSONL_EmptyFile returns an empty map when file doesn't exist.
func TestDeriveFromJSONL_EmptyFile(t *testing.T) {
	items, err := state.DeriveFromJSONL("/nonexistent/path/mutations.jsonl")
	if err != nil {
		t.Fatalf("expected nil error for missing file, got: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty map for missing file, got %d items", len(items))
	}
}

// TestDeriveFromJSONL_EmptyContent returns an empty map for a file with no records.
func TestDeriveFromJSONL_EmptyContent(t *testing.T) {
	path := writeMutJSONL(t, nil)
	items, err := state.DeriveFromJSONL(path)
	if err != nil {
		t.Fatalf("expected nil error for empty file, got: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected empty map for empty file, got %d items", len(items))
	}
}

// TestDeriveFromJSONL_MatchesDeriveCreate verifies that DeriveFromJSONL produces
// the same state as Derive() for a single work:create message.
func TestDeriveFromJSONL_MatchesDeriveCreate(t *testing.T) {
	ts := time.Now().UnixNano()
	p := payload(map[string]interface{}{
		"id": "ready-j01", "title": "JSONL Item One", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
		// Fields under test for field-completeness (Finding 1).
		"context":   "some context text",
		"level":     "task",
		"project":   "myproject",
		"gate":      "review",
		"due":       "2026-12-31",
		"parent_id": "ready-parent-x",
	})

	msg := store.MessageRecord{
		ID:         "msg-j01",
		CampfireID: jsonlTestCampfire,
		Sender:     "testsender",
		Payload:    []byte(p),
		Tags:       []string{"work:create"},
		Timestamp:  ts,
	}

	// Derive via store.MessageRecord (reference).
	want := state.Derive(jsonlTestCampfire, []store.MessageRecord{msg})

	// Derive via JSONL.
	path := writeMutJSONL(t, []mutRec{mutFromMsg(msg)})
	got, err := state.DeriveFromJSONLWithCampfire(path, jsonlTestCampfire)
	if err != nil {
		t.Fatalf("DeriveFromJSONLWithCampfire error: %v", err)
	}

	if len(got) != len(want) {
		t.Errorf("item count: got %d, want %d", len(got), len(want))
	}
	for id, wantItem := range want {
		gotItem, ok := got[id]
		if !ok {
			t.Errorf("missing item %q in JSONL-derived state", id)
			continue
		}
		if gotItem.Title != wantItem.Title {
			t.Errorf("item %q title: got %q, want %q", id, gotItem.Title, wantItem.Title)
		}
		if gotItem.Status != wantItem.Status {
			t.Errorf("item %q status: got %q, want %q", id, gotItem.Status, wantItem.Status)
		}
		if gotItem.Priority != wantItem.Priority {
			t.Errorf("item %q priority: got %q, want %q", id, gotItem.Priority, wantItem.Priority)
		}
		if gotItem.For != wantItem.For {
			t.Errorf("item %q for: got %q, want %q", id, gotItem.For, wantItem.For)
		}
		if gotItem.MsgID != wantItem.MsgID {
			t.Errorf("item %q msg_id: got %q, want %q", id, gotItem.MsgID, wantItem.MsgID)
		}
		if gotItem.CreatedAt != wantItem.CreatedAt {
			t.Errorf("item %q created_at: got %d, want %d", id, gotItem.CreatedAt, wantItem.CreatedAt)
		}
		// Field-completeness: these fields must survive the JSONL round-trip.
		if gotItem.Context != wantItem.Context {
			t.Errorf("item %q context: got %q, want %q", id, gotItem.Context, wantItem.Context)
		}
		if gotItem.Level != wantItem.Level {
			t.Errorf("item %q level: got %q, want %q", id, gotItem.Level, wantItem.Level)
		}
		if gotItem.Project != wantItem.Project {
			t.Errorf("item %q project: got %q, want %q", id, gotItem.Project, wantItem.Project)
		}
		if gotItem.Gate != wantItem.Gate {
			t.Errorf("item %q gate: got %q, want %q", id, gotItem.Gate, wantItem.Gate)
		}
		if gotItem.Due != wantItem.Due {
			t.Errorf("item %q due: got %q, want %q", id, gotItem.Due, wantItem.Due)
		}
		if gotItem.ParentID != wantItem.ParentID {
			t.Errorf("item %q parent_id: got %q, want %q", id, gotItem.ParentID, wantItem.ParentID)
		}
	}
}

// TestDeriveFromJSONL_MutationSequence verifies that a full lifecycle sequence
// (create → status → claim → close) produces identical state via both paths.
func TestDeriveFromJSONL_MutationSequence(t *testing.T) {
	base := time.Now().UnixNano()
	msgs := []store.MessageRecord{
		{
			ID: "msg-seq-create", CampfireID: jsonlTestCampfire, Sender: "human",
			Payload:   payload(map[string]interface{}{"id": "ready-seq", "title": "Sequence Test", "type": "task", "for": "baron@3dl.dev", "priority": "p2"}),
			Tags:      []string{"work:create"},
			Timestamp: base,
		},
		{
			ID: "msg-seq-status", CampfireID: jsonlTestCampfire, Sender: "human",
			Payload:     payload(map[string]interface{}{"target": "msg-seq-create", "to": "active"}),
			Tags:        []string{"work:status"},
			Antecedents: []string{"msg-seq-create"},
			Timestamp:   base + 1,
		},
		{
			ID: "msg-seq-claim", CampfireID: jsonlTestCampfire, Sender: "agent-pubkey",
			Payload:     payload(map[string]interface{}{"target": "msg-seq-create"}),
			Tags:        []string{"work:claim"},
			Antecedents: []string{"msg-seq-create"},
			Timestamp:   base + 2,
		},
	}

	want := state.Derive(jsonlTestCampfire, msgs)

	muts := make([]mutRec, len(msgs))
	for i, m := range msgs {
		muts[i] = mutFromMsg(m)
	}
	path := writeMutJSONL(t, muts)
	got, err := state.DeriveFromJSONLWithCampfire(path, jsonlTestCampfire)
	if err != nil {
		t.Fatalf("DeriveFromJSONLWithCampfire error: %v", err)
	}

	wantItem := want["ready-seq"]
	gotItem := got["ready-seq"]
	if wantItem == nil || gotItem == nil {
		t.Fatal("item ready-seq not found in one or both derived states")
	}
	if gotItem.Status != wantItem.Status {
		t.Errorf("status: got %q, want %q", gotItem.Status, wantItem.Status)
	}
	if gotItem.By != wantItem.By {
		t.Errorf("by: got %q, want %q", gotItem.By, wantItem.By)
	}
}

// TestDeriveFromJSONL_BlockDependency verifies that block/unblock edges are
// replayed correctly from JSONL.
func TestDeriveFromJSONL_BlockDependency(t *testing.T) {
	base := time.Now().UnixNano()
	msgs := []store.MessageRecord{
		{
			ID: "msg-b-blocker", CampfireID: jsonlTestCampfire, Sender: "human",
			Payload:   payload(map[string]interface{}{"id": "ready-b-blocker", "title": "Blocker", "type": "task", "for": "baron@3dl.dev", "priority": "p0"}),
			Tags:      []string{"work:create"},
			Timestamp: base,
		},
		{
			ID: "msg-b-blocked", CampfireID: jsonlTestCampfire, Sender: "human",
			Payload:   payload(map[string]interface{}{"id": "ready-b-blocked", "title": "Blocked", "type": "task", "for": "baron@3dl.dev", "priority": "p1"}),
			Tags:      []string{"work:create"},
			Timestamp: base + 1,
		},
		{
			ID: "msg-b-block", CampfireID: jsonlTestCampfire, Sender: "human",
			Payload:   payload(map[string]interface{}{"blocker_id": "ready-b-blocker", "blocked_id": "ready-b-blocked", "blocker_msg": "msg-b-blocker", "blocked_msg": "msg-b-blocked"}),
			Tags:      []string{"work:block"},
			Timestamp: base + 2,
		},
	}

	want := state.Derive(jsonlTestCampfire, msgs)

	muts := make([]mutRec, len(msgs))
	for i, m := range msgs {
		muts[i] = mutFromMsg(m)
	}
	path := writeMutJSONL(t, muts)
	got, err := state.DeriveFromJSONLWithCampfire(path, jsonlTestCampfire)
	if err != nil {
		t.Fatalf("DeriveFromJSONLWithCampfire error: %v", err)
	}

	wantBlocked := want["ready-b-blocked"]
	gotBlocked := got["ready-b-blocked"]
	if wantBlocked == nil || gotBlocked == nil {
		t.Fatal("ready-b-blocked not found")
	}
	if gotBlocked.Status != state.StatusBlocked {
		t.Errorf("expected blocked status, got %q", gotBlocked.Status)
	}
	if gotBlocked.Status != wantBlocked.Status {
		t.Errorf("status mismatch: got %q, want %q", gotBlocked.Status, wantBlocked.Status)
	}
}

// TestDeriveFromJSONL_MalformedLinesSkipped verifies that malformed lines do
// not cause an error and are silently skipped.
func TestDeriveFromJSONL_MalformedLinesSkipped(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "mutations.jsonl")

	ts := time.Now().UnixNano()
	goodRecord := mutRec{
		MsgID:      "msg-good",
		CampfireID: jsonlTestCampfire,
		Timestamp:  ts,
		Operation:  "work:create",
		Payload:    payload(map[string]interface{}{"id": "ready-good", "title": "Good Item", "type": "task", "for": "baron@3dl.dev", "priority": "p1"}),
		Tags:       []string{"work:create"},
		Sender:     "testsender",
	}
	goodJSON, _ := json.Marshal(goodRecord)

	content := "this is not json\n" +
		string(goodJSON) + "\n" +
		"{\"broken\": ]\n" // another malformed line

	if err := os.WriteFile(path, []byte(content), 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	items, err := state.DeriveFromJSONL(path)
	if err != nil {
		t.Fatalf("unexpected error with malformed lines: %v", err)
	}
	if len(items) != 1 {
		t.Errorf("expected 1 item (good record), got %d", len(items))
	}
	if _, ok := items["ready-good"]; !ok {
		t.Error("expected ready-good item from the good record")
	}
}

// TestDeriveFromJSONL_TimestampOrderingPreserved verifies that records written
// out-of-order in the file are still processed in timestamp order.
// (If processed out-of-order, a work:close before work:create would not find
// the item and close would be silently skipped — state stays open.)
func TestDeriveFromJSONL_TimestampOrderingPreserved(t *testing.T) {
	base := time.Now().UnixNano()
	createRec := mutRec{
		MsgID: "msg-ord-create", CampfireID: jsonlTestCampfire, Timestamp: base,
		Operation: "work:create", Sender: "human",
		Payload: payload(map[string]interface{}{"id": "ready-ord", "title": "Order Test", "type": "task", "for": "baron@3dl.dev", "priority": "p2"}),
		Tags:    []string{"work:create"},
	}
	closeRec := mutRec{
		MsgID: "msg-ord-close", CampfireID: jsonlTestCampfire, Timestamp: base + 1000,
		Operation: "work:close", Sender: "human",
		Payload:     payload(map[string]interface{}{"target": "msg-ord-create", "resolution": "done", "reason": "finished"}),
		Tags:        []string{"work:close"},
		Antecedents: []string{"msg-ord-create"},
	}

	// Write close BEFORE create in the file (out of timestamp order).
	path := writeMutJSONL(t, []mutRec{closeRec, createRec})

	items, err := state.DeriveFromJSONL(path)
	if err != nil {
		t.Fatalf("DeriveFromJSONL error: %v", err)
	}
	item, ok := items["ready-ord"]
	if !ok {
		t.Fatal("expected item ready-ord")
	}
	if item.Status != state.StatusDone {
		t.Errorf("expected status=done (create processed before close), got %q", item.Status)
	}
}
