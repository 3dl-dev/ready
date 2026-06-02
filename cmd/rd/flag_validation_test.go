package main

// flag_validation_test.go — tests for rd command flag validation.
//
// Covers: required flag enforcement, invalid enum values, mutually exclusive
// flag combinations, and the executor's schema-level validation for convention
// operations (gate, close, status).

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/spf13/cobra"
)

// TestExecutorValidates_Gate_BadGateType verifies that the executor rejects an
// invalid gate_type value ("fire-drill") before sending any network request.
func TestExecutorValidates_Gate_BadGateType(t *testing.T) {
	decl, err := loadDeclaration("gate")
	if err != nil {
		t.Fatalf("loadDeclaration(gate): %v", err)
	}
	exec := newNoopExecutor()
	argsMap := map[string]any{
		"target":    "msg-cafebabe-0000-0000-0000-000000000001",
		"gate_type": "fire-drill",
	}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for invalid gate_type 'fire-drill', got nil")
	}
	if !strings.Contains(err.Error(), "fire-drill") {
		t.Errorf("expected error to mention 'fire-drill', got: %q", err.Error())
	}
}

// TestExecutorValidates_Gate_ValidTypes verifies that all declared gate_type
// enum values are accepted by the executor.
func TestExecutorValidates_Gate_ValidTypes(t *testing.T) {
	decl, err := loadDeclaration("gate")
	if err != nil {
		t.Fatalf("loadDeclaration(gate): %v", err)
	}
	exec := newNoopExecutor()
	for _, gt := range []string{"budget", "design", "scope", "review", "human", "stall", "periodic"} {
		t.Run(gt, func(t *testing.T) {
			argsMap := map[string]any{
				"target":    "msg-cafebabe-0000-0000-0000-000000000001",
				"gate_type": gt,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err != nil {
				t.Errorf("gate_type=%q: expected no error, got: %v", gt, err)
			}
		})
	}
}

// TestExecutorValidates_Gate_MissingTarget verifies that the executor rejects
// a gate operation with no target (required field missing).
func TestExecutorValidates_Gate_MissingTarget(t *testing.T) {
	decl, err := loadDeclaration("gate")
	if err != nil {
		t.Fatalf("loadDeclaration(gate): %v", err)
	}
	exec := newNoopExecutor()
	argsMap := map[string]any{"gate_type": "design"}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for missing required 'target', got nil")
	}
}

// TestExecutorValidates_Close_BadResolution verifies that the executor rejects
// an invalid resolution value ("withdrawn") before sending.
func TestExecutorValidates_Close_BadResolution(t *testing.T) {
	decl, err := loadDeclaration("close")
	if err != nil {
		t.Fatalf("loadDeclaration(close): %v", err)
	}
	exec := newNoopExecutor()
	argsMap := map[string]any{
		"target":     "msg-cafebabe-0000-0000-0000-000000000001",
		"resolution": "withdrawn",
	}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for invalid resolution 'withdrawn', got nil")
	}
	if !strings.Contains(err.Error(), "withdrawn") {
		t.Errorf("expected error to mention 'withdrawn', got: %q", err.Error())
	}
}

// TestExecutorValidates_Close_ValidResolutions verifies that all declared
// resolution enum values (done, cancelled, failed) are accepted.
func TestExecutorValidates_Close_ValidResolutions(t *testing.T) {
	decl, err := loadDeclaration("close")
	if err != nil {
		t.Fatalf("loadDeclaration(close): %v", err)
	}
	exec := newNoopExecutor()
	for _, res := range []string{"done", "cancelled", "failed"} {
		t.Run(res, func(t *testing.T) {
			argsMap := map[string]any{
				"target":     "msg-cafebabe-0000-0000-0000-000000000001",
				"resolution": res,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err != nil {
				t.Errorf("resolution=%q: expected no error, got: %v", res, err)
			}
		})
	}
}

// TestExecutorValidates_Close_MissingResolution verifies that the executor
// rejects a close operation with no resolution (required field missing).
func TestExecutorValidates_Close_MissingResolution(t *testing.T) {
	decl, err := loadDeclaration("close")
	if err != nil {
		t.Fatalf("loadDeclaration(close): %v", err)
	}
	exec := newNoopExecutor()
	argsMap := map[string]any{"target": "msg-cafebabe-0000-0000-0000-000000000001"}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for missing required 'resolution', got nil")
	}
}

// TestExecutorValidates_Status_BadToValue verifies that the executor rejects
// an invalid 'to' status value ("paused") before sending.
func TestExecutorValidates_Status_BadToValue(t *testing.T) {
	decl, err := loadDeclaration("status")
	if err != nil {
		t.Fatalf("loadDeclaration(status): %v", err)
	}
	exec := newNoopExecutor()
	argsMap := map[string]any{
		"target": "msg-cafebabe-0000-0000-0000-000000000001",
		"to":     "paused",
	}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for invalid status 'paused', got nil")
	}
	if !strings.Contains(err.Error(), "paused") {
		t.Errorf("expected error to mention 'paused', got: %q", err.Error())
	}
}

// TestExecutorValidates_Status_ValidToValues verifies that all declared 'to'
// enum values are accepted.
func TestExecutorValidates_Status_ValidToValues(t *testing.T) {
	decl, err := loadDeclaration("status")
	if err != nil {
		t.Fatalf("loadDeclaration(status): %v", err)
	}
	exec := newNoopExecutor()
	for _, s := range []string{"inbox", "active", "scheduled", "waiting", "done", "cancelled", "failed"} {
		t.Run(s, func(t *testing.T) {
			argsMap := map[string]any{
				"target": "msg-cafebabe-0000-0000-0000-000000000001",
				"to":     s,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err != nil {
				t.Errorf("status to=%q: expected no error, got: %v", s, err)
			}
		})
	}
}

// TestExecutorValidates_Status_BadWaitingType verifies that the executor rejects
// an invalid waiting_type value ("limbo") before sending.
func TestExecutorValidates_Status_BadWaitingType(t *testing.T) {
	decl, err := loadDeclaration("status")
	if err != nil {
		t.Fatalf("loadDeclaration(status): %v", err)
	}
	exec := newNoopExecutor()
	argsMap := map[string]any{
		"target":       "msg-cafebabe-0000-0000-0000-000000000001",
		"to":           "waiting",
		"waiting_type": "limbo",
	}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for invalid waiting_type 'limbo', got nil")
	}
	if !strings.Contains(err.Error(), "limbo") {
		t.Errorf("expected error to mention 'limbo', got: %q", err.Error())
	}
}

// TestExecutorValidates_Update_BadPriority verifies that the executor rejects
// an invalid priority value ("urgent") on a work:update operation.
func TestExecutorValidates_Update_BadPriority(t *testing.T) {
	decl, err := loadDeclaration("update")
	if err != nil {
		t.Fatalf("loadDeclaration(update): %v", err)
	}
	exec := newNoopExecutor()
	argsMap := map[string]any{
		"target":   "msg-cafebabe-0000-0000-0000-000000000001",
		"priority": "urgent",
	}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for invalid update priority 'urgent', got nil")
	}
	if !strings.Contains(err.Error(), "urgent") {
		t.Errorf("expected error to mention 'urgent', got: %q", err.Error())
	}
}

// TestExecutorValidates_Update_BadLevel verifies that the executor rejects an
// invalid level value ("section") on a work:update operation.
func TestExecutorValidates_Update_BadLevel(t *testing.T) {
	decl, err := loadDeclaration("update")
	if err != nil {
		t.Fatalf("loadDeclaration(update): %v", err)
	}
	exec := newNoopExecutor()
	argsMap := map[string]any{
		"target": "msg-cafebabe-0000-0000-0000-000000000001",
		"level":  "section",
	}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for invalid update level 'section', got nil")
	}
	if !strings.Contains(err.Error(), "section") {
		t.Errorf("expected error to mention 'section', got: %q", err.Error())
	}
}

// --- cobra command flag validation (standalone, no live store) ---

// TestUpdate_NoFlags_RequiresAtLeastOneFlag verifies that the update command
// returns a clear error when no field flags are provided.
func TestUpdate_NoFlags_RequiresAtLeastOneFlag(t *testing.T) {
	cmd := buildUpdateValidationCmd()
	cmd.SetArgs([]string{"ready-abc"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when no update flags provided, got nil")
	}
	if !strings.Contains(err.Error(), "no fields to update") {
		t.Errorf("expected 'no fields to update' error, got: %q", err.Error())
	}
}

// TestUpdate_WithTitle_Accepted verifies that --title alone passes the no-fields
// validation gate.
func TestUpdate_WithTitle_Accepted(t *testing.T) {
	cmd := buildUpdateValidationCmd()
	cmd.SetArgs([]string{"ready-abc", "--title", "New title"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("expected no validation error with --title, got: %v", err)
	}
}

// TestUpdate_WithStatus_Accepted verifies that --status alone passes validation.
func TestUpdate_WithStatus_Accepted(t *testing.T) {
	cmd := buildUpdateValidationCmd()
	cmd.SetArgs([]string{"ready-abc", "--status", "active"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("expected no validation error with --status, got: %v", err)
	}
}

// TestUpdate_WithClaim_Accepted verifies that --claim alone passes validation.
func TestUpdate_WithClaim_Accepted(t *testing.T) {
	cmd := buildUpdateValidationCmd()
	cmd.SetArgs([]string{"ready-abc", "--claim"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("expected no validation error with --claim, got: %v", err)
	}
}

// TestClose_MissingReason_ReturnsError verifies that rd close requires --reason.
func TestClose_MissingReason_ReturnsError(t *testing.T) {
	cmd := buildCloseValidationCmd()
	cmd.SetArgs([]string{"ready-abc"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --reason is missing, got nil")
	}
	if !strings.Contains(err.Error(), "--reason is required") {
		t.Errorf("expected '--reason is required' error, got: %q", err.Error())
	}
}

// TestClose_WithReason_Accepted verifies that --reason alone passes validation.
func TestClose_WithReason_Accepted(t *testing.T) {
	cmd := buildCloseValidationCmd()
	cmd.SetArgs([]string{"ready-abc", "--reason", "Completed"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("expected no validation error with --reason, got: %v", err)
	}
}

// TestGate_MissingGateType_ReturnsError verifies that rd gate requires --gate-type.
func TestGate_MissingGateType_ReturnsError(t *testing.T) {
	cmd := buildGateValidationCmd()
	cmd.SetArgs([]string{"ready-abc"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --gate-type is missing, got nil")
	}
	if !strings.Contains(err.Error(), "--gate-type is required") {
		t.Errorf("expected '--gate-type is required' error, got: %q", err.Error())
	}
}

// TestGate_WithGateType_Accepted verifies that --gate-type alone passes validation.
func TestGate_WithGateType_Accepted(t *testing.T) {
	cmd := buildGateValidationCmd()
	cmd.SetArgs([]string{"ready-abc", "--gate-type", "design"})
	if err := cmd.Execute(); err != nil {
		t.Errorf("expected no validation error with --gate-type, got: %v", err)
	}
}

// --- helpers ---

// newNoopExecutor returns a convention.Executor backed by noopBackend (defined in
// executor_validation_test.go). It uses a level-2 provenance checker so that
// min_operator_level checks do not interfere with argument validation tests.
func newNoopExecutor() *convention.Executor {
	return convention.NewExecutorForTest(&noopBackend{}, "test-key-hex").
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{"test-key-hex": 2}})
}

// buildUpdateValidationCmd constructs a minimal cobra command that mirrors the
// update flag validation logic in updateCmd.RunE.
func buildUpdateValidationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "update <item-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			blocks, _ := cmd.Flags().GetString("blocks")
			if blocks != "" {
				return fmt.Errorf("--blocks is not a flag on rd update. Use: rd dep add <this-item> %s", blocks)
			}
			title, _ := cmd.Flags().GetString("title")
			ctx, _ := cmd.Flags().GetString("context")
			priority, _ := cmd.Flags().GetString("priority")
			eta, _ := cmd.Flags().GetString("eta")
			due, _ := cmd.Flags().GetString("due")
			level, _ := cmd.Flags().GetString("level")
			statusTo, _ := cmd.Flags().GetString("status")
			waitingOn, _ := cmd.Flags().GetString("waiting-on")
			claim, _ := cmd.Flags().GetBool("claim")
			hasFieldUpdate := title != "" || ctx != "" || priority != "" || eta != "" || due != "" || level != ""
			hasStatusUpdate := statusTo != "" || waitingOn != ""
			if !hasFieldUpdate && !hasStatusUpdate && !claim {
				return fmt.Errorf("no fields to update: specify at least one of --title, --context, --priority, --eta, --due, --level, --status, --waiting-on, --claim")
			}
			return nil
		},
	}
	cmd.Flags().String("title", "", "")
	cmd.Flags().String("context", "", "")
	cmd.Flags().String("priority", "", "")
	cmd.Flags().String("eta", "", "")
	cmd.Flags().String("due", "", "")
	cmd.Flags().String("level", "", "")
	cmd.Flags().String("status", "", "")
	cmd.Flags().String("waiting-on", "", "")
	cmd.Flags().String("blocks", "", "")
	cmd.Flags().Bool("claim", false, "")
	return cmd
}

// buildCloseValidationCmd constructs a minimal cobra command that mirrors the
// close flag validation logic in closeCmd.RunE.
func buildCloseValidationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "close <item-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			reason, _ := cmd.Flags().GetString("reason")
			if reason == "" {
				return fmt.Errorf("--reason is required (why is this item being closed?)")
			}
			return nil
		},
	}
	cmd.Flags().String("reason", "", "")
	cmd.Flags().String("resolution", "done", "")
	return cmd
}

// buildGateValidationCmd constructs a minimal cobra command that mirrors the
// gate flag validation logic in gateCmd.RunE.
func buildGateValidationCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:  "gate <item-id>",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			gateType, _ := cmd.Flags().GetString("gate-type")
			if gateType == "" {
				return fmt.Errorf("--gate-type is required: choose from budget, design, scope, review, human, stall, periodic")
			}
			return nil
		},
	}
	cmd.Flags().String("gate-type", "", "")
	cmd.Flags().String("description", "", "")
	return cmd
}
