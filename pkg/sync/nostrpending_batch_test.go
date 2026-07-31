// Batched offline-buffer drain tests (ready-046).
//
// FlushNostrPending used to redial a fresh websocket per BUFFERED EVENT per
// relay (publishEventToRelays). That is durable but not fast: a large offline
// backlog turned every repair round into a redial loop dominating the round's
// wall-clock, even though the events themselves were never at risk. These
// tests prove the drain now opens a BOUNDED number of connections (one per
// relay, not one per event), and that the at-least-once dead-letter contract
// (fsync the rejection BEFORE the event leaves the retry buffer) survives the
// switch to a batched connection.
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	"github.com/gorilla/websocket"
)

// acceptingBatchRelay is a fake relay that accepts every EVENT frame it is
// sent over a PERSISTENT connection (looping reads, unlike a one-shot
// per-connection fixture) and counts how many connections were opened. That
// count is the direct observable that separates a batched drain from the
// ready-046 defect: draining N buffered events through ONE relay must open
// ONE connection, not N.
func acceptingBatchRelay(t *testing.T) (url string, conns func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		mu.Lock()
		count++
		mu.Unlock()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if err := json.Unmarshal(data, &frame); err != nil || len(frame) < 2 {
				continue
			}
			var typ string
			_ = json.Unmarshal(frame[0], &typ)
			if typ != "EVENT" {
				continue
			}
			var ev struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(frame[1], &ev)
			resp, _ := json.Marshal([]any{"OK", ev.ID, true, ""})
			if werr := conn.WriteMessage(websocket.TextMessage, resp); werr != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// TestFlushNostrPending_BatchesOneConnectionPerRelay is the ready-046 proof:
// draining a large backlog through one relay costs exactly ONE connection.
// Before this fix, FlushNostrPending's per-event loop dialed a fresh websocket
// for EVERY buffered event, so this exact scenario would have measured
// connections == n (40), not 1.
func TestFlushNostrPending_BatchesOneConnectionPerRelay(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, _ := readyPaths(dir)

	const n = 40
	for i := 0; i < n; i++ {
		ev := signedEvent(t, k, fmt.Sprintf("backlog-%d", i))
		if err := appendPendingEvent(pending, ev); err != nil {
			t.Fatalf("seed pending %d: %v", i, err)
		}
	}

	url, conns := acceptingBatchRelay(t)
	res, err := FlushNostrPending(context.Background(), pending, []string{url}, false)
	if err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if res.Flushed != n {
		t.Fatalf("Flushed=%d, want %d — every buffered event should have reached the relay", res.Flushed, n)
	}
	if got := conns(); got != 1 {
		t.Fatalf("relay saw %d connections draining a %d-event backlog through 1 relay, want exactly 1 (batched drain, ready-046)", got, n)
	}
}

// TestFlushNostrPending_BatchesOneConnectionPerRelay_MultipleRelays extends the
// above to two write relays: the bound is "one connection PER RELAY", not "one
// connection total" — each relay independently must see exactly 1 connection
// for the whole backlog, i.e. 2 connections total for a 2-relay fleet
// regardless of how many events are buffered.
func TestFlushNostrPending_BatchesOneConnectionPerRelay_MultipleRelays(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, _ := readyPaths(dir)

	const n = 25
	for i := 0; i < n; i++ {
		ev := signedEvent(t, k, fmt.Sprintf("multi-backlog-%d", i))
		if err := appendPendingEvent(pending, ev); err != nil {
			t.Fatalf("seed pending %d: %v", i, err)
		}
	}

	url1, conns1 := acceptingBatchRelay(t)
	url2, conns2 := acceptingBatchRelay(t)
	res, err := FlushNostrPending(context.Background(), pending, []string{url1, url2}, false)
	if err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if res.Flushed != n {
		t.Fatalf("Flushed=%d, want %d", res.Flushed, n)
	}
	if got := conns1(); got != 1 {
		t.Fatalf("relay 1 saw %d connections, want exactly 1", got)
	}
	if got := conns2(); got != 1 {
		t.Fatalf("relay 2 saw %d connections, want exactly 1", got)
	}
}

// TestFlushNostrPending_PermanentAtOneRelayTransientAtAnother_Buffers is the
// FlushNostrPending-path counterpart of
// TestRelayPublish_PermanentAtOneRelayTransientAtAnother_Buffers: partial
// acceptance across relays must classify identically whether the drain is
// batched or per-event. When one relay PERMANENTLY rejects a buffered event but
// another is only transiently unreachable, the event must stay BUFFERED (never
// dead-lettered) — the down relay may still accept it once it recovers.
func TestFlushNostrPending_PermanentAtOneRelayTransientAtAnother_Buffers(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)

	ev := signedEvent(t, k, "anything")
	if err := appendPendingEvent(pending, ev); err != nil {
		t.Fatalf("seed pending: %v", err)
	}

	permRelay := fixedRelay(t, false, "invalid: bad event", false)
	downRelay := fixedRelay(t, false, "", true) // hangs up -> transient

	if _, err := FlushNostrPending(context.Background(), pending, []string{permRelay, downRelay}, false); err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if fileHasID(t, rejected, ev.ID) {
		t.Errorf("event was dead-lettered even though a relay was only transiently down — it must be retried")
	}
	if !fileHasID(t, pending, ev.ID) {
		t.Errorf("event should remain buffered for retry (transient dominates permanent)")
	}
}

// TestFlushNostrPending_DeadLetterWriteFailure_FallsBackToBuffer is the
// at-least-once / fsync-before-removal proof this item's DONE criteria calls
// for, run against the FLUSH path specifically (the finding-4 regression guard,
// TestRelayPublish_DeadLetterWriteFailure_FallsBackToBuffer, only covers the
// fresh-publish path). The dead-letter write (appendRejectedEvent) is made to
// fail by occupying nostr-rejected.jsonl's path with a directory. The event
// MUST fall back into the retry buffer (`remaining`) rather than being dropped:
// rewritePendingEvents only removes an event from pending.jsonl once it is
// KNOWN durable somewhere else (a relay ack, or a successful dead-letter
// write) — never on the strength of an attempt alone.
func TestFlushNostrPending_DeadLetterWriteFailure_FallsBackToBuffer(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)

	ev := signedEvent(t, k, "bad") // contentRelay: "bad" -> permanent rejection
	if err := appendPendingEvent(pending, ev); err != nil {
		t.Fatalf("seed pending: %v", err)
	}
	// Occupy the dead-letter path with a directory so the append cannot open it.
	if err := os.MkdirAll(rejected, 0o700); err != nil {
		t.Fatalf("mkdir rejected-as-dir: %v", err)
	}

	relay := contentRelay(t)
	res, err := FlushNostrPending(context.Background(), pending, []string{relay}, false)
	if err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if len(res.WriteErrors) == 0 {
		t.Fatalf("expected a WriteErrors entry for the failed dead-letter write, got none")
	}
	if !fileHasID(t, pending, ev.ID) {
		t.Errorf("dead-letter write failed but event was not kept in pending.jsonl — it is lost from every on-disk queue")
	}
	if res.Rejected != 0 {
		t.Errorf("Rejected=%d, want 0 — a failed dead-letter write must not be counted as dead-lettered", res.Rejected)
	}
}

// hangupBatchRelay is a fake relay that accepts every EVENT frame whose
// Content does NOT contain "hangup", replying OK,true the instant it reads
// each one — and, the moment it reads a frame whose Content DOES contain
// "hangup", stops acking anything further on this connection (the poison
// frame and everything behind it get NO reply, ever, on this connection).
//
// Before actually closing, it DRAINS (reads and discards, with a short idle
// deadline) whatever the client already wrote into THIS connection's receive
// buffer. This is not cosmetic: PublishMany's write-ahead window (32) means a
// client can have written several events past the poison before it ever
// blocks on a read, and a server that closes its socket while data sent BY
// THE CLIENT is still unread in its own kernel receive buffer gets an OS-level
// RST, not a clean FIN — and an RST can silently discard bytes THIS SERVER
// already wrote (the OK replies for the good events before the poison) if
// they have not yet reached the client's kernel buffer. That race is exactly
// what a real (mis)behaving relay's TCP stack would do too, but it makes a
// fixed-input-order test fixture flaky (observed directly: an earlier version
// of this fixture measured Flushed swinging between 0 and 20 for the IDENTICAL
// 40-event/1-poison input across repeated runs). Draining first means the
// close is always a clean FIN, so the already-sent OKs are always delivered —
// deterministic given a fixed input order, matching the fixture's job of
// pinning down forward-progress behaviour rather than TCP-stack timing.
// Counts connections like acceptingBatchRelay, so a test can measure how many
// extra connections recovering from a hangup actually costs.
func hangupBatchRelay(t *testing.T) (url string, conns func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		mu.Lock()
		count++
		mu.Unlock()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if err := json.Unmarshal(data, &frame); err != nil || len(frame) < 2 {
				continue
			}
			var typ string
			_ = json.Unmarshal(frame[0], &typ)
			if typ != "EVENT" {
				continue
			}
			var ev struct {
				ID      string `json:"id"`
				Content string `json:"content"`
			}
			_ = json.Unmarshal(frame[1], &ev)
			if strings.Contains(ev.Content, "hangup") {
				// Poisoned event: never ack THIS frame or anything still
				// behind it — the exact shape the item was filed from ("a
				// relay that transiently rejects part of every burst"). Drain
				// whatever the client already wrote before closing (see doc
				// comment) so the close is a clean FIN, not an RST that could
				// erase the OKs already written for the good events above.
				for {
					if derr := conn.SetReadDeadline(time.Now().Add(200 * time.Millisecond)); derr != nil {
						break
					}
					if _, _, rerr := conn.ReadMessage(); rerr != nil {
						break
					}
				}
				return
			}
			resp, _ := json.Marshal([]any{"OK", ev.ID, true, ""})
			if werr := conn.WriteMessage(websocket.TextMessage, resp); werr != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// TestFlushNostrPending_HangupPartwayThroughBatch_MakesForwardProgress is the
// adversary's own measured regression, reproduced and closed: a batch drain
// sharing ONE connection for the WHOLE buffer used to let a single
// hangup-triggering event AT THE HEAD of the batch block every event queued
// behind it, on EVERY round — measured as flushed=0/remaining=6 forever, even
// though 5 of those 6 events were perfectly deliverable. This is the
// fixture the adversary measured that regression against (1 hangup-triggering
// event at the head + 5 acceptable events, 3 flush rounds): publishManyResilient's
// bisection must recover what the per-event path this batching replaced would
// have (round 1: flushed=5, remaining=1), and every later round must stay at
// flushed=0/remaining=1 (the lone poison, still transient, never lost, never
// re-growing) rather than wedging on 6.
func TestFlushNostrPending_HangupPartwayThroughBatch_MakesForwardProgress(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)

	poison := signedEvent(t, k, "hangup-me") // at the HEAD of the buffer
	if err := appendPendingEvent(pending, poison); err != nil {
		t.Fatalf("seed poison: %v", err)
	}
	var good []*nostr.Event
	for i := 0; i < 5; i++ {
		ev := signedEvent(t, k, fmt.Sprintf("ok-%d", i))
		good = append(good, ev)
		if err := appendPendingEvent(pending, ev); err != nil {
			t.Fatalf("seed good %d: %v", i, err)
		}
	}

	url, _ := hangupBatchRelay(t)

	// Round 1: the 5 deliverable events must flush DESPITE the poison at the
	// head — this is exactly the case measured as flushed=0/remaining=6 before
	// this fix.
	res, err := FlushNostrPending(context.Background(), pending, []string{url}, false)
	if err != nil {
		t.Fatalf("round 1 FlushNostrPending: %v", err)
	}
	if res.Flushed != 5 {
		t.Fatalf("round 1: Flushed=%d, want 5 — the poison at the head must not block the deliverable events behind it (this is the head-of-line regression)", res.Flushed)
	}
	if res.Remaining != 1 {
		t.Fatalf("round 1: Remaining=%d, want 1 (only the poison)", res.Remaining)
	}
	for _, ev := range good {
		if fileHasID(t, pending, ev.ID) {
			t.Errorf("round 1: deliverable event %s still in pending.jsonl", ev.ID)
		}
	}
	if !fileHasID(t, pending, poison.ID) {
		t.Fatalf("round 1: poisoned event should remain buffered (transient, not lost) — pending.jsonl is missing it")
	}
	if fileHasID(t, rejected, poison.ID) {
		t.Errorf("a relay hangup is TRANSIENT, not permanent — the poisoned event must never be dead-lettered")
	}

	// Rounds 2 and 3: the poison alone remains, forever transient — steady
	// state, no crash, no re-growth, and (this is the wedge the adversary
	// measured) still no permanent zero-progress stall.
	for round := 2; round <= 3; round++ {
		res, err := FlushNostrPending(context.Background(), pending, []string{url}, false)
		if err != nil {
			t.Fatalf("round %d FlushNostrPending: %v", round, err)
		}
		if res.Flushed != 0 || res.Remaining != 1 {
			t.Fatalf("round %d: Flushed=%d Remaining=%d, want 0/1 (steady state on the lone poison)", round, res.Flushed, res.Remaining)
		}
		if !fileHasID(t, pending, poison.ID) {
			t.Fatalf("round %d: poisoned event vanished from pending.jsonl — it must stay buffered, not be lost", round)
		}
	}
}

// TestFlushNostrPending_HangupMidLargeBatch_ForwardProgress extends the above
// to a 40-event backlog with ONE hangup-triggering event at position 20 (the
// adversary's second measured scenario) — proving the fix is not merely a
// small-batch special case, and that recovering from a mid-batch hangup costs
// FAR fewer connections than falling all the way back to one-dial-per-event.
func TestFlushNostrPending_HangupMidLargeBatch_ForwardProgress(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, _ := readyPaths(dir)

	const n = 40
	const poisonAt = 20
	var poisonID string
	for i := 0; i < n; i++ {
		content := fmt.Sprintf("backlog-%d", i)
		if i == poisonAt {
			content = "hangup-me"
		}
		ev := signedEvent(t, k, content)
		if i == poisonAt {
			poisonID = ev.ID
		}
		if err := appendPendingEvent(pending, ev); err != nil {
			t.Fatalf("seed pending %d: %v", i, err)
		}
	}

	url, conns := hangupBatchRelay(t)
	res, err := FlushNostrPending(context.Background(), pending, []string{url}, false)
	if err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if res.Flushed != n-1 {
		t.Fatalf("Flushed=%d, want %d — every event except the one hit by a mid-batch hangup must drain in round 1 (parity with the old per-event path, which evacuated all but the poisoned event in round 1)", res.Flushed, n-1)
	}
	if res.Remaining != 1 {
		t.Fatalf("Remaining=%d, want 1 (only the poisoned event)", res.Remaining)
	}
	if !fileHasID(t, pending, poisonID) {
		t.Fatalf("poisoned event should remain buffered, not lost")
	}
	if got := conns(); got >= n/2 {
		t.Errorf("connections opened=%d, want well under %d — recovering from ONE poisoned event in a %d-event backlog must not degrade anywhere near one-dial-per-event", got, n/2, n)
	}
}

// TestFlushNostrPending_GuardRefusal_DoesNotBlockOtherEvents is the SECOND
// defect a veracity adversary found: GuardedPublishMany is documented
// all-or-nothing — a SINGLE event addressing the reserved production board
// coordinate refuses the ENTIRE batch when the flush is not marked production,
// and every ack in that refused batch carries the guard error, which
// classifyRelayResult maps to transient. Before this fix, one poison-pill line
// (a reserved-board event queued alongside ordinary ones) stalled the WHOLE
// drain forever, where the per-event path (GuardedPublish, one dial per event)
// isolated the refusal to that one event. publishManyResilient's bisection is
// the SAME mechanism that recovers a mid-batch hangup (see the tests above);
// this proves it isolates a guard refusal identically.
func TestFlushNostrPending_GuardRefusal_DoesNotBlockOtherEvents(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)

	// A card event addressing the RESERVED production board coordinate — the
	// exact shape GuardedPublishMany refuses outright when production=false.
	card := CardSpec{
		ItemID: "ready-guard-repro", Title: "reserved board event", Status: state.StatusActive,
		Priority: "p3", Type: "task", BoardD: reservedProductionBoardD,
	}
	poison, err := BuildCardEvent(k, card, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	if err := appendPendingEvent(pending, poison); err != nil {
		t.Fatalf("seed poison: %v", err)
	}
	var good []*nostr.Event
	for i := 0; i < 5; i++ {
		ev := signedEvent(t, k, fmt.Sprintf("ordinary-%d", i))
		good = append(good, ev)
		if err := appendPendingEvent(pending, ev); err != nil {
			t.Fatalf("seed good %d: %v", i, err)
		}
	}

	url, _ := acceptingBatchRelay(t)
	// production=false: the posture the auto-drain runs with for anything
	// other than the sanctioned `rd nostr flush` / Publisher.Production path.
	res, err := FlushNostrPending(context.Background(), pending, []string{url}, false)
	if err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if res.Flushed != 5 {
		t.Fatalf("Flushed=%d, want 5 — a single guard-refused event must not block the ordinary events sharing its batch", res.Flushed)
	}
	if res.Remaining != 1 {
		t.Fatalf("Remaining=%d, want 1 (the guard-refused event)", res.Remaining)
	}
	for _, ev := range good {
		if fileHasID(t, pending, ev.ID) {
			t.Errorf("ordinary event %s still buffered behind the guard-refused event", ev.ID)
		}
	}
	if !fileHasID(t, pending, poison.ID) {
		t.Fatalf("guard-refused event vanished — it must stay buffered (transient), never lost")
	}
	if fileHasID(t, rejected, poison.ID) {
		t.Errorf("a guard refusal is not a relay-classified permanent rejection — it must never be dead-lettered")
	}
}

// TestFlushNostrPending_TotallyDownRelay_BoundedRecursion_NoDataLoss is the
// WORST CASE for publishManyResilient's bisection: every single event fails
// (a relay that is not listening at all — every dial refused), so the
// no-progress branch bisects all the way down to singletons on EVERY relay
// attempt, not just around one poisoned event. This proves that degenerate
// case still terminates promptly (the recursion tree for n events has at most
// 2n-1 nodes — bounded, not exponential and not unbounded) and, critically,
// loses nothing: every event must still end up buffered for retry, none
// dropped and none dead-lettered (a fully unreachable relay is a transport
// failure, never permanent).
func TestFlushNostrPending_TotallyDownRelay_BoundedRecursion_NoDataLoss(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)

	const n = 40
	var ids []string
	for i := 0; i < n; i++ {
		ev := signedEvent(t, k, fmt.Sprintf("down-%d", i))
		ids = append(ids, ev.ID)
		if err := appendPendingEvent(pending, ev); err != nil {
			t.Fatalf("seed pending %d: %v", i, err)
		}
	}

	// Nothing is listening on this port — every dial is refused immediately.
	const unreachable = "ws://127.0.0.1:1"
	res, err := FlushNostrPending(context.Background(), pending, []string{unreachable}, false)
	if err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if res.Flushed != 0 {
		t.Fatalf("Flushed=%d, want 0 — an unreachable relay accepts nothing", res.Flushed)
	}
	if res.Remaining != n {
		t.Fatalf("Remaining=%d, want %d — every event must stay buffered when the relay is completely unreachable", res.Remaining, n)
	}
	for _, id := range ids {
		if !fileHasID(t, pending, id) {
			t.Errorf("event %s lost from pending.jsonl against a totally down relay", id)
		}
		if fileHasID(t, rejected, id) {
			t.Errorf("event %s dead-lettered against a totally down relay — a dial failure is transient, never permanent", id)
		}
	}
}

// hangsAfterUpgradeRelay is a fake relay that completes the WebSocket upgrade
// — so a dial from PublishMany SUCCEEDS, unlike ws://127.0.0.1:1's instant
// connection-refused — and then closes immediately without reading or acking
// a single frame, so EVERY event sent over this connection fails. Counts
// connections like the other fixtures in this file. The immediate close
// (rather than accepting and going silent) is deliberate: PublishMany's idle
// read deadline is 60s, and a fixture that just went silent would make a
// worst-case bisection test (which opens MANY connections) unusably slow;
// closing immediately fails fast and deterministically instead.
func hangsAfterUpgradeRelay(t *testing.T) (url string, conns func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		mu.Lock()
		count++
		mu.Unlock()
		conn.Close() // hang up immediately — no read, no ack, ever
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// TestFlushNostrPending_HangsAfterUpgrade_DialCountBounded is the SAME
// worst-case shape as TestFlushNostrPending_TotallyDownRelay_BoundedRecursion_
// NoDataLoss above, but against a relay that actually completes a TCP/WS
// handshake before hanging up on every event, instead of refusing the dial
// outright. ws://127.0.0.1:1's connection-refused is the CHEAPEST possible
// transport failure — instant, no handshake, nothing to count — which is
// exactly why that test cannot see the amplification publishManyResilient's
// no-progress bisection branch pays: a veracity adversary measured 127 dials
// recovering a 64-event totally-down batch (vs 1 pre-rework, and 64 on the
// original one-dial-per-event path), and nothing in the suite asserted a dial
// count, so that cost was invisible. This fixture makes each bisection
// attempt a REAL, countable connection, and this test asserts the exact bound
// publishManyResilient/resolveBatch's own doc comment claims: the recursion
// tree for n events has at most 2n-1 nodes (T(n) = 1 + 2*T(n/2), T(1) = 1;
// for n=64 that is exactly 127 — matching the adversary's own measurement).
func TestFlushNostrPending_HangsAfterUpgrade_DialCountBounded(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)

	const n = 64
	var ids []string
	for i := 0; i < n; i++ {
		ev := signedEvent(t, k, fmt.Sprintf("blackhole-%d", i))
		ids = append(ids, ev.ID)
		if err := appendPendingEvent(pending, ev); err != nil {
			t.Fatalf("seed pending %d: %v", i, err)
		}
	}

	url, conns := hangsAfterUpgradeRelay(t)
	res, err := FlushNostrPending(context.Background(), pending, []string{url}, false)
	if err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if res.Flushed != 0 {
		t.Fatalf("Flushed=%d, want 0 — a relay that hangs up on every event accepts nothing", res.Flushed)
	}
	if res.Remaining != n {
		t.Fatalf("Remaining=%d, want %d", res.Remaining, n)
	}
	for _, id := range ids {
		if !fileHasID(t, pending, id) {
			t.Errorf("event %s lost from pending.jsonl against a relay that hangs up on every event", id)
		}
		if fileHasID(t, rejected, id) {
			t.Errorf("event %s dead-lettered against a hung-up relay — a transport failure is transient, never permanent", id)
		}
	}
	if got, want := conns(), 2*n-1; got != want {
		t.Fatalf("connections opened=%d, want exactly %d (2n-1, the documented worst-case bisection-tree size for n=%d totally-failing events) — this is the dial amplification a veracity adversary measured (127 dials for n=64) that ws://127.0.0.1:1's instant connection-refused hides entirely", got, want, n)
	}
}

// rstMidBatchRelay is hangupBatchRelay's UNCOVERED sibling case, named
// explicitly in hangupBatchRelay's own doc comment: it acks every non-poison
// EVENT frame identically, but the instant it reads a frame whose Content
// contains "hangup" it forces a hard OS-level RST — SetLinger(0) then Close,
// no drain — instead of hangupBatchRelay's deliberate clean-FIN drain. An RST
// can silently discard bytes the server already wrote (the OK replies for
// good events preceding the poison) if they have not yet reached the
// client's kernel receive buffer, so the client may fail to observe an OK it
// was, in fact, sent. Deterministic (not a timing-dependent race) BECAUSE
// SetLinger(0) forces the OS to send RST unconditionally on close regardless
// of buffer state — no reliance on beating a drain-vs-close race, which is
// what made an EARLIER, undrained version of hangupBatchRelay itself flaky
// (see that function's doc comment: Flushed swinging 0..20 for identical
// input).
func rstMidBatchRelay(t *testing.T) (url string, conns func() int) {
	t.Helper()
	var mu sync.Mutex
	count := 0
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		mu.Lock()
		count++
		mu.Unlock()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if err := json.Unmarshal(data, &frame); err != nil || len(frame) < 2 {
				continue
			}
			var typ string
			_ = json.Unmarshal(frame[0], &typ)
			if typ != "EVENT" {
				continue
			}
			var ev struct {
				ID      string `json:"id"`
				Content string `json:"content"`
			}
			_ = json.Unmarshal(frame[1], &ev)
			if strings.Contains(ev.Content, "hangup") {
				// Force a hard RST on close: no drain, no clean FIN, even
				// though OK replies for earlier good events may still be
				// sitting unread in the client's kernel receive buffer.
				if tcp, ok := conn.UnderlyingConn().(*net.TCPConn); ok {
					_ = tcp.SetLinger(0)
				}
				return // deferred conn.Close() now delivers a hard RST
			}
			resp, _ := json.Marshal([]any{"OK", ev.ID, true, ""})
			if werr := conn.WriteMessage(websocket.TextMessage, resp); werr != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), func() int {
		mu.Lock()
		defer mu.Unlock()
		return count
	}
}

// TestFlushNostrPending_RSTMidBatch_MakesForwardProgress covers the RST path
// hangupBatchRelay's own doc comment names as uncovered (item 3 of a veracity
// adversary's ready-046 rework-3 finding): an abrupt RST — not the clean FIN
// every OTHER fixture in this file uses — can erase already-written OK
// replies for good events preceding the poison, so the client may see FEWER
// resolved events after the FIRST sub-batch connection than the relay
// actually accepted. That does not, by itself, lose anything: resolveBatch's
// recursion treats any event left unresolved (Err != nil, whatever the cause)
// as needing another connection, and keeps bisecting — WITHIN this ONE
// FlushNostrPending call — until every event that CAN succeed does, so the
// final result is identical to the clean-FIN case
// (TestFlushNostrPending_HangupPartwayThroughBatch_MakesForwardProgress),
// just at the cost of however many extra connections the RST forced. This
// test asserts exactly that final convergence, proving an RST cannot turn
// into permanent data loss even though it CAN erase in-flight acks.
func TestFlushNostrPending_RSTMidBatch_MakesForwardProgress(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	pending, rejected := readyPaths(dir)

	poison := signedEvent(t, k, "hangup-me")
	if err := appendPendingEvent(pending, poison); err != nil {
		t.Fatalf("seed poison: %v", err)
	}
	var good []*nostr.Event
	for i := 0; i < 20; i++ {
		ev := signedEvent(t, k, fmt.Sprintf("rst-ok-%d", i))
		good = append(good, ev)
		if err := appendPendingEvent(pending, ev); err != nil {
			t.Fatalf("seed good %d: %v", i, err)
		}
	}

	url, _ := rstMidBatchRelay(t)
	res, err := FlushNostrPending(context.Background(), pending, []string{url}, false)
	if err != nil {
		t.Fatalf("FlushNostrPending: %v", err)
	}
	if res.Flushed != len(good) {
		t.Fatalf("Flushed=%d, want %d — an RST erasing already-written OKs must still converge to full forward progress WITHIN one flush call (the bisection recursion keeps retrying whatever is left unresolved, however many extra connections that costs)", res.Flushed, len(good))
	}
	if res.Remaining != 1 {
		t.Fatalf("Remaining=%d, want 1 (only the poison, which RSTs even in isolation)", res.Remaining)
	}
	for _, ev := range good {
		if fileHasID(t, pending, ev.ID) {
			t.Errorf("good event %s still buffered after an RST mid-batch — must fully drain within one flush call", ev.ID)
		}
		if fileHasID(t, rejected, ev.ID) {
			t.Errorf("good event %s dead-lettered under RST — a transport reset is transient, never permanent", ev.ID)
		}
	}
	if !fileHasID(t, pending, poison.ID) {
		t.Fatalf("poison vanished from pending.jsonl — it must stay buffered (transient), never lost")
	}
	if fileHasID(t, rejected, poison.ID) {
		t.Errorf("poison was dead-lettered — an RST is a transport failure, never permanent")
	}
}
