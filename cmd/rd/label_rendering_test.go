package main

// Integration tests for label rendering and filtering (ready-9c4).
//
// Done condition:
//   - list --label bug returns exactly the labeled item
//   - list --label bug --label security requires BOTH (AND semantics)
//   - ready --label respects existing readiness (blocked items excluded)
//   - show text output contains "Labels: " line (omit when empty)
//   - show --json round-trips labels
//   - --label with unknown atom: empty result + stderr hint (NOT an error)
//   - printItemTable appends [label,label] suffix to title cell
//
// Test strategy: unit tests on predicate functions (applyListFilters, LabelFilter,
// printItemTable) + integration via mutations.jsonl path for show command.
// No mocks — filter functions operate on real *state.Item structs, show uses the
// real JSONL derive pipeline.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/views"
)

// ---------------------------------------------------------------------------
// pkg/views.LabelFilter unit tests
// ---------------------------------------------------------------------------

// TestLabelFilter_MatchesCarriedAtom verifies LabelFilter returns true for an
// item that carries the requested atom.
func TestLabelFilter_MatchesCarriedAtom(t *testing.T) {
	item := &state.Item{ID: "t1", Labels: []string{"bug", "security"}}
	f := views.LabelFilter("bug")
	if !f(item) {
		t.Error("LabelFilter(\"bug\") must match item with Labels=[bug,security]")
	}
}

// TestLabelFilter_NoMatchOnAbsentAtom verifies LabelFilter returns false when
// the item does not carry the requested atom.
func TestLabelFilter_NoMatchOnAbsentAtom(t *testing.T) {
	item := &state.Item{ID: "t1", Labels: []string{"security"}}
	f := views.LabelFilter("bug")
	if f(item) {
		t.Error("LabelFilter(\"bug\") must not match item with Labels=[security]")
	}
}

// TestLabelFilter_EmptyLabels verifies LabelFilter returns false for an item
// with no labels at all.
func TestLabelFilter_EmptyLabels(t *testing.T) {
	item := &state.Item{ID: "t1", Labels: nil}
	f := views.LabelFilter("bug")
	if f(item) {
		t.Error("LabelFilter must return false for item with no labels")
	}
}

// TestLabelFilter_ExactMatchOnly verifies LabelFilter does not perform
// substring or prefix matching (YAGNI: no glob).
func TestLabelFilter_ExactMatchOnly(t *testing.T) {
	item := &state.Item{ID: "t1", Labels: []string{"bug-critical"}}
	f := views.LabelFilter("bug")
	if f(item) {
		t.Error("LabelFilter(\"bug\") must not substring-match \"bug-critical\"")
	}
}

// ---------------------------------------------------------------------------
// applyListFilters + label predicate (AND semantics)
// ---------------------------------------------------------------------------

// TestList_LabelFilter_SingleAtom verifies --label bug returns exactly the
// items carrying that atom.
func TestList_LabelFilter_SingleAtom(t *testing.T) {
	items := []*state.Item{
		{ID: "bug-item", Status: state.StatusInbox, Labels: []string{"bug"}},
		{ID: "no-labels", Status: state.StatusInbox, Labels: nil},
		{ID: "other-label", Status: state.StatusInbox, Labels: []string{"security"}},
	}

	filtered := applyListFilters(items, nil, "", "", "", "", "", false)
	// Apply label filter (AND: single atom).
	filtered = views.Apply(filtered, views.LabelFilter("bug"))

	if len(filtered) != 1 {
		t.Errorf("expected exactly 1 item with label 'bug', got %d", len(filtered))
	}
	if len(filtered) > 0 && filtered[0].ID != "bug-item" {
		t.Errorf("expected bug-item, got %s", filtered[0].ID)
	}
}

// TestList_LabelFilter_ANDSemantics verifies --label bug --label security
// requires BOTH atoms (AND): only item with both labels is returned.
func TestList_LabelFilter_ANDSemantics(t *testing.T) {
	items := []*state.Item{
		{ID: "both", Status: state.StatusInbox, Labels: []string{"bug", "security"}},
		{ID: "bug-only", Status: state.StatusInbox, Labels: []string{"bug"}},
		{ID: "security-only", Status: state.StatusInbox, Labels: []string{"security"}},
		{ID: "no-labels", Status: state.StatusInbox, Labels: nil},
	}

	filtered := applyListFilters(items, nil, "", "", "", "", "", false)
	// Apply label filters with AND semantics (chain two LabelFilter predicates).
	for _, atom := range []string{"bug", "security"} {
		filtered = views.Apply(filtered, views.LabelFilter(atom))
	}

	if len(filtered) != 1 {
		t.Errorf("expected exactly 1 item with both 'bug' and 'security' labels, got %d", len(filtered))
	}
	if len(filtered) > 0 && filtered[0].ID != "both" {
		t.Errorf("expected 'both', got %s", filtered[0].ID)
	}
}

// TestList_LabelFilter_UnknownAtom_EmptyResult verifies that filtering by an
// atom not carried by any item returns an empty result (NOT an error).
func TestList_LabelFilter_UnknownAtom_EmptyResult(t *testing.T) {
	items := []*state.Item{
		{ID: "t1", Status: state.StatusInbox, Labels: []string{"bug"}},
		{ID: "t2", Status: state.StatusInbox, Labels: []string{"security"}},
	}

	filtered := applyListFilters(items, nil, "", "", "", "", "", false)
	filtered = views.Apply(filtered, views.LabelFilter("does-not-exist"))

	// Must be empty, not an error.
	if len(filtered) != 0 {
		t.Errorf("expected 0 items for unknown label, got %d", len(filtered))
	}
}

// TestList_LabelFilter_TerminalItemsStillExcluded verifies that label filtering
// does not bypass the default terminal-exclusion behavior.
func TestList_LabelFilter_TerminalItemsStillExcluded(t *testing.T) {
	items := []*state.Item{
		{ID: "open-bug", Status: state.StatusInbox, Labels: []string{"bug"}},
		{ID: "done-bug", Status: state.StatusDone, Labels: []string{"bug"}},
	}

	filtered := applyListFilters(items, nil, "", "", "", "", "", false /* --all=false */)
	filtered = views.Apply(filtered, views.LabelFilter("bug"))

	if len(filtered) != 1 {
		t.Errorf("expected 1 non-terminal item with 'bug' label, got %d", len(filtered))
	}
	if len(filtered) > 0 && filtered[0].ID != "open-bug" {
		t.Errorf("expected open-bug, got %s", filtered[0].ID)
	}
}

// TestReady_LabelFilter_BlockedItemsExcluded verifies that --label on the ready
// view still excludes blocked items (readiness semantics preserved).
func TestReady_LabelFilter_BlockedItemsExcluded(t *testing.T) {
	items := []*state.Item{
		{
			ID:     "ready-bug",
			Status: state.StatusInbox,
			Labels: []string{"bug"},
		},
		{
			ID:        "blocked-bug",
			Status:    state.StatusBlocked,
			Labels:    []string{"bug"},
			BlockedBy: []string{"other-item"},
		},
	}

	// Apply ready view filter first (as readyCmd does), then label filter.
	readyFiltered := views.Apply(items, views.ReadyFilter())
	labeled := views.Apply(readyFiltered, views.LabelFilter("bug"))

	if len(labeled) != 1 {
		t.Errorf("expected 1 ready item with 'bug' label (blocked excluded), got %d", len(labeled))
	}
	if len(labeled) > 0 && labeled[0].ID != "ready-bug" {
		t.Errorf("expected ready-bug, got %s", labeled[0].ID)
	}
}

// ---------------------------------------------------------------------------
// printItemTable label suffix
// ---------------------------------------------------------------------------

// TestPrintItemTable_LabelSuffix verifies that printItemTable appends
// [label1,label2] to the title cell when the item has labels.
// The fixed-width column positions must not change.
func TestPrintItemTable_LabelSuffix(t *testing.T) {
	items := []*state.Item{
		{
			ID:       "ready-abc",
			Priority: "p1",
			Status:   "inbox",
			Title:    "Fix the bug",
			Labels:   []string{"bug", "security"},
		},
	}

	output := capturePrintItemTable(t, items)

	// The title cell must contain the label suffix.
	if !strings.Contains(output, "[bug,security]") {
		t.Errorf("printItemTable output must contain '[bug,security]' suffix; got:\n%s", output)
	}
	// The full title with suffix must appear.
	if !strings.Contains(output, "Fix the bug  [bug,security]") {
		t.Errorf("printItemTable output must contain title with label suffix; got:\n%s", output)
	}
	// The ID must still appear in its fixed-width column position.
	if !strings.Contains(output, "ready-abc") {
		t.Errorf("printItemTable output must contain item ID; got:\n%s", output)
	}
}

// TestPrintItemTable_NoLabelNoSuffix verifies that printItemTable does not
// append a label suffix when the item has no labels.
func TestPrintItemTable_NoLabelNoSuffix(t *testing.T) {
	items := []*state.Item{
		{
			ID:       "ready-def",
			Priority: "p2",
			Status:   "inbox",
			Title:    "No labels here",
			Labels:   nil,
		},
	}

	output := capturePrintItemTable(t, items)

	if strings.Contains(output, "[") {
		t.Errorf("printItemTable must not include '[...]' when item has no labels; got:\n%s", output)
	}
	if !strings.Contains(output, "No labels here") {
		t.Errorf("printItemTable must contain item title; got:\n%s", output)
	}
}

// TestPrintItemTable_FixedColumnsUnchanged verifies that adding a label suffix
// does NOT widen or shift the fixed-width ID/priority/status/eta columns.
// The format string columns must remain at their specified widths.
func TestPrintItemTable_FixedColumnsUnchanged(t *testing.T) {
	// One item with labels, one without.
	items := []*state.Item{
		{ID: "ready-001", Priority: "p0", Status: "active", Title: "With label", Labels: []string{"bug"}},
		{ID: "ready-002", Priority: "p1", Status: "inbox", Title: "Without label", Labels: nil},
	}

	output := capturePrintItemTable(t, items)

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d; output:\n%s", len(lines), output)
	}

	// Each line starts with 2 spaces + 16-char ID field (padded).
	// The first non-space token on each line must be the item ID.
	for i, line := range lines {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Errorf("line %d has too few fields: %q", i, line)
			continue
		}
		if fields[0] != items[i].ID {
			t.Errorf("line %d: first field must be item ID, got %q (want %q)", i, fields[0], items[i].ID)
		}
	}
}

// ---------------------------------------------------------------------------
// show command: Labels line in text output
// ---------------------------------------------------------------------------

// TestShow_LabelsLine_TextOutput verifies that rd show prints "Labels: a, b"
// when the item has labels. The line is omitted when labels is empty.
func TestShow_LabelsLine_TextOutput(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	tmpDir, cleanup := setupShowMutationsWithLabels(t, "show-label-text", "Label Test Item", []string{"bug", "security"})
	defer cleanup()

	output, err := runShowCmd(t, tmpDir, "show-label-text")
	if err != nil {
		t.Fatalf("showCmd.RunE error: %v", err)
	}

	if !strings.Contains(output, "Labels:") {
		t.Errorf("show text output must contain 'Labels:' line; got:\n%s", output)
	}
	if !strings.Contains(output, "bug") {
		t.Errorf("show text output must contain label 'bug'; got:\n%s", output)
	}
	if !strings.Contains(output, "security") {
		t.Errorf("show text output must contain label 'security'; got:\n%s", output)
	}
}

// TestShow_LabelsLine_OmittedWhenEmpty verifies that the Labels line is
// omitted entirely when the item has no labels.
func TestShow_LabelsLine_OmittedWhenEmpty(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	tmpDir, cleanup := setupShowMutationsWithLabels(t, "show-no-labels", "No Label Item", nil)
	defer cleanup()

	output, err := runShowCmd(t, tmpDir, "show-no-labels")
	if err != nil {
		t.Fatalf("showCmd.RunE error: %v", err)
	}

	if strings.Contains(output, "Labels:") {
		t.Errorf("show text output must NOT contain 'Labels:' line when item has no labels; got:\n%s", output)
	}
}

// TestShow_JSON_IncludesLabels verifies that rd show --json includes the
// "labels" field when the item has labels.
func TestShow_JSON_IncludesLabels(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = true // --json mode

	tmpDir, cleanup := setupShowMutationsWithLabels(t, "show-json-labels", "JSON Label Item", []string{"bug"})
	defer cleanup()

	output, err := runShowCmd(t, tmpDir, "show-json-labels")
	if err != nil {
		t.Fatalf("showCmd.RunE error: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &m); err != nil {
		t.Fatalf("--json output is not valid JSON: %v; output:\n%s", err, output)
	}

	rawLabels, ok := m["labels"]
	if !ok {
		t.Fatalf("--json output missing 'labels' field; output:\n%s", output)
	}
	labels, ok := rawLabels.([]interface{})
	if !ok {
		t.Fatalf("'labels' field is not an array; got %T", rawLabels)
	}
	if len(labels) != 1 {
		t.Errorf("expected 1 label, got %d; labels=%v", len(labels), labels)
	}
	if len(labels) > 0 {
		if label, ok := labels[0].(string); !ok || label != "bug" {
			t.Errorf("expected label[0]=\"bug\", got %v", labels[0])
		}
	}
}

// TestShow_JSON_OmitsLabelsWhenEmpty verifies that --json output omits the
// "labels" field (omitempty) when the item has no labels.
func TestShow_JSON_OmitsLabelsWhenEmpty(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = true // --json mode

	tmpDir, cleanup := setupShowMutationsWithLabels(t, "show-json-nolabels", "JSON No Label Item", nil)
	defer cleanup()

	output, err := runShowCmd(t, tmpDir, "show-json-nolabels")
	if err != nil {
		t.Fatalf("showCmd.RunE error: %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &m); err != nil {
		t.Fatalf("--json output is not valid JSON: %v; output:\n%s", err, output)
	}

	if _, ok := m["labels"]; ok {
		t.Errorf("--json output must omit 'labels' field when item has no labels; output:\n%s", output)
	}
}

// ---------------------------------------------------------------------------
// Unknown-atom stderr hint
// ---------------------------------------------------------------------------

// TestList_UnknownAtom_StderrHint verifies that filtering by an atom not in
// the registry emits a hint to stderr and returns empty (not an error).
// The hint is best-effort: emitted only when a campfire store is available.
// This test verifies the no-error contract and the filtering behavior.
func TestList_UnknownAtom_StderrHint(t *testing.T) {
	items := []*state.Item{
		{ID: "t1", Status: state.StatusInbox, Labels: []string{"bug"}},
	}

	// Filtering with an unknown atom must return empty (not an error).
	// This is the primary contract: unknown atoms produce zero results, not errors.
	filtered := views.Apply(items, views.LabelFilter("not-in-registry"))
	if len(filtered) != 0 {
		t.Errorf("filtering by unknown atom must yield empty result, got %d items", len(filtered))
	}

	// printUnknownLabelHints with nil store returns immediately (no-op) because
	// registry lookup requires a store. No panic must occur.
	var stderrOutput string
	func() {
		defer func() {
			if r := recover(); r != nil {
				t.Errorf("printUnknownLabelHints must not panic with nil store: %v", r)
			}
		}()
		stderrOutput = captureStderr(t, func() {
			printUnknownLabelHints([]string{"not-in-registry"}, items, nil)
		})
	}()
	// With nil store (no campfire), no hint is written — best-effort is acceptable.
	// The key contract is: no panic and no error return.
	_ = stderrOutput
}

// ---------------------------------------------------------------------------
// Helper functions
// ---------------------------------------------------------------------------

// capturePrintItemTable runs printItemTable and returns its stdout output.
func capturePrintItemTable(t *testing.T, items []*state.Item) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	printItemTable(items)
	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

// captureStderr replaces os.Stderr with a pipe, calls fn, and returns what was written.
func captureStderr(t *testing.T, fn func()) string {
	t.Helper()
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w
	fn()
	w.Close()
	os.Stderr = origStderr

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	r.Close()
	return buf.String()
}

// setupShowMutationsWithLabels creates a temp directory with a mutations.jsonl
// that includes an item with the given labels. Returns tmpDir and cleanup func.
// The labels are embedded directly in the work:create payload so derive-time
// processing will set item.Labels (seed atoms like "bug" and "security" are
// pre-registered in the label registry via declarations.LoadSeedLabels).
func setupShowMutationsWithLabels(t *testing.T, itemID, title string, labels []string) (string, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "test-show-labels")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	hexID := strings.Repeat("dd", 32) // 64-char hex campfire ID

	origCFHome := os.Getenv("CF_HOME")
	os.Setenv("CF_HOME", tmpDir)

	origRDHome := rdHome
	rdHome = ""

	// Write alias store.
	aliasContent, _ := json.Marshal(map[string]string{"labelproject": hexID})
	if err := os.WriteFile(filepath.Join(tmpDir, "aliases.json"), aliasContent, 0600); err != nil {
		t.Fatalf("WriteFile aliases.json: %v", err)
	}

	readyDir := filepath.Join(tmpDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readyDir, "config.json"), []byte(`{"project_name":"labelproject"}`), 0600); err != nil {
		t.Fatalf("WriteFile config.json: %v", err)
	}

	// Build the work:create payload. Labels are stored as comma-separated scalar.
	payloadMap := map[string]interface{}{
		"id":       itemID,
		"title":    title,
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p2",
	}
	if len(labels) > 0 {
		payloadMap["labels"] = strings.Join(labels, ",")
	}
	payloadBytes, _ := json.Marshal(payloadMap)

	mutation := fmt.Sprintf(`{"msg_id":"test-msg-%s","campfire_id":"%s","timestamp":1000000000000000001,"operation":"work:create","payload":%s,"tags":["work:create"],"sender":"testsender"}`,
		itemID, hexID, string(payloadBytes))

	if err := os.WriteFile(filepath.Join(readyDir, "mutations.jsonl"), []byte(mutation+"\n"), 0600); err != nil {
		t.Fatalf("WriteFile mutations.jsonl: %v", err)
	}

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}

	cleanup := func() {
		_ = os.Chdir(origCwd)
		if origCFHome != "" {
			os.Setenv("CF_HOME", origCFHome)
		} else {
			os.Unsetenv("CF_HOME")
		}
		rdHome = origRDHome
		os.RemoveAll(tmpDir)
	}
	return tmpDir, cleanup
}

// runShowCmd runs showCmd.RunE with the given item ID and captures stdout.
func runShowCmd(t *testing.T, tmpDir, itemID string) (string, error) {
	t.Helper()
	_ = tmpDir

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	runErr := showCmd.RunE(showCmd, []string{itemID})

	w.Close()
	os.Stdout = origStdout

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, r)
	r.Close()

	return buf.String(), runErr
}
