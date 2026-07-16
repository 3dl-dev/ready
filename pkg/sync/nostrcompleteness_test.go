package sync

import (
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
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

// itemCardEvents is a minimal fixture builder: a board event plus one signed card
// event per item — enough valid signed events to exercise the log-durability paths.
func itemCardEvents(t *testing.T, k *nostr.Key, boardD string, src map[string]*state.Item) []*nostr.Event {
	t.Helper()
	board, err := BuildBoardEvent(k, BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{k.PubKeyHex()}}, 1)
	if err != nil {
		t.Fatalf("board: %v", err)
	}
	events := []*nostr.Event{board}
	ts := int64(2)
	for _, item := range src {
		ce, err := BuildCardEvent(k, CardSpecFromItem(item, boardD), ts)
		if err != nil {
			t.Fatalf("card %s: %v", item.ID, err)
		}
		events = append(events, ce)
		ts++
	}
	return events
}

// TestCardSpec_RoundTripsAllFields guards the live write-path invariant that the
// deleted migration test used to cover (ready review [9]): CardSpecFromItem ->
// BuildCardEvent -> ProjectItems must preserve Level/For/ParentID/Due/ETA/labels,
// because CardSpecFromItem materializes every rd create/claim/progress/close card.
// A future edit dropping one of these tags would silently lose the field on every
// republish; this test fails loudly instead.
func TestCardSpec_RoundTripsAllFields(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	src := &state.Item{
		ID:       "ready-rt",
		Title:    "round-trip item",
		Type:     "task",
		Status:   state.StatusActive,
		Priority: "p1",
		For:      "atlas/worker-7",
		Level:    "human",
		ParentID: "ready-epic",
		ETA:      "2026-07-03T09:00:00Z",
		Due:      "2026-07-04T09:00:00Z",
		Labels:   []string{"security", "bug"}, // seed atoms so the registry admits them
	}
	events := itemCardEvents(t, k, "ready", map[string]*state.Item{src.ID: src})
	trusted := map[string]bool{k.PubKeyHex(): true}
	got := ProjectItems(events, ProjectOptions{Maintainers: trusted, Trusted: trusted})[src.ID]
	if got == nil {
		t.Fatalf("item %s lost in projection", src.ID)
	}
	for _, c := range []struct{ name, got, want string }{
		{"Level", got.Level, src.Level},
		{"For", got.For, src.For},
		{"ParentID", got.ParentID, src.ParentID},
		{"ETA", got.ETA, src.ETA},
		{"Due", got.Due, src.Due},
	} {
		if c.got != c.want {
			t.Errorf("%s not preserved: got %q want %q", c.name, c.got, c.want)
		}
	}
	labels := map[string]bool{}
	for _, l := range got.Labels {
		labels[l] = true
	}
	for _, want := range src.Labels {
		if !labels[want] {
			t.Errorf("label %q not preserved (got %v)", want, got.Labels)
		}
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
	good := itemCardEvents(t, k, "ready", map[string]*state.Item{"ready-full": fullItem()})
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
	more := itemCardEvents(t, k, "ready", map[string]*state.Item{other.ID: other})
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
	src := make(map[string]*state.Item, nItems)
	for i := 0; i < nItems; i++ {
		it := fullItem()
		it.ID = fmt.Sprintf("ready-r%02d", i)
		src[it.ID] = it
	}
	events := itemCardEvents(t, k, "ready", src)

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
