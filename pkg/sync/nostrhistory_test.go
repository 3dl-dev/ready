// Deterministic unit tests for ready-b5f: `rd show` (nostr path) must replay the
// FULL audit history from the append-only log after edits, with close-with-reason
// preserved — not just the latest-wins card state.
package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestProjection_FullHistoryReplay is the ready-b5f core proof: create, claim,
// progress (card-only edit), title edit (card-only edit), and done --reason all
// land in the log; the replay must show EVERY status transition (not just the
// winning one), each with its own reason, and the card-only edits must not add or
// remove any history entries.
func TestProjection_FullHistoryReplay(t *testing.T) {
	k := testKey(t)
	itemID := "ready-hist-1"

	// 1. create -> card (inbox) + status event (inbox, no reason).
	c0, err := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v1", Status: state.StatusInbox, Priority: "p1", BoardD: "ready"}, 1700000000)
	if err != nil {
		t.Fatalf("build create card: %v", err)
	}
	s0, err := BuildStatusEvent(k, itemID, state.StatusInbox, c0.ID, "", 1700000000)
	if err != nil {
		t.Fatalf("build create status: %v", err)
	}

	// 2. claim -> refreshed card (active, assignee) + status event (inbox->active).
	c1, err := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v1", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), BoardD: "ready"}, 1700000100)
	if err != nil {
		t.Fatalf("build claim card: %v", err)
	}
	s1, err := BuildStatusEvent(k, itemID, state.StatusActive, c1.ID, "", 1700000100)
	if err != nil {
		t.Fatalf("build claim status: %v", err)
	}

	// 3. progress -> card-ONLY edit (context changed), NO status event.
	c2, err := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v1", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), Context: "progress note", BoardD: "ready"}, 1700000200)
	if err != nil {
		t.Fatalf("build progress card: %v", err)
	}

	// 4. edit -> another card-ONLY edit (title changed), NO status event. This is
	// the "editing the addressable card does NOT erase history" proof point.
	c3, err := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v2 (edited)", Status: state.StatusActive, Priority: "p1", Assignee: k.PubKeyHex(), Context: "progress note", BoardD: "ready"}, 1700000300)
	if err != nil {
		t.Fatalf("build edit card: %v", err)
	}

	// 5. done --reason -> refreshed card (done) + status event carrying the reason.
	c4, err := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v2 (edited)", Status: state.StatusDone, Priority: "p1", Assignee: k.PubKeyHex(), Context: "progress note", BoardD: "ready"}, 1700000400)
	if err != nil {
		t.Fatalf("build done card: %v", err)
	}
	s2, err := BuildStatusEvent(k, itemID, state.StatusDone, c4.ID, "implemented and merged", 1700000400)
	if err != nil {
		t.Fatalf("build done status: %v", err)
	}

	events := []*nostr.Event{c0, s0, c1, s1, c2, c3, c4, s2}
	items := ProjectItems(events, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})

	it, ok := items[itemID]
	if !ok {
		t.Fatalf("item not projected")
	}

	// Current state reflects the LATEST card (edit survives) and the LATEST
	// status (done).
	if it.Title != "v2 (edited)" {
		t.Errorf("current title = %q, want %q (latest card should win)", it.Title, "v2 (edited)")
	}
	if it.Status != state.StatusDone {
		t.Errorf("current status = %q, want %q", it.Status, state.StatusDone)
	}

	// FULL history: exactly 3 authoritative status transitions (create, claim,
	// done) — the two card-only edits (progress, title edit) must NOT appear as
	// history entries and must NOT erase the earlier ones.
	if len(it.History) != 3 {
		t.Fatalf("history length = %d, want 3 (create, claim, done) — got %+v", len(it.History), it.History)
	}

	wantSeq := []struct {
		from, to, note string
	}{
		{"", state.StatusInbox, ""},
		{state.StatusInbox, state.StatusActive, ""},
		{state.StatusActive, state.StatusDone, "implemented and merged"},
	}
	for i, w := range wantSeq {
		h := it.History[i]
		if h.FromStatus != w.from || h.ToStatus != w.to {
			t.Errorf("history[%d] = %s->%s, want %s->%s", i, h.FromStatus, h.ToStatus, w.from, w.to)
		}
		if h.Note != w.note {
			t.Errorf("history[%d].Note = %q, want %q", i, h.Note, w.note)
		}
		if h.ChangedBy != k.PubKeyHex() {
			t.Errorf("history[%d].ChangedBy = %q, want %q", i, h.ChangedBy, k.PubKeyHex())
		}
	}

	// close-with-reason preserved EXACTLY in the terminal entry.
	if it.History[2].Note != "implemented and merged" {
		t.Errorf("close-with-reason not preserved: %q", it.History[2].Note)
	}

	// History is in chronological order (timestamps non-decreasing).
	for i := 1; i < len(it.History); i++ {
		if it.History[i].Timestamp < it.History[i-1].Timestamp {
			t.Errorf("history not chronological at index %d: %s < %s", i, it.History[i].Timestamp, it.History[i-1].Timestamp)
		}
	}
}

// TestProjection_CardEditDoesNotAffectHistory proves the hybrid-design invariant
// directly: publishing N additional card-only edits (no status events) after a
// close-with-reason leaves History byte-for-byte identical.
func TestProjection_CardEditDoesNotAffectHistory(t *testing.T) {
	k := testKey(t)
	itemID := "ready-hist-2"

	c0, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v1", Status: state.StatusActive, BoardD: "ready"}, 1700000000)
	s0, _ := BuildStatusEvent(k, itemID, state.StatusActive, c0.ID, "", 1700000000)
	c1, _ := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: "v1", Status: state.StatusDone, BoardD: "ready"}, 1700000100)
	s1, _ := BuildStatusEvent(k, itemID, state.StatusDone, c1.ID, "shipped", 1700000100)

	before := ProjectItems([]*nostr.Event{c0, s0, c1, s1}, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	beforeHist := before[itemID].History
	if len(beforeHist) != 2 {
		t.Fatalf("expected 2 history entries before edits, got %d", len(beforeHist))
	}

	// Pile on several card-only edits after the close.
	edits := []*nostr.Event{}
	for i, title := range []string{"edit-1", "edit-2", "edit-3"} {
		ce, err := BuildCardEvent(k, CardSpec{ItemID: itemID, Title: title, Status: state.StatusDone, BoardD: "ready"}, 1700000200+int64(i))
		if err != nil {
			t.Fatalf("build edit %d: %v", i, err)
		}
		edits = append(edits, ce)
	}

	all := append([]*nostr.Event{c0, s0, c1, s1}, edits...)
	after := ProjectItems(all, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	afterItem := after[itemID]

	if afterItem.Title != "edit-3" {
		t.Errorf("latest edit should win for current state: title = %q, want edit-3", afterItem.Title)
	}
	if len(afterItem.History) != len(beforeHist) {
		t.Fatalf("card-only edits changed history length: before=%d after=%d", len(beforeHist), len(afterItem.History))
	}
	for i := range beforeHist {
		if afterItem.History[i] != beforeHist[i] {
			t.Errorf("history[%d] changed by a card-only edit: before=%+v after=%+v", i, beforeHist[i], afterItem.History[i])
		}
	}
	// close-with-reason still intact after the edits.
	if afterItem.History[len(afterItem.History)-1].Note != "shipped" {
		t.Errorf("close-with-reason lost after card edits: %q", afterItem.History[len(afterItem.History)-1].Note)
	}
}
