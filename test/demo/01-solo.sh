#!/usr/bin/env bash
# 01-solo.sh — Solo developer demo: init → create → ready → claim → progress → done
# Nostr-native: 'rd init' mints a local secp256k1 identity and stores work items
# as signed events in .ready/nostr-log.jsonl (the source of truth). Everything
# below runs fully OFFLINE — relays are a replaceable cache, never required.
# Produces a real terminal transcript for documentation.
set -euo pipefail

RD="${RD:-/tmp/rd-demo}"
if [[ ! -x "$RD" ]]; then
    export PATH="$PATH:/usr/local/go/bin"
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    ( cd "$repo_root" && go build -o "$RD" ./cmd/rd )
fi

OUTPUT_DIR="$(cd "$(dirname "$0")" && pwd)/output"
OUTPUT_FILE="$OUTPUT_DIR/01-solo.txt"
mkdir -p "$OUTPUT_DIR"

# Isolated environment: a throwaway project directory plus a dedicated rd-home
# for the nostr identity, so the demo never touches the real ~/.config/rd.
PROJECT=$(mktemp -d /tmp/rdtest-solo-proj-XXXX)
export RD_HOME=$(mktemp -d /tmp/rdtest-solo-home-XXXX)
trap 'rm -rf "$PROJECT" "$RD_HOME"' EXIT

# Tee all output to the transcript file
exec > >(tee "$OUTPUT_FILE") 2>&1

# From now on: cd into the project and run — walk-up finds .ready/
cd "$PROJECT"

echo "=== SECTION: init ==="
echo "$ cd myproject && rd init --name \"myproject\""
"$RD" init --name "myproject"

echo ""
echo "=== SECTION: create ==="
echo '$ rd create "Ship login page" --priority p1 --type task'
ITEM_ID=$("$RD" create "Ship login page" --priority p1 --type task)
echo "# item ID: $ITEM_ID"

echo ""
echo "=== SECTION: ready ==="
echo "$ rd ready"
"$RD" ready

echo ""
echo "=== SECTION: claim ==="
echo "$ rd claim $ITEM_ID"
"$RD" claim "$ITEM_ID"

echo ""
echo "=== SECTION: progress ==="
echo "$ rd progress $ITEM_ID --notes \"Wired up auth middleware\""
"$RD" progress "$ITEM_ID" --notes "Wired up auth middleware"

echo ""
echo "=== SECTION: done ==="
echo "$ rd done $ITEM_ID --reason \"Login page ships with JWT auth\""
"$RD" done "$ITEM_ID" --reason "Login page ships with JWT auth"

echo ""
echo "=== SECTION: verify ==="
echo "$ rd list --all"
"$RD" list --all

echo ""
echo "# Demo complete. Transcript written to: $OUTPUT_FILE"
