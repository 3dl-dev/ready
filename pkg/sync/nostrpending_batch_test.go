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
	res, err := FlushNostrPending(context.Background(), pending, []string{url}, 3*time.Second, false)
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
	res, err := FlushNostrPending(context.Background(), pending, []string{url1, url2}, 3*time.Second, false)
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

	if _, err := FlushNostrPending(context.Background(), pending, []string{permRelay, downRelay}, 3*time.Second, false); err != nil {
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
	res, err := FlushNostrPending(context.Background(), pending, []string{relay}, 3*time.Second, false)
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
