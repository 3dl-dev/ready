package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestProjectRootGuard_RejectsUnisolatedResolution proves the ready-b3b guard's
// core promise: a command run from a SUBDIRECTORY of a real project tree, with
// deliberately missing isolation, resolves the project via walk-up and FAILS
// LOUDLY (ready-b3b panic) instead of silently returning the real project dir —
// which is what let junk items ("Test bug item" ×2, "Test item") leak into the
// production ready campfire from git worktrees during swarm ready-375.
//
// The proof is HERMETIC: it builds its own project tree rather than depending on
// the host repo's .ready/ + .campfire/root, which are gitignored and therefore
// absent on a clean checkout / CI. Both guarded resolvers are exercised — the
// .campfire path (projectRoot) and the .ready-only JSONL path (readyProjectDir).
func TestProjectRootGuard_RejectsUnisolatedResolution(t *testing.T) {
	t.Run("campfire walk-up (projectRoot)", func(t *testing.T) {
		project := chdirIntoUnsandboxedProject(t, true)
		assertGuardPanics(t, project, func() { projectRoot() })
	})
	t.Run("jsonl walk-up (readyProjectDir)", func(t *testing.T) {
		project := chdirIntoUnsandboxedProject(t, false)
		assertGuardPanics(t, project, func() { readyProjectDir() })
	})
}

// TestProjectRootGuard_AllowsIsolatedResolution confirms the guard is silent for
// a properly isolated test: a project rooted under t.TempDir() resolves normally.
func TestProjectRootGuard_AllowsIsolatedResolution(t *testing.T) {
	dir := isolateTempDir(t) // chdir into t.TempDir() (under os.TempDir())
	if err := os.Mkdir(filepath.Join(dir, ".ready"), 0o755); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}

	got, found := readyProjectDir() // must NOT panic
	if !found {
		t.Fatal("expected readyProjectDir to resolve the isolated temp project")
	}
	// Compare via resolved symlinks; a failure here must surface, not be swallowed
	// into a vacuous empty-string match.
	wantEval, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("evalsymlinks(want): %v", err)
	}
	gotEval, err := filepath.EvalSymlinks(got)
	if err != nil {
		t.Fatalf("evalsymlinks(got): %v", err)
	}
	if gotEval != wantEval {
		t.Fatalf("resolved %q, want isolated temp project %q", gotEval, wantEval)
	}
}

// chdirIntoUnsandboxedProject builds a self-contained project tree that the
// guard treats as OUTSIDE the temp sandbox, then chdir's into a nested
// subdirectory of it (so resolution must walk UP, reproducing the ready-b3b
// worktree topology). It returns the project root.
//
// The trick: create the project under the real os.TempDir(), then redirect
// os.TempDir() (via TMPDIR) to a SIBLING temp dir. The project is now no longer
// "within the sandbox" from the guard's perspective, so resolving it fires the
// guard — exactly as resolving /home/baron/projects/ready would in CI. This
// keeps the proof hermetic (no host-repo state) and portable (clean checkout).
func chdirIntoUnsandboxedProject(t *testing.T, campfire bool) string {
	t.Helper()
	// Both created under the ORIGINAL os.TempDir(), before the redirect below.
	project := t.TempDir()
	sandbox := t.TempDir()
	// os.TempDir() now resolves to sandbox; project is a sibling, i.e. outside it.
	t.Setenv("TMPDIR", sandbox)

	if err := os.MkdirAll(filepath.Join(project, ".ready"), 0o755); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	if campfire {
		cfDir := filepath.Join(project, ".campfire")
		if err := os.MkdirAll(cfDir, 0o755); err != nil {
			t.Fatalf("mkdir .campfire: %v", err)
		}
		// projectRoot() only accepts a 64-char root id.
		id := strings.Repeat("a", 64)
		if err := os.WriteFile(filepath.Join(cfDir, "root"), []byte(id), 0o644); err != nil {
			t.Fatalf("write .campfire/root: %v", err)
		}
	}

	sub := filepath.Join(project, "nested", "deep")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("mkdir nested subdir: %v", err)
	}
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	if err := os.Chdir(sub); err != nil {
		t.Fatalf("chdir nested subdir: %v", err)
	}
	return project
}

// assertGuardPanics runs fn and asserts it panics with the ready-b3b guard
// marker. If fn returns without panicking, the guard failed to fire.
func assertGuardPanics(t *testing.T, project string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Errorf("expected ready-b3b guard panic resolving unsandboxed project %q, got none", project)
			return
		}
		msg, ok := r.(string)
		if !ok || !strings.Contains(msg, "ready-b3b") {
			t.Errorf("panic did not carry the ready-b3b guard marker: %v", r)
		}
	}()
	fn()
}
