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
	"io"
	"os"
	"path/filepath"
	"time"

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

	// MaxCreatedAtSkew bounds how far into the FUTURE an inbound (relay-reconciled,
	// git-merged, or negentropy-synced) event's created_at may be relative to the
	// admitting machine's wall clock (ready-f92 skew/monotonicity bound).
	//
	// Latest-wins projection keys on created_at (seconds). A validly-signed event
	// with an implausibly far-future created_at would otherwise win forever and be
	// unbeatable by honest edits — a trivial state-pin/replay attack, or the same
	// effect from a grossly clock-skewed writer. AppendUnique rejects any inbound
	// event stamped beyond now+MaxCreatedAtSkew, so it never reaches the local
	// authoritative log and never influences projection.
	//
	// The bound is applied ONLY at ingestion (a point-in-time admission decision),
	// never at projection: once an event is in the log, ProjectItems stays a PURE
	// function of the event set, so two machines projecting the same log always
	// converge (skew-filtering at projection would make the result depend on each
	// machine's read-time wall clock). Local self-writes go through Append (not
	// AppendUnique) and are trusted against the local clock by construction.
	MaxCreatedAtSkew = 15 * time.Minute
)

// admissibleCreatedAt reports whether an inbound event may enter the local log
// under the future-skew bound: its created_at must not exceed now+MaxCreatedAtSkew.
// Past-dated events are always admissible (staleness is handled deterministically
// by latest-wins + the id tie-break at projection, not by dropping old events).
func admissibleCreatedAt(e *nostr.Event, now time.Time) bool {
	return e.CreatedAt <= now.Add(MaxCreatedAtSkew).Unix()
}

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

// AppendUnique appends only events whose id is not already present in the log, and
// whose created_at is within the future-skew bound. Returns the number of events
// actually appended. This is the single admission choke point for every inbound
// merge from an untrusted source — relay reconcile, git-JSONL MergeFrom, and NIP-77
// negentropy download all funnel through here — so it enforces two ingestion-time
// invariants (ready-f92):
//
//   - DEDUP by event id: re-ingesting an already-known event is a no-op (relays
//     dedupe by id; so does the log). This also gives replay idempotence — refeeding
//     a stale-but-valid event that is already in the log adds nothing.
//   - FUTURE-SKEW REJECTION: an event stamped implausibly far in the future
//     (now+MaxCreatedAtSkew) is dropped so it cannot pin latest-wins state. Skew is
//     read once at the start of the pass so a single call is internally consistent.
//
// CONCURRENCY (ready-187): the read-existing → decide-new → write sequence is a
// single ATOMIC critical section guarded by one exclusive advisory lock held for the
// WHOLE call. The prior implementation read the log (ReadAll, its own open/close) and
// THEN wrote via per-event Append (each its own open+lock) — so two concurrent
// AppendUnique calls both read the SAME "known" set, both decided the same event was
// novel, and both appended it: a duplicate line (and a phantom-history replay). Now
// the dedup snapshot and the appends happen under ONE flock, so a second writer
// blocks until the first fully commits, then reads the first writer's appends and
// dedups correctly. flock is per-open-file-description, so it serializes concurrent
// goroutines (each with its own open) AND concurrent processes.
func (l *NostrLog) AppendUnique(events []*nostr.Event) (int, error) {
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return 0, fmt.Errorf("sync: nostr-log mkdir: %w", err)
	}
	// O_RDWR so we can read the current contents under the SAME lock we write with;
	// O_APPEND so every write lands at EOF regardless of the read seek offset.
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_RDWR|os.O_APPEND, 0o600)
	if err != nil {
		return 0, fmt.Errorf("sync: nostr-log open: %w", err)
	}
	defer f.Close()
	if err := jsonl.LockFile(f); err != nil {
		return 0, fmt.Errorf("sync: nostr-log lock: %w", err)
	}
	defer jsonl.UnlockFile(f) //nolint:errcheck // advisory unlock in defer

	// Snapshot the current log UNDER THE LOCK so no concurrent writer can slip an
	// append between our read and our writes. Seek to 0 for the read; O_APPEND keeps
	// subsequent writes at EOF. A corrupt line is skipped (durability invariant) — it
	// simply is not in the known set, matching projection which drops it too.
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("sync: nostr-log seek: %w", err)
	}
	existing, _, err := scanEvents(f, l.path)
	if err != nil {
		return 0, err
	}
	known := make(map[string]bool, len(existing))
	for _, e := range existing {
		known[e.ID] = true
	}

	now := time.Now()
	added := 0
	for _, e := range events {
		if e == nil || known[e.ID] {
			continue
		}
		if !admissibleCreatedAt(e, now) {
			continue // far-future created_at — reject (skew/replay defense)
		}
		data, err := json.Marshal(e)
		if err != nil {
			return added, fmt.Errorf("sync: nostr-log marshal: %w", err)
		}
		data = append(data, '\n')
		if _, err := f.Write(data); err != nil {
			return added, fmt.Errorf("sync: nostr-log write: %w", err)
		}
		known[e.ID] = true
		added++
	}
	if added > 0 {
		if err := f.Sync(); err != nil {
			return added, fmt.Errorf("sync: nostr-log sync: %w", err)
		}
	}
	return added, nil
}

// MergeFrom integrates another signed-event log file into this one, appending
// only events not already present (dedup by event id) that pass an independent
// schnorr Verify AND the web-of-trust author gate. It returns the number of events
// actually added.
//
// This is the DEGRADE FLOOR (epic ready-a14 invariant): with every relay
// unreachable, two machines still converge by exchanging their git-committed
// nostr-log.jsonl files (the "beads-without-dolt" fallback) and merging. Because
// the log is append-only signed events, a git merge of the JSONL plus this
// idempotent, id-deduped, verify-gated merge yields the union with zero data loss
// — no relay required. Forged or tampered lines in the other file are rejected.
//
// TRUST GATE (ready-b57): the OTHER machine's committed log is an untrusted input —
// a foreign or hostile file could carry validly-signed events from a key that was
// never admitted. Verify alone proves consistency, not authorization, so an event
// is admitted ONLY when its author is in the `trusted` allowlist (the SAME TrustSet
// reconcile() and the negentropy download apply). This keeps AppendUnique the
// single admission choke point for every inbound-from-untrusted merge: a
// validly-signed-but-untrusted line in the other file is dropped before it can
// enter the local authoritative log. A nil `trusted` set disables the gate (tests /
// legacy paths); production callers pass at least the self pubkey plus every
// admitted machine (rdconfig.Config.TrustSet), so a legitimately-admitted machine's
// committed log still merges in full.
func (l *NostrLog) MergeFrom(otherPath string, trusted map[string]bool) (int, error) {
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
			continue // trust gate step 1: never merge an event that does not verify
		}
		// Trust gate step 2 (ready-b57): drop validly-signed events from untrusted
		// authors before local-log admission. A nil set disables the gate.
		if trusted != nil && !trusted[e.PubKey] {
			continue
		}
		verified = append(verified, e)
	}
	return l.AppendUnique(verified)
}

// ReadAll reads every signed event from the log in append order. A missing file
// yields an empty slice (fresh / wiped cache), not an error.
//
// DURABILITY INVARIANT (ready-187): the log MUST stay "fully rebuildable by replay"
// even if ONE line is malformed or truncated (a partial write on a crash, a corrupt
// byte on disk, a torn tail). A single bad line therefore SKIPS+reports rather than
// hard-erroring the WHOLE log — otherwise one truncated tail line would make every
// item on the machine unreadable, exactly the opposite of the append-only log's
// durability guarantee. Bad lines are counted and surfaced via CorruptLines, and the
// projection is a pure function of the parseable events (a skipped forged/tampered
// line would have been dropped by Verify at projection anyway). Returns the parsed
// events plus a nil error; callers that need the corrupt count use ReadAllReport.
func (l *NostrLog) ReadAll() ([]*nostr.Event, error) {
	out, _, err := l.ReadAllReport()
	return out, err
}

// ReadAllReport is ReadAll plus the count of skipped corrupt lines. A structural
// read error (open/scan) is still returned as an error; a per-line parse failure is
// NOT — it is skipped and counted, so the log stays replayable past a bad line.
func (l *NostrLog) ReadAllReport() ([]*nostr.Event, int, error) {
	f, err := os.Open(l.path)
	if os.IsNotExist(err) {
		return nil, 0, nil
	}
	if err != nil {
		return nil, 0, fmt.Errorf("sync: nostr-log open: %w", err)
	}
	defer f.Close()
	return scanEvents(f, l.path)
}

// scanEvents parses JSONL nostr events from r. A per-line parse failure is SKIPPED
// and counted (returned as the corrupt count) rather than aborting — this is the
// durability invariant (ready-187): one malformed/truncated line must not make the
// whole log unreadable. A scanner-level error (structural, e.g. an over-long line)
// is returned alongside the good prefix so the caller decides. path is used only for
// the skip warning.
func scanEvents(r io.Reader, path string) ([]*nostr.Event, int, error) {
	var out []*nostr.Event
	corrupt := 0
	lineNo := 0
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		lineNo++
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e nostr.Event
		if err := json.Unmarshal(line, &e); err != nil {
			corrupt++
			fmt.Fprintf(os.Stderr, "warning: sync: nostr-log %s line %d unparseable, skipping: %v\n", path, lineNo, err)
			continue
		}
		ev := e
		out = append(out, &ev)
	}
	if err := sc.Err(); err != nil {
		return out, corrupt, fmt.Errorf("sync: nostr-log scan: %w", err)
	}
	return out, corrupt, nil
}
