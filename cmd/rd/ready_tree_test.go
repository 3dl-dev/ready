package main

import (
	"encoding/json"
	"fmt"
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
	// line, EXCEPT a group at or under headerThreshold (7 -- see its doc),
	// which inlines with no header at all since a header there would cost
	// MORE lines than the flat list, not fewer (ready-e88 rework challenge 4).
	// Expected: epic-a (1 header + 5 shown + 1 "more") = 7, epic-b (same
	// shape) = 7, epic-c (2 children, under headerThreshold -- inlined, no
	// header, 2 plain lines) = 2, 3 standalone orphans (no header, one line
	// each) = 3. Total = 19.
	const wantTreeLines = 19
	if treeLines != wantTreeLines {
		t.Fatalf("tree output: expected exactly %d lines under the cap-per-epic + inline-small-groups rule, got %d:\n%s", wantTreeLines, treeLines, treeOutput)
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
	// epic-c has only 2 children, under headerThreshold -- must inline with NO
	// header row at all (its root ID must not appear anywhere in the output),
	// and must not collapse or drop either child.
	if strings.Contains(treeOutput, "epic-c") {
		t.Errorf("epic-c has only 2 children (under headerThreshold=%d) and must inline with no header row -- its root ID must not appear in output:\n%s", headerThreshold, treeOutput)
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

// TestOutputItemsJSON_UnchangedByParentGrouping is a narrow unit test of
// outputItemsJSON's own contract (flat array in, flat array out) -- it does
// NOT reach readyCmd.RunE and was never in a position to catch a --json
// branch rewired to nest by parent (ready-e88 rework, challenge 2: this test
// would pass unchanged even if RunE's --json branch emitted a tree, since it
// calls outputItemsJSON directly on a hand-built slice untouched by RunE).
// The authoritative regression gate for "rd ready --json stays flat under
// grouping" is TestReadyCmd_RunE_JSON_StaysFlat_WithParentStructure in
// ready_runE_test.go, which drives RunE itself. This test is kept only as a
// smaller-scoped check on outputItemsJSON in isolation.
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

// TestPrintReadyTree_ReadyRootNotDoublePrinted covers ready-e88 rework
// challenge 3: an epic ROOT can itself be ready (unblocked, not just a
// closed/blocked aggregator) -- nothing about parent_id grouping depends on
// dep-block status, and the whole premise of this feature is that grouping
// is independent of readiness. Before the fix, printReadyTree printed such a
// root once as the header row (via g.root) and AGAIN as one of its own
// children rows (since findReadyRoot(root)==root put it in g.children too),
// and the "(N ready)" count included it. This asserts: the root's ID appears
// in the output EXACTLY ONCE, the header count reflects only the OTHER ready
// children (not the root itself), and none of the root's sibling children go
// missing.
func TestPrintReadyTree_ReadyRootNotDoublePrinted(t *testing.T) {
	root := mkItem("epic-live-root", "", "inbox", "Unblocked epic, itself ready")
	child1 := mkItem("epic-live-child-1", "epic-live-root", "inbox", "Child 1")
	child2 := mkItem("epic-live-child-2", "epic-live-root", "inbox", "Child 2")
	child3 := mkItem("epic-live-child-3", "epic-live-root", "inbox", "Child 3")

	// root is READY (status inbox, in the items slice passed to printReadyTree),
	// exactly the case the guard "len(g.children)==1 && children[0]==root"
	// alone cannot catch once other ready children exist alongside it.
	items := []*state.Item{root, child1, child2, child3}
	allItems := items

	groups := buildReadyGroups(items, byIDFrom(allItems))
	if len(groups) != 1 || groups[0].root.ID != "epic-live-root" {
		t.Fatalf("expected a single group rooted at epic-live-root, got %+v", groups)
	}
	if len(groups[0].children) != 4 {
		t.Fatalf("expected root+3 children (4 total) grouped together, got %d", len(groups[0].children))
	}

	output := captureStdoutPipe(t, func() { printReadyTree(items, allItems) })

	count := strings.Count(output, "epic-live-root")
	if count != 1 {
		t.Fatalf("expected root ID 'epic-live-root' to appear EXACTLY ONCE (header only, not also as a child row), appeared %d times:\n%s", count, output)
	}
	// 3 non-root children, under headerThreshold (7) -- this group inlines
	// (no header at all, per challenge 4's rule), so all 4 items (root +
	// 3 children) print as plain rows, one line each. The root is NOT
	// duplicated: it appears once, as its own row, same as any other item.
	lines := countNonEmptyLines(output)
	if lines != 4 {
		t.Fatalf("expected exactly 4 lines (root + 3 children, each once, no header since under headerThreshold), got %d:\n%s", lines, output)
	}
	for _, id := range []string{"epic-live-child-1", "epic-live-child-2", "epic-live-child-3"} {
		if strings.Count(output, id) != 1 {
			t.Errorf("expected child %q to appear exactly once, output:\n%s", id, output)
		}
	}
}

// TestPrintReadyTree_ReadyRootWithManyChildren_HeaderCountExcludesRoot covers
// the same challenge-3 defect but past headerThreshold, where the header IS
// printed: the "(N ready)" count must reflect only the children distinct
// from the root, not len(g.children) (which would include the root and
// over-count by one), and the root must still not appear as one of the
// printed/collapsed child rows.
func TestPrintReadyTree_ReadyRootWithManyChildren_HeaderCountExcludesRoot(t *testing.T) {
	root := mkItem("epic-big-root", "", "active", "Unblocked big epic, itself ready")
	var items []*state.Item
	items = append(items, root)
	for i := 0; i < 8; i++ {
		items = append(items, mkItem(fmt.Sprintf("epic-big-child-%d", i), "epic-big-root", "inbox", "Child"))
	}
	allItems := items // 1 root + 8 children = 9 total ready items

	output := captureStdoutPipe(t, func() { printReadyTree(items, allItems) })

	// 8 non-root children >= headerThreshold(7): header form used.
	// Header count must be 8 (the children), NOT 9 (children+root).
	if !strings.Contains(output, "(8 ready)") {
		t.Errorf("expected header count '(8 ready)' (8 non-root children, root excluded), output:\n%s", output)
	}
	if strings.Contains(output, "(9 ready)") {
		t.Errorf("header count must NOT include the root itself (would read '(9 ready)'), output:\n%s", output)
	}
	// The root ID legitimately appears twice: once in the header row, once in
	// the "+N more" hint's "rd dep tree <root>" pointer -- neither is a
	// duplicate ITEM ROW. What must NOT happen is the root appearing as one
	// of the indented child rows (4-space indent, formatItemRow's shape).
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "    epic-big-root") {
			t.Errorf("root must not be printed as one of its own indented child rows: %q\nfull output:\n%s", line, output)
		}
	}
	headerLines := 0
	for _, line := range strings.Split(output, "\n") {
		if strings.HasPrefix(line, "epic-big-root  (") {
			headerLines++
		}
	}
	if headerLines != 1 {
		t.Errorf("expected exactly 1 header line for epic-big-root, found %d, output:\n%s", headerLines, output)
	}
	// 8 children, cap 5 -> shown 5 + "+3 more".
	if !strings.Contains(output, "+3 more") {
		t.Errorf("expected '+3 more' (8 children, cap 5), output:\n%s", output)
	}
}

// byIDFrom builds the byID index buildReadyGroups/findReadyRoot expect,
// covering every status (not just ready) -- a small helper so tests don't
// repeat the map-building loop printReadyTree itself does internally.
func byIDFrom(allItems []*state.Item) map[string]*state.Item {
	byID := make(map[string]*state.Item, len(allItems))
	for _, it := range allItems {
		byID[it.ID] = it
	}
	return byID
}

// TestPrintReadyTree_ManySmallEpics_NeverLongerThanFlat covers ready-e88
// rework challenge 4: a board of MANY SMALL epics (each at or under
// headerThreshold) is the plausible end state of the very reparenting work
// this item serves (parent_id triage landed on ~88 open items across 3
// epics; a healthier tree splits into many smaller epics over time, not
// fewer/bigger ones). Under the pre-rework rule (always print a header for
// any non-single-item group), a group of N<=maxChildrenPerEpic children
// costs N+1 lines -- one MORE than flat's N -- and that +1 compounds per
// epic: 12 epics of 2 children each would have rendered 12*3=36 lines
// against flat's 24, a tree LONGER than the list it replaces. The fix
// (inline any group at/under headerThreshold, see its doc) makes this
// fixture -- deliberately ALL-small-groups -- render EXACTLY as many lines
// as flat: no header anywhere, since no group reaches the threshold.
func TestPrintReadyTree_ManySmallEpics_NeverLongerThanFlat(t *testing.T) {
	const numEpics = 12
	const childrenPerEpic = 2

	var items []*state.Item
	var allItems []*state.Item
	for e := 0; e < numEpics; e++ {
		epicID := fmt.Sprintf("small-epic-%02d", e)
		epic := mkItem(epicID, "", "blocked", fmt.Sprintf("Small Epic %d", e))
		allItems = append(allItems, epic)
		for c := 0; c < childrenPerEpic; c++ {
			it := mkItem(fmt.Sprintf("%s-child-%d", epicID, c), epicID, "inbox", "Small child")
			items = append(items, it)
			allItems = append(allItems, it)
		}
	}

	if len(items) != numEpics*childrenPerEpic {
		t.Fatalf("test setup: expected %d ready items, got %d", numEpics*childrenPerEpic, len(items))
	}

	flatOutput := captureStdoutPipe(t, func() { printItemTable(items) })
	treeOutput := captureStdoutPipe(t, func() { printReadyTree(items, allItems) })

	flatLines := countNonEmptyLines(flatOutput)
	treeLines := countNonEmptyLines(treeOutput)

	if flatLines != numEpics*childrenPerEpic {
		t.Fatalf("flat baseline: expected %d lines, got %d", numEpics*childrenPerEpic, flatLines)
	}
	if treeLines > flatLines {
		t.Fatalf("tree output (%d lines) is LONGER than flat (%d lines) for a board of many small epics -- exactly the regression ready-e88's rework must prevent:\n%s", treeLines, flatLines, treeOutput)
	}
	if treeLines != flatLines {
		t.Fatalf("expected tree to equal flat's line count (%d) exactly when every epic is at/under headerThreshold (%d), got %d:\n%s", flatLines, headerThreshold, treeLines, treeOutput)
	}
	// No epic header should have been printed at all -- every group is small
	// enough to inline. Child IDs are prefixed with their epic's ID
	// (small-epic-NN-child-M), so a raw substring check for the epic ID would
	// false-positive on every child row; check for the HEADER's specific
	// "<id>  (" pattern instead (formatItemRow's row format never produces
	// two spaces followed by an open paren for these fixtures).
	for e := 0; e < numEpics; e++ {
		epicID := fmt.Sprintf("small-epic-%02d", e)
		headerPattern := epicID + "  ("
		if strings.Contains(treeOutput, headerPattern) {
			t.Errorf("small epic %q must be inlined (no header row) -- found header pattern %q in output:\n%s", epicID, headerPattern, treeOutput)
		}
	}
	if strings.Contains(treeOutput, " ready)") {
		t.Errorf("no group in this fixture should reach headerThreshold -- output must contain no '(N ready)' header at all:\n%s", treeOutput)
	}
}
