#!/usr/bin/env bash
# 02-team.sh — Two-identity team workflow (self-mint invite model, fully OFFLINE)
# Owner initializes a project and mints a one-use invite token. The token carries
# NO key — only the board coordinate, relays, a TTL and a claim-nonce. The joiner
# self-mints its own secp256k1 key with 'rd join', pins the board READ-ONLY, and
# reports back a pubkey + claim-nonce. The owner then publishes an owner-signed
# kind-39301 role-grant that admits the joiner as a contributor.
# Produces a real terminal transcript for documentation.
set -euo pipefail

RD="${RD:-/tmp/rd-demo}"
if [[ ! -x "$RD" ]]; then
    export PATH="$PATH:/usr/local/go/bin"
    repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
    ( cd "$repo_root" && go build -o "$RD" ./cmd/rd )
fi

OUTPUT_DIR="$(cd "$(dirname "$0")" && pwd)/output"
OUTPUT_FILE="$OUTPUT_DIR/02-team.txt"
mkdir -p "$OUTPUT_DIR"

# Two identities = two rd-homes. Two separate project trees (each identity keeps
# its own local signed-event log). Everything is isolated under /tmp.
OWNER_PROJ=$(mktemp -d /tmp/rdtest-team-owner-XXXX)
OWNER_HOME=$(mktemp -d /tmp/rdtest-team-ohome-XXXX)
JOIN_PROJ=$(mktemp -d /tmp/rdtest-team-join-XXXX)
JOIN_HOME=$(mktemp -d /tmp/rdtest-team-jhome-XXXX)
trap 'rm -rf "$OWNER_PROJ" "$OWNER_HOME" "$JOIN_PROJ" "$JOIN_HOME"' EXIT

# Tee all output to the transcript file
exec > >(tee "$OUTPUT_FILE") 2>&1

echo "=== SECTION: owner-init ==="
echo "$ cd teamproject && rd init --name teamproject   (owner)"
( cd "$OWNER_PROJ" && "$RD" --rd-home "$OWNER_HOME" init --name teamproject )

echo ""
echo "=== SECTION: owner-invite ==="
echo "# Owner mints a one-use invite token (no key inside — TTL-bounded)."
echo "$ rd invite"
INVITE_OUT=$(cd "$OWNER_PROJ" && "$RD" --rd-home "$OWNER_HOME" invite 2>&1)
TOKEN=$(echo "$INVITE_OUT" | grep -oE 'rd1_[A-Za-z0-9_-]+' | head -1)
echo "rd1_...  (invite token — treat as secret; carries no private key)"

echo ""
echo "=== SECTION: joiner-join ==="
echo "# Joiner runs 'rd join' in a FRESH rd-home + project dir. It self-mints its"
echo "# own key, pins the board READ-ONLY, and prints a pubkey + claim-nonce."
echo "$ rd join <invite-token>"
JOIN_OUT=$(cd "$JOIN_PROJ" && RD_HOME="$JOIN_HOME" "$RD" join "$TOKEN" 2>&1)
echo "$JOIN_OUT"
PUBKEY=$(echo "$JOIN_OUT" | grep -oE 'pubkey=[0-9a-f]+' | cut -d= -f2)
CLAIM=$(echo "$JOIN_OUT" | grep -oE 'claim=[0-9a-f]+' | cut -d= -f2)

echo ""
echo "=== SECTION: owner-grant ==="
echo "# Owner grants write access, consuming the joiner's one-use claim-nonce."
echo "$ rd grant <joiner-pubkey> contributor --claim <claim-nonce>"
( cd "$OWNER_PROJ" && "$RD" --rd-home "$OWNER_HOME" grant "$PUBKEY" contributor --claim "$CLAIM" )

echo ""
echo "=== SECTION: verify-grant ==="
echo "# The owner-signed grant landed — the joiner is now an admitted contributor."
echo "$ rd sessions"
( cd "$OWNER_PROJ" && "$RD" --rd-home "$OWNER_HOME" sessions )

echo ""
echo "# Demo complete. Transcript written to: $OUTPUT_FILE"
