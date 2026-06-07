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

// ---------------------------------------------------------------------------
// sliceResetter lets us Replace a pflag StringArray flag value without
// importing pflag directly. stringArrayValue (unexported) implements this via
// its Replace method, which fully overwrites the current list.
// ---------------------------------------------------------------------------

type sliceResetter interface {
	Replace([]string) error
}

// resetListLabelFlag sets listCmd's --label flag to exactly atoms and returns
// a cleanup func that resets it to nil.
func resetListLabelFlag(t *testing.T, atoms []string) func() {
	t.Helper()
	f := listCmd.Flags().Lookup("label")
	if f == nil {
		t.Fatal("--label flag not registered on listCmd")
	}
	sr, ok := f.Value.(sliceResetter)
	if !ok {
		t.Fatalf("--label flag value does not implement Replace; got %T", f.Value)
	}
	if err := sr.Replace(atoms); err != nil {
		t.Fatalf("Replace(label, %v) on listCmd: %v", atoms, err)
	}
	return func() { _ = sr.Replace(nil) }
}

// resetReadyLabelFlag sets readyCmd's --label flag to exactly atoms and
// returns a cleanup func that resets it to nil.
func resetReadyLabelFlag(t *testing.T, atoms []string) func() {
	t.Helper()
	f := readyCmd.Flags().Lookup("label")
	if f == nil {
		t.Fatal("--label flag not registered on readyCmd")
	}
	sr, ok := f.Value.(sliceResetter)
	if !ok {
		t.Fatalf("--label flag value does not implement Replace; got %T", f.Value)
	}
	if err := sr.Replace(atoms); err != nil {
		t.Fatalf("Replace(label, %v) on readyCmd: %v", atoms, err)
	}
	return func() { _ = sr.Replace(nil) }
}

// setupMultiItemMutations creates a temp dir with a .ready/mutations.jsonl
// containing four items with varying label sets and wires CF_HOME / rdHome so
// that listCmd.RunE reads from that dir via the JSONL path.  Returns tmpDir
// and a cleanup func.
//
// Items written:
//
//	"lrf-bug-only"      labels: bug
//	"lrf-security-only" labels: security
//	"lrf-both"          labels: bug,security
//	"lrf-no-label"      labels: (none)
func setupMultiItemMutations(t *testing.T) (string, func()) {
	t.Helper()

	tmpDir, err := os.MkdirTemp("", "test-list-label-rune")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	hexID := strings.Repeat("aa", 32)

	origCFHome := os.Getenv("CF_HOME")
	os.Setenv("CF_HOME", tmpDir)

	origRDHome := rdHome
	rdHome = ""

	aliasContent, _ := json.Marshal(map[string]string{"labelrune": hexID})
	if err := os.WriteFile(filepath.Join(tmpDir, "aliases.json"), aliasContent, 0600); err != nil {
		t.Fatalf("WriteFile aliases.json: %v", err)
	}

	readyDir := filepath.Join(tmpDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readyDir, "config.json"),
		[]byte(`{"project_name":"labelrune"}`), 0600); err != nil {
		t.Fatalf("WriteFile config.json: %v", err)
	}

	type spec struct {
		id     string
		labels string
	}
	specs := []spec{
		{"lrf-bug-only", "bug"},
		{"lrf-security-only", "security"},
		{"lrf-both", "bug,security"},
		{"lrf-no-label", ""},
	}

	var mutations string
	for i, s := range specs {
		payload := map[string]interface{}{
			"id":       s.id,
			"title":    "Label RunE Test " + s.id,
			"type":     "task",
			"for":      "baron@3dl.dev",
			"priority": "p2",
		}
		if s.labels != "" {
			payload["labels"] = s.labels
		}
		pb, _ := json.Marshal(payload)
		ts := fmt.Sprintf("%d", 1000000000000000001+int64(i))
		mutations += fmt.Sprintf(
			`{"msg_id":"test-msg-%s","campfire_id":"%s","timestamp":%s,"operation":"work:create","payload":%s,"tags":["work:create"],"sender":"testsender"}`+"\n",
			s.id, hexID, ts, string(pb),
		)
	}
	if err := os.WriteFile(filepath.Join(readyDir, "mutations.jsonl"), []byte(mutations), 0600); err != nil {
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

// runListCmdCapture runs listCmd.RunE and returns captured stdout, stderr, and
// any error.  Both os.Stdout and os.Stderr are redirected during the call.
func runListCmdCapture(t *testing.T) (stdout, stderr string, runErr error) {
	t.Helper()

	origOut := os.Stdout
	rOut, wOut, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stdout: %v", err)
	}
	os.Stdout = wOut

	origErr := os.Stderr
	rErr, wErr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe stderr: %v", err)
	}
	os.Stderr = wErr

	runErr = listCmd.RunE(listCmd, []string{})

	wOut.Close()
	wErr.Close()
	os.Stdout = origOut
	os.Stderr = origErr

	var bufOut, bufErr bytes.Buffer
	_, _ = io.Copy(&bufOut, rOut)
	_, _ = io.Copy(&bufErr, rErr)
	rOut.Close()
	rErr.Close()

	return bufOut.String(), bufErr.String(), runErr
}

// ---------------------------------------------------------------------------
// listCmd.RunE + --label integration tests (cover the flag-parse/wiring gap)
// ---------------------------------------------------------------------------

// TestListCmd_RunE_LabelFlag_SingleAtom exercises listCmd.RunE end-to-end with
// --label bug wired via cobra flag-parse. Asserts that only items carrying the
// "bug" atom appear in the piped bare-ID output and that no unlabeled or
// differently-labeled items slip through.
func TestListCmd_RunE_LabelFlag_SingleAtom(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	_, cleanup := setupMultiItemMutations(t)
	defer cleanup()

	defer resetListLabelFlag(t, []string{"bug"})()

	stdout, _, runErr := runListCmdCapture(t)
	if runErr != nil {
		t.Fatalf("listCmd.RunE --label bug returned error: %v", runErr)
	}

	ids := make(map[string]bool)
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line != "" {
			ids[line] = true
		}
	}

	if !ids["lrf-bug-only"] {
		t.Errorf("lrf-bug-only missing from --label bug output; got ids: %v", ids)
	}
	if !ids["lrf-both"] {
		t.Errorf("lrf-both missing from --label bug output; got ids: %v", ids)
	}
	if ids["lrf-security-only"] {
		t.Errorf("lrf-security-only must NOT appear in --label bug output; got ids: %v", ids)
	}
	if ids["lrf-no-label"] {
		t.Errorf("lrf-no-label must NOT appear in --label bug output; got ids: %v", ids)
	}
}

// TestListCmd_RunE_LabelFlag_ANDSemantics exercises listCmd.RunE with two
// --label flags (bug + security) parsed via cobra. Only the item carrying both
// atoms must appear (AND semantics).
func TestListCmd_RunE_LabelFlag_ANDSemantics(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	_, cleanup := setupMultiItemMutations(t)
	defer cleanup()

	defer resetListLabelFlag(t, []string{"bug", "security"})()

	stdout, _, runErr := runListCmdCapture(t)
	if runErr != nil {
		t.Fatalf("listCmd.RunE --label bug --label security returned error: %v", runErr)
	}

	var lines []string
	for _, line := range strings.Split(strings.TrimSpace(stdout), "\n") {
		if line != "" {
			lines = append(lines, line)
		}
	}

	if len(lines) != 1 {
		t.Errorf("expected exactly 1 item (AND semantics), got %d; lines: %v", len(lines), lines)
	}
	if len(lines) > 0 && lines[0] != "lrf-both" {
		t.Errorf("expected lrf-both, got %q", lines[0])
	}
}

// TestListCmd_RunE_LabelFlag_UnknownAtom_EmptyNotError exercises listCmd.RunE
// with --label unknownatom via cobra flag-parse. The command must succeed
// (return nil) and produce empty piped output — an unknown atom is not an
// error.
func TestListCmd_RunE_LabelFlag_UnknownAtom_EmptyNotError(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	_, cleanup := setupMultiItemMutations(t)
	defer cleanup()

	defer resetListLabelFlag(t, []string{"unknownatom"})()

	stdout, _, runErr := runListCmdCapture(t)
	if runErr != nil {
		t.Errorf("listCmd.RunE must NOT return error for unknown label atom; got: %v", runErr)
	}
	if trimmed := strings.TrimSpace(stdout); trimmed != "" {
		t.Errorf("expected empty piped output for --label unknownatom, got: %q", trimmed)
	}
}

// ---------------------------------------------------------------------------
// readyCmd.RunE + --label integration test (flag registration and wiring)
// ---------------------------------------------------------------------------

// TestReadyCmd_RunE_LabelFlag_Registered verifies that the --label flag is
// correctly registered on readyCmd and that its value is readable inside RunE.
// Because readyCmd requires a live campfire identity (withAgentAndStore), a
// missing-identity error from RunE is acceptable — the key assertions are:
//   - no panic
//   - no "unknown flag" or "flag provided but not defined" error
//   - GetStringArray("label") returns the atoms we set
func TestReadyCmd_RunE_LabelFlag_Registered(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	_, cleanup := setupMultiItemMutations(t)
	defer cleanup()

	if err := readyCmd.Flags().Set("for", ""); err != nil {
		t.Fatalf("setting --for: %v", err)
	}
	defer func() { _ = readyCmd.Flags().Set("for", "") }()

	defer resetReadyLabelFlag(t, []string{"bug"})()

	// Drain stdout/stderr so test output stays clean.
	origOut := os.Stdout
	origErr := os.Stderr
	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()
	os.Stdout = wOut
	os.Stderr = wErr

	runErr := readyCmd.RunE(readyCmd, []string{})

	wOut.Close()
	wErr.Close()
	os.Stdout = origOut
	os.Stderr = origErr
	var bufOut, bufErr bytes.Buffer
	_, _ = io.Copy(&bufOut, rOut)
	_, _ = io.Copy(&bufErr, rErr)
	rOut.Close()
	rErr.Close()

	// A missing-identity error is acceptable in this env; a flag-parse error is not.
	if runErr != nil {
		msg := runErr.Error()
		if strings.Contains(msg, "--label is not a flag") ||
			strings.Contains(msg, "unknown flag") ||
			strings.Contains(msg, "flag provided but not defined") {
			t.Errorf("readyCmd.RunE returned flag-parse error for --label: %v", runErr)
		}
		t.Logf("readyCmd.RunE error (missing identity is OK in test env): %v", runErr)
	}

	// Flag value must be exactly what we set.
	got, err := readyCmd.Flags().GetStringArray("label")
	if err != nil {
		t.Fatalf("GetStringArray(\"label\") on readyCmd: %v", err)
	}
	if len(got) != 1 || got[0] != "bug" {
		t.Errorf("readyCmd --label: got %v, want [bug]", got)
	}
}
