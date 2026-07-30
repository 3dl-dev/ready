#!/usr/bin/env bash
# install-git-stash-guard.sh — activate the ready-f75 stash guard for the
# repository this script is run from (main checkout or any linked worktree;
# hooks and non-worktree config are common to the whole clone, so one run
# covers every worktree, past and future).
#
# WHAT IT INSTALLS
#   1. hooks/reference-transaction — aborts writes to refs/stash. This is the
#      blocking half. Read that file for its EXACT coverage: it blocks
#      `git stash push` and `git stash clear` before any damage, and it
#      CANNOT block `git stash apply` or the reflog-rewrite half of
#      drop/pop, because those open no ref transaction. Nothing here claims
#      otherwise — an earlier version of these files claimed
#      push/pop/apply/drop/clear were all blocked, and that was false.
#   2. hooks/post-index-change — the detection half: turns the unblockable
#      `git stash apply` / `git stash pop` into a LOUD stderr warning.
#   3. hooks/post-checkout — re-asserts the guard on every checkout, and in
#      particular on `git worktree add`, which git runs post-checkout for.
#      That is the worktree-creation path, so newly dispatched worktrees pick
#      the guard up without anybody remembering to run this script.
#   4. wt-stash.sh, wired up as `git wtstash` — the worktree-scoped
#      replacement for `git stash`, on refs/worktree/wtstash/N.
#
#   Aliasing `git stash` itself is NOT one of the mechanisms, because it does
#   not work: git resolves its own builtins before consulting an alias of the
#   same name, so `alias.stash` is silently ignored (measured, not assumed).
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
  SELF_SRC="${BASH_SOURCE[0]}"
elif [[ -d "$SCRIPT_DIR/hooks" ]]; then
  HOOK_SRC_DIR="$SCRIPT_DIR/hooks"          # installed layout: .git/f75-stash-guard/
  WT_STASH_SRC="$SCRIPT_DIR/wt-stash.sh"
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

# --- install -----------------------------------------------------------
if [[ "$MODE" != "verify-only" ]]; then
  mkdir -p "$HOOKS_DIR" "$GUARD_DIR/hooks" || die "cannot create $HOOKS_DIR"

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
if [[ "${stack_depth:-0}" -gt 0 && "$MODE" != "quiet" ]]; then
  warn "ready-f75 stash guard: the SHARED stash stack still holds $stack_depth entr$([[ "$stack_depth" == 1 ]] && echo y || echo ies)."
  warn "  No new entry can be pushed onto it any more, so it can only shrink."
  warn "  But 'git stash apply'/'pop'/'drop' against one of those entries CANNOT be"
  warn "  blocked by any git hook — apply/pop would dump another worktree's old"
  warn "  content into your tree, and drop removes an entry before any hook runs."
  warn "  Do not run raw 'git stash apply', 'pop' or 'drop' here; use 'git wtstash'."
  warn "  Clearing the backlog is owner-reserved data deletion: ready-bef."
fi

case "$MODE" in
  verbose)
    say "ready-f75 stash guard ACTIVE (verified: a guarded ref write was refused by the hook)"
    say "  hooks dir:  $HOOKS_DIR"
    for n in "${HOOK_NAMES[@]}"; do say "    $n"; done
    say "  git wtstash -> $GUARD_DIR/wt-stash.sh  (per-worktree, refs/worktree/wtstash/*)"
    say "  blocked before damage: git stash push | git stash clear"
    say "  NOT blockable by any git hook: git stash apply/pop (no ref transaction —"
    say "    warned loudly instead) and the reflog-rewrite half of drop/pop."
    say "    Per-verb measurements: docs/ops/shared-git-state-audit.md"
    ;;
  new-worktree)
    warn "ready-f75 stash guard is active in this repo: raw 'git stash' is refused"
    warn "  because refs/stash is shared across every worktree. Use 'git wtstash'"
    warn "  (push|list|pop|apply|drop|clear) or just commit WIP on your own branch."
    ;;
  verify-only)
    say "ready-f75 stash guard verified ACTIVE ($HOOKS_DIR)"
    ;;
esac

exit 0
