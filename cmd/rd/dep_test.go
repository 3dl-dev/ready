package main

import (
	"encoding/json"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// buildBlockArgsMap constructs the argsMap for a work:block operation,
// mirroring the logic in depAddCmd.RunE.
func buildBlockArgsMap(blockerID, blockedID, blockerMsgID, blockedMsgID string) (map[string]any, []string, []string) {
	argsMap := map[string]any{
		"blocker_id":  blockerID,
		"blocked_id":  blockedID,
		"blocker_msg": blockerMsgID,
		"blocked_msg": blockedMsgID,
	}
	tags := []string{"work:block"}
	antecedents := []string{blockerMsgID, blockedMsgID}
	return argsMap, tags, antecedents
}

// buildUnblockArgsMap constructs the argsMap for a work:unblock operation,
// mirroring the logic in depRemoveCmd.RunE.
func buildUnblockArgsMap(blockMsgID, reason string) (map[string]any, []string, []string) {
	argsMap := map[string]any{
		"target": blockMsgID,
	}
	if reason != "" {
		argsMap["reason"] = reason
	}
	tags := []string{"work:unblock"}
	antecedents := []string{blockMsgID}
	return argsMap, tags, antecedents
}

// TestBuildBlockArgsMap_FieldsAndTags verifies that buildBlockArgsMap produces
// the correct JSON fields, exactly one tag (work:block), and two antecedents
// referencing both work:create message IDs per convention §4.6.
func TestBuildBlockArgsMap_FieldsAndTags(t *testing.T) {
	blockerID := "ready-t01"
	blockedID := "ready-t02"
	blockerMsgID := "msg-create-t01-aaaa-bbbb-cccc-dddddddddddd"
	blockedMsgID := "msg-create-t02-aaaa-bbbb-cccc-dddddddddddd"

	argsMap, tags, _ := buildBlockArgsMap(blockerID, blockedID, blockerMsgID, blockedMsgID)

	payloadBytes, err := json.Marshal(argsMap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	// Decode and verify payload fields.
	var decoded map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if decoded["blocker_id"] != blockerID {
		t.Errorf("blocker_id=%v, want %q", decoded["blocker_id"], blockerID)
	}
	if decoded["blocked_id"] != blockedID {
		t.Errorf("blocked_id=%v, want %q", decoded["blocked_id"], blockedID)
	}
	if decoded["blocker_msg"] != blockerMsgID {
		t.Errorf("blocker_msg=%v, want %q", decoded["blocker_msg"], blockerMsgID)
	}
	if decoded["blocked_msg"] != blockedMsgID {
		t.Errorf("blocked_msg=%v, want %q", decoded["blocked_msg"], blockedMsgID)
	}

	// Tags: exactly one, must be work:block per convention §4.1.
	if len(tags) != 1 {
		t.Errorf("expected exactly 1 tag, got %d: %v", len(tags), tags)
	}
	if tags[0] != "work:block" {
		t.Errorf("tag[0]=%q, want 'work:block'", tags[0])
	}
}

// TestBuildBlockArgsMap_Antecedents verifies that both work:create message IDs
// appear in the antecedents for a work:block message per convention §4.6.
// This is the campfire causal ordering invariant: the block message causally
// depends on both item creation messages.
func TestBuildBlockArgsMap_Antecedents(t *testing.T) {
	blockerMsgID := "msg-create-t01-aaaa-bbbb-cccc-dddddddddddd"
	blockedMsgID := "msg-create-t02-aaaa-bbbb-cccc-dddddddddddd"

	_, _, antecedents := buildBlockArgsMap("r-t01", "r-t02", blockerMsgID, blockedMsgID)

	// Must have exactly 2 antecedents per §4.6.
	if len(antecedents) != 2 {
		t.Fatalf("expected 2 antecedents, got %d: %v", len(antecedents), antecedents)
	}
	if antecedents[0] != blockerMsgID {
		t.Errorf("antecedents[0]=%q, want blockerMsgID=%q", antecedents[0], blockerMsgID)
	}
	if antecedents[1] != blockedMsgID {
		t.Errorf("antecedents[1]=%q, want blockedMsgID=%q", antecedents[1], blockedMsgID)
	}
}

// TestBuildUnblockArgsMap_TargetIsBlockMsg verifies that buildUnblockArgsMap
// sets target to the work:block message ID (not an item ID) per convention §4.7.
// This is the key invariant: work:unblock references the block message, not the items.
func TestBuildUnblockArgsMap_TargetIsBlockMsg(t *testing.T) {
	blockMsgID := "msg-block-abc-1234-5678-9abc-def012345678"
	reason := "No longer blocked"

	argsMap, tags, antecedents := buildUnblockArgsMap(blockMsgID, reason)

	payloadBytes, err := json.Marshal(argsMap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	// Target must be the block message ID, not an item ID.
	if decoded["target"] != blockMsgID {
		t.Errorf("target=%v, want blockMsgID=%q", decoded["target"], blockMsgID)
	}
	if decoded["reason"] != reason {
		t.Errorf("reason=%v, want %q", decoded["reason"], reason)
	}

	// Tags: exactly one, must be work:unblock.
	if len(tags) != 1 {
		t.Errorf("expected 1 tag, got %d: %v", len(tags), tags)
	}
	if tags[0] != "work:unblock" {
		t.Errorf("tag[0]=%q, want 'work:unblock'", tags[0])
	}

	// Antecedent is the block message being reversed.
	if len(antecedents) != 1 {
		t.Fatalf("expected 1 antecedent, got %d: %v", len(antecedents), antecedents)
	}
	if antecedents[0] != blockMsgID {
		t.Errorf("antecedent=%q, want blockMsgID=%q", antecedents[0], blockMsgID)
	}
}

// TestBuildUnblockArgsMap_ReasonOmittedWhenEmpty verifies that reason is omitted
// from the JSON payload when empty per convention §4.7 (omit when not in argsMap).
func TestBuildUnblockArgsMap_ReasonOmittedWhenEmpty(t *testing.T) {
	argsMap, _, _ := buildUnblockArgsMap("msg-block-xyz", "")

	payloadBytes, err := json.Marshal(argsMap)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded map[string]interface{}
	if err := json.Unmarshal(payloadBytes, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if _, ok := decoded["reason"]; ok {
		t.Error("reason should be omitted when empty, but was present in JSON")
	}
}

// TestBuildDepTree_CyclicDependencies verifies that buildDepTree handles cyclic
// dependencies correctly without infinite recursion. The bug occurred when:
//  1. A blocks B, B blocks A (simple cycle)
//  2. A third item or reference path leads to a node in the cycle
//  3. After processing a child and deleting from visited, the same node could be
//     revisited on another path, causing infinite recursion.
//
// Fix: Keep cycle detection state persistent across all branches, not just the
// current recursion path. Use a separate inPath map for the current recursion path.
func TestBuildDepTree_CyclicDependencies(t *testing.T) {
	// Create items with a cycle: A blocks B, B blocks A
	itemA := &state.Item{
		ID:     "ready-a",
		Title:  "Item A",
		Status: "ready",
		Blocks: []string{"ready-b"},
	}
	itemB := &state.Item{
		ID:     "ready-b",
		Title:  "Item B",
		Status: "ready",
		Blocks: []string{"ready-a"},
	}

	items := map[string]*state.Item{
		"ready-a": itemA,
		"ready-b": itemB,
	}

	// buildDepTree should not panic or stack overflow
	visited := make(map[string]bool)
	tree := buildDepTree("ready-a", items, visited)

	// Verify the root was processed
	if tree == nil {
		t.Fatal("buildDepTree returned nil")
	}
	if tree.ID != "ready-a" {
		t.Errorf("root ID=%q, want 'ready-a'", tree.ID)
	}

	// Verify we have children (B)
	if len(tree.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(tree.Children))
	}
	child := tree.Children[0]
	if child.ID != "ready-b" {
		t.Errorf("child ID=%q, want 'ready-b'", child.ID)
	}

	// B should have A as a child, and it should be marked as cycle
	if len(child.Children) != 1 {
		t.Fatalf("expected B to have 1 child (A), got %d", len(child.Children))
	}
	cycleChild := child.Children[0]
	if cycleChild.ID != "ready-a" {
		t.Errorf("B's child ID=%q, want 'ready-a'", cycleChild.ID)
	}
	// The second reference to A should be marked as "(cycle)"
	if !containsString(cycleChild.Status, "(cycle)") {
		t.Errorf("B's child A status=%q, should contain '(cycle)'", cycleChild.Status)
	}
}

// TestBuildDepTree_CyclicDependencies_WithThirdPath verifies that buildDepTree
// handles the specific case where a cyclic node is reachable via a third path.
// This tests the original bug: A blocks B, B blocks A, and a third reference to A
// or B. After deleting visited[B] after processing B's children, if A appears
// again (directly or through a third reference), visited[A] should not be deleted
// until the entire traversal completes.
func TestBuildDepTree_CyclicDependencies_WithThirdPath(t *testing.T) {
	// A blocks B
	// B blocks A (creating cycle)
	// C blocks A (third reference path to node in cycle)
	itemA := &state.Item{
		ID:     "ready-a",
		Title:  "Item A",
		Status: "ready",
		Blocks: []string{"ready-b"},
	}
	itemB := &state.Item{
		ID:     "ready-b",
		Title:  "Item B",
		Status: "ready",
		Blocks: []string{"ready-a"},
	}
	itemC := &state.Item{
		ID:     "ready-c",
		Title:  "Item C",
		Status: "ready",
		Blocks: []string{"ready-a"},
	}

	items := map[string]*state.Item{
		"ready-a": itemA,
		"ready-b": itemB,
		"ready-c": itemC,
	}

	// Start from C, which references A, which references B, which references A again
	visited := make(map[string]bool)
	tree := buildDepTree("ready-c", items, visited)

	if tree == nil {
		t.Fatal("buildDepTree returned nil")
	}
	if tree.ID != "ready-c" {
		t.Errorf("root ID=%q, want 'ready-c'", tree.ID)
	}

	// C should have A as a child
	if len(tree.Children) != 1 {
		t.Fatalf("expected 1 child for C, got %d", len(tree.Children))
	}
	childA := tree.Children[0]
	if childA.ID != "ready-a" {
		t.Errorf("child ID=%q, want 'ready-a'", childA.ID)
	}

	// A should have B as a child
	if len(childA.Children) != 1 {
		t.Fatalf("expected 1 child for A, got %d", len(childA.Children))
	}
	childB := childA.Children[0]
	if childB.ID != "ready-b" {
		t.Errorf("child B ID=%q, want 'ready-b'", childB.ID)
	}

	// B's child should be A marked as cycle
	if len(childB.Children) != 1 {
		t.Fatalf("expected 1 child for B, got %d", len(childB.Children))
	}
	childA2 := childB.Children[0]
	if childA2.ID != "ready-a" {
		t.Errorf("B's child ID=%q, want 'ready-a'", childA2.ID)
	}
	if !containsString(childA2.Status, "(cycle)") {
		t.Errorf("B's child A status=%q, should contain '(cycle)'", childA2.Status)
	}
}

// containsString is a helper to check if a string contains a substring.
func containsString(s, substring string) bool {
	for i := 0; i <= len(s)-len(substring); i++ {
		if s[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}

// TestBuildDepTree_DiamondDependency verifies that buildDepTree correctly handles
// diamond dependency patterns without duplicating nodes.
// Pattern: A blocks both B and C, both B and C block D
//
//	 A
//	/ \
//
// B   C
//
//	\ /
//	 D
func TestBuildDepTree_DiamondDependency(t *testing.T) {
	itemA := &state.Item{
		ID:     "ready-a",
		Title:  "Item A",
		Status: "active",
		Blocks: []string{"ready-b", "ready-c"},
	}
	itemB := &state.Item{
		ID:     "ready-b",
		Title:  "Item B",
		Status: "active",
		Blocks: []string{"ready-d"},
	}
	itemC := &state.Item{
		ID:     "ready-c",
		Title:  "Item C",
		Status: "active",
		Blocks: []string{"ready-d"},
	}
	itemD := &state.Item{
		ID:     "ready-d",
		Title:  "Item D",
		Status: "active",
		Blocks: []string{},
	}

	items := map[string]*state.Item{
		"ready-a": itemA,
		"ready-b": itemB,
		"ready-c": itemC,
		"ready-d": itemD,
	}

	visited := make(map[string]bool)
	tree := buildDepTree("ready-a", items, visited)

	if tree == nil {
		t.Fatal("buildDepTree returned nil")
	}
	if tree.ID != "ready-a" {
		t.Errorf("root ID=%q, want 'ready-a'", tree.ID)
	}

	// A should have 2 children: B and C
	if len(tree.Children) != 2 {
		t.Fatalf("expected 2 children for A, got %d", len(tree.Children))
	}

	// Find B and C among children
	var childB, childC *treeNode
	for _, child := range tree.Children {
		if child.ID == "ready-b" {
			childB = child
		} else if child.ID == "ready-c" {
			childC = child
		}
	}

	if childB == nil {
		t.Fatal("child B not found")
	}
	if childC == nil {
		t.Fatal("child C not found")
	}

	// Both B and C should block D
	if len(childB.Children) != 1 {
		t.Fatalf("expected 1 child for B, got %d", len(childB.Children))
	}
	if childB.Children[0].ID != "ready-d" {
		t.Errorf("B's child ID=%q, want 'ready-d'", childB.Children[0].ID)
	}

	if len(childC.Children) != 1 {
		t.Fatalf("expected 1 child for C, got %d", len(childC.Children))
	}
	if childC.Children[0].ID != "ready-d" {
		t.Errorf("C's child ID=%q, want 'ready-d'", childC.Children[0].ID)
	}

	// D should be the same node (by reference) in both branches when they refer to it
	// Actually, they'll be different tree nodes due to how the recursion builds,
	// but both should have the same ID
	if childB.Children[0].ID != childC.Children[0].ID {
		t.Errorf("D nodes have different IDs: B's=%q, C's=%q", childB.Children[0].ID, childC.Children[0].ID)
	}
}

// TestBuildDepTree_MissingItem verifies that buildDepTree returns a placeholder
// when an item is not found in the items map.
func TestBuildDepTree_MissingItem(t *testing.T) {
	itemA := &state.Item{
		ID:     "ready-a",
		Title:  "Item A",
		Status: "active",
		Blocks: []string{"ready-missing"},
	}

	items := map[string]*state.Item{
		"ready-a": itemA,
	}

	visited := make(map[string]bool)
	tree := buildDepTree("ready-a", items, visited)

	if tree == nil {
		t.Fatal("buildDepTree returned nil")
	}

	// Should have 1 child: the missing item
	if len(tree.Children) != 1 {
		t.Fatalf("expected 1 child, got %d", len(tree.Children))
	}

	missing := tree.Children[0]
	if missing.ID != "ready-missing" {
		t.Errorf("missing child ID=%q, want 'ready-missing'", missing.ID)
	}
	if missing.Title != "(not found)" {
		t.Errorf("missing child Title=%q, want '(not found)'", missing.Title)
	}
	if missing.Status != "unknown" {
		t.Errorf("missing child Status=%q, want 'unknown'", missing.Status)
	}
}

// TestBuildDepTree_DeepTree verifies that buildDepTree correctly handles
// deep linear dependency chains without errors.
// Pattern: A blocks B blocks C blocks D blocks E (5 levels)
func TestBuildDepTree_DeepTree(t *testing.T) {
	itemA := &state.Item{
		ID:     "ready-a",
		Title:  "Item A",
		Status: "active",
		Blocks: []string{"ready-b"},
	}
	itemB := &state.Item{
		ID:     "ready-b",
		Title:  "Item B",
		Status: "active",
		Blocks: []string{"ready-c"},
	}
	itemC := &state.Item{
		ID:     "ready-c",
		Title:  "Item C",
		Status: "active",
		Blocks: []string{"ready-d"},
	}
	itemD := &state.Item{
		ID:     "ready-d",
		Title:  "Item D",
		Status: "active",
		Blocks: []string{"ready-e"},
	}
	itemE := &state.Item{
		ID:     "ready-e",
		Title:  "Item E",
		Status: "active",
		Blocks: []string{},
	}

	items := map[string]*state.Item{
		"ready-a": itemA,
		"ready-b": itemB,
		"ready-c": itemC,
		"ready-d": itemD,
		"ready-e": itemE,
	}

	visited := make(map[string]bool)
	tree := buildDepTree("ready-a", items, visited)

	if tree == nil {
		t.Fatal("buildDepTree returned nil")
	}

	// Walk the chain and verify depth
	current := tree
	expectedDepth := 5
	for i := 0; i < expectedDepth; i++ {
		if current == nil {
			t.Fatalf("tree is nil at depth %d", i)
		}
		expectedID := string(rune('a' + i))
		if current.ID != "ready-"+expectedID {
			t.Errorf("at depth %d: ID=%q, want 'ready-%s'", i, current.ID, expectedID)
		}

		if i < expectedDepth-1 {
			if len(current.Children) != 1 {
				t.Fatalf("at depth %d: expected 1 child, got %d", i, len(current.Children))
			}
			current = current.Children[0]
		}
	}

	// At the end, E should have no children
	if len(current.Children) != 0 {
		t.Errorf("leaf E should have 0 children, got %d", len(current.Children))
	}
}

// TestBuildDepTree_ParentChildRelationship verifies that buildDepTree includes
// child items via parent_id relationship in addition to blocks relationship.
func TestBuildDepTree_ParentChildRelationship(t *testing.T) {
	parent := &state.Item{
		ID:     "ready-parent",
		Title:  "Parent Item",
		Status: "active",
		Blocks: []string{},
	}
	child1 := &state.Item{
		ID:       "ready-child1",
		Title:    "Child 1",
		Status:   "active",
		ParentID: "ready-parent",
		Blocks:   []string{},
	}
	child2 := &state.Item{
		ID:       "ready-child2",
		Title:    "Child 2",
		Status:   "active",
		ParentID: "ready-parent",
		Blocks:   []string{},
	}

	items := map[string]*state.Item{
		"ready-parent": parent,
		"ready-child1": child1,
		"ready-child2": child2,
	}

	visited := make(map[string]bool)
	tree := buildDepTree("ready-parent", items, visited)

	if tree == nil {
		t.Fatal("buildDepTree returned nil")
	}
	if tree.ID != "ready-parent" {
		t.Errorf("root ID=%q, want 'ready-parent'", tree.ID)
	}

	// Should have 2 children (both via parent_id)
	if len(tree.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(tree.Children))
	}

	// Verify both children are present
	childIDs := map[string]bool{}
	for _, child := range tree.Children {
		childIDs[child.ID] = true
	}

	if !childIDs["ready-child1"] {
		t.Error("child1 not found in tree.Children")
	}
	if !childIDs["ready-child2"] {
		t.Error("child2 not found in tree.Children")
	}
}

// TestBuildDepTree_MixedBlocksAndParent verifies that buildDepTree correctly
// combines both blocks relationships and parent_id relationships.
// Pattern: A blocks B, C is a child of A
func TestBuildDepTree_MixedBlocksAndParent(t *testing.T) {
	itemA := &state.Item{
		ID:     "ready-a",
		Title:  "Item A",
		Status: "active",
		Blocks: []string{"ready-b"},
	}
	itemB := &state.Item{
		ID:     "ready-b",
		Title:  "Item B",
		Status: "active",
		Blocks: []string{},
	}
	itemC := &state.Item{
		ID:       "ready-c",
		Title:    "Item C",
		Status:   "active",
		ParentID: "ready-a",
		Blocks:   []string{},
	}

	items := map[string]*state.Item{
		"ready-a": itemA,
		"ready-b": itemB,
		"ready-c": itemC,
	}

	visited := make(map[string]bool)
	tree := buildDepTree("ready-a", items, visited)

	if tree == nil {
		t.Fatal("buildDepTree returned nil")
	}

	// A should have 2 children: B (from blocks) and C (from parent_id)
	if len(tree.Children) != 2 {
		t.Fatalf("expected 2 children for A, got %d", len(tree.Children))
	}

	childIDs := map[string]bool{}
	for _, child := range tree.Children {
		childIDs[child.ID] = true
	}

	if !childIDs["ready-b"] {
		t.Error("B (blocks relationship) not found in A's children")
	}
	if !childIDs["ready-c"] {
		t.Error("C (parent_id relationship) not found in A's children")
	}
}

// TestBuildDepTree_DuplicateChildren verifies that buildDepTree does not add
// the same child twice even if it appears through multiple relationship types
// or in the Blocks list multiple times.
func TestBuildDepTree_DuplicateChildren(t *testing.T) {
	itemA := &state.Item{
		ID:     "ready-a",
		Title:  "Item A",
		Status: "active",
		Blocks: []string{"ready-b", "ready-b"}, // duplicate in Blocks
	}
	itemB := &state.Item{
		ID:     "ready-b",
		Title:  "Item B",
		Status: "active",
		Blocks: []string{},
	}

	items := map[string]*state.Item{
		"ready-a": itemA,
		"ready-b": itemB,
	}

	visited := make(map[string]bool)
	tree := buildDepTree("ready-a", items, visited)

	if tree == nil {
		t.Fatal("buildDepTree returned nil")
	}

	// A should have exactly 1 child, not 2 (even though B appears twice in Blocks)
	if len(tree.Children) != 1 {
		t.Fatalf("expected 1 child for A, got %d", len(tree.Children))
	}
	if tree.Children[0].ID != "ready-b" {
		t.Errorf("child ID=%q, want 'ready-b'", tree.Children[0].ID)
	}
}

// TestBuildDepTree_RootNotInItems verifies that buildDepTree returns a placeholder
// when the root item itself is not found.
func TestBuildDepTree_RootNotInItems(t *testing.T) {
	items := make(map[string]*state.Item)

	visited := make(map[string]bool)
	tree := buildDepTree("ready-missing", items, visited)

	if tree == nil {
		t.Fatal("buildDepTree returned nil")
	}
	if tree.ID != "ready-missing" {
		t.Errorf("root ID=%q, want 'ready-missing'", tree.ID)
	}
	if tree.Title != "(not found)" {
		t.Errorf("root Title=%q, want '(not found)'", tree.Title)
	}
	if tree.Status != "unknown" {
		t.Errorf("root Status=%q, want 'unknown'", tree.Status)
	}
	if len(tree.Children) != 0 {
		t.Errorf("root should have 0 children, got %d", len(tree.Children))
	}
}
