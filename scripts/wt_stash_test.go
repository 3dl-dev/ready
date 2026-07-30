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
// WHERE THE SCOPE OF THIS GUARD IS WRITTEN DOWN: here, in the tests, and
// nowhere else. Three review rounds were lost to a coverage claim in prose —
// first a false one, then a blacklist of its phrasings, then a whitelist of
// claim words, each defeated by rewording. The claim has been DELETED from
// every shipped file, and TestStashGuard_ShippedFilesCarryNoCoverageClaim
// keeps it deleted. An absent claim cannot be an overclaim.
//
// The two tests below are the statement, and each is falsifiable because the
// expectation is hard-coded here and the behaviour is measured by running raw
// git against a really-installed mechanism:
//
//   - TestStashGuard_HooksBlockPushSaveClearOnlyAtRealisticDepth — the git
//     hooks, with the PATH shim absent.
//   - TestStashGuard_PathShimBlocksEveryMutatingVerbAtAnyDepth — the PATH
//     shim, with the hooks absent.
//
// ISOLATION IS THE POINT OF THAT "absent". An earlier round measured the shim
// in a repo where the hooks were also installed, so the shim's rows were
// satisfied by the hook and a fail-open shim still passed. Each mechanism is
// now exercised alone: mechHooks installs the hooks and leaves PATH clean,
// mechShim puts the shim on PATH and installs NO hooks at all.
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

// TestStashGuard_ApplyBySHAStillWarns_ButWtStashStaysSilent pins the exemption
// boundary for the detection hook. `git wtstash` must not warn about its own
// internal `git stash apply <sha>` — it is the safe, per-worktree path — but a
// hand-typed apply of a shared entry BY SHA must still be loud. An earlier
// version exempted any command line containing a 40-hex object id, which
// silenced exactly the hazard the hook exists to surface. The only exemption
// is now the RD_STASH_GUARD_INTERNAL env var that wt-stash.sh exports.
func TestStashGuard_ApplyBySHAStillWarns_ButWtStashStaysSilent(t *testing.T) {
	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	mustAddWorktree(t, base, wtA, "work/a")
	seedPreGuardStashEntry(t, wtA, "sibling agent's work\n", "sibling wip")
	mustInstallGuard(t, scriptsDir(t), base)

	sha := strings.TrimSpace(mustGit(t, wtA, "rev-parse", "refs/stash"))
	out, err := gitOut(wtA, "stash", "apply", sha)
	if err != nil {
		t.Fatalf("apply by sha should succeed (unblockable): %v\n%s", err, out)
	}
	if !strings.Contains(out, "ready-f75 stash guard: WARNING") {
		t.Fatalf("a hand-typed `git stash apply <sha>` of a shared entry must still warn, got:\n%s", out)
	}

	// The safe path must stay silent through a full push/pop round trip.
	mustGit(t, wtA, "reset", "--hard", "-q", "HEAD")
	mustWriteWork(t, wtA, "my own work\n")
	pushOut := mustGit(t, wtA, "wtstash")
	popOut := mustGit(t, wtA, "wtstash", "pop")
	if strings.Contains(pushOut+popOut, "ready-f75 stash guard: WARNING") {
		t.Fatalf("git wtstash must not warn about its own internals:\npush: %s\npop: %s", pushOut, popOut)
	}
	if got := mustReadWork(t, wtA); got != "my own work\n" {
		t.Fatalf("wtstash round trip lost the work: %q", got)
	}
}

// TestStashGuard_RawStashPopIsNotBlockedButWarnsLoudly covers the exact verb
// from the 2026-07-29 incident, raw. pop applies BEFORE it drops, so no hook
// runs before the working tree changes and it cannot be prevented. What must
// be true: it is LOUD, and the entry it dropped is still reachable (the
// trailing refs/stash deletion is refused) so the work is recoverable.
func TestStashGuard_RawStashPopIsNotBlockedButWarnsLoudly(t *testing.T) {
	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	mustAddWorktree(t, base, wtA, "work/a")
	seedPreGuardStashEntry(t, wtA, "sibling agent's work\n", "sibling wip")
	mustInstallGuard(t, scriptsDir(t), base)

	out, _ := gitOut(wtA, "stash", "pop")
	// Exit status is deliberately not asserted: pop's apply succeeds and its
	// trailing ref deletion is refused, so the status reflects the refusal
	// while the apply already landed. That split is the honest behavior.
	if got := mustReadWork(t, wtA); got != "sibling agent's work\n" {
		t.Fatalf("no git hook can stop pop's apply; expected the sibling content to land, got %q", got)
	}
	if !strings.Contains(out, "ready-f75 stash guard") {
		t.Fatalf("raw `git stash pop` must be LOUD, got:\n%s", out)
	}
	if !strings.Contains(out, "WARNING") {
		t.Fatalf("pop must produce the shared-stack warning, got:\n%s", out)
	}
	// The dropped entry must still be reachable rather than garbage.
	sha := strings.TrimSpace(mustGit(t, wtA, "rev-parse", "--verify", "refs/stash"))
	if sha == "" {
		t.Fatalf("refs/stash deleted by pop — the entry is now unreachable garbage")
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

// TestStashGuard_HooksBlockPushSaveClearOnlyAtRealisticDepth is one half of
// the guard's entire statement of scope, and it is deliberately a HARD-CODED
// expectation checked against a live measurement rather than a claim derived
// from one. Round 3 derived the expected verb list from the measurement, so
// the assertion could not fail for the reason it existed; the expectation
// below is written out, so changing the guard's behaviour breaks this test.
//
// The hooks are exercised ALONE: installed, with the PATH shim nowhere on
// PATH. Depth 3 is used because the live clone runs at depth 26+ and the
// hooks' effect on pop/drop is depth-dependent — see
// TestStashGuard_HooksHaveNoRefLevelEffectAtRealisticDepth.
func TestStashGuard_HooksBlockPushSaveClearOnlyAtRealisticDepth(t *testing.T) {
	root := repoRootDir(t)

	// blocked pre-damage == non-zero exit AND unchanged shared stack AND an
	// uncontaminated working tree AND the caller's own work still present.
	want := map[string]bool{
		"push":  true,
		"save":  true,
		"clear": true,
		"apply": false,
		"pop":   false,
		"drop":  false,
	}
	for _, verb := range stashVerbs {
		res := rawVerbRun(t, root, mechHooks, 3, verb)
		got := blockedPreDamage(res)
		if got != want[verb] {
			t.Errorf("git-hooks alone, depth 3: `git stash %s` blocked-pre-damage=%v, want %v\nexit-ok=%v depth %d->%d shared=%q mine=%q\n%s",
				verb, got, want[verb], res.exitOK, res.depthBefore, res.depthAfter, res.sharedAfter, res.mineAfter, res.output)
		}
	}
}

// TestStashGuard_ShippedFilesCarryNoCoverageClaim keeps the deletion done.
// Three rounds of this item shipped a per-verb coverage claim in prose — a
// false one, then a blacklist of its wordings, then a whitelist of claim
// words — and each was defeated by rephrasing. Testing prose does not work.
// The claim is gone; the tests above are the only statement of scope. This
// asserts the machine-readable table that carried it has not come back.
//
// It is an exact check on a marker this repo controls, not a natural-language
// scanner, and it is not claimed to catch a hand-written English overclaim.
// Nothing can; that is why the claim was removed instead of policed.
func TestStashGuard_ShippedFilesCarryNoCoverageClaim(t *testing.T) {
	root := repoRootDir(t)
	files := shippedGuardFiles(root)
	if len(files) == 0 {
		t.Fatalf("no shipped guard files to check — this test has stopped testing anything")
	}
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		if strings.Contains(string(b), "ready-f75 coverage table") {
			t.Errorf("%s has re-grown a coverage table. Scope belongs in scripts/wt_stash_test.go, "+
				"where it is executable; a table in prose drifts and has already been defeated three times.",
				filepath.Base(f))
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
	// "installed", not "active": the notice says what happened (hooks were
	// written) and points at the tests for what that is worth. It used to say
	// "active", which reads as a safety guarantee it cannot make.
	if !strings.Contains(out, "ready-f75 stash guard is installed in this repo") {
		t.Errorf("a freshly created worktree should be told the guard was installed, got:\n%s", out)
	}
	if !strings.Contains(out, "go test ./scripts/ -run TestStashGuard") {
		t.Errorf("the new-worktree notice must point at the measurement rather than assert safety, got:\n%s", out)
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

// TestStashGuard_ReportsNonEmptySharedStackLoudly: a non-empty shared stack is
// the standing hazard, and emptying it is owner-reserved data deletion
// (ready-bef), so the installer must report it rather than let "guard
// installed" read as "you are safe".
//
// The assertions are on FACTS and POINTERS — the measured depth, the safe
// replacement, the activation line, the item that owns the deletion, and an
// explicit denial that installation means safety. They are deliberately NOT on
// a per-verb statement of what is stopped: the installer no longer makes one,
// because three rounds of this item proved a maintained claim drifts. Scope is
// asserted by the two measurement tests instead.
func TestStashGuard_ReportsNonEmptySharedStackLoudly(t *testing.T) {
	base := setupBaseRepo(t)
	seedPreGuardStashEntry(t, base, "backlog entry\n", "backlog")

	out, err := installGuard(scriptsDir(t), base)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"SHARED stash stack still holds 1",
		"does NOT make raw 'git stash' safe",
		"go test ./scripts/ -run TestStashGuard",
		"git wtstash",
		"ready-bef",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("non-empty shared stack must be reported loudly; missing %q in:\n%s", want, out)
		}
	}

	// Two entries: the depth must be reported as measured, not rounded to
	// "some", and the activation line for the stronger mechanism offered.
	deep := setupBaseRepo(t)
	seedPreGuardStashEntry(t, deep, "backlog one\n", "one")
	seedPreGuardStashEntry(t, deep, "backlog two\n", "two")
	out, err = installGuard(scriptsDir(t), deep)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"SHARED stash stack still holds 2",
		"The PATH shim is NOT active",
		"export PATH=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("at depth 2 the report must state the depth and the activation line; missing %q in:\n%s", want, out)
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

// TestStashGuard_HooksHaveNoRefLevelEffectAtRealisticDepth pins the limit the
// round-2 review found, as a measurement rather than a footnote. The guard was
// only ever demonstrated on a single-entry stack, which is the one depth where
// the trailing refs/stash deletion opens a transaction a hook can abort. The
// live repo runs at depth 27. If this test ever starts failing because pop is
// refused at depth 3, that is good news and the coverage table must be
// rewritten to match.
func TestStashGuard_HooksHaveNoRefLevelEffectAtRealisticDepth(t *testing.T) {
	root := repoRootDir(t)

	atOne := rawVerbRun(t, root, mechHooks, 1, "pop")
	if atOne.exitOK {
		t.Errorf("depth 1: expected the trailing refs/stash deletion to be refused, got exit 0:\n%s", atOne.output)
	}
	if atOne.depthAfter != 0 {
		t.Errorf("depth 1: the reflog rewrite happens before any hook, so the entry should already be gone; depth=%d", atOne.depthAfter)
	}

	for _, depth := range []int{2, 3, 5} {
		res := rawVerbRun(t, root, mechHooks, depth, "pop")
		if !res.exitOK {
			t.Errorf("depth %d: expected raw `git stash pop` to succeed under the hooks (that is the limit being documented), got:\n%s", depth, res.output)
		}
		if res.depthAfter != depth-1 {
			t.Errorf("depth %d: expected the entry to be consumed (depth %d), got %d", depth, depth-1, res.depthAfter)
		}
		if res.sharedAfter != siblingContent(depth) {
			t.Errorf("depth %d: expected the sibling's stashed content in the caller's tree, got %q", depth, res.sharedAfter)
		}
		// The warning is the ONLY thing left at this depth, so it had better
		// be there.
		if !strings.Contains(res.output, "ready-f75") {
			t.Errorf("depth %d: pop is unblocked here, so the loud warning is the whole of the coverage — and it did not fire:\n%s", depth, res.output)
		}
	}

	// drop at depth >= 2 is the fully silent case. Assert the silence rather
	// than letting somebody discover it.
	res := rawVerbRun(t, root, mechHooks, 3, "drop")
	if !res.exitOK {
		t.Errorf("depth 3: expected raw `git stash drop` to succeed under the hooks, got:\n%s", res.output)
	}
	if strings.Contains(res.output, "ready-f75") {
		t.Errorf("depth 3 drop unexpectedly produced guard output; the documented claim that it is silent is now wrong:\n%s", res.output)
	}
}

// TestStashGuard_PathShimBlocksEveryMutatingVerbAtAnyDepth is the other half
// of the guard's statement of scope, and the answer to the hooks' depth limit.
// The done condition is that dispatched agents cannot clobber each other, and
// no git hook can deliver it, so the mechanism has to sit outside git: a `git`
// wrapper earlier on the agent's PATH. It refuses before git runs at all, so
// stack depth is irrelevant.
//
// THE SHIM IS ALONE HERE. rawVerbRun with mechShim installs no hooks at all
// (assertNoGuardHooks enforces it), because an earlier round measured the shim
// in a repo where the hooks were also installed: the hook refused push, save
// and clear, so those rows passed even with the shim's own fail-open put back.
// With the hooks gone, every assertion below can only be satisfied by the shim.
func TestStashGuard_PathShimBlocksEveryMutatingVerbAtAnyDepth(t *testing.T) {
	root := repoRootDir(t)
	for _, depth := range []int{1, 3, 27} {
		for _, verb := range stashVerbs {
			res := rawVerbRun(t, root, mechShim, depth, verb)
			if !blockedPreDamage(res) {
				t.Errorf("depth %d: `git stash %s` was NOT blocked pre-damage by the PATH shim alone\nexit-ok=%v depth %d->%d shared=%q mine=%q\n%s",
					depth, verb, res.exitOK, res.depthBefore, res.depthAfter, res.sharedAfter, res.mineAfter, res.output)
			}
			// Only the shim prints this: it names the mechanism and the verb it
			// resolved. A hook refusal, were one ever to creep back into the
			// fixture, says "REFUSED a write to refs/stash" instead.
			want := "ready-f75 stash guard: REFUSED `git stash " + verb + "` (PATH shim"
			if !strings.Contains(res.output, want) {
				t.Errorf("depth %d: refusal did not come from the shim naming `%s`; wanted %q in:\n%s", depth, verb, want, res.output)
			}
			if !strings.Contains(res.output, "git wtstash") {
				t.Errorf("depth %d: `git stash %s` refusal must name the replacement:\n%s", depth, verb, res.output)
			}
		}
	}
}

// TestStashGuard_PathShimPassesEverythingElseThrough: a wrapper on PATH that
// broke ordinary git would be removed within the hour, and a wrapper that
// blocked read-only inspection would push agents to work around it. Both
// failure modes are worse than the hazard.
//
// mechBoth deliberately: this is the coexistence case (the `git wtstash` alias
// comes from the installer), and none of its assertions could be satisfied by
// a hook — at depth 2 the hooks let pop through at exit 0, and they say
// nothing at all about `git stash list`.
func TestStashGuard_PathShimPassesEverythingElseThrough(t *testing.T) {
	root := repoRootDir(t)
	repo := setupSharedStackRepo(t, root, 2, mechBoth)
	env := envWithPath(filepath.Join(root, "scripts", "git-shim"))

	// The branch name is whatever `git init` chose, so take it from git
	// itself rather than guessing master vs main.
	branch := strings.TrimSpace(mustGit(t, repo, "rev-parse", "--abbrev-ref", "HEAD"))

	for _, tc := range []struct {
		name string
		cmd  string
		want string
	}{
		{"stash list", "git stash list", "stash@{1}"},
		{"stash show", "git stash show", "shared.txt"},
		{"rev-parse", "git rev-parse --abbrev-ref HEAD", branch},
		{"status", "git status --short", "mine.txt"},
		{"log", "git log --oneline -1", "init"},
	} {
		out, err := bashOut(repo, env, tc.cmd)
		if err != nil {
			t.Errorf("%s: shim broke an ordinary command (%q): %v\n%s", tc.name, tc.cmd, err, out)
			continue
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("%s: %q produced unexpected output, wanted %q in:\n%s", tc.name, tc.cmd, tc.want, out)
		}
	}

	// Global options must not be mistaken for the subcommand: `git -C <dir>
	// stash pop` has to be caught too.
	out, err := bashOut(t.TempDir(), env, "git -C "+repo+" stash pop")
	if err == nil {
		t.Errorf("`git -C <dir> stash pop` slipped past the shim:\n%s", out)
	}
	if n := stashDepth(t, repo); n != 2 {
		t.Errorf("`git -C <dir> stash pop` consumed an entry: depth %d", n)
	}

	// wt-stash.sh runs `git stash create` / `git stash apply <sha>` against
	// its OWN per-worktree refs and must keep working with the shim on PATH.
	mustWriteFile(t, repo, "mine.txt", "wtstash work\n")
	if out, err := bashOut(repo, env, "git wtstash push -m shim-check"); err != nil {
		t.Fatalf("git wtstash push broke with the shim on PATH: %v\n%s", err, out)
	}
	if out, err := bashOut(repo, env, "git wtstash pop"); err != nil {
		t.Fatalf("git wtstash pop broke with the shim on PATH: %v\n%s", err, out)
	}
	if got := mustReadFile(t, repo, "mine.txt"); got != "wtstash work\n" {
		t.Fatalf("git wtstash round-trip lost the change under the shim: %q", got)
	}
	if n := stashDepth(t, repo); n != 2 {
		t.Fatalf("git wtstash touched the SHARED stack: depth %d", n)
	}
}

// TestStashGuard_PathShimReadsTheVerbTheWayGitDoes covers the fail-open hole
// found by probing the shim instead of reading it. An earlier version took the
// first NON-OPTION argument after `stash` as the verb, so `git stash -m list`
// — which git executes as a push with the message "list" — was read as a
// harmless `list` and let straight onto the shared stack. git's actual rule is
// that only the token immediately after `stash` is the subcommand, so anything
// else is the implicit-push form.
//
// THE PREVIOUS VERSION OF THIS TEST COULD NOT DETECT THAT DEFECT. It ran under
// an installed guard and accepted any refusal containing "ready-f75", so with
// the broken parse `git stash -m list` fell through to real git, the hook
// refused the refs/stash write, and every row still passed: exit non-zero,
// message matched, depth 2, mine.txt "precious". Two changes fix that, and
// either alone is sufficient:
//
//  1. mechShim — no hooks exist, so a mis-parsed verb reaches real git and
//     LANDS on the shared stack (depth 3, mine.txt reverted).
//  2. the expected verb is asserted against the shim's own refusal line, which
//     names the verb it resolved. Only the corrected parse says "push" for
//     `git stash -m list`.
func TestStashGuard_PathShimReadsTheVerbTheWayGitDoes(t *testing.T) {
	root := repoRootDir(t)
	env := envForMechanism(root, mechShim)

	for _, tc := range []struct {
		cmd string
		// verb is the subcommand the shim must resolve, or "" if the command
		// is read-only and must pass through untouched.
		verb string
	}{
		{"git stash", "push"},                  // implicit push
		{"git stash -m list", "push"},          // push whose MESSAGE is "list"
		{"git stash -m show", "push"},          // push whose MESSAGE is "show"
		{"git stash --keep-index", "push"},     // push with an option first
		{"git stash -u", "push"},               // push with untracked files
		{"git stash list", ""},                 // genuinely read-only
		{"git stash show", ""},                 // genuinely read-only
		{"git stash apply stash@{0}", "apply"}, // verb with an argument
		{"git stash push -m list", "push"},     // explicit push
	} {
		repo := setupSharedStackRepo(t, root, 2, mechShim)
		out, err := bashOut(repo, env, tc.cmd)

		if tc.verb == "" {
			if err != nil {
				t.Errorf("%q: read-only command was refused:\n%s", tc.cmd, out)
			}
			continue
		}

		if err == nil {
			t.Errorf("%q: allowed through the shim:\n%s", tc.cmd, out)
		}
		// The load-bearing assertion: the shim must name the verb it resolved.
		// With the pre-fix parse `git stash -m list` resolves to "list" and is
		// not refused at all, so this line cannot be produced.
		want := "ready-f75 stash guard: REFUSED `git stash " + tc.verb + "` (PATH shim"
		if !strings.Contains(out, want) {
			t.Errorf("%q: shim resolved the wrong subcommand; wanted %q in:\n%s", tc.cmd, want, out)
		}
		// And with no hooks present, a mis-parse is not merely mislabelled —
		// it reaches the shared stack.
		if n := stashDepth(t, repo); n != 2 {
			t.Errorf("%q: reached the shared stack (depth %d, want 2)", tc.cmd, n)
		}
		if got := mustReadFile(t, repo, "mine.txt"); got != "precious\n" {
			t.Errorf("%q: lost the caller's uncommitted work: %q", tc.cmd, got)
		}
	}
}

// TestStashGuard_TwoShimCopiesOnPathDoNotRecurse: the installer materialises a
// SECOND copy of the shim inside .git, so an operator who puts both that
// directory and scripts/git-shim on PATH has two wrappers that would each
// resolve to the other. A shim that fork-bombs the moment somebody is
// thorough is worse than no shim, so each copy skips any candidate carrying
// the guard's marker.
func TestStashGuard_TwoShimCopiesOnPathDoNotRecurse(t *testing.T) {
	root := repoRootDir(t)
	// mechBoth: the second shim copy only exists because the installer put it
	// there, so this case needs the installed guard by construction.
	repo := setupSharedStackRepo(t, root, 2, mechBoth)
	common := strings.TrimSpace(mustGit(t, repo, "rev-parse", "--git-common-dir"))
	if !filepath.IsAbs(common) {
		common = filepath.Join(repo, common)
	}
	installed := filepath.Join(common, "f75-stash-guard", "bin")

	env := envWithPath(filepath.Join(root, "scripts", "git-shim") + string(os.PathListSeparator) + installed)

	// A refused verb must still refuse, in bounded time, from either copy.
	out, err := bashOut(repo, env, "timeout 20 git stash pop")
	if err == nil {
		t.Fatalf("two shims on PATH: `git stash pop` was allowed through:\n%s", out)
	}
	// Specifically from a shim, not from the hooks that mechBoth also installed:
	// at depth 2 the hooks let pop through at exit 0 anyway, but assert the
	// source rather than relying on that.
	if !strings.Contains(out, "(PATH shim") {
		t.Fatalf("two shims on PATH: refusal did not come from a shim:\n%s", out)
	}

	// And an ordinary command must reach the real git rather than bouncing
	// between the copies until the timeout kills it.
	out, err = bashOut(repo, env, "timeout 20 git rev-parse --git-dir")
	if err != nil {
		t.Fatalf("two shims on PATH: ordinary git never reached the real binary (%v):\n%s", err, out)
	}
	if strings.Contains(out, "ready-f75") {
		t.Fatalf("two shims on PATH: ordinary git was intercepted:\n%s", out)
	}
}

// TestStashGuard_InstallerMaterialisesShimAndDoesNotClaimItIsActive: the shim
// only works if something puts it on PATH, and that something is the process
// that spawns an agent — not this repo. An installer that printed "guard
// active" without distinguishing the two mechanisms would recreate exactly the
// overclaim this item keeps failing on.
func TestStashGuard_InstallerMaterialisesShimAndDoesNotClaimItIsActive(t *testing.T) {
	root := repoRootDir(t)
	base := setupBaseRepo(t)
	out, err := installGuard(filepath.Join(root, "scripts"), base)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}

	common := strings.TrimSpace(mustGit(t, base, "rev-parse", "--git-common-dir"))
	if !filepath.IsAbs(common) {
		common = filepath.Join(base, common)
	}
	shim := filepath.Join(common, "f75-stash-guard", "bin", "git")
	info, err := os.Stat(shim)
	if err != nil {
		t.Fatalf("installer did not materialise the PATH shim at %s: %v", shim, err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("materialised shim is not executable: %v", info.Mode())
	}
	src, err := os.ReadFile(filepath.Join(root, "scripts", "git-shim", "git"))
	if err != nil {
		t.Fatalf("read shim source: %v", err)
	}
	got, err := os.ReadFile(shim)
	if err != nil {
		t.Fatalf("read installed shim: %v", err)
	}
	if string(got) != string(src) {
		t.Fatalf("installed shim differs from scripts/git-shim/git")
	}

	// It must say the shim is NOT on PATH, and print the line that fixes it.
	if !strings.Contains(out, "Currently on PATH: no") {
		t.Errorf("installer must report that the PATH shim is inactive, got:\n%s", out)
	}
	if !strings.Contains(out, "export PATH=\""+filepath.Join(common, "f75-stash-guard", "bin")) {
		t.Errorf("installer must print the resolved activation line, got:\n%s", out)
	}

	// And with the shim genuinely on PATH it must say so rather than nagging.
	env := envWithPath(filepath.Join(common, "f75-stash-guard", "bin"))
	onPath, err := bashOut(base, env, filepath.Join(root, "scripts", "install-git-stash-guard.sh"))
	if err != nil {
		t.Fatalf("install with shim on PATH failed: %v\n%s", err, onPath)
	}
	if !strings.Contains(onPath, "Currently on PATH: yes") {
		t.Errorf("installer did not notice the shim was on PATH:\n%s", onPath)
	}
}

// --- helpers -----------------------------------------------------------

// The mechanisms, which are also the isolation modes. See
// setupSharedStackRepo: mechHooks and mechShim each exclude the other, so a
// coverage assertion for one can never be satisfied by the other.
const (
	mechHooks = "git-hooks"
	mechShim  = "path-shim"
	mechBoth  = "both"
)

// stashVerbs is every mutating `git stash` verb. Each one is run for real;
// nothing is assumed. `list` and `show` are excluded because they mutate
// nothing (and the shim is asserted to pass them through).
var stashVerbs = []string{"push", "save", "apply", "pop", "drop", "clear"}

type verbRun struct {
	output      string
	exitOK      bool
	depthBefore int
	depthAfter  int
	sharedAfter string
	mineAfter   string
}

// siblingContent is what the top entry of a depth-n seeded stack holds.
func siblingContent(depth int) string { return fmt.Sprintf("sibling %d\n", depth) }

// setupSharedStackRepo builds the incident's shape: a repo whose SHARED stash
// stack already holds `depth` entries from a sibling worktree (all touching
// shared.txt), with the local agent's own uncommitted work sitting in
// mine.txt — exactly as the guard meets a repo that already had a backlog.
//
// `mechanism` decides WHICH guard is present, and it is load-bearing:
//
//	mechHooks — install the hooks. No shim anywhere.
//	mechShim  — install NOTHING. The only thing standing between the caller
//	            and the shared stack is scripts/git-shim/git on PATH.
//	mechBoth  — install the hooks (for the wtstash alias and the .git-resident
//	            shim copy) AND put the shim on PATH; for tests about how the
//	            two coexist, never for measuring either one's coverage.
//
// Measuring the shim in a repo that also had the hooks installed is what let a
// fail-open shim pass review: the hook refused push/save/clear and the shim's
// rows were satisfied by it.
func setupSharedStackRepo(t *testing.T, root string, depth int, mechanism string) string {
	t.Helper()
	dir := t.TempDir()
	mustGit(t, dir, "init", "-q")
	mustGit(t, dir, "config", "user.email", "wt-stash-test@example.com")
	mustGit(t, dir, "config", "user.name", "wt-stash-test")
	mustWriteFile(t, dir, "shared.txt", "shared-base\n")
	mustWriteFile(t, dir, "mine.txt", "mine-base\n")
	mustGit(t, dir, "add", ".")
	mustGit(t, dir, "commit", "-q", "-m", "init")

	for i := 1; i <= depth; i++ {
		mustWriteFile(t, dir, "shared.txt", siblingContent(i))
		// Hooks off for the seeding only: this is how the live backlog got
		// there, before the guard existed. Everything measured afterwards
		// runs raw git with the guard fully active.
		mustGit(t, dir, "-c", "core.hooksPath=/dev/null", "stash", "push", "-q", "-m", fmt.Sprintf("sibling entry %d", i))
	}
	if mechanism != mechShim {
		mustInstallGuard(t, filepath.Join(root, "scripts"), dir)
	} else {
		assertNoGuardHooks(t, dir)
	}
	mustWriteFile(t, dir, "mine.txt", "precious\n")
	return dir
}

// assertNoGuardHooks is the isolation check itself. A shim-only fixture that
// quietly grew a hook — from a stray installer run, a template hooks dir, a
// core.hooksPath inherited from the user's config — would silently put the
// old, unisolated measurement back. Fail rather than measure the wrong thing.
func assertNoGuardHooks(t *testing.T, dir string) {
	t.Helper()
	hooksDir := strings.TrimSpace(mustGit(t, dir, "rev-parse", "--git-path", "hooks"))
	if !filepath.IsAbs(hooksDir) {
		hooksDir = filepath.Join(dir, hooksDir)
	}
	for _, n := range []string{"reference-transaction", "post-index-change", "post-checkout"} {
		p := filepath.Join(hooksDir, n)
		b, err := os.ReadFile(p)
		if err != nil {
			continue // absent is what we want
		}
		if strings.Contains(string(b), "ready-f75 stash guard") {
			t.Fatalf("shim-only fixture is contaminated: %s carries the guard, so a hook could satisfy assertions meant for the shim", p)
		}
	}
}

// rawVerbRun runs ONE raw `git stash <verb>` the way an agent would type it —
// through bash, resolving git off PATH — against a freshly built shared stack
// of the given depth, and reports what actually happened.
func rawVerbRun(t *testing.T, root, mechanism string, depth int, verb string) verbRun {
	t.Helper()
	dir := setupSharedStackRepo(t, root, depth, mechanism)
	env := envForMechanism(root, mechanism)

	before := stashDepth(t, dir)
	out, err := bashOut(dir, env, "git stash "+verb)
	return verbRun{
		output:      out,
		exitOK:      err == nil,
		depthBefore: before,
		depthAfter:  stashDepth(t, dir),
		sharedAfter: mustReadFile(t, dir, "shared.txt"),
		mineAfter:   mustReadFile(t, dir, "mine.txt"),
	}
}

// envForMechanism puts the PATH shim on PATH only for the mechanisms that are
// supposed to have it. mechHooks gets a clean PATH so a shim can never be the
// thing that satisfies a hook assertion, and vice versa.
func envForMechanism(root, mechanism string) []string {
	if mechanism == mechHooks {
		return envWithPath("")
	}
	return envWithPath(filepath.Join(root, "scripts", "git-shim"))
}

// blockedPreDamage is the only definition of "blocked" this file uses: a
// non-zero exit AND an unchanged shared stack AND an uncontaminated working
// tree AND the caller's own work still present. Anything less is not blocked,
// however loudly it complained.
func blockedPreDamage(res verbRun) bool {
	return !res.exitOK &&
		res.depthAfter == res.depthBefore &&
		res.sharedAfter == "shared-base\n" &&
		res.mineAfter == "precious\n"
}

// envWithPath returns the current environment with `prepend` (if non-empty)
// placed ahead of the real PATH, and any pre-existing PATH entry removed so
// there is exactly one.
func envWithPath(prepend string) []string {
	path := os.Getenv("PATH")
	if prepend != "" {
		path = prepend + string(os.PathListSeparator) + path
	}
	out := []string{"PATH=" + path}
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "PATH=") {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// bashOut runs a command line through bash so that `git` is resolved off PATH
// exactly as it would be for an agent typing it.
func bashOut(dir string, env []string, line string) (string, error) {
	cmd := exec.Command("bash", "-c", line)
	cmd.Dir = dir
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func mustWriteFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatalf("write %s in %s: %v", name, dir, err)
	}
}

func mustReadFile(t *testing.T, dir, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		t.Fatalf("read %s in %s: %v", name, dir, err)
	}
	return string(b)
}

// --- shipped files -------------------------------------------------------

// shippedGuardFiles is every file this item ships whose text an agent or
// operator might read as a promise about `git stash`. They are checked for the
// ABSENCE of a coverage table, not for the honesty of their prose: three
// rounds established that prose honesty is not testable.
func shippedGuardFiles(root string) []string {
	return []string{
		filepath.Join(root, "scripts", "git-hooks", "reference-transaction"),
		filepath.Join(root, "scripts", "git-hooks", "post-index-change"),
		filepath.Join(root, "scripts", "git-hooks", "post-checkout"),
		filepath.Join(root, "scripts", "install-git-stash-guard.sh"),
		filepath.Join(root, "scripts", "wt-stash.sh"),
		filepath.Join(root, "scripts", "git-shim", "git"),
		filepath.Join(root, "docs", "ops", "shared-git-state-audit.md"),
	}
}

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
