// Batched relay publish over ONE websocket connection (ready-260).
//
// Publish dials a fresh websocket per event. That is the right shape for the
// live per-mutation write path (one item mutation = 3-4 events), but it makes a
// BULK republish — the ready-260 backfill re-sends every event already durable
// in a project's authoritative log — infeasible: ~23k events across the
// portfolio, one TLS handshake each, against a scale-to-zero relay. PublishMany
// keeps ONE connection open for the whole batch and pipelines a bounded window
// of in-flight EVENT frames, so the cost per event collapses to a single
// round-trip's share of the window instead of a full dial.
//
// It changes NOTHING about what is sent: each event goes out as the same NIP-01
// ["EVENT", <event>] frame with its ORIGINAL id, created_at and signature. A
// batch republish is therefore byte-identical to what a per-event Publish loop
// would have sent, which is what makes the backfill idempotent — a relay that
// already holds an event answers "duplicate:" (or OK,true) and stores nothing
// new.
package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"
)

// PublishAck is one event's outcome in a batched publish: the relay's NIP-20
// ["OK", <id>, <accepted>, <message>] reply, or the transport error that
// prevented one. Exactly one PublishAck is returned per INPUT event, in input
// order, so a caller can zip acks back onto its own event slice by index even
// when the relay acknowledges out of order or never answers at all.
type PublishAck struct {
	EventID  string
	Accepted bool
	Message  string
	Err      error
}

// PublishManyWindow bounds how many events may be written but not yet
// acknowledged on the shared connection. A window (rather than "write
// everything, then read") keeps the relay's receive buffer bounded — a relay
// that stops reading while rd blasts thousands of frames would otherwise
// deadlock both sides — while still amortizing the round-trip across many
// events. 32 was chosen as comfortably below any relay's per-connection
// in-flight expectations and large enough that batch throughput is dominated by
// relay write latency, not by RTT.
const PublishManyWindow = 32

// publishManyIdleTimeout bounds how long PublishMany waits for the NEXT frame
// from the relay while it still has unacknowledged events in flight. It is a
// LIVENESS bound, not a total-batch bound: a large batch legitimately takes
// minutes, so a single deadline for the whole run would either be far too short
// for a big board or far too generous for a hung relay. The effective read
// deadline is always the EARLIER of this idle bound and ctx's own deadline, so a
// caller's overall budget still wins when it is tighter.
const publishManyIdleTimeout = 60 * time.Second

// PublishMany publishes every event in events to a single relay over ONE
// websocket connection, and returns one PublishAck per input event in input
// order.
//
// GUARD FIRST, DIAL SECOND: PublishGuard (installed by pkg/sync) is consulted
// for EVERY event before any connection is opened. If it refuses any event, the
// whole batch is refused — no bytes leave this process, every returned ack
// carries the guard error, and the error is returned. This is deliberately
// all-or-nothing: a batch is a single operator intent, and silently sending the
// permitted subset of a batch the guard partially refused would be the exact
// "it reported success but the important part never went" failure the guard
// exists to prevent.
//
// The returned slice is ALWAYS len(events) long, even when the dial itself
// fails — every event then carries that dial error — so callers never have to
// index-check before zipping acks onto events. A non-nil error means the batch
// did not complete; the acks still describe how far it got, which events the
// relay acknowledged, and which never got a reply.
//
// Duplicate ids within a batch are supported: acks are matched to input
// positions FIFO per id, so two entries with the same id both receive the
// relay's replies (a relay may answer once or twice; the second position then
// reports the no-reply error, never a wrong-event ack).
func PublishMany(ctx context.Context, relayURL string, events []*Event) ([]PublishAck, error) {
	acks := make([]PublishAck, len(events))
	for i, e := range events {
		if e != nil {
			acks[i].EventID = e.ID
		}
	}
	if len(events) == 0 {
		return acks, nil
	}
	// Guard EVERY event before opening a connection (see doc comment).
	if PublishGuard != nil {
		for _, e := range events {
			if e == nil {
				continue
			}
			if gerr := PublishGuard(ctx, e); gerr != nil {
				for i := range acks {
					acks[i].Err = gerr
				}
				return acks, gerr
			}
		}
	}
	for i, e := range events {
		if e == nil {
			acks[i].Err = fmt.Errorf("nostr: nil event at index %d", i)
		}
	}

	conn, err := dialRelay(ctx, relayURL)
	if err != nil {
		for i := range acks {
			if acks[i].Err == nil {
				acks[i].Err = err
			}
		}
		return acks, err
	}
	defer conn.Close()

	// pendingByID maps an event id to the FIFO queue of input positions still
	// awaiting a reply for it, so duplicate ids in one batch resolve in order.
	pendingByID := make(map[string][]int, len(events))
	unacked := 0
	next := 0 // index of the next event to write

	writeOne := func() error {
		for next < len(events) && events[next] == nil {
			next++ // nil entries were already errored above; never written
		}
		if next >= len(events) {
			return nil
		}
		e := events[next]
		if err := armDeadlines(ctx, conn); err != nil {
			return err
		}
		if err := conn.WriteJSON([]any{"EVENT", e}); err != nil {
			return fmt.Errorf("nostr: write EVENT %s: %w", e.ID, err)
		}
		pendingByID[e.ID] = append(pendingByID[e.ID], next)
		unacked++
		next++
		return nil
	}

	fail := func(err error) ([]PublishAck, error) {
		for _, idxs := range pendingByID {
			for _, i := range idxs {
				acks[i].Err = err
			}
		}
		// Anything never written at all shares the same fate.
		for i := next; i < len(events); i++ {
			if acks[i].Err == nil {
				acks[i].Err = err
			}
		}
		return acks, err
	}

	for {
		for unacked < PublishManyWindow && next < len(events) {
			if werr := writeOne(); werr != nil {
				return fail(werr)
			}
		}
		if unacked == 0 {
			// Nothing in flight and nothing left to write — batch complete.
			if next >= len(events) {
				// No CLOSE frame: this connection never opened a subscription,
				// only wrote EVENTs. Closing the socket (the deferred Close) is
				// the whole teardown.
				return acks, nil
			}
			continue
		}
		if err := armDeadlines(ctx, conn); err != nil {
			return fail(err)
		}
		typ, frame, rerr := readNIP01Frame(conn)
		if rerr != nil {
			return fail(fmt.Errorf("nostr: read OK from %s: %w", relayURL, rerr))
		}
		if typ != "OK" || len(frame) < 3 {
			continue // NOTICE / EOSE / unparseable — keep reading
		}
		var id string
		_ = json.Unmarshal(frame[1], &id)
		idxs := pendingByID[id]
		if len(idxs) == 0 {
			continue // an OK for an id we are not waiting on
		}
		i := idxs[0]
		if len(idxs) == 1 {
			delete(pendingByID, id)
		} else {
			pendingByID[id] = idxs[1:]
		}
		unacked--
		var ok bool
		_ = json.Unmarshal(frame[2], &ok)
		var msg string
		if len(frame) >= 4 {
			_ = json.Unmarshal(frame[3], &msg)
		}
		acks[i].Accepted = ok
		acks[i].Message = msg
	}
}

// armDeadlines sets the connection's read and write deadlines to the EARLIER of
// ctx's deadline and now+publishManyIdleTimeout. It is re-armed before every
// write and every read so a long batch keeps making progress (a single deadline
// set once at dial time, which is what dialRelay does, would expire mid-batch),
// while a relay that stops responding still trips the idle bound instead of
// hanging forever.
func armDeadlines(ctx context.Context, conn *websocket.Conn) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	dl := time.Now().Add(publishManyIdleTimeout)
	if cd, ok := ctx.Deadline(); ok && cd.Before(dl) {
		dl = cd
	}
	if err := conn.SetWriteDeadline(dl); err != nil {
		return err
	}
	return conn.SetReadDeadline(dl)
}
