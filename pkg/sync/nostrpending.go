// Offline pending buffer flush (ready-797).
//
// When rd mutates an item while every relay is unreachable, the signed events are
// ALWAYS durable in the local authoritative log, and additionally queued in the
// offline relay-retry buffer (nostr-pending.jsonl) by the Publisher. FlushPending
// drains that buffer on reconnect: it re-publishes each buffered event to the
// write relays and drops the ones a relay accepted. Re-publishing is idempotent
// by nostr event id — a relay that already stored the event answers OK,true, so
// flushing twice (or racing two machines) is harmless. Events a relay is still
// unable to accept stay buffered for the next flush.
package sync

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
)

// FlushResult summarises a pending-buffer flush.
type FlushResult struct {
	// Total is how many events were in the buffer at flush time.
	Total int
	// Flushed is how many at least one relay accepted (and were dropped from the buffer).
	Flushed int
	// Remaining is how many still could not reach any relay (kept in the buffer).
	Remaining int
	// RelayErrors holds per-relay publish errors encountered.
	RelayErrors []string
}

// FlushPending re-publishes every event buffered in pendingPath to the write
// relays and rewrites the buffer with only the events that still reached no relay.
// A missing/empty buffer is a no-op. The authoritative log is never touched here —
// the buffer is purely a relay-delivery retry queue.
func FlushNostrPending(ctx context.Context, pendingPath string, relays []string, timeout time.Duration) (FlushResult, error) {
	var res FlushResult
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}

	events, err := readPendingEvents(pendingPath)
	if err != nil {
		return res, err
	}
	res.Total = len(events)
	if len(events) == 0 {
		return res, nil
	}

	var remaining []*nostr.Event
	for _, e := range events {
		anyRelay := false
		for _, relay := range relays {
			pctx, cancel := context.WithTimeout(ctx, timeout)
			accepted, _, perr := nostr.Publish(pctx, relay, e)
			cancel()
			if perr != nil {
				res.RelayErrors = append(res.RelayErrors, fmt.Sprintf("%s: %v", relay, perr))
				continue
			}
			if accepted {
				anyRelay = true
			}
		}
		if anyRelay {
			res.Flushed++
		} else {
			remaining = append(remaining, e)
		}
	}
	res.Remaining = len(remaining)

	if err := rewritePendingEvents(pendingPath, remaining); err != nil {
		return res, err
	}
	return res, nil
}

func readPendingEvents(path string) ([]*nostr.Event, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("sync: open pending: %w", err)
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
			return nil, fmt.Errorf("sync: parse pending: %w", err)
		}
		ev := e
		out = append(out, &ev)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("sync: scan pending: %w", err)
	}
	return out, nil
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
