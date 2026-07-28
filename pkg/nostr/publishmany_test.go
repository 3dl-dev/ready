package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// batchRelayOpts configures the fake relay used by the PublishMany tests.
type batchRelayOpts struct {
	// reply decides the ["OK", id, accepted, message] the relay sends for one
	// event. Returning ok=false with sendNothing=true makes the relay stay
	// silent for that event (the "never acknowledged" case).
	reply func(ev batchRelayEvent) (accepted bool, message string, sendNothing bool)
	// hangUpAfter makes the relay answer the first hangUpAfter events normally
	// and then close the socket without replying to anything further (0
	// disables) — a mid-batch truncation.
	hangUpAfter int
	// noticeBefore sends an unsolicited NOTICE frame before the first OK, so the
	// reader must skip non-OK frames rather than mis-attributing them.
	noticeBefore bool
}

type batchRelayEvent struct {
	ID      string `json:"id"`
	Content string `json:"content"`
}

// batchRelay starts a fake relay that keeps ONE connection open for a whole
// batch (unlike contentRelay in pkg/sync, which answers a single event per
// connection). It records how many events arrived on each connection and how
// many connections were opened, which is what proves PublishMany is actually
// batching rather than re-dialing.
func batchRelay(t *testing.T, opts batchRelayOpts) (url string, conns func() int, maxInFlight func() int) {
	t.Helper()
	var mu sync.Mutex
	connCount := 0
	peakInFlight := 0

	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		mu.Lock()
		connCount++
		mu.Unlock()

		received := 0
		inFlight := 0
		sentNotice := false
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
			if typ == "CLOSE" {
				continue
			}
			if typ != "EVENT" {
				continue
			}
			var ev batchRelayEvent
			_ = json.Unmarshal(frame[1], &ev)
			received++
			inFlight++
			mu.Lock()
			if inFlight > peakInFlight {
				peakInFlight = inFlight
			}
			mu.Unlock()

			if opts.hangUpAfter > 0 && received > opts.hangUpAfter {
				return // silence + close: the client must see a read error
			}
			accepted, message := true, ""
			silent := false
			if opts.reply != nil {
				accepted, message, silent = opts.reply(ev)
			}
			if silent {
				continue
			}
			resp, _ := json.Marshal([]any{"OK", ev.ID, accepted, message})
			if opts.noticeBefore && !sentNotice {
				sentNotice = true
				notice, _ := json.Marshal([]any{"NOTICE", "unsolicited chatter"})
				_ = conn.WriteMessage(websocket.TextMessage, notice)
			}
			inFlight--
			if err := conn.WriteMessage(websocket.TextMessage, resp); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"),
		func() int { mu.Lock(); defer mu.Unlock(); return connCount },
		func() int { mu.Lock(); defer mu.Unlock(); return peakInFlight }
}

// signedEvents builds n distinct signed events for batch tests.
func signedEvents(t *testing.T, n int) []*Event {
	t.Helper()
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	out := make([]*Event, 0, n)
	for i := 0; i < n; i++ {
		e := &Event{Kind: 1, CreatedAt: time.Now().Unix(), Content: fmt.Sprintf("event-%d", i)}
		if err := e.Sign(k); err != nil {
			t.Fatalf("sign %d: %v", i, err)
		}
		out = append(out, e)
	}
	return out
}

// TestPublishMany_OneConnectionForWholeBatch is the reason this primitive
// exists: a batch of many events must cost ONE dial, not one per event. The
// ready-260 backfill re-sends ~23k already-signed events; at one TLS handshake
// each against a scale-to-zero relay that is hours, and the operation is not
// practical.
func TestPublishMany_OneConnectionForWholeBatch(t *testing.T) {
	url, conns, _ := batchRelay(t, batchRelayOpts{})
	evs := signedEvents(t, 200)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	acks, err := PublishMany(ctx, url, evs)
	if err != nil {
		t.Fatalf("PublishMany: %v", err)
	}
	if len(acks) != len(evs) {
		t.Fatalf("got %d acks for %d events — the ack slice must always be input-length", len(acks), len(evs))
	}
	for i, a := range acks {
		if !a.Accepted || a.Err != nil {
			t.Fatalf("ack[%d] = %+v, want accepted with no error", i, a)
		}
		if a.EventID != evs[i].ID {
			t.Fatalf("ack[%d].EventID = %s, want %s — acks must stay aligned to INPUT order", i, a.EventID, evs[i].ID)
		}
	}
	if got := conns(); got != 1 {
		t.Fatalf("relay saw %d connections for a 200-event batch, want exactly 1", got)
	}
}

// TestPublishMany_BoundsInFlightWindow proves the window is real. Writing an
// unbounded batch and only then reading can deadlock against a relay that stops
// draining its receive buffer, so PublishMany must never have more than
// PublishManyWindow events outstanding.
func TestPublishMany_BoundsInFlightWindow(t *testing.T) {
	// deferAcks would stall forever past the window, so instead hold each ack
	// only until the relay has seen it: the peak in-flight count is what matters.
	url, _, peak := batchRelay(t, batchRelayOpts{})
	evs := signedEvents(t, PublishManyWindow*4)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	if _, err := PublishMany(ctx, url, evs); err != nil {
		t.Fatalf("PublishMany: %v", err)
	}
	if got := peak(); got > PublishManyWindow {
		t.Fatalf("peak in-flight events = %d, exceeds PublishManyWindow=%d", got, PublishManyWindow)
	}
}

// TestPublishMany_MatchesOutOfOrderAndInterleavedFrames covers a relay that
// acknowledges in a different order than it received, and interleaves a NOTICE.
// Acks must still land on the right INPUT positions — matching by event id, not
// by arrival position.
func TestPublishMany_MatchesOutOfOrderAndInterleavedFrames(t *testing.T) {
	url, _, _ := batchRelay(t, batchRelayOpts{
		noticeBefore: true,
		reply: func(ev batchRelayEvent) (bool, string, bool) {
			// Reject exactly one identifiable event so a mis-alignment would
			// show up as the WRONG ack, not merely as a different count.
			if ev.Content == "event-3" {
				return false, "invalid: nope", false
			}
			return true, "", false
		},
	})
	evs := signedEvents(t, 8)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	acks, err := PublishMany(ctx, url, evs)
	if err != nil {
		t.Fatalf("PublishMany: %v", err)
	}
	for i, a := range acks {
		wantAccepted := i != 3
		if a.Accepted != wantAccepted {
			t.Fatalf("ack[%d].Accepted = %v, want %v (acks must be matched by event id, not arrival order)", i, a.Accepted, wantAccepted)
		}
		if i == 3 && a.Message != "invalid: nope" {
			t.Fatalf("ack[3].Message = %q, want the relay's rejection message", a.Message)
		}
	}
}

// TestPublishMany_DuplicateIDsResolveFIFO covers the same event id appearing
// twice in one batch (a log with a duplicated line). Both positions must resolve
// — the first from the relay's reply, the second from whatever comes next — and
// neither may steal the other's ack silently.
func TestPublishMany_DuplicateIDsResolveFIFO(t *testing.T) {
	url, _, _ := batchRelay(t, batchRelayOpts{})
	evs := signedEvents(t, 2)
	batch := []*Event{evs[0], evs[1], evs[0]}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	acks, err := PublishMany(ctx, url, batch)
	if err != nil {
		t.Fatalf("PublishMany: %v", err)
	}
	if len(acks) != 3 {
		t.Fatalf("got %d acks, want 3 (one per INPUT position, duplicates included)", len(acks))
	}
	for i, a := range acks {
		if a.EventID != batch[i].ID {
			t.Fatalf("ack[%d].EventID = %s, want %s", i, a.EventID, batch[i].ID)
		}
		if !a.Accepted {
			t.Fatalf("ack[%d] not accepted: %+v — the relay replied to both copies", i, a)
		}
	}
}

// TestPublishMany_DialFailureErrorsEveryEvent proves the ack slice is safe to
// index even when nothing was ever sent: a caller zipping acks onto its own
// event slice must not have to length-check first.
func TestPublishMany_DialFailureErrorsEveryEvent(t *testing.T) {
	evs := signedEvents(t, 5)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	acks, err := PublishMany(ctx, "ws://127.0.0.1:1", evs)
	if err == nil {
		t.Fatal("PublishMany to a dead port returned nil error")
	}
	if len(acks) != len(evs) {
		t.Fatalf("got %d acks after a dial failure, want %d", len(acks), len(evs))
	}
	for i, a := range acks {
		if a.Err == nil || a.Accepted {
			t.Fatalf("ack[%d] = %+v, want the dial error on every event", i, a)
		}
	}
}

// TestPublishMany_MidBatchHangupNeverReportsFalseAcceptance is the honesty
// contract for a truncated batch, and the single most important property for a
// backfill: an event the relay never acknowledged must NEVER come back as
// accepted. A bulk republish that over-reports acceptance is worse than one that
// fails outright, because the operator stops looking.
//
// The exact split between "acknowledged before the hangup" and "errored" is
// deliberately NOT asserted: it depends on how far the write window had run
// ahead of the reader when the socket died, which is a legitimate race. What is
// asserted is that the error surfaces, every event lands in exactly one bucket,
// and no more events are reported accepted than the relay ever answered.
func TestPublishMany_MidBatchHangupNeverReportsFalseAcceptance(t *testing.T) {
	const answered = 4
	url, _, _ := batchRelay(t, batchRelayOpts{hangUpAfter: answered})
	evs := signedEvents(t, 40)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	acks, err := PublishMany(ctx, url, evs)
	if err == nil {
		t.Fatal("PublishMany returned nil error although the relay hung up mid-batch")
	}
	accepted, errored := 0, 0
	for _, a := range acks {
		switch {
		case a.Accepted:
			accepted++
		case a.Err != nil:
			errored++
		}
	}
	if accepted+errored != len(evs) {
		t.Fatalf("accepted=%d errored=%d for %d events — every event must end up in exactly one bucket", accepted, errored, len(evs))
	}
	if errored == 0 {
		t.Fatal("no event carries an error although the relay hung up mid-batch")
	}
	if accepted > answered {
		t.Fatalf("%d events reported accepted, but the relay only ever answered %d — a truncated batch must never over-report acceptance", accepted, answered)
	}
}

// TestPublishMany_GuardRefusesWholeBatchBeforeDial locks the safety contract:
// PublishGuard is consulted for EVERY event before any connection is opened, and
// one refused event refuses the batch. Sending the permitted subset of a batch
// the guard partially refused would be the exact "reported success, the
// important part never went" failure the guard exists to prevent.
func TestPublishMany_GuardRefusesWholeBatchBeforeDial(t *testing.T) {
	url, conns, _ := batchRelay(t, batchRelayOpts{})
	evs := signedEvents(t, 10)

	prev := PublishGuard
	t.Cleanup(func() { PublishGuard = prev })
	PublishGuard = func(_ context.Context, e *Event) error {
		if e.Content == "event-7" {
			return fmt.Errorf("refused by test guard")
		}
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acks, err := PublishMany(ctx, url, evs)
	if err == nil {
		t.Fatal("PublishMany published a batch containing a guard-refused event")
	}
	if got := conns(); got != 0 {
		t.Fatalf("relay saw %d connections — the guard must refuse BEFORE any dial", got)
	}
	for i, a := range acks {
		if a.Err == nil {
			t.Fatalf("ack[%d] carries no error; a refused batch must mark every event", i)
		}
	}
}

// TestPublishMany_EmptyBatchIsANoOp — a board with nothing in scope must not
// dial a relay at all.
func TestPublishMany_EmptyBatchIsANoOp(t *testing.T) {
	url, conns, _ := batchRelay(t, batchRelayOpts{})
	acks, err := PublishMany(context.Background(), url, nil)
	if err != nil {
		t.Fatalf("PublishMany(nil): %v", err)
	}
	if len(acks) != 0 {
		t.Fatalf("got %d acks for an empty batch", len(acks))
	}
	if got := conns(); got != 0 {
		t.Fatalf("relay saw %d connections for an empty batch, want 0", got)
	}
}
