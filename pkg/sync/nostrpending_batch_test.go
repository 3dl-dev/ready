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
