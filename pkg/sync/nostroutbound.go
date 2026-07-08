// Nostr outbound publish (ready-a13).
//
// This is the nostr replacement for the campfire send path. Flow for publishing
// an rd item (the hybrid write, epic ready-a14):
//
//  1. Build + sign the events (board 30301, card 30302, status 1630..) via the
//     wire mapping.
//  2. Append every event to the LOCAL AUTHORITATIVE LOG. This ALWAYS happens and
//     is the durability guarantee — rd must work with every relay offline.
//  3. Publish to write relays, best-effort. Events that reach no relay are
//     buffered to nostr-pending.jsonl for a later flush (offline-buffer
//     semantics, mirroring the existing pending.jsonl). A relay failure NEVER
//     fails the operation: the event is already durable in the log.
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
)

// Publisher publishes rd item events to the local log and to write relays.
type Publisher struct {
	Key         *nostr.Key
	Log         *NostrLog
	WriteRelays []string
	// PendingPath is the offline relay-retry buffer (nostr-pending.jsonl). May be
	// empty to disable buffering (events still land in the authoritative log).
	PendingPath string
	// Timeout per relay publish. Zero uses nostr.DefaultTimeout.
	Timeout time.Duration
}

// RelayAck records the per-relay outcome of a publish attempt.
type RelayAck struct {
	Relay    string
	Accepted bool
	Message  string
	Err      string
}

// EventAck records what happened to one signed event.
type EventAck struct {
	EventID string
	Kind    int
	Acks    []RelayAck
	// AnyRelay is true if at least one relay accepted (OK,true).
	AnyRelay bool
}

// PublishResult summarises a PublishItem call.
type PublishResult struct {
	ItemID string
	Events []EventAck
	// Buffered is true if at least one event reached no relay and was buffered.
	Buffered bool
}

// PublishItem materializes an item as board+card+status events, appends them to
// the authoritative log, and publishes to the write relays. createdAt is the
// item's timestamp in unix SECONDS (NIP-01 granularity). board may be nil to
// skip re-publishing the board event.
func (p *Publisher) PublishItem(ctx context.Context, board *BoardSpec, card CardSpec, createdAt int64) (PublishResult, error) {
	var res PublishResult
	res.ItemID = card.ItemID

	var events []*nostr.Event
	if board != nil {
		be, err := BuildBoardEvent(p.Key, *board, createdAt)
		if err != nil {
			return res, err
		}
		events = append(events, be)
	}
	ce, err := BuildCardEvent(p.Key, card, createdAt)
	if err != nil {
		return res, err
	}
	events = append(events, ce)

	se, err := BuildStatusEvent(p.Key, card.ItemID, card.Status, ce.ID, "", createdAt)
	if err != nil {
		return res, err
	}
	events = append(events, se)

	return p.publishEvents(ctx, res, events)
}

// PublishStatusChange publishes a NIP-34 status event (with optional close/change
// reason) for an existing item, plus a refreshed card materializing the new
// current state. This is how status transitions (claim/done/fail/...) ride the
// hybrid model: history via the status event, current state via the card.
func (p *Publisher) PublishStatusChange(ctx context.Context, card CardSpec, reason string, createdAt int64) (PublishResult, error) {
	var res PublishResult
	res.ItemID = card.ItemID

	ce, err := BuildCardEvent(p.Key, card, createdAt)
	if err != nil {
		return res, err
	}
	se, err := BuildStatusEvent(p.Key, card.ItemID, card.Status, ce.ID, reason, createdAt)
	if err != nil {
		return res, err
	}
	return p.publishEvents(ctx, res, []*nostr.Event{ce, se})
}

func (p *Publisher) publishEvents(ctx context.Context, res PublishResult, events []*nostr.Event) (PublishResult, error) {
	// Phase 1 — append to the authoritative log. This MUST succeed; it is the
	// durability guarantee independent of any relay.
	for _, e := range events {
		if err := p.Log.Append(e); err != nil {
			return res, fmt.Errorf("sync: appending to authoritative log: %w", err)
		}
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}

	// Phase 2 — publish to write relays, best-effort.
	for _, e := range events {
		ack := EventAck{EventID: e.ID, Kind: e.Kind}
		for _, relay := range p.WriteRelays {
			rctx, cancel := context.WithTimeout(ctx, timeout)
			accepted, msg, err := nostr.Publish(rctx, relay, e)
			cancel()
			ra := RelayAck{Relay: relay, Accepted: accepted, Message: msg}
			if err != nil {
				ra.Err = err.Error()
			}
			if accepted {
				ack.AnyRelay = true
			}
			ack.Acks = append(ack.Acks, ra)
		}
		if !ack.AnyRelay {
			// Reached no relay — buffer for later flush. Already durable in the log.
			res.Buffered = true
			if p.PendingPath != "" {
				if bufErr := appendPendingEvent(p.PendingPath, e); bufErr != nil {
					fmt.Fprintf(os.Stderr, "warning: sync: buffering nostr event to pending: %v\n", bufErr)
				}
			}
		}
		res.Events = append(res.Events, ack)
	}
	return res, nil
}

// appendPendingEvent appends a signed event to the nostr pending buffer (JSONL).
func appendPendingEvent(path string, e *nostr.Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
