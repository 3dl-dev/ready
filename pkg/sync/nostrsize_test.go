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

// TestGuardEventSizes_RealBoundary anchors the guard against an EXTERNAL
// ground truth — the relay's own limit expression, not this package's own
// measuring function (ready-c3e REWORK). The prior version of this test built
// its at-limit/over-limit filler using marshaledEventSize and then asserted
// guardEventSizes agreed with marshaledEventSize's own count: a proof that the
// guard is internally self-consistent, which it was never in doubt about,
// not a proof that it matches what any relay actually enforces.
//
// The real relay-side boundary this guard must match is
// nostr-relay/internal/cosmosstore/limits.go:88, RejectOversizeEvent:
//
//	b, _ := json.Marshal(evt)
//	if len(b) > MaxEventSizeBytes { ... }   // MaxEventSizeBytes = 64*1024
//
// This test reproduces that exact expression INLINE — a bare json.Marshal
// plus a literal `64*1024` and a strict `>` comparison, written directly in
// this test body, never by calling back into marshaledEventSize/
// guardEventSizes — and then asserts guardEventSizes agrees with that
// independently-computed verdict at both the at-limit boundary and one byte
// over. If this package's own measuring function and the relay's ever
// diverge, this test — not TestPublisher_OversizedEvent_* — is what would
// catch it, because it never calls the thing it is trying to verify.
func TestGuardEventSizes_RealBoundary(t *testing.T) {
	k := testKey(t)
	spec := CardSpec{ItemID: "ready-c3e-test", Status: "active"}

	// relayBoundaryExceeds reproduces nostr-relay/internal/cosmosstore/
	// limits.go:88's RejectOversizeEvent expression verbatim and independently
	// of anything this package defines, so it is a real external anchor, not a
	// second call into the code under test.
	relayBoundaryExceeds := func(e *nostr.Event) (n int, exceeds bool) {
		b, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("json.Marshal (relay-expression anchor): %v", err)
		}
		return len(b), len(b) > 64*1024
	}

	baseline, err := BuildCardEvent(k, spec, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent baseline: %v", err)
	}
	baseSize, exceeds := relayBoundaryExceeds(baseline)
	if exceeds {
		t.Fatalf("baseline event already exceeds the relay's own boundary (%d bytes) — test assumption broken", baseSize)
	}

	// Filler content is plain ASCII 'x': encoding/json escapes nothing in it, so
	// growing Context by N bytes grows the marshaled event by EXACTLY N bytes.
	// That is what makes the boundary construction below exact, not approximate.
	atLimitLen := 64*1024 - baseSize
	atLimitSpec := spec
	atLimitSpec.Context = strings.Repeat("x", atLimitLen)
	atLimit, err := BuildCardEvent(k, atLimitSpec, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent at-limit: %v", err)
	}
	n, exceeds := relayBoundaryExceeds(atLimit)
	if n != 64*1024 {
		t.Fatalf("test construction error: at-limit event is %d bytes per the relay's own expression, want exactly %d", n, 64*1024)
	}
	if exceeds {
		t.Fatalf("test construction error: the relay's own expression already rejects the at-limit event (%d bytes)", n)
	}
	if err := guardEventSizes([]*nostr.Event{atLimit}); err != nil {
		t.Errorf("guardEventSizes rejected an event the relay's own expression (len(json.Marshal(evt)) > 64*1024) ACCEPTS (%d bytes): %v", n, err)
	}

	overSpec := spec
	overSpec.Context = strings.Repeat("x", atLimitLen+1)
	over, err := BuildCardEvent(k, overSpec, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent over-limit: %v", err)
	}
	n2, exceeds2 := relayBoundaryExceeds(over)
	if n2 != 64*1024+1 {
		t.Fatalf("test construction error: over-limit event is %d bytes per the relay's own expression, want exactly %d", n2, 64*1024+1)
	}
	if !exceeds2 {
		t.Fatalf("test construction error: the relay's own expression still accepts the over-limit event (%d bytes)", n2)
	}
	err = guardEventSizes([]*nostr.Event{over})
	if err == nil {
		t.Fatalf("guardEventSizes ACCEPTED an event the relay's own expression (len(json.Marshal(evt)) > 64*1024) rejects (%d bytes)", n2)
	}
	if !strings.Contains(err.Error(), "ready-c3e-test") {
		t.Errorf("error does not name the offending item: %v", err)
	}
	if !strings.Contains(err.Error(), "65537") {
		t.Errorf("error does not name the measured size: %v", err)
	}
}

// TestPublisher_OversizedCard_AppendsLocallyButSkipsRelayDial is ready-c3e's
// REWORKED done-condition test for the write path (the prior version of this
// test, TestPublisher_RefusesOversizedCard_BeforeLogAppendAndBeforeRelayDial,
// asserted the OPPOSITE of what is now required and has been replaced, not
// kept alongside this one).
//
// THE BUG THIS REWORK FIXES: refusing BEFORE Log.Append (this test's prior
// behavior) froze the three real stranded items this defect produced
// (3dl-7e0, 3dl-ece, galtrader-bbd) — every status transition rebuilds and
// re-checks the FULL current card (PublishStatusChange, PublishCardEdit), so
// `rd claim`, a close verb, and `rd progress` on those items ALL refused, and
// refused before anything was recorded locally either. The local signed log
// is the source of truth and a relay is a replaceable cache; refusing the
// LOCAL append inverted that (see nostrsize.go's doc for the full account).
//
// THE CORRECTED CONTRACT this test proves: an oversized card's local append
// MUST succeed (durability guarantee, unconditional on size) and the mutation
// itself returns NO error; only the relay dial is skipped (the outcome is
// already certain, so no network I/O happens at all) and the event is
// dead-lettered directly, exactly as if a real relay had rejected it.
func TestPublisher_OversizedCard_AppendsLocallyButSkipsRelayDial(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".ready", NostrLogFile)
	pendingPath := filepath.Join(dir, ".ready", "nostr-pending.jsonl")
	rejectedPath := filepath.Join(dir, ".ready", NostrRejectedFile)

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

	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(logPath),
		WriteRelays: []string{relay},
		PendingPath: pendingPath,
		Timeout:     3 * time.Second,
	}

	// PublishCardEdit is the simplest publish path (one event, no board/issue/
	// status events) — exactly what `rd progress` drives, one of the three
	// commands this bug froze on the real stranded items.
	card := CardSpec{ItemID: "ready-c3e-big", Status: "active", Context: strings.Repeat("x", maxEventWireSize)}
	res, err := pub.PublishCardEdit(context.Background(), card, 1000)
	if err != nil {
		t.Fatalf("PublishCardEdit returned an error for an oversized card — the local append/durability guarantee must never be blocked by the relay-size guard (ready-c3e REWORK): %v", err)
	}

	if got := atomic.LoadInt32(&dialed); got != 0 {
		t.Errorf("relay was dialed %d time(s) for an event this client already knows exceeds the relay's size limit — it must be dead-lettered directly with no dial", got)
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
	if !fileHasID(t, rejectedPath, events[0].ID) {
		t.Errorf("oversized card's event id was not recorded to the dead-letter file %s", NostrRejectedFile)
	}
}
