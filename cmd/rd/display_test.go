package main

import (
	"os"
	"strings"
	"testing"
)

// TestFormatCampfireIDForDisplay_DebugFlagShowsHex tests that --debug flag
// causes hex IDs to be displayed unchanged.
func TestFormatCampfireIDForDisplay_DebugFlagShowsHex(t *testing.T) {
	originalDebug := debugOutput
	defer func() { debugOutput = originalDebug }()

	hexID := strings.Repeat("ab", 32) // 64-char hex ID
	debugOutput = true

	result := formatCampfireIDForDisplay(hexID)
	if result != hexID {
		t.Errorf("with --debug, expected hex unchanged, got %q", result)
	}
}

// TestFormatCampfireIDForDisplay_NonHexPassthrough tests that non-64-char
// strings are passed through unchanged.
func TestFormatCampfireIDForDisplay_NonHexPassthrough(t *testing.T) {
	originalDebug := debugOutput
	defer func() { debugOutput = originalDebug }()

	debugOutput = false

	testCases := []string{
		"",           // empty
		"short",      // short string
		"not-64-hex", // not hex length
	}

	for _, tc := range testCases {
		result := formatCampfireIDForDisplay(tc)
		if result != tc {
			t.Errorf("expected non-64-char %q to pass through unchanged, got %q", tc, result)
		}
	}
}

// TestFormatCampfireIDForDisplay_NoConfigFallsback tests that when no
// .ready/config.json exists, the hex ID is returned unchanged.
func TestFormatCampfireIDForDisplay_NoConfigFallsback(t *testing.T) {
	originalDebug := debugOutput
	defer func() { debugOutput = originalDebug }()

	debugOutput = false

	// Create a temporary directory that has no .ready/config.json
	tmpDir, err := os.MkdirTemp("", "test-no-config")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Change to the temp directory
	originalCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("failed to chdir: %v", err)
	}
	defer os.Chdir(originalCwd)

	hexID := strings.Repeat("cd", 32) // 64-char hex ID
	result := formatCampfireIDForDisplay(hexID)

	if result != hexID {
		t.Errorf("with no config, expected hex unchanged, got %q", result)
	}
}
