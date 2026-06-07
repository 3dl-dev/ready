package views_test

// Tests for views.LabelFilter (ready-9c4).

import (
	"testing"

	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/views"
)

// TestLabelFilter_MatchesCarriedAtom verifies that LabelFilter returns true
// when the item's Labels slice contains the requested atom (exact match).
func TestLabelFilter_MatchesCarriedAtom(t *testing.T) {
	item := &state.Item{ID: "t1", Labels: []string{"bug", "security"}}
	f := views.LabelFilter("bug")
	if !f(item) {
		t.Error("LabelFilter(\"bug\") must match item with Labels=[bug,security]")
	}
}

// TestLabelFilter_MatchesOnlyAtom verifies LabelFilter matches when the item
// has exactly one label that equals the requested atom.
func TestLabelFilter_MatchesOnlyAtom(t *testing.T) {
	item := &state.Item{ID: "t2", Labels: []string{"security"}}
	f := views.LabelFilter("security")
	if !f(item) {
		t.Error("LabelFilter(\"security\") must match item with Labels=[security]")
	}
}

// TestLabelFilter_NoMatchOnAbsentAtom verifies LabelFilter returns false when
// the item does not carry the requested atom.
func TestLabelFilter_NoMatchOnAbsentAtom(t *testing.T) {
	item := &state.Item{ID: "t3", Labels: []string{"security"}}
	f := views.LabelFilter("bug")
	if f(item) {
		t.Error("LabelFilter(\"bug\") must not match item with Labels=[security]")
	}
}

// TestLabelFilter_NoMatchOnEmptyLabels verifies LabelFilter returns false when
// the item has no labels.
func TestLabelFilter_NoMatchOnEmptyLabels(t *testing.T) {
	item := &state.Item{ID: "t4", Labels: nil}
	f := views.LabelFilter("bug")
	if f(item) {
		t.Error("LabelFilter must return false for item with nil Labels")
	}
}

// TestLabelFilter_ExactMatchOnly verifies LabelFilter does not substring-match.
// "bug" must not match "bug-critical".
func TestLabelFilter_ExactMatchOnly(t *testing.T) {
	item := &state.Item{ID: "t5", Labels: []string{"bug-critical"}}
	f := views.LabelFilter("bug")
	if f(item) {
		t.Error("LabelFilter(\"bug\") must not substring-match \"bug-critical\"")
	}
}

// TestLabelFilter_ANDComposition verifies that two LabelFilter predicates
// chained via views.Apply implement AND semantics.
func TestLabelFilter_ANDComposition(t *testing.T) {
	items := []*state.Item{
		{ID: "both", Labels: []string{"bug", "security"}},
		{ID: "bug-only", Labels: []string{"bug"}},
		{ID: "security-only", Labels: []string{"security"}},
		{ID: "neither", Labels: nil},
	}

	result := views.Apply(items, views.LabelFilter("bug"))
	result = views.Apply(result, views.LabelFilter("security"))

	if len(result) != 1 {
		t.Errorf("AND composition must return exactly 1 item (both labels), got %d", len(result))
	}
	if len(result) > 0 && result[0].ID != "both" {
		t.Errorf("expected 'both', got %s", result[0].ID)
	}
}

// TestLabelFilter_UnknownAtomReturnsEmpty verifies that filtering by an atom
// not carried by any item returns empty (not an error path — filtering an
// absent atom is a valid query).
func TestLabelFilter_UnknownAtomReturnsEmpty(t *testing.T) {
	items := []*state.Item{
		{ID: "t1", Labels: []string{"bug"}},
		{ID: "t2", Labels: []string{"security"}},
	}

	result := views.Apply(items, views.LabelFilter("not-in-any-item"))
	if len(result) != 0 {
		t.Errorf("filtering by absent atom must return empty, got %d items", len(result))
	}
}
