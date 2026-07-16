package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain installs the project-root sandbox guard for every test in this
// package. Any test that resolves a REAL project directory (via projectRoot or
// readyProjectDir) from outside its temp sandbox now panics loudly instead of
// silently reading/writing production .ready/ + .campfire/ state.
//
// This closes ready-b3b: during swarm ready-375, unisolated RunE-level tests
// executed from git worktrees under .claude/worktrees/<agent>/ walked up into
// the real ready project and minted three junk items ("Test bug item" ×2,
// "Test item"). The walk-up resolvers stop at any ancestor .ready/ or
// .campfire/root, so any command run from within the repo tree resolves
// production state unless the test first isolates cwd into t.TempDir().
//
// The correct fix for a tripped test is to isolate it with isolateTempDir(t)
// (or otherwise chdir into t.TempDir()) BEFORE invoking any command RunE or
// resolver. The guard is inert in production — projectDirGuard is nil there
// because this TestMain is compiled only into the test binary.
func TestMain(m *testing.M) {
	projectDirGuard = assertProjectDirSandboxed
	rdHomeGuard = assertRDHomeSandboxed
	os.Exit(m.Run())
}

// assertRDHomeSandboxed is the ready-bf8 analogue of assertProjectDirSandboxed:
// it panics unless the resolved rd home lives inside the process temp root, so a
// test that resolves RDHome() without isolating it (and would read/write the real
// ~/.config/rd nostr identity + config) fails loudly instead of leaking.
func assertRDHomeSandboxed(dir string) {
	if projectDirWithinTemp(dir) {
		return
	}
	panic(fmt.Sprintf(
		"ready-bf8: test resolved rd home %q outside the temp sandbox %q — "+
			"this test would read/write the REAL nostr identity + config (~/.config/rd). "+
			"Isolate it with isolateTempDir(t) (which pins RD_HOME) before resolving RDHome().",
		dir, os.TempDir()))
}

// assertProjectDirSandboxed panics unless dir lives inside the process temp root
// (os.TempDir()), where t.TempDir() and isolateTempDir place isolated project
// state. A resolved dir anywhere else means the test escaped its sandbox and is
// about to touch the real project — the ready-b3b failure mode.
func assertProjectDirSandboxed(dir string) {
	if projectDirWithinTemp(dir) {
		return
	}
	panic(fmt.Sprintf(
		"ready-b3b: test resolved project directory %q outside the temp sandbox %q — "+
			"this test would read/write the REAL project state (.ready/ + .campfire/). "+
			"Isolate it with isolateTempDir(t) before invoking any command RunE or resolver.",
		dir, os.TempDir()))
}

// projectDirWithinTemp reports whether dir is os.TempDir() or a descendant of
// it. Both paths are symlink-resolved first so the check is robust on platforms
// where the temp root is a symlink (e.g. macOS /var -> /private/var).
func projectDirWithinTemp(dir string) bool {
	tmp := os.TempDir()
	if r, err := filepath.EvalSymlinks(tmp); err == nil {
		tmp = r
	}
	d := dir
	if r, err := filepath.EvalSymlinks(dir); err == nil {
		d = r
	}
	rel, err := filepath.Rel(tmp, d)
	if err != nil {
		return false
	}
	return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator)))
}
