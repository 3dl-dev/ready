# Shared `.git` state and cross-worktree hazards

**Item:** ready-f75, clause 2 of the done condition ("audit the common `.git`
directory for OTHER shared mutable state with the same hazard").
**Measured on:** git 2.43.0, Linux, in throwaway repos with two or three real
`git worktree add` checkouts. Every verdict below is a measurement, not a
reading of the documentation. Re-measure before trusting any of it on a
different git version — the whole reason this item exists is that a guard was
shipped on an assumption about hook coverage that turned out to be false.

## Why this audit exists

`git worktree` is what makes parallel dispatched agents safe: each agent gets
its own working tree and index, so it cannot see or overwrite a sibling's
files. That isolation is real but **partial** — the `.git` directory is
*common* to every worktree, and anything stored there is shared mutable state.

On 2026-07-29 two agents in separate worktrees both used `git stash`. Because
`refs/stash` is common, their entries interleaved on one stack, and one
agent's `git stash pop` reverted the other's uncommitted files to HEAD.
Nothing failed. It surfaced only because one agent volunteered it.

So the question this audit answers is: **what else is like `refs/stash`?**

## Method

For each candidate, `git rev-parse --git-path <thing>` was run from inside a
*linked* worktree. If the resolved path lands under
`.git/worktrees/<id>/`, the state is per-worktree and cannot be a
cross-worktree hazard. If it lands in `.git/` itself, it is shared, and the
follow-up question is whether one worktree can mutate it in a way that harms
another. Where the answer was "maybe", the harm was attempted for real.

## Verdicts

### 1. `refs/stash` — CONFIRMED HAZARD, the observed incident

`.git/refs/stash`. Shared. This is the incident. Addressed by the guard in
`scripts/install-git-stash-guard.sh`; exact, measured coverage is documented
in `scripts/git-hooks/reference-transaction` and summarised here:

| verb | blockable? | measured behaviour with the guard installed |
| --- | --- | --- |
| `git stash push` | **yes, pre-damage** | ref transaction aborted, exit 128, working tree keeps the change, nothing reaches the stack |
| `git stash clear` | **yes** | pure ref deletion, aborted, every entry preserved |
| `git stash apply` | **no** | opens *no* ref transaction. Only `post-index-change` fires, and git ignores its exit code (a hook returning 1 still leaves apply at exit 0 with the tree rewritten). Covered by a loud warning only. |
| `git stash pop` | **no** | applies *before* it drops; no hook runs before the apply |
| `git stash drop` | **no** | rewrites the `refs/stash` reflog via `git reflog delete --rewrite --updateref`, which opens no transaction. Aborting the trailing ref deletion does keep the last entry reachable via `refs/stash` (recoverable with `git stash apply refs/stash`) instead of letting it become garbage. |
| all three, on an **empty** stack | **n/a** | fail at `stash@{0}` resolution: exit 1, "No stash entries found.", working tree untouched |

The load-bearing property is the first row: the stack becomes **ungrowable**,
so an agent's work can never reach shared state and a sibling's pop can never
discard it. The residual — an agent popping a *pre-existing* entry into its
own tree — shrinks to zero once the stack is empty. Emptying the existing
28-entry backlog is data deletion and is owner-reserved: **ready-bef**.

Aliasing `git stash` was measured and discarded: git resolves its own builtins
before consulting an alias of the same name, so `alias.stash` is silently
ignored. Pointing `refs/stash` at the per-worktree `refs/worktree/*` namespace
was also measured and discarded: push and list work, but `git stash pop` then
fails with "not a stash reference", which strands work instead of protecting
it.

### 2. `git worktree remove --force` on a sibling — CONFIRMED HAZARD, worst of the lot

Not a file, a command, but it is exactly the same class and it is worse than
the stash bug: from worktree B, `git worktree remove --force <path-to-A>`
exited 0 and **deleted worktree A's entire directory including its
uncommitted work**. Plain `remove` refuses ("contains modified or untracked
files, use --force"), so a single `--force` is all it takes.

`git worktree lock` does defend: removing a locked worktree needs `-f -f`.
Measured on the live repo: **3 of 67 worktree admin directories carry a
`locked` marker**, so 64 are one `--force` away from destruction.

Filed as a child item. Severity: high — unlike the stash case there is no
recovery at all, the files are gone.

### 3. `git update-ref` bypasses the checked-out-branch guard — CONFIRMED HAZARD

Shared: `.git/refs/heads/*`, `.git/packed-refs`, `.git/logs/refs/*`.

git *does* protect a branch that another worktree has checked out — measured
from a sibling worktree:

- `git branch -f wta HEAD` → exit 128, "cannot force update the branch 'wta'
  used by worktree at ..."
- `git branch -D wta` → exit 1, "cannot delete branch 'wta' used by worktree
  at ..."

But the plumbing skips that check entirely:

- `git update-ref refs/heads/wta HEAD` → **exit 0**, branch silently moved.

The sibling's `HEAD` is symbolic to that branch, so after this its working
tree and index silently disagree with its own `HEAD` — the next `git status`
or commit in that worktree reports a diff nobody made. Branches *not* checked
out anywhere have no protection at all: `git branch -f idle` and
`git branch -D idle` both succeeded from an unrelated worktree.

Filed as a child item.

### 4. Shared `.git/config` with `extensions.worktreeConfig` unset — CONFIRMED HAZARD

`git rev-parse --git-path config` → `.git/config`, shared. Measured: a
`git config --local audit.probe from-wtb` in worktree B is read back verbatim
from worktree A. And because `extensions.worktreeConfig` is **unset** in this
repo, the per-worktree escape hatch is unavailable — `git config --worktree`
fails with "cannot be used with multiple working trees unless the config
extension is enabled".

So any agent's `git config --local` silently rewrites every sibling's
configuration: `user.email`, `commit.gpgsign`, `remote.origin.url`,
`core.hooksPath` (which would disable this very guard), `alias.*`. Note that
the stash guard's own installer writes `alias.wtstash` here deliberately —
that is why one install covers every worktree, past and future.

Filed as a child item.

### 5. `.git/hooks/` — SHARED BY DESIGN, accepted with a mitigation

Shared (or wherever `core.hooksPath` points — measured: `git rev-parse
--git-path hooks` honors it, identically from the main checkout and from a
linked worktree). This sharing is what makes the stash guard work: one install
protects every worktree including ones created later.

The same property is the risk: any agent can install a hook that affects every
sibling. Mitigation in place: the installer refuses to overwrite a
`reference-transaction`, `post-index-change` or `post-checkout` hook it did
not write (asserted by
`TestStashGuard_InstallerRefusesToClobberForeignHook`), and it verifies
empirically that its hook actually fires rather than trusting the copy
(`TestStashGuard_InstallerFailsLoudlyWhenHookNeverFires`). No further action.

### 6. `.git/objects/`, gc and pruning — REAL BUT NARROW, monitored, not actioned

Shared. Object writes are content-addressed and effectively append-only, so
concurrent writers do not corrupt each other. The hazard is deletion: `git gc`
treats every worktree's HEAD and index as roots, but an object reachable only
from a *dropped* stash entry, or from a `git stash create` commit not yet
pointed at by any ref, is prunable. `gc.pid` is shared, so concurrent gc runs
serialise rather than race.

No observed incident, and the stash guard reduces the exposure (refusing the
trailing `refs/stash` deletion keeps the last dropped entry reachable).
Recorded here rather than filed.

### 7. `rr-cache` (rerere) — NOT A HAZARD TODAY, keep it that way

`git rev-parse --git-path rr-cache` → `.git/rr-cache`, shared. If rerere were
enabled, a conflict resolution recorded by one agent would be replayed
automatically into another agent's conflicting merge — a silent wrong
resolution, exactly the failure mode of the stash bug.

Measured on the live repo: `rerere.enabled` is unset (default off) and
`.git/rr-cache` does not exist. No hazard now. Enabling rerere repo-wide would
create one, and that decision should not be made casually.

### 8. Per-worktree, therefore NOT hazards — measured, including the ones the review asked about

`git rev-parse --git-path X` from a linked worktree resolved all of these
under `.git/worktrees/<id>/`:

| state | resolved to |
| --- | --- |
| `index`, **`index.lock`** | `.git/worktrees/<id>/index`, `.../index.lock` |
| `HEAD`, `ORIG_HEAD`, `logs/HEAD` | `.git/worktrees/<id>/...` |
| `rebase-merge`, `rebase-apply`, `sequencer` | `.git/worktrees/<id>/...` |
| `MERGE_HEAD`, `CHERRY_PICK_HEAD` | `.git/worktrees/<id>/...` |
| `FETCH_HEAD`, `COMMIT_EDITMSG` | `.git/worktrees/<id>/...` |
| `refs/worktree/*`, `refs/bisect/*` | `.git/worktrees/<id>/refs/...` |
| `config.worktree` | `.git/worktrees/<id>/config.worktree` (inert unless `extensions.worktreeConfig`) |

**The index lock is explicitly not a shared hazard** — it was on the candidate
list in the item, and the measurement says each worktree has its own. Two
agents cannot block each other on `index.lock`.

`refs/worktree/*` being per-worktree *at the filesystem level* is what
`git wtstash` is built on: two worktrees' stacks are physically different
files, so there is nothing to interleave.

### 9. Also shared, low severity — recorded, not filed

- `.git/info/exclude`, `.git/info/attributes` — shared ignore/attribute rules;
  an edit changes every sibling's view of which files are ignored.
- `.git/refs/tags/*` — shared; tag creation/deletion crosses worktrees, but
  dispatched agents do not tag.
- `.git/shallow` — shared; absent here (full clone).
- `.git/modules/` — shared; no submodules in this repo.

## Child items filed from this audit

| hazard | item |
| --- | --- |
| §2 `git worktree remove --force` destroys a sibling's uncommitted work; 64 of 67 worktrees unlocked | ready-8d2 |
| §3 `git update-ref` bypasses the checked-out-branch protection | ready-281 |
| §4 `git config --local` is shared; `extensions.worktreeConfig` unset | ready-2c6 |
| §1 pre-existing 28-entry `refs/stash` backlog (owner-reserved deletion) | ready-bef |

Sections 6, 7 and 9 were judged real-but-not-actionable and are recorded here
rather than filed, with the measurement that justifies that call. Section 8 is
the negative result: the states most likely to be *assumed* hazards — the
index lock above all — are per-worktree and are not hazards.
