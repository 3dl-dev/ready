package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

func makeListItem(id, status string) *state.Item {
	return &state.Item{
		ID:     id,
		Title:  "Test " + id,
		Status: status,
	}
}

// TestList_MultipleStatus_ORSemantics verifies that providing multiple --status
// flags returns items matching ANY of the given statuses (OR semantics).
// This is the core behavior of rudi-19a: rd list --status inbox --status active
// must return both inbox and active items.
func TestList_MultipleStatus_ORSemantics(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusInbox),
		makeListItem("t2", state.StatusActive),
		makeListItem("t3", state.StatusWaiting),
		makeListItem("t4", state.StatusDone),
	}

	result := applyListFilters(items, []string{state.StatusInbox, state.StatusActive}, "", "", "", "", "", false)

	if len(result) != 2 {
		t.Errorf("expected 2 items (inbox + active), got %d", len(result))
	}
	ids := map[string]bool{}
	for _, item := range result {
		ids[item.ID] = true
	}
	if !ids["t1"] {
		t.Errorf("expected t1 (inbox) in result, missing")
	}
	if !ids["t2"] {
		t.Errorf("expected t2 (active) in result, missing")
	}
	if ids["t3"] {
		t.Errorf("t3 (waiting) must not appear when not in status filter")
	}
	if ids["t4"] {
		t.Errorf("t4 (done) must not appear when not in status filter")
	}
}

// TestList_SingleStatus verifies backward compatibility: a single --status flag
// still filters to exactly that status (no regression from StringVar -> StringArrayVar).
func TestList_SingleStatus(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusInbox),
		makeListItem("t2", state.StatusActive),
		makeListItem("t3", state.StatusDone),
	}

	result := applyListFilters(items, []string{state.StatusActive}, "", "", "", "", "", false)

	if len(result) != 1 {
		t.Errorf("expected 1 item (active), got %d", len(result))
	}
	if result[0].ID != "t2" {
		t.Errorf("expected t2 (active), got %s", result[0].ID)
	}
}

// TestList_NoStatus_DefaultExcludesTerminal verifies that when no --status is
// provided, terminal items (done, cancelled, failed) are excluded by default.
// This is the existing behavior that must be preserved unchanged.
func TestList_NoStatus_DefaultExcludesTerminal(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusInbox),
		makeListItem("t2", state.StatusActive),
		makeListItem("t3", state.StatusDone),
		makeListItem("t4", state.StatusCancelled),
		makeListItem("t5", state.StatusFailed),
		makeListItem("t6", state.StatusWaiting),
	}

	result := applyListFilters(items, nil, "", "", "", "", "", false)

	// Terminal statuses must be excluded.
	for _, item := range result {
		if state.IsTerminal(item) {
			t.Errorf("terminal item %s (status=%s) must not appear in default list", item.ID, item.Status)
		}
	}
	// Non-terminal items must be included.
	if len(result) != 3 {
		t.Errorf("expected 3 non-terminal items (inbox+active+waiting), got %d", len(result))
	}
}

// TestList_NoStatus_AllFlagIncludesTerminal verifies that --all overrides the
// default terminal exclusion when no --status is given.
func TestList_NoStatus_AllFlagIncludesTerminal(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusActive),
		makeListItem("t2", state.StatusDone),
		makeListItem("t3", state.StatusCancelled),
	}

	result := applyListFilters(items, nil, "", "", "", "", "", true)

	if len(result) != 3 {
		t.Errorf("expected 3 items with --all, got %d", len(result))
	}
}

// TestList_StatusAlias_InProgress verifies that items with status=active match
// the alias filter "in_progress" (bd-compat alias).
func TestList_StatusAlias_InProgress(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusActive),
		makeListItem("t2", state.StatusInbox),
		makeListItem("t3", state.StatusDone),
	}

	result := applyListFilters(items, []string{"in_progress"}, "", "", "", "", "", false)

	if len(result) != 1 {
		t.Errorf("expected 1 item (active via in_progress alias), got %d", len(result))
	}
	if len(result) > 0 && result[0].ID != "t1" {
		t.Errorf("expected t1 (active), got %s", result[0].ID)
	}
}

// TestList_StatusAlias_Closed verifies that items with status=done match
// the alias filter "closed" (bd-compat alias).
func TestList_StatusAlias_Closed(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusActive),
		makeListItem("t2", state.StatusDone),
		makeListItem("t3", state.StatusCancelled),
	}

	result := applyListFilters(items, []string{"closed"}, "", "", "", "", "", false)

	if len(result) != 1 {
		t.Errorf("expected 1 item (done via closed alias), got %d", len(result))
	}
	if len(result) > 0 && result[0].ID != "t2" {
		t.Errorf("expected t2 (done), got %s", result[0].ID)
	}
}

// TestList_StatusAlias_MixedCanonicalAndAlias verifies that a filter containing
// both an alias ("in_progress") and a canonical status ("inbox") matches items
// of both statuses.
func TestList_StatusAlias_MixedCanonicalAndAlias(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusActive),
		makeListItem("t2", state.StatusInbox),
		makeListItem("t3", state.StatusWaiting),
		makeListItem("t4", state.StatusDone),
	}

	result := applyListFilters(items, []string{"in_progress", state.StatusInbox}, "", "", "", "", "", false)

	if len(result) != 2 {
		t.Errorf("expected 2 items (active via in_progress + inbox), got %d", len(result))
	}
	ids := map[string]bool{}
	for _, item := range result {
		ids[item.ID] = true
	}
	if !ids["t1"] {
		t.Errorf("expected t1 (active via in_progress alias)")
	}
	if !ids["t2"] {
		t.Errorf("expected t2 (inbox canonical)")
	}
	if ids["t3"] {
		t.Errorf("t3 (waiting) must not appear")
	}
}

// TestList_StatusAlias_Unknown verifies that an unknown/unrecognised filter value
// matches no items (not an alias, not a canonical status).
func TestList_StatusAlias_Unknown(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusActive),
		makeListItem("t2", state.StatusInbox),
	}

	result := applyListFilters(items, []string{"foobar"}, "", "", "", "", "", false)

	if len(result) != 0 {
		t.Errorf("expected 0 items for unknown filter, got %d", len(result))
	}
}

// TestList_MultipleStatus_IncludesTerminalExplicitly verifies that providing a
// terminal status explicitly in --status returns those items (no default exclusion
// when statuses are explicitly specified).
func TestList_MultipleStatus_IncludesTerminalExplicitly(t *testing.T) {
	items := []*state.Item{
		makeListItem("t1", state.StatusInbox),
		makeListItem("t2", state.StatusDone),
		makeListItem("t3", state.StatusCancelled),
	}

	// Explicitly asking for done and cancelled must return them.
	result := applyListFilters(items, []string{state.StatusDone, state.StatusCancelled}, "", "", "", "", "", false)

	if len(result) != 2 {
		t.Errorf("expected 2 items (done+cancelled explicitly requested), got %d", len(result))
	}
	ids := map[string]bool{}
	for _, item := range result {
		ids[item.ID] = true
	}
	if !ids["t2"] {
		t.Errorf("expected t2 (done) in result")
	}
	if !ids["t3"] {
		t.Errorf("expected t3 (cancelled) in result")
	}
}

// TestList_NoHexInOutputWhenProjectNameConfigured verifies that rd list human-readable
// output does not contain 64-char hex campfire IDs when project_name is configured.
// The campfire hex must never leak into the list table.
func TestList_NoHexInOutputWhenProjectNameConfigured(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	tmpDir, err := os.MkdirTemp("", "test-list-nohex")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	hexID := strings.Repeat("cd", 32) // 64-char hex campfire ID

	// Point CF_HOME at tmpDir.
	origCFHome := os.Getenv("CF_HOME")
	os.Setenv("CF_HOME", tmpDir)
	defer func() {
		if origCFHome != "" {
			os.Setenv("CF_HOME", origCFHome)
		} else {
			os.Unsetenv("CF_HOME")
		}
	}()
	origRDHome := rdHome
	rdHome = ""
	defer func() { rdHome = origRDHome }()

	// Write alias store: "listproject" -> hexID.
	aliasContent, _ := json.Marshal(map[string]string{"listproject": hexID})
	if err := os.WriteFile(filepath.Join(tmpDir, "aliases.json"), aliasContent, 0600); err != nil {
		t.Fatalf("WriteFile aliases.json: %v", err)
	}

	// Write .ready/config.json with project_name.
	readyDir := filepath.Join(tmpDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readyDir, "config.json"), []byte(`{"project_name":"listproject"}`), 0600); err != nil {
		t.Fatalf("WriteFile config.json: %v", err)
	}

	// Write mutations.jsonl with one work item whose campfire_id is the hex.
	createPayload := `{"id":"ready-list1","title":"List Test Item","type":"task","for":"agent@test","priority":"p2"}`
	mutation := `{"msg_id":"test-msg-list1","campfire_id":"` + hexID + `","timestamp":1000000000000000001,"operation":"work:create","payload":` + createPayload + `,"tags":["work:create"],"sender":"testsender"}`
	if err := os.WriteFile(filepath.Join(readyDir, "mutations.jsonl"), []byte(mutation+"\n"), 0600); err != nil {
		t.Fatalf("WriteFile mutations.jsonl: %v", err)
	}

	// Chdir to tmpDir so readyProjectDir() and projectRoot() find our setup.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer os.Chdir(origCwd)

	// Capture os.Stdout.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	runErr := listCmd.RunE(listCmd, []string{})

	w.Close()
	os.Stdout = origStdout

	var buf strings.Builder
	outBuf := make([]byte, 4096)
	for {
		n, readErr := r.Read(outBuf)
		if n > 0 {
			buf.Write(outBuf[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()

	if runErr != nil {
		t.Fatalf("listCmd.RunE error: %v", runErr)
	}

	output := buf.String()

	// The item must appear in the list (piped mode: bare ID on its own line).
	if !strings.Contains(output, "ready-list1") {
		t.Errorf("list output does not contain item ID; output:\n%s", output)
	}

	// No 64-char hex token must appear in any line of the output.
	for _, line := range strings.Split(output, "\n") {
		for _, tok := range strings.Fields(line) {
			if len(tok) == 64 && isHexString(tok) {
				t.Errorf("list output contains raw 64-char hex %q on line: %q", tok, line)
			}
		}
	}
}

// isHexString returns true if s is a 64-char lowercase hexadecimal string.
func isHexString(s string) bool {
	if len(s) != 64 {
		return false
	}
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// setupMutationsDir creates a temp dir with a .ready/mutations.jsonl containing
// the given items and wires CF_HOME / rdHome accordingly.
// Returns tmpDir and a cleanup function.
func setupMutationsDir(t *testing.T, items []struct{ id, title string }) (string, func()) {
	t.Helper()
	tmpDir, err := os.MkdirTemp("", "test-pipe-output")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}

	hexID := strings.Repeat("ee", 32) // 64-char hex campfire ID

	origCFHome := os.Getenv("CF_HOME")
	os.Setenv("CF_HOME", tmpDir)

	origRDHome := rdHome
	rdHome = ""

	// Write alias store.
	aliasContent, _ := json.Marshal(map[string]string{"pipeproject": hexID})
	if err := os.WriteFile(filepath.Join(tmpDir, "aliases.json"), aliasContent, 0600); err != nil {
		t.Fatalf("WriteFile aliases.json: %v", err)
	}

	readyDir := filepath.Join(tmpDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readyDir, "config.json"), []byte(`{"project_name":"pipeproject"}`), 0600); err != nil {
		t.Fatalf("WriteFile config.json: %v", err)
	}

	// Write mutations for each item.
	var mutations string
	for i, item := range items {
		payload := `{"id":"` + item.id + `","title":"` + item.title + `","type":"task","for":"agent@test","priority":"p2"}`
		mutation := `{"msg_id":"test-msg-` + item.id + `","campfire_id":"` + hexID + `","timestamp":` + fmt.Sprintf("%d", 1000000000000000001+int64(i)) + `,"operation":"work:create","payload":` + payload + `,"tags":["work:create"],"sender":"testsender"}`
		mutations += mutation + "\n"
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

// captureStdoutPipe replaces os.Stdout with a pipe, calls fn, then returns the output.
func captureStdoutPipe(t *testing.T, fn func()) string {
	t.Helper()
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	fn()
	w.Close()
	os.Stdout = origStdout

	var buf strings.Builder
	outBuf := make([]byte, 4096)
	for {
		n, readErr := r.Read(outBuf)
		if n > 0 {
			buf.Write(outBuf[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()
	return buf.String()
}

// TestList_PipedOutput_BareIDs verifies that when stdout is not a TTY (piped),
// rd list prints one item ID per line with no table formatting.
func TestList_PipedOutput_BareIDs(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	_, cleanup := setupMutationsDir(t, []struct{ id, title string }{
		{"pipe-list-a1b", "First Item"},
		{"pipe-list-c3d", "Second Item"},
	})
	defer cleanup()

	output := captureStdoutPipe(t, func() {
		if err := listCmd.RunE(listCmd, []string{}); err != nil {
			t.Errorf("listCmd.RunE error: %v", err)
		}
	})

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines in piped output, got %d; output:\n%s", len(lines), output)
	}

	// Each line must be exactly an item ID -- no leading spaces, no extra columns.
	ids := map[string]bool{}
	for _, line := range lines {
		if strings.ContainsAny(line, " \t") {
			t.Errorf("piped output line %q contains whitespace -- expected bare ID only", line)
		}
		ids[line] = true
	}
	if !ids["pipe-list-a1b"] {
		t.Errorf("expected pipe-list-a1b in piped output; output:\n%s", output)
	}
	if !ids["pipe-list-c3d"] {
		t.Errorf("expected pipe-list-c3d in piped output; output:\n%s", output)
	}
}

// TestList_PipedOutput_JSON_Unchanged verifies that --json output is not
// affected by the pipe-friendly mode change.
func TestList_PipedOutput_JSON_Unchanged(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = true // --json mode

	_, cleanup := setupMutationsDir(t, []struct{ id, title string }{
		{"pipe-json-a1b", "JSON Item"},
	})
	defer cleanup()

	output := captureStdoutPipe(t, func() {
		if err := listCmd.RunE(listCmd, []string{}); err != nil {
			t.Errorf("listCmd.RunE error: %v", err)
		}
	})

	// Must be valid JSON array.
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &items); err != nil {
		t.Fatalf("piped --json output is not valid JSON: %v; output:\n%s", err, output)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 JSON item, got %d", len(items))
	}
	if id, _ := items[0]["id"].(string); id != "pipe-json-a1b" {
		t.Errorf("expected id=pipe-json-a1b, got %q", id)
	}
}
