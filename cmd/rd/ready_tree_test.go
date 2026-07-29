package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// mkItem is a small builder for synthetic tree tests below -- avoids the
// nostr/campfire project setup (setupMutationsDir) since findReadyRoot,
// buildReadyGroups, and printReadyTree are pure functions over []*state.Item.
func mkItem(id, parentID, status, title string) *state.Item {
	return &state.Item{ID: id, ParentID: parentID, Status: status, Priority: "p2", Title: title, For: "agent@test"}
}

// TestPrintReadyTree_ShorterThanFlat_MultiEpicTree is the mutation-proof
// claim ready-e88 exists to establish: an indented tree of the SAME items as
// the flat list must actually be shorter, not just re-formatted. A test that
// only asserted "output contains item X" would pass on a flat pretty-printed
// list too and prove nothing -- this asserts the rendered LINE COUNT, which a
// flat-list-with-indentation implementation cannot satisfy once an epic has
// more than maxChildrenPerEpic ready children.
//
// The shape mirrors the real board's measured 2026-07-29 grouping (ready-e88
// progress notes): three epics with 21/22/2 ready children plus a handful of
// parentless orphan items standing alone. Numbers here are synthetic but
// proportioned the same way on purpose, so the cap/collapse math is exercised
// the same way the live board exercises it (see also the real-board run
// recorded in this item's test_decisions).
func TestPrintReadyTree_ShorterThanFlat_MultiEpicTree(t *testing.T) {
	var items []*state.Item
	var allItems []*state.Item

	epicA := mkItem("epic-a", "", "blocked", "Epic A")
	epicB := mkItem("epic-b", "", "blocked", "Epic B")
	epicC := mkItem("epic-c", "", "blocked", "Epic C")
	allItems = append(allItems, epicA, epicB, epicC)

	for i := 0; i < 21; i++ {
		it := mkItem(idFor("a", i), "epic-a", "inbox", "Child A")
		items = append(items, it)
		allItems = append(allItems, it)
	}
	for i := 0; i < 22; i++ {
		it := mkItem(idFor("b", i), "epic-b", "inbox", "Child B")
		items = append(items, it)
		allItems = append(allItems, it)
	}
	for i := 0; i < 2; i++ {
		it := mkItem(idFor("c", i), "epic-c", "inbox", "Child C")
		items = append(items, it)
		allItems = append(allItems, it)
	}
	for i := 0; i < 3; i++ {
		it := mkItem(idFor("orphan", i), "", "inbox", "Orphan")
		items = append(items, it)
		allItems = append(allItems, it)
	}

	if len(items) != 48 {
		t.Fatalf("test setup: expected 48 ready items, got %d", len(items))
	}

	flatOutput := captureStdoutPipe(t, func() { printItemTable(items) })
	treeOutput := captureStdoutPipe(t, func() { printReadyTree(items, allItems) })

	flatLines := countNonEmptyLines(flatOutput)
	treeLines := countNonEmptyLines(treeOutput)

	if flatLines != 48 {
		t.Fatalf("flat baseline: expected 48 lines (one per ready item), got %d:\n%s", flatLines, flatOutput)
	}

	// The reduction rule under test: cap each epic's shown children at
	// maxChildrenPerEpic (5) and collapse the remainder into one "N more"
	// line. Expected: epic-a (1 header + 5 shown + 1 "more") = 7,
	// epic-b (same shape) = 7, epic-c (1 header + 2 shown, no cap hit) = 3,
	// 3 standalone orphans (no header, one line each) = 3. Total = 20.
	const wantTreeLines = 20
	if treeLines != wantTreeLines {
		t.Fatalf("tree output: expected exactly %d lines under the cap-per-epic rule, got %d:\n%s", wantTreeLines, treeLines, treeOutput)
	}
	if treeLines >= flatLines {
		t.Fatalf("tree output (%d lines) is not shorter than the flat equivalent (%d lines) -- indentation without collapsing fails the one-screen outcome", treeLines, flatLines)
	}

	// The collapsed epics must each surface a "more" pointer -- silently
	// dropping items would be worse than the flat list, not better.
	if !strings.Contains(treeOutput, "+16 more") {
		t.Errorf("expected epic-a's 21 children (5 shown, cap=5) to report '+16 more', output:\n%s", treeOutput)
	}
	if !strings.Contains(treeOutput, "+17 more") {
		t.Errorf("expected epic-b's 22 children (5 shown, cap=5) to report '+17 more', output:\n%s", treeOutput)
	}
	// epic-c has only 2 children, under the cap -- must NOT collapse or drop.
	if strings.Contains(treeOutput, "epic-c") && strings.Contains(treeOutput, "more)") {
		// only a problem if the "more" line is specifically attached to epic-c;
		// checked precisely via line count above, this is a smoke check.
		for _, line := range strings.Split(treeOutput, "\n") {
			if strings.Contains(line, "epic-c") && strings.Contains(line, "more") {
				t.Errorf("epic-c has only 2 children (under cap=5) and must not show a 'more' line: %q", line)
			}
		}
	}
	// The 3 orphans must still each be individually visible (standalone, no
	// header) -- grouping must never silently swallow a parentless item.
	for i := 0; i < 3; i++ {
		id := idFor("orphan", i)
		if !strings.Contains(treeOutput, id) {
			t.Errorf("orphan item %q missing from tree output entirely -- must render standalone:\n%s", id, treeOutput)
		}
	}
}

func idFor(prefix string, i int) string {
	return prefix + "-" + string(rune('0'+i/10)) + string(rune('0'+i%10))
}

func countNonEmptyLines(s string) int {
	n := 0
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) != "" {
			n++
		}
	}
	return n
}

// TestBuildReadyGroups_ClosedParentStillAnchorsWalk covers the live-board
// case called out explicitly in ready-e88: a ready item whose IMMEDIATE
// parent is closed (done/cancelled/failed) still belongs to its epic, and
// the walk must traverse through the closed item rather than stopping at it.
// parent_id records structure, not liveness (ready-500's derived-status
// guard is about writes resetting status; this is a read-only traversal that
// must not treat "closed" as "absent").
func TestBuildReadyGroups_ClosedParentStillAnchorsWalk(t *testing.T) {
	epic := mkItem("epic-live", "", "blocked", "Live Epic")
	closedMid := mkItem("mid-closed", "epic-live", "done", "Closed intermediate")
	leaf := mkItem("leaf-ready", "mid-closed", "inbox", "Ready leaf behind a closed parent")

	allItems := []*state.Item{epic, closedMid, leaf}
	byID := map[string]*state.Item{epic.ID: epic, closedMid.ID: closedMid, leaf.ID: leaf}

	root := findReadyRoot(leaf, byID)
	if root.ID != "epic-live" {
		t.Fatalf("expected leaf behind a closed parent to resolve to epic-live, got %q", root.ID)
	}

	groups := buildReadyGroups([]*state.Item{leaf}, byID)
	if len(groups) != 1 || groups[0].root.ID != "epic-live" {
		t.Fatalf("expected leaf grouped under epic-live, got groups=%+v", groups)
	}
	if len(groups[0].children) != 1 || groups[0].children[0].ID != "leaf-ready" {
		t.Fatalf("expected leaf-ready as the sole child of epic-live's group, got %+v", groups[0].children)
	}

	_ = allItems // exercised via byID above; kept for readability of the fixture
}

// TestFindReadyRoot_OrphanNoParent covers the other live-board case named in
// ready-e88: an item with NO parent_id at all (a true orphan -- 4 exist on
// the live board per the item's own measurement). It must resolve to itself,
// not error or panic, and printReadyTree must render it standalone (no epic
// header repeating the same line).
func TestFindReadyRoot_OrphanNoParent(t *testing.T) {
	orphan := mkItem("orphan-x", "", "active", "No parent at all")
	byID := map[string]*state.Item{orphan.ID: orphan}

	root := findReadyRoot(orphan, byID)
	if root.ID != "orphan-x" {
		t.Fatalf("expected orphan to be its own root, got %q", root.ID)
	}

	groups := buildReadyGroups([]*state.Item{orphan}, byID)
	if len(groups) != 1 || groups[0].root.ID != "orphan-x" {
		t.Fatalf("expected a single group rooted at orphan-x, got %+v", groups)
	}

	output := captureStdoutPipe(t, func() { printReadyTree([]*state.Item{orphan}, []*state.Item{orphan}) })
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 1 {
		t.Fatalf("expected exactly 1 line for a standalone orphan (no epic header), got %d:\n%s", len(lines), output)
	}
	if !strings.Contains(output, "orphan-x") {
		t.Errorf("orphan-x missing from its own standalone line:\n%s", output)
	}
}

// TestFindReadyRoot_DanglingParentDoesNotPanic is defensive: ready-e88's
// progress notes state the live board currently has NO dangling parent_id
// pointers, but the walk must degrade gracefully (treat the item as its own
// root) rather than panic or loop if one ever appears, since byID is built
// from a live nostr projection this code does not control.
func TestFindReadyRoot_DanglingParentDoesNotPanic(t *testing.T) {
	dangling := mkItem("dangling-1", "ready-does-not-exist", "active", "Parent not in byID")
	byID := map[string]*state.Item{dangling.ID: dangling}

	root := findReadyRoot(dangling, byID)
	if root.ID != "dangling-1" {
		t.Fatalf("expected a dangling parent pointer to fall back to the item itself, got %q", root.ID)
	}
}

// TestOutputItemsJSON_UnchangedByParentGrouping verifies constraint 1 from
// the item: `rd ready --json` consumers must keep working unmodified. The
// tree/grouping render only touches the TTY branch (printReadyTree); the
// --json path still calls outputItemsJSON directly on the flat, sorted
// []*state.Item slice with no grouping wrapper, no matter how much parent_id
// structure exists among the items.
func TestOutputItemsJSON_UnchangedByParentGrouping(t *testing.T) {
	items := []*state.Item{
		mkItem("epic-json", "", "blocked", "Epic"),
		mkItem("child-json-1", "epic-json", "inbox", "Child 1"),
		mkItem("child-json-2", "epic-json", "inbox", "Child 2"),
	}

	output := captureStdoutPipe(t, func() {
		if err := outputItemsJSON(items); err != nil {
			t.Fatalf("outputItemsJSON: %v", err)
		}
	})

	var decoded []map[string]interface{}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("outputItemsJSON output is not a flat JSON array: %v\noutput:\n%s", err, output)
	}
	if len(decoded) != len(items) {
		t.Fatalf("expected %d top-level entries (flat, no grouping), got %d", len(items), len(decoded))
	}
	for i, item := range items {
		if decoded[i]["id"] != item.ID {
			t.Errorf("entry %d: expected id %q, got %v", i, item.ID, decoded[i]["id"])
		}
		if _, hasChildren := decoded[i]["children"]; hasChildren {
			t.Errorf("entry %d: unexpected 'children' key -- --json must stay a flat array of Item, not a nested tree", i)
		}
	}
}
