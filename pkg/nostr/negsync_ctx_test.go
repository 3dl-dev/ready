package nostr

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// TestNegentropyReconcile_CtxCancelNoDeadline_UnblocksPromptly is the
// ground-source proof for ready-ce9: NegentropyReconcile's read loop must not
// depend solely on a websocket read error/deadline to stop. If the caller's
// ctx carries NO Deadline (so the ctx.Deadline()-derived SetReadDeadline call
// in NegentropyReconcile is a no-op) and the caller cancels ctx, the read
// loop must unblock promptly instead of hanging until the connection
// otherwise errors.
//
// The fake relay here accepts the NEG-OPEN and then goes silent forever
// (no NEG-MSG, no close, no error) — exactly the scenario where a bare
// conn.ReadMessage() would block indefinitely. We cancel ctx (no deadline)
// shortly after dialing and assert the call returns well before a generous
// upper bound, with a context-cancellation error.
func TestNegentropyReconcile_CtxCancelNoDeadline_UnblocksPromptly(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		// Read the NEG-OPEN and then go silent forever: never reply, never
		// close. Block until the test server itself is torn down.
		_, _, _ = conn.ReadMessage()
		<-r.Context().Done()
	}))
	defer srv.Close()

	relayURL := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithCancel(context.Background()) // NO deadline
	filter := map[string]any{"kinds": []int{1}}

	done := make(chan struct{})
	var recErr error
	go func() {
		defer close(done)
		_, recErr = NegentropyReconcile(ctx, relayURL, filter, nil)
	}()

	// Give the dial + NEG-OPEN write time to land, then cancel with no deadline set.
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case <-done:
		if recErr == nil {
			t.Fatal("expected an error from cancelled ctx, got nil")
		}
		if !errors.Is(recErr, context.Canceled) {
			t.Fatalf("expected context.Canceled, got: %v", recErr)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("NegentropyReconcile did not unblock promptly after ctx cancellation without a deadline")
	}
}
