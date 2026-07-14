package sync

import (
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// sampleItemSet builds a small but representative EXISTING campfire item set: an
// item with a multi-entry audit trail and a close-reason, a blocker→blocked dep
// pair, and a gated (waiting) item. The actors on the history entries are
// DELIBERATELY not the signer, so a passing parity proves provenance is carried
// through the migration rather than collapsed onto the portfolio key.
func sampleItemSet() map[string]*state.Item {
	const (
		baron  = "baron@3dl.dev"
		atlas  = "atlas/worker-3"
		system = "system"
	)
	t0 := time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)
	sec := func(off int) string { return t0.Add(time.Duration(off) * time.Minute).Format(time.RFC3339) }
	nano := func(off int) int64 { return t0.Add(time.Duration(off) * time.Minute).UnixNano() }

	blocker := &state.Item{
		ID: "ready-aaa", Title: "blocker", Type: "task", Priority: "p1",
		Status: state.StatusDone, CreatedAt: nano(0), UpdatedAt: nano(30),
		History: []state.HistoryEntry{
			{Timestamp: sec(0), FromStatus: "", ToStatus: state.StatusInbox, ChangedBy: baron, Note: "created"},
			{Timestamp: sec(10), FromStatus: state.StatusInbox, ToStatus: state.StatusActive, ChangedBy: atlas},
			{Timestamp: sec(30), FromStatus: state.StatusActive, ToStatus: state.StatusDone, ChangedBy: atlas, Note: "blocker complete"},
		},
	}
	// blocked depends on blocker; blocker is done, so blocked is NOT blocked — its
	// current status is active, and the dep edge is still recorded (BlockedBy).
	blocked := &state.Item{
		ID: "ready-bbb", Title: "blocked", Type: "task", Priority: "p2",
		Status: state.StatusActive, CreatedAt: nano(5), UpdatedAt: nano(20),
		BlockedBy: []string{"ready-aaa"},
		History: []state.HistoryEntry{
			{Timestamp: sec(5), FromStatus: "", ToStatus: state.StatusInbox, ChangedBy: baron, Note: "created"},
			{Timestamp: sec(20), FromStatus: state.StatusInbox, ToStatus: state.StatusActive, ChangedBy: baron},
		},
	}
	// gated item waiting on a human gate.
	gated := &state.Item{
		ID: "ready-ccc", Title: "gated", Type: "decision", Priority: "p0",
		Status: state.StatusWaiting, WaitingType: "gate", WaitingOn: "needs sign-off",
		Gate: "design", CreatedAt: nano(2), UpdatedAt: nano(15),
		History: []state.HistoryEntry{
			{Timestamp: sec(2), FromStatus: "", ToStatus: state.StatusInbox, ChangedBy: baron, Note: "created"},
			{Timestamp: sec(8), FromStatus: state.StatusInbox, ToStatus: state.StatusActive, ChangedBy: baron},
			{Timestamp: sec(15), FromStatus: state.StatusActive, ToStatus: state.StatusWaiting, ChangedBy: system, Note: "confirm approach"},
		},
	}
	return map[string]*state.Item{blocker.ID: blocker, blocked.ID: blocked, gated.ID: gated}
}

// migrateToEvents runs the migration builder over a source set and returns the
// full signed-event stream (board once + per-item card + history events).
func migrateToEvents(t *testing.T, k *nostr.Key, boardD string, src map[string]*state.Item) []*nostr.Event {
	t.Helper()
	board, err := BuildBoardEvent(k, BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{k.PubKeyHex()}}, 1)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	events := []*nostr.Event{board}
	for _, item := range src {
		evs, err := BuildItemMigrationEvents(k, boardD, item)
		if err != nil {
			t.Fatalf("migrate %s: %v", item.ID, err)
		}
		events = append(events, evs...)
	}
	return events
}

// TestMigration_ItemForItemParity is the failing-first parity proof (ready-d65):
// re-emit an existing campfire item set as nostr events, project it back, and
// assert item-for-item parity on count, status, priority, type, deps, gate,
// history length + close-reasons, and provenance. It fails before the migration
// preserves provenance (the "by" tag) and current-status/deps materialization.
func TestMigration_ItemForItemParity(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	src := sampleItemSet()
	events := migrateToEvents(t, k, "ready", src)

	trusted := map[string]bool{k.PubKeyHex(): true}
	projected := ProjectItems(events, ProjectOptions{Maintainers: trusted, Trusted: trusted})

	rep := CompareItemSets(src, projected)
	if !rep.AllMatch() {
		for _, ip := range rep.Items {
			if !ip.Match() {
				t.Errorf("item %s diffs: %v", ip.ItemID, ip.Diffs)
			}
		}
		t.Fatalf("parity failed: source=%d projected=%d matched=%d mismatched=%d",
			rep.SourceCount, rep.ProjectedCount, rep.Matched, rep.Mismatched)
	}

	// Provenance MUST be the original actor, not the signer — the whole point of
	// the "by" tag carry. Assert the blocker's audit trail names atlas/baron, not
	// the portfolio pubkey.
	pb := projected["ready-aaa"]
	if pb == nil {
		t.Fatalf("ready-aaa missing from projection")
	}
	sawAtlas, sawBaron, sawSigner := false, false, false
	for _, h := range pb.History {
		switch h.ChangedBy {
		case "atlas/worker-3":
			sawAtlas = true
		case "baron@3dl.dev":
			sawBaron = true
		case k.PubKeyHex():
			sawSigner = true
		}
	}
	if !sawAtlas || !sawBaron {
		t.Errorf("provenance lost: history actors=%v want atlas + baron", actorsOf(pb.History))
	}
	if sawSigner {
		t.Errorf("provenance collapsed onto signer pubkey %s: %v", k.PubKeyHex(), actorsOf(pb.History))
	}

	// The close-reason must survive verbatim on the terminal transition.
	if got := lastNote(pb.History); got != "blocker complete" {
		t.Errorf("close-reason lost: got %q want %q", got, "blocker complete")
	}

	// The gated item must project as waiting-on-a-gate (current-state materialized).
	gc := projected["ready-ccc"]
	if gc == nil || gc.Status != state.StatusWaiting {
		t.Fatalf("gated item lost its waiting status: %+v", gc)
	}
	if gc.GateMsgID == "" {
		t.Errorf("gated item lost its gate (GateMsgID empty) — GatesFilter would miss it")
	}
}

// TestMigration_Idempotent proves a re-run adds nothing: the same source
// re-migrated yields byte-identical event ids, so AppendUnique drops every repeat
// and the projection is unchanged. No item forks or duplicates its history.
func TestMigration_Idempotent(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	src := sampleItemSet()

	log := NewNostrLog(t.TempDir() + "/nostr-log.jsonl")
	first := migrateToEvents(t, k, "ready", src)
	n1, err := log.AppendUnique(first)
	if err != nil {
		t.Fatalf("append1: %v", err)
	}
	if n1 == 0 {
		t.Fatalf("first migration appended nothing")
	}
	second := migrateToEvents(t, k, "ready", src)
	n2, err := log.AppendUnique(second)
	if err != nil {
		t.Fatalf("append2: %v", err)
	}
	if n2 != 0 {
		t.Fatalf("re-run was not idempotent: appended %d duplicate events", n2)
	}

	all, err := log.ReadAll()
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	trusted := map[string]bool{k.PubKeyHex(): true}
	projected := ProjectItems(all, ProjectOptions{Maintainers: trusted, Trusted: trusted})
	rep := CompareItemSets(src, projected)
	if !rep.AllMatch() {
		t.Fatalf("parity broke after idempotent re-run: mismatched=%d", rep.Mismatched)
	}
}

// TestMigration_DetectsLostItem proves the parity check is not vacuous: dropping
// one item from the projection is reported as a LOST item and fails AllMatch.
func TestMigration_DetectsLostItem(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	src := sampleItemSet()
	events := migrateToEvents(t, k, "ready", src)
	trusted := map[string]bool{k.PubKeyHex(): true}
	projected := ProjectItems(events, ProjectOptions{Maintainers: trusted, Trusted: trusted})
	delete(projected, "ready-bbb") // simulate a lost item

	rep := CompareItemSets(src, projected)
	if rep.AllMatch() {
		t.Fatalf("parity check failed to detect a lost item")
	}
	found := false
	for _, ip := range rep.Items {
		if ip.ItemID == "ready-bbb" && !ip.Match() {
			found = true
		}
	}
	if !found {
		t.Fatalf("lost item ready-bbb not flagged in parity report")
	}
}

func actorsOf(hist []state.HistoryEntry) []string {
	out := make([]string, 0, len(hist))
	for _, h := range hist {
		out = append(out, h.ChangedBy)
	}
	return out
}

func lastNote(hist []state.HistoryEntry) string {
	for i := len(hist) - 1; i >= 0; i-- {
		if hist[i].Note != "" {
			return hist[i].Note
		}
	}
	return ""
}
