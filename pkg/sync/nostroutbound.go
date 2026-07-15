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

	"github.com/3dl-dev/ready/pkg/nostr"
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
// skip re-publishing the board event. Equivalent to PublishItemWithReason with
// reason="" — unchanged behaviour for every existing caller.
func (p *Publisher) PublishItem(ctx context.Context, board *BoardSpec, card CardSpec, createdAt int64) (PublishResult, error) {
	return p.PublishItemWithReason(ctx, board, card, "", createdAt)
}

// PublishItemWithReason is PublishItem plus an explicit NIP-34 status-event
// reason (ready-da7). It exists so the manual `rd nostr publish` path can carry
// an item's already-recorded close/change reason (its last history entry's
// note) through on republish — the create-time and status-change LIVE hooks
// never had this gap (they receive the reason as an explicit argument already);
// only the standalone republish command hand-carried an empty one. Also anchors
// the status event to the item's NIP-34 kind:1621 issue-root event (additive,
// generic-client interop — see BuildStatusEventWithIssueRoot), publishing that
// issue event ONCE per item the first time this (or PublishStatusChange) is
// called for it, never again on subsequent calls.
func (p *Publisher) PublishItemWithReason(ctx context.Context, board *BoardSpec, card CardSpec, reason string, createdAt int64) (PublishResult, error) {
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

	issueID, issueEvent, err := p.ensureIssueEvent(card, createdAt)
	if err != nil {
		return res, err
	}
	if issueEvent != nil {
		events = append(events, issueEvent)
	}

	se, err := BuildStatusEventWithIssueRoot(p.Key, card.ItemID, card.Status, ce.ID, issueID, cardBoardCoord(p.Key, card), reason, createdAt, card.Enc)
	if err != nil {
		return res, err
	}
	events = append(events, se)

	return p.publishEvents(ctx, res, events)
}

// PublishStatusChange publishes a NIP-34 status event (with optional close/change
// reason) for an existing item, plus a refreshed card materializing the new
// current state. This is how status transitions (claim/done/fail/...) ride the
// hybrid model: history via the status event, current state via the card. The
// status event also carries the item's NIP-34 issue-root anchor (ready-da7,
// additive) — see PublishItemWithReason's doc for the one-issue-event-per-item
// invariant.
func (p *Publisher) PublishStatusChange(ctx context.Context, card CardSpec, reason string, createdAt int64) (PublishResult, error) {
	var res PublishResult
	res.ItemID = card.ItemID

	ce, err := BuildCardEvent(p.Key, card, createdAt)
	if err != nil {
		return res, err
	}

	issueID, issueEvent, err := p.ensureIssueEvent(card, createdAt)
	if err != nil {
		return res, err
	}

	se, err := BuildStatusEventWithIssueRoot(p.Key, card.ItemID, card.Status, ce.ID, issueID, cardBoardCoord(p.Key, card), reason, createdAt, card.Enc)
	if err != nil {
		return res, err
	}
	events := []*nostr.Event{ce}
	if issueEvent != nil {
		events = append(events, issueEvent)
	}
	events = append(events, se)
	return p.publishEvents(ctx, res, events)
}

// ensureIssueEvent returns the event id of card.ItemID's NIP-34 issue-root event
// (ready-da7), building+signing a NEW one for the caller to append+publish only
// when the item doesn't already have one in the local authoritative log. This
// keeps the interop anchor to exactly ONE extra event per item's lifetime — not
// one per status change/republish. Returns ("", nil, err) on a log read error;
// the caller then fails the whole publish (matching every other build-step
// error in this file) rather than silently skipping the anchor.
func (p *Publisher) ensureIssueEvent(card CardSpec, createdAt int64) (issueID string, newEvent *nostr.Event, err error) {
	// CONFIDENTIAL boards (ready-216): the NIP-34 kind:1621 issue event carries the
	// title (clear "subject" tag) and description (clear Content) for generic NIP-34
	// client interop. On a confidential board that would leak exactly the two most
	// sensitive free-text fields the envelope seals into the card — and a generic
	// client cannot read confidential content anyway — so suppress the issue-event
	// anchor entirely. The status event then simply carries no issue-root "e" tag
	// (BuildStatusEventWithIssueRoot treats issueEventID="" as "no anchor").
	if card.Enc != nil {
		return "", nil, nil
	}
	existing, err := p.Log.ReadAll()
	if err != nil {
		return "", nil, fmt.Errorf("sync: reading log for issue-root lookup: %w", err)
	}
	if id := FindIssueEventID(existing, card.ItemID); id != "" {
		return id, nil, nil
	}
	ie, err := BuildIssueEvent(p.Key, card, createdAt)
	if err != nil {
		return "", nil, err
	}
	return ie.ID, ie, nil
}

// PublishCardEdit publishes ONLY a refreshed 30302 card (no accompanying NIP-34
// status event) for a pure field edit — title/context/priority changes that do
// NOT change status (e.g. `rd progress`, `rd update --context`). This is the
// hybrid model's proof point (ready-b5f): the addressable card is a disposable,
// re-publishable CURRENT-state materialization; history lives exclusively in the
// append-only status-event chain built by PublishItem/PublishStatusChange. A
// card-only edit can therefore never add to, or erase, that history — `rd show`
// keeps replaying the exact same status transitions after any number of edits.
func (p *Publisher) PublishCardEdit(ctx context.Context, card CardSpec, createdAt int64) (PublishResult, error) {
	var res PublishResult
	res.ItemID = card.ItemID
	ce, err := BuildCardEvent(p.Key, card, createdAt)
	if err != nil {
		return res, err
	}
	return p.publishEvents(ctx, res, []*nostr.Event{ce})
}

// PublishEvents appends a pre-built signed-event slice to the authoritative log
// and best-effort publishes it to the write relays — the low-level primitive the
// ready-d65 migration drives (board + per-item card + history status events built
// by BuildItemMigrationEvents). Same durability contract as PublishItem: the log
// append MUST succeed; a relay failure only buffers, never fails.
func (p *Publisher) PublishEvents(ctx context.Context, events []*nostr.Event) (PublishResult, error) {
	return p.publishEvents(ctx, PublishResult{}, events)
}

// PublishEventsUnique is the RE-RUN-SAFE variant PublishEvents that the ready-d65
// migration uses. It appends only events whose id is not already in the local log
// (dedup by content-hash event id, via AppendUnique) and publishes ONLY those
// newly-added events to the relays. Re-migrating the same item set therefore adds
// nothing to the log and issues no redundant relay writes — the "idempotent by
// nostr event id, re-run safe" guarantee (a plain Append would balloon the log with
// byte-identical duplicates on every re-run, even though projection stays
// idempotent). Returns the number of events actually added.
func (p *Publisher) PublishEventsUnique(ctx context.Context, events []*nostr.Event) (PublishResult, int, error) {
	added, err := p.Log.AppendUnique(events)
	if err != nil {
		return PublishResult{}, 0, fmt.Errorf("sync: appending unique to authoritative log: %w", err)
	}
	// When AppendUnique added nothing (a full re-run), skip the relays entirely — a
	// re-run issues zero redundant writes. Otherwise relay-publish the distinct input
	// events; relays dedup by id, so re-sending an already-present id is harmless, and
	// the costly disk log is the part AppendUnique already deduped. (We don't have the
	// exact "before" id set here, so this over-approximates the fresh set upward only,
	// never dropping a genuinely new event.)
	res := PublishResult{}
	p.relayPublish(ctx, &res, distinctIfAdded(events, added))
	return res, added, nil
}

// distinctIfAdded returns the id-deduped input events when added>0, or nil when
// added==0 (nothing new — skip the relays). Deduping within the input means a slice
// with internal repeats publishes each id at most once.
func distinctIfAdded(events []*nostr.Event, added int) []*nostr.Event {
	if added == 0 {
		return nil
	}
	seen := make(map[string]bool, len(events))
	out := make([]*nostr.Event, 0, len(events))
	for _, e := range events {
		if e == nil || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out
}

func (p *Publisher) publishEvents(ctx context.Context, res PublishResult, events []*nostr.Event) (PublishResult, error) {
	// Phase 1 — append to the authoritative log. This MUST succeed; it is the
	// durability guarantee independent of any relay.
	for _, e := range events {
		if err := p.Log.Append(e); err != nil {
			return res, fmt.Errorf("sync: appending to authoritative log: %w", err)
		}
	}
	// Phase 2 — publish to write relays, best-effort.
	p.relayPublish(ctx, &res, events)
	return res, nil
}

// relayPublish publishes each event to every write relay, best-effort, recording
// per-relay acks and buffering any event that reaches no relay. It NEVER fails —
// the events are already durable in the local log by the time this runs.
func (p *Publisher) relayPublish(ctx context.Context, res *PublishResult, events []*nostr.Event) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}
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
