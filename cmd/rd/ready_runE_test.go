package main

// ready-e88 rework, challenge 1 & 2: these tests drive readyCmd.RunE itself
// (the full command entrypoint), not printReadyTree/outputItemsJSON in
// isolation, closing the coverage gap where the ViewReady-only,
// !flatFlag-only branch at ready.go:
//
//	if viewName == views.ViewReady && !flatFlag { printReadyTree(...) }
//
// had ZERO automated coverage: no prior test could reach it, because go
// test's own stdout is never a real terminal (piped or not, it always
// reports non-TTY), and the branch is TTY-gated. isTTYStdout (a package var,
// see ready.go) exists so these tests can force either branch deterministically.
//
// VERIFIED against the acceptance bar ("deleting that branch must FAIL the
// suite"): the branch was deleted (collapsing the if/else to an unconditional
// printItemTable call) and `go test ./cmd/rd/... -run TestReadyCmd_RunE`
// was run -- TestReadyCmd_RunE_TTY_PrintsGroupedTree failed (line count 8,
// wanted 7; no epic header substring) as expected, then the branch was
// restored and the suite went green again. See this item's test_decisions
// for the exact commands.

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// resetReadyRunFlags sets --for/--flat/--view to known values before a RunE
// call and returns a func that restores them to the same known-clean state
// (readyCmd is a package-level singleton shared across tests in this
// package, so leaking a flag value from one test would bleed into the next).
func resetReadyRunFlags(t *testing.T) func() {
	t.Helper()
	set := func() {
		if err := readyCmd.Flags().Set("for", ""); err != nil {
			t.Fatalf("setting --for: %v", err)
		}
		if err := readyCmd.Flags().Set("flat", "false"); err != nil {
			t.Fatalf("setting --flat: %v", err)
		}
		if err := readyCmd.Flags().Set("view", "ready"); err != nil {
			t.Fatalf("setting --view: %v", err)
		}
	}
	set()
	return set
}

// buildTreeShapedProject publishes one blocked/closed epic-style root plus n
// ready children under it, returning the project dir. n children puts the
// group's displayChildren count at n, which the caller chooses relative to
// headerThreshold to land in either the header or inline render branch.
func buildTreeShapedProject(t *testing.T, projectName, rootID string, n int) {
	t.Helper()
	items := []*state.Item{
		{ID: rootID, Status: "blocked", Priority: "p2", Title: "Epic", For: "agent@test"},
	}
	for i := 0; i < n; i++ {
		items = append(items, &state.Item{
			ID:       fmt.Sprintf("%s-child-%02d", rootID, i),
			ParentID: rootID,
			Status:   "inbox",
			Priority: "p2",
			Title:    "Child",
			For:      "agent@test",
		})
	}
	setupNostrProjectWithItems(t, projectName, items)
}

// TestReadyCmd_RunE_TTY_PrintsGroupedTree is the headline test for challenge
// 1: it forces the TTY branch and asserts the OUTPUT SHAPE that only
// printReadyTree produces (a collapsed line count below the flat item
// count, an epic header, and a "+N more" line) -- properties printItemTable
// can never satisfy. If the `if viewName == views.ViewReady && !flatFlag`
// branch were deleted (falling through to unconditional printItemTable),
// this test fails: line count would be 8 (one per ready child), not 7, and
// neither "(8 ready)" nor "+3 more" would appear anywhere in the output.
func TestReadyCmd_RunE_TTY_PrintsGroupedTree(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }

	buildTreeShapedProject(t, "runeproject1", "runE-epic-1", 8)
	defer resetReadyRunFlags(t)()

	output := captureStdoutPipe(t, func() {
		if err := readyCmd.RunE(readyCmd, []string{}); err != nil {
			t.Fatalf("readyCmd.RunE: %v", err)
		}
	})

	lines := countNonEmptyLines(output)
	// 8 children >= headerThreshold(7): header form. 1 header + 5 shown + 1
	// "+3 more" = 7 lines. The root is status=blocked (never ready), so flat
	// would print exactly 8 lines (one per ready child) -- a printItemTable
	// fallback (i.e. the branch under test deleted) would fail every
	// assertion below.
	if lines != 7 {
		t.Fatalf("expected 7 lines from the grouped-tree render (1 header + 5 shown + 1 more), got %d -- printReadyTree branch may not be reachable:\n%s", lines, output)
	}
	if !strings.Contains(output, "(8 ready)") {
		t.Errorf("expected epic header to report '(8 ready)', output:\n%s", output)
	}
	if !strings.Contains(output, "+3 more") {
		t.Errorf("expected '+3 more' (8 children, cap 5), output:\n%s", output)
	}
	if lines >= 8 {
		t.Fatalf("tree output (%d lines) is not shorter than the flat 8-item equivalent", lines)
	}
}

// TestReadyCmd_RunE_TTY_Flat_PrintsFlatTable verifies --flat restores the
// pre-ready-e88 behavior through the SAME entrypoint: with the TTY branch
// forced but --flat set, RunE must call printItemTable (one line per ready
// item, no header, no "more" collapsing) rather than printReadyTree.
func TestReadyCmd_RunE_TTY_Flat_PrintsFlatTable(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }

	buildTreeShapedProject(t, "runeproject2", "runE-epic-2", 8)
	reset := resetReadyRunFlags(t)
	if err := readyCmd.Flags().Set("flat", "true"); err != nil {
		t.Fatalf("setting --flat=true: %v", err)
	}
	defer reset()

	output := captureStdoutPipe(t, func() {
		if err := readyCmd.RunE(readyCmd, []string{}); err != nil {
			t.Fatalf("readyCmd.RunE: %v", err)
		}
	})

	lines := countNonEmptyLines(output)
	if lines != 8 {
		t.Fatalf("expected 8 lines (one per ready child, flat, no grouping) under --flat, got %d:\n%s", lines, output)
	}
	if strings.Contains(output, "ready)") {
		t.Errorf("--flat output must not contain an epic header ('N ready)'), output:\n%s", output)
	}
	if strings.Contains(output, "more") {
		t.Errorf("--flat output must not contain a '+N more' collapse line, output:\n%s", output)
	}
}

// TestReadyCmd_RunE_JSON_StaysFlat_WithParentStructure replaces the
// tautological TestOutputItemsJSON_UnchangedByParentGrouping as the
// authoritative regression gate for constraint 1 (ready-e88 rework,
// challenge 2): it drives RunE ITSELF with --json over items that carry real
// parent_id structure (an epic + 3 children, the shape that triggers
// grouping on the TTY path), and asserts the JSON RunE actually emits is a
// flat array -- the only path that could catch RunE's --json branch being
// rewired to nest by parent, which the old test (calling outputItemsJSON
// directly on a hand-built slice never touched by this commit) could not.
func TestReadyCmd_RunE_JSON_StaysFlat_WithParentStructure(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()

	// isTTYStdout is irrelevant to the --json path (it returns before the
	// TTY check in RunE) but pin it anyway so this test can't accidentally
	// depend on go test's real terminal-ness.
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }

	// buildTreeShapedProject (via setupNostrCmdTest) resets jsonOutput=false
	// as part of its own isolation -- set jsonOutput=true AFTER project setup,
	// immediately before the RunE call, or setup silently clobbers it back.
	buildTreeShapedProject(t, "runeproject3", "runE-epic-json", 3)
	jsonOutput = true
	defer resetReadyRunFlags(t)()

	output := captureStdoutPipe(t, func() {
		if err := readyCmd.RunE(readyCmd, []string{}); err != nil {
			t.Fatalf("readyCmd.RunE: %v", err)
		}
	})

	var decoded []map[string]interface{}
	if err := json.Unmarshal([]byte(output), &decoded); err != nil {
		t.Fatalf("rd ready --json output is not a flat JSON array: %v\noutput:\n%s", err, output)
	}
	// 1 root (blocked, not ready) + 3 ready children -> the ready set is
	// exactly the 3 children; the root must not appear at all (it fails
	// ReadyFilter), and there must be no nested "children" key.
	if len(decoded) != 3 {
		t.Fatalf("expected 3 top-level entries (the 3 ready children, flat), got %d:\noutput:\n%s", len(decoded), output)
	}
	for i, entry := range decoded {
		if _, hasChildren := entry["children"]; hasChildren {
			t.Errorf("entry %d: unexpected 'children' key -- rd ready --json must stay a flat array of Item, not a nested tree:\n%s", i, output)
		}
		id, _ := entry["id"].(string)
		if !strings.HasPrefix(id, "runE-epic-json-child-") {
			t.Errorf("entry %d: expected a child id, got %v", i, entry["id"])
		}
	}
}

// TestSortByPriorityETA_DeterministicTiebreak covers ready-e88 rework
// challenge 5: sortByPriorityETA now breaks ties on ID, making the sort a
// strict total order. Two items sharing priority and ETA (the common case
// for un-triaged items, both empty) previously sorted in whatever order
// they arrived in the slice -- and callers build that slice from a nostr
// projection whose map iteration is itself nondeterministic, which is why
// the adversary observed differing piped/--json order across two live-binary
// runs with no state change in between. This test sorts the SAME item set
// starting from two DIFFERENT input orders and asserts the output order is
// identical both times.
func TestSortByPriorityETA_DeterministicTiebreak(t *testing.T) {
	mk := func(id string) *state.Item { return &state.Item{ID: id, Priority: "p1", ETA: ""} }

	orderA := []*state.Item{mk("z-item"), mk("a-item"), mk("m-item")}
	orderB := []*state.Item{mk("m-item"), mk("z-item"), mk("a-item")}

	sortByPriorityETA(orderA)
	sortByPriorityETA(orderB)

	idsOf := func(items []*state.Item) []string {
		out := make([]string, len(items))
		for i, it := range items {
			out[i] = it.ID
		}
		return out
	}

	want := []string{"a-item", "m-item", "z-item"}
	gotA := idsOf(orderA)
	gotB := idsOf(orderB)

	if strings.Join(gotA, ",") != strings.Join(want, ",") {
		t.Fatalf("sorted order from input order A: got %v, want %v", gotA, want)
	}
	if strings.Join(gotB, ",") != strings.Join(want, ",") {
		t.Fatalf("sorted order from input order B (different starting order, same item set): got %v, want %v -- sort must be deterministic regardless of input order", gotB, want)
	}
}
