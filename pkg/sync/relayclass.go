// Relay publish-outcome classification (ready-1c2, nostr backend).
//
// ready-1c2 (filed against the retired campfire backend) observed that a
// PERMANENT rejection — one that retrying the identical bytes can never fix —
// was buffered to the pending retry queue as if it were a transient delivery
// failure, so it was re-published forever while the operator was told "buffered,
// will retry". The nostr backend inherits the same hazard at the relay seam:
// nostr.Publish returns (accepted, message, err).
//
// The classification criterion is crisp: permanence is a property of the
// IMMUTABLE signed event bytes, not of mutable relay state. A malformed event
// ("invalid:") is malformed at every relay forever, and a proof-of-work demand
// ("pow:") can never be met by re-sending the SAME event id — both are permanent.
// Everything else (transport error, "blocked:"/"restricted:" allow-list/auth/
// payment gaps that admission can close, "rate-limited:", "error:", unknown) is a
// property of relay state a later retry may satisfy, so it is transient.
//
// The Publisher (relayPublish) and the flush path (FlushNostrPending) act on the
// reduced per-event outcome: transient -> buffer/retry, permanent -> dead-letter
// to nostr-rejected.jsonl and never retry.
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// relayOutcome classifies the result of publishing one signed event to one relay.
type relayOutcome int

const (
	// outcomeAccepted: the relay stored the event (OK,true) or already had it
	// ("duplicate:"). Durable remotely — nothing more to do.
	outcomeAccepted relayOutcome = iota
	// outcomeTransient: not stored, but retrying the identical event later may
	// succeed — a dial/timeout/read error, "rate-limited:", "error:", a policy
	// gap admission can close ("blocked:"/"restricted:"), or any unrecognized
	// rejection. Buffer and retry.
	outcomeTransient
	// outcomePermanent: the relay refused the event for a reason no retry of the
	// identical bytes can fix — the event is malformed ("invalid:") or demands a
	// proof-of-work the fixed event id can never carry ("pow:"). Dead-letter it;
	// never re-buffer it as a pending retry (that is the ready-1c2 defect).
	outcomePermanent
)

// classifyRelayResult maps a single nostr.Publish (accepted, message, err) result
// to a relayOutcome. It is deliberately CONSERVATIVE about permanence: only NIP-20
// "invalid:" and "pow:" — the two rejections intrinsic to the immutable signed
// bytes — are permanent; everything uncertain is transient, so a still-deliverable
// event is never dropped by a misclassification. "duplicate:" means the relay
// already holds the event and counts as accepted.
func classifyRelayResult(accepted bool, message string, err error) relayOutcome {
	if accepted {
		return outcomeAccepted
	}
	if err != nil {
		// Transport-layer failure (dial/write/read) — always retryable.
		return outcomeTransient
	}
	msg := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.HasPrefix(msg, "duplicate:"):
		// Relay already holds the event — durable remotely.
		return outcomeAccepted
	case strings.HasPrefix(msg, "invalid:"), strings.HasPrefix(msg, "pow:"):
		// Permanence is a property of the immutable signed bytes: a malformed
		// event is malformed at every relay forever, and a proof-of-work demand
		// can never be met by re-sending the SAME event id (satisfying it requires
		// re-mining a DIFFERENT event). Retrying identical bytes is provably
		// futile -> dead-letter.
		return outcomePermanent
	default:
		// Everything else is a property of MUTABLE relay state a later retry of
		// the identical event may satisfy: "blocked:"/"restricted:" (an allow-list
		// / auth / payment gap admission can close), "rate-limited:", "error:", an
		// unprefixed or unknown message. Buffer and retry.
		return outcomeTransient
	}
}

// reduceEventOutcome combines the per-relay outcomes for ONE event into a single
// disposition. Accepted wins — any relay that stored it makes the event durable
// remotely. Otherwise the event is dead-lettered ONLY when EVERY non-accepted
// relay verdict is permanent: a single transiently-unreachable relay keeps the
// event in the retry queue, because it may accept the event once it recovers, so
// we must never drop it. Transient therefore dominates permanent. An empty
// outcome set (no relays configured) is transient — an event that reached no
// relay is deliverable once relays exist.
func reduceEventOutcome(outcomes []relayOutcome) relayOutcome {
	sawPermanent, sawTransient := false, false
	for _, o := range outcomes {
		switch o {
		case outcomeAccepted:
			return outcomeAccepted
		case outcomeTransient:
			sawTransient = true
		case outcomePermanent:
			sawPermanent = true
		}
	}
	if sawPermanent && !sawTransient {
		return outcomePermanent
	}
	return outcomeTransient
}

// relayAttempt is the result of publishing one event to one relay: the raw relay
// response plus its classified outcome (so callers reflect "duplicate:" — an
// OK,false the relay nonetheless stored — as accepted, not as a no-op).
type relayAttempt struct {
	Relay    string
	Accepted bool
	Message  string
	Err      error
	Outcome  relayOutcome
}

// publishEventToRelays publishes e to every relay in order and returns the
// per-relay attempts (each carrying its classified Outcome), the reduced
// event-level outcome, and a permanent-rejection reason. permReason is the
// message from the FIRST relay that returned a permanent verdict, so it may be
// non-empty even when the reduced outcome is transient or accepted (e.g. one
// relay "invalid:" while another is down); callers read it only inside their
// permanent branch. This is the SINGLE place the classify+reduce contract lives
// so the fresh-publish path (relayPublish) and the retry path (FlushNostrPending)
// can never diverge. It has no side effects beyond the network calls — buffering,
// dead-lettering, and ack/error bookkeeping are the caller's.
func publishEventToRelays(ctx context.Context, relays []string, e *nostr.Event, timeout time.Duration) (attempts []relayAttempt, outcome relayOutcome, permReason string) {
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}
	outcomes := make([]relayOutcome, 0, len(relays))
	attempts = make([]relayAttempt, 0, len(relays))
	for _, relay := range relays {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		accepted, msg, err := nostr.Publish(rctx, relay, e)
		cancel()
		oc := classifyRelayResult(accepted, msg, err)
		attempts = append(attempts, relayAttempt{Relay: relay, Accepted: accepted, Message: msg, Err: err, Outcome: oc})
		if oc == outcomePermanent && permReason == "" {
			permReason = relayLabel(relay, msg)
		}
		outcomes = append(outcomes, oc)
	}
	return attempts, reduceEventOutcome(outcomes), permReason
}

// RejectedRecord is one dead-lettered event: a signed event a relay PERMANENTLY
// refused (malformed / proof-of-work demanded). It is written to
// .ready/nostr-rejected.jsonl for operator diagnosis and is NEVER retried. The
// event stays durable in the authoritative log regardless — dead-lettering only
// removes it from the relay-delivery retry queue.
type RejectedRecord struct {
	Event  *nostr.Event `json:"event"`
	Reason string       `json:"reason"` // the relay message that classified it permanent
}

// rejectedPathFor returns the dead-letter file path adjacent to a pending path
// (both live directly under .ready/).
func rejectedPathFor(pendingPath string) string {
	return filepath.Join(filepath.Dir(pendingPath), NostrRejectedFile)
}

// appendRejectedEvent appends a dead-lettered record to nostr-rejected.jsonl.
//
// Dead-lettering is at-least-once: relayPublish/FlushNostrPending fsync this file
// BEFORE removing the event from the pending buffer, so a crash in the window
// between the two may re-dead-letter the same event on the next flush (a duplicate
// diagnostic line, never data loss). Operators reading nostr-rejected.jsonl should
// treat records as deduplicable by event id.
func appendRejectedEvent(path string, rec RejectedRecord) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(rec)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// corruptPathFor returns the quarantine file path for unparseable pending lines,
// adjacent to the pending path (both live directly under .ready/).
func corruptPathFor(pendingPath string) string {
	return filepath.Join(filepath.Dir(pendingPath), NostrCorruptFile)
}

// appendCorruptLine quarantines a raw, unparseable pending-buffer line to
// nostr-corrupt.jsonl for operator forensics (ready-e52). The line is written
// verbatim (it is not valid JSON, so it cannot be re-marshaled). Like
// dead-lettering, this fsyncs before the caller drops the line from the buffer,
// so a crash in the window re-quarantines rather than losing the line.
func appendCorruptLine(path string, raw []byte) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	if _, err := f.Write(append(append([]byte{}, raw...), '\n')); err != nil {
		return err
	}
	return f.Sync()
}

// relayLabel formats a relay+message pair for a dead-letter reason string.
func relayLabel(relay, message string) string {
	return fmt.Sprintf("%s: %s", relay, message)
}
