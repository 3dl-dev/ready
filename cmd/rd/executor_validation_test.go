package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
)

// noopBackend is a minimal ExecutorBackend that never actually sends anything.
// It is used to test executor argument validation, which happens before any network call.
type noopBackend struct{}

func (n *noopBackend) SendMessage(_ context.Context, _ string, _ []byte, _ []string, _ []string) (string, error) {
	return "msg-noop", nil
}

func (n *noopBackend) SendCampfireKeySigned(_ context.Context, _ string, _ []byte, _ []string, _ []string) (string, error) {
	return "msg-noop", nil
}

func (n *noopBackend) ReadMessages(_ context.Context, _ string, _ []string) ([]convention.MessageRecord, error) {
	return nil, nil
}

func (n *noopBackend) SendFutureAndAwait(_ context.Context, _ string, _ []byte, _ []string, _ []string, _ time.Duration) (string, []byte, error) {
	return "msg-noop", nil, nil
}

// TestExecutorValidates_BadType verifies that the executor rejects an invalid
// work:create type value ("foobar") before sending any network request.
// The create.json declaration declares type as an enum; the executor must validate
// args and return an error for values not in the allowed set.
func TestExecutorValidates_BadType(t *testing.T) {
	decl, err := loadDeclaration("create")
	if err != nil {
		t.Fatalf("loadDeclaration(create): %v", err)
	}

	exec := convention.NewExecutorForTest(&noopBackend{}, "test-key-hex").
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{"test-key-hex": 2}})

	argsMap := map[string]any{
		"id":       "ready-test-bad",
		"title":    "Test bad type",
		"type":     "foobar", // invalid — not in create.json enum
		"for":      "baron@3dl.dev",
		"priority": "p2",
	}

	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for invalid type 'foobar', got nil")
	}
	if !strings.Contains(err.Error(), "foobar") {
		t.Errorf("expected error to mention 'foobar', got: %q", err.Error())
	}
}

// TestExecutorValidates_BadPriority verifies that the executor rejects an invalid
// work:create priority value ("urgent") before sending any network request.
// The create.json declaration declares priority as an enum (p0–p3).
func TestExecutorValidates_BadPriority(t *testing.T) {
	decl, err := loadDeclaration("create")
	if err != nil {
		t.Fatalf("loadDeclaration(create): %v", err)
	}

	exec := convention.NewExecutorForTest(&noopBackend{}, "test-key-hex").
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{"test-key-hex": 2}})

	argsMap := map[string]any{
		"id":       "ready-test-bad",
		"title":    "Test bad priority",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "urgent", // invalid — not in create.json enum (p0/p1/p2/p3)
	}

	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err == nil {
		t.Fatal("expected error for invalid priority 'urgent', got nil")
	}
	if !strings.Contains(err.Error(), "urgent") {
		t.Errorf("expected error to mention 'urgent', got: %q", err.Error())
	}
}

// TestExecutorValidates_ValidTypeAndPriority verifies that the executor accepts
// a valid type and priority without error (noop backend never sends).
func TestExecutorValidates_ValidTypeAndPriority(t *testing.T) {
	decl, err := loadDeclaration("create")
	if err != nil {
		t.Fatalf("loadDeclaration(create): %v", err)
	}

	exec := convention.NewExecutorForTest(&noopBackend{}, "test-key-hex").
		WithProvenance(&staticProvenanceChecker{levels: map[string]int{"test-key-hex": 2}})

	argsMap := map[string]any{
		"id":       "ready-test-valid",
		"title":    "Test valid args",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p1",
	}

	_, err = exec.Execute(context.Background(), decl, "cf-test-campfire", argsMap)
	if err != nil {
		t.Errorf("expected no error for valid type/priority, got: %v", err)
	}
}
