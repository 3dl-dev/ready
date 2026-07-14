// Package sync provides outbound sync logic for the ready work management convention.
//
// Outbound sync is the process of posting locally-written JSONL mutations to a
// campfire after they have been durably written. The flow:
//
//  1. rd command writes mutation to .ready/mutations.jsonl (always, fatal on failure).
//  2. rd command attempts campfire send. On success, calls MarkSynced.
//  3. On campfire failure, mutation is appended to .ready/pending.jsonl.
//  4. On any successful campfire send, FlushPending is called to retry buffered mutations.
//
// This package is pure logic — it does not import cmd/rd packages and does not
// interact with campfire transports directly. Callers (cmd/rd/send.go) handle
// the campfire send; this package handles the sync state and pending buffer.
package sync

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3dl-dev/ready/pkg/jsonl"
)

const (
	// SyncStateFile is the filename for the sync state (last sync time, cursor).
	SyncStateFile = "sync-state.json"

	// PendingFile is the filename for the pending mutations buffer.
	PendingFile = "pending.jsonl"

	// ReadyDir is the .ready directory name under the project root.
	ReadyDir = ".ready"
)

// State holds the current sync state, persisted to .ready/sync-state.json.
type State struct {
	// LastSyncedAt is the Unix nanosecond timestamp of the most recently
	// synced mutation. Zero means nothing has been synced yet.
	LastSyncedAt int64 `json:"last_synced_at"`

	// LastSyncedMsgID is the message ID of the most recently synced mutation.
	LastSyncedMsgID string `json:"last_synced_msg_id,omitempty"`

	// PendingCount is the number of mutations currently buffered in pending.jsonl.
	// This is a cached count — authoritative value comes from counting pending.jsonl lines.
	PendingCount int `json:"pending_count"`

	// LastPullAt is the Unix nanosecond timestamp of the most recent inbound pull.
	// Zero means no pull has been performed yet.
	LastPullAt int64 `json:"last_pull_at,omitempty"`
}

// LoadState reads the sync state from .ready/sync-state.json in projectDir.
// Returns a zero State if the file does not exist.
func LoadState(projectDir string) (*State, error) {
	path := statePath(projectDir)
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return &State{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sync: reading state: %w", err)
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, fmt.Errorf("sync: parsing state: %w", err)
	}
	return &s, nil
}

// SaveState writes the sync state to .ready/sync-state.json in projectDir.
func SaveState(projectDir string, s *State) error {
	dir := filepath.Join(projectDir, ReadyDir)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return fmt.Errorf("sync: mkdir %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return fmt.Errorf("sync: encoding state: %w", err)
	}
	return os.WriteFile(statePath(projectDir), append(data, '\n'), 0600)
}

// MarkSynced updates the sync cursor after a successful campfire send.
// projectDir is the project root (directory containing .ready/).
// msgID is the campfire message ID of the successfully sent mutation.
// timestamp is the mutation timestamp (nanoseconds since Unix epoch).
func MarkSynced(projectDir, msgID string, timestamp int64) error {
	s, err := LoadState(projectDir)
	if err != nil {
		// Non-fatal: if we can't load state, try to write a fresh one.
		s = &State{}
	}
	s.LastSyncedAt = timestamp
	s.LastSyncedMsgID = msgID
	// Recount pending on each mark-synced so the cached count stays accurate.
	s.PendingCount = CountPending(projectDir)
	return SaveState(projectDir, s)
}

// BufferPending appends a mutation record to .ready/pending.jsonl.
// Returns an error if the write fails. Callers log the error as a warning —
// the primary mutation already succeeded in mutations.jsonl.
func BufferPending(projectDir string, rec jsonl.MutationRecord) error {
	pp := filepath.Join(projectDir, ReadyDir, PendingFile)
	w := jsonl.NewWriter(pp)
	if err := w.Append(rec); err != nil {
		return fmt.Errorf("sync: buffering to pending.jsonl: %w", err)
	}
	return nil
}

// CountPending returns the number of mutation records in .ready/pending.jsonl.
// Returns 0 if the file does not exist or cannot be read.
func CountPending(projectDir string) int {
	pp := filepath.Join(projectDir, ReadyDir, PendingFile)
	r := jsonl.NewReader(pp)
	recs, err := r.ReadAll()
	if err != nil {
		return 0
	}
	return len(recs)
}

// Status holds the sync status for display by `rd sync status`.
type Status struct {
	// PendingCount is the number of mutations buffered in pending.jsonl.
	PendingCount int `json:"pending_count"`

	// LastSyncedAt is the time of the most recent successful sync.
	// Zero value means nothing has been synced.
	LastSyncedAt time.Time `json:"last_synced_at,omitempty"`

	// LastSyncedMsgID is the message ID of the most recently synced mutation.
	LastSyncedMsgID string `json:"last_synced_msg_id,omitempty"`

	// HasSynced is true if at least one mutation has been successfully synced.
	HasSynced bool `json:"has_synced"`
}

// GetStatus returns the current sync status for projectDir.
func GetStatus(projectDir string) (*Status, error) {
	s, err := LoadState(projectDir)
	if err != nil {
		return nil, fmt.Errorf("sync: loading state: %w", err)
	}

	// Always recount pending from the file — the cached count may be stale.
	pendingCount := CountPending(projectDir)

	status := &Status{
		PendingCount:    pendingCount,
		LastSyncedMsgID: s.LastSyncedMsgID,
		HasSynced:       s.LastSyncedAt > 0,
	}
	if s.LastSyncedAt > 0 {
		status.LastSyncedAt = time.Unix(0, s.LastSyncedAt)
	}
	return status, nil
}

// Flusher is a function that sends a single pending mutation to campfire.
// Returns nil on success, non-nil on failure.
// The implementation lives in cmd/rd (which has access to campfire transport).
type Flusher func(rec jsonl.MutationRecord) error

// FlushPending reads .ready/pending.jsonl and calls flush for each record in order.
// On success for each record, it updates the sync cursor. After all records are
// processed, the successfully flushed records are removed from pending.jsonl.
//
// FlushPending is fire-and-forget with respect to the caller: it returns the
// number of records flushed and any error that stopped the flush early.
// A partial flush (some records sent, transport fails mid-way) leaves the
// unflushed records in pending.jsonl.
//
// projectDir is the project root. flush is called for each pending record.
func FlushPending(projectDir string, flush Flusher) (flushed int, err error) {
	pp := filepath.Join(projectDir, ReadyDir, PendingFile)
	r := jsonl.NewReader(pp)
	recs, err := r.ReadAll()
	if err != nil {
		return 0, fmt.Errorf("sync: reading pending.jsonl: %w", err)
	}
	if len(recs) == 0 {
		return 0, nil
	}

	var remaining []jsonl.MutationRecord
	var lastErr error

	for _, rec := range recs {
		if sendErr := flush(rec); sendErr != nil {
			// Stop on first failure — preserve order, don't skip.
			lastErr = sendErr
			remaining = append(remaining, rec)
			// Collect all remaining records too.
			remaining = append(remaining, recs[flushed+1:]...)
			break
		}
		flushed++
		// Update sync cursor after each successful send.
		if markErr := MarkSynced(projectDir, rec.MsgID, rec.Timestamp); markErr != nil {
			// Non-fatal: sync cursor update failed, but the send succeeded.
			fmt.Fprintf(os.Stderr, "warning: sync: could not update sync cursor: %v\n", markErr)
		}
	}

	if lastErr == nil {
		// All records flushed — truncate pending.jsonl.
		if err := truncatePending(pp); err != nil {
			return flushed, fmt.Errorf("sync: truncating pending.jsonl: %w", err)
		}
	} else if flushed > 0 {
		// Partial flush — rewrite pending.jsonl with only the remaining records.
		if err := rewritePending(pp, remaining); err != nil {
			return flushed, fmt.Errorf("sync: rewriting pending.jsonl after partial flush: %w", err)
		}
	}

	return flushed, lastErr
}

// truncatePending truncates pending.jsonl to zero bytes (all records flushed).
func truncatePending(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_TRUNC, 0600)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("sync: opening pending.jsonl for truncation: %w", err)
	}
	defer f.Close()
	// Acquire exclusive lock during truncation (platform-specific).
	if err := jsonl.LockFile(f); err != nil {
		return fmt.Errorf("sync: flock pending.jsonl: %w", err)
	}
	defer jsonl.UnlockFile(f) //nolint:errcheck // advisory unlock in defer; error cannot be acted on at this point
	return f.Sync()
}

// rewritePending atomically rewrites pending.jsonl with the given records.
// Uses a write-to-temp-then-rename pattern for atomicity.
func rewritePending(path string, recs []jsonl.MutationRecord) error {
	dir := filepath.Dir(path)
	tmp, err := os.CreateTemp(dir, "pending-*.jsonl.tmp")
	if err != nil {
		return fmt.Errorf("sync: creating temp file: %w", err)
	}
	tmpPath := tmp.Name()
	// Ensure restrictive permissions (os.CreateTemp may use 0600, but we set
	// explicitly to guarantee no group/other bits regardless of platform or umask).
	if err := os.Chmod(tmpPath, 0600); err != nil {
		tmp.Close()
		os.Remove(tmpPath) //nolint:errcheck
		return fmt.Errorf("sync: chmod temp file: %w", err)
	}
	defer func() {
		tmp.Close()
		// Clean up temp file on error (rename will have moved it on success).
		os.Remove(tmpPath) //nolint:errcheck // best-effort cleanup; file may already be gone after successful rename
	}()

	w := bufio.NewWriter(tmp)
	enc := json.NewEncoder(w)
	for _, rec := range recs {
		if err := enc.Encode(rec); err != nil {
			return fmt.Errorf("sync: encoding pending record: %w", err)
		}
	}
	if err := w.Flush(); err != nil {
		return fmt.Errorf("sync: flushing pending.jsonl: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		return fmt.Errorf("sync: fsync pending.jsonl: %w", err)
	}
	tmp.Close()

	if err := os.Rename(tmpPath, path); err != nil {
		return fmt.Errorf("sync: renaming pending.jsonl: %w", err)
	}
	return nil
}

// statePath returns the path to sync-state.json within projectDir.
func statePath(projectDir string) string {
	return filepath.Join(projectDir, ReadyDir, SyncStateFile)
}
