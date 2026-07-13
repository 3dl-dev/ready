package playbook

import (
	"path/filepath"
	"testing"
)

// mkTemplate builds a minimal valid template for store tests.
func mkTemplate(t *testing.T, id, title string, items []TemplateItem) *PlaybookTemplate {
	t.Helper()
	tmpl := &PlaybookTemplate{ID: id, Title: title, Items: items}
	if err := tmpl.Validate(); err != nil {
		t.Fatalf("mkTemplate %q invalid: %v", id, err)
	}
	return tmpl
}

// TestStore_AddListRoundTrip proves a store-free playbook template round-trips
// through the append-only JSONL file: Add writes it, List reads it back with all
// fields (including dep edges) intact, sorted by ID.
func TestStore_AddListRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ready", "playbooks.jsonl")
	s := NewStore(path)

	// Empty store lists nothing.
	if got, err := s.List(); err != nil || len(got) != 0 {
		t.Fatalf("List on empty store = (%v, %v); want (nil, nil)", got, err)
	}

	sre := mkTemplate(t, "sre-incident", "SRE Incident", []TemplateItem{
		{Title: "Triage", Type: "task", Priority: "p0"},
		{Title: "Postmortem", Type: "task", Priority: "p2", Deps: []int{0}},
	})
	alpha := mkTemplate(t, "alpha", "Alpha", []TemplateItem{
		{Title: "Only", Type: "task", Priority: "p1"},
	})

	if err := s.Add(sre); err != nil {
		t.Fatalf("Add sre: %v", err)
	}
	if err := s.Add(alpha); err != nil {
		t.Fatalf("Add alpha: %v", err)
	}

	got, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List returned %d templates; want 2", len(got))
	}
	// Sorted by ID: alpha before sre-incident.
	if got[0].ID != "alpha" || got[1].ID != "sre-incident" {
		t.Fatalf("List order = [%s, %s]; want [alpha, sre-incident]", got[0].ID, got[1].ID)
	}
	// Dep edge survived the round-trip.
	if len(got[1].Items) != 2 || len(got[1].Items[1].Deps) != 1 || got[1].Items[1].Deps[0] != 0 {
		t.Fatalf("sre-incident dep edge lost on round-trip: %+v", got[1].Items)
	}
}

// TestStore_LatestWins proves that re-Adding the same playbook ID appends a new
// record and List returns the most recent one (append-only, latest-wins).
func TestStore_LatestWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ready", "playbooks.jsonl")
	s := NewStore(path)

	if err := s.Add(mkTemplate(t, "dup", "First", []TemplateItem{
		{Title: "A", Type: "task", Priority: "p1"},
	})); err != nil {
		t.Fatalf("Add first: %v", err)
	}
	if err := s.Add(mkTemplate(t, "dup", "Second", []TemplateItem{
		{Title: "B", Type: "task", Priority: "p2"},
		{Title: "C", Type: "task", Priority: "p3"},
	})); err != nil {
		t.Fatalf("Add second: %v", err)
	}

	all, err := s.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("List after re-add = %d templates; want 1 (latest-wins collapses the ID)", len(all))
	}
	if all[0].Title != "Second" || len(all[0].Items) != 2 {
		t.Fatalf("latest-wins failed: got title=%q items=%d; want Second/2", all[0].Title, len(all[0].Items))
	}
}

// TestStore_Find proves Find returns the template by ID (latest-wins) and errors
// on an unknown ID.
func TestStore_Find(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".ready", "playbooks.jsonl")
	s := NewStore(path)
	if err := s.Add(mkTemplate(t, "found", "Found", []TemplateItem{
		{Title: "A", Type: "task", Priority: "p1"},
	})); err != nil {
		t.Fatalf("Add: %v", err)
	}
	pb, err := s.Find("found")
	if err != nil {
		t.Fatalf("Find(found): %v", err)
	}
	if pb.Title != "Found" {
		t.Fatalf("Find(found).Title = %q; want Found", pb.Title)
	}
	if _, err := s.Find("missing"); err == nil {
		t.Fatalf("Find(missing) = nil error; want not-found error")
	}
}
