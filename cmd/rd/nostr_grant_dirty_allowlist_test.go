package main

// nostr_grant_dirty_allowlist_test.go — ready-0df: `rd grant --all-boards` (and the
// single-board `rd grant`/`rd revoke`) MUST NOT clobber an operator's uncommitted edits
// to scripts/relay-policy/write-allowlist.json as a side effect of publishing a grant.
// The grant EVENT (the authoritative kind-39301 act) is separable from the relay
// ALLOWLIST FILE regen (an ops step that `rd relay sync-allowlist --apply` owns): when
// the target allowlist file has uncommitted git changes, the automatic regen SKIPS +
// WARNS instead of overwriting it; when the file is clean (or absent), it regenerates
// exactly as before. Either way the grant event still publishes.

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// gitInitAllowlistRepo turns dir into its own tiny git repo containing a COMMITTED
// scripts/relay-policy/write-allowlist.json with the given initial content, so
// defaultAllowlistFile() (which shells out to `git rev-parse --show-toplevel` from the
// process cwd) resolves the file INSIDE a real git worktree — the exact condition
// under which "uncommitted changes" is a meaningful, checkable git fact. Returns the
// allowlist file's absolute path.
func gitInitAllowlistRepo(t *testing.T, dir, initialContent string) string {
	t.Helper()
	runGit := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	runGit("init", "-q")
	runGit("-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "--allow-empty", "-q", "-m", "init")

	file := filepath.Join(dir, "scripts", "relay-policy", "write-allowlist.json")
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatalf("mkdir allowlist dir: %v", err)
	}
	if err := os.WriteFile(file, []byte(initialContent), 0o644); err != nil {
		t.Fatalf("write initial allowlist: %v", err)
	}
	runGit("add", "-A")
	runGit("-c", "user.email=test@example.com", "-c", "user.name=test", "commit", "-q", "-m", "add write-allowlist.json")
	return file
}

// TestGrantAllBoards_DirtyWriteAllowlist_SkipsRegen is the ready-0df done condition: a
// write-allowlist.json with UNCOMMITTED changes (the operator mid-edit, e.g. a
// hand-relabeled key) is left byte-for-byte UNTOUCHED by `rd grant --all-boards`, and
// the grant event still publishes and resolves.
func TestGrantAllBoards_DirtyWriteAllowlist_SkipsRegen(t *testing.T) {
	projectsRoot, owner, dirByName := setupOwnedBoardsUnderRoot(t, "repoalpha")
	dir := dirByName["repoalpha"]

	file := gitInitAllowlistRepo(t, dir, "{\n  \"aaaa\": \"committed label\"\n}\n")

	// Simulate the operator's in-progress, UNCOMMITTED hand-edit (e.g. relabeling a
	// key) — deliberately NOT the shape writeAllowlistFile would emit, so any
	// overwrite is trivially detectable.
	dirtyContent := "{\n  \"aaaa\": \"workshop VM portfolio key (hand relabeled, uncommitted)\"\n}\n"
	if err := os.WriteFile(file, []byte(dirtyContent), 0o644); err != nil {
		t.Fatalf("write dirty allowlist: %v", err)
	}

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}
	grantee := gk.PubKeyHex()

	if err := runGrantAllBoards(projectsRoot, grantee, rdSync.RoleContributor, "onboard-agent", 0, ""); err != nil {
		t.Fatalf("runGrantAllBoards: %v", err)
	}

	// The dirty file must be BYTE-FOR-BYTE unchanged — no relabel, no silent overwrite.
	got, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("ReadFile allowlist: %v", err)
	}
	if !bytes.Equal(got, []byte(dirtyContent)) {
		t.Fatalf("write-allowlist.json was mutated despite uncommitted changes:\n got=%q\nwant=%q", got, dirtyContent)
	}

	// The grant event must still be durable and resolvable regardless.
	grants := readGrantEventsForTest(t, dir, grantee)
	if len(grants) != 1 {
		t.Fatalf("expected exactly 1 signed kind-39301 grant, got %d", len(grants))
	}
	if err := grants[0].Verify(); err != nil {
		t.Fatalf("published grant does not verify: %v", err)
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	levels, _ := rdSync.DeriveLevels(events, owner, "repoalpha")
	if lvl, ok := levels[grantee]; !ok || lvl != rdSync.LevelContributor {
		t.Fatalf("resolved grantee level = (%d, present=%v), want contributor (%d)", levels[grantee], ok, rdSync.LevelContributor)
	}
}

// TestGrantAllBoards_CleanWriteAllowlist_RegeneratesAsBefore is the companion proof:
// with NO uncommitted changes to write-allowlist.json, `rd grant --all-boards` keeps
// regenerating it exactly as before (grantee admitted, label carried through) — the fix
// only skips the dirty case, it does not disable the ops-convenience regen entirely.
func TestGrantAllBoards_CleanWriteAllowlist_RegeneratesAsBefore(t *testing.T) {
	projectsRoot, _, dirByName := setupOwnedBoardsUnderRoot(t, "repobeta")
	dir := dirByName["repobeta"]

	file := gitInitAllowlistRepo(t, dir, "{\n}\n")

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}
	grantee := gk.PubKeyHex()

	if err := runGrantAllBoards(projectsRoot, grantee, rdSync.RoleContributor, "onboard-agent", 0, ""); err != nil {
		t.Fatalf("runGrantAllBoards: %v", err)
	}

	allow, err := readAllowlistFile(file)
	if err != nil {
		t.Fatalf("readAllowlistFile: %v", err)
	}
	if allow[grantee] != "onboard-agent" {
		t.Fatalf("clean-file regen: allowlist[grantee] = %q, want %q (allowlist=%v)", allow[grantee], "onboard-agent", allow)
	}
}
