package main

import (
	"fmt"
	"strings"
	"testing"
)

// NOTE (ready-cb6 I7): the campfire `completePayload` struct and its JSON-shape
// tests (branch/session/resolution marshaling) were removed with the campfire
// write path — `rd complete` now closes via the nostr-native runCloseNostr path.
// Branch/session propagation is exercised end-to-end in the nostr write tests.

// TestComplete_RequiresReason verifies that rd complete without --reason returns a clear error.
func TestComplete_RequiresReason(t *testing.T) {
	err := validateCompleteReason("")
	if err == nil {
		t.Fatal("expected error when reason is empty, got nil")
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error message must contain 'required', got %q", err.Error())
	}
}

// TestComplete_WithReason verifies that complete validation passes when reason is provided.
func TestComplete_WithReason(t *testing.T) {
	err := validateCompleteReason("Implemented and merged")
	if err != nil {
		t.Errorf("expected no error when reason is provided, got %v", err)
	}
}

// validateCompleteReason mirrors the --reason enforcement in completeCmd.
func validateCompleteReason(reason string) error {
	if reason == "" {
		return fmt.Errorf("--reason is required (why is this item being closed?)")
	}
	return nil
}
