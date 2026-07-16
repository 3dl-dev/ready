package sync

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestFlushNostrPending_MalformedLineDoesNotWedgeQueue is the ready-e52
// reproduction: a single unparseable line in nostr-pending.jsonl used to make
// readPendingEvents return an error, aborting FlushNostrPending before ANY event
// was attempted — every valid event queued behind the garbage line was blocked
// forever (head-of-line block). The fix skips + quarantines the bad line and
// flushes the rest.
func TestFlushNostrPending_MalformedLineDoesNotWedgeQueue(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, _ := readyPaths(dir)
	corrupt := filepath.Join(dir, ".ready", NostrCorruptFile)

	evGood := signedEvent(t, k, "ok") // accepted -> should flush despite the bad line ahead of it

	// Seed the buffer with a MALFORMED line at the HEAD, then a valid event.
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	if err := os.WriteFile(pending, []byte("{not valid json at all\n"), 0o600); err != nil {
		t.Fatalf("seed malformed line: %v", err)
	}
	if err := appendPendingEvent(pending, evGood); err != nil {
		t.Fatalf("seed good event: %v", err)
	}

	res, err := FlushNostrPending(context.Background(), pending, []string{contentRelay(t)}, 3*time.Second)
	if err != nil {
		t.Fatalf("FlushNostrPending must not error on a malformed line: %v", err)
	}

	// The valid event behind the garbage line must have flushed.
	if res.Flushed != 1 {
		t.Errorf("expected the valid event to flush past the malformed line; Flushed=%d", res.Flushed)
	}
	if fileHasID(t, pending, evGood.ID) {
		t.Errorf("valid event still in pending.jsonl — the malformed line wedged the queue (ready-e52)")
	}
	// The malformed line must be quarantined, not silently lost and not left to
	// re-wedge the queue on the next flush.
	if res.Corrupt != 1 {
		t.Errorf("expected 1 quarantined corrupt line, got Corrupt=%d", res.Corrupt)
	}
	cdata, _ := os.ReadFile(corrupt)
	if len(cdata) == 0 {
		t.Errorf("malformed line was not quarantined to %s", NostrCorruptFile)
	}
	// The corrupt line must be gone from the pending buffer so it does not
	// re-quarantine forever.
	pdata, _ := os.ReadFile(pending)
	if len(pdata) != 0 {
		t.Errorf("pending buffer should be empty after flush; still has: %s", pdata)
	}
}

// TestFlushNostrPending_OnlyMalformedLine_ClearsBuffer covers the degenerate
// case: a buffer that is nothing but a corrupt line must be drained (quarantined
// then removed) rather than left to re-quarantine on every flush.
func TestFlushNostrPending_OnlyMalformedLine_ClearsBuffer(t *testing.T) {
	dir := t.TempDir()
	pending, _ := readyPaths(dir)
	if err := os.MkdirAll(filepath.Dir(pending), 0o700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	if err := os.WriteFile(pending, []byte("garbage\n"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := FlushNostrPending(context.Background(), pending, []string{contentRelay(t)}, 3*time.Second)
	if err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if res.Corrupt != 1 {
		t.Errorf("Corrupt=%d, want 1", res.Corrupt)
	}
	if _, err := os.Stat(pending); !os.IsNotExist(err) {
		t.Errorf("pending buffer should have been removed after quarantining the only (corrupt) line")
	}
}
