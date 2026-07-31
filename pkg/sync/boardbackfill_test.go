package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	"github.com/gorilla/websocket"
)

// storeRelay is a fake relay with actual STORAGE: it accepts EVENT frames over a
// persistent connection (so it exercises the batched publish path) and answers
// REQ subscriptions from what it stored (so the same instance can be used for the
// read-back audit). It implements NIP-01 addressable-event replacement — one
// retained event per (kind, pubkey, d) for kinds 30000-39999 — because that
// replacement is the whole reason the audit compares projections instead of
// counts, and a fake that stored everything would never exercise it.
type storeRelay struct {
	mu sync.Mutex
	// byID holds non-addressable events; byCoord holds the retained addressable
	// event per coordinate.
	byID    map[string]*nostr.Event
	byCoord map[string]*nostr.Event
	// rejectAll, when set, makes the relay answer every EVENT with this NIP-20
	// message and store nothing — the "silently rejects everything while other
	// relays accept" case.
	rejectAll string
	// maxPage models the relay's own per-REQ maximum. It mirrors the production
	// relay's behaviour exactly: a filter asking for MORE than this is REFUSED
	// with a NIP-01 CLOSED frame rather than silently truncated, which is what
	// lets the audit infer "a short page means nothing older exists". 0 means
	// auditPageLimit.
	maxPage int
	// dropNth, when > 0, makes every Nth EVENT fail with the SAME transient
	// store error the production relay returns under throttling. It stores
	// nothing for those writes, so only a retry can close the gap.
	dropNth int
	// underReturnAuthors, when set, models the deployed relay's author-index
	// defect (ready-d84): ANY REQ carrying an "authors" filter is answered with
	// NOTHING, no matter what the relay actually holds, while a kind/tag-only
	// REQ for the exact same events answers correctly. This is the measured
	// shape from ready-5c5 (galtrader: 108/371 by authors vs 371/371 by #a) and
	// ready-0ab (a grants query returning 8 of 11) — a real query against the
	// live relay under-returns, it does not error, so a fixture that errored on
	// "authors" would not model the actual failure a client must tolerate.
	underReturnAuthors bool
	// ignoreTagFilters makes the relay answer a REQ WITHOUT applying its "#a"/"#d"
	// tag filters — it over-returns instead of under-returning. A public relay is
	// untrusted infrastructure and a filter is a request, not a guarantee: a client
	// that treats every returned event as in-scope because it asked for a scope has
	// delegated its own correctness to a stranger's server. The inventory's
	// client-side EventBelongsToBoard check exists for exactly this, and cannot be
	// proven against a fixture that always honours the filter (ready-207 audit).
	ignoreTagFilters bool
	// serveOldestFirst reverses the order events are written to a subscription.
	// Relays conventionally answer newest-first and the audit's pagination assumes
	// it, but ORDER IS NOT A GUARANTEE a client may lean on for correctness: a
	// latest-wins dedup that only picks the right event because the newer one
	// happened to arrive first has not implemented latest-wins at all. Flipping this
	// is what makes that difference observable (ready-207 veracity audit).
	serveOldestFirst bool
	writeSeq         int
	url              string
}

func newStoreRelay(t *testing.T) *storeRelay {
	t.Helper()
	sr := &storeRelay{byID: map[string]*nostr.Event{}, byCoord: map[string]*nostr.Event{}}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
			switch typ {
			case "EVENT":
				var e nostr.Event
				if err := json.Unmarshal(frame[1], &e); err != nil {
					continue
				}
				ok, msg := sr.store(&e)
				resp, _ := json.Marshal([]any{"OK", e.ID, ok, msg})
				if werr := conn.WriteMessage(websocket.TextMessage, resp); werr != nil {
					return
				}
			case "REQ":
				if len(frame) < 3 {
					continue
				}
				var sub string
				_ = json.Unmarshal(frame[1], &sub)
				var filter map[string]any
				_ = json.Unmarshal(frame[2], &filter)
				if msg, refuse := sr.refuseOverLimit(filter); refuse {
					closed, _ := json.Marshal([]any{"CLOSED", sub, msg})
					if werr := conn.WriteMessage(websocket.TextMessage, closed); werr != nil {
						return
					}
					continue
				}
				for _, e := range sr.match(filter) {
					out, _ := json.Marshal([]any{"EVENT", sub, e})
					if werr := conn.WriteMessage(websocket.TextMessage, out); werr != nil {
						return
					}
				}
				eose, _ := json.Marshal([]any{"EOSE", sub})
				if werr := conn.WriteMessage(websocket.TextMessage, eose); werr != nil {
					return
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	sr.url = "ws" + strings.TrimPrefix(srv.URL, "http")
	return sr
}

func (s *storeRelay) store(e *nostr.Event) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.rejectAll != "" {
		return false, s.rejectAll
	}
	s.writeSeq++
	if s.dropNth > 0 && s.writeSeq%s.dropNth == 0 {
		return false, "error: save event doc: throttled"
	}
	if !isAddressableKind(e.Kind) {
		if _, ok := s.byID[e.ID]; ok {
			return true, "duplicate: have this event"
		}
		s.byID[e.ID] = e
		return true, ""
	}
	c := coord(e.Kind, e.PubKey, tagValue(e, "d"))
	if cur, ok := s.byCoord[c]; ok && !newerThan(e, cur) {
		return true, "duplicate: have a newer event for this coordinate"
	}
	s.byCoord[c] = e
	return true, ""
}

// putRaw seeds the relay with an event bypassing replacement rules, so a test
// can pin the relay into a STALE state the audit must detect.
func (s *storeRelay) putRaw(e *nostr.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if isAddressableKind(e.Kind) {
		s.byCoord[coord(e.Kind, e.PubKey, tagValue(e, "d"))] = e
		return
	}
	s.byID[e.ID] = e
}

// putDup seeds an ADDITIONAL version of an addressable coordinate, so the wire
// carries TWO events for one (kind, pubkey, d) at the same time.
//
// putRaw cannot express this: it keys addressable events by coordinate, so seeding a
// stale version and then the current one leaves exactly ONE event in the fixture, and
// any "latest-wins picked the newer event" assertion downstream is really asserting
// against the only event that could have been returned. That is how the dedup rule
// this inventory's method makes load-bearing came to be reported as proven while no
// test covered it (ready-207 veracity audit).
//
// A conformant relay does not serve two versions of one coordinate, and the live relay
// measurably does not today (0 of 2,316 coordinates probed). That is exactly why the
// fixture must be able to: the inventory reads an UNTRUSTED public relay, and a client
// whose dedup only works because the server behaved has not implemented dedup.
func (s *storeRelay) putDup(e *nostr.Event) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.byID[e.ID] = e
}

// refuseOverLimit models the production relay's no-silent-truncation policy: a
// filter whose limit exceeds the relay's maximum is REFUSED, not quietly cut
// down. The audit's termination rule depends on this, so the fake must enforce
// it too — a fake that silently truncated would let a broken pagination rule
// pass here and fail against the real relay.
func (s *storeRelay) refuseOverLimit(filter map[string]any) (string, bool) {
	s.mu.Lock()
	max := s.maxPage
	s.mu.Unlock()
	if max <= 0 {
		max = auditPageLimit
	}
	if lv, ok := filter["limit"].(float64); ok && int(lv) > max {
		return fmt.Sprintf("invalid: requested limit %d exceeds this relay's max of %d", int(lv), max), true
	}
	return "", false
}

func (s *storeRelay) match(filter map[string]any) []*nostr.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	limit := s.maxPage
	if limit <= 0 {
		limit = auditPageLimit
	}
	if lv, ok := filter["limit"].(float64); ok && int(lv) < limit {
		limit = int(lv)
	}
	var until int64
	if uv, ok := filter["until"].(float64); ok {
		until = int64(uv)
	}
	wantKinds := map[int]bool{}
	if kv, ok := filter["kinds"].([]any); ok {
		for _, k := range kv {
			if f, ok := k.(float64); ok {
				wantKinds[int(f)] = true
			}
		}
	}
	wantAuthors := map[string]bool{}
	if av, ok := filter["authors"].([]any); ok {
		for _, a := range av {
			if s, ok := a.(string); ok {
				wantAuthors[s] = true
			}
		}
	}
	if len(wantAuthors) > 0 && s.underReturnAuthors {
		// The deployed relay's author-index defect: an authors-filtered REQ
		// gets nothing, deterministically, regardless of what is actually
		// stored. Any caller still shaping a query around `authors` must see
		// this as a silent empty answer, not an error — that is what makes it
		// dangerous.
		return nil
	}
	tagFilter := func(name string) map[string]bool {
		out := map[string]bool{}
		if tv, ok := filter["#"+name].([]any); ok {
			for _, v := range tv {
				if s, ok := v.(string); ok {
					out[s] = true
				}
			}
		}
		return out
	}
	wantA := tagFilter("a")
	wantD := tagFilter("d")
	if s.ignoreTagFilters {
		wantA, wantD = nil, nil
	}

	var all []*nostr.Event
	for _, e := range s.byID {
		all = append(all, e)
	}
	for _, e := range s.byCoord {
		all = append(all, e)
	}
	// Newest first, matching relay convention (the audit pages backwards).
	for i := 0; i < len(all); i++ {
		for j := i + 1; j < len(all); j++ {
			if newerThan(all[j], all[i]) {
				all[i], all[j] = all[j], all[i]
			}
		}
	}
	if s.serveOldestFirst {
		for i, j := 0, len(all)-1; i < j; i, j = i+1, j-1 {
			all[i], all[j] = all[j], all[i]
		}
	}
	var out []*nostr.Event
	for _, e := range all {
		if len(wantKinds) > 0 && !wantKinds[e.Kind] {
			continue
		}
		if len(wantAuthors) > 0 && !wantAuthors[e.PubKey] {
			continue
		}
		if until > 0 && e.CreatedAt > until {
			continue
		}
		if len(wantA) > 0 {
			hit := false
			for _, a := range tagValues(e, "a") {
				if wantA[a] {
					hit = true
					break
				}
			}
			if !hit {
				continue
			}
		}
		if len(wantD) > 0 && !wantD[tagValue(e, "d")] {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// backfillFixture builds an isolated board's worth of signed events in a temp
// log: one board definition, and for each item a card plus a status event. The
// board D-tag is deliberately NOT the reserved production coordinate.
type backfillFixture struct {
	key    *nostr.Key
	log    *NostrLog
	dir    string
	coord  string
	boardD string
	events []*nostr.Event
}

func newBackfillFixture(t *testing.T, items int) *backfillFixture {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	dir := t.TempDir()
	logPath := filepath.Join(dir, ReadyDir, NostrLogFile)
	log := NewNostrLog(logPath)
	boardD := "ready260-backfill-fixture"
	c := BoardCoord(k.PubKeyHex(), boardD)

	base := time.Now().Unix() - 10000
	var evs []*nostr.Event
	be, err := BuildBoardEvent(k, BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{k.PubKeyHex()}}, base)
	if err != nil {
		t.Fatalf("board event: %v", err)
	}
	evs = append(evs, be)
	for i := 0; i < items; i++ {
		card := CardSpec{
			ItemID: fmt.Sprintf("fix-%03d", i), Title: fmt.Sprintf("item %d", i),
			Status: state.StatusActive, Priority: "p2", Type: "task", BoardD: boardD,
		}
		ce, err := BuildCardEvent(k, card, base+int64(i)+1)
		if err != nil {
			t.Fatalf("card %d: %v", i, err)
		}
		se, err := BuildStatusEventWithIssueRoot(k, card.ItemID, card.Status, ce.ID, "", c, "", base+int64(i)+1, nil)
		if err != nil {
			t.Fatalf("status %d: %v", i, err)
		}
		evs = append(evs, ce, se)
	}
	for _, e := range evs {
		if err := log.Append(e); err != nil {
			t.Fatalf("append: %v", err)
		}
	}
	return &backfillFixture{key: k, log: log, dir: dir, coord: c, boardD: boardD, events: evs}
}

func (f *backfillFixture) publisher(relays ...string) *Publisher {
	return &Publisher{
		Key:         f.key,
		Log:         f.log,
		WriteRelays: relays,
		PendingPath: filepath.Join(f.dir, ReadyDir, NostrPendingFile),
		Timeout:     10 * time.Second,
	}
}

// TestPlanBoardPublish_DescribesExactlyWhatPublishWouldSend is the dry-run
// contract. A bulk republish writes to production data across a whole portfolio,
// so the plan must be computable with NO relay contact and must describe the
// same event set the real publish sends — a plan that could disagree with the
// write it precedes is worse than no plan.
func TestPlanBoardPublish_DescribesExactlyWhatPublishWouldSend(t *testing.T) {
	f := newBackfillFixture(t, 5)
	// A relay URL that would fail instantly if the plan dialed anything.
	pub := f.publisher("ws://127.0.0.1:1")

	plan, err := pub.PlanBoardPublish(f.coord)
	if err != nil {
		t.Fatalf("PlanBoardPublish: %v", err)
	}
	if plan.Events != len(f.events) {
		t.Fatalf("plan.Events = %d, want %d", plan.Events, len(f.events))
	}
	if plan.Items != 5 {
		t.Fatalf("plan.Items = %d, want 5 distinct cards", plan.Items)
	}
	if !plan.HasBoardDefinition {
		t.Fatal("plan.HasBoardDefinition = false, but the fixture wrote a 30301 for this coordinate")
	}
	if plan.ByKind[fmt.Sprint(KindCard)] != 5 || plan.ByKind[fmt.Sprint(KindBoard)] != 1 {
		t.Fatalf("plan.ByKind = %v, want 5 cards and 1 board", plan.ByKind)
	}
	if plan.OldestCreatedAt == 0 || plan.NewestCreatedAt < plan.OldestCreatedAt {
		t.Fatalf("plan time range %d..%d is not sane", plan.OldestCreatedAt, plan.NewestCreatedAt)
	}

	scoped, err := pub.boardScopedEvents(f.coord)
	if err != nil {
		t.Fatalf("boardScopedEvents: %v", err)
	}
	if len(scoped) != plan.Events {
		t.Fatalf("plan says %d events but the publish would send %d — the dry run must describe the real write", plan.Events, len(scoped))
	}
}

// TestBoardScopedEvents_OrdersForAddressableReplacement locks the ordering rule.
// Kinds 30301/30302 are addressable, so a bulk republish decides the relay's
// final projection by the order it sends. The last event written for a
// coordinate must be the one the local log projects as the winner (newerThan):
// created_at ascending, and on a same-second tie the LOWEST id last.
func TestBoardScopedEvents_OrdersForAddressableReplacement(t *testing.T) {
	f := newBackfillFixture(t, 3)
	pub := f.publisher()

	// Two same-second competing cards for one item, appended NEWEST FIRST so
	// append order is the opposite of publish order.
	base := time.Now().Unix() - 500
	var tied []*nostr.Event
	for i := 0; i < 6; i++ {
		card := CardSpec{ItemID: "tie-item", Title: fmt.Sprintf("variant %d", i), Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: f.boardD}
		ce, err := BuildCardEvent(f.key, card, base)
		if err != nil {
			t.Fatalf("card: %v", err)
		}
		tied = append(tied, ce)
	}
	// A later-timestamped event appended FIRST, so a naive append-order publish
	// would send it before the older ones.
	late := CardSpec{ItemID: "late-item", Title: "late", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: f.boardD}
	lateEv, err := BuildCardEvent(f.key, late, base+9000)
	if err != nil {
		t.Fatalf("late card: %v", err)
	}
	if err := f.log.Append(lateEv); err != nil {
		t.Fatalf("append late: %v", err)
	}
	for _, e := range tied {
		if err := f.log.Append(e); err != nil {
			t.Fatalf("append tied: %v", err)
		}
	}

	scoped, err := pub.boardScopedEvents(f.coord)
	if err != nil {
		t.Fatalf("boardScopedEvents: %v", err)
	}
	for i := 1; i < len(scoped); i++ {
		if scoped[i].CreatedAt < scoped[i-1].CreatedAt {
			t.Fatalf("event %d (created_at %d) sorts after %d (created_at %d) — publish order must be created_at ascending or a relay can be handed an OLDER card last",
				i, scoped[i].CreatedAt, i-1, scoped[i-1].CreatedAt)
		}
	}
	// The LAST tied card sent must be the projection winner (lowest id).
	var lastTied *nostr.Event
	winner := tied[0]
	for _, e := range tied {
		if newerThan(e, winner) {
			winner = e
		}
	}
	for _, e := range scoped {
		for _, c := range tied {
			if e.ID == c.ID {
				lastTied = e
			}
		}
	}
	if lastTied == nil || lastTied.ID != winner.ID {
		t.Fatalf("last same-second card sent has id %v, but the projection winner is %s — a relay that simply keeps the last write would retain the wrong card",
			lastTied, winner.ID)
	}
}

// TestPublishBoard_BatchedBackfillThenAuditMatches is the end-to-end proof: an
// untouched relay receives a whole board through the batched publish path, and
// an INDEPENDENT read-back (fresh REQs, signature-verified) then reports a match.
func TestPublishBoard_BatchedBackfillThenAuditMatches(t *testing.T) {
	f := newBackfillFixture(t, 40)
	sr := newStoreRelay(t)
	pub := f.publisher(sr.url)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	local, err := f.log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	before, err := AuditBoardOnRelay(ctx, sr.url, local, f.coord)
	if err != nil {
		t.Fatalf("audit before: %v", err)
	}
	if before.Match || before.BoardDefinitionOnRelay || before.RelayItems != 0 {
		t.Fatalf("audit before backfill reported %+v, want a total miss", before)
	}

	res, err := pub.PublishBoard(ctx, f.coord)
	if err != nil {
		t.Fatalf("PublishBoard: %v", err)
	}
	if len(res.Events) != len(f.events) {
		t.Fatalf("publish reported %d event acks, want %d", len(res.Events), len(f.events))
	}
	for _, ev := range res.Events {
		if !ev.AnyRelay {
			t.Fatalf("event %s reported as reaching no relay", ev.EventID)
		}
	}

	after, err := AuditBoardOnRelay(ctx, sr.url, local, f.coord)
	if err != nil {
		t.Fatalf("audit after: %v", err)
	}
	if !after.Match {
		t.Fatalf("audit after backfill = %+v, want Match", after)
	}
	if after.RelayItems != after.LocalItems || after.LocalItems != 40 {
		t.Fatalf("items local=%d relay=%d, want both 40", after.LocalItems, after.RelayItems)
	}
	if !after.BoardDefinitionOnRelay {
		t.Fatal("board definition not on relay after backfill — the board would be invisible to a reader")
	}

	// Idempotence: re-running must change nothing and must still audit clean.
	if _, err := pub.PublishBoard(ctx, f.coord); err != nil {
		t.Fatalf("PublishBoard re-run: %v", err)
	}
	again, err := AuditBoardOnRelay(ctx, sr.url, local, f.coord)
	if err != nil {
		t.Fatalf("audit after re-run: %v", err)
	}
	if !again.Match || again.RelayEvents != after.RelayEvents {
		t.Fatalf("re-run changed the relay: before=%d after=%d match=%v — a verbatim republish must be idempotent", after.RelayEvents, again.RelayEvents, again.Match)
	}
}

// TestPublishBoard_RepublishesVerbatim is the single most important safety
// property: the backfill re-sends the EXISTING signed events. If any path
// re-signed or re-stamped an event, its id would change and the history would
// fork. This compares what the relay ended up holding, byte for byte, with what
// the local log held.
func TestPublishBoard_RepublishesVerbatim(t *testing.T) {
	f := newBackfillFixture(t, 6)
	sr := newStoreRelay(t)
	pub := f.publisher(sr.url)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pub.PublishBoard(ctx, f.coord); err != nil {
		t.Fatalf("PublishBoard: %v", err)
	}

	localByID := map[string]*nostr.Event{}
	for _, e := range f.events {
		localByID[e.ID] = e
	}
	sr.mu.Lock()
	stored := make([]*nostr.Event, 0, len(sr.byID)+len(sr.byCoord))
	for _, e := range sr.byID {
		stored = append(stored, e)
	}
	for _, e := range sr.byCoord {
		stored = append(stored, e)
	}
	sr.mu.Unlock()

	if len(stored) != len(f.events) {
		t.Fatalf("relay holds %d events, local log has %d", len(stored), len(f.events))
	}
	for _, got := range stored {
		want, ok := localByID[got.ID]
		if !ok {
			t.Fatalf("relay holds event id %s which is NOT in the local log — something re-signed or fabricated an event", got.ID)
		}
		wantJSON, _ := json.Marshal(want)
		gotJSON, _ := json.Marshal(got)
		if string(wantJSON) != string(gotJSON) {
			t.Fatalf("event %s differs on the relay:\n local: %s\n relay: %s", got.ID, wantJSON, gotJSON)
		}
		if got.CreatedAt != want.CreatedAt || got.Sig != want.Sig {
			t.Fatalf("event %s had created_at/sig altered in transit", got.ID)
		}
	}
}

// TestAuditBoardOnRelay_DetectsSilentRejectionBehindAnAcceptingRelay is the
// ready-f7b failure mode at bulk scale, and the reason the audit exists at all:
// with one relay accepting everything and another rejecting everything, the
// publish reports success for every event, so only an independent read-back of
// the REJECTING relay can reveal that it holds nothing.
func TestAuditBoardOnRelay_DetectsSilentRejectionBehindAnAcceptingRelay(t *testing.T) {
	f := newBackfillFixture(t, 10)
	accepting := newStoreRelay(t)
	rejecting := newStoreRelay(t)
	rejecting.rejectAll = "blocked: pubkey not admitted"

	pub := f.publisher(accepting.url, rejecting.url)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	res, err := pub.PublishBoard(ctx, f.coord)
	if err != nil {
		t.Fatalf("PublishBoard: %v", err)
	}
	for _, ev := range res.Events {
		if !ev.AnyRelay {
			t.Fatalf("event %s: AnyRelay=false, but the accepting relay took it", ev.EventID)
		}
	}
	if res.Buffered {
		t.Fatal("result reports Buffered, but one relay accepted every event")
	}

	// rd's own report says success. The read-back says otherwise.
	local, err := f.log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	bad, err := AuditBoardOnRelay(ctx, rejecting.url, local, f.coord)
	if err != nil {
		t.Fatalf("audit rejecting relay: %v", err)
	}
	if bad.Match {
		t.Fatal("audit reported a match against a relay that rejected every event — the read-back is not independent")
	}
	if bad.BoardDefinitionOnRelay || bad.RelayItems != 0 {
		t.Fatalf("audit of the rejecting relay = %+v, want nothing found", bad)
	}
	good, err := AuditBoardOnRelay(ctx, accepting.url, local, f.coord)
	if err != nil {
		t.Fatalf("audit accepting relay: %v", err)
	}
	if !good.Match {
		t.Fatalf("audit of the accepting relay = %+v, want Match", good)
	}

	// And the per-relay acks carry the rejection even though the reduced
	// outcome is "accepted" — that detail is what an operator needs.
	sawRejection := false
	for _, ev := range res.Events {
		for _, a := range ev.Acks {
			if a.Relay == rejecting.url && !a.Accepted {
				sawRejection = true
			}
		}
	}
	if !sawRejection {
		t.Fatal("no per-relay ack recorded the rejecting relay's refusal — the batch path must keep per-relay detail")
	}
}

// TestAuditBoardOnRelay_DetectsStaleAddressableEvent covers the regression the
// publish ordering exists to prevent: the relay retained an OLDER card than the
// local log's winner. Counting events would call this a match (the coordinate is
// present); comparing projections must not.
func TestAuditBoardOnRelay_DetectsStaleAddressableEvent(t *testing.T) {
	f := newBackfillFixture(t, 4)
	sr := newStoreRelay(t)
	pub := f.publisher(sr.url)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pub.PublishBoard(ctx, f.coord); err != nil {
		t.Fatalf("PublishBoard: %v", err)
	}

	// Append a NEWER card locally without publishing it: the relay is now stale.
	newer, err := BuildCardEvent(f.key, CardSpec{
		ItemID: "fix-000", Title: "edited later", Status: state.StatusActive,
		Priority: "p0", Type: "task", BoardD: f.boardD,
	}, time.Now().Unix())
	if err != nil {
		t.Fatalf("build newer card: %v", err)
	}
	if err := f.log.Append(newer); err != nil {
		t.Fatalf("append newer: %v", err)
	}
	local, err := f.log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	a, err := AuditBoardOnRelay(ctx, sr.url, local, f.coord)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if a.Match {
		t.Fatal("audit reported a match although the relay retained an older card for fix-000")
	}
	if len(a.StaleCoords) != 1 || !strings.HasSuffix(a.StaleCoords[0], ":fix-000") {
		t.Fatalf("StaleCoords = %v, want exactly the fix-000 card coordinate", a.StaleCoords)
	}
	if a.RelayItems != a.LocalItems {
		t.Fatalf("item counts differ (local=%d relay=%d) — this case must be caught by the STALE check, not by a count", a.LocalItems, a.RelayItems)
	}
}

// TestAuditBoardOnRelay_DetectsMissingStatusEvents proves the non-addressable
// half of the comparison: a board whose cards all landed but whose append-only
// status chain did not is incomplete, and item counts alone would call it clean.
func TestAuditBoardOnRelay_DetectsMissingStatusEvents(t *testing.T) {
	f := newBackfillFixture(t, 5)
	sr := newStoreRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Seed the relay with everything EXCEPT the status events.
	for _, e := range f.events {
		if e.Kind == KindStatusOpen {
			continue
		}
		sr.putRaw(e)
	}
	local, err := f.log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	a, err := AuditBoardOnRelay(ctx, sr.url, local, f.coord)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if a.Match {
		t.Fatal("audit reported a match although every status event is missing")
	}
	if a.MissingRegular != 5 {
		t.Fatalf("MissingRegular = %d, want 5", a.MissingRegular)
	}
	if a.RelayItems != 5 || len(a.MissingCoords) != 0 {
		t.Fatalf("addressable side should be clean: items=%d missing=%v", a.RelayItems, a.MissingCoords)
	}
}

// TestAuditBoardOnRelay_RejectsUnverifiableRelayEvents proves the read-back is
// evidence rather than hearsay: an event a relay serves under the right
// coordinate but with a broken signature must not count towards coverage.
func TestAuditBoardOnRelay_RejectsUnverifiableRelayEvents(t *testing.T) {
	f := newBackfillFixture(t, 3)
	sr := newStoreRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	for _, e := range f.events {
		tampered := *e
		if tampered.Kind == KindCard {
			tampered.Content = tampered.Content + " tampered"
		}
		sr.putRaw(&tampered)
	}
	local, err := f.log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	a, err := AuditBoardOnRelay(ctx, sr.url, local, f.coord)
	if err != nil {
		t.Fatalf("audit: %v", err)
	}
	if a.RelayVerifyFailures == 0 {
		t.Fatal("audit counted zero verify failures although every card was tampered with")
	}
	if a.Match {
		t.Fatal("audit reported a match while the relay served unverifiable events")
	}
}

// TestAuditBoardOnRelay_PaginatesPastThePageLimit proves the read-back is not
// silently truncated by the relay's per-REQ cap. A truncated read-back is the
// worst possible audit failure: it under-counts the relay and would report a
// COMPLETE board as a gap, or — after a backfill — could be mistaken for one.
func TestAuditBoardOnRelay_PaginatesPastThePageLimit(t *testing.T) {
	// More board-scoped events than ONE page can carry, so the walk must page.
	const items = auditPageLimit/2 + 40 // 2 events per item + the board event
	f := newBackfillFixture(t, items)
	if len(f.events) <= auditPageLimit {
		t.Fatalf("fixture built %d events, need more than one page (%d) for this test to mean anything", len(f.events), auditPageLimit)
	}
	sr := newStoreRelay(t)
	pub := f.publisher(sr.url)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if _, err := pub.PublishBoard(ctx, f.coord); err != nil {
		t.Fatalf("PublishBoard: %v", err)
	}
	local, err := f.log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	a, err := AuditBoardOnRelay(ctx, sr.url, local, f.coord)
	if err != nil {
		t.Fatalf("audit across page boundaries: %v", err)
	}
	if !a.Match {
		t.Fatalf("audit = %+v, want Match — a paginated read-back must find everything a single page would", a)
	}
	if a.RelayItems != items {
		t.Fatalf("RelayItems = %d, want %d — pagination dropped events", a.RelayItems, items)
	}
}

// lossyRelay wraps a storeRelay's acceptance decision to drop a deterministic
// share of writes with the SAME transient message the production relay returns
// when its backing store throttles ("error: save …"). rd classifies that as
// retryable, so the events are buffered rather than dead-lettered — which is
// exactly the condition the repair loop exists to converge out of.
func (s *storeRelay) dropEveryNth(n int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dropNth = n
}

// TestPublishBoardDelta_ConvergesAgainstALossyRelay is the ready-260 repair
// proof. The production relay accepts only a share of a burst and answers the
// rest with a transient store error; a single whole-board pass therefore never
// finishes a board. Repair must (a) send only what is actually missing, so each
// round is cheaper than the last, and (b) still reach a full match.
func TestPublishBoardDelta_ConvergesAgainstALossyRelay(t *testing.T) {
	f := newBackfillFixture(t, 60)
	sr := newStoreRelay(t)
	sr.dropEveryNth(3) // drop every 3rd write, transiently
	pub := f.publisher(sr.url)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	prevSent := -1
	var audit BoardRelayAudit
	for round := 0; round < 12; round++ {
		var sent int
		var err error
		audit, sent, err = pub.PublishBoardDelta(ctx, sr.url, f.coord)
		if err != nil {
			t.Fatalf("round %d: %v", round, err)
		}
		if audit.Match {
			if sent != 0 {
				t.Fatalf("round %d: audit matched but %d event(s) were still re-sent — a matched board has nothing to repair", round, sent)
			}
			break
		}
		if sent == 0 {
			t.Fatalf("round %d: board does not match but the delta is empty — repair cannot make progress", round)
		}
		if prevSent >= 0 && sent > prevSent {
			t.Fatalf("round %d re-sent %d event(s), MORE than the previous round's %d — a delta repair must shrink, otherwise it is just a whole-board re-send with extra steps", round, sent, prevSent)
		}
		prevSent = sent
	}
	if !audit.Match {
		t.Fatalf("board never converged against a lossy relay: %+v", audit)
	}
}

// TestBoardRelayDelta_SendsOnlyTheGap proves the delta is a GAP, not the whole
// board: with all but a handful of events already on the relay, repair must send
// only the handful. On a throttled relay a whole-board re-send makes every
// round as expensive as the first, which is what made the portfolio backfill
// impractical before this path existed.
func TestBoardRelayDelta_SendsOnlyTheGap(t *testing.T) {
	f := newBackfillFixture(t, 20)
	sr := newStoreRelay(t)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Seed the relay with everything except three known events.
	var held []*nostr.Event
	for i, e := range f.events {
		if i%13 == 5 {
			held = append(held, e)
			continue
		}
		sr.putRaw(e)
	}
	if len(held) == 0 {
		t.Fatal("fixture withheld nothing — the test would prove nothing")
	}
	local, err := f.log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	_, delta, err := BoardRelayDelta(ctx, sr.url, local, f.coord)
	if err != nil {
		t.Fatalf("BoardRelayDelta: %v", err)
	}
	got := map[string]bool{}
	for _, e := range delta {
		got[e.ID] = true
	}
	for _, e := range held {
		if !got[e.ID] {
			t.Fatalf("delta omits withheld event %s (kind %d) — repair would never close the gap", e.ID, e.Kind)
		}
	}
	if len(delta) != len(held) {
		t.Fatalf("delta has %d event(s) for a %d-event gap — repair must send the GAP, not the board", len(delta), len(held))
	}
	// And the delta is in publish order, so an addressable replacement can never
	// regress.
	for i := 1; i < len(delta); i++ {
		if delta[i].CreatedAt < delta[i-1].CreatedAt {
			t.Fatalf("delta is not in created_at order at index %d", i)
		}
	}
}

// TestAuditBoardOnRelay_ErrorsRatherThanTruncateOnATimestampPlateau locks the
// audit's honesty failure mode. `until` pagination cannot advance past more
// events than one page can hold sharing a single created_at second. Returning
// what it managed to read would understate the relay and could report a
// backfilled board as a gap — or, worse, a gap as a match. It must error.
func TestAuditBoardOnRelay_ErrorsRatherThanTruncateOnATimestampPlateau(t *testing.T) {
	f := newBackfillFixture(t, 1)
	sr := newStoreRelay(t)

	// Seed more same-second card events than ONE page can hold, so `until`
	// (inclusive) can never move past that second.
	stamp := time.Now().Unix() - 42
	for i := 0; i < auditPageLimit+5; i++ {
		ce, err := BuildCardEvent(f.key, CardSpec{
			ItemID: fmt.Sprintf("plateau-%02d", i), Title: "plateau", Status: state.StatusActive,
			Priority: "p2", Type: "task", BoardD: f.boardD,
		}, stamp)
		if err != nil {
			t.Fatalf("build plateau card: %v", err)
		}
		sr.putRaw(ce)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	local, err := f.log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	if _, err := AuditBoardOnRelay(ctx, sr.url, local, f.coord); err == nil {
		t.Fatal("audit returned a clean result although pagination could not read the whole board — a truncated read-back must be an error, never a silent short answer")
	} else if !strings.Contains(err.Error(), "stalled") {
		t.Fatalf("audit error = %v, want the pagination-stall error", err)
	}
}

// TestAuditBoardOnRelay_DefinitionSurvivesAuthorIndexUnderReturn is the
// ready-d84 proof. boardaudit.go used to fetch the board's own kind-30301
// DEFINITION with an authors-filtered REQ — the one filter shape ready-5c5 and
// ready-0ab measured the deployed relay to silently under-return on (galtrader:
// 108/371 by authors vs 371/371 by #a; a grants query returning 8 of 11 by
// authors). Simulating that exact defect here (storeRelay.underReturnAuthors)
// is deliberate: ready-0ab records the relay-side fix as merged but NOT
// deployed, so a test run against the live relay today would not exercise the
// under-return at all and would prove nothing about the client-side query
// shape — the very trap this item's done condition calls out ("a test that
// passes against the live relay is not proof the filter is safe").
//
// Against the PRE-FIX query ({kinds:[30301],"authors":[owner],"#d":[boardD]}),
// this fixture answers with nothing (authors is set), so
// BoardDefinitionOnRelay would be false and Match would be false for a board
// that is fully present. Against the fix (kind + "#d" only, no authors), the
// fixture answers normally and the audit reports Match.
func TestAuditBoardOnRelay_DefinitionSurvivesAuthorIndexUnderReturn(t *testing.T) {
	f := newBackfillFixture(t, 5)
	sr := newStoreRelay(t)
	pub := f.publisher(sr.url)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := pub.PublishBoard(ctx, f.coord); err != nil {
		t.Fatalf("PublishBoard: %v", err)
	}

	// The board is fully and correctly published. NOW break the relay's author
	// index for every REQ that carries `authors` — the definition fetch is the
	// only one in this package that would, pre-fix, shape a query that way.
	sr.underReturnAuthors = true

	local, err := f.log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	a, err := AuditBoardOnRelay(ctx, sr.url, local, f.coord)
	if err != nil {
		t.Fatalf("AuditBoardOnRelay: %v", err)
	}
	if !a.BoardDefinitionOnRelay {
		t.Fatalf("BoardDefinitionOnRelay=false for a board definition that is provably on the relay — the audit is still querying it in a way the author-index under-return defeats: %+v", a)
	}
	if !a.Match {
		t.Fatalf("Match=false although every event is present and verified: %+v", a)
	}
}

// TestGuardedPublishMany_RefusesReservedBoardBatch locks the reserved-production
// board guard onto the BATCH path. A backfill is exactly the shape of operation
// that would casually sweep this repo's own live board into a test run, so one
// offending event must refuse the whole batch before any dial.
func TestGuardedPublishMany_RefusesReservedBoardBatch(t *testing.T) {
	f := newBackfillFixture(t, 2)
	sr := newStoreRelay(t)

	reserved, err := BuildCardEvent(f.key, CardSpec{
		ItemID: "ready-260-probe", Title: "probe", Status: state.StatusActive,
		Priority: "p3", Type: "task", BoardD: reservedProductionBoardD,
	}, time.Now().Unix())
	if err != nil {
		t.Fatalf("build reserved card: %v", err)
	}
	batch := append([]*nostr.Event{}, f.events...)
	batch = append(batch, reserved)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	acks, err := GuardedPublishMany(ctx, sr.url, batch, false)
	if err == nil {
		t.Fatal("GuardedPublishMany accepted a batch containing a reserved-production-board event")
	}
	if len(acks) != len(batch) {
		t.Fatalf("got %d acks for a %d-event batch", len(acks), len(batch))
	}
	for i, a := range acks {
		if a.Err == nil {
			t.Fatalf("ack[%d] carries no error — a refused batch must mark every event", i)
		}
	}
	sr.mu.Lock()
	stored := len(sr.byID) + len(sr.byCoord)
	sr.mu.Unlock()
	if stored != 0 {
		t.Fatalf("relay stored %d events from a refused batch — the guard must run before any dial", stored)
	}
}
