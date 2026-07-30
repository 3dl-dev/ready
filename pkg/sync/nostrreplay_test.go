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

// TestProjection_CreatedAtSurvivesMutation is the ready-4ec regression: an item's
// creation time must survive EVERY republish of its 30302 card (update, close,
// cancel, delegate, gate, approve, dep add all funnel through the same
// card-republish + status-event mechanics this test exercises), while its
// last-modified time (UpdatedAt) keeps advancing.
//
// This drives the REAL production write path, not a hand-set CardSpec.CreatedAt:
// each step takes the *state.Item ProjectItems just returned, mutates it in place
// exactly as an `rd` verb would (title edit, status change), and rebuilds the
// republish CardSpec by calling CardSpecFromItem(item, boardD) — the same call
// nostrwrite.go makes on every live mutation. If CardSpecFromItem's `CreatedAt:
// itemCreatedAtSecs(item)` line were deleted (or the field ignored), every
// republish below would carry NO "created" tag, itemFromCard would fall back to
// each new card's OWN event created_at, and the CreatedAt assertions after the
// 2000/3000/4000 steps would fail — this test cannot pass without that line
// actually running. The genesis card alone carries NO "created" tag (a brand-new
// item's CreatedAt is unset), so itemFromCard falls back to ITS OWN event
// created_at — correct for exactly that one bootstrap card.
func TestProjection_CreatedAtSurvivesMutation(t *testing.T) {
	k := testKey(t)
	itemID := "ready-created-1"
	boardD := "ready"
	opts := ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}}
	wantCreated := int64(1000) * int64(time.Second)

	// t=1000: genesis create (inbox). No "created" tag yet (CreatedAt unset) --
	// the fallback to this card's own created_at is what seeds the true value.
	c0, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v0", Status: state.StatusInbox, Priority: "p1", BoardD: boardD}, 1000)
	s0, _ := BuildStatusEvent(k, itemID, state.StatusInbox, c0.ID, "", 1000)

	events := []*nostr.Event{c0, s0}
	items := ProjectItems(events, opts)
	it, ok := items[itemID]
	if !ok {
		t.Fatalf("item %s not projected after genesis", itemID)
	}
	if it.CreatedAt != wantCreated {
		t.Fatalf("genesis CreatedAt = %d, want %d", it.CreatedAt, wantCreated)
	}
	if it.UpdatedAt != wantCreated {
		t.Fatalf("genesis UpdatedAt = %d, want %d", it.UpdatedAt, wantCreated)
	}

	// t=2000: MUTATE the projected item (title edit, no status change — the same
	// shape as `rd update`/`rd dep add`/a re-parent) and republish it through
	// CardSpecFromItem, the real production call, not a hand-authored CardSpec.
	it.Title = "v1 edited"
	c1, _ := BuildCardEvent(k, CardSpecFromItem(it, boardD), 2000)
	events = append(events, c1)

	items = ProjectItems(events, opts)
	it, ok = items[itemID]
	if !ok {
		t.Fatalf("item %s not projected after card-only republish", itemID)
	}
	if it.CreatedAt != wantCreated {
		t.Errorf("CreatedAt after card-only republish = %d, want unchanged genesis value %d (ready-4ec regression)", it.CreatedAt, wantCreated)
	}
	wantUpdatedAfterEdit := int64(2000) * int64(time.Second)
	if it.UpdatedAt != wantUpdatedAfterEdit {
		t.Errorf("UpdatedAt after card-only republish = %d, want %d", it.UpdatedAt, wantUpdatedAfterEdit)
	}

	// t=3000: MUTATE status to active (the same shape as `rd claim`/`rd
	// delegate`) — republish the card via CardSpecFromItem AND emit an
	// authoritative status event.
	it.Status = state.StatusActive
	it.By = k.PubKeyHex()
	c2, _ := BuildCardEvent(k, CardSpecFromItem(it, boardD), 3000)
	s1, _ := BuildStatusEvent(k, itemID, state.StatusActive, c2.ID, "claimed", 3000)
	events = append(events, c2, s1)

	items = ProjectItems(events, opts)
	it, ok = items[itemID]
	if !ok {
		t.Fatalf("item %s not projected after claim", itemID)
	}
	if it.CreatedAt != wantCreated {
		t.Errorf("CreatedAt after claim = %d, want unchanged genesis value %d (ready-4ec regression)", it.CreatedAt, wantCreated)
	}

	// t=4000: MUTATE status to done (the same shape as `rd done`/`rd cancel`/`rd
	// fail`) — republish the card via CardSpecFromItem AND emit a terminal status
	// event.
	it.Status = state.StatusDone
	c3, _ := BuildCardEvent(k, CardSpecFromItem(it, boardD), 4000)
	s2, _ := BuildStatusEvent(k, itemID, state.StatusDone, c3.ID, "shipped", 4000)
	events = append(events, c3, s2)

	all := events
	items = ProjectItems(all, opts)
	it, ok = items[itemID]
	if !ok {
		t.Fatalf("item %s not projected after mutations", itemID)
	}
	if it.CreatedAt != wantCreated {
		t.Errorf("CreatedAt after 3 republishes = %d, want unchanged genesis value %d (ready-4ec regression)", it.CreatedAt, wantCreated)
	}
	wantUpdated := int64(4000) * int64(time.Second)
	if it.UpdatedAt != wantUpdated {
		t.Errorf("UpdatedAt = %d, want %d (last-modified must still advance)", it.UpdatedAt, wantUpdated)
	}
	if it.CreatedAt == it.UpdatedAt {
		t.Errorf("CreatedAt and UpdatedAt collapsed to the same value %d — creation time is not distinguishable from last-modified", it.CreatedAt)
	}
	if it.Status != state.StatusDone {
		t.Fatalf("status = %q, want done (mutation sequence sanity check)", it.Status)
	}

	// Convergence: the fix must not depend on append order — every permutation of
	// the same event set must recover the identical CreatedAt/UpdatedAt.
	for seed := int64(0); seed < 50; seed++ {
		got := ProjectItems(permute(all, seed), opts)[itemID]
		if got == nil {
			t.Fatalf("seed=%d: item missing after permutation", seed)
		}
		if got.CreatedAt != wantCreated {
			t.Fatalf("seed=%d: CreatedAt = %d, want %d", seed, got.CreatedAt, wantCreated)
		}
		if got.UpdatedAt != wantUpdated {
			t.Fatalf("seed=%d: UpdatedAt = %d, want %d", seed, got.UpdatedAt, wantUpdated)
		}
	}
}

// TestProjection_CreatedAtSubsetSafe is the adversary's fatal probe (ready-4ec
// rework): a machine bootstrapped via `rd join` pulls from relays, which retain
// ONLY the latest addressable 30302 card per item (NIP-33) — never historical
// card revisions. A DERIVED min()-over-admitted-events CreatedAt disagreed
// between the full local log (which still has every past card) and that
// relay-bootstrapped subset (newest card + status events only), because the
// subset's minimum created_at was necessarily later than the full set's. The
// CARRIED "created" tag on the winning card has no such dependency: whatever
// subset holds the item's current card holds the identical tag value.
func TestProjection_CreatedAtSubsetSafe(t *testing.T) {
	k := testKey(t)
	itemID := "ready-subset-1"
	opts := ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}}

	// Full history: genesis @1000, claim @2000, a later card-only edit @3000 that
	// (like production) carries the genesis CreatedAt forward explicitly.
	c0, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v0", Status: state.StatusInbox, Priority: "p1", BoardD: "ready"}, 1000)
	s0, _ := BuildStatusEvent(k, itemID, state.StatusInbox, c0.ID, "", 1000)
	c1, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v1", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready", CreatedAt: 1000}, 2000)
	s1, _ := BuildStatusEvent(k, itemID, state.StatusActive, c1.ID, "claimed", 2000)
	c2, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v2 latest", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready", CreatedAt: 1000}, 3000)

	fullSet := []*nostr.Event{c0, s0, c1, s1, c2}
	// Relay-bootstrapped SUBSET: only the newest card (c2) + status events (s0,
	// s1) — the historical cards c0/c1 are ABSENT, matching what a relay actually
	// retains (latest-wins addressable event only) and what `rd join` pulls down.
	subset := []*nostr.Event{c2, s0, s1}

	full := summarize(t, fullSet, opts, itemID)
	if full.title != "v2 latest" {
		t.Fatalf("full-set winning title = %q, want v2 latest (sanity check)", full.title)
	}

	fullItem := ProjectItems(fullSet, opts)[itemID]
	subItem := ProjectItems(subset, opts)[itemID]
	if fullItem == nil || subItem == nil {
		t.Fatalf("item missing: full=%v subset=%v", fullItem, subItem)
	}
	wantCreated := int64(1000) * int64(time.Second)
	if fullItem.CreatedAt != wantCreated {
		t.Fatalf("full-set CreatedAt = %d, want %d", fullItem.CreatedAt, wantCreated)
	}
	if subItem.CreatedAt != fullItem.CreatedAt {
		t.Errorf("SUBSET-SENSITIVITY: relay-subset CreatedAt = %d, full-set CreatedAt = %d — two machines holding different subsets of the same board disagree about this item's creation time", subItem.CreatedAt, fullItem.CreatedAt)
	}
}

// TestProjection_BackdatedNonAuthorityStatusIgnored is the adversary's spec
// probe (ready-4ec rework, §6.4/§19.8): a status event from neither the item
// author nor a board maintainer is NON-authoritative and must contribute
// NEITHER state NOR history — including, specifically, CreatedAt. Under the old
// firstSeen/min() mechanism this event's early created_at (BACKDATED before the
// card's own genesis) would still lower CreatedAt, because firstSeen folded
// every admitted event (status-authority-blind) before the authority filter ran.
// Under the carried-tag mechanism no status event is ever consulted for
// CreatedAt at all, so this is structurally impossible — this test pins that.
func TestProjection_BackdatedNonAuthorityStatusIgnored(t *testing.T) {
	author := testKey(t)
	stranger := testKey(t) // NOT a maintainer, NOT the card author
	itemID := "ready-backdated-1"
	opts := ProjectOptions{Maintainers: map[string]bool{author.PubKeyHex(): true}}

	// Card created @2000.
	c0, _ := BuildCardEvent(author, CardSpec{ItemID: itemID, Title: "v0", Status: state.StatusActive, Priority: "p1", BoardD: "ready"}, 2000)
	sAuthor, _ := BuildStatusEvent(author, itemID, state.StatusActive, c0.ID, "claimed", 2000)

	// A NON-authoritative status event, BACKDATED to @500 — well before the
	// card's own created_at (2000) — signed by a stranger who is neither the
	// author nor a maintainer.
	sBackdated, _ := BuildStatusEvent(stranger, itemID, state.StatusActive, c0.ID, "spoofed", 500)

	withoutStranger := ProjectItems([]*nostr.Event{c0, sAuthor}, opts)[itemID]
	withStranger := ProjectItems([]*nostr.Event{c0, sAuthor, sBackdated}, opts)[itemID]
	if withoutStranger == nil || withStranger == nil {
		t.Fatalf("item missing: without=%v with=%v", withoutStranger, withStranger)
	}
	wantCreated := int64(2000) * int64(time.Second)
	if withoutStranger.CreatedAt != wantCreated {
		t.Fatalf("baseline CreatedAt = %d, want %d", withoutStranger.CreatedAt, wantCreated)
	}
	if withStranger.CreatedAt != wantCreated {
		t.Errorf("SPEC VIOLATION: a backdated NON-authoritative status event changed CreatedAt from %d to %d — a non-authority must contribute neither state nor history (§6.4/§19.8)", wantCreated, withStranger.CreatedAt)
	}
	// The stranger's event must also be entirely absent from history (already
	// covered elsewhere, re-asserted here for locality with the CreatedAt claim).
	if len(withStranger.History) != len(withoutStranger.History) {
		t.Errorf("non-authoritative status event leaked into history: len=%d, want %d", len(withStranger.History), len(withoutStranger.History))
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
