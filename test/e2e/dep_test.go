package e2e_test

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestE2E_Dep_Remove_UnblocksItem verifies that rd dep remove unblocks an item
// so it reappears in rd ready.
func TestE2E_Dep_Remove_UnblocksItem(t *testing.T) {
	e := NewEnv(t)
	blocker := createItem(e, t, "Dep-Remove Blocker", "p1", "task")
	blocked := createItem(e, t, "Dep-Remove Blocked", "p1", "task")

	// Wire the dependency: blocked is now blocked by blocker.
	e.RdMustSucceed("dep", "add", blocked.ID, blocker.ID)

	// Confirm blocked is excluded from ready.
	ready := e.ReadyItems()
	if containsItem(ready, blocked.ID) {
		t.Fatal("blocked item should not be in ready after dep add")
	}

	// Remove the dependency.
	e.RdMustSucceed("dep", "remove", blocked.ID, blocker.ID)

	// After removal, blocked item must reappear in ready.
	ready = e.ReadyItems()
	if !containsItem(ready, blocked.ID) {
		t.Errorf("item %s should be in ready after dep remove, but is absent", blocked.ID)
	}
	if !containsItem(ready, blocker.ID) {
		t.Errorf("blocker %s should still be in ready after dep remove", blocker.ID)
	}
}

// TestE2E_Dep_Tree_ShowsStructure verifies rd dep tree outputs the correct
// hierarchical tree for a parent→child dependency.
func TestE2E_Dep_Tree_ShowsStructure(t *testing.T) {
	e := NewEnv(t)
	parent := createItem(e, t, "Tree Parent", "p1", "task")
	child := createItem(e, t, "Tree Child", "p1", "task")

	// parent blocks child: child depends on parent.
	e.RdMustSucceed("dep", "add", child.ID, parent.ID)

	// rd dep tree on parent — must mention child somewhere in tree output.
	out := e.RdMustSucceed("dep", "tree", parent.ID)

	if !strings.Contains(out, parent.ID) {
		t.Errorf("dep tree output should contain parent ID %s, got:\n%s", parent.ID, out)
	}
	if !strings.Contains(out, child.ID) {
		t.Errorf("dep tree output should contain child ID %s, got:\n%s", child.ID, out)
	}
}

// TestE2E_Dep_Tree_JSON_ShowsStructure verifies rd dep tree --json outputs a
// valid tree with children reflecting the dependency structure.
func TestE2E_Dep_Tree_JSON_ShowsStructure(t *testing.T) {
	e := NewEnv(t)
	parent := createItem(e, t, "Tree JSON Parent", "p1", "task")
	child := createItem(e, t, "Tree JSON Child", "p1", "task")

	// parent blocks child.
	e.RdMustSucceed("dep", "add", child.ID, parent.ID)

	// rd dep tree --json on parent.
	stdout, stderr, code := e.Rd("dep", "tree", "--json", parent.ID)
	if code != 0 {
		t.Fatalf("rd dep tree --json failed (exit %d): %s", code, stderr)
	}

	type treeNode struct {
		ID       string      `json:"id"`
		Title    string      `json:"title"`
		Status   string      `json:"status"`
		Children []treeNode  `json:"children"`
	}

	var node treeNode
	if err := json.Unmarshal([]byte(stdout), &node); err != nil {
		t.Fatalf("dep tree --json parse failed: %v\noutput: %s", err, stdout)
	}

	if node.ID != parent.ID {
		t.Errorf("root node ID: got %q, want %q", node.ID, parent.ID)
	}

	// Find child in the tree — may be under "blocks" children.
	found := false
	for _, c := range node.Children {
		if c.ID == child.ID {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("child %s not found in dep tree children of parent %s; children: %+v", child.ID, parent.ID, node.Children)
	}
}

// TestE2E_Dep_Add_CrossProjectSyntax verifies that a dotted cross-project
// reference (e.g. "other.project.item-abc") to rd dep add fails with a clear
// error. CUTOVER (ready-cb6 I7): cross-campfire dep resolution was a
// campfire-federation feature and has been deleted — a nostr board is a single
// project. Such a reference now resolves against THIS board's nostr projection
// and fails as an unknown item, which is the correct, unambiguous outcome.
func TestE2E_Dep_Add_CrossProjectSyntax(t *testing.T) {
	e := NewEnv(t)
	local := createItem(e, t, "Local item for cross-project test", "p1", "task")

	// A dotted ref in the blocker position resolves against this board's nostr
	// projection and fails as an unknown item.
	stderr := e.RdMustFail("dep", "add", local.ID, "other.project.item-abc")
	if !strings.Contains(stderr, "not found") {
		t.Errorf("expected 'not found' in error message, got: %q", stderr)
	}
	if !strings.Contains(stderr, "other.project.item-abc") {
		t.Errorf("error should name the unresolvable blocker ref, got: %q", stderr)
	}

	// A dotted ref in the blocked position is rejected up front by dep add's
	// cross-project guard.
	stderr2 := e.RdMustFail("dep", "add", "other.project.item-xyz", local.ID)
	if !strings.Contains(stderr2, "not supported") {
		t.Errorf("expected 'not supported' in error message for blocked arg, got: %q", stderr2)
	}
	if !strings.Contains(stderr2, "other.project.item-xyz") {
		t.Errorf("error should name the unresolvable blocked ref, got: %q", stderr2)
	}
}
