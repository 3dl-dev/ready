package sync

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/gorilla/websocket"
)

// relayBoundaryExceeds reproduces nostr-relay/internal/cosmosstore/
// limits.go:88's RejectOversizeEvent expression verbatim and independently of
// anything this package defines (a bare json.Marshal, a literal `64*1024`, a
// strict `>`), so it is a real EXTERNAL anchor for "too big for the relay",
// never a second call into the code under test:
//
//	b, _ := json.Marshal(evt)
//	if len(b) > MaxEventSizeBytes { ... }   // MaxEventSizeBytes = 64*1024
func relayBoundaryExceeds(t *testing.T, e *nostr.Event) (n int, exceeds bool) {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("json.Marshal (relay-expression anchor): %v", err)
	}
	return len(b), len(b) > 64*1024
}

// atRelayBoundary returns two CardSpecs for itemID, built at createdAt=1000
// with the SAME testKey a caller will later use to publish them: one whose
// signed BuildCardEvent output lands EXACTLY at the relay's own boundary
// (64*1024 bytes per relayBoundaryExceeds) and one exactly one byte over.
// Returning specs (not built events) is deliberate — the boundary must be
// pinned on what a PRODUCTION CALLER (Publisher.PublishCardEdit, the actual
// path `rd progress` drives) does when IT builds and signs the event from
// this spec, not on an event this test built and handed to the code under
// test. Filler content is plain ASCII 'x' — encoding/json escapes nothing in
// it, so growing Context by N bytes grows the marshaled event by EXACTLY N
// bytes, which is what makes this construction exact rather than
// approximate.
func atRelayBoundary(t *testing.T, k *nostr.Key, itemID string) (atLimit, over CardSpec) {
	t.Helper()
	spec := CardSpec{ItemID: itemID, Status: "active"}
	baseline, err := BuildCardEvent(k, spec, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent baseline: %v", err)
	}
	baseSize, exceeds := relayBoundaryExceeds(t, baseline)
	if exceeds {
		t.Fatalf("baseline event already exceeds the relay's own boundary (%d bytes) — test assumption broken", baseSize)
	}
	atLimitLen := 64*1024 - baseSize

	atLimit = spec
	atLimit.Context = strings.Repeat("x", atLimitLen)
	atLimitEvent, err := BuildCardEvent(k, atLimit, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent at-limit: %v", err)
	}
	n, exceeds := relayBoundaryExceeds(t, atLimitEvent)
	if n != 64*1024 || exceeds {
		t.Fatalf("test construction error: at-limit event is %d bytes (exceeds=%v) per the relay's own expression, want exactly %d bytes, exceeds=false", n, exceeds, 64*1024)
	}

	over = spec
	over.Context = strings.Repeat("x", atLimitLen+1)
	overEvent, err := BuildCardEvent(k, over, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent over-limit: %v", err)
	}
	n2, exceeds2 := relayBoundaryExceeds(t, overEvent)
	if n2 != 64*1024+1 || !exceeds2 {
		t.Fatalf("test construction error: over-limit event is %d bytes (exceeds=%v) per the relay's own expression, want exactly %d bytes, exceeds=true", n2, exceeds2, 64*1024+1)
	}
	return atLimit, over
}

// TestPublisher_CardAtRelayBoundary is ready-c3e's REWORKED done-condition
// test for the write path (the prior version, TestPublisher_
// RefusesOversizedCard_BeforeLogAppendAndBeforeRelayDial, asserted the
// OPPOSITE of what is now required and was replaced, not kept alongside this
// one; the version immediately before THIS one,
// TestPublisher_OversizedCard_AppendsLocallyButSkipsRelayDial, sized its
// filler with `strings.Repeat("x", maxEventWireSize)` — this package's OWN
// constant, self-referentially far over the edge rather than pinned to any
// boundary at all, so it could not have caught a 2x-loosening mutant in
// splitOversized's actual size comparison).
//
// THE BUG THE PRIOR REWORK FIXED: refusing BEFORE Log.Append froze the three
// real stranded items this defect produced (3dl-7e0, 3dl-ece, galtrader-bbd)
// — every status transition rebuilds and re-checks the FULL current card
// (PublishStatusChange, PublishCardEdit), so `rd claim`, a close verb, and
// `rd progress` on those items ALL refused, and refused before anything was
// recorded locally either. The local signed log is the source of truth and a
// relay is a replaceable cache; refusing the LOCAL append inverted that (see
// nostrsize.go's doc for the full account).
//
// WHAT THIS REWORK (2) FIXES: guardEventSizes — the function the boundary was
// previously pinned against — is called by NOTHING in production; only its
// own now-deleted test called it. Meanwhile THIS test's filler was the
// self-referential `maxEventWireSize` constant, so the boundary itself was
// never actually exercised on the path production takes. Both problems are
// fixed together: guardEventSizes is deleted (dead code — see nostrsize.go),
// and this test pins the EXACT 65536/65537-byte boundary (atRelayBoundary,
// anchored on the relay's own len(json.Marshal(evt))>64*1024 expression,
// never on this package's own maxEventWireSize) on the REAL production entry
// point, Publisher.PublishCardEdit (`rd progress`'s path) ->
// publishEvents -> splitOversized -> oversizedEvent. A mutant that loosens
// splitOversized's own comparison (not just oversizedEvent's) would now be
// caught, because this test drives splitOversized itself, not a standalone
// call into oversizedEvent/guardEventSizes.
func TestPublisher_CardAtRelayBoundary(t *testing.T) {
	k := testKey(t)
	atLimit, over := atRelayBoundary(t, k, "ready-c3e-big")

	newPublisher := func(t *testing.T, relay string) (*Publisher, string, string) {
		t.Helper()
		dir := t.TempDir()
		pending, rejected := readyPaths(dir)
		return &Publisher{
			Key:         k,
			Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
			WriteRelays: []string{relay},
			PendingPath: pending,
			Timeout:     3 * time.Second,
		}, pending, rejected
	}

	// PublishCardEdit is the simplest publish path (one event, no board/issue/
	// status events) — exactly what `rd progress` drives, one of the three
	// commands this bug froze on the real stranded items.

	t.Run("exactly at the relay's boundary: dialed and delivered, no dead-letter", func(t *testing.T) {
		var dialed int32
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&dialed, 1)
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
			var ev struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(frame[1], &ev)
			resp, _ := json.Marshal([]any{"OK", ev.ID, true, ""})
			_ = conn.WriteMessage(websocket.TextMessage, resp)
		}))
		t.Cleanup(srv.Close)
		relay := "ws" + strings.TrimPrefix(srv.URL, "http")

		pub, _, rejected := newPublisher(t, relay)
		res, err := pub.PublishCardEdit(context.Background(), atLimit, 1000)
		if err != nil {
			t.Fatalf("PublishCardEdit returned an error for an at-boundary card: %v", err)
		}
		if got := atomic.LoadInt32(&dialed); got != 1 {
			t.Errorf("relay was dialed %d time(s) for a card EXACTLY at the relay's own boundary (not over it) — the guard must let it through to the relay dial, want exactly 1 dial", got)
		}
		if res.Rejected {
			t.Errorf("PublishResult.Rejected is true for a card the relay's own expression accepts (exactly 64*1024 bytes) — the guard is refusing at the wrong boundary")
		}
		events, rerr := pub.Log.ReadAll()
		if rerr != nil {
			t.Fatalf("ReadAll: %v", rerr)
		}
		if len(events) != 1 {
			t.Fatalf("got %d event(s) in the local log, want 1", len(events))
		}
		if fileHasID(t, rejected, events[0].ID) {
			t.Errorf("an at-boundary (not over) card's event id was recorded to the dead-letter file %s — it should never have been dead-lettered", NostrRejectedFile)
		}
	})

	t.Run("one byte over the relay's boundary: appends locally, dead-letters, skips the dial", func(t *testing.T) {
		var dialed int32
		up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			atomic.AddInt32(&dialed, 1)
			conn, err := up.Upgrade(w, r, nil)
			if err == nil {
				conn.Close()
			}
		}))
		t.Cleanup(srv.Close)
		relay := "ws" + strings.TrimPrefix(srv.URL, "http")

		pub, _, rejected := newPublisher(t, relay)
		res, err := pub.PublishCardEdit(context.Background(), over, 1000)
		if err != nil {
			t.Fatalf("PublishCardEdit returned an error for an oversized card — the local append/durability guarantee must never be blocked by the relay-size guard (ready-c3e REWORK): %v", err)
		}
		if got := atomic.LoadInt32(&dialed); got != 0 {
			t.Errorf("relay was dialed %d time(s) for an event ONE BYTE over the relay's own boundary — it must be dead-lettered directly with no dial", got)
		}
		events, rerr := pub.Log.ReadAll()
		if rerr != nil {
			t.Fatalf("ReadAll: %v", rerr)
		}
		if len(events) != 1 {
			t.Fatalf("oversized card was not appended to the local authoritative log (durability guarantee) — got %d event(s), want 1", len(events))
		}
		if !res.Rejected {
			t.Errorf("PublishResult.Rejected should be true for a card no relay in the fleet could ever store")
		}
		if !fileHasID(t, rejected, events[0].ID) {
			t.Errorf("oversized card's event id was not recorded to the dead-letter file %s", NostrRejectedFile)
		}
	})
}
