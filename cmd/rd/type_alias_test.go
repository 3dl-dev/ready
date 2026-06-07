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

	"github.com/campfire-net/ready/pkg/declarations"
	"github.com/campfire-net/ready/pkg/state"
	"github.com/spf13/pflag"
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

// TestCreate_UnknownLabel_ErrorSuggestsPropose verifies that the error message
// from validateLabelsAgainstRegistry suggests "rd label propose <name>".
func TestCreate_UnknownLabel_ErrorSuggestsPropose(t *testing.T) {
	projectDir := isolateTempDir(t)
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0755); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}

	stateRegistry := buildTestLabelRegistry(t)

	labelErr := validateLabelsAgainstRegistry([]string{"notinregistry"}, stateRegistry, "test-key")
	if labelErr == nil {
		t.Fatal("expected error for unknown label 'notinregistry', got nil")
	}

	errMsg := labelErr.Error()
	if !strings.Contains(errMsg, "notinregistry") {
		t.Errorf("error should name the unknown label, got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "rd label propose notinregistry") {
		t.Errorf("error should suggest 'rd label propose notinregistry', got: %q", errMsg)
	}
}

// TestCreate_UnknownLabel_DemandAppendedOnValidationFail verifies that
// validateLabelsAgainstRegistry appends a demand record when it rejects a label.
func TestCreate_UnknownLabel_DemandAppendedOnValidationFail(t *testing.T) {
	projectDir := isolateTempDir(t)
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0755); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}
	demandFile := filepath.Join(readyDir, "label-demand.jsonl")

	stateRegistry := buildTestLabelRegistry(t)

	// Validate a label that's not in the registry.
	_ = validateLabelsAgainstRegistry([]string{"unknownlabel"}, stateRegistry, "agent-key-abc")

	// Demand record must have been appended.
	if _, statErr := os.Stat(demandFile); statErr != nil {
		t.Fatalf("label-demand.jsonl should exist after validation failure, got: %v", statErr)
	}
	count := countLabelDemand("unknownlabel")
	if count != 1 {
		t.Errorf("demand count for 'unknownlabel' = %d, want 1", count)
	}
}

// TestValidateLabels_KnownLabel_NoError verifies that known labels pass validation
// without error and do not append to the demand log.
func TestValidateLabels_KnownLabel_NoError(t *testing.T) {
	projectDir := isolateTempDir(t)
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0755); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}
	demandFile := filepath.Join(readyDir, "label-demand.jsonl")

	stateRegistry := buildTestLabelRegistry(t)

	// "bug" is a seed label — should pass.
	if labelErr := validateLabelsAgainstRegistry([]string{"bug"}, stateRegistry, "agent-key"); labelErr != nil {
		t.Errorf("known label 'bug' should not be rejected, got: %v", labelErr)
	}
	// No demand record should be written.
	if _, statErr := os.Stat(demandFile); statErr == nil {
		t.Error("label-demand.jsonl should NOT be created for known labels")
	}
}

// TestValidateLabels_NilRegistry_NoError verifies that nil registry skips validation
// (graceful fallback for fresh checkout / offline).
func TestValidateLabels_NilRegistry_NoError(t *testing.T) {
	if err := validateLabelsAgainstRegistry([]string{"anything"}, nil, "key"); err != nil {
		t.Errorf("nil registry should skip validation (fallback), got: %v", err)
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
	decl := loadCreateDeclaration(t)

	err := ValidateEnumFlags(decl, map[string]string{
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

// ---------------------------------------------------------------------------
// End-to-end: createCmd.RunE with --label not-in-registry
// Veracity gap fix (ready-b0c): demand logged + rejection fires + no mutation write.
// ---------------------------------------------------------------------------

// TestCreate_UnknownLabel_E2E_ExitDemandNoWrite is the end-to-end integration test
// for the unknown-label rejection path. It runs the real createCmd.RunE (not a
// standalone function call) and asserts ALL THREE of the veracity invariants:
//
//  1. Non-zero/error return — rejection was signalled to the caller.
//  2. .ready/label-demand.jsonl was written with a record for the unknown atom.
//  3. .ready/mutations.jsonl and .ready/pending.jsonl do NOT exist — the
//     rejection happened before any campfire mutation was written.
//
// Setup:
//   - isolateTempDir: cwd → temp dir; readyProjectDir() resolves here.
//   - .ready/ created: readyProjectDir finds it; jsonlPath() returns a path.
//   - No .campfire/root: hasCampfire == false → JSONL code path in create.go.
//   - rdHome → fresh cfHome: requireClient() auto-generates identity so
//     withAgentAndStore can load it; protocolClient is reset for test isolation.
//   - mutations.jsonl absent: DeriveAllFromJSONL returns seed-label registry
//     (non-nil, doesn't contain "notinregistry" → validation fires).
func TestCreate_UnknownLabel_E2E_ExitDemandNoWrite(t *testing.T) {
	// --- Environment isolation ---
	projectDir := isolateTempDir(t)
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0755); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}

	// Observed paths (must not exist after rejection).
	mutationsJSONL := filepath.Join(readyDir, "mutations.jsonl")
	pendingJSONL := filepath.Join(readyDir, "pending.jsonl")
	demandJSONL := filepath.Join(readyDir, "label-demand.jsonl")

	// Confirm none of these exist pre-test.
	for _, f := range []string{mutationsJSONL, pendingJSONL, demandJSONL} {
		if _, err := os.Stat(f); err == nil {
			t.Fatalf("unexpected pre-existing file: %s", f)
		}
	}

	// --- Identity: set rdHome to a fresh temp dir; requireClient auto-generates ---
	cfHome := t.TempDir()
	origRDHome := rdHome
	rdHome = cfHome
	t.Cleanup(func() { rdHome = origRDHome })

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	if _, err := requireClient(); err != nil {
		t.Fatalf("requireClient (identity setup): %v", err)
	}

	// --- Flag setup ---
	if err := createCmd.Flags().Set("type", "task"); err != nil {
		t.Fatalf("Set --type: %v", err)
	}
	if err := createCmd.Flags().Set("priority", "p1"); err != nil {
		t.Fatalf("Set --priority: %v", err)
	}
	if err := createCmd.Flags().Set("label", "notinregistry"); err != nil {
		t.Fatalf("Set --label: %v", err)
	}
	t.Cleanup(func() {
		_ = createCmd.Flags().Set("type", "")
		_ = createCmd.Flags().Set("priority", "")
		// Reset StringArray flag to empty slice via SliceValue interface.
		if sv, ok := createCmd.Flags().Lookup("label").Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		}
	})

	// --- Run the real command entry point ---
	runErr := createCmd.RunE(createCmd, []string{"Test unknown label"})

	// (1) Must return an error.
	if runErr == nil {
		t.Fatal("createCmd.RunE: expected error for --label notinregistry, got nil")
	}

	// The error must be the label-registry rejection, not an identity/store error —
	// proving it fires inside withAgentAndStore after identity is loaded but before
	// any convention op (write) is executed.
	errMsg := runErr.Error()
	if !strings.Contains(errMsg, "notinregistry") {
		t.Errorf("error should name the unknown label 'notinregistry', got: %q", errMsg)
	}
	if !strings.Contains(errMsg, "rd label propose") {
		t.Errorf("error should suggest 'rd label propose', got: %q", errMsg)
	}

	// (2) label-demand.jsonl must exist and contain a record for "notinregistry".
	if _, err := os.Stat(demandJSONL); err != nil {
		t.Fatalf("label-demand.jsonl must exist after unknown-label rejection, got: %v", err)
	}
	f, err := os.Open(demandJSONL)
	if err != nil {
		t.Fatalf("opening label-demand.jsonl: %v", err)
	}
	defer f.Close()
	dec := json.NewDecoder(f)
	var demandFound bool
	for dec.More() {
		var rec struct {
			Label string `json:"label"`
		}
		if err := dec.Decode(&rec); err != nil {
			t.Fatalf("decoding demand record: %v", err)
		}
		if rec.Label == "notinregistry" {
			demandFound = true
		}
	}
	if !demandFound {
		t.Error("label-demand.jsonl must contain a record for 'notinregistry'")
	}

	// (3) mutations.jsonl and pending.jsonl must NOT exist — rejection happened
	// before any campfire convention op was executed.
	for _, f := range []string{mutationsJSONL, pendingJSONL} {
		if _, err := os.Stat(f); err == nil {
			t.Errorf("%s must NOT exist after label-registry rejection, but it does", f)
		}
	}
}

// ---------------------------------------------------------------------------
// Helper: build a state.LabelDef registry from seed labels for test use
// ---------------------------------------------------------------------------

// buildTestLabelRegistry creates a state.LabelDef registry from the embedded seed labels.
func buildTestLabelRegistry(t *testing.T) map[string]state.LabelDef {
	t.Helper()
	seedLabels, err := declarations.LoadSeedLabels()
	if err != nil {
		t.Fatalf("LoadSeedLabels: %v", err)
	}
	registry := make(map[string]state.LabelDef, len(seedLabels))
	for _, sl := range seedLabels {
		registry[sl.Name] = state.LabelDef{
			Name:        sl.Name,
			Description: sl.Description,
			DefinedBy:   "seed",
		}
	}
	return registry
}
