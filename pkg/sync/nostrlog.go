// Nostr local authoritative signed-event log (ready-a13).
//
// This is the SOURCE OF TRUTH for the rd->nostr migration (epic ready-a14 design
// invariant, 2026-07-06): an append-only JSONL file where each line is a full,
// schnorr-signed nostr.Event. The 30302 card is a projection OF this log; relays
// are replaceable caches that cache-fill INTO this log. rd must work with EVERY
// relay offline, so the log write is the durability guarantee — never the relay.
//
// A lost or rewritten card is fully rebuildable by replaying this log
// (ProjectItems). Reads prefer the local log; a relay query is a
// cache-fill/reconcile, never the authority.
package sync

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/campfire-net/ready/pkg/jsonl"
	"github.com/campfire-net/ready/pkg/nostr"
)

const (
	// NostrLogFile is the append-only signed-event log filename under .ready/.
	NostrLogFile = "nostr-log.jsonl"
	// NostrPendingFile buffers signed events that have not yet reached any relay
	// (offline). They are already durable in the log; this is only the relay
	// publish retry queue.
	NostrPendingFile = "nostr-pending.jsonl"
)

// NostrLog is an append-only, signed-event log persisted as JSONL.
type NostrLog struct {
	path string
}

// NewNostrLog returns a log rooted at path (typically <projectDir>/.ready/nostr-log.jsonl).
func NewNostrLog(path string) *NostrLog { return &NostrLog{path: path} }

// NostrLogPath returns the conventional log path for a project directory.
func NostrLogPath(projectDir string) string {
	return filepath.Join(projectDir, ReadyDir, NostrLogFile)
}

// Path returns the underlying file path.
func (l *NostrLog) Path() string { return l.path }

// Append writes one signed event as a JSON line. It creates the .ready directory
// on demand. The event is NOT re-verified here (callers sign via pkg/nostr); use
// ReadAll+Verify for a trust pass.
func (l *NostrLog) Append(e *nostr.Event) error {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return fmt.Errorf("sync: nostr-log mkdir: %w", err)
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("sync: nostr-log open: %w", err)
	}
	defer f.Close()
	if err := jsonl.LockFile(f); err != nil {
		return fmt.Errorf("sync: nostr-log lock: %w", err)
	}
	defer jsonl.UnlockFile(f) //nolint:errcheck // advisory unlock in defer
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("sync: nostr-log marshal: %w", err)
	}
	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("sync: nostr-log write: %w", err)
	}
	return f.Sync()
}

// AppendUnique appends only events whose id is not already present in the log.
// Returns the number of events actually appended. Used by relay reconcile to
// cache-fill without duplicating events (relays dedupe by id; so does the log).
func (l *NostrLog) AppendUnique(events []*nostr.Event) (int, error) {
	existing, err := l.ReadAll()
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(existing))
	for _, e := range existing {
		known[e.ID] = true
	}
	added := 0
	for _, e := range events {
		if e == nil || known[e.ID] {
			continue
		}
		if err := l.Append(e); err != nil {
			return added, err
		}
		known[e.ID] = true
		added++
	}
	return added, nil
}

// MergeFrom integrates another signed-event log file into this one, appending
// only events not already present (dedup by event id) that pass an independent
// schnorr Verify. It returns the number of events actually added.
//
// This is the DEGRADE FLOOR (epic ready-a14 invariant): with every relay
// unreachable, two machines still converge by exchanging their git-committed
// nostr-log.jsonl files (the "beads-without-dolt" fallback) and merging. Because
// the log is append-only signed events, a git merge of the JSONL plus this
// idempotent, id-deduped, verify-gated merge yields the union with zero data loss
// — no relay required. Forged or tampered lines in the other file are rejected.
func (l *NostrLog) MergeFrom(otherPath string) (int, error) {
	other := NewNostrLog(otherPath)
	evs, err := other.ReadAll()
	if err != nil {
		return 0, err
	}
	verified := make([]*nostr.Event, 0, len(evs))
	for _, e := range evs {
		if e == nil {
			continue
		}
		if err := e.Verify(); err != nil {
			continue // trust gate: never merge an event that does not verify
		}
		verified = append(verified, e)
	}
	return l.AppendUnique(verified)
}

// ReadAll reads every signed event from the log in append order. A missing file
// yields an empty slice (fresh / wiped cache), not an error.
func (l *NostrLog) ReadAll() ([]*nostr.Event, error) {
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sync: nostr-log open: %w", err)
	}
	defer f.Close()

	var out []*nostr.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e nostr.Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, fmt.Errorf("sync: nostr-log parse: %w", err)
		}
		ev := e
		out = append(out, &ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sync: nostr-log scan: %w", err)
	}
	return out, nil
}
