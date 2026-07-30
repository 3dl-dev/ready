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
// relay behaviour (the cap) and a test needs to choose the cap to drive the walk
// deterministically. It is not a stub: it speaks the REAL NIP-77 exchange via
// nostr.Negentropy.ServerReply and the REAL NIP-01 REQ/EVENT/EOSE download over a
// real websocket, holding REAL signed events. The code under test — the `until`
// walk in NegentropySync — is untouched by the fake and is what these tests
// exercise.
//
// AND EVERY BEHAVIOUR IT MODELS WAS MEASURED FIRST, ON A RELAY CONFIGURED TO
// PRODUCE IT. That order matters here more than usual: this file has twice
// carried a relay behaviour nobody had ever observed. The first was "a capped
// relay refuses loudly" (retired). The second was its replacement — "a relay
// silently clamps the NEG-OPEN window below the limit the client asked for" —
// which was assumed because neither reachable relay could be told to cap at 400.
// It can be: strfry's cap is its own `maxFilterLimit`, so a throwaway strfry was
// stood up on loopback at maxFilterLimit=400 with a 600-event board, and the
// answer was neither of the two guesses:
//
//	NEG-OPEN limit=401/500/1000/5000 -> 401/500/600/600  the limit is HONOURED;
//	                                                     maxFilterLimit does not
//	                                                     apply to NIP-77 at all
//	REQ      limit=401/500/5000/none -> 400 every time    CLAMPED, SILENTLY
//
// So the silent sub-limit truncation is REAL but lives on the DOWNLOAD half, not
// the reconcile half: negentropy names 450 ids and the REQ that fetches them
// returns 400, with no error. That is what cappedRelay now models —
// an honoured `limit` on NEG-OPEN, `reqCap` on REQ — and what
// TestNegentropySync_RelayCapBelowOurLimitStillGetsTheWholeBoard drives. The live
// counterpart is TestLiveRelay_ASubLimitCappedRelayClampsTheDownloadSilently in
// negentropy_live_cap_test.go, which re-runs the same experiment against the real
// thing; the whole-board and newest-first properties are measured by
// TestLiveRelay_OneQueryIsBoundedAndTheWalkPagesPastIt and
// TestLiveRelay_FreshCloneOfTheProductionBoardIsWhole.
//
// The third modelled behaviour, `reqMaxLimit`, is the loud refusal — also measured
// rather than imagined, on wss://relay.3dl.network, which answers a REQ carrying
// limit=600 with CLOSED "invalid: requested limit 600 exceeds this relay's max of
// 500". rd must never trip it; see
// TestNegentropySync_NeverAsksAREQLimitTheRelayWillRefuse.
//
// The walk is what makes these tests pass. Deleting the paging (one exchange, no
// `until` cursor) turns TestNegentropySync_PagesPastTheRelayCap red at 500 of 1200
// events, and terminating on window size instead of on the cursor turns
// TestNegentropySync_RelayCapBelowOurLimitStillGetsTheWholeBoard red at 400 of 450
// — the exact numbers the live capped strfry produced.
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

// cappedRelay is an in-process nostr relay reproducing the three relay behaviours
// this item turns on, each of which was MEASURED on a real relay before it was
// modelled here (see the file header for the experiments and their numbers):
//
//  1. NEG-OPEN honours the client's `limit` exactly. It reconciles the newest
//     min(limit, matching) records at or below `until`, and with no limit it
//     reconciles everything. A relay's own maxFilterLimit does NOT bound this
//     path — measured on a strfry capped at 400, which answered limit=500 with
//     500 records and an unbounded query with all 600 it held.
//
//  2. REQ is clamped to `reqCap` records, SILENTLY, newest first — no error, no
//     NOTICE, no CLOSED, just EOSE with fewer events than were asked for. This is
//     rd's download half, so `reqCap` below SyncPageLimit is how negentropy comes
//     to name more ids than the fetch delivers. Measured on the same strfry:
//     every REQ came back with exactly 400 whatever limit was named, including
//     none at all.
//
//  3. `reqMaxLimit`, when set, is a STATED ceiling: a REQ naming a limit above it
//     is refused loudly with CLOSED "invalid: …". Measured on
//     wss://relay.3dl.network, which refuses limit=600 against a max of 500. rd
//     must stay under it rather than rely on it.
//
// It honours `until` on both halves, which is what lets a client page past any of
// the three.
type cappedRelay struct {
	srv *httptest.Server
	// reqCap bounds ONE NIP-01 REQ, silently. It is the relay's maxFilterLimit.
	reqCap int
	// reqMaxLimit, when > 0, is the ceiling above which an explicitly-named REQ
	// limit is refused with CLOSED instead of clamped. 0 = never refuse.
	reqMaxLimit int

	mu        sync.Mutex
	events    []*nostr.Event // newest first
	byID      map[string]*nostr.Event
	windows   []int64 // the `until` of every NEG-OPEN seen; 0 = unbounded
	negErrAt  int     // 1-indexed NEG-OPEN to answer with NEG-ERR; 0 = never
	accepted  []*nostr.Event
	reqLimits []int64 // the explicit `limit` of every REQ seen; -1 = none named
	reqIDs    []int   // how many ids each REQ asked for
}

func newCappedRelay(t *testing.T, events []*nostr.Event, capN int) *cappedRelay {
	t.Helper()
	cr := &cappedRelay{reqCap: capN, byID: map[string]*nostr.Event{}}
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

// window returns the records this relay will reconcile for ONE NEG-OPEN: the
// newest `limit` events at or below `until`, matching the filter's kinds and #a
// scope. The limit is HONOURED, not clamped — reqCap plays no part here, because
// a real strfry's maxFilterLimit was measured to have no effect on the NIP-77
// path (see the file header). With no limit named, everything in the window is
// reconciled.
func (cr *cappedRelay) window(f map[string]any) []*nostr.Event {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	var until int64 = 1<<62 - 1
	if raw, ok := f["until"]; ok {
		if ts, ok := timestampField(raw); ok {
			until = ts
		}
	}
	bound := -1 // no limit named: reconcile the whole window
	if l, ok := timestampField(f["limit"]); ok {
		bound = int(l)
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
		if bound >= 0 && len(out) == bound {
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
			var until int64
			if ts, ok := timestampField(f["until"]); ok {
				until = ts
			}
			cr.mu.Lock()
			cr.windows = append(cr.windows, until)
			nth, errAt := len(cr.windows), cr.negErrAt
			cr.mu.Unlock()
			// Not a cap behaviour — a relay simply failing mid-walk. See
			// TestNegentropySync_AMidWalkRelayErrorIsNotReportedAsSuccess.
			if errAt > 0 && nth == errAt {
				resp, _ := json.Marshal([]any{"NEG-ERR", negSub, "blocked: relay says no"})
				_ = conn.WriteMessage(websocket.TextMessage, resp)
				continue
			}

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

			askedLimit := int64(-1)
			if l, ok := timestampField(f["limit"]); ok {
				askedLimit = l
			}
			cr.mu.Lock()
			var served []*nostr.Event
			nIDs := 0
			if raw, ok := f["ids"]; ok {
				if ids, ok := raw.([]any); ok {
					nIDs = len(ids)
					for _, r := range ids {
						if id, ok := r.(string); ok {
							if e := cr.byID[id]; e != nil {
								served = append(served, e)
							}
						}
					}
				}
			}
			cr.reqLimits = append(cr.reqLimits, askedLimit)
			cr.reqIDs = append(cr.reqIDs, nIDs)
			maxLimit := cr.reqMaxLimit
			capN := cr.reqCap
			cr.mu.Unlock()

			// (3) THE LOUD REFUSAL, measured on wss://relay.3dl.network: a REQ that
			// NAMES a limit above the relay's stated max is closed, not clamped. Only
			// an explicit limit trips it — an absent one is clamped like any other.
			if maxLimit > 0 && askedLimit > int64(maxLimit) {
				closed, _ := json.Marshal([]any{"CLOSED", sub, fmt.Sprintf(
					"invalid: requested limit %d exceeds this relay's max of %d — no silent truncation; narrow with since/until or resubmit with a smaller limit",
					askedLimit, maxLimit)})
				_ = conn.WriteMessage(websocket.TextMessage, closed)
				continue
			}

			// (2) THE SILENT CLAMP, measured on a strfry with maxFilterLimit=400: at
			// most reqCap events come back, NEWEST FIRST, and the client is told
			// nothing — the frames it gets are ordinary EVENTs followed by an
			// ordinary EOSE. Newest-first is load-bearing: it is what makes the
			// events the walk DOES receive the top of the requested range, so the
			// cursor lands above the records that were withheld and a later window
			// still reaches them.
			sort.SliceStable(served, func(i, j int) bool { return served[i].CreatedAt > served[j].CreatedAt })
			if capN > 0 && len(served) > capN {
				served = served[:capN]
			}
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

// reqShapes returns the explicit `limit` (-1 when none was named) and the id count
// of every REQ this relay saw, in order — the two numbers that decide whether a
// real relay clamps, refuses, or answers in full.
func (cr *cappedRelay) reqShapes() (limits []int64, idCounts []int) {
	cr.mu.Lock()
	defer cr.mu.Unlock()
	return append([]int64(nil), cr.reqLimits...), append([]int(nil), cr.reqIDs...)
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

// sameSecondBoard builds n distinct signed cards that ALL carry the same
// created_at — the one shape an `until` cursor cannot step through, because
// `until` is a timestamp and there is nowhere older to move it to within the
// second.
func sameSecondBoard(t *testing.T, k *nostr.Key, boardCoord string, n int, at int64) []*nostr.Event {
	t.Helper()
	out := make([]*nostr.Event, 0, n)
	for i := 0; i < n; i++ {
		e := &nostr.Event{
			Kind:      KindCard,
			CreatedAt: at,
			Tags: [][]string{
				{"d", fmt.Sprintf("ready-ss%04d", i)},
				{"a", boardCoord},
				{"title", fmt.Sprintf("same-second card %d", i)},
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

// TestNegentropySync_SameSecondOverflowIsAnErrorNotASilentDrop pins the one case
// the cursor genuinely cannot solve: MORE than a full window's worth of events
// sharing a single created_at second. `until` cannot move below the second
// without skipping records inside it, so the walk has no honest way forward — and
// the whole point of ready-bec is that a truncation must never be reported as a
// completed sync. It must be an error.
func TestNegentropySync_SameSecondOverflowIsAnErrorNotASilentDrop(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	boardCoord := "30301:" + k.PubKeyHex() + ":jamboard"
	events := sameSecondBoard(t, k, boardCoord, SyncPageLimit+100, time.Now().Unix()-3600)
	relay := newCappedRelay(t, events, SyncPageLimit)

	log := NewNostrLog(t.TempDir() + "/" + NostrLogFile)
	filter := BoardSyncFilter(boardCoord, nil)
	trusted := map[string]bool{k.PubKeyHex(): true}

	res, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err == nil {
		t.Fatalf("%d events on ONE second behind a %d-record window cannot all be reached, and saying nothing is the defect: got downloaded=%d pages=%d",
			len(events), SyncPageLimit, res.Downloaded, res.Pages)
	}
	if !strings.Contains(err.Error(), "shares that timestamp") {
		t.Fatalf("the error must name WHY the walk cannot continue, got: %v", err)
	}
}

// TestNegentropySync_ASmallBoardOnOneSecondIsNotAJam is the discriminating twin.
// A tiny board whose events all share a created_at second is completely ordinary
// — `rd init` writes a board, a card and a status within the same second — and it
// must sync clean. Any jam detector that fires on "the cursor stopped moving"
// alone, or on "this window was as big as the biggest one seen", declares this
// board broken. Only a window FULL at the limit rd itself asked for is evidence
// of overflow.
func TestNegentropySync_ASmallBoardOnOneSecondIsNotAJam(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	boardCoord := "30301:" + k.PubKeyHex() + ":oneSecondBoard"
	events := sameSecondBoard(t, k, boardCoord, 3, time.Now().Unix()-3600)
	relay := newCappedRelay(t, events, SyncPageLimit)

	log := NewNostrLog(t.TempDir() + "/" + NostrLogFile)
	filter := BoardSyncFilter(boardCoord, nil)
	trusted := map[string]bool{k.PubKeyHex(): true}

	res, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err != nil {
		t.Fatalf("a %d-event board that happens to share one second must sync clean: %v", len(events), err)
	}
	if res.Downloaded != len(events) {
		t.Fatalf("downloaded %d of %d", res.Downloaded, len(events))
	}
	res2, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res2.Downloaded != 0 || res2.Uploaded != 0 {
		t.Fatalf("converged re-sync moved events: downloaded=%d uploaded=%d", res2.Downloaded, res2.Uploaded)
	}
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
// walk depends on: every query names a limit, so the per-window cost is a number
// rd chose rather than whatever default the relay volunteers, and the cursor is
// stamped only once the walk is bounded. The limit is NOT a correctness
// mechanism — a relay may clamp below it silently, which is why termination is
// the cursor (see SyncPageLimit).
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

// TestNegentropySync_RelayCapBelowOurLimitStillGetsTheWholeBoard is the walk's
// hardest case, and the one this file has now got wrong TWICE before measuring it.
//
// Revision 1 asserted that a relay whose cap is below the limit rd asks for
// REFUSES the query (NEG-ERR), and made that refusal the safety net under the
// walk. Revision 2 replaced it with "the relay silently clamps the NEG-OPEN
// window to its cap" — also unmeasured, because neither reachable relay could be
// asked to cap at 400. Both were guesses about a relay nobody had configured.
//
// STRFRY'S CAP IS ITS OWN CONFIG, so it was configured: a throwaway strfry on
// loopback with maxFilterLimit=400, a 600-event board, and the answer is a third
// thing neither guess predicted. maxFilterLimit does not touch NIP-77 at all —
// NEG-OPEN at limit=500 returned 500, unbounded returned all 600. It bounds every
// NIP-01 REQ, silently — every REQ came back with exactly 400 events whatever
// limit was named, including none.
//
// THAT PUTS THE SILENT TRUNCATION ON THE DOWNLOAD HALF, and this test pins the
// consequence. 450 events behind a relay whose REQ cap is 400:
//
//   - window 1: NEG-OPEN at limit=500 names all 450 ids. That is SHORT of the
//     limit, so a walk terminating on window size treats it as the whole board.
//   - the REQ that fetches those 450 ids returns the newest 400. No error.
//     50 events are simply absent, and nothing anywhere says so.
//   - the walk must page anyway, on the `until` cursor alone, and land all 450.
//
// RED BEFORE THE FIX, AND MEASURED RED — these are not the fake's numbers, they
// are what the real capped strfry produced on 2026-07-30 with the two termination
// rules swapped under it:
//
//	relayWindow >= SyncPageLimit -> downloaded=400 of 450, pages=1, no error
//	the `until` cursor           -> downloaded=450,        pages=3
//
// The control is TestNegentropySync_PagesPastTheRelayCap (reqCap == SyncPageLimit,
// so nothing is withheld), which stayed green throughout: the defect is only ever
// visible when the download comes back short of what negentropy promised.
func TestNegentropySync_RelayCapBelowOurLimitStillGetsTheWholeBoard(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	boardCoord := "30301:" + k.PubKeyHex() + ":subcapboard"
	// 450 sits in the band that discriminates the two termination rules: below
	// SyncPageLimit (so window size says "done") and above reqCap (so the download
	// is short). This is the size the live experiment used.
	const total = 450
	const relayCap = SyncPageLimit - 100 // strictly below what the walk asks for
	if total >= SyncPageLimit || total <= relayCap {
		t.Fatalf("board size %d must sit strictly between reqCap=%d and SyncPageLimit=%d or this test measures nothing", total, relayCap, SyncPageLimit)
	}
	events := pagingBoard(t, k, boardCoord, total, time.Now().Unix()-int64(total)-10)
	relay := newCappedRelay(t, events, relayCap)

	log := NewNostrLog(t.TempDir() + "/" + NostrLogFile)
	filter := BoardSyncFilter(boardCoord, nil)
	trusted := map[string]bool{k.PubKeyHex(): true}

	// PREMISE 1: NEG-OPEN honours rd's limit, so window 1 names every one of the
	// 450 ids and comes back SHORT of the limit rd asked for. That short window is
	// exactly what the retired rule read as "the board ends here".
	first, err := nostr.NegentropyReconcile(context.Background(), relay.url(), syncWindowFilter(filter, 0, false), nil)
	if err != nil {
		t.Fatalf("probe reconcile: %v", err)
	}
	if len(first.Need) != total {
		t.Fatalf("probe: NEG-OPEN at limit=%d must HONOUR the limit and name all %d ids, got %d — the fake no longer models the measured strfry",
			SyncPageLimit, total, len(first.Need))
	}
	if len(first.Need) >= SyncPageLimit {
		t.Fatalf("probe: window 1 (%d records) must come back BELOW SyncPageLimit (%d) or the window-size rule would page anyway and this test measures nothing",
			len(first.Need), SyncPageLimit)
	}

	// PREMISE 2: the REQ that fetches those ids is clamped, silently, to strictly
	// fewer than were asked for. This is the measured behaviour the whole test
	// rests on, so it is asserted rather than assumed.
	fetched, err := nostr.FetchByIDs(context.Background(), relay.url(), first.Need)
	if err != nil {
		t.Fatalf("probe fetch: %v", err)
	}
	if len(fetched) != relayCap {
		t.Fatalf("probe: a REQ for %d ids must come back SILENTLY clamped to the relay's %d-record cap, got %d events and no error",
			len(first.Need), relayCap, len(fetched))
	}

	res, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if res.Downloaded != total {
		t.Fatalf("a relay clamping the download at %d BELOW our limit=%d silently truncated the sync: downloaded=%d of %d (need=%d pages=%d)",
			relayCap, SyncPageLimit, res.Downloaded, total, res.Need, res.Pages)
	}
	if res.Pages < 2 {
		t.Fatalf("%d events behind a %d-record silent download cap needs more than one window, walk used %d", total, relayCap, res.Pages)
	}
	stored, err := log.ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if len(stored) != total {
		t.Fatalf("local log holds %d events, want %d", len(stored), total)
	}
	items := ProjectItems(stored, ProjectOptions{Trusted: trusted})
	if len(items) != total {
		t.Fatalf("fresh clone projected %d of %d cards", len(items), total)
	}

	// And it converges: a second sync against the same clamping relay moves nothing.
	res2, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err != nil {
		t.Fatalf("re-sync: %v", err)
	}
	if res2.Downloaded != 0 || res2.Uploaded != 0 {
		t.Fatalf("converged re-sync moved events: downloaded=%d uploaded=%d", res2.Downloaded, res2.Uploaded)
	}
}

// TestNegentropySync_NeverAsksAREQLimitTheRelayWillRefuse pins the OTHER half of
// what the sub-limit experiment turned up, and the half rd has to stay on the
// right side of rather than survive.
//
// A previous revision of this package stated flatly that no measured relay
// refuses a limit above its cap. Measured 2026-07-30, read-only,
// wss://relay.3dl.network refuses one outright:
//
//	REQ {…,limit:600} -> CLOSED "invalid: requested limit 600 exceeds this
//	                     relay's max of 500 — no silent truncation; narrow with
//	                     since/until or resubmit with a smaller limit"
//
// A refused REQ is a failed download, not a short one, so if rd ever names a REQ
// limit above 500 — by raising MaxREQIDs, or by stamping the window's limit onto
// the id-fetch — every sync against that relay breaks outright rather than
// degrading. The relay here refuses on the same rule, and the walk must complete
// against it: not by handling the refusal, but by never provoking it.
func TestNegentropySync_NeverAsksAREQLimitTheRelayWillRefuse(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	boardCoord := "30301:" + k.PubKeyHex() + ":refuseboard"
	const total = 1200
	events := pagingBoard(t, k, boardCoord, total, time.Now().Unix()-int64(total)-10)
	relay := newCappedRelay(t, events, SyncPageLimit)
	relay.reqMaxLimit = SyncPageLimit // the measured 3dl ceiling

	log := NewNostrLog(t.TempDir() + "/" + NostrLogFile)
	filter := BoardSyncFilter(boardCoord, nil)
	trusted := map[string]bool{k.PubKeyHex(): true}

	res, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err != nil {
		t.Fatalf("sync against a relay that refuses over-limit REQs: %v", err)
	}
	if res.Downloaded != total {
		t.Fatalf("downloaded %d of %d (pages=%d)", res.Downloaded, total, res.Pages)
	}

	limits, idCounts := relay.reqShapes()
	if len(limits) == 0 {
		t.Fatal("the walk issued no REQ at all, so this proves nothing about REQ shape")
	}
	for i, l := range limits {
		if l > int64(SyncPageLimit) {
			t.Fatalf("REQ %d named limit=%d, above the %d a real relay refuses outright — every sync against wss://relay.3dl.network would fail",
				i, l, SyncPageLimit)
		}
	}
	// The id set is the other way to exceed a relay's per-REQ bound: 500 ids with
	// no limit named is already at strfry's default maxFilterLimit, and more than
	// that is what ready-8de measured returning no frames at all.
	for i, n := range idCounts {
		if n > nostr.MaxREQIDs {
			t.Fatalf("REQ %d asked for %d ids, above the %d chunk FetchByIDs promises", i, n, nostr.MaxREQIDs)
		}
	}
}

// TestNegentropySync_AMidWalkRelayErrorIsNotReportedAsSuccess guards the walk's
// error path, which the multi-window shape made reachable: a relay that fails on
// the SECOND window has already handed over a partial board on the first, and the
// tempting bug is to keep what arrived and return nil.
//
// This is NOT a cap behaviour and is not offered as one — no measured relay
// answers NEG-ERR to a cap-exceeding limit (see
// TestNegentropySync_RelayCapBelowOurLimitStillGetsTheWholeBoard). It is ordinary
// failure hygiene: a partial board must never be returned as a completed sync.
func TestNegentropySync_AMidWalkRelayErrorIsNotReportedAsSuccess(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	boardCoord := "30301:" + k.PubKeyHex() + ":midwalkerrboard"
	const total = 900
	events := pagingBoard(t, k, boardCoord, total, time.Now().Unix()-int64(total)-10)
	relay := newCappedRelay(t, events, SyncPageLimit)
	relay.negErrAt = 2 // the first window succeeds; the walk then hits a wall

	log := NewNostrLog(t.TempDir() + "/" + NostrLogFile)
	filter := BoardSyncFilter(boardCoord, nil)
	trusted := map[string]bool{k.PubKeyHex(): true}

	res, err := NegentropySync(context.Background(), relay.url(), log, filter, trusted, 30*time.Second, false)
	if err == nil {
		t.Fatalf("a walk that could not finish must not report success: downloaded=%d of %d, pages=%d",
			res.Downloaded, total, res.Pages)
	}
	if !strings.Contains(err.Error(), relay.url()) {
		t.Fatalf("the error must name the relay it came from, got: %v", err)
	}
	if res.Downloaded >= total {
		t.Fatalf("the fake failed the second window, so the board cannot be complete: downloaded=%d of %d", res.Downloaded, total)
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
