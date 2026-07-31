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
	"bufio"
	"bytes"
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

// GuardedPublish is the SINGLE sanctioned entry point to the network write —
// pkg/nostr's Publish — for this entire repo (ready-6d0 finding (3)). Every
// other call site that used to dial nostr.Publish directly (relayclass.go's own
// loop below, negentropy.go's upload step, the relay-policy probe, the nostr-demo
// proof tool, and the self-heal adversarial tests) now funnels through here.
//
// WHY HERE AND NOT IN pkg/nostr: pkg/nostr is a lower layer than pkg/sync and
// knows nothing about rd boards or coordinates — pushing hitsReservedBoard's
// "d"/"a" tag semantics down into it would be the wrong layer for that
// knowledge to live in. This wrapper stays in pkg/sync, where hitsReservedBoard
// already lives (nostroutbound.go), and becomes the mandatory choke point
// instead: production mirrors Publisher.Production — false refuses to dial the
// relay AT ALL (no network I/O) for an event addressing the reserved production
// board coordinate; true passes through unconditionally, exactly as
// guardReservedBoard does for the Publisher's own call surface.
//
// THIS IS A CLASS FIX, NOT AN INSTANCE FIX: guardReservedBoard protects
// Publisher's methods, but Publisher was only one of several ways to reach the
// network. Finding (3) was that a brand-new file could sidestep Publisher
// entirely (BuildCardEvent + nostr.Publish, ready-6d0's own adversary probe A5)
// and nothing would stop it. Fixing this file's call sites closes the KNOWN
// vectors; publish_chokepoint_test.go (mechanical, not conventional) is what
// closes the class — it fails CI the moment ANY file other than this one calls
// nostr.Publish directly, so a brand-new violating file cannot even compile a
// green test run, regardless of whether its author read this comment.
func GuardedPublish(ctx context.Context, relayURL string, e *nostr.Event, production bool) (accepted bool, message string, err error) {
	if !production && hitsReservedBoard(e) {
		return false, "", fmt.Errorf("sync: refusing to dial %s with kind %d event addressing the reserved production board coordinate %q — not marked production (ready-6d0 chokepoint guard); use an isolated board D-tag, or thread production=true from a sanctioned CLI path if this really is a production write", relayURL, e.Kind, reservedProductionBoardD)
	}
	return nostr.Publish(withPublishProduction(ctx, production), relayURL, e)
}

// GuardedPublishMany is GuardedPublish's BATCH counterpart (ready-260): the
// single sanctioned entry point to pkg/nostr's PublishMany, which republishes a
// whole board's already-signed events over ONE websocket connection instead of
// one dial per event.
//
// Same guard contract as GuardedPublish, applied to the WHOLE batch: a
// Publisher not marked production may not send ANY event addressing the
// reserved production board coordinate, and because a batch is one operator
// intent, a single offending event refuses the entire call before any dial —
// sending "the rest of the batch" would be precisely the partial, silently
// incomplete write this guard exists to prevent. The returned ack slice is
// always len(events) long (every entry carrying the refusal), so callers zip
// acks onto events by index without a length check.
//
// pkg/nostr.PublishMany ALSO consults the nostr.PublishGuard hook per event
// (relayclass.go's init installs it), so the class guard still applies to any
// caller that somehow reaches PublishMany without coming through here — exactly
// as it does for Publish.
func GuardedPublishMany(ctx context.Context, relayURL string, events []*nostr.Event, production bool) ([]nostr.PublishAck, error) {
	if !production {
		for _, e := range events {
			if !hitsReservedBoard(e) {
				continue
			}
			err := fmt.Errorf("sync: refusing to dial %s with a %d-event batch containing a kind %d event addressing the reserved production board coordinate %q — not marked production (ready-6d0 chokepoint guard, ready-260 batch path); use an isolated board D-tag, or thread production=true from a sanctioned CLI path if this really is a production write", relayURL, len(events), e.Kind, reservedProductionBoardD)
			acks := make([]nostr.PublishAck, len(events))
			for i := range acks {
				if events[i] != nil {
					acks[i].EventID = events[i].ID
				}
				acks[i].Err = err
			}
			return acks, err
		}
	}
	return nostr.PublishMany(withPublishProduction(ctx, production), relayURL, events)
}

// publishProductionCtxKey is the unexported context key carrying this call's
// production opt-in down into pkg/nostr.Publish, for nostr.PublishGuard
// (installed below in init) to read. It is unexported and its type is defined
// only in this package, so no caller outside pkg/sync — in particular no
// brand-new file an adversary writes, however it imports pkg/nostr — can
// forge a context that satisfies it; the zero value (ctx with no such key, or
// any ctx a new file constructs itself) always reads as production=false.
type publishProductionCtxKey struct{}

// withPublishProduction stamps ctx with this call's production opt-in so
// nostr.PublishGuard (installed in init below) can see it. Only GuardedPublish
// calls this — every path already sanctioned to reach nostr.Publish threads
// the SAME production bool its Publisher (or CLI command) carries.
func withPublishProduction(ctx context.Context, production bool) context.Context {
	return context.WithValue(ctx, publishProductionCtxKey{}, production)
}

// isPublishProductionCtx reports the production opt-in stamped on ctx by
// withPublishProduction, defaulting to false (fail closed) for any ctx that
// never passed through it — which is every ctx a caller builds itself instead
// of going through GuardedPublish.
func isPublishProductionCtx(ctx context.Context) bool {
	v, _ := ctx.Value(publishProductionCtxKey{}).(bool)
	return v
}

// init installs the board semantics (hitsReservedBoard + the production
// opt-in) into pkg/nostr's nil-able PublishGuard hook (ready-69e class fix).
// pkg/nostr itself gains no board knowledge — it only ever sees this closure
// as an opaque func(ctx, *Event) error.
//
// SCOPE, stated precisely — an earlier version of this comment claimed EVERY
// call to nostr.Publish in the module is checked here "regardless of how the
// caller reached it". That was FALSE and a veracity adversary disproved it, so
// do not restore that wording. What is actually true:
//
//	IN A BINARY THAT LINKS pkg/sync, every SPELLING is covered. The check lives
//	in the callee, so an aliased import, a dot-import, a function value taken
//	off the package, and reflection all land in this same closure. None of them
//	can stamp publishProductionCtxKey (its type is unexported here), so they
//	read as production=false and are refused the instant the event addresses
//	the reserved coordinate, before any dial. Only GuardedPublish can stamp the
//	opt-in, and only from a sanctioned CLI path.
//
//	ARMING IS A LINK-TIME SIDE EFFECT of this init(), which is the residual gap
//	(ready-fcf, open). pkg/nostr ships with PublishGuard == nil, so two routes
//	are NOT covered: (1) a file inside pkg/nostr itself, which calls Publish as
//	a bare in-package identifier and cannot be reached by this hook because
//	pkg/nostr cannot import pkg/sync without an import cycle; and (2) any
//	binary that imports pkg/nostr without pkg/sync, which never runs this
//	init(). pkg/nostr/live_relay_test.go and negentropy_live_relay_test.go are
//	existing files of shape (1) — they publish kind 1 / kind 30078 with no
//	board coordinate, so there is no exposure today, but a production-board
//	test added there would NOT be guarded.
//
// The static counterpart (pkg/sync/publish_chokepoint_test.go) has the same
// blind spot for shape (1): it only resolves bindings for files that IMPORT
// pkg/nostr, so a file inside that package is never inspected.
func init() {
	nostr.PublishGuard = func(ctx context.Context, e *nostr.Event) error {
		if isPublishProductionCtx(ctx) {
			return nil
		}
		if hitsReservedBoard(e) {
			return fmt.Errorf("nostr: refusing to publish kind %d event addressing the reserved production board coordinate %q — no production opt-in reached pkg/nostr for this call (ready-69e class guard); route through sync.GuardedPublish with production=true from a sanctioned CLI path, or use an isolated board D-tag", e.Kind, reservedProductionBoardD)
		}
		return nil
	}
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
//
// production is threaded straight through to GuardedPublish (ready-6d0 finding
// (3)): callers pass the SAME opt-in their Publisher (or sanctioned CLI command)
// carries, so an event addressing the reserved production board coordinate is
// refused here — before any relay dial — exactly as it already is at the
// Publisher-method layer, but now also at the actual network choke point.
func publishEventToRelays(ctx context.Context, relays []string, e *nostr.Event, timeout time.Duration, production bool) (attempts []relayAttempt, outcome relayOutcome, permReason string) {
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}
	outcomes := make([]relayOutcome, 0, len(relays))
	attempts = make([]relayAttempt, 0, len(relays))
	for _, relay := range relays {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		accepted, msg, err := GuardedPublish(rctx, relay, e, production)
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

// publishEventsToRelaysBatch is publishEventToRelays' BATCHED counterpart
// (ready-046): it publishes every event in events to every relay in relays
// over ONE websocket connection PER RELAY (GuardedPublishMany) instead of one
// dial per event per relay, then applies the identical classify+reduce
// contract per event. Returns, for each input index, the same three values
// publishEventToRelays returns for one event — so a caller iterating events
// can switch from calling publishEventToRelays per event to reading these
// slices index-for-index with no other change to its dead-letter/buffer logic.
//
// EXTRACTED (ready-046) so the batched board-publish path (relayPublishBatch)
// and the offline-buffer batched drain (FlushNostrPending) share ONE
// definition of "what attempts + outcome does event i get from a batched
// publish" — duplicating this loop was the obvious alternative and the wrong
// one, for the same reason applyRelayOutcome already unifies what happens
// AFTER an outcome is known (see relayPublishBatch's doc comment).
//
// No per-call ctx timeout is applied here, matching relayPublishBatch's prior
// behaviour: PublishMany/GuardedPublishMany re-arm their own read/write
// deadline before every frame (armDeadlines), which already bounds a stalled
// relay without capping a large batch's total wall-clock the way wrapping ctx
// in a single fixed timeout would.
func publishEventsToRelaysBatch(ctx context.Context, relays []string, events []*nostr.Event, production bool) (attempts [][]relayAttempt, outcomes []relayOutcome, permReasons []string) {
	attempts = make([][]relayAttempt, len(events))
	for _, relay := range relays {
		acks, err := GuardedPublishMany(ctx, relay, events, production)
		for i := range events {
			var a relayAttempt
			if i < len(acks) {
				ak := acks[i]
				a = relayAttempt{Relay: relay, Accepted: ak.Accepted, Message: ak.Message, Err: ak.Err}
			} else {
				// PublishMany always returns len(events) acks; this is a
				// belt-and-braces fallback so a future contract change cannot
				// turn into an index panic mid-backfill/mid-flush.
				a = relayAttempt{Relay: relay, Err: err}
			}
			a.Outcome = classifyRelayResult(a.Accepted, a.Message, a.Err)
			attempts[i] = append(attempts[i], a)
		}
	}

	outcomes = make([]relayOutcome, len(events))
	permReasons = make([]string, len(events))
	for i := range events {
		perEvent := make([]relayOutcome, 0, len(attempts[i]))
		permReason := ""
		for _, a := range attempts[i] {
			if a.Outcome == outcomePermanent && permReason == "" {
				permReason = relayLabel(a.Relay, a.Message)
			}
			perEvent = append(perEvent, a.Outcome)
		}
		outcomes[i] = reduceEventOutcome(perEvent)
		permReasons[i] = permReason
	}
	return attempts, outcomes, permReasons
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

// deadLetterHasEvent reports whether path (nostr-rejected.jsonl) already holds
// a record for eventID. ready-c3e finding (2): `rd relay repair` re-measures
// the relay's gap every round and republishes exactly that gap; for a
// PERMANENTLY oversized event the gap can never close, so the identical event
// was re-sent and re-dead-lettered on EVERY round of EVERY repair run — 12MB
// on one project (8 distinct oversized events across 2 stale coordinates),
// 3.6MB on another (1 distinct event), growing forever at up to 5 records per
// `rd relay repair` invocation (re-measured directly against both projects'
// on-disk nostr-rejected.jsonl; corrected from an earlier restatement that
// conflated "2 distinct events" with "2 stale coordinates" — not the same
// count: 3dl has 8 distinct oversized event ids across those 2 coordinates).
// Permanence is a property of the immutable signed bytes (same premise as
// classifyRelayResult): once a record for this exact event id exists, a
// second one carries zero new information, so the caller skips the append
// instead of growing the file.
//
// FAILS CLOSED ON A BAD RECORD (ready-c3e finding 4, rework 2): a single
// malformed or torn line is skipped, not treated as end-of-scan. The prior
// version used a json.Decoder over the whole stream and returned false the
// instant Decode failed on ANY record — so one corrupt line positioned BEFORE
// the target silently disabled dedup for every record after it, permanently
// restoring the unbounded-growth bug this function exists to fix.
// appendRejectedEvent's own doc states the file is at-least-once (a crash
// mid-append may leave a torn line) — that is an ANTICIPATED on-disk state,
// not a hypothetical, so scanning must tolerate it. A read failure (including
// "file does not exist yet") reports false — the caller then appends the
// first record, exactly as before this fix.
//
// THE TORN SHAPE, PRECISELY (rework 2 — the first version of this fix got the
// shape wrong): appendRejectedEvent does ONE os.OpenFile(O_APPEND) + ONE
// f.Write(json+'\n'). A crash mid-write can land after only PART of that
// single Write made it to the underlying file, with NO trailing '\n' at all —
// so the NEXT call to appendRejectedEvent (a later round/run) opens with
// O_APPEND and writes its own record starting immediately after those torn
// bytes, on what bufio.Reader.ReadBytes('\n') sees as ONE physical line:
// [torn prefix of record B, no closing brace][record C, complete]'\n'. A
// plain "unmarshal the whole line, else skip it" (the first version of this
// function, and finding (4)'s own regression test before rework 2) treats
// that WHOLE merged line as one malformed record and skips it — so record C,
// even though it is completely well-formed, is never seen: dedup fails open
// for C's event id and a duplicate gets appended. recordsInLine below
// recovers C by re-scanning the line for an embedded '{' the first
// (whole-line) unmarshal missed.
func deadLetterHasEvent(path, eventID string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	// Line-based, not json.Decoder-over-the-stream: appendRejectedEvent always
	// writes exactly one JSON object followed by '\n' per record in the
	// UNCORRUPTED case, so ordinary damage is contained to its own line — the
	// reader can resync at the next '\n' instead of a decode error aborting
	// the whole scan. bufio.Reader.ReadString has no line-length ceiling
	// (unlike bufio.Scanner's default token limit), which matters here: a
	// dead-lettered record's Event.Content is, by definition, an OVERSIZED
	// event, so lines in this exact file routinely exceed 64KiB.
	r := bufio.NewReader(f)
	for {
		line, rerr := r.ReadBytes('\n')
		if trimmed := bytes.TrimSpace(line); len(trimmed) > 0 {
			for _, rec := range recordsInLine(trimmed) {
				if rec.Event != nil && rec.Event.ID == eventID {
					return true
				}
			}
			// No match (or nothing parseable) on this line: skip it and keep
			// scanning. Do NOT return false here — that was finding (4)'s bug.
		}
		if rerr != nil {
			return false // EOF (or a read error) — nothing further to scan.
		}
	}
}

// recordsInLine recovers every well-formed RejectedRecord present in line,
// tolerating the torn-append corruption deadLetterHasEvent's doc describes: a
// crash mid-write can merge a truncated PRIOR record's tail bytes onto the
// front of a later, COMPLETE record with no separating newline.
//
// Fast path: the whole line is one clean record (every uncorrupted line in
// practice) — a single json.Unmarshal handles it with no extra scanning.
//
// Recovery path: the whole-line parse failed, so line may be [torn prefix]
// [complete record] concatenated with no delimiter. There is no way to know
// where the complete record starts, so every '{' in the line is tried as a
// candidate start: a json.Decoder seeded at that offset either fails fast
// (the torn prefix and any mid-content '{' are not valid JSON from that
// point) or decodes exactly one well-formed value (the complete record that
// follows the torn prefix). This recovers record C in the shape above without
// needing to locate the torn/complete boundary explicitly.
func recordsInLine(line []byte) []RejectedRecord {
	var whole RejectedRecord
	if err := json.Unmarshal(line, &whole); err == nil {
		if whole.Event == nil {
			return nil
		}
		return []RejectedRecord{whole}
	}
	var out []RejectedRecord
	for i, b := range line {
		if b != '{' {
			continue
		}
		var rec RejectedRecord
		dec := json.NewDecoder(bytes.NewReader(line[i:]))
		if derr := dec.Decode(&rec); derr != nil || rec.Event == nil {
			continue
		}
		out = append(out, rec)
	}
	return out
}

// appendRejectedEvent appends a dead-lettered record to nostr-rejected.jsonl.
//
// Dead-lettering is at-least-once: relayPublish/FlushNostrPending fsync this file
// BEFORE removing the event from the pending buffer, so a crash in the window
// between the two may re-dead-letter the same event on the next flush (a duplicate
// diagnostic line, never data loss). Operators reading nostr-rejected.jsonl should
// treat records as deduplicable by event id. Callers that already know the file may
// hold a repeat (relayPublish/relayPublishBatch's permanent branch) should call
// deadLetterHasEvent first and skip the append on a hit (ready-c3e) — this function
// itself stays a plain unconditional append so the rare crash-window duplicate
// above still gets written rather than silently swallowed.
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
