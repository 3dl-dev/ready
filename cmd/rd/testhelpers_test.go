package main

import (
	"os"
	"testing"
)

// isolateTempDir changes to a temporary directory and defers chdir back.
// This prevents projectRoot() from finding a parent .campfire/root in the test
// environment. Shared across cmd/rd unit tests.
func isolateTempDir(t *testing.T) string {
	tempDir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { os.Chdir(orig) })
	if err := os.Chdir(tempDir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return tempDir
}
