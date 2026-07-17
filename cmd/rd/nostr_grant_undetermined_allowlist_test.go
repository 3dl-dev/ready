package main

// nostr_grant_undetermined_allowlist_test.go — ready-a76: the allowlist git-status
// check used to FAIL-OPEN to "not dirty" whenever `git status` itself errored — git
// binary
// absent, or the allowlist file living outside any git work tree — not just when git
// determines the file is clean. That fail-open let the automatic grant-time regen
// silently overwrite an operator's uncommitted hand-edit with no warning whenever the
// git check simply couldn't run. Fix: when cleanliness CANNOT be determined and the
// target file exists with non-empty content, be conservative — skip the regen and
// warn, mirroring the ready-0df dirty-file guard, instead of writing over it. A file
// that is absent or empty has nothing to lose, so that case still regenerates (as does
// the ordinary in-repo clean case, unchanged from ready-0df — see
// TestGrantAllBoards_CleanWriteAllowlist_RegeneratesAsBefore).

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestSurfaceAllowlistRegen_GitStatusUndetermined_NonEmptyFile_SkipsRegen is the
// ready-a76 done condition: when the git check cannot run at all (simulated here by
// stripping git off PATH, so `git status` fails exactly as it would with git binary
// absent or the file outside any work tree) and the target allowlist file EXISTS with
// non-empty content, surfaceAllowlistRegen must NOT overwrite it — skip + warn.
func TestSurfaceAllowlistRegen_GitStatusUndetermined_NonEmptyFile_SkipsRegen(t *testing.T) {
	dir := isolateTempDir(t) // ready-bf8: pins RD_HOME so a reached regen never hits ~/.config/rd

	file := filepath.Join(dir, "scripts", "relay-policy", "write-allowlist.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("mkdir allowlist dir: %v", err)
	}
	handEdited := "{\n  \"aaaa\": \"hand-edited, git status undeterminable\"\n}\n"
	if err := os.WriteFile(file, []byte(handEdited), 0o644); err != nil {
		t.Fatalf("write allowlist: %v", err)
	}

	// Strip git off PATH so BOTH defaultAllowlistFile()'s `git rev-parse
	// --show-toplevel` and checkAllowlistGitStatus's `git status` fail with
	// "executable file not found" — the git-binary-absent case named in the item,
	// and representative of any git error (also covers "outside a work tree").
	t.Setenv("PATH", "")

	stderr := captureStderrPipe(t, func() {
		surfaceAllowlistRegen(dir)
	})

	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("ReadFile allowlist: %v", err)
	}
	if string(got) != handEdited {
		t.Fatalf("write-allowlist.json was overwritten despite undetermined git status:\n got=%q\nwant=%q", got, handEdited)
	}
	if !strings.Contains(stderr, "cannot determine git status") {
		t.Fatalf("expected a 'cannot determine git status' warning, got stderr=%q", stderr)
	}
}

// TestSurfaceAllowlistRegen_GitStatusUndetermined_AbsentFile_RegeneratesAsBefore is
// the boundary companion: when the git check cannot run AND the target file does not
// exist yet, there is nothing an operator's edit could lose — the conservative skip
// only guards an EXISTING non-empty file, so an absent file still gets a fresh
// allowlist written (fail-open only where fail-open is harmless).
func TestSurfaceAllowlistRegen_GitStatusUndetermined_AbsentFile_RegeneratesAsBefore(t *testing.T) {
	_, _, dirByName := setupOwnedBoardsUnderRoot(t, "repogamma")
	dir := dirByName["repogamma"]

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}

	// No write-allowlist.json exists at all under dir yet.
	file := filepath.Join(dir, "scripts", "relay-policy", "write-allowlist.json")
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Fatalf("precondition: expected %s absent, stat err=%v", file, err)
	}

	// Strip git off PATH: defaultAllowlistFile() falls back to the repo-relative
	// path (still resolving to `file` above since cwd is dir), and the git-status
	// check itself cannot run either.
	t.Setenv("PATH", "")

	surfaceAllowlistRegen(dir)

	if _, err := os.Stat(file); err != nil {
		t.Fatalf("expected write-allowlist.json to be regenerated despite undetermined git status, stat err=%v", err)
	}
}
