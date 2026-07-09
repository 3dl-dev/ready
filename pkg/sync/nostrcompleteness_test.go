package sync

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
)

// fullItem builds a source item that populates EVERY field the migration must
// carry — including the ready-187 additions (Level, For, ParentID, Due) plus
// labels/eta/assignee — so a round-trip through the wire mapping and back proves
// nothing is silently dropped.
func fullItem() *state.Item {
	t0 := time.Date(2026, 7, 2, 9, 0, 0, 0, time.UTC)
	return &state.Item{
		ID:        "ready-full",
		Title:     "full item",
		Context:   "a rich description",
		Type:      "task",
		Status:    state.StatusActive,
		Priority:  "p1",
		By:        "baron@3dl.dev",
		For:       "atlas/worker-7",
		Level:     "human",
		ParentID:  "ready-epic",
		ETA:       t0.Add(24 * time.Hour).Format(time.RFC3339),
		Due:       t0.Add(48 * time.Hour).Format(time.RFC3339),
		Labels:    []string{"security", "backend"},
		CreatedAt: t0.UnixNano(),
		UpdatedAt: t0.Add(time.Hour).UnixNano(),
		History: []state.HistoryEntry{
			{Timestamp: t0.Format(time.RFC3339), FromStatus: "", ToStatus: state.StatusInbox, ChangedBy: "baron@3dl.dev", Note: "created"},
			{Timestamp: t0.Add(time.Hour).Format(time.RFC3339), FromStatus: state.StatusInbox, ToStatus: state.StatusActive, ChangedBy: "atlas/worker-7"},
		},
	}
}

// TestMigration_CarriesAllFields is the completeness proof (ready-187): a single
// item that populates Level/For/ParentID/Due (and labels/eta/assignee) must survive
// the migration round-trip item-for-item. Before the CardSpec carried these tags
// they projected back empty and STRICT CompareItem flags the loss.
func TestMigration_CarriesAllFields(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	src := fullItem()
	events := migrateToEvents(t, k, "ready", map[string]*state.Item{src.ID: src})
	trusted := map[string]bool{k.PubKeyHex(): true}
	projected := ProjectItems(events, ProjectOptions{Maintainers: trusted, Trusted: trusted})

	p := projected[src.ID]
	if p == nil {
		t.Fatalf("item lost from projection")
	}
	if p.Level != src.Level {
		t.Errorf("Level lost: got %q want %q", p.Level, src.Level)
	}
	if p.For != src.For {
		t.Errorf("For lost: got %q want %q", p.For, src.For)
	}
	if p.ParentID != src.ParentID {
		t.Errorf("ParentID lost: got %q want %q", p.ParentID, src.ParentID)
	}
	if p.Due != src.Due {
		t.Errorf("Due lost: got %q want %q", p.Due, src.Due)
	}
	if par := CompareItem(src, p); !par.Match() {
		t.Fatalf("strict parity failed on a fully-populated item: %v", par.Diffs)
	}
}

// TestCompareItem_DetectsEachField proves the STRICT checker is not vacuous: it must
// flag a silent alteration in EVERY newly-covered field. For each field we take a
// perfect projection, mutate exactly one field, and require CompareItem to report a
// diff naming that field.
func TestCompareItem_DetectsEachField(t *testing.T) {
	base := func() *state.Item {
		it := fullItem()
		// clone-ish: fullItem returns a fresh struct each call, so base() and the
		// mutated copy are independent.
		return it
	}
	cases := []struct {
		field  string
		mutate func(*state.Item)
	}{
		{"title", func(i *state.Item) { i.Title = "changed" }},
		{"context", func(i *state.Item) { i.Context = "changed" }},
		{"status", func(i *state.Item) { i.Status = state.StatusDone }},
		{"priority", func(i *state.Item) { i.Priority = "p0" }},
		{"type", func(i *state.Item) { i.Type = "bug" }},
		{"deps", func(i *state.Item) { i.BlockedBy = []string{"ready-x"} }},
		{"labels", func(i *state.Item) { i.Labels = []string{"different"} }},
		{"eta", func(i *state.Item) { i.ETA = "2030-01-01T00:00:00Z" }},
		{"assignee", func(i *state.Item) { i.By = "someone-else" }},
		{"level", func(i *state.Item) { i.Level = "agent" }},
		{"for", func(i *state.Item) { i.For = "other-party" }},
		{"parent", func(i *state.Item) { i.ParentID = "ready-other-epic" }},
		{"due", func(i *state.Item) { i.Due = "2030-01-01T00:00:00Z" }},
		{"history_len", func(i *state.Item) { i.History = i.History[:1] }},
		{"close_reasons", func(i *state.Item) { i.History[0].Note = "tampered" }},
		{"provenance", func(i *state.Item) { i.History[0].ChangedBy = "impostor" }},
	}
	for _, tc := range cases {
		t.Run(tc.field, func(t *testing.T) {
			source := base()
			projected := base()
			tc.mutate(projected)
			par := CompareItem(source, projected)
			if par.Match() {
				t.Fatalf("CompareItem did NOT detect an alteration in %q", tc.field)
			}
			found := false
			for _, d := range par.Diffs {
				if len(d) >= len(tc.field) && d[:len(tc.field)] == tc.field {
					found = true
				}
			}
			if !found {
				t.Fatalf("alteration in %q not reported by that field name; diffs=%v", tc.field, par.Diffs)
			}
		})
	}
}

// TestReadAll_SkipsCorruptLine proves the durability invariant (ready-187): a single
// malformed/truncated line does NOT take the whole log down. ReadAll must skip the
// bad line and keep replaying the good ones.
func TestReadAll_SkipsCorruptLine(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	path := t.TempDir() + "/nostr-log.jsonl"
	log := NewNostrLog(path)

	// Two good events...
	good := migrateToEvents(t, k, "ready", map[string]*state.Item{"ready-full": fullItem()})
	if _, err := log.AppendUnique(good); err != nil {
		t.Fatalf("append good: %v", err)
	}
	// ...then splice a corrupt line in the MIDDLE and append another good event after.
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := f.WriteString("{ this is not valid json \n"); err != nil {
		t.Fatalf("write corrupt: %v", err)
	}
	f.Close()

	other := fullItem()
	other.ID = "ready-second"
	more := migrateToEvents(t, k, "ready", map[string]*state.Item{other.ID: other})
	// AppendUnique itself must survive the corrupt line already on disk.
	if _, err := log.AppendUnique(more); err != nil {
		t.Fatalf("append after corrupt: %v", err)
	}

	events, corrupt, err := log.ReadAllReport()
	if err != nil {
		t.Fatalf("ReadAllReport errored on a single bad line: %v", err)
	}
	if corrupt != 1 {
		t.Fatalf("expected exactly 1 corrupt line reported, got %d", corrupt)
	}
	// Both good items must still project.
	trusted := map[string]bool{k.PubKeyHex(): true}
	projected := ProjectItems(events, ProjectOptions{Maintainers: trusted, Trusted: trusted})
	if projected["ready-full"] == nil || projected["ready-second"] == nil {
		t.Fatalf("good items lost after skipping the corrupt line: %v", keysOf(projected))
	}
}

// TestAppendUnique_RaceSafe proves two concurrent AppendUnique calls of the SAME
// event set never produce a duplicate line (run under -race). Without the single
// lock held across read+decide+write, both callers read an empty log, both decide
// the event is novel, and both append it.
func TestAppendUnique_RaceSafe(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	path := t.TempDir() + "/nostr-log.jsonl"

	// Build a batch of distinct events once; every goroutine tries to append the
	// SAME batch, so a correct AppendUnique writes each event exactly once total.
	const nItems = 40
	events := make([]*nostr.Event, 0, nItems)
	for i := 0; i < nItems; i++ {
		it := fullItem()
		it.ID = fmt.Sprintf("ready-r%02d", i)
		evs, err := BuildItemMigrationEvents(k, "ready", it)
		if err != nil {
			t.Fatalf("build %d: %v", i, err)
		}
		events = append(events, evs...)
	}

	const workers = 8
	var wg sync.WaitGroup
	errs := make([]error, workers)
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			// Each goroutine gets its own NostrLog handle on the SAME path (mirrors
			// separate processes / independent callers).
			log := NewNostrLog(path)
			_, errs[idx] = log.AppendUnique(events)
		}(w)
	}
	wg.Wait()
	for i, e := range errs {
		if e != nil {
			t.Fatalf("worker %d append error: %v", i, e)
		}
	}

	// The log must contain each event id EXACTLY once — no duplicates from the race.
	final, corrupt, err := NewNostrLog(path).ReadAllReport()
	if err != nil {
		t.Fatalf("readall: %v", err)
	}
	if corrupt != 0 {
		t.Fatalf("race produced %d corrupt/torn line(s)", corrupt)
	}
	counts := map[string]int{}
	for _, e := range final {
		counts[e.ID]++
	}
	for id, c := range counts {
		if c != 1 {
			t.Fatalf("event %s appended %d times — AppendUnique is not race-safe", id, c)
		}
	}
	if len(counts) != len(events) {
		t.Fatalf("expected %d unique events, got %d", len(events), len(counts))
	}
}

func keysOf(m map[string]*state.Item) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
