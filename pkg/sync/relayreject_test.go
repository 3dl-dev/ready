package sync

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/gorilla/websocket"
)

func TestClassifyRelayResult(t *testing.T) {
	cases := []struct {
		name     string
		accepted bool
		msg      string
		err      error
		want     relayOutcome
	}{
		{"accepted", true, "", nil, outcomeAccepted},
		{"accepted with duplicate msg", true, "duplicate: had it", nil, outcomeAccepted},
		{"ok-false duplicate is accepted", false, "duplicate: already have this event", nil, outcomeAccepted},
		{"transport error is transient", false, "", errors.New("dial tcp: connection refused"), outcomeTransient},
		{"blocked is transient (allow-list may change)", false, "blocked: pubkey not admitted", nil, outcomeTransient},
		{"restricted is transient (mutable relay policy, like blocked)", false, "restricted: author not permitted", nil, outcomeTransient},
		{"rate-limited is transient", false, "rate-limited: slow down", nil, outcomeTransient},
		{"error is transient", false, "error: internal", nil, outcomeTransient},
		{"unprefixed rejection is transient", false, "no thanks", nil, outcomeTransient},
		{"invalid is permanent (malformed bytes)", false, "invalid: bad signature", nil, outcomePermanent},
		{"invalid uppercase is permanent", false, "INVALID: bad sig", nil, outcomePermanent},
		{"invalid with leading space is permanent", false, "   invalid: kind not allowed", nil, outcomePermanent},
		{"pow is permanent (fixed event id can never satisfy PoW)", false, "pow: 24 bits required", nil, outcomePermanent},
		{"transport error takes precedence over a permanent-looking msg", false, "invalid: x", errors.New("read: broken pipe"), outcomeTransient},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := classifyRelayResult(c.accepted, c.msg, c.err); got != c.want {
				t.Fatalf("classifyRelayResult(%v,%q,%v) = %d, want %d", c.accepted, c.msg, c.err, got, c.want)
			}
		})
	}
}

func TestReduceEventOutcome(t *testing.T) {
	cases := []struct {
		name string
		in   []relayOutcome
		want relayOutcome
	}{
		{"empty is transient", nil, outcomeTransient},
		{"accepted wins over permanent", []relayOutcome{outcomePermanent, outcomeAccepted}, outcomeAccepted},
		{"accepted wins over transient", []relayOutcome{outcomeTransient, outcomeAccepted}, outcomeAccepted},
		{"transient DOMINATES permanent (a down relay may still accept it)", []relayOutcome{outcomeTransient, outcomePermanent}, outcomeTransient},
		{"all transient is transient", []relayOutcome{outcomeTransient, outcomeTransient}, outcomeTransient},
		{"all permanent is permanent", []relayOutcome{outcomePermanent, outcomePermanent}, outcomePermanent},
		{"single permanent", []relayOutcome{outcomePermanent}, outcomePermanent},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := reduceEventOutcome(c.in); got != c.want {
				t.Fatalf("reduceEventOutcome(%v) = %d, want %d", c.in, got, c.want)
			}
		})
	}
}

// contentRelay is a fake nostr relay whose reply to an EVENT depends on the
// event's Content, so a single relay can exercise all three publish outcomes in
// one test:
//   - Content contains "bad"  -> replies ["OK", id, false, "invalid: ..."]  (PERMANENT)
//   - Content contains "drop" -> closes the socket without replying          (TRANSIENT)
//   - otherwise               -> replies ["OK", id, true, ""]                (ACCEPTED)
//
// nostr.Publish opens a fresh websocket per call, so each event is one
// Upgrade/one frame/one reply — the handler reads a single EVENT and returns.
func contentRelay(t *testing.T) string {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var frame []json.RawMessage
		if err := json.Unmarshal(data, &frame); err != nil || len(frame) < 2 {
			return
		}
		var typ string
		_ = json.Unmarshal(frame[0], &typ)
		if typ != "EVENT" {
			return
		}
		var ev struct {
			ID      string `json:"id"`
			Content string `json:"content"`
		}
		_ = json.Unmarshal(frame[1], &ev)
		switch {
		case strings.Contains(ev.Content, "drop"):
			// Transient: hang up without an OK so the client sees a read error.
			return
		case strings.Contains(ev.Content, "bad"):
			resp, _ := json.Marshal([]any{"OK", ev.ID, false, "invalid: malformed event"})
			_ = conn.WriteMessage(websocket.TextMessage, resp)
		default:
			resp, _ := json.Marshal([]any{"OK", ev.ID, true, ""})
			_ = conn.WriteMessage(websocket.TextMessage, resp)
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// fixedRelay is a fake nostr relay that answers EVERY event the same way,
// independent of content — used to compose multi-relay scenarios where each relay
// must have a distinct, stable disposition. drop=true hangs up without an OK
// (transient); otherwise it replies ["OK", id, accepted, message].
func fixedRelay(t *testing.T, accepted bool, message string, drop bool) string {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var frame []json.RawMessage
		if err := json.Unmarshal(data, &frame); err != nil || len(frame) < 2 {
			return
		}
		if drop {
			return
		}
		var ev struct {
			ID string `json:"id"`
		}
		_ = json.Unmarshal(frame[1], &ev)
		resp, _ := json.Marshal([]any{"OK", ev.ID, accepted, message})
		_ = conn.WriteMessage(websocket.TextMessage, resp)
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

func signedEvent(t *testing.T, k *nostr.Key, content string) *nostr.Event {
	t.Helper()
	ev := &nostr.Event{Kind: 1, CreatedAt: 1000, Tags: [][]string{}, Content: content}
	if err := ev.Sign(k); err != nil {
		t.Fatalf("sign: %v", err)
	}
	return ev
}

// fileHasID reports whether the JSONL file at path contains the given event id
// anywhere in its bytes. A 64-hex event id is unique enough that a substring
// match is a reliable on-disk contract check. A missing file reports false.
func fileHasID(t *testing.T, path, id string) bool {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return false
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Contains(string(data), id)
}

func readyPaths(dir string) (pending, rejected string) {
	return filepath.Join(dir, ".ready", "nostr-pending.jsonl"),
		filepath.Join(dir, ".ready", "nostr-rejected.jsonl")
}

// TestRelayPublish_PermanentReject_DeadLettersNotBuffers is the ready-1c2
// reproduction (translated to the nostr backend): an event a relay PERMANENTLY
// rejects ("invalid:" — malformed, no retry can ever fix it) must be
// dead-lettered, NOT buffered to the pending retry queue where it would be
// re-published forever while the operator is told "buffered, will retry".
func TestRelayPublish_PermanentReject_DeadLettersNotBuffers(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{contentRelay(t)},
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	ev := signedEvent(t, k, "bad")
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); err != nil {
		t.Fatalf("PublishEvents: %v", err)
	}
	if fileHasID(t, pending, ev.ID) {
		t.Errorf("permanently-rejected event was buffered to pending.jsonl (the ready-1c2 defect); it must be dead-lettered instead")
	}
	if !fileHasID(t, rejected, ev.ID) {
		t.Errorf("permanently-rejected event was NOT dead-lettered to nostr-rejected.jsonl")
	}
}

// TestRelayPublish_Transient_Buffers is the control: a purely transient delivery
// failure (relay hung up, no OK) must still buffer for retry — the fix must not
// over-classify and drop deliverable events.
func TestRelayPublish_Transient_Buffers(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{contentRelay(t)},
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	ev := signedEvent(t, k, "drop")
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); err != nil {
		t.Fatalf("PublishEvents: %v", err)
	}
	if !fileHasID(t, pending, ev.ID) {
		t.Errorf("transient failure should have buffered to pending.jsonl for retry")
	}
	if fileHasID(t, rejected, ev.ID) {
		t.Errorf("transient failure must NOT be dead-lettered")
	}
}

// TestRelayPublish_Accepted_NeitherFile is the control: an accepted event
// touches neither buffer.
func TestRelayPublish_Accepted_NeitherFile(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{contentRelay(t)},
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	ev := signedEvent(t, k, "ok")
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); err != nil {
		t.Fatalf("PublishEvents: %v", err)
	}
	if fileHasID(t, pending, ev.ID) || fileHasID(t, rejected, ev.ID) {
		t.Errorf("accepted event must not be buffered or dead-lettered")
	}
}

// TestRelayPublish_AutoDrainsBacklogOnConnectedWrite proves the automatic flush:
// an event a prior offline write buffered is drained from the pending queue as
// soon as ANY later write reaches a relay — no manual flush command needed.
func TestRelayPublish_AutoDrainsBacklogOnConnectedWrite(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, _ := readyPaths(dir)

	// Seed the offline backlog: an event an earlier disconnected write buffered.
	backlog := signedEvent(t, k, "ok-backlog")
	if err := appendPendingEvent(pending, backlog); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	if !fileHasID(t, pending, backlog.ID) {
		t.Fatalf("precondition: backlog should be buffered in pending")
	}

	// A later write that reaches an accepting relay.
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{contentRelay(t)},
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	fresh := signedEvent(t, k, "ok-fresh")
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{fresh}); err != nil {
		t.Fatalf("PublishEvents: %v", err)
	}

	if fileHasID(t, pending, backlog.ID) {
		t.Errorf("connected write must auto-drain the buffered backlog event %s", backlog.ID)
	}
	if fileHasID(t, pending, fresh.ID) {
		t.Errorf("accepted fresh event must not be buffered")
	}
}

// TestRelayPublish_NoAutoDrainWhenOffline is the control: a write that reaches NO
// relay does not touch the backlog — connectivity was never proven, so the queue
// stays intact for a later successful write to drain.
func TestRelayPublish_NoAutoDrainWhenOffline(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, _ := readyPaths(dir)

	backlog := signedEvent(t, k, "ok-backlog")
	if err := appendPendingEvent(pending, backlog); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{fixedRelay(t, false, "", true)}, // hangs up -> transient, no connectivity
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	fresh := signedEvent(t, k, "ok-fresh")
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{fresh}); err != nil {
		t.Fatalf("PublishEvents: %v", err)
	}

	if !fileHasID(t, pending, backlog.ID) {
		t.Errorf("offline write must NOT drain the backlog; it should remain buffered for a later flush")
	}
}

// TestRelayPublish_OkFalseDuplicate_CountsAsAccepted guards the verifier's
// finding-1: a nonstandard relay that replies OK,false,"duplicate:" nonetheless
// holds the event, so it must count as accepted — AnyRelay true, neither buffered
// nor dead-lettered — not read back as a silent drop.
func TestRelayPublish_OkFalseDuplicate_CountsAsAccepted(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{fixedRelay(t, false, "duplicate: already have it", false)},
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	ev := signedEvent(t, k, "x")
	res, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev})
	if err != nil {
		t.Fatalf("PublishEvents: %v", err)
	}
	if res.Buffered || res.Rejected {
		t.Errorf("ok=false duplicate must count as accepted: Buffered=%v Rejected=%v", res.Buffered, res.Rejected)
	}
	if len(res.Events) != 1 || !res.Events[0].AnyRelay {
		t.Errorf("ok=false duplicate must set AnyRelay=true, got events=%+v", res.Events)
	}
	if fileHasID(t, pending, ev.ID) || fileHasID(t, rejected, ev.ID) {
		t.Errorf("duplicate-accepted event must not be buffered or dead-lettered")
	}
}

// TestRelayPublish_PermanentAtOneRelayTransientAtAnother_Buffers is the review
// finding-1/2 regression guard: when one relay PERMANENTLY rejects an event
// ("invalid:") but another is only transiently unreachable, the event must be
// BUFFERED for retry — never dead-lettered — because the down relay may accept it
// on recovery. Transient dominates permanent.
func TestRelayPublish_PermanentAtOneRelayTransientAtAnother_Buffers(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)
	permRelay := fixedRelay(t, false, "invalid: bad event", false)
	downRelay := fixedRelay(t, false, "", true) // hangs up -> transient
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{permRelay, downRelay},
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	ev := signedEvent(t, k, "anything")
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); err != nil {
		t.Fatalf("PublishEvents: %v", err)
	}
	if fileHasID(t, rejected, ev.ID) {
		t.Errorf("event was dead-lettered even though a relay was only transiently down — it must be retried")
	}
	if !fileHasID(t, pending, ev.ID) {
		t.Errorf("event should have been buffered for retry (transient dominates permanent)")
	}
}

// TestRelayPublish_DeadLetterWriteFailure_FallsBackToBuffer is the finding-4
// regression guard: if the dead-letter write fails (here: nostr-rejected.jsonl
// path is occupied by a directory so the append cannot open it), the permanently
// rejected event must fall back to the pending retry buffer rather than vanish
// from every on-disk queue.
func TestRelayPublish_DeadLetterWriteFailure_FallsBackToBuffer(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)
	// Occupy the dead-letter path with a directory so OpenFile(append) fails.
	if err := os.MkdirAll(rejected, 0o700); err != nil {
		t.Fatalf("mkdir rejected-as-dir: %v", err)
	}
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{fixedRelay(t, false, "invalid: bad event", false)},
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	ev := signedEvent(t, k, "anything")
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); err != nil {
		t.Fatalf("PublishEvents: %v", err)
	}
	if !fileHasID(t, pending, ev.ID) {
		t.Errorf("dead-letter write failed but event was not re-buffered — it is lost from every on-disk queue")
	}
}

// TestFlushNostrPending_DeadLettersPermanentKeepsTransient proves the flush path
// (a) dead-letters a permanently-rejected buffered event instead of keeping it
// forever, (b) drops an accepted event, and (c) keeps a transient one for the
// next flush — a poisoned record at the head never blocks the rest.
func TestFlushNostrPending_DeadLettersPermanentKeepsTransient(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)

	evBad := signedEvent(t, k, "bad")   // permanent -> dead-letter
	evGood := signedEvent(t, k, "ok")   // accepted  -> drop
	evDrop := signedEvent(t, k, "drop") // transient -> keep
	// Seed the pending buffer with the poisoned record at the HEAD.
	for _, e := range []*nostr.Event{evBad, evGood, evDrop} {
		if err := appendPendingEvent(pending, e); err != nil {
			t.Fatalf("seed pending: %v", err)
		}
	}

	relay := contentRelay(t)
	if _, err := FlushNostrPending(context.Background(), pending, []string{relay}, 3*time.Second, false); err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}

	if fileHasID(t, pending, evBad.ID) {
		t.Errorf("permanently-rejected head record still in pending.jsonl (blocks the queue forever)")
	}
	if !fileHasID(t, rejected, evBad.ID) {
		t.Errorf("permanently-rejected record was not dead-lettered")
	}
	if fileHasID(t, pending, evGood.ID) {
		t.Errorf("accepted record should have been dropped from pending.jsonl")
	}
	if !fileHasID(t, pending, evDrop.ID) {
		t.Errorf("transient record should remain in pending.jsonl for the next flush")
	}
}

// countIDOccurrences counts how many lines/records in the JSONL file at path
// contain id — used to detect duplicate dead-letter records, not just presence.
func countIDOccurrences(t *testing.T, path, id string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return 0
	}
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return strings.Count(string(data), id)
}

// TestRelayPublish_PermanentReject_DeadLetterDoesNotGrowOnRepeat is ready-c3e
// finding (2)'s regression guard for the PER-EVENT dial transport
// (relayPublish, driven by PublishEvents — the live single-mutation write
// path). `rd relay repair` re-measures a relay's gap every round (5 by
// default, relayRepairCmd's --rounds) and republishes exactly that gap; for a
// PERMANENTLY rejected event the gap can never close, so the identical event
// was re-sent and re-dead-lettered on EVERY round of EVERY repair invocation —
// ground truth on two real projects, re-measured directly against each
// project's on-disk nostr-rejected.jsonl: 3dl's reached 12,261,702 bytes for
// 8 DISTINCT oversized event ids across 2 stale coordinates (3dl-7e0, 3dl-ece
// — NOT "2 distinct events", an earlier restatement's conflation of event
// count with coordinate count), galtrader's reached 3,599,901 bytes for 1
// distinct event at 1 coordinate. Publishing the identical already-dead-
// lettered event again must add ZERO new dead-letter records — the file is
// bounded by the number of DISTINCT permanently-rejected events, not by how
// many times a repair loop rediscovers the same one.
//
// This test alone does not cover `rd relay repair`'s ACTUAL transport — see
// TestRelayPublishBatch_PermanentReject_DeadLetterDoesNotGrowOnRepeat below
// for the batched-connection path (relayPublishBatch / PublishBoardDelta /
// GuardedPublishMany) that `rd relay repair` really drives, and which is what
// produced the 12MB/3.6MB files above.
func TestRelayPublish_PermanentReject_DeadLetterDoesNotGrowOnRepeat(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{fixedRelay(t, false, "invalid: event is 999999 bytes, exceeds this relay's max of 65536 bytes (64KiB, strfry default)", false)},
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	ev := signedEvent(t, k, "oversized-payload")

	const rounds = 5 // mirrors relayRepairCmd's default --rounds
	for i := 0; i < rounds; i++ {
		if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{ev}); err != nil {
			t.Fatalf("round %d: PublishEvents: %v", i, err)
		}
	}
	if got := countIDOccurrences(t, rejected, ev.ID); got != 1 {
		t.Errorf("dead-letter file has %d record(s) for the same permanently-rejected event after %d repair rounds, want exactly 1 (unbounded growth, ready-c3e)", got, rounds)
	}
}

// TestRelayPublishBatch_PermanentReject_DeadLetterDoesNotGrowOnRepeat is
// ready-c3e finding (2)'s regression guard for the transport `rd relay
// repair` ACTUALLY drives: PublishBoardDelta -> relayPublishBatch ->
// GuardedPublishMany, ONE websocket connection for the whole gap, not one
// dial per event (ready-260). This is the path that produced the real 12MB/
// 3.6MB dead-letter files — TestRelayPublish_PermanentReject_
// DeadLetterDoesNotGrowOnRepeat above exercises PublishEvents/relayPublish
// instead, which never touches relayPublishBatch or GuardedPublishMany at
// all, so it could pass while the actual repair-loop transport still grew the
// file without bound.
//
// storeRelay (boardbackfill_test.go) is reused here specifically because it
// holds a PERSISTENT connection per dial and answers every EVENT frame with
// its rejectAll message — the same shape `rd relay repair`'s real target
// relay used (a single connection, one OK per EVENT, the same "invalid: ...
// exceeds this relay's max" message every time).
func TestRelayPublishBatch_PermanentReject_DeadLetterDoesNotGrowOnRepeat(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)
	sr := newStoreRelay(t)
	sr.rejectAll = "invalid: event is 999999 bytes, exceeds this relay's max of 65536 bytes (64KiB, strfry default)"
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
		WriteRelays: []string{sr.url},
		PendingPath: pending,
		Timeout:     3 * time.Second,
	}
	ev := signedEvent(t, k, "oversized-payload")

	const rounds = 5 // mirrors relayRepairCmd's default --rounds
	for i := 0; i < rounds; i++ {
		var res PublishResult
		pub.relayPublishBatch(context.Background(), &res, []*nostr.Event{ev})
	}
	if got := countIDOccurrences(t, rejected, ev.ID); got != 1 {
		t.Errorf("dead-letter file has %d record(s) for the same permanently-rejected event after %d relayPublishBatch (rd relay repair's real transport) rounds, want exactly 1 (unbounded growth, ready-c3e)", got, rounds)
	}
}

// TestDeadLetterHasEvent_SkipsMalformedRecords is ready-c3e finding (4)'s
// regression guard: deadLetterHasEvent must not FAIL OPEN on a single
// malformed or torn record. appendRejectedEvent's own doc states the file is
// at-least-once (a crash mid-append can leave a torn line) — that is an
// ANTICIPATED on-disk state, not a hypothetical, so a corrupt line must never
// disable dedup for every record scanned after it. This directly exercises
// deadLetterHasEvent (not through a Publisher), so each shape of corruption is
// isolated.
func TestDeadLetterHasEvent_SkipsMalformedRecords(t *testing.T) {
	k := testKey(t)
	target := signedEvent(t, k, "target-event")
	other := signedEvent(t, k, "some-other-event")
	targetLine, err := json.Marshal(RejectedRecord{Event: target, Reason: "invalid: too big"})
	if err != nil {
		t.Fatalf("marshal target record: %v", err)
	}
	otherLine, err := json.Marshal(RejectedRecord{Event: other, Reason: "invalid: too big"})
	if err != nil {
		t.Fatalf("marshal other record: %v", err)
	}
	const corrupt = `{"event":{"id":"deadbeef", "content": UNTERMINATED`

	writeLines := func(t *testing.T, lines ...string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "nostr-rejected.jsonl")
		var buf strings.Builder
		for _, l := range lines {
			buf.WriteString(l)
			buf.WriteString("\n")
		}
		if err := os.WriteFile(path, []byte(buf.String()), 0o600); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
		return path
	}

	t.Run("corrupt line AFTER the target still finds it", func(t *testing.T) {
		path := writeLines(t, string(targetLine), corrupt)
		if !deadLetterHasEvent(path, target.ID) {
			t.Errorf("deadLetterHasEvent missed a record that comes before a corrupt line")
		}
	})

	t.Run("corrupt line BEFORE the target must NOT fail open (the bug)", func(t *testing.T) {
		path := writeLines(t, corrupt, string(targetLine))
		if !deadLetterHasEvent(path, target.ID) {
			t.Errorf("deadLetterHasEvent returned false because of a malformed record BEFORE the target — " +
				"failing open here silently disables dedup for every record after the first corrupt line, " +
				"restoring ready-c3e's unbounded dead-letter growth")
		}
	})

	t.Run("corrupt line between two good non-matching records", func(t *testing.T) {
		path := writeLines(t, string(otherLine), corrupt, string(targetLine))
		if !deadLetterHasEvent(path, target.ID) {
			t.Errorf("deadLetterHasEvent missed the target past a corrupt line sandwiched between good records")
		}
	})

	t.Run("target genuinely absent, only corrupt/other records", func(t *testing.T) {
		path := writeLines(t, string(otherLine), corrupt)
		if deadLetterHasEvent(path, target.ID) {
			t.Errorf("deadLetterHasEvent reported true for an id that was never recorded")
		}
	})

	t.Run("file does not exist", func(t *testing.T) {
		if deadLetterHasEvent(filepath.Join(t.TempDir(), "does-not-exist.jsonl"), target.ID) {
			t.Errorf("deadLetterHasEvent reported true for a nonexistent file")
		}
	})

	t.Run("only corrupt lines, no valid records at all", func(t *testing.T) {
		path := writeLines(t, corrupt, corrupt)
		if deadLetterHasEvent(path, target.ID) {
			t.Errorf("deadLetterHasEvent reported true when the file has no valid records")
		}
	})
}
