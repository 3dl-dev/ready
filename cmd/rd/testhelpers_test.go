package main

import (
	"os"
	"path/filepath"
	"testing"
)

// isolateTempDir changes to a temporary directory and defers chdir back, and
// (ready-bf8) points RD_HOME at a sandbox dir under that temp so identity/invite
// writes never touch the real ~/.config/rd. This prevents projectRoot() from
// finding a parent .campfire/root AND stops RDHome() from resolving the
// production identity home. Shared across cmd/rd unit tests.
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
	// ready-bf8: RDHome()'s default (~/.config/rd) is independent of cwd, so the
	// chdir above does not isolate it. Pin RD_HOME into the sandbox. t.Setenv
	// restores the prior value on cleanup.
	t.Setenv("RD_HOME", filepath.Join(tempDir, ".rd-home"))
	return tempDir
}
