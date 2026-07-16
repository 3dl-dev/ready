package main

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// TestBuildUpTree_LinearChain verifies that buildUpTree walks blocked_by[]
// upstream: C blocked by B blocked by A.
func TestBuildUpTree_LinearChain(t *testing.T) {
	itemA := &state.Item{ID: "ready-a", Title: "A", Status: "done"}
	itemB := &state.Item{ID: "ready-b", Title: "B", Status: "active", BlockedBy: []string{"ready-a"}}
	itemC := &state.Item{ID: "ready-c", Title: "C", Status: "blocked", BlockedBy: []string{"ready-b"}}

	items := map[string]*state.Item{"ready-a": itemA, "ready-b": itemB, "ready-c": itemC}

	tree := buildUpTree("ready-c", items, map[string]bool{})
	if tree.ID != "ready-c" {
		t.Fatalf("root ID=%q, want ready-c", tree.ID)
	}
	if len(tree.BlockedBy) != 1 || tree.BlockedBy[0].ID != "ready-b" {
		t.Fatalf("C should be blocked by B, got %+v", tree.BlockedBy)
	}
	b := tree.BlockedBy[0]
	if len(b.BlockedBy) != 1 || b.BlockedBy[0].ID != "ready-a" {
		t.Fatalf("B should be blocked by A, got %+v", b.BlockedBy)
	}
	a := b.BlockedBy[0]
	if len(a.BlockedBy) != 0 {
		t.Errorf("A should have no upstream blockers, got %+v", a.BlockedBy)
	}
}

// TestBuildUpTree_Cycle verifies cycle-safety: A blocked by B, B blocked by A.
// The traversal must terminate and mark the revisited node with "(cycle)".
func TestBuildUpTree_Cycle(t *testing.T) {
	itemA := &state.Item{ID: "ready-a", Title: "A", Status: "blocked", BlockedBy: []string{"ready-b"}}
	itemB := &state.Item{ID: "ready-b", Title: "B", Status: "blocked", BlockedBy: []string{"ready-a"}}
	items := map[string]*state.Item{"ready-a": itemA, "ready-b": itemB}

	tree := buildUpTree("ready-a", items, map[string]bool{})
	if tree.ID != "ready-a" {
		t.Fatalf("root ID=%q, want ready-a", tree.ID)
	}
	if len(tree.BlockedBy) != 1 {
		t.Fatalf("A should be blocked by B, got %+v", tree.BlockedBy)
	}
	b := tree.BlockedBy[0]
	if b.ID != "ready-b" {
		t.Fatalf("A blocked by %q, want ready-b", b.ID)
	}
	if len(b.BlockedBy) != 1 {
		t.Fatalf("B should reference A, got %+v", b.BlockedBy)
	}
	cyc := b.BlockedBy[0]
	if cyc.ID != "ready-a" {
		t.Errorf("cycle node ID=%q, want ready-a", cyc.ID)
	}
	if !containsString(cyc.Status, "(cycle)") {
		t.Errorf("revisited node status=%q, should contain (cycle)", cyc.Status)
	}
}

// TestBuildUpTree_Diamond verifies that a diamond upstream shape does not recurse
// infinitely and that a shared blocker is walked on each branch it appears.
// D blocked by B and C; both B and C blocked by A.
func TestBuildUpTree_Diamond(t *testing.T) {
	itemA := &state.Item{ID: "ready-a", Title: "A", Status: "done"}
	itemB := &state.Item{ID: "ready-b", Title: "B", Status: "done", BlockedBy: []string{"ready-a"}}
	itemC := &state.Item{ID: "ready-c", Title: "C", Status: "done", BlockedBy: []string{"ready-a"}}
	itemD := &state.Item{ID: "ready-d", Title: "D", Status: "active", BlockedBy: []string{"ready-b", "ready-c"}}
	items := map[string]*state.Item{"ready-a": itemA, "ready-b": itemB, "ready-c": itemC, "ready-d": itemD}

	tree := buildUpTree("ready-d", items, map[string]bool{})
	if len(tree.BlockedBy) != 2 {
		t.Fatalf("D should have 2 upstream blockers, got %d", len(tree.BlockedBy))
	}
	for _, child := range tree.BlockedBy {
		if len(child.BlockedBy) != 1 || child.BlockedBy[0].ID != "ready-a" {
			t.Errorf("%s should be blocked by A, got %+v", child.ID, child.BlockedBy)
		}
	}
}

// TestBuildUpTree_MissingBlocker verifies a placeholder is emitted for a blocker
// ID not present in the item map (e.g. a closed blocker whose card was pruned).
func TestBuildUpTree_MissingBlocker(t *testing.T) {
	itemA := &state.Item{ID: "ready-a", Title: "A", Status: "blocked", BlockedBy: []string{"ready-gone"}}
	items := map[string]*state.Item{"ready-a": itemA}

	tree := buildUpTree("ready-a", items, map[string]bool{})
	if len(tree.BlockedBy) != 1 {
		t.Fatalf("expected 1 blocker, got %d", len(tree.BlockedBy))
	}
	missing := tree.BlockedBy[0]
	if missing.ID != "ready-gone" || missing.Title != "(not found)" || missing.Status != "unknown" {
		t.Errorf("missing blocker placeholder wrong: %+v", missing)
	}
}

// TestBuildUpTree_RootMissing verifies a placeholder for an unknown root item.
func TestBuildUpTree_RootMissing(t *testing.T) {
	tree := buildUpTree("ready-nope", map[string]*state.Item{}, map[string]bool{})
	if tree.ID != "ready-nope" || tree.Title != "(not found)" || tree.Status != "unknown" {
		t.Errorf("root placeholder wrong: %+v", tree)
	}
	if len(tree.BlockedBy) != 0 {
		t.Errorf("missing root should have no blockers, got %+v", tree.BlockedBy)
	}
}

// TestBuildUpTree_NoBlockers verifies a leaf (no blocked_by) has an empty upstream.
func TestBuildUpTree_NoBlockers(t *testing.T) {
	itemA := &state.Item{ID: "ready-a", Title: "A", Status: "active"}
	tree := buildUpTree("ready-a", map[string]*state.Item{"ready-a": itemA}, map[string]bool{})
	if len(tree.BlockedBy) != 0 {
		t.Errorf("expected no upstream blockers, got %+v", tree.BlockedBy)
	}
}
