package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

// These tests exercise the ready-f75 shared-stash guard:
//   scripts/git-hooks/reference-transaction  (blocking half)
//   scripts/git-hooks/post-index-change      (detection half)
//   scripts/git-hooks/post-checkout          (self-installation half)
//   scripts/install-git-stash-guard.sh       (installer + self-verification)
//   scripts/wt-stash.sh                      (`git wtstash`, the replacement)
//
// THE HAZARD: refs/stash lives in the repository's COMMON .git directory —
// the one piece of state `git worktree` does NOT isolate. Two agents
// dispatched into separate worktrees who both run `git stash` push onto the
// same stack; a pop by either can silently apply and then delete the OTHER's
// entry. Observed 2026-07-29: 14+ interleaved entries from 10 branches, one
// agent's pop reverting a sibling's uncommitted files to HEAD with no error.
//
// WHAT IS AND IS NOT BLOCKABLE — measured on git 2.43 by these tests, not
// asserted from documentation. The first version of this guard claimed in its
// own user-facing text that it blocked push/pop/apply/drop/clear; that was
// false, and TestStashGuard_DocumentedCoverageMatchesMeasuredCoverage exists
// specifically to keep it from becoming false again:
//
//	git stash push   BLOCKED before any damage (ref transaction aborted;
//	                 the caller keeps their working-tree changes)
//	git stash clear  BLOCKED, every entry preserved (pure ref deletion)
//	git stash apply  NOT BLOCKABLE — opens no ref transaction at all. The
//	                 only hook git fires is post-index-change, whose exit
//	                 code git ignores. Covered by a LOUD warning instead.
//	git stash pop    NOT BLOCKABLE — applies before it drops.
//	git stash drop   NOT BLOCKABLE — rewrites the refs/stash reflog before
//	                 any transaction opens. Aborting the trailing ref
//	                 deletion does keep the last entry REACHABLE via
//	                 refs/stash instead of letting it become garbage.
//
// The guard therefore makes the shared stack UNGROWABLE, which is what
// actually severs the observed incident: agent A's work can never reach the
// shared stack, so agent B's pop can never discard it. On an EMPTY stack all
// three of pop/apply/drop fail at stash@{0} resolution with zero damage —
// that is the end state, and it is asserted below.
//
// (Aliasing `git stash` to the safe implementation does not work at all: git
// resolves its own builtins before consulting an alias of the same name, so
// `alias.stash` is silently ignored. Measured, then discarded as a design.)

// TestStashGuard_RawStashPushRefusedPreDamage is the core blocking claim,
// driven with RAW `git stash` (not the wrapper) from both the main checkout
// and a linked worktree: the push fails loudly, the caller's uncommitted
// change survives, and nothing lands on the shared stack.
func TestStashGuard_RawStashPushRefusedPreDamage(t *testing.T) {
	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	mustAddWorktree(t, base, wtA, "work/a")
	mustInstallGuard(t, scriptsDir(t), base)

	for _, tc := range []struct {
		name string
		dir  string
	}{
		{"main checkout", base},
		{"linked worktree", wtA},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mustWriteWork(t, tc.dir, "precious "+tc.name+"\n")
			out, err := gitOut(tc.dir, "stash", "push", "-m", "should be blocked")
			if err == nil {
				t.Fatalf("raw `git stash push` succeeded under the guard: %s", out)
			}
			if !strings.Contains(out, "ready-f75") {
				t.Fatalf("refusal must explain itself (ready-f75 guard message), got: %s", out)
			}
			if !strings.Contains(out, "git wtstash") {
				t.Fatalf("refusal must name the replacement command, got: %s", out)
			}
			// Fail SAFE: the change must still be in the working tree.
			if got := mustReadWork(t, tc.dir); got != "precious "+tc.name+"\n" {
				t.Fatalf("blocked push lost the working-tree change: got %q", got)
			}
			if n := stashDepth(t, tc.dir); n != 0 {
				t.Fatalf("blocked push still wrote refs/stash: %d entries", n)
			}
			mustGit(t, tc.dir, "reset", "--hard", "-q", "HEAD")
		})
	}
}

// TestStashGuard_RawStashClearRefusedEntriesPreserved: `git stash clear` is a
// single refs/stash deletion with no preceding reflog rewrite, so aborting
// the transaction genuinely preserves every entry. With the pre-guard backlog
// being owner-reserved data (ready-bef), a guard that let a stray
// `git stash clear` wipe it would be worse than no guard.
func TestStashGuard_RawStashClearRefusedEntriesPreserved(t *testing.T) {
	base := setupBaseRepo(t)
	seedPreGuardStashEntry(t, base, "sibling one\n", "sibling one")
	seedPreGuardStashEntry(t, base, "sibling two\n", "sibling two")
	seedPreGuardStashEntry(t, base, "sibling three\n", "sibling three")
	mustInstallGuard(t, scriptsDir(t), base)

	if n := stashDepth(t, base); n != 3 {
		t.Fatalf("fixture: expected 3 pre-guard entries, got %d", n)
	}
	out, err := gitOut(base, "stash", "clear")
	if err == nil {
		t.Fatalf("raw `git stash clear` succeeded under the guard: %s", out)
	}
	if !strings.Contains(out, "ready-f75") {
		t.Fatalf("clear refusal must explain itself, got: %s", out)
	}
	if n := stashDepth(t, base); n != 3 {
		t.Fatalf("`git stash clear` destroyed entries despite being refused: %d left of 3", n)
	}
}

// TestStashGuard_RawPopApplyDropOnEmptyStackFailWithoutDamage asserts the end
// state the guard drives the repository toward. Because push is blocked, the
// shared stack can only ever shrink; once it is empty, every remaining verb
// fails at stash@{0} resolution before touching anything. Each verb is
// invoked as RAW git.
func TestStashGuard_RawPopApplyDropOnEmptyStackFailWithoutDamage(t *testing.T) {
	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	mustAddWorktree(t, base, wtA, "work/a")
	mustInstallGuard(t, scriptsDir(t), base)

	for _, verb := range []string{"pop", "apply", "drop"} {
		t.Run(verb, func(t *testing.T) {
			mustWriteWork(t, wtA, "my own work\n")
			out, err := gitOut(wtA, "stash", verb)
			if err == nil {
				t.Fatalf("`git stash %s` on an empty shared stack should fail, got: %s", verb, out)
			}
			if !strings.Contains(out, "No stash entries found") {
				t.Fatalf("`git stash %s` should fail at stash@{0} resolution, got: %s", verb, out)
			}
			if got := mustReadWork(t, wtA); got != "my own work\n" {
				t.Fatalf("`git stash %s` on an empty stack touched the working tree: %q", verb, got)
			}
			mustGit(t, wtA, "reset", "--hard", "-q", "HEAD")
		})
	}
}

// TestStashGuard_RawStashApplyIsNotBlockedButWarnsLoudly pins the honest
// limit. `git stash apply` opens no ref transaction, and post-index-change —
// the only hook that fires — has its exit code ignored by git, so no hook can
// stop it. This test asserts BOTH halves of the truth: the apply is not
// prevented (so nobody reads the guard as stronger than it is), AND it is no
// longer silent, which is the property whose absence made the 2026-07-29
// incident invisible.
func TestStashGuard_RawStashApplyIsNotBlockedButWarnsLoudly(t *testing.T) {
	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	mustAddWorktree(t, base, wtA, "work/a")
	// An entry that predates the guard, exactly like the 28 live in the real
	// repo. Seeded with hooks disabled because the guard's whole point is
	// that this can no longer be done once it is installed.
	seedPreGuardStashEntry(t, wtA, "sibling agent's work\n", "sibling wip")
	mustInstallGuard(t, scriptsDir(t), base)

	out, err := gitOut(wtA, "stash", "apply")
	if err != nil {
		t.Fatalf("no git hook can block `git stash apply`; if this now fails, the guard changed shape and the docs must be re-measured: %v\n%s", err, out)
	}
	if got := mustReadWork(t, wtA); got != "sibling agent's work\n" {
		t.Fatalf("fixture: apply should have restored the sibling entry, got %q", got)
	}
	if !strings.Contains(out, "ready-f75 stash guard: WARNING") {
		t.Fatalf("unblockable apply must at least be LOUD; got no guard warning:\n%s", out)
	}
	if !strings.Contains(out, "SHARED stash stack") {
		t.Fatalf("warning must name the hazard (shared stack), got:\n%s", out)
	}
	if !strings.Contains(out, "git wtstash") {
		t.Fatalf("warning must name the replacement, got:\n%s", out)
	}
}

// TestStashGuard_RawStashDropLeavesLastEntryRecoverable pins the other honest
// limit. drop rewrites the refs/stash reflog before any transaction opens, so
// the entry is off `git stash list` before the hook runs — it is NOT blocked.
// What aborting the trailing ref deletion buys is that refs/stash keeps
// pointing at the entry, so the commit stays reachable and the work is
// recoverable rather than becoming unreferenced garbage.
func TestStashGuard_RawStashDropLeavesLastEntryRecoverable(t *testing.T) {
	base := setupBaseRepo(t)
	seedPreGuardStashEntry(t, base, "only entry\n", "only entry")
	mustInstallGuard(t, scriptsDir(t), base)

	out, err := gitOut(base, "stash", "drop")
	if err == nil {
		t.Fatalf("the trailing refs/stash deletion should still be refused, got: %s", out)
	}
	if !strings.Contains(out, "ready-f75") {
		t.Fatalf("drop refusal must explain itself, got: %s", out)
	}
	// Honest: the reflog rewrite already happened, so the entry is gone from
	// `git stash list`. Assert that rather than pretending it was blocked.
	if n := stashDepth(t, base); n != 0 {
		t.Fatalf("expected drop's reflog rewrite to have already taken effect (measured git behavior), got %d entries", n)
	}
	// ...but the commit must still be reachable, and its content restorable.
	sha := strings.TrimSpace(mustGit(t, base, "rev-parse", "--verify", "refs/stash"))
	if sha == "" {
		t.Fatalf("refs/stash was deleted — the dropped entry is now unreachable garbage")
	}
	mustGit(t, base, "reset", "--hard", "-q", "HEAD")
	mustGit(t, base, "stash", "apply", "refs/stash")
	if got := mustReadWork(t, base); got != "only entry\n" {
		t.Fatalf("dropped entry was not recoverable via refs/stash: got %q", got)
	}
}

// TestStashGuard_DocumentedCoverageMatchesMeasuredCoverage is the regression
// test for the specific failure that sent this item back: the guard's own
// user-facing text claimed it blocked "push/pop/apply/drop/clear" while
// pop/apply/drop were in fact unblocked. Promising a control you do not have
// is worse than documenting the gap, so the shipped text must state the gap.
func TestStashGuard_DocumentedCoverageMatchesMeasuredCoverage(t *testing.T) {
	root := repoRootDir(t)
	files := []string{
		filepath.Join(root, "scripts", "git-hooks", "reference-transaction"),
		filepath.Join(root, "scripts", "git-hooks", "post-index-change"),
		filepath.Join(root, "scripts", "install-git-stash-guard.sh"),
		filepath.Join(root, "scripts", "wt-stash.sh"),
	}
	// Any phrasing that presents apply/pop/drop as blocked. These are the
	// exact shapes the false claim took.
	overclaims := []string{
		"push/pop/apply/drop/clear) with",
		"blocks raw git stash (push/pop/apply/drop/clear)",
		"blocks git stash push/pop/apply/drop/clear",
		"(i.e. blocks git stash push/pop/apply/drop/clear outright)",
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		text := string(b)
		for _, bad := range overclaims {
			if strings.Contains(text, bad) {
				t.Errorf("%s claims coverage the guard does not have: %q", filepath.Base(f), bad)
			}
		}
	}

	// The blocking hook must state the gap explicitly, naming apply.
	hook, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("read hook: %v", err)
	}
	for _, want := range []string{"NOT BLOCKABLE BY ANY GIT HOOK", "git stash apply", "post-index-change"} {
		if !strings.Contains(string(hook), want) {
			t.Errorf("reference-transaction hook must document the gap; missing %q", want)
		}
	}
}

// TestStashGuard_InstallerHonorsCoreHooksPath: git reads hooks from
// core.hooksPath when it is set. An installer that writes to
// `--git-common-dir`/hooks regardless is silently inert in any such repo
// while printing success — the third disqualifying finding on this item.
func TestStashGuard_InstallerHonorsCoreHooksPath(t *testing.T) {
	base := setupBaseRepo(t)
	custom := filepath.Join(t.TempDir(), "elsewhere-hooks")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustGit(t, base, "config", "core.hooksPath", custom)

	mustInstallGuard(t, scriptsDir(t), base)

	if _, err := os.Stat(filepath.Join(custom, "reference-transaction")); err != nil {
		t.Fatalf("installer did not write the hook where git reads it (%s): %v", custom, err)
	}
	if _, err := os.Stat(filepath.Join(base, ".git", "hooks", "reference-transaction")); err == nil {
		t.Fatalf("installer wrote to .git/hooks even though core.hooksPath points elsewhere")
	}
	// And it must actually fire from there.
	mustWriteWork(t, base, "precious\n")
	out, err := gitOut(base, "stash", "push", "-m", "blocked?")
	if err == nil {
		t.Fatalf("guard installed under core.hooksPath did not fire: %s", out)
	}
	if got := mustReadWork(t, base); got != "precious\n" {
		t.Fatalf("blocked push lost the change: %q", got)
	}
}

// TestStashGuard_InstallerFailsLoudlyWhenHookNeverFires: "installed" must
// mean "measured to fire", never "file copied". The installer proves it by
// attempting a write to its own canary ref (never refs/stash) and asserting
// the refusal. Here git is pointed at a hooks directory the guard is not in,
// which is exactly the silently-inert case.
func TestStashGuard_InstallerFailsLoudlyWhenHookNeverFires(t *testing.T) {
	base := setupBaseRepo(t)
	mustInstallGuard(t, scriptsDir(t), base)

	// Now make git read hooks from somewhere the guard is not.
	empty := filepath.Join(t.TempDir(), "no-hooks-here")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	mustGit(t, base, "config", "core.hooksPath", empty)

	out, err := installGuard(scriptsDir(t), base, "--verify-only")
	if err == nil {
		t.Fatalf("installer reported success while the guard was inert:\n%s", out)
	}
	for _, want := range []string{"FAILED", "NOT active", "hooks dir git will read"} {
		if !strings.Contains(out, want) {
			t.Errorf("inert-guard failure must be loud and diagnostic; missing %q in:\n%s", want, out)
		}
	}
	// The self-check must not leave its canary behind.
	if sha, _ := gitOut(base, "rev-parse", "--verify", "-q", "refs/f75-stash-guard-canary"); strings.TrimSpace(sha) != "" {
		t.Fatalf("self-check left its canary ref behind: %s", sha)
	}
}

// TestStashGuard_InstallerRefusesToClobberForeignHook: a repo may already use
// reference-transaction for something else. Silently overwriting it would
// break that, so the installer must fail loudly and leave it alone.
func TestStashGuard_InstallerRefusesToClobberForeignHook(t *testing.T) {
	base := setupBaseRepo(t)
	hookPath := filepath.Join(base, ".git", "hooks", "reference-transaction")
	foreign := "#!/bin/sh\n# somebody else's hook\nexit 0\n"
	if err := os.WriteFile(hookPath, []byte(foreign), 0o755); err != nil {
		t.Fatalf("write foreign hook: %v", err)
	}

	out, err := installGuard(scriptsDir(t), base)
	if err == nil {
		t.Fatalf("installer overwrote a foreign reference-transaction hook:\n%s", out)
	}
	if !strings.Contains(out, "not the ready-f75 guard") && !strings.Contains(out, "Refusing to overwrite") {
		t.Errorf("clobber refusal must say what it refused, got:\n%s", out)
	}
	got, err := os.ReadFile(hookPath)
	if err != nil {
		t.Fatalf("read hook after refused install: %v", err)
	}
	if string(got) != foreign {
		t.Fatalf("foreign hook was modified despite the refusal")
	}
}

// TestStashGuard_WorktreeAddReAssertsGuard tests the WORKTREE-CREATION PATH,
// which is what clause 2 of the done condition is about: a guard somebody has
// to remember to install does not stop dispatched agents from clobbering each
// other. `git worktree add` runs the post-checkout hook inside the new
// worktree, so the guard re-installs itself there. Simulated drift (the hook
// deleted) must be repaired by the act of creating a worktree.
func TestStashGuard_WorktreeAddReAssertsGuard(t *testing.T) {
	base := setupBaseRepo(t)
	mustInstallGuard(t, scriptsDir(t), base)

	hookPath := filepath.Join(base, ".git", "hooks", "reference-transaction")
	if err := os.Remove(hookPath); err != nil {
		t.Fatalf("remove hook to simulate drift: %v", err)
	}
	// Sanity: with the hook gone the hazard is live again.
	mustWriteWork(t, base, "unguarded\n")
	if out, err := gitOut(base, "stash", "push", "-m", "unguarded"); err != nil {
		t.Fatalf("fixture: with the hook removed, raw stash push should succeed: %v\n%s", err, out)
	}
	mustGit(t, base, "-c", "core.hooksPath=/dev/null", "stash", "drop")

	// Creating a worktree must repair it.
	wtNew := filepath.Join(t.TempDir(), "wt-new")
	out := mustGit(t, base, "worktree", "add", "-b", "work/new", wtNew, "HEAD")
	if !strings.Contains(out, "ready-f75 stash guard is active") {
		t.Errorf("a freshly created worktree should be told the guard is on, got:\n%s", out)
	}
	if _, err := os.Stat(hookPath); err != nil {
		t.Fatalf("`git worktree add` did not re-assert the guard: %v", err)
	}
	// And raw git stash must now be blocked IN THE NEW WORKTREE.
	mustWriteWork(t, wtNew, "new worktree work\n")
	stashOut, err := gitOut(wtNew, "stash", "push", "-m", "should be blocked")
	if err == nil {
		t.Fatalf("raw stash push in the newly created worktree was not blocked: %s", stashOut)
	}
	if got := mustReadWork(t, wtNew); got != "new worktree work\n" {
		t.Fatalf("blocked push in new worktree lost the change: %q", got)
	}
}

// TestStashGuard_ReportsNonEmptySharedStackLoudly: the guard cannot make an
// already non-empty shared stack safe (apply/pop/drop of a pre-existing entry
// is unblockable), and emptying it is owner-reserved data deletion
// (ready-bef). It must therefore say so rather than imply full protection.
func TestStashGuard_ReportsNonEmptySharedStackLoudly(t *testing.T) {
	base := setupBaseRepo(t)
	seedPreGuardStashEntry(t, base, "backlog entry\n", "backlog")

	out, err := installGuard(scriptsDir(t), base)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	for _, want := range []string{"SHARED stash stack still holds 1", "CANNOT be", "ready-bef"} {
		if !strings.Contains(out, want) {
			t.Errorf("non-empty shared stack must be reported loudly; missing %q in:\n%s", want, out)
		}
	}

	// And with an empty stack it must NOT cry wolf.
	clean := setupBaseRepo(t)
	out, err = installGuard(scriptsDir(t), clean)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	if strings.Contains(out, "SHARED stash stack still holds") {
		t.Errorf("clean repo should not be warned about a backlog:\n%s", out)
	}
}

// TestStashGuard_SelfInstallsInThisClone closes clause 2 for the bootstrap
// case. Hooks and non-worktree config are common to a whole clone, so one
// install covers every worktree — but SOMETHING has to run it. `go test ./...`
// is the baseline every agent and CI run in this repo already execute, so the
// guard activates itself there, idempotently, and fails loudly if it cannot.
// Before this, the guard existed as committed files with nothing installing
// them: .git/hooks/reference-transaction was absent across 57 live worktrees.
func TestStashGuard_SelfInstallsInThisClone(t *testing.T) {
	root := repoRootDir(t)
	out, err := installGuard(filepath.Join(root, "scripts"), root)
	if err != nil {
		t.Fatalf("the guard must self-install in this clone: %v\n%s", err, out)
	}
	// Independently re-verify, and assert the hook landed where git reads.
	verifyOut, err := installGuard(filepath.Join(root, "scripts"), root, "--verify-only")
	if err != nil {
		t.Fatalf("guard installed but does not fire in this clone: %v\n%s", err, verifyOut)
	}
	hooksDir := strings.TrimSpace(mustGit(t, root, "rev-parse", "--git-path", "hooks"))
	if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(root, hooksDir)
	}
	for _, n := range []string{"reference-transaction", "post-index-change", "post-checkout"} {
		if _, err := os.Stat(filepath.Join(hooksDir, n)); err != nil {
			t.Errorf("hook %s missing from the directory git reads (%s): %v", n, hooksDir, err)
		}
	}
}

// TestGoTestWorkflow_ActivatesStashGuard: CI must exercise the installation
// path on a clean checkout, and fail the job if the guard does not fire, so a
// change that breaks installation cannot merge green.
func TestGoTestWorkflow_ActivatesStashGuard(t *testing.T) {
	raw := readWorkflow(t, "go-test.yml")
	wf := parseWorkflow(t, raw)
	job, ok := wf.Jobs["test"]
	if !ok {
		t.Fatalf("go-test.yml has no `test` job")
	}
	installIdx, testIdx := -1, -1
	for i, step := range job.Steps {
		if strings.Contains(step.Run, "install-git-stash-guard.sh") {
			installIdx = i
		}
		if strings.Contains(step.Run, "go test ./...") {
			testIdx = i
		}
	}
	if installIdx < 0 {
		t.Fatalf("go-test.yml `test` job never runs scripts/install-git-stash-guard.sh (ready-f75: nothing installed the guard)")
	}
	if testIdx < 0 {
		t.Fatalf("go-test.yml `test` job no longer runs `go test ./...`")
	}
	if installIdx > testIdx {
		t.Errorf("the guard activation step (%d) must run before `go test ./...` (%d) so a broken guard fails fast", installIdx, testIdx)
	}
}

// TestWtStash_WorktreesDoNotClobberEachOther proves the sequential case for
// the safe replacement: one worktree's `git wtstash` push/pop never shows up
// in, or disturbs, another worktree's stack.
func TestWtStash_WorktreesDoNotClobberEachOther(t *testing.T) {
	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	mustAddWorktree(t, base, wtA, "work/a")

	mustInstallGuard(t, scriptsDir(t), base)

	// wt-b is created AFTER the guard is installed, from the SAME base repo,
	// proving the hook + alias (set once, in shared .git state) automatically
	// cover a worktree the installer never saw — the real swarm-dispatch
	// shape, where worktrees keep being created after setup.
	wtB := filepath.Join(t.TempDir(), "wt-b")
	mustAddWorktree(t, base, wtB, "work/b")

	mustWriteWork(t, wtA, "wt-a change 1\n")
	mustGit(t, wtA, "wtstash")
	if got := mustReadWork(t, wtA); got != "baseline\n" {
		t.Fatalf("wt-a working tree not reset after wtstash push: got %q", got)
	}

	mustWriteWork(t, wtB, "wt-b change 1\n")
	mustGit(t, wtB, "wtstash")
	if got := mustReadWork(t, wtB); got != "baseline\n" {
		t.Fatalf("wt-b working tree not reset after wtstash push: got %q", got)
	}

	listA := mustGit(t, wtA, "wtstash", "list")
	if n := strings.Count(listA, "stash@{"); n != 1 {
		t.Fatalf("wt-a wtstash list should show exactly its own 1 entry, got %d: %q", n, listA)
	}
	listB := mustGit(t, wtB, "wtstash", "list")
	if n := strings.Count(listB, "stash@{"); n != 1 {
		t.Fatalf("wt-b wtstash list should show exactly its own 1 entry, got %d: %q", n, listB)
	}

	// wt-a pushes a second entry. wt-b's count must not move.
	mustWriteWork(t, wtA, "wt-a change 2\n")
	mustGit(t, wtA, "wtstash")
	if n := strings.Count(mustGit(t, wtA, "wtstash", "list"), "stash@{"); n != 2 {
		t.Fatalf("wt-a should have 2 entries after its second push, got %d", n)
	}
	if n := strings.Count(mustGit(t, wtB, "wtstash", "list"), "stash@{"); n != 1 {
		t.Fatalf("wt-b entry count changed by wt-a's push — cross-worktree leak, got %d", n)
	}

	// wt-b pops its own (only) entry: gets its own change back, and wt-a's
	// 2-entry stack is untouched.
	mustGit(t, wtB, "wtstash", "pop")
	if got := mustReadWork(t, wtB); got != "wt-b change 1\n" {
		t.Fatalf("wt-b pop restored wrong content: got %q, want %q", got, "wt-b change 1\n")
	}
	if got := strings.TrimSpace(mustGit(t, wtB, "wtstash", "list")); got != "" {
		t.Fatalf("wt-b wtstash should be empty after popping its only entry, got: %q", got)
	}
	if n := strings.Count(mustGit(t, wtA, "wtstash", "list"), "stash@{"); n != 2 {
		t.Fatalf("wt-a's stack was disturbed by wt-b's pop — cross-worktree clobber, now has %d entries", n)
	}

	// wt-a pops its own two entries in the right (LIFO) order. Between the
	// two pops the working tree is reset back to the committed baseline —
	// exactly like real `git stash`, applying a second entry on top of an
	// UNCOMMITTED, conflicting first restore correctly fails ("local changes
	// would be overwritten"); that's expected stash behavior, not something
	// this wrapper needs to (or should) work around.
	mustGit(t, wtA, "wtstash", "pop")
	if got := mustReadWork(t, wtA); got != "wt-a change 2\n" {
		t.Fatalf("wt-a first pop restored wrong content: got %q", got)
	}
	mustGit(t, wtA, "reset", "--hard", "-q", "HEAD")
	mustGit(t, wtA, "wtstash", "pop")
	if got := mustReadWork(t, wtA); got != "wt-a change 1\n" {
		t.Fatalf("wt-a second pop restored wrong content: got %q", got)
	}
	if got := strings.TrimSpace(mustGit(t, wtA, "wtstash", "list")); got != "" {
		t.Fatalf("wt-a wtstash should be empty after popping both entries, got: %q", got)
	}
	// The shared stack must have stayed empty throughout.
	if n := stashDepth(t, base); n != 0 {
		t.Fatalf("git wtstash leaked onto the SHARED refs/stash: %d entries", n)
	}
}

// TestWtStash_PushAcceptsOptionsInFirstPosition: `git stash -m msg` is valid
// git and defaults to push, so the replacement must accept the same shape.
// It did not — it reported "unsupported subcommand '-m'" and left the
// caller's changes unstashed, which is how an agent told to stop using
// `git stash` ends up with neither command working. Found by probing the
// installed guard in the live repo, not by reading the script.
func TestWtStash_PushAcceptsOptionsInFirstPosition(t *testing.T) {
	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	mustAddWorktree(t, base, wtA, "work/a")
	mustInstallGuard(t, scriptsDir(t), base)

	mustWriteWork(t, wtA, "option-form work\n")
	out := mustGit(t, wtA, "wtstash", "-m", "message via option")
	if !strings.Contains(out, "Saved worktree-scoped stash") {
		t.Fatalf("`git wtstash -m msg` should push, got: %s", out)
	}
	if got := mustReadWork(t, wtA); got != "baseline\n" {
		t.Fatalf("working tree not reset after push: %q", got)
	}
	list := mustGit(t, wtA, "wtstash", "list")
	if !strings.Contains(list, "message via option") {
		t.Fatalf("message not carried onto the entry: %q", list)
	}
	mustGit(t, wtA, "wtstash", "pop")
	if got := mustReadWork(t, wtA); got != "option-form work\n" {
		t.Fatalf("pop did not restore the work: %q", got)
	}
	// A genuinely unknown subcommand must still be rejected, not silently
	// swallowed as a push.
	if out, err := gitOut(wtA, "wtstash", "bogus"); err == nil {
		t.Fatalf("unknown subcommand should fail, got: %s", out)
	}
}

// TestWtStash_ConcurrentPushPopAcrossWorktreesStaysIsolated is the DONE
// condition's proof: it actually runs two worktrees stashing AT THE SAME
// TIME (real OS processes, released off a shared start barrier, not just
// sequential calls) and checks that every push/pop round in each worktree
// only ever sees and restores ITS OWN content, never the other's.
func TestWtStash_ConcurrentPushPopAcrossWorktreesStaysIsolated(t *testing.T) {
	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	wtB := filepath.Join(t.TempDir(), "wt-b")
	mustAddWorktree(t, base, wtA, "work/a")
	mustAddWorktree(t, base, wtB, "work/b")
	mustInstallGuard(t, scriptsDir(t), base)

	const rounds = 12
	var wg sync.WaitGroup
	start := make(chan struct{})
	errs := make(chan string, rounds*2*3)

	worker := func(dir, label string) {
		defer wg.Done()
		<-start
		for i := 0; i < rounds; i++ {
			marker := fmt.Sprintf("%s-round-%d\n", label, i)
			if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte(marker), 0o644); err != nil {
				errs <- fmt.Sprintf("%s round %d: write work.txt: %v", label, i, err)
				return
			}
			if out, err := gitOut(dir, "wtstash"); err != nil {
				errs <- fmt.Sprintf("%s round %d: wtstash push failed: %v (%s)", label, i, err, out)
				continue
			}
			if got, err := os.ReadFile(filepath.Join(dir, "work.txt")); err != nil || string(got) != "baseline\n" {
				errs <- fmt.Sprintf("%s round %d: working tree not reset after push, got %q (err=%v)", label, i, string(got), err)
			}
			if out, err := gitOut(dir, "wtstash", "pop"); err != nil {
				errs <- fmt.Sprintf("%s round %d: wtstash pop failed: %v (%s)", label, i, err, out)
				continue
			}
			got, err := os.ReadFile(filepath.Join(dir, "work.txt"))
			if err != nil {
				errs <- fmt.Sprintf("%s round %d: read after pop: %v", label, i, err)
				continue
			}
			if string(got) != marker {
				errs <- fmt.Sprintf("%s round %d: pop restored %q, want its own %q — cross-worktree clobber", label, i, string(got), marker)
			}
		}
	}

	wg.Add(2)
	go worker(wtA, "wt-a")
	go worker(wtB, "wt-b")
	close(start) // release both workers together so their pushes/pops overlap
	wg.Wait()
	close(errs)

	for e := range errs {
		t.Error(e)
	}
}

// --- helpers -----------------------------------------------------------

func scriptsDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(repoRootDir(t), "scripts")
}

func gitOut(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := gitOut(dir, args...)
	if err != nil {
		t.Fatalf("git %v (dir=%s) failed: %v\n%s", args, dir, err, out)
	}
	return out
}

func setupBaseRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "wt-stash-test@example.com")
	mustGit(t, dir, "config", "user.name", "wt-stash-test")
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte("baseline\n"), 0o644); err != nil {
		t.Fatalf("write work.txt: %v", err)
	}
	mustGit(t, dir, "add", "work.txt")
	mustGit(t, dir, "commit", "-q", "-m", "init")
	return dir
}

func mustAddWorktree(t *testing.T, base, worktreeDir, branch string) {
	t.Helper()
	mustGit(t, base, "worktree", "add", "-q", "-b", branch, worktreeDir, "HEAD")
}

// installGuard runs the real installer from runFromDir and returns its
// combined output. No mock: the thing under test is the installer.
func installGuard(scriptsPath, runFromDir string, args ...string) (string, error) {
	cmd := exec.Command(filepath.Join(scriptsPath, "install-git-stash-guard.sh"), args...)
	cmd.Dir = runFromDir
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustInstallGuard(t *testing.T, scriptsPath, runFromDir string) {
	t.Helper()
	out, err := installGuard(scriptsPath, runFromDir)
	if err != nil {
		t.Fatalf("install-git-stash-guard.sh (dir=%s) failed: %v\n%s", runFromDir, err, out)
	}
}

// seedPreGuardStashEntry puts an entry on the SHARED refs/stash the way the 28
// live entries in the real repo got there: before the guard existed. Hooks are
// disabled for this one call precisely because the guard's job is to make this
// impossible afterwards — the tests that matter then run RAW git with the
// guard fully active.
func seedPreGuardStashEntry(t *testing.T, dir, content, msg string) {
	t.Helper()
	mustWriteWork(t, dir, content)
	mustGit(t, dir, "-c", "core.hooksPath=/dev/null", "stash", "push", "-q", "-m", msg)
}

func stashDepth(t *testing.T, dir string) int {
	t.Helper()
	out, err := gitOut(dir, "stash", "list")
	if err != nil {
		t.Fatalf("git stash list (dir=%s) failed: %v\n%s", dir, err, out)
	}
	return strings.Count(out, "stash@{")
}

func mustWriteWork(t *testing.T, dir, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, "work.txt"), []byte(content), 0o644); err != nil {
		t.Fatalf("write work.txt in %s: %v", dir, err)
	}
}

func mustReadWork(t *testing.T, dir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, "work.txt"))
	if err != nil {
		t.Fatalf("read work.txt in %s: %v", dir, err)
	}
	return string(b)
}
