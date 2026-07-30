#!/usr/bin/env bash
# wt-stash.sh — a worktree-scoped replacement for `git stash`.
#
# WHY THIS EXISTS (ready-f75): `git stash` stores every entry on a single
# ref, refs/stash, which lives in the repository's COMMON .git directory —
# the one thing every `git worktree` shares. Two agents dispatched into
# separate worktrees of the same repo who both run `git stash` are pushing
# onto the SAME stack. Their entries interleave, and popping by position
# ("pop the top entry") can restore or discard the WRONG agent's work with
# no error of any kind. Observed 2026-07-29: 14+ interleaved entries from 10
# branches on refs/stash, one agent's stash pop silently reverting a
# sibling's uncommitted files to HEAD.
#
# THE FIX: git has a ref namespace that is NOT shared across worktrees —
# refs/worktree/<name> — physically stored per-worktree (.git/refs/worktree/
# for the main worktree, .git/worktrees/<id>/refs/worktree/ for linked ones;
# verified empirically, not just from docs, before writing this). This
# script reimplements push/list/pop/apply/drop/clear on top of that
# namespace (refs/worktree/wtstash/N) instead of refs/stash, using the
# officially-documented low-level plumbing `git stash create`
# (produces a stash-shaped commit without touching any ref) and
# `git stash apply <commit>` (accepts any commit that looks like a stash
# entry, not just one from refs/stash). Two worktrees can now push and pop
# concurrently with zero shared mutable state — each one's stack lives in a
# physically different file, so there is no interleaving to race on.
#
# This intentionally does NOT support `-u/--include-untracked`: `git stash
# create` doesn't carry it, and adding it back would require re-deriving the
# untracked-inclusion machinery `git stash push` keeps internal. No observed
# use of it in the hazard this fixes (bisecting agents saving tracked WIP).
# If a real need shows up, it's a follow-up, not silent scope creep here.
#
# Installed via scripts/install-git-stash-guard.sh, which wires this script
# up as `git wtstash` (push/list/pop/apply/drop/clear) — a NEW subcommand
# name, not an override of `git stash` itself. Aliasing `stash` directly
# does not work: git resolves its own builtins (stash, status, log, ...)
# before consulting an alias of the same name, so such an alias is silently
# ignored (verified empirically). The installer's reference-transaction hook
# is what makes raw `git stash` PUSH fail loudly instead; raw
# `git stash apply`/`pop` cannot be blocked by any git hook at all and get a
# loud post-index-change warning instead. Exact coverage, with the
# measurements behind it, is in scripts/git-hooks/reference-transaction and
# docs/ops/shared-git-state-audit.md.
set -euo pipefail

REF_PREFIX="refs/worktree/wtstash"

# This script is the SAFE path: it calls `git stash create` (writes no ref)
# and `git stash apply <sha>` against its own per-worktree refs. Tell the
# guard's post-index-change hook not to warn about those.
export RD_STASH_GUARD_INTERNAL=1

_die() {
  echo "wt-stash: $*" >&2
  exit 1
}

# Prints existing slot indices under $REF_PREFIX, ascending, one per line.
_all_indices() {
  git for-each-ref --format='%(refname)' "$REF_PREFIX/" 2>/dev/null \
    | sed -n "s#^${REF_PREFIX}/##p" \
    | sort -n
}

# Replaces the whole stack with the given SHAs in order (arg 1 -> index 0).
_rewrite_stack() {
  local existing i
  existing=$(_all_indices)
  for i in $existing; do
    git update-ref -d "$REF_PREFIX/$i"
  done
  i=0
  for sha in "$@"; do
    git update-ref "$REF_PREFIX/$i" "$sha"
    i=$((i + 1))
  done
}

# Accepts a bare index ("0") or git-stash-style ("stash@{0}") and echoes the
# bare index, defaulting to 0.
_parse_index() {
  local raw="${1:-0}"
  raw="${raw#stash@\{}"
  raw="${raw%\}}"
  [[ -z "$raw" ]] && raw=0
  echo "$raw"
}

cmd_push() {
  local msg=""
  if [[ $# -gt 0 ]]; then
    # Accept `-m msg` or a bare trailing message, mirroring common usage.
    if [[ "$1" == "-m" ]]; then
      shift
      msg="$*"
    else
      msg="$*"
    fi
  fi

  if git diff --quiet HEAD -- 2>/dev/null && git diff --cached --quiet -- 2>/dev/null; then
    echo "No local changes to save"
    return 0
  fi

  local sha
  if [[ -n "$msg" ]]; then
    sha=$(git stash create "$msg")
  else
    sha=$(git stash create)
  fi

  if [[ -z "$sha" ]]; then
    echo "No local changes to save"
    return 0
  fi

  local existing i new_shas
  new_shas=("$sha")
  existing=$(_all_indices)
  for i in $existing; do
    new_shas+=("$(git rev-parse "$REF_PREFIX/$i")")
  done
  _rewrite_stack "${new_shas[@]}"

  git reset --hard -q HEAD

  local subject
  subject=$(git log -1 --format=%s "$sha")
  echo "Saved worktree-scoped stash: $subject"
}

cmd_list() {
  local existing i subject
  existing=$(_all_indices)
  for i in $existing; do
    subject=$(git log -1 --format=%s "$REF_PREFIX/$i")
    echo "stash@{$i}: $subject"
  done
}

_apply_or_pop() {
  local idx sha drop="$1"
  idx=$(_parse_index "${2:-}")
  sha=$(git rev-parse --verify -q "$REF_PREFIX/$idx") || _die "no worktree-scoped stash entry at index $idx (run 'git stash list')"

  if git stash apply --index --quiet "$sha"; then
    if [[ "$drop" == true ]]; then
      cmd_drop "$idx"
    fi
  else
    echo "wt-stash: apply had conflicts; entry kept at stash@{$idx}" >&2
    return 1
  fi
}

cmd_apply() { _apply_or_pop false "${1:-}"; }
cmd_pop() { _apply_or_pop true "${1:-}"; }

cmd_drop() {
  local idx existing i new_shas=()
  idx=$(_parse_index "${1:-}")
  existing=$(_all_indices)
  for i in $existing; do
    [[ "$i" == "$idx" ]] && continue
    new_shas+=("$(git rev-parse "$REF_PREFIX/$i")")
  done
  if [[ ${#new_shas[@]} -eq 0 ]]; then
    _rewrite_stack
  else
    _rewrite_stack "${new_shas[@]}"
  fi
}

cmd_clear() {
  local existing i
  existing=$(_all_indices)
  for i in $existing; do
    git update-ref -d "$REF_PREFIX/$i"
  done
}

main() {
  local cmd="${1:-push}"
  # `git stash -m msg` (no explicit subcommand) is valid git and defaults to
  # push, so `git wtstash -m msg` must too. Without this, an option in first
  # position was reported as an "unsupported subcommand" and the caller's
  # changes were left unstashed — found probing the installed guard for real.
  if [[ "$cmd" == -* ]]; then
    cmd_push "$@"
    return
  fi
  [[ $# -gt 0 ]] && shift
  case "$cmd" in
    push) cmd_push "$@" ;;
    list) cmd_list "$@" ;;
    pop) cmd_pop "$@" ;;
    apply) cmd_apply "$@" ;;
    drop) cmd_drop "$@" ;;
    clear) cmd_clear "$@" ;;
    *) _die "unsupported subcommand '$cmd' (supported: push, list, pop, apply, drop, clear)" ;;
  esac
}

main "$@"
