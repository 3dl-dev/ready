package nostr

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// fakeRelay is a minimal in-process NIP-01 relay for exercising the REQ/EVENT/EOSE
// (and CLOSED) frames against a real websocket, no mocks. It serves back, for each
// REQ, one EVENT per requested id that it "holds", then an EOSE. If a REQ's ids
// filter exceeds maxIDs it emulates strfry's oversized-filter refusal by the
// configured mode. It records the ids requested per REQ so a test can assert the
// client chunked correctly.
type fakeRelay struct {
	srv    *httptest.Server
	maxIDs int // per-REQ ids cap; a REQ over this is refused
	// refuse controls what an oversized REQ does:
	//   "closed"  -> send ["CLOSED", sub, msg]
	//   "silent"  -> send nothing (reproduces the real strfry i/o-timeout hang)
	refuse string
	held   map[string]bool // ids this relay "has"

	reqIDsPerCall [][]string // recorded ids per REQ, in order
}

func newFakeRelay(held []string, maxIDs int, refuse string) *fakeRelay {
	fr := &fakeRelay{maxIDs: maxIDs, refuse: refuse, held: map[string]bool{}}
	for _, id := range held {
		fr.held[id] = true
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	fr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
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
			var sub string
			_ = json.Unmarshal(frame[1], &sub)
			switch typ {
			case "CLOSE":
				// client closed the subscription; keep the conn open for more REQs.
			case "REQ":
				var filter struct {
					IDs []string `json:"ids"`
				}
				if len(frame) >= 3 {
					_ = json.Unmarshal(frame[2], &filter)
				}
				fr.reqIDsPerCall = append(fr.reqIDsPerCall, filter.IDs)
				if len(filter.IDs) > fr.maxIDs {
					switch fr.refuse {
					case "silent":
						// send nothing — emulates strfry dropping an oversized REQ
						continue
					default: // "closed"
						_ = conn.WriteJSON([]any{"CLOSED", sub, "error: filter too large"})
						continue
					}
				}
				for _, id := range filter.IDs {
					if !fr.held[id] {
						continue
					}
					ev := Event{ID: id, PubKey: strings.Repeat("a", 64), Kind: 1, Tags: [][]string{}, Content: "x"}
					_ = conn.WriteJSON([]any{"EVENT", sub, ev})
				}
				_ = conn.WriteJSON([]any{"EOSE", sub})
			}
		}
	}))
	return fr
}

func (fr *fakeRelay) url() string { return "ws" + strings.TrimPrefix(fr.srv.URL, "http") }
func (fr *fakeRelay) close()      { fr.srv.Close() }

func makeIDs(n int) []string {
	ids := make([]string, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%064x", i)
	}
	return ids
}

// TestFetchByIDs_ChunksLargeSet proves the ready-8de fix: FetchByIDs downloads a
// large id set by paging it into MaxREQIDs-sized REQs, so a relay that refuses an
// oversized single REQ still returns the full set. It asserts every id came back
// AND that no single REQ the relay saw exceeded MaxREQIDs.
func TestFetchByIDs_ChunksLargeSet(t *testing.T) {
	ids := makeIDs(1750) // 4 chunks at MaxREQIDs=500
	// Relay refuses any REQ over MaxREQIDs (silent — the real strfry failure mode).
	fr := newFakeRelay(ids, MaxREQIDs, "silent")
	defer fr.close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	got, err := FetchByIDs(ctx, fr.url(), ids)
	if err != nil {
		t.Fatalf("FetchByIDs: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("got %d events, want %d", len(got), len(ids))
	}
	gotIDs := map[string]bool{}
	for _, e := range got {
		gotIDs[e.ID] = true
	}
	for _, id := range ids {
		if !gotIDs[id] {
			t.Fatalf("missing id %s from batched download", id)
		}
	}
	// Every REQ the relay handled must be within the per-REQ cap.
	if len(fr.reqIDsPerCall) == 0 {
		t.Fatal("relay saw no REQs")
	}
	for i, req := range fr.reqIDsPerCall {
		if len(req) > MaxREQIDs {
			t.Fatalf("REQ #%d carried %d ids, exceeds MaxREQIDs=%d", i, len(req), MaxREQIDs)
		}
	}
	wantChunks := (len(ids) + MaxREQIDs - 1) / MaxREQIDs
	if len(fr.reqIDsPerCall) != wantChunks {
		t.Fatalf("expected %d chunked REQs, relay saw %d", wantChunks, len(fr.reqIDsPerCall))
	}
}

// TestFetchByIDs_SingleReqTimesOutUnbatched is the control: it proves the failure
// FetchByIDs fixes. A raw single FetchMany with the whole oversized id set against a
// relay that silently drops it blocks until the deadline and returns an i/o error —
// exactly the live "read EVENT: i/o timeout" (ready-8de). This documents WHY the
// chunking exists so a later refactor cannot silently reintroduce the hang.
func TestFetchByIDs_SingleReqTimesOutUnbatched(t *testing.T) {
	ids := makeIDs(MaxREQIDs + 1) // one over the cap -> relay refuses
	fr := newFakeRelay(ids, MaxREQIDs, "silent")
	defer fr.close()

	ctx, cancel := context.WithTimeout(context.Background(), 1500*time.Millisecond)
	defer cancel()
	_, err := FetchMany(ctx, fr.url(), map[string]any{"ids": ids})
	if err == nil {
		t.Fatal("expected the oversized single REQ to fail (deadline), got nil")
	}
	// FetchByIDs, given the SAME set, chunks and succeeds against the same relay.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel2()
	got, err := FetchByIDs(ctx2, fr.url(), ids)
	if err != nil {
		t.Fatalf("FetchByIDs on the same oversized set must succeed by chunking: %v", err)
	}
	if len(got) != len(ids) {
		t.Fatalf("FetchByIDs got %d, want %d", len(got), len(ids))
	}
}

// TestFetchMany_ClosedFrameIsError proves FetchMany surfaces a relay CLOSED as an
// error instead of blocking until the read deadline (part of the ready-8de fix:
// make refusal visible).
func TestFetchMany_ClosedFrameIsError(t *testing.T) {
	ids := makeIDs(MaxREQIDs + 1)
	fr := newFakeRelay(ids, MaxREQIDs, "closed")
	defer fr.close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := FetchMany(ctx, fr.url(), map[string]any{"ids": ids})
	if err == nil {
		t.Fatal("expected CLOSED to produce an error")
	}
	if !strings.Contains(err.Error(), "closed subscription") {
		t.Fatalf("expected a closed-subscription error, got: %v", err)
	}
	if time.Since(start) > 2*time.Second {
		t.Fatalf("CLOSED should fail fast, took %v (blocked on deadline?)", time.Since(start))
	}
}
