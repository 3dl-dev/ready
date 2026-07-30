#!/usr/bin/env bash
# install-git-stash-guard.sh — activates the ready-f75 stash guard for the
# repository this script is run from.
#
# Run once per clone, from anywhere inside the repo (main checkout or any
# worktree). It installs TWO things, both required (see scripts/wt-stash.sh
# and scripts/git-hooks/reference-transaction for the full why):
#
#   1. A `reference-transaction` hook, copied into the repo's COMMON
#      .git/hooks/ directory, that ABORTS any attempt to write refs/stash
#      (i.e. blocks git stash push/pop/apply/drop/clear outright) with a
#      message pointing at the replacement below. This is the actual
#      enforcement: aliasing `git stash` itself does NOT work (git resolves
#      its own builtins before consulting an alias of the same name — the
#      alias is silently ignored), so a hook is the only thing that can make
#      raw `git stash` fail loudly instead of silently corrupting a
#      sibling's stack.
#
#   2. scripts/wt-stash.sh, copied into that same COMMON .git directory
#      (again: must be reachable regardless of which commit/branch any
#      given worktree has checked out) and wired up as `git wtstash` via a
#      NEW alias name (aliasing works fine for names that aren't existing
#      git builtins). `git wtstash [push|list|pop|apply|drop|clear]` is the
#      safe, worktree-scoped replacement for `git stash`.
#
# Because git config and .git/hooks (without extensions.worktreeConfig) are
# common to every worktree, this one run protects every worktree of this
# clone immediately — past, present, and any created afterward.
#
# Idempotent: safe to re-run. If a reference-transaction hook already exists
# and isn't this one, it is left in place and the script fails rather than
# silently discarding someone else's hook.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
STASH_SRC="$SCRIPT_DIR/wt-stash.sh"
HOOK_SRC="$SCRIPT_DIR/git-hooks/reference-transaction"
HOOK_MARKER="ready-f75 guard"

for f in "$STASH_SRC" "$HOOK_SRC"; do
  if [[ ! -f "$f" ]]; then
    echo "install-git-stash-guard.sh: missing $f" >&2
    exit 1
  fi
done

COMMON_DIR="$(git rev-parse --git-common-dir)"
case "$COMMON_DIR" in
  /*) : ;;
  *) COMMON_DIR="$(pwd)/$COMMON_DIR" ;;
esac

HOOKS_DIR="$COMMON_DIR/hooks"
mkdir -p "$HOOKS_DIR"
HOOK_DEST="$HOOKS_DIR/reference-transaction"
if [[ -f "$HOOK_DEST" ]] && ! grep -q "$HOOK_MARKER" "$HOOK_DEST"; then
  echo "install-git-stash-guard.sh: $HOOK_DEST already exists and isn't the ready-f75 guard — not overwriting. Merge it by hand." >&2
  exit 1
fi
cp "$HOOK_SRC" "$HOOK_DEST"
chmod +x "$HOOK_DEST"

STASH_DEST="$COMMON_DIR/wt-stash-guard.sh"
cp "$STASH_SRC" "$STASH_DEST"
chmod +x "$STASH_DEST"

git config --local alias.wtstash "!f() { \"$STASH_DEST\" \"\$@\"; }; f"

echo "installed ready-f75 stash guard:"
echo "  - $HOOK_DEST blocks raw git stash (push/pop/apply/drop/clear)"
echo "  - git wtstash [push|list|pop|apply|drop|clear] -> $STASH_DEST (per-worktree via refs/worktree/wtstash/*)"
