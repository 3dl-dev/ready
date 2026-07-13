package main

// cfhome_test.go — tests for CFHome resolution including walk-up identity detection.

import (
	"os"
	"path/filepath"
	"testing"
)

// saveAndClearCFHomeState saves rdHome, CF_HOME, HOME, and cwd, returning a
// restore function. All CFHome tests need this to isolate from the real env.
func saveAndClearCFHomeState(t *testing.T) func() {
	t.Helper()
	oldRdHome := rdHome
	oldEnv := os.Getenv("CF_HOME")
	oldHomeEnv := os.Getenv("HOME")
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("failed to get cwd: %v", err)
	}
	return func() {
		rdHome = oldRdHome
		if oldEnv != "" {
			os.Setenv("CF_HOME", oldEnv)
		} else {
			os.Unsetenv("CF_HOME")
		}
		if oldHomeEnv != "" {
			os.Setenv("HOME", oldHomeEnv)
		} else {
			os.Unsetenv("HOME")
		}
		os.Chdir(oldCwd)
	}
}

// TestCFHome_FlagTakesHighestPriority verifies that the --cf-home flag takes
// precedence over all other sources.
func TestCFHome_FlagTakesHighestPriority(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	rdHome = "/tmp/flag-cf-home"
	os.Setenv("CF_HOME", "/tmp/env-cf-home")

	result := CFHome()
	if result != "/tmp/flag-cf-home" {
		t.Errorf("expected /tmp/flag-cf-home, got %q", result)
	}
}

// TestCFHome_EnvSecondPriority verifies that CF_HOME env var is used when
// the flag is not set.
func TestCFHome_EnvSecondPriority(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	rdHome = ""
	os.Setenv("CF_HOME", "/tmp/env-cf-home")

	result := CFHome()
	if result != "/tmp/env-cf-home" {
		t.Errorf("expected /tmp/env-cf-home, got %q", result)
	}
}

// TestCFHome_NewInstallPath verifies that ~/.cf is used when it exists and
// no flag/env is set.
func TestCFHome_NewInstallPath(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpHome := t.TempDir()
	newPath := filepath.Join(tmpHome, ".cf")
	if err := os.MkdirAll(newPath, 0755); err != nil {
		t.Fatalf("failed to create .cf directory: %v", err)
	}

	os.Setenv("HOME", tmpHome)
	os.Chdir(tmpHome)
	rdHome = ""
	os.Unsetenv("CF_HOME")

	result := CFHome()
	if result != newPath {
		t.Errorf("expected %q, got %q", newPath, result)
	}
}

// TestCFHome_LegacyUserPath verifies that ~/.campfire is used when it exists
// but ~/.cf does not.
func TestCFHome_LegacyUserPath(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpHome := t.TempDir()
	legacyPath := filepath.Join(tmpHome, ".campfire")
	if err := os.MkdirAll(legacyPath, 0755); err != nil {
		t.Fatalf("failed to create .campfire directory: %v", err)
	}

	os.Setenv("HOME", tmpHome)
	os.Chdir(tmpHome)
	rdHome = ""
	os.Unsetenv("CF_HOME")

	result := CFHome()
	if result != legacyPath {
		t.Errorf("expected %q, got %q", legacyPath, result)
	}
}

// TestCFHome_NewInstallDefault verifies that ~/.cf is returned when neither
// ~/.cf nor ~/.campfire exists (new install).
func TestCFHome_NewInstallDefault(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpHome := t.TempDir()
	os.Setenv("HOME", tmpHome)
	os.Chdir(tmpHome)
	rdHome = ""
	os.Unsetenv("CF_HOME")

	result := CFHome()
	expected := filepath.Join(tmpHome, ".cf")
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

// TestCFHome_NewPathTakesPreferenceOverLegacy verifies that ~/.cf is preferred
// over ~/.campfire when both exist.
func TestCFHome_NewPathTakesPreferenceOverLegacy(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpHome := t.TempDir()
	newPath := filepath.Join(tmpHome, ".cf")
	legacyPath := filepath.Join(tmpHome, ".campfire")
	os.MkdirAll(newPath, 0755)
	os.MkdirAll(legacyPath, 0755)

	os.Setenv("HOME", tmpHome)
	os.Chdir(tmpHome)
	rdHome = ""
	os.Unsetenv("CF_HOME")

	result := CFHome()
	if result != newPath {
		t.Errorf("expected %q (new path), got %q", newPath, result)
	}
}

// --- Walk-up identity resolution tests ---

// TestCFHome_WalkUpFindsIdentity verifies that CFHome() walks up from cwd
// to find a .cf/identity.json in an ancestor directory.
func TestCFHome_WalkUpFindsIdentity(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpDir := t.TempDir()
	project := filepath.Join(tmpDir, "project")
	cfDir := filepath.Join(project, ".cf")
	subdir := filepath.Join(project, "worktree", "subdir")
	os.MkdirAll(cfDir, 0755)
	os.WriteFile(filepath.Join(cfDir, "identity.json"), []byte(`{}`), 0644)
	os.MkdirAll(subdir, 0755)

	os.Setenv("HOME", filepath.Join(tmpDir, "fakehome"))
	rdHome = ""
	os.Unsetenv("CF_HOME")
	os.Chdir(subdir)

	result := CFHome()
	if result != cfDir {
		t.Errorf("expected walk-up to find %q, got %q", cfDir, result)
	}
}

// TestCFHome_WalkUpSiblingIsolation verifies that two sibling worktrees
// with different .cf/identity.json resolve to different CF homes.
func TestCFHome_WalkUpSiblingIsolation(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpDir := t.TempDir()

	aCF := filepath.Join(tmpDir, "worktree-a", ".cf")
	os.MkdirAll(aCF, 0755)
	os.WriteFile(filepath.Join(aCF, "identity.json"), []byte(`{"key":"a"}`), 0644)

	bCF := filepath.Join(tmpDir, "worktree-b", ".cf")
	os.MkdirAll(bCF, 0755)
	os.WriteFile(filepath.Join(bCF, "identity.json"), []byte(`{"key":"b"}`), 0644)

	os.Setenv("HOME", filepath.Join(tmpDir, "fakehome"))
	rdHome = ""
	os.Unsetenv("CF_HOME")

	os.Chdir(filepath.Join(tmpDir, "worktree-a"))
	resultA := CFHome()
	if resultA != aCF {
		t.Errorf("from worktree-a: expected %q, got %q", aCF, resultA)
	}

	os.Chdir(filepath.Join(tmpDir, "worktree-b"))
	resultB := CFHome()
	if resultB != bCF {
		t.Errorf("from worktree-b: expected %q, got %q", bCF, resultB)
	}

	if resultA == resultB {
		t.Error("sibling worktrees should resolve to different CF homes")
	}
}

// TestCFHome_FlagOverridesWalkUp verifies that --cf-home flag takes
// precedence over a walk-up match.
func TestCFHome_FlagOverridesWalkUp(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpDir := t.TempDir()
	cfDir := filepath.Join(tmpDir, ".cf")
	os.MkdirAll(cfDir, 0755)
	os.WriteFile(filepath.Join(cfDir, "identity.json"), []byte(`{}`), 0644)

	os.Setenv("HOME", filepath.Join(tmpDir, "fakehome"))
	os.Unsetenv("CF_HOME")
	os.Chdir(tmpDir)

	rdHome = "/tmp/flag-override"
	result := CFHome()
	if result != "/tmp/flag-override" {
		t.Errorf("flag should override walk-up, got %q", result)
	}
}

// TestCFHome_EnvOverridesWalkUp verifies that CF_HOME env var takes
// precedence over a walk-up match.
func TestCFHome_EnvOverridesWalkUp(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpDir := t.TempDir()
	cfDir := filepath.Join(tmpDir, ".cf")
	os.MkdirAll(cfDir, 0755)
	os.WriteFile(filepath.Join(cfDir, "identity.json"), []byte(`{}`), 0644)

	os.Setenv("HOME", filepath.Join(tmpDir, "fakehome"))
	os.Setenv("CF_HOME", "/tmp/env-override")
	os.Chdir(tmpDir)

	rdHome = ""
	result := CFHome()
	if result != "/tmp/env-override" {
		t.Errorf("CF_HOME env should override walk-up, got %q", result)
	}
}

// TestCFHome_WalkUpSkipsEmptyCFDir verifies that a .cf/ directory without
// identity.json is skipped during walk-up.
func TestCFHome_WalkUpSkipsEmptyCFDir(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpDir := t.TempDir()
	// Empty .cf/ at project level (no identity.json)
	os.MkdirAll(filepath.Join(tmpDir, "project", ".cf"), 0755)
	// Valid .cf/identity.json at parent level
	parentCF := filepath.Join(tmpDir, ".cf")
	os.MkdirAll(parentCF, 0755)
	os.WriteFile(filepath.Join(parentCF, "identity.json"), []byte(`{}`), 0644)

	subdir := filepath.Join(tmpDir, "project", "src")
	os.MkdirAll(subdir, 0755)

	os.Setenv("HOME", filepath.Join(tmpDir, "fakehome"))
	rdHome = ""
	os.Unsetenv("CF_HOME")
	os.Chdir(subdir)

	result := CFHome()
	if result != parentCF {
		t.Errorf("should skip empty .cf/ and find parent, expected %q, got %q", parentCF, result)
	}
}

// TestCFHome_WalkUpFallsBackToGlobal verifies that when no .cf/identity.json
// is found during walk-up, the global ~/.cf fallback is used.
func TestCFHome_WalkUpFallsBackToGlobal(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpDir := t.TempDir()
	fakeHome := filepath.Join(tmpDir, "fakehome")
	globalCF := filepath.Join(fakeHome, ".cf")
	os.MkdirAll(globalCF, 0755)

	workdir := filepath.Join(tmpDir, "some", "deep", "path")
	os.MkdirAll(workdir, 0755)

	os.Setenv("HOME", fakeHome)
	rdHome = ""
	os.Unsetenv("CF_HOME")
	os.Chdir(workdir)

	result := CFHome()
	if result != globalCF {
		t.Errorf("should fall back to global ~/.cf, expected %q, got %q", globalCF, result)
	}
}

// TestCFHome_WalkUpSkipsGlobalCF verifies that the walk-up does not match
// ~/.cf itself (that's handled by the global fallback path, not walk-up).
func TestCFHome_WalkUpSkipsGlobalCF(t *testing.T) {
	defer saveAndClearCFHomeState(t)()

	tmpDir := t.TempDir()
	fakeHome := filepath.Join(tmpDir, "home")
	globalCF := filepath.Join(fakeHome, ".cf")
	os.MkdirAll(globalCF, 0755)
	os.WriteFile(filepath.Join(globalCF, "identity.json"), []byte(`{}`), 0644)

	workdir := filepath.Join(fakeHome, "projects", "myproject")
	os.MkdirAll(workdir, 0755)

	os.Setenv("HOME", fakeHome)
	rdHome = ""
	os.Unsetenv("CF_HOME")
	os.Chdir(workdir)

	walkResult := cfHomeWalkUp()
	if walkResult != "" {
		t.Errorf("walk-up should skip ~/.cf, but returned %q", walkResult)
	}

	result := CFHome()
	if result != globalCF {
		t.Errorf("expected global fallback %q, got %q", globalCF, result)
	}
}
