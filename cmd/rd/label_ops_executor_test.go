package main

// label_ops_executor_test.go — executor-level tests for label-add and label-remove ops.
//
// Done conditions from i3.md:
//   - Executor accepts label-add/label-remove for any member (no min_operator_level).
//   - Malformed label (pattern-invalid) is rejected by the executor (write-side gate).
//   - Valid label passes the executor's arg validation.
//   - Declarations are correctly formed: convention=work, version=0.4, no min_operator_level.

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// label-add executor tests.
// ---------------------------------------------------------------------------

// TestLabelAdd_Declaration verifies label_add.json is correctly formed.
func TestLabelAdd_Declaration(t *testing.T) {
	data, err := loadDeclarationRaw("label-add")
	if err != nil {
		t.Fatalf("loadDeclarationRaw(label-add): %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal label-add declaration: %v", err)
	}

	if conv, _ := m["convention"].(string); conv != "work" {
		t.Errorf("convention=%q, want \"work\"", conv)
	}
	if ver, _ := m["version"].(string); ver != "0.4" {
		t.Errorf("version=%q, want \"0.4\"", ver)
	}
	if op, _ := m["operation"].(string); op != "label-add" {
		t.Errorf("operation=%q, want \"label-add\"", op)
	}
	// No min_operator_level — any member can add labels.
	if level, ok := m["min_operator_level"]; ok {
		t.Errorf("label-add must have NO min_operator_level (any member can add labels), got: %v", level)
	}
}

// TestLabelRemove_Declaration verifies label_remove.json is correctly formed.
func TestLabelRemove_Declaration(t *testing.T) {
	data, err := loadDeclarationRaw("label-remove")
	if err != nil {
		t.Fatalf("loadDeclarationRaw(label-remove): %v", err)
	}

	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("unmarshal label-remove declaration: %v", err)
	}

	if conv, _ := m["convention"].(string); conv != "work" {
		t.Errorf("convention=%q, want \"work\"", conv)
	}
	if ver, _ := m["version"].(string); ver != "0.4" {
		t.Errorf("version=%q, want \"0.4\"", ver)
	}
	if op, _ := m["operation"].(string); op != "label-remove" {
		t.Errorf("operation=%q, want \"label-remove\"", op)
	}
	// No min_operator_level — any member can remove labels.
	if level, ok := m["min_operator_level"]; ok {
		t.Errorf("label-remove must have NO min_operator_level (any member can remove labels), got: %v", level)
	}
}

// TestLabelAdd_ValidArgs_Accepted verifies that the executor accepts valid
// label-add args (valid item id + valid label pattern).
func TestLabelAdd_ValidArgs_Accepted(t *testing.T) {
	decl, err := loadDeclaration("label-add")
	if err != nil {
		t.Fatalf("loadDeclaration(label-add): %v", err)
	}

	exec := newNoopExecutor()

	tests := []struct {
		itemID string
		label  string
		desc   string
	}{
		{"ready-94a", "bug", "seed label on valid item id"},
		{"ready-abc", "security", "security seed label"},
		{"item-xyz", "my-custom-label", "user-defined label name"},
		{"item-abc", "a" + strings.Repeat("b", 31), "32-char label (max length)"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			argsMap := map[string]any{
				"id":    tc.itemID,
				"label": tc.label,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err != nil {
				t.Errorf("executor should accept id=%q label=%q (%s), got error: %v",
					tc.itemID, tc.label, tc.desc, err)
			}
		})
	}
}

// TestLabelAdd_MalformedLabel_Rejected verifies that the executor rejects label-add
// when the label arg fails pattern validation (write-side gate).
func TestLabelAdd_MalformedLabel_Rejected(t *testing.T) {
	decl, err := loadDeclaration("label-add")
	if err != nil {
		t.Fatalf("loadDeclaration(label-add): %v", err)
	}

	exec := newNoopExecutor()

	tests := []struct {
		label string
		desc  string
	}{
		{"Bad Label!", "uppercase, space, exclamation — invalid"},
		{"UPPERCASE", "all uppercase"},
		{"-leading-hyphen", "leading hyphen — invalid"},
		{strings.Repeat("a", 33), "33 chars — exceeds max_length=32"},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			argsMap := map[string]any{
				"id":    "ready-94a",
				"label": tc.label,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err == nil {
				t.Errorf("executor should reject label=%q (%s), but accepted it", tc.label, tc.desc)
			}
		})
	}
}

// TestLabelRemove_ValidArgs_Accepted verifies that the executor accepts valid
// label-remove args.
func TestLabelRemove_ValidArgs_Accepted(t *testing.T) {
	decl, err := loadDeclaration("label-remove")
	if err != nil {
		t.Fatalf("loadDeclaration(label-remove): %v", err)
	}

	exec := newNoopExecutor()

	tests := []struct {
		itemID string
		label  string
		desc   string
	}{
		{"ready-94a", "bug", "seed label"},
		{"item-abc", "feature", "feature seed label"},
		{"ready-abc", "blog-candidate", "blog-candidate seed label"},
	}

	for _, tc := range tests {
		t.Run(tc.desc, func(t *testing.T) {
			argsMap := map[string]any{
				"id":    tc.itemID,
				"label": tc.label,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err != nil {
				t.Errorf("executor should accept label-remove id=%q label=%q (%s), got error: %v",
					tc.itemID, tc.label, tc.desc, err)
			}
		})
	}
}

// TestLabelRemove_MalformedLabel_Rejected verifies that the executor rejects
// label-remove when the label arg fails pattern validation.
func TestLabelRemove_MalformedLabel_Rejected(t *testing.T) {
	decl, err := loadDeclaration("label-remove")
	if err != nil {
		t.Fatalf("loadDeclaration(label-remove): %v", err)
	}

	exec := newNoopExecutor()

	tests := []struct {
		label string
		desc  string
	}{
		{"Bad Label!", "uppercase, space, exclamation"},
		{"UPPERCASE", "all uppercase"},
		{"-leading-hyphen", "leading hyphen"},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			argsMap := map[string]any{
				"id":    "ready-94a",
				"label": tc.label,
			}
			_, err := exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
			if err == nil {
				t.Errorf("executor should reject label-remove label=%q (%s), but accepted it", tc.label, tc.desc)
			}
		})
	}
}

// TestLabelAdd_MissingRequiredArgs_Rejected verifies that label-add rejects requests
// missing required args.
func TestLabelAdd_MissingRequiredArgs_Rejected(t *testing.T) {
	decl, err := loadDeclaration("label-add")
	if err != nil {
		t.Fatalf("loadDeclaration(label-add): %v", err)
	}

	exec := newNoopExecutor()

	// Missing label arg.
	argsMap := map[string]any{
		"id": "ready-94a",
		// "label" missing
	}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Error("executor should reject label-add with missing 'label' arg, but accepted it")
	}

	// Missing id arg.
	argsMap2 := map[string]any{
		// "id" missing
		"label": "bug",
	}
	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap2)
	if err == nil {
		t.Error("executor should reject label-add with missing 'id' arg, but accepted it")
	}
}
