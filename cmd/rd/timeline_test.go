package main

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// TestParseProgressNotes_ExtractsTimestampedBlocks verifies that timestamped
// progress blocks are extracted and the leading (un-timestamped) description is
// skipped. This mirrors exactly how `rd progress` appends notes to the context.
func TestParseProgressNotes_ExtractsTimestampedBlocks(t *testing.T) {
	ctx := "Original description of the item.\n\n" +
		"[2026-07-16T10:00Z] Started work on the parser.\n\n" +
		"[2026-07-16T14:30Z] Parser done, moving to validation."

	notes := parseProgressNotes(ctx)

	if len(notes) != 2 {
		t.Fatalf("expected 2 progress notes, got %d: %+v", len(notes), notes)
	}
	if notes[0].Timestamp != "2026-07-16T10:00Z" {
		t.Errorf("note[0] timestamp=%q, want 2026-07-16T10:00Z", notes[0].Timestamp)
	}
	if notes[0].Note != "Started work on the parser." {
		t.Errorf("note[0] text=%q", notes[0].Note)
	}
	if notes[1].Note != "Parser done, moving to validation." {
		t.Errorf("note[1] text=%q", notes[1].Note)
	}
	for _, n := range notes {
		if n.Kind != logKindProgress {
			t.Errorf("note kind=%q, want %q", n.Kind, logKindProgress)
		}
	}
}

// TestParseProgressNotes_NoNotes verifies that context with no timestamped blocks
// yields no progress notes (the leading description is not a progress note).
func TestParseProgressNotes_NoNotes(t *testing.T) {
	if got := parseProgressNotes("Just a plain description."); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
	if got := parseProgressNotes(""); got != nil {
		t.Errorf("expected nil for empty context, got %+v", got)
	}
}

// TestParseProgressNotes_MultiParagraphNote verifies that a progress note whose
// own text contains a blank line is reassembled as one note, not split.
func TestParseProgressNotes_MultiParagraphNote(t *testing.T) {
	ctx := "[2026-07-16T10:00Z] First paragraph.\n\nSecond paragraph of the same note."
	notes := parseProgressNotes(ctx)
	if len(notes) != 1 {
		t.Fatalf("expected 1 note, got %d: %+v", len(notes), notes)
	}
	want := "First paragraph.\n\nSecond paragraph of the same note."
	if notes[0].Note != want {
		t.Errorf("note text=%q, want %q", notes[0].Note, want)
	}
}

// TestBuildTimeline_MergesAndSorts verifies that status history and progress notes
// are merged into ONE chronologically-ordered timeline.
func TestBuildTimeline_MergesAndSorts(t *testing.T) {
	item := &state.Item{
		ID:    "ready-tl1",
		Title: "Timeline item",
		History: []state.HistoryEntry{
			{Timestamp: "2026-07-16T09:00:00Z", FromStatus: "inbox", ToStatus: "inbox", ChangedBy: "sys", Note: "created"},
			{Timestamp: "2026-07-16T11:00:00Z", FromStatus: "inbox", ToStatus: "active", ChangedBy: "atlas"},
			{Timestamp: "2026-07-16T16:00:00Z", FromStatus: "active", ToStatus: "done", ChangedBy: "atlas", Note: "shipped"},
		},
		Context: "The task.\n\n" +
			"[2026-07-16T10:00Z] investigating\n\n" +
			"[2026-07-16T14:30Z] fix identified",
	}

	entries := buildTimeline(item)

	if len(entries) != 5 {
		t.Fatalf("expected 5 timeline entries (3 status + 2 progress), got %d", len(entries))
	}

	// Verify chronological order by the raw timestamps.
	wantOrder := []struct {
		kind string
		ts   string
	}{
		{logKindStatus, "2026-07-16T09:00:00Z"},
		{logKindProgress, "2026-07-16T10:00Z"},
		{logKindStatus, "2026-07-16T11:00:00Z"},
		{logKindProgress, "2026-07-16T14:30Z"},
		{logKindStatus, "2026-07-16T16:00:00Z"},
	}
	for i, w := range wantOrder {
		if entries[i].Kind != w.kind {
			t.Errorf("entry[%d] kind=%q, want %q", i, entries[i].Kind, w.kind)
		}
		if entries[i].Timestamp != w.ts {
			t.Errorf("entry[%d] ts=%q, want %q", i, entries[i].Timestamp, w.ts)
		}
	}

	// Spot-check status-entry field mapping.
	if entries[2].From != "inbox" || entries[2].To != "active" || entries[2].By != "atlas" {
		t.Errorf("status entry fields wrong: %+v", entries[2])
	}
	// Spot-check progress-entry note mapping.
	if entries[1].Note != "investigating" {
		t.Errorf("progress entry note=%q, want investigating", entries[1].Note)
	}
}

// TestBuildTimeline_Empty verifies a bare item (no history, no progress notes)
// yields an empty timeline rather than panicking.
func TestBuildTimeline_Empty(t *testing.T) {
	item := &state.Item{ID: "ready-empty", Title: "Empty"}
	if got := buildTimeline(item); len(got) != 0 {
		t.Errorf("expected empty timeline, got %+v", got)
	}
}

// TestBuildTimeline_HistoryOnly verifies that an item with history but no progress
// notes produces exactly its status entries.
func TestBuildTimeline_HistoryOnly(t *testing.T) {
	item := &state.Item{
		ID: "ready-h",
		History: []state.HistoryEntry{
			{Timestamp: "2026-07-16T09:00:00Z", FromStatus: "inbox", ToStatus: "inbox", ChangedBy: "sys", Note: "created"},
		},
		Context: "Plain description, no progress notes.",
	}
	entries := buildTimeline(item)
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Kind != logKindStatus {
		t.Errorf("kind=%q, want status", entries[0].Kind)
	}
}
