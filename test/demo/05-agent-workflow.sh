#!/usr/bin/env bash
# 05-agent-workflow.sh — Programmatic agent workflow (fully OFFLINE)
# An automated agent (CI bot, Claude session, automaton) drives its own board:
#   1. Queries the ready queue as JSON
#   2. Parses out an item id with no wrapper library
#   3. Claims the item
#   4. Posts incremental progress notes
#   5. Closes the item with a structured result
# Key differentiator from the human solo flow: --json throughout for machine parsing.
# Produces a real terminal transcript for documentation.
set -euo pipefail

RD="${RD:-/tmp/rd-demo}"
if [[ ! -x "$RD" ]]; then
    export PATH="$PATH:/usr/local/go/bin"
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    ( cd "$repo_root" && go build -o "$RD" ./cmd/rd )
fi

OUTPUT_DIR="$(cd "$(dirname "$0")" && pwd)/output"
OUTPUT_FILE="$OUTPUT_DIR/05-agent-workflow.txt"
mkdir -p "$OUTPUT_DIR"

# Isolated environment: throwaway project dir + dedicated rd-home for the identity.
PROJECT=$(mktemp -d /tmp/rdtest-agent-proj-XXXX)
export RD_HOME=$(mktemp -d /tmp/rdtest-agent-home-XXXX)
trap 'rm -rf "$PROJECT" "$RD_HOME"' EXIT

# Tee all output to the transcript file
exec > >(tee "$OUTPUT_FILE") 2>&1

cd "$PROJECT"

echo "=== SECTION: setup ==="
echo "$ cd ci-project && rd init --name ci-project"
"$RD" init --name ci-project

echo ""
echo "=== SECTION: create-work ==="
echo '$ rd create "Reindex search corpus" --type task --priority p1'
ITEM1_ID=$("$RD" create "Reindex search corpus" --type task --priority p1)
echo "# created: $ITEM1_ID"
echo ""
echo '$ rd create "Update dependency manifest" --type task --priority p2'
ITEM2_ID=$("$RD" create "Update dependency manifest" --type task --priority p2)
echo "# created: $ITEM2_ID"

echo ""
echo "=== SECTION: agent-query ==="
echo "# Agent queries its ready queue as JSON and projects the fields it needs —"
echo "# no wrapper library, just a one-line parse over the machine-readable output."
echo "$ rd ready --json | python3 -c '<project id, title, priority, status>'"
"$RD" ready --json | python3 -c "
import sys, json
for i in json.load(sys.stdin):
    print(json.dumps({k: i[k] for k in ('id', 'title', 'priority', 'status')}))
"
AGENT_ITEM_ID=$("$RD" ready --json | python3 -c "
import sys, json
items = json.load(sys.stdin)
print(items[0]['id'] if items else '')
")
echo "# agent selected item: $AGENT_ITEM_ID"

echo ""
echo "=== SECTION: agent-claim ==="
echo "$ rd claim $AGENT_ITEM_ID --reason \"Starting batch reindex job\""
"$RD" claim "$AGENT_ITEM_ID" --reason "Starting batch reindex job"
echo ""
echo "# Confirm the item is now active."
echo "$ rd ready --view work --json | python3 -c '<project id, status>'"
"$RD" ready --view work --json | python3 -c "
import sys, json
for i in json.load(sys.stdin):
    print(json.dumps({k: i[k] for k in ('id', 'status')}))
"

echo ""
echo "=== SECTION: agent-progress ==="
echo "$ rd progress $AGENT_ITEM_ID --notes \"Processed 47/142 records, 0 errors\""
"$RD" progress "$AGENT_ITEM_ID" --notes "Processed 47/142 records, 0 errors"
echo ""
echo "$ rd progress $AGENT_ITEM_ID --notes \"Processed 142/142 records, 0 errors — indexing complete\""
"$RD" progress "$AGENT_ITEM_ID" --notes "Processed 142/142 records, 0 errors — indexing complete"

echo ""
echo "=== SECTION: agent-done ==="
echo "$ rd done $AGENT_ITEM_ID --reason \"Batch complete: 142 records processed, 0 errors\""
"$RD" done "$AGENT_ITEM_ID" --reason "Batch complete: 142 records processed, 0 errors"

echo ""
echo "=== SECTION: verify ==="
echo "# Owner queries all items as JSON and projects a status summary."
echo "$ rd list --all --json | python3 -c '<project id, title, status>'"
"$RD" list --all --json | python3 -c "
import sys, json
for i in json.load(sys.stdin):
    print(json.dumps({k: i[k] for k in ('id', 'title', 'status')}))
"
echo ""
echo "$ rd list --all"
"$RD" list --all

echo ""
echo "# Demo complete. Transcript written to: $OUTPUT_FILE"
