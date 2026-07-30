package scripts

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
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
// This file deliberately contains NO written-down statement of what the guard
// covers. Two rounds of review were lost to exactly that: text asserting a
// control that measurement did not support. The coverage claim lives in one
// machine-readable table carried by the shipped files, and
// TestStashGuard_DocumentedCoverageMatchesMeasuredCoverage derives the other
// side of the comparison by RUNNING each verb against an installed guard. If
// you want to know what is covered, run the tests and read the t.Logf lines.
//
// Two mechanisms are measured, because git hooks alone cannot meet the done
// condition:
//
//   - the git hooks, which are self-installing and cover every worktree of the
//     clone, and whose effect on pop/drop is depth-dependent (see
//     TestStashGuard_HooksHaveNoRefLevelEffectAtRealisticDepth);
//   - scripts/git-shim/git, a `git` wrapper earlier on PATH, which holds at
//     arbitrary depth because it never invokes git for those verbs, and which
//     only applies where something puts it on PATH.
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

// TestStashGuard_DocumentedCoverageMatchesMeasuredCoverage is the regression
// test for the failure that sent this item back twice. Round 1 shipped text
// claiming push/pop/apply/drop/clear were all blocked when three of them were
// not. Round 2's fix was a blacklist of those four exact phrasings, which an
// adversary defeated in one line by writing the same false claim in different
// words — a blacklist of yesterday's wording cannot catch tomorrow's
// overclaim.
//
// So this is inverted. Nothing here knows what the guard is supposed to do.
// The MEASURED side is derived by running every verb against a really
// installed guard on a realistic shared stack; the DOCUMENTED side is parsed
// out of the shipped text; the test asserts the two verb lists are equal. A
// claim not backed by a measurement fails no matter how it is phrased, and a
// measurement not reflected in the documentation fails too.
func TestStashGuard_DocumentedCoverageMatchesMeasuredCoverage(t *testing.T) {
	root := repoRootDir(t)

	// --- the measured side: run each verb, for real, at realistic depth ---
	measured := map[string]map[string]bool{
		mechHooks: measureCoverage(t, root, mechHooks, 3),
		mechShim:  measureCoverage(t, root, mechShim, 3),
	}
	for mech, verdicts := range measured {
		for _, verb := range stashVerbs {
			t.Logf("measured %s: git stash %-6s blocked-pre-damage=%v", mech, verb, verdicts[verb])
		}
	}

	// --- the documented side: parsed, and identical in every file ---
	var canonical map[string]coverageClaim
	for _, f := range coverageTableFiles(root) {
		claims := parseCoverageTable(t, f)
		if canonical == nil {
			canonical = claims
			continue
		}
		if !sameCoverage(canonical, claims) {
			t.Fatalf("%s carries a DIFFERENT coverage table from the first file; there must be exactly one truth\ngot:  %+v\nwant: %+v",
				filepath.Base(f), claims, canonical)
		}
	}
	if canonical == nil {
		t.Fatalf("no file carries a ready-f75 coverage table")
	}
	if len(canonical) != len(measured) {
		t.Fatalf("coverage table declares %d mechanisms, %d were measured: %v", len(canonical), len(measured), canonical)
	}

	// --- documented == measured, per mechanism, per verb ---
	for mech, verdicts := range measured {
		claim, ok := canonical[mech]
		if !ok {
			t.Fatalf("mechanism %q was measured but is not documented", mech)
		}
		var wantBlocked, wantOpen []string
		for _, verb := range stashVerbs {
			if verdicts[verb] {
				wantBlocked = append(wantBlocked, verb)
			} else {
				wantOpen = append(wantOpen, verb)
			}
		}
		if got := sortedCopy(claim.blocked); !equalStrings(got, sortedCopy(wantBlocked)) {
			t.Errorf("%s: documented blocked-pre-damage %v, MEASURED %v", mech, got, sortedCopy(wantBlocked))
		}
		if got := sortedCopy(claim.notBlocked); !equalStrings(got, sortedCopy(wantOpen)) {
			t.Errorf("%s: documented not-blocked %v, MEASURED %v", mech, got, sortedCopy(wantOpen))
		}
		if strings.TrimSpace(claim.depthLimit) == "" {
			t.Errorf("%s: coverage table states no depth-limit; the whole point of round 2's finding is that the verdict is depth-dependent", mech)
		}
	}

	// --- the depth limit itself is a claim, so measure it too -------------
	// The hooks' refusal on pop is ref-effective at depth 1 and has no effect
	// at all at depth >= 2. If that ever stops being true, the depth-limit
	// text above is wrong and must be rewritten.
	if !measureCoverage(t, root, mechHooks, 3)["pop"] {
		hooksAtOne := rawVerbRun(t, root, mechHooks, 1, "pop")
		if hooksAtOne.exitOK {
			t.Errorf("depth-limit claim says the hooks refuse pop's trailing ref deletion at depth 1, but it exited 0: %s", hooksAtOne.output)
		}
		hooksAtThree := rawVerbRun(t, root, mechHooks, 3, "pop")
		if !hooksAtThree.exitOK {
			t.Errorf("depth-limit claim says the hooks have no ref-level effect on pop at depth >= 2, but it exited non-zero: %s", hooksAtThree.output)
		}
		if hooksAtThree.depthAfter != 2 {
			t.Errorf("pop at depth 3 under the hooks should have consumed an entry (depth 2), got %d", hooksAtThree.depthAfter)
		}
	}
}

// TestStashGuard_NoShippedSentenceOverclaims is the anti-rewording half. It
// reads every sentence of every shipped user-facing file, keeps the ones that
// make a blocking claim about `git stash` verbs, and checks each against the
// MEASURED verdicts — positive claims must name only verbs measured to be
// blocked, negated claims only verbs measured not to be. It does not know any
// phrasing in advance, so "this guard blocks git stash push, pop, apply, drop
// and clear outright" fails on its content, not on its wording.
func TestStashGuard_NoShippedSentenceOverclaims(t *testing.T) {
	root := repoRootDir(t)
	measured := map[string]map[string]bool{
		mechHooks: measureCoverage(t, root, mechHooks, 3),
		mechShim:  measureCoverage(t, root, mechShim, 3),
	}

	checked := 0
	for _, f := range coverageTextFiles(root) {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatalf("read %s: %v", f, err)
		}
		for _, c := range scanCoverageClaims(string(b)) {
			checked++
			verdicts := measured[c.mechanism]
			for _, verb := range c.verbs {
				blocked := verdicts[verb]
				if c.positive && !blocked {
					t.Errorf("%s claims a control it does not have.\n  sentence: %s\n  mechanism: %s\n  `git stash %s` is MEASURED not blocked",
						filepath.Base(f), c.sentence, c.mechanism, verb)
				}
				if !c.positive && blocked {
					t.Errorf("%s understates its coverage, which is also a lie about what happened.\n  sentence: %s\n  mechanism: %s\n  `git stash %s` is MEASURED blocked",
						filepath.Base(f), c.sentence, c.mechanism, verb)
				}
			}
		}
	}
	if checked == 0 {
		t.Fatalf("the sentence scanner matched nothing at all — it has stopped testing anything")
	}
	t.Logf("checked %d verb-bearing blocking claims across %d files", checked, len(coverageTextFiles(root)))
}

// TestStashGuard_ScannerCatchesARewordedOverclaim proves the scanner above is
// not vacuous, using the exact defeat the round-2 adversary used: a plainly
// worded overclaim in wording that appears nowhere in the current text.
func TestStashGuard_ScannerCatchesARewordedOverclaim(t *testing.T) {
	root := repoRootDir(t)
	hooks := measureCoverage(t, root, mechHooks, 3)

	for _, injected := range []string{
		"# This guard blocks git stash push, pop, apply, drop and clear outright.",
		"# Raw `git stash pop` is refused before it can touch your tree.",
		"# Nothing an agent types as `git stash drop` will be allowed to run.",
		"# The hook prevents git stash apply.",
	} {
		claims := scanCoverageClaims(injected)
		if len(claims) == 0 {
			t.Errorf("scanner did not even see a claim in: %s", injected)
			continue
		}
		flagged := false
		for _, c := range claims {
			for _, verb := range c.verbs {
				if c.positive != hooks[verb] {
					flagged = true
				}
			}
		}
		if !flagged {
			t.Errorf("scanner accepted a reworded overclaim: %s\n  parsed: %+v", injected, claims)
		}
	}

	// ...and it must not flag the honest sentences the guard actually ships.
	for _, honest := range []string{
		"# The hooks stop 'git stash push', 'save' and 'clear' before any damage.",
		"# They cannot stop 'git stash apply', 'pop' or 'drop'.",
	} {
		for _, c := range scanCoverageClaims(honest) {
			for _, verb := range c.verbs {
				if c.positive != hooks[verb] {
					t.Errorf("scanner flagged an honest, measured-true sentence: %s\n  parsed: %+v", honest, c)
				}
			}
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
	for _, want := range []string{"SHARED stash stack still holds 1", "cannot stop", "ready-bef"} {
		if !strings.Contains(out, want) {
			t.Errorf("non-empty shared stack must be reported loudly; missing %q in:\n%s", want, out)
		}
	}
	// At depth 1 the operator sees the exit-128 refusal on pop/drop and can
	// easily conclude the guard has them covered. It does not say so.
	if strings.Contains(out, "NO REF-LEVEL EFFECT") {
		t.Errorf("depth-1 install should not print the depth >= 2 warning:\n%s", out)
	}

	// At the depth this repo actually runs at, the report must state the
	// limit found by the round-2 review, and offer the mechanism that closes
	// it. Two entries is already past the boundary.
	deep := setupBaseRepo(t)
	seedPreGuardStashEntry(t, deep, "backlog one\n", "one")
	seedPreGuardStashEntry(t, deep, "backlog two\n", "two")
	out, err = installGuard(scriptsDir(t), deep)
	if err != nil {
		t.Fatalf("install failed: %v\n%s", err, out)
	}
	for _, want := range []string{
		"SHARED stash stack still holds 2",
		"NO REF-LEVEL EFFECT AT ALL",
		"exit 0 and consume an entry",
		"The PATH shim is NOT active",
		"export PATH=",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("at depth >= 2 the report must state the limit and the fix; missing %q in:\n%s", want, out)
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

// TestStashGuard_PathShimHoldsAtArbitraryDepth is the answer to that limit.
// The done condition is that dispatched agents cannot clobber each other, and
// no git hook can deliver it, so the mechanism has to sit outside git: a `git`
// wrapper earlier on the agent's PATH. It refuses before git runs at all, so
// stack depth is irrelevant — asserted here at the depth where the hooks have
// already been measured to do nothing.
func TestStashGuard_PathShimHoldsAtArbitraryDepth(t *testing.T) {
	root := repoRootDir(t)
	for _, depth := range []int{1, 3, 27} {
		for _, verb := range stashVerbs {
			res := rawVerbRun(t, root, mechShim, depth, verb)
			if res.exitOK {
				t.Errorf("depth %d: `git stash %s` was allowed through the PATH shim:\n%s", depth, verb, res.output)
			}
			if res.depthAfter != depth {
				t.Errorf("depth %d: `git stash %s` changed the shared stack to %d entries", depth, verb, res.depthAfter)
			}
			if res.sharedAfter != "shared-base\n" {
				t.Errorf("depth %d: `git stash %s` let a sibling's content into the tree: %q", depth, verb, res.sharedAfter)
			}
			if res.mineAfter != "precious\n" {
				t.Errorf("depth %d: `git stash %s` lost the caller's own uncommitted work: %q", depth, verb, res.mineAfter)
			}
			if !strings.Contains(res.output, "ready-f75") {
				t.Errorf("depth %d: `git stash %s` refusal must explain itself:\n%s", depth, verb, res.output)
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
func TestStashGuard_PathShimPassesEverythingElseThrough(t *testing.T) {
	root := repoRootDir(t)
	repo := setupSharedStackRepo(t, root, 2)
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
func TestStashGuard_PathShimReadsTheVerbTheWayGitDoes(t *testing.T) {
	root := repoRootDir(t)
	env := envWithPath(filepath.Join(root, "scripts", "git-shim"))

	for _, tc := range []struct {
		cmd     string
		refused bool
	}{
		{"git stash", true},                 // implicit push
		{"git stash -m list", true},         // push whose MESSAGE is "list"
		{"git stash -m show", true},         // push whose MESSAGE is "show"
		{"git stash --keep-index", true},    // push with an option first
		{"git stash -u", true},              // push with untracked files
		{"git stash list", false},           // genuinely read-only
		{"git stash show", false},           // genuinely read-only
		{"git stash apply stash@{0}", true}, // verb with an argument
		{"git stash push -m list", true},    // explicit push
	} {
		repo := setupSharedStackRepo(t, root, 2)
		out, err := bashOut(repo, env, tc.cmd)
		gotRefused := err != nil && strings.Contains(out, "ready-f75")
		if gotRefused != tc.refused {
			t.Errorf("%q: refused=%v, want %v\n%s", tc.cmd, gotRefused, tc.refused, out)
		}
		if tc.refused {
			if n := stashDepth(t, repo); n != 2 {
				t.Errorf("%q: reached the shared stack (depth %d, want 2)", tc.cmd, n)
			}
			if got := mustReadFile(t, repo, "mine.txt"); got != "precious\n" {
				t.Errorf("%q: lost the caller's uncommitted work: %q", tc.cmd, got)
			}
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
	repo := setupSharedStackRepo(t, root, 2)
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
	if !strings.Contains(out, "ready-f75") {
		t.Fatalf("two shims on PATH: refusal did not come from the guard:\n%s", out)
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

const (
	mechHooks = "git-hooks"
	mechShim  = "path-shim"
)

// stashVerbs is every `git stash` verb the shipped text is allowed to make a
// coverage claim about. Each one is measured; nothing is assumed. `list` and
// `show` are excluded because they mutate nothing.
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
// mine.txt. The guard is installed afterwards, exactly as it would be in a
// repo that already had a backlog — the live one has 27 entries.
func setupSharedStackRepo(t *testing.T, root string, depth int) string {
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
	mustInstallGuard(t, filepath.Join(root, "scripts"), dir)
	mustWriteFile(t, dir, "mine.txt", "precious\n")
	return dir
}

// rawVerbRun runs ONE raw `git stash <verb>` the way an agent would type it —
// through bash, resolving git off PATH — against a freshly built shared stack
// of the given depth, and reports what actually happened.
func rawVerbRun(t *testing.T, root, mechanism string, depth int, verb string) verbRun {
	t.Helper()
	dir := setupSharedStackRepo(t, root, depth)

	var env []string
	if mechanism == mechShim {
		env = envWithPath(filepath.Join(root, "scripts", "git-shim"))
	} else {
		env = envWithPath("")
	}

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

// measureCoverage derives the coverage claim from behaviour: for each verb, is
// it refused with NOTHING damaged? "Blocked pre-damage" means all four of a
// non-zero exit, an unchanged shared stack, an uncontaminated working tree,
// and the caller's own work still present. Anything less is not blocked.
func measureCoverage(t *testing.T, root, mechanism string, depth int) map[string]bool {
	t.Helper()
	verdicts := make(map[string]bool, len(stashVerbs))
	for _, verb := range stashVerbs {
		res := rawVerbRun(t, root, mechanism, depth, verb)
		verdicts[verb] = !res.exitOK &&
			res.depthAfter == res.depthBefore &&
			res.sharedAfter == "shared-base\n" &&
			res.mineAfter == "precious\n"
	}
	return verdicts
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

// --- the documented side -------------------------------------------------

type coverageClaim struct {
	blocked    []string
	notBlocked []string
	depthLimit string
}

// coverageTableFiles are the files that must carry the canonical, machine
// readable coverage table, byte-identical once comment markers are stripped.
func coverageTableFiles(root string) []string {
	return []string{
		filepath.Join(root, "scripts", "git-hooks", "reference-transaction"),
		filepath.Join(root, "scripts", "install-git-stash-guard.sh"),
		filepath.Join(root, "docs", "ops", "shared-git-state-audit.md"),
	}
}

// coverageTextFiles is every shipped file whose prose an agent or operator
// might read as a promise about `git stash`.
func coverageTextFiles(root string) []string {
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

var (
	tableBeginRe = regexp.MustCompile(`ready-f75 coverage table BEGIN`)
	tableEndRe   = regexp.MustCompile(`ready-f75 coverage table END`)
)

// parseCoverageTable pulls the machine-readable claim out of a shipped file.
// The point of the format is that the claim cannot be reworded: it is a verb
// list, and a verb list is comparable to a measurement.
func parseCoverageTable(t *testing.T, file string) map[string]coverageClaim {
	t.Helper()
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("read %s: %v", file, err)
	}
	lines := strings.Split(string(b), "\n")
	claims := map[string]coverageClaim{}
	var mech string
	inside := false
	for _, raw := range lines {
		line := strings.TrimLeft(raw, " \t")
		line = strings.TrimPrefix(line, "#")
		if tableBeginRe.MatchString(line) {
			inside = true
			continue
		}
		if !inside {
			continue
		}
		if tableEndRe.MatchString(line) {
			inside = false
			continue
		}
		f := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(f, "mechanism:"):
			mech = strings.TrimSpace(strings.TrimPrefix(f, "mechanism:"))
			claims[mech] = coverageClaim{}
		case strings.HasPrefix(f, "blocked-pre-damage:"):
			c := claims[mech]
			c.blocked = parseVerbList(strings.TrimPrefix(f, "blocked-pre-damage:"))
			claims[mech] = c
		case strings.HasPrefix(f, "not-blocked:"):
			c := claims[mech]
			c.notBlocked = parseVerbList(strings.TrimPrefix(f, "not-blocked:"))
			claims[mech] = c
		case strings.HasPrefix(f, "depth-limit:"):
			c := claims[mech]
			c.depthLimit = strings.TrimSpace(strings.TrimPrefix(f, "depth-limit:"))
			claims[mech] = c
		default:
			// continuation of depth-limit prose
			if mech != "" && f != "" {
				c := claims[mech]
				if c.depthLimit != "" {
					c.depthLimit += " " + f
					claims[mech] = c
				}
			}
		}
	}
	if inside {
		t.Fatalf("%s: coverage table has a BEGIN with no END", filepath.Base(file))
	}
	if len(claims) == 0 {
		t.Fatalf("%s: no ready-f75 coverage table found; every file in coverageTableFiles must carry it", filepath.Base(file))
	}
	return claims
}

func parseVerbList(s string) []string {
	s = strings.TrimSpace(s)
	if s == "" || s == "(none)" {
		return nil
	}
	return strings.Fields(s)
}

func sameCoverage(a, b map[string]coverageClaim) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		vb, ok := b[k]
		if !ok {
			return false
		}
		if !equalStrings(sortedCopy(va.blocked), sortedCopy(vb.blocked)) ||
			!equalStrings(sortedCopy(va.notBlocked), sortedCopy(vb.notBlocked)) ||
			normaliseSpace(va.depthLimit) != normaliseSpace(vb.depthLimit) {
			return false
		}
	}
	return true
}

func normaliseSpace(s string) string { return strings.Join(strings.Fields(s), " ") }

func sortedCopy(in []string) []string {
	out := append([]string(nil), in...)
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// --- the prose scanner ---------------------------------------------------

type coverageSentence struct {
	sentence  string
	positive  bool
	mechanism string
	verbs     []string
}

var (
	gitStashRe = regexp.MustCompile(`(?i)git\s+stash\b`)
	// Two families, because their polarity is opposite: "not blocked" means
	// open, "not allowed" means blocked. Reading only one family is how a
	// scanner gets talked past.
	restrictiveRe = regexp.MustCompile(`(?i)\b(block|blocks|blocked|blocking|blockable|refuse|refuses|refused|refusal|prevent|prevents|prevented|stop|stops|stopped|reject|rejects|rejected|deny|denies|denied|forbid|forbids|forbidden)\b`)
	permissiveRe  = regexp.MustCompile(`(?i)\b(allow|allows|allowed|permit|permits|permitted)\b`)
	negationRe    = regexp.MustCompile(`(?i)\b(no|not|never|cannot|can't|nothing|neither|nor|without|unable)\b`)
	shimWordRe    = regexp.MustCompile(`(?i)\b(shim|wrapper)\b`)
	stashVerbRe   = regexp.MustCompile(`(?i)\b(push|save|apply|pop|drop|clear)\b`)
)

// scanCoverageClaims finds every sentence that makes a blocking claim about
// named `git stash` verbs. It knows no phrasings: it looks for the co-presence
// of "git stash", a claim word, and at least one verb, then reads the polarity
// off a negation. Everything it returns is checked against a measurement, so
// the only way to pass is to say something true.
func scanCoverageClaims(text string) []coverageSentence {
	var out []coverageSentence
	for _, s := range splitClaimSentences(text) {
		if !gitStashRe.MatchString(s) {
			continue
		}
		restrictive := restrictiveRe.MatchString(s)
		permissive := permissiveRe.MatchString(s)
		if !restrictive && !permissive {
			continue
		}
		verbs := uniqueLower(stashVerbRe.FindAllString(s, -1))
		if len(verbs) == 0 {
			continue
		}
		negated := negationRe.MatchString(s)
		// "blocks X" and "does not allow X" both claim X is blocked;
		// "does not block X" and "allows X" both claim it is open.
		claimsBlocked := negated == permissive
		if restrictive && permissive {
			claimsBlocked = !negated // restrictive reading wins when mixed
		}
		mech := mechHooks
		if shimWordRe.MatchString(s) {
			mech = mechShim
		}
		out = append(out, coverageSentence{
			sentence:  normaliseSpace(s),
			positive:  claimsBlocked,
			mechanism: mech,
			verbs:     verbs,
		})
	}
	return out
}

// splitClaimSentences turns a shell script, a hook or a markdown document into
// claim-sized units: comment markers stripped, a break at every blank line and
// at every bullet / table row / heading (so a markdown table is a claim per
// cell rather than one giant run-on), then split on sentence punctuation and
// on the markdown cell separator.
func splitClaimSentences(text string) []string {
	var joined strings.Builder
	for _, raw := range strings.Split(text, "\n") {
		line := strings.TrimLeft(raw, " \t")
		line = strings.TrimPrefix(line, "//")
		line = strings.TrimPrefix(line, "#")
		line = strings.TrimLeft(line, " \t")
		if line == "" {
			joined.WriteString(" . ")
			continue
		}
		switch line[0] {
		case '*', '-', '|', '#', '>', '+':
			joined.WriteString(" . ")
		}
		joined.WriteString(line)
		joined.WriteString(" ")
	}

	runes := []rune(joined.String())
	var out []string
	var cur strings.Builder
	for i, r := range runes {
		if r == '.' || r == ';' || r == '!' || r == '?' || r == '|' {
			// Do not split inside a version number such as "git 2.43".
			if r == '.' && i > 0 && i+1 < len(runes) && isASCIIDigit(runes[i-1]) && isASCIIDigit(runes[i+1]) {
				cur.WriteRune(r)
				continue
			}
			if s := strings.TrimSpace(cur.String()); s != "" {
				out = append(out, s)
			}
			cur.Reset()
			continue
		}
		cur.WriteRune(r)
	}
	if s := strings.TrimSpace(cur.String()); s != "" {
		out = append(out, s)
	}
	return out
}

func isASCIIDigit(r rune) bool { return r >= '0' && r <= '9' }

func uniqueLower(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		s = strings.ToLower(s)
		if seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	sort.Strings(out)
	return out
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
