package main

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// TestMatchesSearch_Title verifies a case-insensitive title match.
func TestMatchesSearch_Title(t *testing.T) {
	item := &state.Item{ID: "ready-1", Title: "Fix the Auth Bug"}
	if !matchesSearch(item, "auth") {
		t.Error("expected match on 'auth' in title")
	}
	if !matchesSearch(item, "auth bug") {
		t.Error("expected match on multi-word substring")
	}
	if matchesSearch(item, "payment") {
		t.Error("did not expect match on 'payment'")
	}
}

// TestMatchesSearch_Context verifies matching against the context/description.
func TestMatchesSearch_Context(t *testing.T) {
	item := &state.Item{ID: "ready-1", Title: "Task", Context: "We tried the retry approach and it deadlocked."}
	if !matchesSearch(item, "deadlock") {
		t.Error("expected match on 'deadlock' in context")
	}
	if !matchesSearch(item, "RETRY") {
		t.Error("expected case-insensitive match on 'RETRY'")
	}
}

// TestMatchesSearch_HistoryNotes verifies matching against history notes.
func TestMatchesSearch_HistoryNotes(t *testing.T) {
	item := &state.Item{
		ID:    "ready-1",
		Title: "Task",
		History: []state.HistoryEntry{
			{Timestamp: "2026-07-16T09:00:00Z", ToStatus: "failed", Note: "OOM killed the worker"},
		},
	}
	if !matchesSearch(item, "oom") {
		t.Error("expected match on 'oom' in history note")
	}
	if matchesSearch(item, "nonexistent") {
		t.Error("did not expect match on 'nonexistent'")
	}
}

// TestMatchesSearch_EmptyQueryMatchesAll verifies an empty query matches everything.
func TestMatchesSearch_EmptyQueryMatchesAll(t *testing.T) {
	item := &state.Item{ID: "ready-1", Title: "anything"}
	if !matchesSearch(item, "") {
		t.Error("empty query should match all items")
	}
}

// TestFilterBySearch_ReturnsMatchingSubset verifies the slice-level filter and
// its case-insensitivity across the full corpus.
func TestFilterBySearch_ReturnsMatchingSubset(t *testing.T) {
	items := []*state.Item{
		{ID: "ready-1", Title: "Auth service login"},
		{ID: "ready-2", Title: "Billing", Context: "handles AUTH tokens too"},
		{ID: "ready-3", Title: "Unrelated feature"},
		{ID: "ready-4", Title: "Task", History: []state.HistoryEntry{{Note: "auth regression fixed"}}},
	}
	got := filterBySearch(items, "auth")
	ids := map[string]bool{}
	for _, it := range got {
		ids[it.ID] = true
	}
	if len(got) != 3 {
		t.Fatalf("expected 3 matches (title, context, history), got %d: %v", len(got), ids)
	}
	if !ids["ready-1"] || !ids["ready-2"] || !ids["ready-4"] {
		t.Errorf("wrong match set: %v", ids)
	}
	if ids["ready-3"] {
		t.Error("ready-3 should not match 'auth'")
	}
}

// TestFilterBySearch_NoMatch verifies an empty result for a non-matching query.
func TestFilterBySearch_NoMatch(t *testing.T) {
	items := []*state.Item{{ID: "ready-1", Title: "hello"}}
	if got := filterBySearch(items, "zzz"); len(got) != 0 {
		t.Errorf("expected 0 matches, got %d", len(got))
	}
}

// TestValidateResolution verifies the outcome-filter value validation and its
// mapping to canonical terminal statuses.
func TestValidateResolution(t *testing.T) {
	cases := []struct {
		in      string
		want    string
		wantErr bool
	}{
		{"done", state.StatusDone, false},
		{"completed", state.StatusDone, false},
		{"failed", state.StatusFailed, false},
		{"cancelled", state.StatusCancelled, false},
		{"active", "", true},
		{"", "", true},
		{"bogus", "", true},
	}
	for _, c := range cases {
		got, err := validateResolution(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("validateResolution(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("validateResolution(%q): unexpected error %v", c.in, err)
		}
		if got != c.want {
			t.Errorf("validateResolution(%q)=%q, want %q", c.in, got, c.want)
		}
	}
}

// TestResolutionFilter_ThroughApplyListFilters verifies the end-to-end semantics:
// a validated resolution, appended as an explicit status, selects exactly the
// closed items with that outcome (and includes terminal items that the default
// view would otherwise exclude).
func TestResolutionFilter_ThroughApplyListFilters(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusActive),
		makeListItem("t2", state.StatusDone),
		makeListItem("t3", state.StatusFailed),
		makeListItem("t4", state.StatusCancelled),
	}
	canonical, err := validateResolution("failed")
	if err != nil {
		t.Fatalf("validateResolution: %v", err)
	}
	result := applyListFilters(items, []string{canonical}, "", "", "", "", "", false)
	if len(result) != 1 {
		t.Fatalf("expected 1 failed item, got %d", len(result))
	}
	if result[0].ID != "t3" {
		t.Errorf("expected t3 (failed), got %s", result[0].ID)
	}
}
