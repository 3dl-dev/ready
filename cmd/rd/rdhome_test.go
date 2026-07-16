package main

import (
	"os"
	"path/filepath"
	"testing"
)

// saveAndClearRDHomeState isolates every input RDHome() reads: the --rd-home flag
// global, RD_HOME, XDG_CONFIG_HOME, HOME, and cwd. RD_HOME / XDG are cleared via
// t.Setenv("") (RDHome treats "" as unset), auto-restored at test end.
func saveAndClearRDHomeState(t *testing.T) {
	t.Helper()
	oldFlag := rdHomeFlag
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		rdHomeFlag = oldFlag
		_ = os.Chdir(oldCwd)
	})
	rdHomeFlag = ""
	t.Setenv("RD_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
}

func TestRDHome_FlagHighestPriority(t *testing.T) {
	saveAndClearRDHomeState(t)
	rdHomeFlag = "/tmp/flag-rd-home"
	t.Setenv("RD_HOME", "/tmp/env-rd-home")
	if got := RDHome(); got != "/tmp/flag-rd-home" {
		t.Fatalf("flag must win: got %q", got)
	}
}

func TestRDHome_EnvSecondPriority(t *testing.T) {
	saveAndClearRDHomeState(t)
	t.Setenv("RD_HOME", "/tmp/env-rd-home")
	if got := RDHome(); got != "/tmp/env-rd-home" {
		t.Fatalf("env must win when flag unset: got %q", got)
	}
}

func TestRDHome_WalkUpMarkerThirdPriority(t *testing.T) {
	saveAndClearRDHomeState(t)
	// A repo-like tree with a .rd/ marker at the top and a nested cwd below it.
	top := t.TempDir()
	marker := filepath.Join(top, ".rd")
	if err := os.MkdirAll(marker, 0o700); err != nil {
		t.Fatalf("mkdir .rd: %v", err)
	}
	nested := filepath.Join(top, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	got := RDHome()
	// macOS /tmp is a symlink to /private/tmp; compare resolved paths.
	gotResolved, _ := filepath.EvalSymlinks(got)
	wantResolved, _ := filepath.EvalSymlinks(marker)
	if gotResolved != wantResolved {
		t.Fatalf("walk-up must find the .rd marker: got %q want %q", got, marker)
	}
}

func TestRDHome_DefaultXDG(t *testing.T) {
	saveAndClearRDHomeState(t)
	// cwd must have no .rd ancestor, else walk-up would pre-empt the default.
	empty := t.TempDir()
	if err := os.Chdir(empty); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := RDHome(); got != filepath.Join("/tmp/xdg", "rd") {
		t.Fatalf("XDG_CONFIG_HOME default: got %q", got)
	}
}

func TestRDHome_DefaultConfigDir(t *testing.T) {
	saveAndClearRDHomeState(t)
	empty := t.TempDir()
	if err := os.Chdir(empty); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	// Assert the pure resolution cascade (resolveRDHome), not RDHome() — the
	// latter is intentionally guarded (ready-bf8) to panic when a test resolves
	// the real ~/.config/rd home. Here we are verifying the default path string,
	// not touching the home, so the unguarded resolver is the right unit.
	if got := resolveRDHome(); got != filepath.Join(home, ".config", "rd") {
		t.Fatalf("default ~/.config/rd: got %q", got)
	}
}
