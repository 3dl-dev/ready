#!/usr/bin/env bash
# install-git-stash-guard.sh — activate the ready-f75 stash guard for the
# repository this script is run from (main checkout or any linked worktree;
# hooks and non-worktree config are common to the whole clone, so one run
# covers every worktree, past and future).
#
# WHAT IT INSTALLS
#   1. hooks/reference-transaction — aborts writes to refs/stash. This is the
#      blocking half, and its reach is bounded; see the coverage table below,
#      which that file carries verbatim.
#   2. hooks/post-index-change — the detection half: turns the unstoppable
#      apply/pop case into a LOUD stderr warning.
#   3. hooks/post-checkout — re-asserts the guard on every checkout, and in
#      particular on `git worktree add`, which git runs post-checkout for.
#      That is the worktree-creation path, so newly dispatched worktrees pick
#      the guard up without anybody remembering to run this script.
#   4. wt-stash.sh, wired up as `git wtstash` — the worktree-scoped
#      replacement for `git stash`, on refs/worktree/wtstash/N.
#   5. bin/git — the PATH shim, materialised but NOT activated (see below).
#
#   Aliasing `git stash` itself is NOT one of the mechanisms, because it does
#   not work: git resolves its own builtins before consulting an alias of the
#   same name, so `alias.stash` is silently ignored (measured, not assumed).
#
# COVERAGE. The table is not prose: every row is re-derived on every test run
# by TestStashGuard_DocumentedCoverageMatchesMeasuredCoverage, which runs each
# verb against a really-installed guard and fails if the table and the
# measurement disagree. An earlier version of this file asserted coverage it
# did not have.
#
# ready-f75 coverage table BEGIN
#   mechanism: git-hooks
#     blocked-pre-damage: push save clear
#     not-blocked: apply pop drop
#     depth-limit: REF-EFFECTIVE ONLY AT DEPTH <= 1. At a shared-stack depth
#       of 1 the trailing refs/stash deletion is refused, but the entry has
#       already left `git stash list` by then; the refusal only keeps the
#       commit reachable via refs/stash. At depth 2 or more there is no
#       ref-level effect whatsoever — exit 0, entry consumed, sibling content
#       in the caller's tree. Measured at depth 1, 2, 3 and 5. The live ready
#       clone sits at depth 27, i.e. in the row with no ref-level effect, so
#       at realistic depth the post-index-change warning is the whole of the
#       hooks' coverage — and for drop at depth >= 2 there is not even that.
#   mechanism: path-shim
#     blocked-pre-damage: push save apply pop drop clear
#     not-blocked: (none)
#     depth-limit: none — scripts/git-shim/git never invokes git for these
#       verbs, so shared-stack depth is irrelevant. It applies only where its
#       directory is earlier on PATH than the real git, which is a property
#       of the process that spawns an agent and cannot be installed from
#       inside this repository the way the hooks can.
# ready-f75 coverage table END
#
# READ THE TWO MECHANISMS TOGETHER, NOT SEPARATELY. "The stash guard is
# installed" does NOT mean an agent is safe at this repo's stack depth. The
# hooks make the shared stack ungrowable, which is what stops a NEW cross-
# worktree entanglement from forming. They do not make consuming the existing
# 27-entry backlog safe, and at that depth they do not even fail. Only the
# PATH shim does, and activating it is one line the caller must run:
#
#   export PATH="$(git rev-parse --git-common-dir)/f75-stash-guard/bin:$PATH"
#
# (The installer prints the resolved form. Emptying the backlog, which would
# make the whole question moot, is owner-reserved data deletion: ready-bef.)
#
# WHO RUNS IT — the guard must not depend on a human remembering:
#   * hooks/post-checkout re-asserts it on every `git worktree add`, which is
#     the worktree-creation path dispatched agents come through;
#   * scripts/wt_stash_test.go's TestStashGuard_SelfInstallsInThisClone runs it
#     on the `go test ./...` baseline every agent and CI already execute;
#   * .github/workflows/go-test.yml runs it explicitly and fails the job if it
#     does not fire.
#
#   STATED LIMITATION: the FIRST activation in a brand-new clone still has to
#   come from one of the three above (in practice: the first test run). git
#   deliberately provides no way for a repository to install its own hooks at
#   clone time — that would be arbitrary code execution on `git clone` — so
#   "zero-step activation on clone" is not achievable and is not claimed. What
#   is claimed: after any one of those three has run once, every worktree of
#   the clone is covered, including ones created later, because hooks live in
#   shared state.
#
# WHERE IT INSTALLS: the directory git itself will read hooks from, i.e.
# `git rev-parse --git-path hooks`, which honors core.hooksPath. A previous
# version wrote to `--git-common-dir`/hooks unconditionally and would have
# been silently inert in any repo that points core.hooksPath elsewhere, while
# printing success.
#
# SELF-VERIFICATION: after installing, this attempts a real ref write that
# the hook is supposed to refuse (refs/f75-stash-guard-canary — never
# refs/stash, which must not be touched) and FAILS LOUDLY, non-zero, if the
# write is allowed. "Installed" means "measured to fire", not "file copied".
#
# Idempotent. Refuses to overwrite a hook it did not write.
#
# Modes:
#   (default)        install + verify, verbose
#   --quiet          install + verify, print only warnings/failures
#   --new-worktree   like --quiet plus a short one-time notice for a freshly
#                    created worktree
#   --verify-only    do not install anything; just verify and report
set -uo pipefail

HOOK_MARKER="ready-f75 stash guard"
CANARY_REF="refs/f75-stash-guard-canary"
HOOK_NAMES=(reference-transaction post-index-change post-checkout)

MODE="verbose"
case "${1:-}" in
  --quiet) MODE="quiet" ;;
  --new-worktree) MODE="new-worktree" ;;
  --verify-only) MODE="verify-only" ;;
  "") : ;;
  *)
    echo "install-git-stash-guard.sh: unknown option '$1'" >&2
    exit 2
    ;;
esac

say() { [[ "$MODE" == "verbose" ]] && echo "$@"; return 0; }
warn() { echo "$@" >&2; }

die() {
  warn ""
  warn "install-git-stash-guard.sh: FAILED — the ready-f75 stash guard is NOT active."
  for line in "$@"; do warn "  $line"; done
  warn ""
  warn "  hooks dir git will read:  ${HOOKS_DIR:-<unresolved>}"
  warn "  core.hooksPath:           ${HOOKS_PATH_CFG:-<unset>}"
  warn "  git common dir:           ${COMMON_DIR:-<unresolved>}"
  warn ""
  warn "  Do not proceed as if git stash were guarded. Fix the above and re-run."
  exit 1
}

# --- locate sources ----------------------------------------------------
# Two source layouts, because post-checkout has to be able to re-run this
# script from inside the .git directory, where scripts/ may not exist (the
# worktree can be checked out at any commit).
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
if [[ -d "$SCRIPT_DIR/git-hooks" ]]; then
  HOOK_SRC_DIR="$SCRIPT_DIR/git-hooks"      # repo layout: scripts/
  WT_STASH_SRC="$SCRIPT_DIR/wt-stash.sh"
  SHIM_SRC="$SCRIPT_DIR/git-shim/git"
  SELF_SRC="${BASH_SOURCE[0]}"
elif [[ -d "$SCRIPT_DIR/hooks" ]]; then
  HOOK_SRC_DIR="$SCRIPT_DIR/hooks"          # installed layout: .git/f75-stash-guard/
  WT_STASH_SRC="$SCRIPT_DIR/wt-stash.sh"
  SHIM_SRC="$SCRIPT_DIR/bin/git"
  SELF_SRC="${BASH_SOURCE[0]}"
else
  warn "install-git-stash-guard.sh: cannot find the hook sources next to $SCRIPT_DIR"
  exit 1
fi

if [[ "$MODE" != "verify-only" ]]; then
  for n in "${HOOK_NAMES[@]}"; do
    [[ -f "$HOOK_SRC_DIR/$n" ]] || { warn "install-git-stash-guard.sh: missing $HOOK_SRC_DIR/$n"; exit 1; }
  done
  [[ -f "$WT_STASH_SRC" ]] || { warn "install-git-stash-guard.sh: missing $WT_STASH_SRC"; exit 1; }
  [[ -f "$SHIM_SRC" ]] || { warn "install-git-stash-guard.sh: missing $SHIM_SRC"; exit 1; }
fi

# --- resolve the paths git itself will use -----------------------------
COMMON_DIR="$(git rev-parse --git-common-dir 2>/dev/null)" || {
  warn "install-git-stash-guard.sh: not inside a git repository"
  exit 1
}
case "$COMMON_DIR" in /*) : ;; *) COMMON_DIR="$(pwd)/$COMMON_DIR" ;; esac

HOOKS_PATH_CFG="$(git config --get core.hooksPath || true)"
# --git-path resolves core.hooksPath for us (verified: honored identically
# from the main checkout and from a linked worktree). Fall back to the common
# dir only if that ever comes back empty.
HOOKS_DIR="$(git rev-parse --git-path hooks 2>/dev/null || true)"
[[ -n "$HOOKS_DIR" ]] || HOOKS_DIR="$COMMON_DIR/hooks"
case "$HOOKS_DIR" in /*) : ;; *) HOOKS_DIR="$(pwd)/$HOOKS_DIR" ;; esac

GUARD_DIR="$COMMON_DIR/f75-stash-guard"
SHIM_DIR="$GUARD_DIR/bin"

# --- install -----------------------------------------------------------
if [[ "$MODE" != "verify-only" ]]; then
  mkdir -p "$HOOKS_DIR" "$GUARD_DIR/hooks" "$SHIM_DIR" || die "cannot create $HOOKS_DIR"

  for n in "${HOOK_NAMES[@]}"; do
    dest="$HOOKS_DIR/$n"
    if [[ -e "$dest" ]] && ! grep -q "$HOOK_MARKER" "$dest" 2>/dev/null; then
      die "$dest already exists and is not the ready-f75 guard." \
          "Refusing to overwrite somebody else's hook — merge it by hand."
    fi
    cp "$HOOK_SRC_DIR/$n" "$dest" || die "cannot write $dest"
    chmod +x "$dest"
    # When re-run from the .git-resident copy (the post-checkout path), the
    # source IS the cached copy; copying it onto itself is an error, not a
    # no-op.
    if [[ "$HOOK_SRC_DIR/$n" != "$GUARD_DIR/hooks/$n" ]]; then
      cp "$HOOK_SRC_DIR/$n" "$GUARD_DIR/hooks/$n"
    fi
    chmod +x "$GUARD_DIR/hooks/$n"
  done

  # Cache the sources (and this script) inside the common .git dir so
  # post-checkout can re-run the install from any worktree at any commit.
  if [[ "$WT_STASH_SRC" != "$GUARD_DIR/wt-stash.sh" ]]; then
    cp "$WT_STASH_SRC" "$GUARD_DIR/wt-stash.sh"
  fi
  chmod +x "$GUARD_DIR/wt-stash.sh"

  # The PATH shim is materialised but never activated from here: activating
  # it means changing PATH in the process that spawns an agent, which this
  # script does not own. It is put somewhere stable so the export line the
  # caller needs is a real path, not an instruction to go find a file.
  if [[ "$SHIM_SRC" != "$SHIM_DIR/git" ]]; then
    cp "$SHIM_SRC" "$SHIM_DIR/git" || die "cannot write $SHIM_DIR/git"
  fi
  chmod +x "$SHIM_DIR/git"
  if [[ "$(cd "$(dirname "$SELF_SRC")" && pwd)" != "$GUARD_DIR" ]]; then
    cp "$SELF_SRC" "$GUARD_DIR/install.sh"
  fi
  chmod +x "$GUARD_DIR/install.sh"

  git config --local alias.wtstash "!f() { \"$GUARD_DIR/wt-stash.sh\" \"\$@\"; }; f" \
    || die "cannot set alias.wtstash"
fi

# --- self-verification: does git actually RUN the hook here? -----------
head_sha="$(git rev-parse --verify -q HEAD || true)"
if [[ -z "$head_sha" ]]; then
  warn "install-git-stash-guard.sh: repository has no commits yet — cannot verify the"
  warn "  hook empirically. Re-run this after the first commit."
else
  canary_out="$(git update-ref "$CANARY_REF" "$head_sha" 2>&1)"
  canary_rc=$?
  if [[ $canary_rc -eq 0 ]]; then
    # The write went through: git did not run our hook. Clean up, then fail.
    residue=()
    git update-ref -d "$CANARY_REF" >/dev/null 2>&1 ||
      residue=("(could not clean up $CANARY_REF — delete it by hand)")
    die "the hook did NOT fire: a write to $CANARY_REF was ALLOWED." \
        "git is reading hooks from somewhere other than where the guard was written," \
        "or the installed hook is not executable." \
        ${residue+"${residue[@]}"}
  fi
  if [[ "$canary_out" != *"$HOOK_MARKER"* ]]; then
    die "a write to $CANARY_REF was refused, but not by the ready-f75 guard:" \
        "${canary_out}"
  fi
fi

# --- report the state of the SHARED stack ------------------------------
# The guard makes the shared stack ungrowable. It cannot make an already
# non-empty stack safe: `git stash apply`/`pop` of a PRE-EXISTING entry opens
# no ref transaction and no hook can stop it. Emptying the stack is data
# deletion and is owner-reserved (ready-bef), so this reports and does not
# act.
stack_depth="$(git stash list 2>/dev/null | wc -l | tr -d ' ')"
shim_on_path=no
if command -v git >/dev/null 2>&1 && [[ "$(command -v git)" == "$SHIM_DIR/git" ]]; then
  shim_on_path=yes
fi

if [[ "${stack_depth:-0}" -gt 0 && "$MODE" != "quiet" ]]; then
  warn "ready-f75 stash guard: the SHARED stash stack still holds $stack_depth entr$([[ "$stack_depth" == 1 ]] && echo y || echo ies)."
  warn "  No new entry can be pushed onto it any more, so it can only shrink."
  warn "  But the hooks cannot stop 'git stash apply', 'pop' or 'drop' against one"
  warn "  of those entries — apply/pop would dump another worktree's old content"
  warn "  into your tree, and drop removes an entry before any hook runs."
  if [[ "${stack_depth:-0}" -ge 2 ]]; then
    warn "  AT THIS DEPTH ($stack_depth >= 2) THE HOOKS HAVE NO REF-LEVEL EFFECT AT ALL:"
    warn "  raw 'git stash pop' and 'git stash drop' exit 0 and consume an entry, and"
    warn "  drop does so without printing anything. Only at depth 1 does the trailing"
    warn "  ref deletion get refused, and even then the entry is already consumed."
  fi
  if [[ "$shim_on_path" == yes ]]; then
    warn "  The PATH shim IS active, so those verbs are refused before git runs."
  else
    warn "  The PATH shim is NOT active. To make those verbs impossible at any depth:"
    warn "    export PATH=\"$SHIM_DIR:\$PATH\""
  fi
  warn "  Otherwise use 'git wtstash' and do not run raw 'git stash apply'/'pop'/'drop'."
  warn "  Emptying the backlog is owner-reserved data deletion: ready-bef."
fi

case "$MODE" in
  verbose)
    say "ready-f75 stash guard ACTIVE (verified: a guarded ref write was refused by the hook)"
    say "  hooks dir:  $HOOKS_DIR"
    for n in "${HOOK_NAMES[@]}"; do say "    $n"; done
    say "  git wtstash -> $GUARD_DIR/wt-stash.sh  (per-worktree, refs/worktree/wtstash/*)"
    say "  shared stack depth: ${stack_depth:-0}"
    say ""
    say "  hooks:     stop 'git stash push', 'save' and 'clear' before any damage."
    say "             They cannot stop 'git stash apply', 'pop' or 'drop', and their"
    say "             refusal on pop/drop is ref-effective only at stack depth 1."
    say "  PATH shim: stops every one of those verbs at any depth, by never running"
    say "             git — but ONLY where it is on PATH, which this script cannot"
    say "             do for you. Currently on PATH: $shim_on_path"
    say "             export PATH=\"$SHIM_DIR:\$PATH\""
    say "  Per-verb measurements: docs/ops/shared-git-state-audit.md"
    ;;
  new-worktree)
    warn "ready-f75 stash guard is active in this repo: raw 'git stash' push is"
    warn "  refused because refs/stash is shared across every worktree. Use"
    warn "  'git wtstash' (push|list|pop|apply|drop|clear) or commit WIP on your"
    warn "  own branch. The hooks do NOT stop apply/pop/drop of an existing entry;"
    warn "  for that, export PATH=\"$SHIM_DIR:\$PATH\""
    ;;
  verify-only)
    say "ready-f75 stash guard verified ACTIVE ($HOOKS_DIR)"
    ;;
esac

exit 0
