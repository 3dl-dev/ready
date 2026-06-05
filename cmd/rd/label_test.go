package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	cfauthprov "github.com/campfire-net/campfire/cf-conventions/cf-authority/provenance"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	cfprov "github.com/campfire-net/campfire/pkg/provenance"
	"github.com/campfire-net/campfire/cf-protocol/store"

	"github.com/campfire-net/ready/pkg/declarations"
	"github.com/campfire-net/ready/pkg/provenance"
	"github.com/campfire-net/ready/pkg/storetest"
)

// newRealExecutor constructs a convention.Executor wired exactly as requireExecutor
// does in root.go, but using a caller-supplied store and campfire/creator details
// instead of the global project root. This allows integration tests to exercise
// the real ProvenanceV2 path with real role-grant messages in the store.
//
// The executor uses a noopBackend so no actual campfire network calls are made —
// we only care about the provenance gate (Accept vs Reject), not the send.
func newRealExecutor(t *testing.T, s store.Store, campfireID, creatorKey, callerKey string) *convention.Executor {
	t.Helper()

	// Build the same provenance chain as requireExecutor: NewStoreChecker → rdLevelSource → cfauthprov.NewChecker.
	checker, err := provenance.NewStoreChecker(s, campfireID, creatorKey)
	if err != nil {
		t.Fatalf("NewStoreChecker: %v", err)
	}

	exec := convention.NewExecutorForTest(&noopBackend{}, callerKey)
	exec = exec.WithProvenanceV2(cfauthprov.NewChecker(testLevelSource{checker}))
	return exec
}

// testLevelSource adapts *provenance.StoreChecker to the cfprov.LevelSource interface.
// Mirrors rdLevelSource in root.go.
type testLevelSource struct {
	inner *provenance.StoreChecker
}

func (s testLevelSource) Level(keyHex string) cfprov.Level {
	return cfprov.Level(s.inner.Level(keyHex))
}

// ---------------------------------------------------------------------------
// TDD: Failing tests that encode the done condition.
// These tests must fail before implementation and pass after.
// ---------------------------------------------------------------------------

// TestLabelDefine_Level2SenderAccepted verifies that a grant-holder (operator level 2)
// can successfully execute a label-define operation through the real executor.
// Level 2 is granted via a real work:role-grant message in the store — not a stub.
func TestLabelDefine_Level2SenderAccepted(t *testing.T) {
	h := storetest.New(t)

	// The test sender is the caller.
	const callerKey = "aaaa0000000000000000000000000000000000000000000000000000000000000001"
	// Creator is a different key (not the caller) so the caller's level comes
	// from an explicit role-grant message.
	const creatorKey = "bbbb0000000000000000000000000000000000000000000000000000000000000002"

	// Add a work:role-grant message granting callerKey maintainer (level 2).
	h.WithSender(creatorKey).RoleGrant(callerKey, "maintainer")

	// Load the real label-define declaration.
	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define): %v", err)
	}
	if decl.MinOperatorLevel != 2 {
		t.Fatalf("expected label-define.json to have min_operator_level=2, got %d", decl.MinOperatorLevel)
	}

	// Wire the real executor with provenance driven by the role-grant message.
	exec := newRealExecutor(t, h.Store, h.CampfireID, creatorKey, callerKey)

	argsMap := map[string]any{
		"label":       "custom-label",
		"description": "A custom label for testing",
	}

	_, err = exec.Execute(context.Background(), decl, h.CampfireID, argsMap)
	if err != nil {
		t.Fatalf("level-2 sender should be accepted, but got error: %v", err)
	}
}

// TestLabelDefine_Level1SenderRejected verifies that a contributor (operator level 1,
// the default) cannot execute label-define — the executor must reject with a
// min_operator_level error. Level is determined by real role-grant messages.
func TestLabelDefine_Level1SenderRejected(t *testing.T) {
	h := storetest.New(t)

	// callerKey has no role-grant → defaults to level 1 (contributor).
	const callerKey = "cccc0000000000000000000000000000000000000000000000000000000000000003"
	const creatorKey = "dddd0000000000000000000000000000000000000000000000000000000000000004"
	// No role-grant message — caller is default contributor (level 1).

	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define): %v", err)
	}

	exec := newRealExecutor(t, h.Store, h.CampfireID, creatorKey, callerKey)

	argsMap := map[string]any{
		"label": "should-be-rejected",
	}

	_, err = exec.Execute(context.Background(), decl, h.CampfireID, argsMap)
	if err == nil {
		t.Fatal("level-1 sender should be rejected for label-define (min_operator_level=2), got nil error")
	}
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected provenance error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "requires level 2") {
		t.Errorf("expected 'requires level 2' in error, got: %v", err)
	}
}

// TestLabelDefine_Level0SenderRejected verifies that a revoked operator (level 0)
// also cannot execute label-define. Level 0 is set via a real role-grant revocation.
func TestLabelDefine_Level0SenderRejected(t *testing.T) {
	h := storetest.New(t)

	const callerKey = "eeee0000000000000000000000000000000000000000000000000000000000000005"
	const creatorKey = "ffff0000000000000000000000000000000000000000000000000000000000000006"

	// Explicitly revoke the caller key.
	h.WithSender(creatorKey).RoleGrant(callerKey, "revoked")

	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define): %v", err)
	}

	exec := newRealExecutor(t, h.Store, h.CampfireID, creatorKey, callerKey)

	argsMap := map[string]any{
		"label": "also-rejected",
	}

	_, err = exec.Execute(context.Background(), decl, h.CampfireID, argsMap)
	if err == nil {
		t.Fatal("level-0 (revoked) sender should be rejected for label-define, got nil error")
	}
	if !strings.Contains(err.Error(), "operator provenance level") {
		t.Errorf("expected provenance error, got: %v", err)
	}
}

// TestLabelDefine_PatternRejection verifies that invalid label names are rejected
// by the convention executor (arg validation before the provenance gate).
func TestLabelDefine_PatternRejection(t *testing.T) {
	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define): %v", err)
	}

	// Use a level-2 caller so the rejection comes from pattern validation, not provenance.
	const callerKey = "test-key-level2-pattern"
	exec := convention.NewExecutorForTest(&noopBackend{}, callerKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{callerKey: 2}})

	tests := []struct {
		label string
		desc  string
	}{
		{"Bad.Label", "uppercase with dot — invalid"},
		{strings.Repeat("a", 33), "33 chars — exceeds max_length=32"},
		{"UPPER", "uppercase letters — invalid"},
		{"-leading-hyphen", "leading hyphen — invalid"},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			argsMap := map[string]any{
				"label": tc.label,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err == nil {
				t.Errorf("expected rejection for label %q (%s), got nil error", tc.label, tc.desc)
			}
		})
	}
}

// TestLabelDefine_ValidLabelAccepted verifies that valid label names pass the
// pattern validation and executor accepts them from a level-2 caller.
func TestLabelDefine_ValidLabelAccepted(t *testing.T) {
	decl, err := loadDeclaration("label-define")
	if err != nil {
		t.Fatalf("loadDeclaration(label-define): %v", err)
	}

	const callerKey = "test-key-valid-label"
	exec := convention.NewExecutorForTest(&noopBackend{}, callerKey).
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{callerKey: 2}})

	validLabels := []string{
		"bug-2",
		"feature",
		"my-custom-label",
		"a",
		"a" + strings.Repeat("b", 31), // 32 chars — at max_length
	}

	for _, label := range validLabels {
		t.Run(label, func(t *testing.T) {
			argsMap := map[string]any{
				"label": label,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err != nil {
				t.Errorf("expected acceptance for label %q, got error: %v", label, err)
			}
		})
	}
}

// TestLabelDefine_Declaration verifies the label-define.json declaration is
// correctly formed: convention=work, version=0.4, min_operator_level=2.
func TestLabelDefine_Declaration(t *testing.T) {
	data, err := loadDeclarationRaw("label-define")
	if err != nil {
		t.Fatalf("loadDeclarationRaw(label-define): %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal declaration: %v", err)
	}

	if conv, _ := m["convention"].(string); conv != "work" {
		t.Errorf("convention=%q, want \"work\"", conv)
	}
	if ver, _ := m["version"].(string); ver != "0.4" {
		t.Errorf("version=%q, want \"0.4\"", ver)
	}
	if op, _ := m["operation"].(string); op != "label-define" {
		t.Errorf("operation=%q, want \"label-define\"", op)
	}
	if level, ok := m["min_operator_level"].(float64); !ok || int(level) != 2 {
		t.Errorf("min_operator_level=%v, want 2", m["min_operator_level"])
	}
}

// loadDeclarationRaw returns the raw JSON bytes for a declaration by operation name.
// Used in tests that need to inspect the declaration JSON directly.
func loadDeclarationRaw(name string) ([]byte, error) {
	return declarations.Load(name)
}
