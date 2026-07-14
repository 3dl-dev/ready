// Deterministic unit tests for ready-f92: replay + causal-ordering protection at
// nostr ingestion/projection.
//
// Four proof points, all deterministic (no wall-clock dependence except the
// far-future admission gate, which uses an unreachable +10y stamp):
//
//	(a) same event SET in different append orders projects the IDENTICAL state —
//	    the cross-machine convergence guarantee, including same-second competing
//	    card AND status edits resolved by the NIP-01 id tie-break;
//	(b) a stale but validly-signed OLD status event re-fed to projection does NOT
//	    resurrect old state (supersession/replay protection);
//	(c) a far-future created_at is rejected at ingestion (AppendUnique skew gate);
//	(d) dedup is idempotent — a duplicated status event does not fabricate a phantom
//	    history entry, and re-ingesting a known event adds nothing.
package sync

import (
	"math/rand"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// projSummary is a stable, order-independent fingerprint of a single projected
// item — everything two converging machines MUST agree on.
type projSummary struct {
	title   string
	status  string
	history []string // "from->to|by|note" per entry, in projected order
}

func summarize(t *testing.T, events []*nostr.Event, opts ProjectOptions, itemID string) projSummary {
	t.Helper()
	items := ProjectItems(events, opts)
	it, ok := items[itemID]
	if !ok {
		t.Fatalf("item %s not projected", itemID)
	}
	s := projSummary{title: it.Title, status: it.Status}
	for _, h := range it.History {
		s.history = append(s.history, h.FromStatus+"->"+h.ToStatus+"|"+h.ChangedBy+"|"+h.Note)
	}
	return s
}

func equalSummary(a, b projSummary) bool {
	if a.title != b.title || a.status != b.status || len(a.history) != len(b.history) {
		return false
	}
	for i := range a.history {
		if a.history[i] != b.history[i] {
			return false
		}
	}
	return true
}

// permute returns a fresh shuffled copy of events under the given seed — this is
// the stand-in for "two machines that appended/fetched the same events in
// different orders" (the local log append order, a relay's fetch order, a merge
// union order are all just permutations of the identical signed-event SET).
func permute(events []*nostr.Event, seed int64) []*nostr.Event {
	out := make([]*nostr.Event, len(events))
	copy(out, events)
	r := rand.New(rand.NewSource(seed))
	r.Shuffle(len(out), func(i, j int) { out[i], out[j] = out[j], out[i] })
	return out
}

// TestProjection_ConvergesUnderPermutation is the ready-f92 convergence keystone
// (folds ready-b6a HIGH + ready-523): a set that INCLUDES same-second competing
// card edits and same-second competing status transitions must project to one
// canonical state, identical under every append/fetch order. The old
// (created_at, append-index) tie-break failed this exact case.
func TestProjection_ConvergesUnderPermutation(t *testing.T) {
	k := testKey(t)
	itemID := "ready-conv-1"
	opts := ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}}

	// Base history: create(inbox) @1000, claim(active) @2000.
	c0, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v0", Status: state.StatusInbox, Priority: "p1", BoardD: "ready"}, 1000)
	s0, _ := BuildStatusEvent(k, itemID, state.StatusInbox, c0.ID, "", 1000)
	c1, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v0", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready"}, 2000)
	s1, _ := BuildStatusEvent(k, itemID, state.StatusActive, c1.ID, "", 2000)

	// SAME-SECOND competing CARD edits @3000 — two machines edited the title in the
	// same clock second. Different content => different ids. NIP-01: lowest id wins.
	cardA, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "edit-A", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready"}, 3000)
	cardB, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "edit-B", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready"}, 3000)

	// SAME-SECOND competing STATUS transitions @3000 — one closes done, one cancels.
	// Both authoritative (author). Different content => different ids. Deterministic
	// order = id asc; current status = the higher-id one.
	sDone, _ := BuildStatusEvent(k, itemID, state.StatusDone, cardA.ID, "reason-done", 3000)
	sCancelled, _ := BuildStatusEvent(k, itemID, state.StatusCancelled, cardB.ID, "reason-cancel", 3000)

	base := []*nostr.Event{c0, s0, c1, s1, cardA, cardB, sDone, sCancelled}

	// Expected canonical winners, computed as a pure function of the ids.
	wantTitle := "edit-A"
	if cardB.ID < cardA.ID {
		wantTitle = "edit-B"
	}
	// Current status = the status event with the GREATER id (last after id-asc sort).
	wantStatus := state.StatusDone
	if sCancelled.ID > sDone.ID {
		wantStatus = state.StatusCancelled
	}

	ref := summarize(t, base, opts, itemID)
	if ref.title != wantTitle {
		t.Errorf("winning title = %q, want %q (NIP-01 lowest-id card wins)", ref.title, wantTitle)
	}
	if ref.status != wantStatus {
		t.Errorf("current status = %q, want %q (deterministic status tie-break)", ref.status, wantStatus)
	}

	// The heart of the test: 200 random permutations must ALL match the reference.
	for seed := int64(0); seed < 200; seed++ {
		got := summarize(t, permute(base, seed), opts, itemID)
		if !equalSummary(got, ref) {
			t.Fatalf("permutation seed=%d diverged:\n  ref=%+v\n  got=%+v", seed, ref, got)
		}
	}
}

// TestProjection_StaleReplayDoesNotResurrect proves supersession/replay protection:
// after the item is DONE, re-feeding an OLD (earlier created_at) 'reopen->active'
// status event — validly signed — must NOT resurrect the active state. Latest-wins
// by created_at handles it; the stale event lands in its chronological history slot
// and never becomes current.
func TestProjection_StaleReplayDoesNotResurrect(t *testing.T) {
	k := testKey(t)
	itemID := "ready-replay-1"
	opts := ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}}

	c0, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v0", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready"}, 1000)
	sActive, _ := BuildStatusEvent(k, itemID, state.StatusActive, c0.ID, "claimed", 1000)
	c1, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v0", Status: state.StatusDone, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready"}, 2000)
	sDone, _ := BuildStatusEvent(k, itemID, state.StatusDone, c1.ID, "shipped", 2000)

	// Baseline: create+claim+done => current status done.
	baseline := summarize(t, []*nostr.Event{c0, sActive, c1, sDone}, opts, itemID)
	if baseline.status != state.StatusDone {
		t.Fatalf("baseline status = %q, want done", baseline.status)
	}

	// ATTACK: replay the OLD active status (created_at 1000) AFTER done (2000).
	replayed := []*nostr.Event{c0, sActive, c1, sDone, sActive} // sActive re-fed
	got := summarize(t, replayed, opts, itemID)
	if got.status != state.StatusDone {
		t.Errorf("after stale replay status = %q, want done (must not resurrect)", got.status)
	}
	// And the duplicated sActive must NOT add a phantom history entry (dedup, point d).
	if !equalSummary(got, baseline) {
		t.Errorf("stale replay changed projection:\n  baseline=%+v\n  got=%+v", baseline, got)
	}

	// Even a stale replay with a HIGHER-created_at forgery is blocked at ingestion by
	// the skew gate (tested separately); here we prove pure ordering resists an
	// in-window stale event regardless of its position in the slice.
	for seed := int64(0); seed < 50; seed++ {
		g := summarize(t, permute(replayed, seed), opts, itemID)
		if g.status != state.StatusDone {
			t.Fatalf("seed=%d: stale replay resurrected state: %q", seed, g.status)
		}
	}
}

// TestAppendUnique_RejectsFarFuture proves the created_at skew bound at INGESTION
// (point c): an event stamped far in the future is rejected by AppendUnique and
// never reaches the local authoritative log, while an in-window event is admitted.
func TestAppendUnique_RejectsFarFuture(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	log := NewNostrLog(filepath.Join(dir, "nostr-log.jsonl"))

	nowSec := time.Now().Unix()
	// In-window event (dated now) — admissible.
	okEv, err := BuildCardEvent(k, CardSpec{ItemID: "ready-skew-ok", Title: "ok", Status: state.StatusActive, Priority: "p1", BoardD: "ready"}, nowSec)
	if err != nil {
		t.Fatalf("build ok event: %v", err)
	}
	// Far-future event (+10 years) — must be rejected.
	future := time.Now().Add(10 * 365 * 24 * time.Hour).Unix()
	badEv, err := BuildCardEvent(k, CardSpec{ItemID: "ready-skew-bad", Title: "bad", Status: state.StatusActive, Priority: "p1", BoardD: "ready"}, future)
	if err != nil {
		t.Fatalf("build future event: %v", err)
	}

	added, err := log.AppendUnique([]*nostr.Event{okEv, badEv})
	if err != nil {
		t.Fatalf("AppendUnique: %v", err)
	}
	if added != 1 {
		t.Errorf("added = %d, want 1 (far-future event must be rejected)", added)
	}

	stored, err := log.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if len(stored) != 1 || stored[0].ID != okEv.ID {
		t.Errorf("log holds %d events, want only the in-window one", len(stored))
	}
	// Direct helper check, robust to run-time: +10y is always beyond the bound.
	if admissibleCreatedAt(badEv, time.Now()) {
		t.Error("admissibleCreatedAt accepted a +10y event")
	}
	if !admissibleCreatedAt(okEv, time.Now()) {
		t.Error("admissibleCreatedAt rejected a now-dated event")
	}
	// Exactly at the boundary (now + MaxCreatedAtSkew) is admissible; one second past is not.
	boundary := &nostr.Event{CreatedAt: time.Now().Add(MaxCreatedAtSkew).Unix()}
	past := &nostr.Event{CreatedAt: time.Now().Add(MaxCreatedAtSkew + time.Second).Unix()}
	if !admissibleCreatedAt(boundary, time.Now()) {
		t.Error("boundary event should be admissible")
	}
	if admissibleCreatedAt(past, time.Now()) {
		t.Error("event past the skew bound should be rejected")
	}
}

// TestAppendUnique_DedupIdempotent proves ingestion dedup (point d): re-ingesting
// an already-known event is a no-op, so refeeding the relay/merge union repeatedly
// never grows the log.
func TestAppendUnique_DedupIdempotent(t *testing.T) {
	k := testKey(t)
	dir := t.TempDir()
	log := NewNostrLog(filepath.Join(dir, "nostr-log.jsonl"))

	nowSec := time.Now().Unix()
	ev, _ := BuildCardEvent(k, CardSpec{ItemID: "ready-dedup-1", Title: "x", Status: state.StatusActive, Priority: "p1", BoardD: "ready"}, nowSec)

	a1, _ := log.AppendUnique([]*nostr.Event{ev})
	a2, _ := log.AppendUnique([]*nostr.Event{ev, ev}) // same event twice + already known
	a3, _ := log.AppendUnique([]*nostr.Event{ev})     // re-ingest known
	if a1 != 1 || a2 != 0 || a3 != 0 {
		t.Errorf("added counts = %d,%d,%d; want 1,0,0 (idempotent dedup)", a1, a2, a3)
	}
	stored, _ := log.ReadAll()
	if len(stored) != 1 {
		t.Errorf("log holds %d events, want 1 after repeated ingestion", len(stored))
	}
}

// TestProjection_DedupNoPhantomHistory proves projection-side dedup (point d): a
// duplicated status event in the projection input must NOT double the history.
func TestProjection_DedupNoPhantomHistory(t *testing.T) {
	k := testKey(t)
	itemID := "ready-dedup-hist"
	opts := ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}}

	c0, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v0", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready"}, 1000)
	s0, _ := BuildStatusEvent(k, itemID, state.StatusActive, c0.ID, "claimed", 1000)
	c1, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v0", Status: state.StatusDone, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready"}, 2000)
	s1, _ := BuildStatusEvent(k, itemID, state.StatusDone, c1.ID, "done", 2000)

	clean := summarize(t, []*nostr.Event{c0, s0, c1, s1}, opts, itemID)
	// Feed every event twice (duplicate ids) + duplicate cards.
	dup := summarize(t, []*nostr.Event{c0, c0, s0, s0, c1, c1, s1, s1}, opts, itemID)
	if !equalSummary(clean, dup) {
		t.Errorf("dedup failed — duplicated events changed projection:\n  clean=%+v\n  dup=%+v", clean, dup)
	}
	if len(dup.history) != 2 {
		t.Errorf("history len = %d, want 2 (no phantom entries from duplicates)", len(dup.history))
	}
}
