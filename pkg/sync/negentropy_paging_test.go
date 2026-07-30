// Paging proof for the negentropy sync walk (ready-bec).
//
// THE DEFECT THIS PINS. A relay answers ONE query with at most a fixed number of
// records and says nothing about having stopped there. rd's sync ran exactly one
// negentropy exchange, so on a board bigger than that cap it downloaded the cap's
// worth, reported success, and NEVER converged: the next sync saw need=0 because
// the capped window it could see was by then fully local. Measured against
// wss://relay.3dl.network on 2026-07-30 with rd's own board filter and an empty
// log: `need=500 downloaded=481`, `rd list --json --all` projected 201 items, and
// a re-sync projected 201 again — against 540 kind-30302 events the relay served
// to an independently paged `#a` walk.
//
// WHAT IS MOCKED AND WHAT IS NOT. The relay is a fake, because the defect IS a
// relay behaviour (the cap) and no live relay lets a test choose its cap. It is
// not a stub: it speaks the REAL NIP-77 exchange via nostr.Negentropy.ServerReply
// and the REAL NIP-01 REQ/EVENT/EOSE download over a real websocket, holding
// REAL signed events. The code under test — the `until` walk in NegentropySync —
// is untouched by the fake and is what these tests exercise.
//
// The walk is what makes these tests pass: cappedRelay serves at most `cap`
// records per query, so deleting the paging (one exchange, no `until` cursor)
// turns TestNegentropySync_PagesPastTheRelayCap red at 500 of 1200 events.
package sync

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// cappedRelay is an in-process nostr relay that reproduces the ONE behaviour this
// item is about: it will reconcile or serve at most `cap` records for any single
// query, choosing the NEWEST matching ones (created_at descending) exactly as a
// limit-clamping relay does, and it honours the `until` bound that lets a client
// page past that cap.
type cappedRelay struct {
	srv *httptest.Server
	cap int

	mu       sync.Mutex
	events   []*nostr.Event // newest first
	byID     map[string]*nostr.Event
	windows  []int64 // the `until` of every NEG-OPEN seen; 0 = unbounded
	rejected int     // queries refused for asking a limit above the cap
	accepted []*nostr.Event
}

func newCappedRelay(t *testing.T, events []*nostr.Event, capN int) *cappedRelay {
	t.Helper()
	cr := &cappedRelay{cap: capN, byID: map[string]*nostr.Event{}}
	cr.events = append(cr.events, events...)
	sort.SliceStable(cr.events, func(i, j int) bool { return cr.events[i].CreatedAt > cr.events[j].CreatedAt })
	for _, e := range cr.events {
		cr.byID[e.ID] = e
	}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	cr.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		cr.serve(conn)
	}))
	t.Cleanup(cr.srv.Close)
	return cr
}

func (cr *cappedRelay) url() string { return "ws" + strings.TrimPrefix(cr.srv.URL, "http") }

// window returns the records this relay will reconcile for one query: the newest
// `cap` events at or below `until`, matching the filter's kinds and #a scope.
func (cr *cappedRelay) window(f map[string]any) []*nostr.Event {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	var until int64 = 1<<62 - 1
	if raw, ok := f["until"]; ok {
		if ts, ok := timestampField(raw); ok {
			until = ts
		}
	}
	var out []*nostr.Event
	for _, e := range cr.events { // already newest-first
		if e.CreatedAt > until {
			continue
		}
		if !matchesFilter(e, map[string]any{"kinds": f["kinds"], "#a": f["#a"]}) {
			continue
		}
		out = append(out, e)
		if len(out) == cr.cap {
			break
		}
	}
	return out
}

func (cr *cappedRelay) serve(conn *websocket.Conn) {
	var neg *nostr.Negentropy
	var negSub string
	for {
		_, data, err := conn.ReadMessage()
		if err != nil {
			return
		}
		var frame []json.RawMessage
		if err := json.Unmarshal(data, &frame); err != nil || len(frame) == 0 {
			continue
		}
		var typ string
		_ = json.Unmarshal(frame[0], &typ)
		switch typ {
		case "NEG-OPEN":
			if len(frame) < 4 {
				continue
			}
			_ = json.Unmarshal(frame[1], &negSub)
			var f map[string]any
			_ = json.Unmarshal(frame[2], &f)
			// A relay refuses a limit above its own cap rather than silently
			// answering short — the property that makes asking explicitly safe.
			if l, ok := timestampField(f["limit"]); ok && int(l) > cr.cap {
				cr.mu.Lock()
				cr.rejected++
				cr.mu.Unlock()
				resp, _ := json.Marshal([]any{"NEG-ERR", negSub, fmt.Sprintf("limit %d exceeds max of %d", l, cr.cap)})
				_ = conn.WriteMessage(websocket.TextMessage, resp)
				continue
			}
			var until int64
			if ts, ok := timestampField(f["until"]); ok {
				until = ts
			}
			cr.mu.Lock()
			cr.windows = append(cr.windows, until)
			cr.mu.Unlock()

			var items []nostr.NegItem
			for _, e := range cr.window(f) {
				it, err := nostr.NegItemFromEvent(e)
				if err != nil {
					return
				}
				items = append(items, it)
			}
			neg, err = nostr.NewNegentropy(items)
			if err != nil {
				return
			}
			cr.negReply(conn, neg, negSub, frame[3])
		case "NEG-MSG":
			if neg == nil || len(frame) < 3 {
				continue
			}
			cr.negReply(conn, neg, negSub, frame[2])
		case "REQ":
			if len(frame) < 3 {
				continue
			}
			var sub string
			_ = json.Unmarshal(frame[1], &sub)
			var f map[string]any
			_ = json.Unmarshal(frame[2], &f)
			cr.mu.Lock()
			var served []*nostr.Event
			if raw, ok := f["ids"]; ok {
				if ids, ok := raw.([]any); ok {
					for _, r := range ids {
						if id, ok := r.(string); ok {
							if e := cr.byID[id]; e != nil {
								served = append(served, e)
							}
						}
					}
				}
			}
			cr.mu.Unlock()
			for _, e := range served {
				payload, _ := json.Marshal([]any{"EVENT", sub, e})
				if err := conn.WriteMessage(websocket.TextMessage, payload); err != nil {
					return
				}
			}
			eose, _ := json.Marshal([]any{"EOSE", sub})
			_ = conn.WriteMessage(websocket.TextMessage, eose)
		case "EVENT":
			if len(frame) < 2 {
				continue
			}
			var e nostr.Event
			_ = json.Unmarshal(frame[1], &e)
			cr.mu.Lock()
			ev := e
			cr.accepted = append(cr.accepted, &ev)
			cr.mu.Unlock()
			ok, _ := json.Marshal([]any{"OK", e.ID, true, ""})
			_ = conn.WriteMessage(websocket.TextMessage, ok)
		}
	}
}

func (cr *cappedRelay) negReply(conn *websocket.Conn, neg *nostr.Negentropy, sub string, hexFrame json.RawMessage) {
	var hexMsg string
	_ = json.Unmarshal(hexFrame, &hexMsg)
	msg, err := hex.DecodeString(hexMsg)
	if err != nil {
		return
	}
	reply, err := neg.ServerReply(msg)
	if err != nil {
		return
	}
	resp, _ := json.Marshal([]any{"NEG-MSG", sub, hex.EncodeToString(reply)})
	_ = conn.WriteMessage(websocket.TextMessage, resp)
}

func (cr *cappedRelay) windowCount() int {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return len(cr.windows)
}

func (cr *cappedRelay) uploadedIDs() map[string]int {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	out := map[string]int{}
	for _, e := range cr.accepted {
		out[e.ID]++
	}
	return out
}

// pagingBoard builds n distinct signed card events on one board coordinate, each
// one second apart so the `until` cursor has something to walk.
func pagingBoard(t *testing.T, k *nostr.Key, boardCoord string, n int, base int64) []*nostr.Event {
	t.Helper()
	out := make([]*nostr.Event, 0, n)
	for i := 0; i < n; i++ {
		e := &nostr.Event{
			Kind:      KindCard,
			CreatedAt: base + int64(i),
			Tags: [][]string{
				{"d", fmt.Sprintf("ready-pg%04d", i)},
				{"a", boardCoord},
				{"title", fmt.Sprintf("paged card %d", i)},
				{"s", "active"},
			},
			Content: fmt.Sprintf("card %d", i),
		}
		if err := e.Sign(k); err != nil {
			t.Fatalf("sign card %d: %v", i, err)
		}
		out = append(out, e)
	}
	return out
}

// TestNegentropySync_PagesPastTheRelayCap is the item's proof: a board holding
// more events than the relay will reconcile in one query syncs COMPLETELY, and a
// second sync is a no-op instead of stalling short forever.
//
// RED WITHOUT PAGING: with a single unbounded exchange the first sync downloads
// 500 of 1200 and the second downloads 0 — the exact measured production
// symptom.
func TestNegentropySync_PagesPastTheRelayCap(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	boardCoord := "30301:" + k.PubKeyHex() + ":pagingboard"
	const total = 1200
	events := pagingBoard(t, k, boardCoord, total, time.Now().Unix()-int64(total)-10)
	relay := newCappedRelay(t, events, SyncPageLimit)

	log := NewNostrLog(t.TempDir() + "/" + NostrLogFile)
	filter := BoardSyncFilter(boardCoord, nil)
	trusted := map[string]bool{k.PubKeyHex(): true}

	res, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Downloaded != total {
		t.Fatalf("a fresh clone must download the WHOLE board, got downloaded=%d of %d (need=%d, pages=%d)",
			res.Downloaded, total, res.Need, res.Pages)
	}
	if res.Pages < 3 {
		t.Fatalf("%d events at a %d-record cap needs at least 3 windows, walk used %d", total, SyncPageLimit, res.Pages)
	}
	if relay.windowCount() != res.Pages {
		t.Fatalf("reported Pages=%d but the relay saw %d queries", res.Pages, relay.windowCount())
	}

	// The local log must now project every card — the done condition's substance.
	stored, err := log.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != total {
		t.Fatalf("local log holds %d events, want %d", len(stored), total)
	}

	// Convergence: syncing again moves nothing. Pre-fix this was also true — and
	// that was the bug, because it was true at 500 of 1200.
	res2, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res2.Downloaded != 0 || res2.Uploaded != 0 || res2.EventBytesDownloaded != 0 || res2.EventBytesUploaded != 0 {
		t.Fatalf("converged re-sync must move zero event bytes, got downloaded=%d uploaded=%d down=%dB up=%dB",
			res2.Downloaded, res2.Uploaded, res2.EventBytesDownloaded, res2.EventBytesUploaded)
	}
	if res2.Need != 0 {
		t.Fatalf("converged re-sync still needs %d events", res2.Need)
	}
}

// TestNegentropySync_CappedWindowDoesNotReUploadTheBacklog pins the other half of
// the walk: a window the relay CAPPED only speaks for the slice of time it
// actually reconciled. A local event older than that slice is not "missing from
// the relay", it is merely below the cap — and treating it as missing would make
// a fully-converged machine re-publish its entire backlog on every single sync.
func TestNegentropySync_CappedWindowDoesNotReUploadTheBacklog(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	boardCoord := "30301:" + k.PubKeyHex() + ":backlogboard"
	const total = 900
	events := pagingBoard(t, k, boardCoord, total, time.Now().Unix()-int64(total)-10)
	relay := newCappedRelay(t, events, SyncPageLimit)

	// The local log already holds the WHOLE board: this machine is converged, it
	// just has more events than the relay will reconcile in one query.
	log := NewNostrLog(t.TempDir() + "/" + NostrLogFile)
	if _, err := log.AppendUnique(events); err != nil {
		t.Fatal(err)
	}
	filter := BoardSyncFilter(boardCoord, nil)
	trusted := map[string]bool{k.PubKeyHex(): true}

	res, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Uploaded != 0 || res.EventBytesUploaded != 0 {
		t.Fatalf("a converged machine must upload NOTHING, got uploaded=%d (%dB) — the relay already holds all %d events",
			res.Uploaded, res.EventBytesUploaded, total)
	}
	if got := len(relay.uploadedIDs()); got != 0 {
		t.Fatalf("relay received %d re-published events from a converged machine", got)
	}
	if res.Downloaded != 0 {
		t.Fatalf("converged machine downloaded %d events", res.Downloaded)
	}
}

// TestNegentropySync_UploadsWhatTheRelayGenuinelyLacks is the guard against
// fixing the backlog case by never uploading anything: an event the relay really
// does not have, sitting BELOW the cap in the oldest part of the board, must
// still reach it.
func TestNegentropySync_UploadsWhatTheRelayGenuinelyLacks(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	boardCoord := "30301:" + k.PubKeyHex() + ":uploadboard"
	const total = 700
	events := pagingBoard(t, k, boardCoord, total, time.Now().Unix()-int64(total)-10)
	// The relay holds everything except the OLDEST event — the one that only a
	// second window can ever reach.
	relay := newCappedRelay(t, events[1:], SyncPageLimit)

	log := NewNostrLog(t.TempDir() + "/" + NostrLogFile)
	if _, err := log.AppendUnique(events); err != nil {
		t.Fatal(err)
	}
	filter := BoardSyncFilter(boardCoord, nil)
	trusted := map[string]bool{k.PubKeyHex(): true}

	res, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Uploaded != 1 {
		t.Fatalf("the one event the relay lacks must be uploaded, got uploaded=%d have=%d pages=%d",
			res.Uploaded, res.Have, res.Pages)
	}
	got := relay.uploadedIDs()
	if got[events[0].ID] != 1 {
		t.Fatalf("relay did not receive exactly the missing oldest event: %v", got)
	}
}

// TestSyncWindowFilter_AsksForAnExplicitLimit pins the two filter properties the
// walk depends on: every query names a limit (so a relay whose cap is BELOW it
// refuses loudly instead of answering short), and the cursor is stamped only
// once the walk is bounded.
func TestSyncWindowFilter_AsksForAnExplicitLimit(t *testing.T) {
	base := map[string]any{"kinds": []int{KindCard}, "#a": []string{"coord"}}

	first := syncWindowFilter(base, 0, false)
	if first["limit"] != SyncPageLimit {
		t.Fatalf("first window must ask for limit=%d, got %v", SyncPageLimit, first["limit"])
	}
	if _, ok := first["until"]; ok {
		t.Fatalf("the first window is unbounded, got until=%v", first["until"])
	}

	next := syncWindowFilter(base, 1783534661, true)
	if next["until"] != int64(1783534661) {
		t.Fatalf("bounded window must carry the cursor, got %v", next["until"])
	}
	if next["limit"] != SyncPageLimit {
		t.Fatalf("bounded window must ask for limit=%d, got %v", SyncPageLimit, next["limit"])
	}
	if _, ok := base["until"]; ok {
		t.Fatal("syncWindowFilter mutated the caller's filter")
	}
	if _, ok := base["limit"]; ok {
		t.Fatal("syncWindowFilter mutated the caller's filter")
	}
	if SyncPageLimit < 500 {
		t.Fatalf("SyncPageLimit must be >= 500 (the measured relay cap), got %d", SyncPageLimit)
	}
}

// TestMatchesFilter_UntilBoundsTheLocalSide pins the local half of a window: the
// diff is only meaningful if BOTH sides are reduced to the same time range.
func TestMatchesFilter_UntilBoundsTheLocalSide(t *testing.T) {
	e := &nostr.Event{Kind: KindCard, CreatedAt: 1000, Tags: [][]string{{"d", "ready-x"}}}
	if !matchesFilter(e, map[string]any{"until": int64(1000)}) {
		t.Fatal("until is INCLUSIVE: created_at == until must match")
	}
	if matchesFilter(e, map[string]any{"until": int64(999)}) {
		t.Fatal("an event newer than until must not match")
	}
	if !matchesFilter(e, map[string]any{"until": float64(1001)}) {
		t.Fatal("a JSON-decoded (float64) until must be honoured")
	}
	if matchesFilter(e, map[string]any{"until": "nonsense"}) {
		t.Fatal("a malformed until must select nothing, not everything")
	}
	if !matchesFilter(e, map[string]any{"since": int64(1000)}) {
		t.Fatal("since is INCLUSIVE: created_at == since must match")
	}
	if matchesFilter(e, map[string]any{"since": int64(1001)}) {
		t.Fatal("an event older than since must not match")
	}
	// limit is a relay-side cap, never a predicate on an event.
	if !matchesFilter(e, map[string]any{"limit": 1}) {
		t.Fatal("limit must not exclude a locally-matching event")
	}
}
