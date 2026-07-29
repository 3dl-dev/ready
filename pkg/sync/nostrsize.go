// Client-side event size guard (ready-c3e).
//
// THE DEFECT THIS CLOSES: rd had NO client-side max-event-size check anywhere.
// BuildCardEvent would sign, locally log, and relay-dial a kind-30302 card the
// public relay (strfry, 64KiB / 65536-byte default maxEventSize) can never
// store. The only signal a human ever got was a warning line printed AFTER the
// relay round-trip, at which point the event was already durable in the local
// log and already dead-lettered to nostr-rejected.jsonl — a silent divergence
// between the local log and every relay, discovered only by an out-of-band
// audit (rd relay audit), not by the write itself.
//
// THE FIX IS "REFUSE", NOT "TRUNCATE". An oversized item genuinely cannot be
// represented as one signed event under a 64KiB relay cap; the two candidate
// behaviors are refusing the write (protects integrity, costs the user a retry)
// or silently truncating the content (loses data invisibly). Truncation is
// exactly the defect class this whole swarm keeps finding elsewhere (silent
// data loss dressed up as success) — it is not an option here. Refusing BEFORE
// the event becomes durable anywhere (local log OR relay), with an error naming
// the item and the exact byte count, gives the user synchronous, actionable
// feedback at the one moment they can still act on it (shrink the description /
// context / progress notes and retry) instead of discovering the problem days
// later via a stale board.
//
// WHERE THIS DOES NOT APPLY: republishing already-signed historical events
// (PublishBoard, PublishBoardDelta / `rd relay repair`, `rd log publish
// --board`) is deliberately NOT guarded here. Those paths exist specifically to
// resend bytes VERBATIM — original id, created_at, signature — because
// re-signing to shrink an oversized event mints a NEW event id and forks
// history (see nostroutbound.go's PublishBoard doc and ready-260). This guard
// only stops a NEW oversized event from being minted in the first place; it
// must never block replaying one that already exists.
package sync

import (
	"encoding/json"
	"fmt"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// maxEventWireSize is the byte ceiling this client enforces on any signed event
// before it may become durable in the local authoritative log or reach a
// relay. It mirrors strfry's default maxEventSize (64 KiB) — the limit every
// relay observed in this portfolio enforces (ready-c3e's re-measurement found
// the SAME 65536-byte cap on wss://relay.3dl.network and on the LAN relay this
// project also writes to; option (1) in the item, raising a relay's own
// configured limit, lives in the nostr-relay repo and is a deploy-time choice,
// not something this client can assume away).
const maxEventWireSize = 65536

// marshaledEventSize returns the byte length of e exactly as nostr.Publish /
// nostr.PublishMany send it: encoding/json's marshaling of the *Event value
// (conn.WriteJSON marshals ["EVENT", e] the same way). This is deliberately NOT
// the NIP-01 canonical id-preimage (nostr.Event.canonicalForID, which omits
// id/sig and escapes differently) — strfry measures the actual bytes it reads
// off the wire, which are this encoding/json form, so this is the number that
// must match the relay's own "event is N bytes" rejection message.
func marshaledEventSize(e *nostr.Event) (int, error) {
	raw, err := json.Marshal(e)
	if err != nil {
		return 0, fmt.Errorf("sync: marshal event for size check: %w", err)
	}
	return len(raw), nil
}

// eventSubjectLabel names what an oversized event refers to, for the guard's
// error message. Card and status events carry the rd item id in their "d" tag
// (BuildCardEvent, BuildStatusEvent); a board event's "d" tag is the board
// name, not an item, so it is labeled by kind instead of misreported as an
// "item".
func eventSubjectLabel(e *nostr.Event) string {
	d := tagValue(e, "d")
	if d == "" {
		return fmt.Sprintf("kind %d event", e.Kind)
	}
	if e.Kind == KindBoard {
		return fmt.Sprintf("board %q", d)
	}
	return fmt.Sprintf("item %q", d)
}

// guardEventSizes refuses the ENTIRE batch the instant any one event exceeds
// maxEventWireSize — before the caller's Log.Append and before any relay dial
// (ready-c3e). A batch is one operator intent (create/status-change/edit all
// build a card + status [+ issue] event together); publishing "the rest" while
// silently dropping the oversized member would itself be a silent partial
// write, the exact class of bug this guard exists to prevent. The error names
// the offending subject (item or board) and the measured byte count against
// the fixed 64KiB ceiling so the operator has everything needed to act:
// shrink the content and retry.
func guardEventSizes(events []*nostr.Event) error {
	for _, e := range events {
		if e == nil {
			continue
		}
		n, err := marshaledEventSize(e)
		if err != nil {
			return err
		}
		if n <= maxEventWireSize {
			continue
		}
		return fmt.Errorf(
			"sync: refusing to publish %s: event is %d bytes, exceeds the 64KiB (%d byte) relay limit every relay in this fleet enforces (strfry default) — shrink its description/context/progress notes below the limit and retry; rd will not sign an event it knows will be silently dead-lettered (ready-c3e guard)",
			eventSubjectLabel(e), n, maxEventWireSize,
		)
	}
	return nil
}
