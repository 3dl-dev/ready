package main

import (
	"context"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// staticProvenanceChecker is a simple ProvenanceChecker that returns a fixed level
// for a given key. Used in integration tests to simulate operator provenance levels.
type staticProvenanceChecker struct {
	levels map[string]int
}

func (s *staticProvenanceChecker) Level(key string) int {
	if l, ok := s.levels[key]; ok {
		return l
	}
	return 0
}

// TestMinOperatorLevel_WorkCloseRejectedAtLevel0 verifies that work:close operation
// with min_operator_level=1 is rejected when a level-0 caller attempts execution.
// This is the integration test verifying the real close.json declaration is enforced.
func TestMinOperatorLevel_WorkCloseRejectedAtLevel0(t *testing.T) {
	// Load the actual close.json declaration (min_operator_level=1).
	decl, err := loadDeclaration("close")
	if err != nil {
		t.Fatalf("loadDeclaration(close): %v", err)
	}

	// Verify the declaration has the expected min_operator_level.
	if decl.MinOperatorLevel != 1 {
		t.Fatalf("expected close.json to have min_operator_level=1, got %d", decl.MinOperatorLevel)
	}

	testKey := "test-key-level0"
	backend := &noopBackend{}
	exec := convention.NewExecutorForTest(backend, testKey)

	// Attach a provenance checker that returns level=0 for the test key.
	exec = exec.WithProvenance(&staticProvenanceChecker{
		levels: map[string]int{testKey: 0},
	})

	// Attempt to execute work:close with a level-0 caller.
	argsMap := map[string]any{
		"target":     "msg-target-abc",
		"resolution": "done",
		"reason":     "Test rejection",
	}

	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)

	// Must be rejected with an operator-level error.
	if err == nil {
		t.Fatal("expected rejection for level-0 caller on work:close (min_operator_level=1), got nil error")
	}

	// Error must mention operator provenance level.
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected provenance error, got: %v", err)
	}

	// Error must indicate the required level.
	if !strings.Contains(err.Error(), "requires level 1") {
		t.Errorf("expected 'requires level 1' in error, got: %v", err)
	}
}

// TestMinOperatorLevel_RoleGrantRejectedAtLevel1 verifies that work:role-grant operation
// with min_operator_level=2 is rejected when a level-1 caller attempts execution.
func TestMinOperatorLevel_RoleGrantRejectedAtLevel1(t *testing.T) {
	// Load the actual role-grant.json declaration (min_operator_level=2).
	decl, err := loadDeclaration("role-grant")
	if err != nil {
		t.Fatalf("loadDeclaration(role-grant): %v", err)
	}

	// Verify the declaration has the expected min_operator_level.
	if decl.MinOperatorLevel != 2 {
		t.Fatalf("expected role-grant.json to have min_operator_level=2, got %d", decl.MinOperatorLevel)
	}

	testKey := "test-key-level1"
	backend := &noopBackend{}
	exec := convention.NewExecutorForTest(backend, testKey)

	// Attach a provenance checker that returns level=1 for the test key.
	exec = exec.WithProvenance(&staticProvenanceChecker{
		levels: map[string]int{testKey: 1},
	})

	// Attempt to execute work:role-grant with a level-1 caller.
	argsMap := map[string]any{
		"pubkey":     "target-pubkey-hex",
		"role":       "member",
		"granted_at": "2026-01-01T00:00:00Z",
	}

	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)

	// Must be rejected with an operator-level error.
	if err == nil {
		t.Fatal("expected rejection for level-1 caller on work:role-grant (min_operator_level=2), got nil error")
	}

	// Error must mention operator provenance level.
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected provenance error, got: %v", err)
	}

	// Error must indicate the required level.
	if !strings.Contains(err.Error(), "requires level 2") {
		t.Errorf("expected 'requires level 2' in error, got: %v", err)
	}
}

// TestMinOperatorLevel_RoleGrantAcceptedAtLevel2 verifies that work:role-grant operation
// with min_operator_level=2 is accepted when a level-2 caller attempts execution.
func TestMinOperatorLevel_RoleGrantAcceptedAtLevel2(t *testing.T) {
	// Load the actual role-grant.json declaration (min_operator_level=2).
	decl, err := loadDeclaration("role-grant")
	if err != nil {
		t.Fatalf("loadDeclaration(role-grant): %v", err)
	}

	testKey := "test-key-level2"
	backend := &noopBackend{}
	exec := convention.NewExecutorForTest(backend, testKey)

	// Attach a provenance checker that returns level=2 for the test key.
	exec = exec.WithProvenance(&staticProvenanceChecker{
		levels: map[string]int{testKey: 2},
	})

	// Attempt to execute work:role-grant with a level-2 caller.
	argsMap := map[string]any{
		"pubkey":     "target-pubkey-hex",
		"role":       "member",
		"granted_at": "2026-01-01T00:00:00Z",
	}

	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)

	// Must succeed (no error).
	if err != nil {
		t.Fatalf("expected success for level-2 caller on work:role-grant, got error: %v", err)
	}
}

// TestMinOperatorLevel_WorkCloseAcceptedAtLevel1 verifies that work:close operation
// with min_operator_level=1 is accepted when a level-1 caller attempts execution.
func TestMinOperatorLevel_WorkCloseAcceptedAtLevel1(t *testing.T) {
	// Load the actual close.json declaration (min_operator_level=1).
	decl, err := loadDeclaration("close")
	if err != nil {
		t.Fatalf("loadDeclaration(close): %v", err)
	}

	testKey := "test-key-level1"
	backend := &noopBackend{}
	exec := convention.NewExecutorForTest(backend, testKey)

	// Attach a provenance checker that returns level=1 for the test key.
	exec = exec.WithProvenance(&staticProvenanceChecker{
		levels: map[string]int{testKey: 1},
	})

	// Attempt to execute work:close with a level-1 caller.
	argsMap := map[string]any{
		"target":     "msg-target-abc",
		"resolution": "done",
		"reason":     "Test acceptance",
	}

	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)

	// Must succeed (no error).
	if err != nil {
		t.Fatalf("expected success for level-1 caller on work:close, got error: %v", err)
	}
}
