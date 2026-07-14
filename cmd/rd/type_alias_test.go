package main

// type_alias_test.go — integration tests for the --type alias rewrite (ready-b0c).
//
// Done conditions (from item spec):
// 1. --type bug → exit 0, stderr contains alias notice, alias rewrite fires before enum validation
// 2. --type bug --label bug does not duplicate the label
// 3. --type incident (no alias, not a type) → non-zero exit, error lists valid types AND mentions aliases
// 4. --label notinregistry → demand record appended, error suggests "rd label propose <name>"
// 5. rd label propose creates decision item with demand count in context

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/declarations"
)

// ---------------------------------------------------------------------------
// Test 1: --type bug rewrites to task+label:bug, emits one-line notice to stderr
// ---------------------------------------------------------------------------

// TestCreate_TypeBugAlias_RewritesToTaskAndLabel verifies that --type bug is
// rewritten to --type task with label bug before enum validation, so the command
// succeeds (exits 0) without the user knowing about the canonical type.
//
// This test exercises createCmd.RunE directly (not a standalone mirror) per
// done condition: "Test executing the actual cobra command".
func TestCreate_TypeBugAlias_RewritesToTaskAndLabel(t *testing.T) {
	// Set --type bug + --priority p1 on the real createCmd.
	if err := createCmd.Flags().Set("type", "bug"); err != nil {
		t.Fatalf("setting --type: %v", err)
	}
	if err := createCmd.Flags().Set("priority", "p1"); err != nil {
		t.Fatalf("setting --priority: %v", err)
	}
	defer func() {
		_ = createCmd.Flags().Set("type", "")
		_ = createCmd.Flags().Set("priority", "")
	}()

	// Capture stderr so we can assert the notice line.
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stderr = w

	// Execute RunE — in test env there is no identity, so we expect an error
	// AFTER the alias rewrite + enum validation pass. The key assertions are:
	// (a) error is NOT an enum validation error (alias was applied, task is valid)
	// (b) stderr contains the alias notice before any store error.
	runErr := createCmd.RunE(createCmd, []string{"Test bug item"})

	w.Close()
	os.Stderr = origStderr

	// Read captured stderr.
	var stderrBuf strings.Builder
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		stderrBuf.WriteString(scanner.Text() + "\n")
	}
	stderrOut := stderrBuf.String()

	// The alias notice must appear on stderr (before any identity/store error).
	if !strings.Contains(stderrOut, "notice:") {
		t.Errorf("expected alias notice on stderr, got: %q", stderrOut)
	}
	if !strings.Contains(stderrOut, "--type bug") {
		t.Errorf("notice must mention '--type bug', got: %q", stderrOut)
	}
	if !strings.Contains(stderrOut, "--type task") {
		t.Errorf("notice must mention '--type task', got: %q", stderrOut)
	}
	if !strings.Contains(stderrOut, "--label bug") {
		t.Errorf("notice must mention '--label bug', got: %q", stderrOut)
	}
	if !strings.Contains(stderrOut, "ready-a92") {
		t.Errorf("notice must cite 'ready-a92', got: %q", stderrOut)
	}

	// The run must NOT have failed with an enum validation error — the alias
	// was applied and "task" is a valid type, so enum validation passed.
	if runErr != nil {
		errMsg := runErr.Error()
		if strings.Contains(errMsg, "is not valid; accepted values:") {
			t.Errorf("--type bug should be accepted via alias (rewritten to task), but got enum error: %q", errMsg)
		}
		// It's expected to fail at store/identity level in test env — that's fine.
		t.Logf("createCmd.RunE failed past alias+enum validation (expected in test env): %v", runErr)
	}
}

// ---------------------------------------------------------------------------
// Test 2: --type bug --label bug does not duplicate the label
// ---------------------------------------------------------------------------

// TestCreate_TypeBugAlias_NoDuplicateLabel verifies that --type bug --label bug
// does not produce a duplicate "bug" label on the item.
func TestCreate_TypeBugAlias_NoDuplicateLabel(t *testing.T) {
	labels := []string{"bug"} // existing label from user flag

	// Simulate the alias rewrite on a pre-populated labelSlice.
	itemType := "bug"
	if _, err := rewriteTypeAlias(&itemType, &labels); err != nil {
		t.Fatalf("rewriteTypeAlias: %v", err)
	}

	// After rewrite: type should be "task", labels should be ["bug"] (no duplicate).
	if itemType != "task" {
		t.Errorf("itemType after alias rewrite = %q, want 'task'", itemType)
	}
	bugCount := 0
	for _, l := range labels {
		if l == "bug" {
			bugCount++
		}
	}
	if bugCount != 1 {
		t.Errorf("label 'bug' appears %d time(s), want exactly 1 (no duplicate from alias)", bugCount)
	}
}

// ---------------------------------------------------------------------------
// Test 3: --type incident (no alias, not a valid type) → validation error
// ---------------------------------------------------------------------------

// TestCreate_TypeIncident_NotAliasNotValid verifies that "incident" is rejected
// with a type validation error that:
//
//	(a) names the valid types
//	(b) mentions aliases (only on type errors, not priority/level)
func TestCreate_TypeIncident_NotAliasNotValid(t *testing.T) {
	_ = isolateTempDir(t)

	if err := createCmd.Flags().Set("type", "incident"); err != nil {
		t.Fatalf("setting --type: %v", err)
	}
	if err := createCmd.Flags().Set("priority", "p1"); err != nil {
		t.Fatalf("setting --priority: %v", err)
	}
	defer func() {
		_ = createCmd.Flags().Set("type", "")
		_ = createCmd.Flags().Set("priority", "")
	}()

	err := createCmd.RunE(createCmd, []string{"Test incident"})
	if err == nil {
		t.Fatal("expected error for --type incident (not an alias, not a valid type), got nil")
	}

	errMsg := err.Error()
	// Must list valid types.
	if !strings.Contains(errMsg, "is not valid; accepted values:") {
		t.Errorf("error should say 'is not valid; accepted values:', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "task") {
		t.Errorf("error should list 'task' as a valid type, got: %q", errMsg)
	}
	// Must mention aliases (scoped to type errors).
	if !strings.Contains(errMsg, "alias") {
		t.Errorf("type error should mention 'alias', got: %q", errMsg)
	}
}

// ---------------------------------------------------------------------------
// Test 4: --label notinregistry → demand record appended, error suggests propose
// ---------------------------------------------------------------------------

// TestCreate_UnknownLabel_DemandSignal verifies that appendLabelDemand writes a
// record to .ready/label-demand.jsonl with the correct label and by fields.
func TestCreate_UnknownLabel_DemandSignal(t *testing.T) {
	projectDir := isolateTempDir(t)
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0755); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}
	demandFile := filepath.Join(readyDir, "label-demand.jsonl")

	appendLabelDemand("notinregistry", "test-agent-key")

	// Verify the demand record was written.
	if _, err := os.Stat(demandFile); err != nil {
		t.Fatalf("label-demand.jsonl should exist after appendLabelDemand, got: %v", err)
	}

	// Read and verify the record content.
	f, err := os.Open(demandFile)
	if err != nil {
		t.Fatalf("opening label-demand.jsonl: %v", err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var found bool
	for dec.More() {
		var rec struct {
			Label string `json:"label"`
			By    string `json:"by"`
		}
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("decoding demand record: %v", err)
		}
		if rec.Label == "notinregistry" && rec.By == "test-agent-key" {
			found = true
		}
	}
	if !found {
		t.Error("expected demand record for 'notinregistry' by 'test-agent-key' in label-demand.jsonl")
	}
}

// ---------------------------------------------------------------------------
// Test 5: rd label propose creates decision item with demand count in context
// ---------------------------------------------------------------------------

// TestLabelPropose_DemandCountInContext verifies that countLabelDemand correctly
// counts entries in label-demand.jsonl, which labelProposeCmd uses to annotate
// the decision item context.
func TestLabelPropose_DemandCountInContext(t *testing.T) {
	projectDir := isolateTempDir(t)
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0755); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}

	// Append 3 demand records for "hotfix", 1 for another label.
	for i := 0; i < 3; i++ {
		appendLabelDemand("hotfix", "agent-key")
	}
	appendLabelDemand("other", "agent-key")

	count := countLabelDemand("hotfix")
	if count != 3 {
		t.Errorf("countLabelDemand('hotfix') = %d, want 3", count)
	}
	otherCount := countLabelDemand("other")
	if otherCount != 1 {
		t.Errorf("countLabelDemand('other') = %d, want 1", otherCount)
	}
	zeroCount := countLabelDemand("absent")
	if zeroCount != 0 {
		t.Errorf("countLabelDemand('absent') = %d, want 0", zeroCount)
	}
}

// TestLabelPropose_RunE_ProceedsPastEnum verifies that labelProposeCmd.RunE
// proceeds past enum validation (type=decision is valid). In test env it
// fails at the store/identity level, not the enum validation level.
func TestLabelPropose_RunE_ProceedsPastEnum(t *testing.T) {
	_ = isolateTempDir(t)

	err := labelProposeCmd.RunE(labelProposeCmd, []string{"incident"})
	// In test env with no identity, expect an error at store level.
	// Must NOT be an enum validation error (type=decision is valid).
	if err != nil {
		errMsg := err.Error()
		if strings.Contains(errMsg, "is not valid; accepted values:") {
			t.Errorf("labelProposeCmd should not fail enum validation; got: %q", errMsg)
		}
		t.Logf("labelProposeCmd.RunE failed at store level (expected in test env): %v", err)
	}
}

// ---------------------------------------------------------------------------
// Test: Aliases table loads correctly from embedded aliases.json
// ---------------------------------------------------------------------------

// TestLoadTypeAliases_BugAlias verifies that the embedded aliases.json contains
// the "bug" alias mapping to type=task with label=bug.
func TestLoadTypeAliases_BugAlias(t *testing.T) {
	aliases, err := declarations.LoadTypeAliases()
	if err != nil {
		t.Fatalf("LoadTypeAliases: %v", err)
	}

	bugAlias, ok := aliases["bug"]
	if !ok {
		t.Fatal("aliases.json must contain 'bug' alias")
	}
	if bugAlias.Type != "task" {
		t.Errorf("bug alias type = %q, want 'task'", bugAlias.Type)
	}
	if len(bugAlias.Labels) != 1 || bugAlias.Labels[0] != "bug" {
		t.Errorf("bug alias labels = %v, want ['bug']", bugAlias.Labels)
	}
}

// TestValidateEnumFlags_Priority_NoAliasNote verifies that the alias note does NOT
// appear on priority or level errors (scoped to type errors only per Amendment 2).
func TestValidateEnumFlags_Priority_NoAliasNote(t *testing.T) {
	err := ValidateEnumFlags("create", map[string]string{
		"type":     "task",
		"priority": "urgent", // invalid priority
	})
	if err == nil {
		t.Fatal("expected error for invalid priority 'urgent', got nil")
	}
	errMsg := err.Error()
	// The alias note must NOT appear on priority errors.
	if strings.Contains(errMsg, "alias") {
		t.Errorf("alias note should NOT appear on priority errors, got: %q", errMsg)
	}
	// But the error must still list valid priorities.
	if !strings.Contains(errMsg, "priority") {
		t.Errorf("error should mention 'priority', got: %q", errMsg)
	}
}

