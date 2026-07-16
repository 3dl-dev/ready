#!/usr/bin/env bash
# 06-gate-escalation.sh — Gate / human-escalation workflow (fully OFFLINE)
# A worker hits a decision point and gates an item for human review. The human
# sees pending gates, then either approves (item returns to active) or rejects
# (item stays in waiting until the approach is revised). All state lives in the
# local signed-event log — no server, no network required.
# Produces a real terminal transcript for documentation.
set -euo pipefail

RD="${RD:-/tmp/rd-demo}"
if [[ ! -x "$RD" ]]; then
    export PATH="$PATH:/usr/local/go/bin"
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    ( cd "$repo_root" && go build -o "$RD" ./cmd/rd )
fi

OUTPUT_DIR="$(cd "$(dirname "$0")" && pwd)/output"
OUTPUT_FILE="$OUTPUT_DIR/06-gate-escalation.txt"
mkdir -p "$OUTPUT_DIR"

# Isolated environment: throwaway project dir + dedicated rd-home for the identity.
PROJECT=$(mktemp -d /tmp/rdtest-gate-proj-XXXX)
export RD_HOME=$(mktemp -d /tmp/rdtest-gate-home-XXXX)
trap 'rm -rf "$PROJECT" "$RD_HOME"' EXIT

# Tee all output to the transcript file
exec > >(tee "$OUTPUT_FILE") 2>&1

cd "$PROJECT"

echo "=== SECTION: setup ==="
echo "$ cd gate-demo && rd init --name gate-demo"
"$RD" init --name gate-demo

echo ""
echo "=== SECTION: claim-work ==="
echo '$ rd create "Migrate auth layer to new token format" --type task --priority p1'
ITEM_ID=$("$RD" create "Migrate auth layer to new token format" --type task --priority p1)
echo "# created: $ITEM_ID"
echo ""
echo "$ rd claim $ITEM_ID"
"$RD" claim "$ITEM_ID"

echo ""
echo "=== SECTION: gate ==="
echo "# Worker hits a decision point: two viable approaches, needs direction."
echo "# Gate type 'design' signals an architectural decision is required."
echo "$ rd gate $ITEM_ID --gate-type design --description \"...\""
"$RD" gate "$ITEM_ID" --gate-type design \
    --description "Two viable approaches: option A saves 2ms but breaks caching, option B is safe. Need direction."
echo ""
echo "$ rd show $ITEM_ID   (item is now waiting on the gate)"
"$RD" show "$ITEM_ID"

echo ""
echo "=== SECTION: human-sees-gate ==="
echo "# Human runs 'rd gates' to see pending escalations."
echo "$ rd gates"
"$RD" gates

echo ""
echo "=== SECTION: human-approves ==="
echo "# Human reviews and approves — go with option B (safe approach)."
echo "$ rd approve $ITEM_ID --reason \"Use option B. Safety over 2ms gain.\""
"$RD" approve "$ITEM_ID" --reason "Use option B. Safety over 2ms gain."
echo ""
echo "$ rd gates   (no pending gates)"
"$RD" gates

echo ""
echo "=== SECTION: done ==="
echo "# Item returned to active — worker finishes it."
echo "$ rd done $ITEM_ID --reason \"Auth layer migrated using option B\""
"$RD" done "$ITEM_ID" --reason "Auth layer migrated using option B"

echo ""
echo "=== SECTION: reject-scenario ==="
echo "# Second scenario: worker gates a new item, the human rejects it."
echo "# After rejection the item stays in waiting until the approach is revised."
echo '$ rd create "Refactor payment processor" --type task --priority p2'
ITEM2_ID=$("$RD" create "Refactor payment processor" --type task --priority p2)
echo "# created: $ITEM2_ID"
echo ""
echo "$ rd claim $ITEM2_ID"
"$RD" claim "$ITEM2_ID"
echo ""
echo "$ rd gate $ITEM2_ID --gate-type scope --description \"...\""
"$RD" gate "$ITEM2_ID" --gate-type scope \
    --description "Scope too broad — touches 6 modules. Needs decomposition or explicit sign-off to proceed."
echo ""
echo "$ rd gates"
"$RD" gates
echo ""
echo "$ rd reject $ITEM2_ID --reason \"Split into smaller items first. One module per item.\""
"$RD" reject "$ITEM2_ID" --reason "Split into smaller items first. One module per item."
echo ""
echo "$ rd show $ITEM2_ID   (item stays waiting after rejection — gate unresolved)"
"$RD" show "$ITEM2_ID"
echo ""
echo "$ rd gates   (rejected item still listed)"
"$RD" gates

echo ""
echo "# Demo complete. Transcript written to: $OUTPUT_FILE"
