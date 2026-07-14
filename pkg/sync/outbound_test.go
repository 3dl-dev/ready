package sync_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/jsonl"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// makeRecord creates a MutationRecord for testing.
func makeRecord(msgID, operation string, timestamp int64) jsonl.MutationRecord {
	return jsonl.MutationRecord{
		MsgID:     msgID,
		Operation: operation,
		Timestamp: timestamp,
		Payload:   json.RawMessage(`{"id":"` + msgID + `"}`),
		Tags:      []string{operation},
	}
}

// setupProject creates a temp dir with a .ready/ subdirectory.
func setupProject(t *testing.T) string {
	t.Helper()
	projectDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectDir, ".ready"), 0700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	return projectDir
}

// writePending writes records to .ready/pending.jsonl.
func writePending(t *testing.T, projectDir string, recs []jsonl.MutationRecord) {
	t.Helper()
	pp := filepath.Join(projectDir, ".ready", "pending.jsonl")
	w := jsonl.NewWriter(pp)
	for _, rec := range recs {
		if err := w.Append(rec); err != nil {
			t.Fatalf("writePending: %v", err)
		}
	}
}

// readPending reads all records from .ready/pending.jsonl.
func readPending(t *testing.T, projectDir string) []jsonl.MutationRecord {
	t.Helper()
	pp := filepath.Join(projectDir, ".ready", "pending.jsonl")
	r := jsonl.NewReader(pp)
	recs, err := r.ReadAll()
	if err != nil {
		t.Fatalf("readPending: %v", err)
	}
	return recs
}

// TestMarkSynced_UpdatesCursor verifies that MarkSynced updates the sync cursor.
func TestMarkSynced_UpdatesCursor(t *testing.T) {
	projectDir := setupProject(t)

	now := time.Now().UnixNano()
	msgID := "abc123"

	if err := rdSync.MarkSynced(projectDir, msgID, now); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	state, err := rdSync.LoadState(projectDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if state.LastSyncedMsgID != msgID {
		t.Errorf("LastSyncedMsgID: got %q, want %q", state.LastSyncedMsgID, msgID)
	}
	if state.LastSyncedAt != now {
		t.Errorf("LastSyncedAt: got %d, want %d", state.LastSyncedAt, now)
	}
}

// TestMarkSynced_OverwritesPreviousCursor verifies that calling MarkSynced twice
// updates the cursor to the most recent message.
func TestMarkSynced_OverwritesPreviousCursor(t *testing.T) {
	projectDir := setupProject(t)

	t1 := time.Now().UnixNano()
	t2 := t1 + int64(time.Second)

	if err := rdSync.MarkSynced(projectDir, "first-msg", t1); err != nil {
		t.Fatalf("first MarkSynced: %v", err)
	}
	if err := rdSync.MarkSynced(projectDir, "second-msg", t2); err != nil {
		t.Fatalf("second MarkSynced: %v", err)
	}

	state, err := rdSync.LoadState(projectDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	if state.LastSyncedMsgID != "second-msg" {
		t.Errorf("LastSyncedMsgID: got %q, want second-msg", state.LastSyncedMsgID)
	}
	if state.LastSyncedAt != t2 {
		t.Errorf("LastSyncedAt: got %d, want %d", state.LastSyncedAt, t2)
	}
}

// TestFlushPending_EmptyPending verifies that FlushPending on an empty pending.jsonl
// returns 0 flushed and no error.
func TestFlushPending_EmptyPending(t *testing.T) {
	projectDir := setupProject(t)

	called := 0
	flush := func(rec jsonl.MutationRecord) error {
		called++
		return nil
	}

	flushed, err := rdSync.FlushPending(projectDir, flush)
	if err != nil {
		t.Fatalf("FlushPending on empty: %v", err)
	}
	if flushed != 0 {
		t.Errorf("flushed: got %d, want 0", flushed)
	}
	if called != 0 {
		t.Errorf("flush called %d times, want 0", called)
	}
}

// TestFlushPending_FlushesAllRecords verifies that FlushPending sends all
// pending records and truncates pending.jsonl on success.
func TestFlushPending_FlushesAllRecords(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	recs := []jsonl.MutationRecord{
		makeRecord("msg-1", "work:create", ts),
		makeRecord("msg-2", "work:claim", ts+1),
		makeRecord("msg-3", "work:close", ts+2),
	}
	writePending(t, projectDir, recs)

	var sentIDs []string
	flush := func(rec jsonl.MutationRecord) error {
		sentIDs = append(sentIDs, rec.MsgID)
		return nil
	}

	flushed, err := rdSync.FlushPending(projectDir, flush)
	if err != nil {
		t.Fatalf("FlushPending: %v", err)
	}
	if flushed != 3 {
		t.Errorf("flushed: got %d, want 3", flushed)
	}

	// Verify order preserved.
	for i, id := range []string{"msg-1", "msg-2", "msg-3"} {
		if i >= len(sentIDs) {
			t.Fatalf("only %d records sent, want 3", len(sentIDs))
		}
		if sentIDs[i] != id {
			t.Errorf("sent order[%d]: got %q, want %q", i, sentIDs[i], id)
		}
	}

	// pending.jsonl should now be empty (truncated).
	remaining := readPending(t, projectDir)
	if len(remaining) != 0 {
		t.Errorf("pending.jsonl has %d records after full flush, want 0", len(remaining))
	}
}

// TestFlushPending_PartialFlush verifies that a transport failure mid-flush
// leaves unflushed records in pending.jsonl.
func TestFlushPending_PartialFlush(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	recs := []jsonl.MutationRecord{
		makeRecord("msg-1", "work:create", ts),
		makeRecord("msg-2", "work:claim", ts+1),
		makeRecord("msg-3", "work:close", ts+2),
	}
	writePending(t, projectDir, recs)

	sentCount := 0
	flush := func(rec jsonl.MutationRecord) error {
		if sentCount >= 1 {
			return fmt.Errorf("transport failure")
		}
		sentCount++
		return nil
	}

	flushed, err := rdSync.FlushPending(projectDir, flush)
	if err == nil {
		t.Error("expected error from partial flush, got nil")
	}
	if flushed != 1 {
		t.Errorf("flushed: got %d, want 1", flushed)
	}

	// pending.jsonl should contain only the unflushed records (msg-2, msg-3).
	remaining := readPending(t, projectDir)
	if len(remaining) != 2 {
		t.Errorf("pending.jsonl has %d records after partial flush, want 2", len(remaining))
	}
	if len(remaining) >= 1 && remaining[0].MsgID != "msg-2" {
		t.Errorf("remaining[0]: got %q, want msg-2", remaining[0].MsgID)
	}
	if len(remaining) >= 2 && remaining[1].MsgID != "msg-3" {
		t.Errorf("remaining[1]: got %q, want msg-3", remaining[1].MsgID)
	}
}

// TestFlushPending_UpdatesSyncCursor verifies that FlushPending updates the
// sync cursor for each successfully flushed record.
func TestFlushPending_UpdatesSyncCursor(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	recs := []jsonl.MutationRecord{
		makeRecord("msg-1", "work:create", ts),
		makeRecord("msg-2", "work:claim", ts+1000),
	}
	writePending(t, projectDir, recs)

	flush := func(rec jsonl.MutationRecord) error { return nil }

	flushed, err := rdSync.FlushPending(projectDir, flush)
	if err != nil {
		t.Fatalf("FlushPending: %v", err)
	}
	if flushed != 2 {
		t.Errorf("flushed: got %d, want 2", flushed)
	}

	state, err := rdSync.LoadState(projectDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}

	// Cursor should reflect the last flushed record.
	if state.LastSyncedMsgID != "msg-2" {
		t.Errorf("LastSyncedMsgID: got %q, want msg-2", state.LastSyncedMsgID)
	}
	if state.LastSyncedAt != ts+1000 {
		t.Errorf("LastSyncedAt: got %d, want %d", state.LastSyncedAt, ts+1000)
	}
}

// TestGetStatus_ReportsPendingCount verifies that GetStatus correctly reports
// the pending mutation count.
func TestGetStatus_ReportsPendingCount(t *testing.T) {
	projectDir := setupProject(t)

	// No pending — count should be 0.
	status, err := rdSync.GetStatus(projectDir)
	if err != nil {
		t.Fatalf("GetStatus (empty): %v", err)
	}
	if status.PendingCount != 0 {
		t.Errorf("PendingCount (empty): got %d, want 0", status.PendingCount)
	}
	if status.HasSynced {
		t.Error("HasSynced should be false before any sync")
	}

	// Add pending records.
	ts := time.Now().UnixNano()
	writePending(t, projectDir, []jsonl.MutationRecord{
		makeRecord("m1", "work:create", ts),
		makeRecord("m2", "work:claim", ts+1),
	})

	status, err = rdSync.GetStatus(projectDir)
	if err != nil {
		t.Fatalf("GetStatus (with pending): %v", err)
	}
	if status.PendingCount != 2 {
		t.Errorf("PendingCount: got %d, want 2", status.PendingCount)
	}
}

// TestGetStatus_ReportsLastSyncTime verifies that GetStatus correctly reports
// the last sync time after MarkSynced is called.
func TestGetStatus_ReportsLastSyncTime(t *testing.T) {
	projectDir := setupProject(t)

	// Before any sync.
	status, err := rdSync.GetStatus(projectDir)
	if err != nil {
		t.Fatalf("GetStatus before sync: %v", err)
	}
	if status.HasSynced {
		t.Error("HasSynced should be false before any sync")
	}
	if !status.LastSyncedAt.IsZero() {
		t.Errorf("LastSyncedAt should be zero before sync, got %v", status.LastSyncedAt)
	}

	// Mark a sync.
	now := time.Now().UnixNano()
	if err := rdSync.MarkSynced(projectDir, "msg-xyz", now); err != nil {
		t.Fatalf("MarkSynced: %v", err)
	}

	status, err = rdSync.GetStatus(projectDir)
	if err != nil {
		t.Fatalf("GetStatus after sync: %v", err)
	}
	if !status.HasSynced {
		t.Error("HasSynced should be true after MarkSynced")
	}
	if status.LastSyncedMsgID != "msg-xyz" {
		t.Errorf("LastSyncedMsgID: got %q, want msg-xyz", status.LastSyncedMsgID)
	}
	// Allow 1 nanosecond tolerance for time conversion.
	gotNano := status.LastSyncedAt.UnixNano()
	if gotNano != now {
		t.Errorf("LastSyncedAt nano: got %d, want %d", gotNano, now)
	}
}

// TestFlushPending_PreservesOrder verifies that records are flushed in the
// order they were appended to pending.jsonl (FIFO).
func TestFlushPending_PreservesOrder(t *testing.T) {
	projectDir := setupProject(t)

	// Write records out of timestamp order (simulate parallel writes).
	// FlushPending uses the file order (reader sorts by timestamp),
	// so we write them with ascending timestamps.
	ts := time.Now().UnixNano()
	recs := []jsonl.MutationRecord{
		makeRecord("first", "work:create", ts),
		makeRecord("second", "work:claim", ts+100),
		makeRecord("third", "work:update", ts+200),
		makeRecord("fourth", "work:close", ts+300),
	}
	writePending(t, projectDir, recs)

	var order []string
	flush := func(rec jsonl.MutationRecord) error {
		order = append(order, rec.MsgID)
		return nil
	}

	flushed, err := rdSync.FlushPending(projectDir, flush)
	if err != nil {
		t.Fatalf("FlushPending: %v", err)
	}
	if flushed != 4 {
		t.Errorf("flushed: got %d, want 4", flushed)
	}

	expected := []string{"first", "second", "third", "fourth"}
	for i, want := range expected {
		if i >= len(order) {
			t.Fatalf("order has only %d elements, want %d", len(order), len(expected))
		}
		if order[i] != want {
			t.Errorf("order[%d]: got %q, want %q", i, order[i], want)
		}
	}
}

// TestBufferPending_AppendsToPendingJSONL verifies that BufferPending appends
// a record to .ready/pending.jsonl and that the record is readable.
func TestBufferPending_AppendsToPendingJSONL(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	rec := makeRecord("buffer-test-msg", "work:create", ts)

	if err := rdSync.BufferPending(projectDir, rec); err != nil {
		t.Fatalf("BufferPending: %v", err)
	}

	recs := readPending(t, projectDir)
	if len(recs) != 1 {
		t.Fatalf("pending.jsonl has %d records, want 1", len(recs))
	}
	if recs[0].MsgID != "buffer-test-msg" {
		t.Errorf("MsgID: got %q, want buffer-test-msg", recs[0].MsgID)
	}
}

// TestLoadState_ZeroOnMissing verifies that LoadState returns a zero State
// when sync-state.json does not exist.
func TestLoadState_ZeroOnMissing(t *testing.T) {
	projectDir := setupProject(t)

	state, err := rdSync.LoadState(projectDir)
	if err != nil {
		t.Fatalf("LoadState on missing file: %v", err)
	}
	if state.LastSyncedAt != 0 {
		t.Errorf("LastSyncedAt: got %d, want 0", state.LastSyncedAt)
	}
	if state.LastSyncedMsgID != "" {
		t.Errorf("LastSyncedMsgID: got %q, want empty", state.LastSyncedMsgID)
	}
	if state.PendingCount != 0 {
		t.Errorf("PendingCount: got %d, want 0", state.PendingCount)
	}
}

func TestSaveState_RestrictivePermissions(t *testing.T) {
	projectDir := t.TempDir()

	s := &rdSync.State{LastSyncedAt: 1, LastSyncedMsgID: "msg-abc", PendingCount: 0}
	if err := rdSync.SaveState(projectDir, s); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	readyDir := filepath.Join(projectDir, ".ready")

	// Directory must be owner-only (0700).
	dirInfo, err := os.Stat(readyDir)
	if err != nil {
		t.Fatalf("stat .ready: %v", err)
	}
	if got := dirInfo.Mode().Perm(); got != 0700 {
		t.Errorf(".ready dir permissions: got %04o, want 0700", got)
	}

	// sync-state.json must be owner-only (0600).
	stateFile := filepath.Join(readyDir, "sync-state.json")
	fileInfo, err := os.Stat(stateFile)
	if err != nil {
		t.Fatalf("stat sync-state.json: %v", err)
	}
	if got := fileInfo.Mode().Perm(); got != 0600 {
		t.Errorf("sync-state.json permissions: got %04o, want 0600", got)
	}
}
