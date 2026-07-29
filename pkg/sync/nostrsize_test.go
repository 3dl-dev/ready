package sync

import (
	"context"
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

// TestGuardEventSizes_RealBoundary proves the guard trips exactly where
// strfry's own 64KiB (65536-byte) maxEventSize does, not at some mocked
// stand-in limit (ready-c3e requires exercising the real size boundary). It
// measures the ACTUAL wire size of a REAL signed nostr.Event built by
// BuildCardEvent — the same builder every write path uses — computes the
// Context length that lands the marshaled event at EXACTLY maxEventWireSize
// bytes, and proves that length passes while one byte more fails. The test
// never overrides maxEventWireSize or substitutes a smaller ceiling.
func TestGuardEventSizes_RealBoundary(t *testing.T) {
	k := testKey(t)
	spec := CardSpec{ItemID: "ready-c3e-test", Status: "active"}

	baseline, err := BuildCardEvent(k, spec, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent baseline: %v", err)
	}
	baseSize, err := marshaledEventSize(baseline)
	if err != nil {
		t.Fatalf("marshaledEventSize baseline: %v", err)
	}
	if baseSize >= maxEventWireSize {
		t.Fatalf("baseline event is already %d bytes (>= %d) — test assumption broken", baseSize, maxEventWireSize)
	}

	// Filler content is plain ASCII 'x': encoding/json escapes nothing in it, so
	// growing Context by N bytes grows the marshaled event by EXACTLY N bytes.
	// That is what makes the boundary construction below exact, not approximate.
	atLimitLen := maxEventWireSize - baseSize
	atLimitSpec := spec
	atLimitSpec.Context = strings.Repeat("x", atLimitLen)
	atLimit, err := BuildCardEvent(k, atLimitSpec, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent at-limit: %v", err)
	}
	n, err := marshaledEventSize(atLimit)
	if err != nil {
		t.Fatalf("marshaledEventSize at-limit: %v", err)
	}
	if n != maxEventWireSize {
		t.Fatalf("test construction error: at-limit event is %d bytes, want exactly %d", n, maxEventWireSize)
	}
	if err := guardEventSizes([]*nostr.Event{atLimit}); err != nil {
		t.Errorf("guardEventSizes rejected an event at EXACTLY the limit (%d bytes): %v — a relay's own \"exceeds ... max of 65536\" message means 65536 itself is accepted", n, err)
	}

	overSpec := spec
	overSpec.Context = strings.Repeat("x", atLimitLen+1)
	over, err := BuildCardEvent(k, overSpec, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent over-limit: %v", err)
	}
	n2, err := marshaledEventSize(over)
	if err != nil {
		t.Fatalf("marshaledEventSize over-limit: %v", err)
	}
	if n2 != maxEventWireSize+1 {
		t.Fatalf("test construction error: over-limit event is %d bytes, want exactly %d", n2, maxEventWireSize+1)
	}
	err = guardEventSizes([]*nostr.Event{over})
	if err == nil {
		t.Fatalf("guardEventSizes accepted an event ONE BYTE over the limit (%d bytes)", n2)
	}
	if !strings.Contains(err.Error(), "ready-c3e-test") {
		t.Errorf("error does not name the offending item: %v", err)
	}
	if !strings.Contains(err.Error(), "65537") {
		t.Errorf("error does not name the measured size: %v", err)
	}
}

// TestPublisher_RefusesOversizedCard_BeforeLogAppendAndBeforeRelayDial is
// ready-c3e's done-condition test for the write path: an oversized NEW card
// must be refused with an actionable error BEFORE it becomes durable in the
// local authoritative log AND before any relay is even dialed — the prior
// behavior (no client-side guard at all) let such a card get signed, logged,
// and relay-dialed, only to be silently dead-lettered after the round trip.
func TestPublisher_RefusesOversizedCard_BeforeLogAppendAndBeforeRelayDial(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".ready", NostrLogFile)

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
		Timeout:     3 * time.Second,
	}

	card := CardSpec{ItemID: "ready-c3e-big", Status: "active", Context: strings.Repeat("x", maxEventWireSize)}
	if _, err := pub.PublishItem(context.Background(), nil, card, 1000); err == nil {
		t.Fatalf("PublishItem accepted an oversized card without error")
	} else if !strings.Contains(err.Error(), "ready-c3e-big") {
		t.Errorf("error does not name the offending item: %v", err)
	}

	if got := atomic.LoadInt32(&dialed); got != 0 {
		t.Errorf("relay was dialed %d time(s) before the size guard ran — it must refuse before any network I/O", got)
	}
	events, rerr := pub.Log.ReadAll()
	if rerr != nil {
		t.Fatalf("ReadAll: %v", rerr)
	}
	if len(events) != 0 {
		t.Errorf("oversized publish appended %d event(s) to the local log — guard must refuse before Log.Append too", len(events))
	}
}
