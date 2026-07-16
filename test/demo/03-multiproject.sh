#!/usr/bin/env bash
# 03-multiproject.sh — One identity, two projects, per-board scoping (fully OFFLINE)
# A single developer identity (one rd-home) spans two separate project trees. Each
# 'rd init' mints its own board; each project keeps its own local signed-event log.
# 'rd' walk-up resolves the project from the current directory via .ready/, so the
# same command produces board-scoped results depending on where you run it.
# Produces a real terminal transcript for documentation.
set -euo pipefail

RD="${RD:-/tmp/rd-demo}"
if [[ ! -x "$RD" ]]; then
    export PATH="$PATH:/usr/local/go/bin"
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    ( cd "$repo_root" && go build -o "$RD" ./cmd/rd )
fi

OUTPUT_DIR="$(cd "$(dirname "$0")" && pwd)/output"
OUTPUT_FILE="$OUTPUT_DIR/03-multiproject.txt"
mkdir -p "$OUTPUT_DIR"

# One identity (one rd-home), two independent project directories.
export RD_HOME=$(mktemp -d /tmp/rdtest-multi-home-XXXX)
BACKEND=$(mktemp -d /tmp/rdtest-backend-XXXX)
FRONTEND=$(mktemp -d /tmp/rdtest-frontend-XXXX)
trap 'rm -rf "$RD_HOME" "$BACKEND" "$FRONTEND"' EXIT

# Tee all output to the transcript file
exec > >(tee "$OUTPUT_FILE") 2>&1

echo "=== SECTION: init-backend ==="
echo "$ cd backend && rd init --name \"backend\""
cd "$BACKEND"
"$RD" init --name "backend"

echo ""
echo "=== SECTION: init-frontend ==="
echo "$ cd frontend && rd init --name \"frontend\""
cd "$FRONTEND"
"$RD" init --name "frontend"

echo ""
echo "=== SECTION: create-items ==="
echo '$ cd backend && rd create "Expose /api/v1/users endpoint" --priority p1 --type task'
cd "$BACKEND"
BACKEND_ID=$("$RD" create "Expose /api/v1/users endpoint" --priority p1 --type task)
echo "# backend item ID: $BACKEND_ID"

echo ""
echo '$ cd frontend && rd create "Build user list page" --priority p1 --type task'
cd "$FRONTEND"
FRONTEND_ID=$("$RD" create "Build user list page" --priority p1 --type task)
echo "# frontend item ID: $FRONTEND_ID"

echo ""
echo "=== SECTION: scope-backend ==="
echo "# Same identity, same command — but run from the backend tree, walk-up"
echo "# resolves the backend board and 'rd ready' shows only the backend item."
echo "$ cd backend && rd ready"
cd "$BACKEND"
"$RD" ready

echo ""
echo "=== SECTION: scope-frontend ==="
echo "# Run from the frontend tree, walk-up resolves the frontend board."
echo "$ cd frontend && rd ready"
cd "$FRONTEND"
"$RD" ready

echo ""
echo "=== SECTION: verify ==="
echo "# Each board is fully independent. 'rd list --all' is board-scoped too."
echo "$ cd backend && rd list --all"
cd "$BACKEND"
"$RD" list --all
echo ""
echo "$ cd frontend && rd list --all"
cd "$FRONTEND"
"$RD" list --all

echo ""
echo "# Demo complete. Transcript written to: $OUTPUT_FILE"
