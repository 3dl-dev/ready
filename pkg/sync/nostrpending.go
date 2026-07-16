// Offline pending buffer flush (ready-797).
//
// When rd mutates an item while every relay is unreachable, the signed events are
// ALWAYS durable in the local authoritative log, and additionally queued in the
// offline relay-retry buffer (nostr-pending.jsonl) by the Publisher.
// FlushNostrPending drains that buffer on reconnect: it re-publishes each
// buffered event to the write relays and drops the ones a relay accepted.
// Re-publishing is idempotent by nostr event id — a relay that already stored the
// event answers OK,true, so flushing twice (or racing two machines) is harmless.
// An event a relay PERMANENTLY refuses (invalid:/restricted:) is dead-lettered to
// nostr-rejected.jsonl instead of being retried forever (ready-1c2); a purely
// transient failure stays buffered for the next flush.
package sync

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3dl-dev/ready/pkg/jsonl"
	"github.com/3dl-dev/ready/pkg/nostr"
)

// lockPending acquires an exclusive advisory lock serializing all access to the
// pending buffer. It locks a SEPARATE, never-removed lock file (not the data file,
// whose rewrite/os.Remove would otherwise break the flock across a new inode), so
// a concurrent appendPendingEvent and FlushNostrPending in two rd processes
// sharing one .ready dir cannot interleave and drop a just-buffered event. Returns
// an unlock func the caller must defer.
func lockPending(pendingPath string) (func(), error) {
	lockPath := pendingPath + ".lock"
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := jsonl.LockFile(f); err != nil {
		f.Close()
		return nil, err
	}
	return func() {
		_ = jsonl.UnlockFile(f)
		_ = f.Close()
	}, nil
}

// FlushResult summarises a pending-buffer flush.
type FlushResult struct {
	// Total is how many events were in the buffer at flush time.
	Total int
	// Flushed is how many at least one relay accepted (and were dropped from the buffer).
	Flushed int
	// Remaining is how many still could not reach any relay for a TRANSIENT reason
	// (kept in the buffer for the next flush).
	Remaining int
	// Rejected is how many were PERMANENTLY refused by a relay and dead-lettered
	// to nostr-rejected.jsonl (ready-1c2). They are removed from the retry queue
	// so a poisoned record can never block the buffer forever.
	Rejected int
	// RejectReasons holds the relay message for each dead-lettered event.
	RejectReasons []string
	// Corrupt is how many buffer lines could not be parsed as a nostr event and
	// were quarantined to nostr-corrupt.jsonl (ready-e52) rather than aborting the
	// flush. The underlying events remain durable in the authoritative log.
	Corrupt int
	// RelayErrors holds per-relay publish (transport) errors encountered.
	RelayErrors []string
	// WriteErrors holds LOCAL filesystem errors from dead-lettering (e.g. writing
	// nostr-rejected.jsonl failed). Kept distinct from RelayErrors so an operator
	// is not sent chasing relay connectivity for a local disk fault.
	WriteErrors []string
}

// FlushNostrPending re-publishes every event buffered in pendingPath to the write
// relays and rewrites the buffer with only the events that still reached no relay.
// A missing/empty buffer is a no-op. The authoritative log is never touched here —
// the buffer is purely a relay-delivery retry queue.
func FlushNostrPending(ctx context.Context, pendingPath string, relays []string, timeout time.Duration) (FlushResult, error) {
	var res FlushResult
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}

	// Serialize the read+publish+rewrite against concurrent appends so a buffered
	// event added mid-flush is never clobbered by the rewrite (ready review [6]).
	unlock, err := lockPending(pendingPath)
	if err != nil {
		return res, err
	}
	defer unlock()

	events, corrupt, err := readPendingEvents(pendingPath)
	if err != nil {
		return res, err
	}

	// Quarantine unparseable lines first so they cannot wedge the queue
	// (ready-e52). They can never be published; move them to nostr-corrupt.jsonl
	// and let the buffer rewrite (event-based) drop them. On quarantine WRITE
	// failure we still drop the line — keeping an unpublishable line in the retry
	// buffer would re-introduce the head-of-line block, and the underlying event
	// is already durable in the authoritative log regardless.
	if len(corrupt) > 0 {
		cp := corruptPathFor(pendingPath)
		for _, raw := range corrupt {
			if cerr := appendCorruptLine(cp, raw); cerr != nil {
				res.WriteErrors = append(res.WriteErrors, fmt.Sprintf("quarantine corrupt line: %v", cerr))
			}
			res.Corrupt++
		}
		fmt.Fprintf(os.Stderr, "warning: sync: skipped %d unparseable line(s) in %s — quarantined to %s (already durable in the log)\n",
			res.Corrupt, NostrPendingFile, NostrCorruptFile)
	}

	res.Total = len(events)
	if len(events) == 0 {
		// Still must rewrite the buffer if we quarantined lines, so the corrupt
		// lines don't linger and re-quarantine on every flush.
		if len(corrupt) > 0 {
			if rerr := rewritePendingEvents(pendingPath, nil); rerr != nil {
				return res, rerr
			}
		}
		return res, nil
	}

	var remaining []*nostr.Event
	var rejected []RejectedRecord
	for _, e := range events {
		attempts, outcome, permReason := publishEventToRelays(ctx, relays, e, timeout)
		for _, a := range attempts {
			if a.Err != nil {
				res.RelayErrors = append(res.RelayErrors, fmt.Sprintf("%s: %v", a.Relay, a.Err))
			}
		}
		switch outcome {
		case outcomeAccepted:
			res.Flushed++
		case outcomePermanent:
			// Permanently refused — dead-letter it so it stops clogging the retry
			// queue forever (ready-1c2). Never blocks the records behind it.
			rejected = append(rejected, RejectedRecord{Event: e, Reason: permReason})
		default: // outcomeTransient
			remaining = append(remaining, e)
		}
	}

	// Persist the dead-letters. A dead-letter WRITE failure is a LOCAL fault (not
	// a relay fault) and must not lose the event, so on failure we keep it in the
	// retry buffer rather than drop it.
	if len(rejected) > 0 {
		rp := rejectedPathFor(pendingPath)
		for _, rec := range rejected {
			if derr := appendRejectedEvent(rp, rec); derr != nil {
				res.WriteErrors = append(res.WriteErrors, fmt.Sprintf("dead-letter %s: %v", rec.Event.ID, derr))
				remaining = append(remaining, rec.Event)
				continue
			}
			res.Rejected++
			res.RejectReasons = append(res.RejectReasons, rec.Reason)
		}
	}
	res.Remaining = len(remaining)

	if err := rewritePendingEvents(pendingPath, remaining); err != nil {
		return res, err
	}
	return res, nil
}

// readPendingEvents parses the pending buffer. A line that cannot be unmarshaled
// into a nostr event is NOT fatal (ready-e52): it is skipped and returned as a
// raw corrupt line so the caller can quarantine it, instead of aborting the whole
// flush and wedging every valid event queued behind it. Only I/O and scan errors
// (e.g. a line exceeding the buffer cap) are returned as err.
func readPendingEvents(path string) (events []*nostr.Event, corrupt [][]byte, err error) {
	f, oerr := os.Open(path)
	if os.IsNotExist(oerr) {
		return nil, nil, nil
	}
	if oerr != nil {
		return nil, nil, fmt.Errorf("sync: open pending: %w", oerr)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var e nostr.Event
		if uerr := json.Unmarshal(line, &e); uerr != nil {
			// Unparseable — can never be published. Copy it (sc.Bytes() is
			// reused on the next Scan) and hand it back for quarantine.
			corrupt = append(corrupt, append([]byte{}, line...))
			continue
		}
		ev := e
		events = append(events, &ev)
	}
	if serr := sc.Err(); serr != nil {
		return nil, nil, fmt.Errorf("sync: scan pending: %w", serr)
	}
	return events, corrupt, nil
}

// rewritePendingEvents atomically replaces the buffer with the given events. An
// empty slice removes the file entirely.
func rewritePendingEvents(path string, events []*nostr.Event) error {
	if len(events) == 0 {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("sync: clear pending: %w", err)
		}
		return nil
	}
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("sync: rewrite pending: %w", err)
	}
	for _, e := range events {
		data, err := json.Marshal(e)
		if err != nil {
			f.Close()
			return err
		}
		if _, err := f.Write(append(data, '\n')); err != nil {
			f.Close()
			return err
		}
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
