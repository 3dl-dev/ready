#!/usr/bin/env bash
# install-git-stash-guard.sh — activate the ready-f75 stash guard for the
# repository this script is run from (main checkout or any linked worktree;
# hooks and non-worktree config are common to the whole clone, so one run
# covers every worktree, past and future).
#
# WHAT IT INSTALLS
#   1. hooks/reference-transaction — aborts ref transactions that write
#      refs/stash.
#   2. hooks/post-index-change — warns loudly on stderr when a stash apply or
#      pop has just rewritten the working tree.
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
# THIS SCRIPT MAKES NO COVERAGE CLAIM, AND NEITHER DOES ANYTHING IT INSTALLS.
# Three review rounds of ready-f75 went on a per-verb claim written in prose —
# a false one, then a blacklist of its phrasings, then a whitelist of claim
# words — each defeated by rewording. The claim has been deleted rather than
# maintained. "Installed" here means "the hook was measured to fire" (see
# SELF-VERIFICATION below) and NOTHING MORE. It does not mean an agent is safe.
#
# What the guard stops is stated where it is executable, in
# scripts/wt_stash_test.go:
#
#   go test ./scripts/ -run TestStashGuard -v
#
# The two mechanisms are measured there SEPARATELY — the hooks with no shim on
# PATH, the shim with no hooks installed — because an earlier round measured
# them together and a fail-open shim passed on the hooks' behaviour.
#
# The PATH shim is materialised by this script but cannot be activated by it;
# PATH belongs to the process that spawns an agent. The one line is:
#
#   export PATH="$(git rev-parse --git-common-dir)/f75-stash-guard/bin:$PATH"
#
# (The installer prints the resolved form and reports whether it is on PATH.
# Emptying the pre-existing stash backlog, which would moot part of this, is
# owner-reserved data deletion: ready-bef.)
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
#   "zero-step activation on clone" is not achievable and is not claimed.
#   Hooks live in shared state, so one run reaches every worktree of the clone
#   (TestStashGuard_WorktreeAddReAssertsGuard exercises that path).
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
# A non-empty shared stack is the standing hazard: every entry on it belongs
# to some worktree, and any worktree can consume it. Emptying the stack is
# data deletion and is owner-reserved (ready-bef), so this reports and does
# not act. It reports the DEPTH — a fact — and points at the tests; it does
# not tell the operator which verbs are safe.
stack_depth="$(git stash list 2>/dev/null | wc -l | tr -d ' ')"
shim_on_path=no
if command -v git >/dev/null 2>&1 && [[ "$(command -v git)" == "$SHIM_DIR/git" ]]; then
  shim_on_path=yes
fi

if [[ "${stack_depth:-0}" -gt 0 && "$MODE" != "quiet" ]]; then
  warn "ready-f75 stash guard: the SHARED stash stack still holds $stack_depth entr$([[ "$stack_depth" == 1 ]] && echo y || echo ies)."
  warn "  Those entries belong to whichever worktree created them, and any worktree"
  warn "  can consume them. Installing this guard does NOT make raw 'git stash' safe"
  warn "  against them, and this script will not tell you which verbs it stops —"
  warn "  that has been wrong in writing three times. Measure it instead:"
  warn "    go test ./scripts/ -run TestStashGuard -v"
  warn "  Use 'git wtstash' (per-worktree) or commit WIP on your own branch."
  if [[ "$shim_on_path" == yes ]]; then
    warn "  The PATH shim IS on PATH in this shell."
  else
    warn "  The PATH shim is NOT active. To activate it for this shell:"
    warn "    export PATH=\"$SHIM_DIR:\$PATH\""
  fi
  warn "  Emptying the backlog is owner-reserved data deletion: ready-bef."
fi

case "$MODE" in
  verbose)
    say "ready-f75 stash guard INSTALLED (verified: a guarded ref write was refused by the hook)"
    say "  hooks dir:  $HOOKS_DIR"
    for n in "${HOOK_NAMES[@]}"; do say "    $n"; done
    say "  git wtstash -> $GUARD_DIR/wt-stash.sh  (per-worktree, refs/worktree/wtstash/*)"
    say "  shared stack depth: ${stack_depth:-0}"
    say "  PATH shim: materialised at $SHIM_DIR"
    say "             This script cannot put it on PATH for you; PATH belongs to"
    say "             the process that spawns an agent. Currently on PATH: $shim_on_path"
    say "             export PATH=\"$SHIM_DIR:\$PATH\""
    say ""
    say "  This script does not state what the guard stops. That claim was wrong"
    say "  in writing three times, so it was deleted rather than maintained. The"
    say "  scope is executable and lives in scripts/wt_stash_test.go:"
    say "    go test ./scripts/ -run TestStashGuard -v"
    ;;
  new-worktree)
    warn "ready-f75 stash guard is installed in this repo, because refs/stash is"
    warn "  SHARED across every worktree of a clone. Use 'git wtstash'"
    warn "  (push|list|pop|apply|drop|clear) or commit WIP on your own branch."
    warn "  Do not assume raw 'git stash' is safe here — run"
    warn "  'go test ./scripts/ -run TestStashGuard -v' to see what is stopped."
    warn "  For the strongest form: export PATH=\"$SHIM_DIR:\$PATH\""
    ;;
  verify-only)
    say "ready-f75 stash guard hooks verified to fire ($HOOKS_DIR)"
    ;;
esac

exit 0
