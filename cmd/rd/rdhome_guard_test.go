package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRDHomeGuard_RejectsRealHome proves the ready-bf8 guard fires when RDHome()
// resolves the real ~/.config/rd default outside the temp sandbox — the leak
// where identity/invite writes touched production state. isolateTempDir's chdir
// alone never covered RDHome() because the case-4 default ignores cwd.
func TestRDHomeGuard_RejectsRealHome(t *testing.T) {
	// Deliberately point RD_HOME at a non-sandbox path and confirm RDHome panics.
	// t.Setenv restores the prior value on cleanup.
	t.Setenv("RD_HOME", "/etc/definitely-not-a-sandbox")
	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected ready-bf8 guard panic resolving an unsandboxed rd home, got none")
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "ready-bf8") {
			t.Fatalf("panic did not carry the ready-bf8 marker: %v", r)
		}
	}()
	_ = RDHome()
}

// TestRDHomeGuard_AllowsIsolated confirms isolateTempDir(t) fully sandboxes the
// rd home: after it pins RD_HOME under t.TempDir(), RDHome() resolves inside the
// sandbox and does NOT panic.
func TestRDHomeGuard_AllowsIsolated(t *testing.T) {
	tempDir := isolateTempDir(t)
	got := RDHome() // must NOT panic
	want := filepath.Join(tempDir, ".rd-home")
	if got != want {
		t.Fatalf("RDHome under isolation = %q, want %q", got, want)
	}
	// And it is genuinely within the process temp root.
	if !projectDirWithinTemp(got) {
		t.Fatalf("isolated rd home %q is not within the temp sandbox %q", got, os.TempDir())
	}
}
