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

// These tests exercise scripts/wt-stash.sh, scripts/git-hooks/reference-transaction,
// and scripts/install-git-stash-guard.sh (ready-f75): git stash's single
// shared refs/stash ref, in the repository's COMMON .git directory, is the
// one thing `git worktree` does NOT isolate per-worktree. Two agents
// dispatched into separate worktrees who both run `git stash` push onto the
// same stack; a `git stash pop` by either one can silently apply and then
// delete the OTHER's entry — observed for real on 2026-07-29 (14+
// interleaved entries from 10 branches, one agent's pop reverting a
// sibling's uncommitted files to HEAD with no error).
//
// The fix has two parts:
//   - a reference-transaction hook (shared, like refs/stash itself) that
//     ABORTS any write to refs/stash, so raw `git stash` fails loudly
//     instead of silently interleaving;
//   - `git wtstash` (push/list/pop/apply/drop/clear), a NEW subcommand that
//     reimplements the same operations on refs/worktree/wtstash/N — a ref
//     namespace git stores per-worktree at the filesystem level
//     (.git/refs/worktree/ for the main worktree,
//     .git/worktrees/<id>/refs/worktree/ for linked ones), so two
//     worktrees' stacks are physically different files, not just logically
//     different ref names.
//
// (Aliasing `git stash` itself to the safe implementation was the first
// design tried here and does NOT work: git resolves its own builtins before
// consulting an alias of the same name, so `alias.stash` is silently a
// no-op — confirmed empirically before committing to the hook design.)
//
// These tests build a real base repo with two real `git worktree add`
// checkouts, install the guard exactly the way
// scripts/install-git-stash-guard.sh does, and drive both `git stash`
// (expected to fail loudly) and `git wtstash` (expected to work, and stay
// isolated) from both worktrees.

// TestGitStashGuard_BlocksRawGitStash proves the enforcement half: once the
// guard is installed, plain `git stash push` in a worktree fails loudly
// (non-zero exit, guidance on stderr) instead of silently succeeding onto
// the shared refs/stash — and the agent's uncommitted change is NOT lost in
// the process.
func TestGitStashGuard_BlocksRawGitStash(t *testing.T) {
	repoRoot := repoRootDir(t)
	scriptsDir := filepath.Join(repoRoot, "scripts")

	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	mustAddWorktree(t, base, wtA, "work/a")
	mustInstallGuard(t, scriptsDir, base)

	mustWriteWork(t, wtA, "wt-a change 1\n")
	out, err := gitOut(wtA, "stash", "push", "-m", "should be blocked")
	if err == nil {
		t.Fatalf("expected `git stash push` to fail under the ready-f75 guard, it succeeded: %s", out)
	}
	if !strings.Contains(out, "ready-f75") {
		t.Fatalf("blocked git stash should explain why (ready-f75 guard message), got: %s", out)
	}
	// The whole point: the guard must fail SAFE. The agent's change must
	// still be sitting in the working tree, not lost mid-abort.
	if got := mustReadWork(t, wtA); got != "wt-a change 1\n" {
		t.Fatalf("blocked git stash must not lose the working tree change: got %q", got)
	}
	if n := strings.Count(mustGit(t, wtA, "stash", "list"), "stash@{"); n != 0 {
		t.Fatalf("blocked git stash must not have partially written refs/stash, got %d entries", n)
	}
}

// TestWtStash_WorktreesDoNotClobberEachOther proves the sequential case for
// the safe replacement: one worktree's `git wtstash` push/pop never shows
// up in, or disturbs, another worktree's stack.
func TestWtStash_WorktreesDoNotClobberEachOther(t *testing.T) {
	repoRoot := repoRootDir(t)
	scriptsDir := filepath.Join(repoRoot, "scripts")

	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	mustAddWorktree(t, base, wtA, "work/a")

	mustInstallGuard(t, scriptsDir, base)

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
}

// TestWtStash_ConcurrentPushPopAcrossWorktreesStaysIsolated is the DONE
// condition's proof: it actually runs two worktrees stashing AT THE SAME
// TIME (real OS processes, released off a shared start barrier, not just
// sequential calls) and checks that every push/pop round in each worktree
// only ever sees and restores ITS OWN content, never the other's.
func TestWtStash_ConcurrentPushPopAcrossWorktreesStaysIsolated(t *testing.T) {
	repoRoot := repoRootDir(t)
	scriptsDir := filepath.Join(repoRoot, "scripts")

	base := setupBaseRepo(t)
	wtA := filepath.Join(t.TempDir(), "wt-a")
	wtB := filepath.Join(t.TempDir(), "wt-b")
	mustAddWorktree(t, base, wtA, "work/a")
	mustAddWorktree(t, base, wtB, "work/b")
	mustInstallGuard(t, scriptsDir, base)

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

func mustInstallGuard(t *testing.T, scriptsDir, runFromDir string) {
	t.Helper()
	cmd := exec.Command(filepath.Join(scriptsDir, "install-git-stash-guard.sh"))
	cmd.Dir = runFromDir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("install-git-stash-guard.sh (dir=%s) failed: %v\n%s", runFromDir, err, out)
	}
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
