#!/usr/bin/env bash
# relay-demo.sh — LIVE ground-source proof for ready-efe.
#
# Proves the self-hosted strfry relay topology works end-to-end against LIVE
# relays (NO MOCKS). It:
#   1. Publishes a SIGNED nostr test event to relay-a AND relay-b.
#   2. Reads it back by event id from EACH relay.
#   3. Takes relay-a offline, proves read-back still succeeds from relay-b;
#      restores; repeats taking relay-b offline.
#   4. Publishes/reads a NIP-65 kind:10002 relay-list (outbox) event.
#   5. Demonstrates `strfry sync` reconciling an event between the two relays
#      via NIP-77 Negentropy (publish to A only, sync into B, read from B).
#
# The relay is a CACHE / always-available copy, NEVER the source of truth.
#
# Requires: nak (github.com/fiatjaf/nak) on PATH; ssh access to the relay VMs
# so the script can stop/start the strfry systemd service for the failover
# proof. Endpoints are read from pkg/rdconfig defaults but can be overridden
# via env vars below.
#
# Usage: scripts/relay-demo.sh
set -euo pipefail

# ---- Config (overridable via env) -------------------------------------------
RELAY_A_HOST="${RELAY_A_HOST:-192.168.2.40}"
RELAY_B_HOST="${RELAY_B_HOST:-192.168.2.41}"
RELAY_A_URL="${RELAY_A_URL:-ws://${RELAY_A_HOST}:7777}"
RELAY_B_URL="${RELAY_B_URL:-ws://${RELAY_B_HOST}:7777}"
# SSH user for stopping/starting the strfry service on each relay VM.
RELAY_SSH_USER="${RELAY_SSH_USER:-baron}"
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=8"

NAK="${NAK:-nak}"

pass() { printf '\033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$1"; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$1"; }

command -v "$NAK" >/dev/null 2>&1 || fail "nak not found on PATH (go install github.com/fiatjaf/nak@latest)"

svc() { # svc <host> <start|stop|restart>
  ssh $SSH_OPTS "${RELAY_SSH_USER}@$1" "sudo systemctl $2 strfry" ;
}

# read_back <relay-url> <event-id> -> prints the id if found, empty otherwise
read_back() {
  "$NAK" req -i "$2" "$1" 2>/dev/null | jq -r 'select(.id=="'"$2"'") | .id' 2>/dev/null | head -1
}

# ---- Throwaway test keypair (real identity handling is ready-41d) -----------
SK="$("$NAK" key generate)"
PK="$("$NAK" key public "$SK")"
info "test pubkey: $PK"

echo
info "STEP 1: publish a SIGNED event to relay-a AND relay-b"
NONCE="ready-efe-$(date +%s)-$RANDOM"
EVENT_JSON="$("$NAK" event --sec "$SK" -k 1 -c "$NONCE" "$RELAY_A_URL" "$RELAY_B_URL" 2>/dev/null)"
EVENT_ID="$(printf '%s' "$EVENT_JSON" | jq -r '.id' | head -1)"
[ -n "$EVENT_ID" ] && [ "$EVENT_ID" != "null" ] || fail "no event id produced"
info "published event id: $EVENT_ID"

echo
info "STEP 2: read the event back by id from EACH relay"
sleep 1
GOT_A="$(read_back "$RELAY_A_URL" "$EVENT_ID")"
GOT_B="$(read_back "$RELAY_B_URL" "$EVENT_ID")"
[ "$GOT_A" = "$EVENT_ID" ] || fail "relay-a did not return event $EVENT_ID"
[ "$GOT_B" = "$EVENT_ID" ] || fail "relay-b did not return event $EVENT_ID"
pass "event $EVENT_ID readable from BOTH relays"

echo
info "STEP 3a: take relay-a OFFLINE, prove read-back still works from relay-b"
svc "$RELAY_A_HOST" stop
sleep 2
# relay-a must be unreachable now
if read_back "$RELAY_A_URL" "$EVENT_ID" | grep -q "$EVENT_ID"; then
  fail "relay-a still answering after stop — offline proof invalid"
fi
GOT_B_FAILOVER="$(read_back "$RELAY_B_URL" "$EVENT_ID")"
[ "$GOT_B_FAILOVER" = "$EVENT_ID" ] || fail "relay-b failover read failed while relay-a down"
pass "relay-a DOWN: event still served by relay-b"
svc "$RELAY_A_HOST" start
sleep 2

echo
info "STEP 3b: take relay-b OFFLINE, prove read-back still works from relay-a"
svc "$RELAY_B_HOST" stop
sleep 2
if read_back "$RELAY_B_URL" "$EVENT_ID" | grep -q "$EVENT_ID"; then
  fail "relay-b still answering after stop — offline proof invalid"
fi
GOT_A_FAILOVER="$(read_back "$RELAY_A_URL" "$EVENT_ID")"
[ "$GOT_A_FAILOVER" = "$EVENT_ID" ] || fail "relay-a failover read failed while relay-b down"
pass "relay-b DOWN: event still served by relay-a"
svc "$RELAY_B_HOST" start
sleep 2

echo
info "STEP 4: publish + read a NIP-65 kind:10002 relay-list (outbox model)"
NIP65_JSON="$("$NAK" event --sec "$SK" -k 10002 -c "" \
  -t "r=${RELAY_A_URL};write" -t "r=${RELAY_B_URL};read" \
  "$RELAY_A_URL" "$RELAY_B_URL" 2>/dev/null)"
NIP65_ID="$(printf '%s' "$NIP65_JSON" | jq -r '.id' | head -1)"
[ -n "$NIP65_ID" ] && [ "$NIP65_ID" != "null" ] || fail "no NIP-65 event id"
sleep 1
GOT_N_A="$(read_back "$RELAY_A_URL" "$NIP65_ID")"
GOT_N_B="$(read_back "$RELAY_B_URL" "$NIP65_ID")"
[ "$GOT_N_A" = "$NIP65_ID" ] || fail "relay-a missing NIP-65 event"
[ "$GOT_N_B" = "$NIP65_ID" ] || fail "relay-b missing NIP-65 event"
pass "NIP-65 kind:10002 relay-list published + readable: $NIP65_ID"

echo
info "STEP 5: strfry sync (NIP-77 Negentropy) reconciles an event A -> B"
# Publish a fresh event to relay-a ONLY, delete it from relay-b's view by
# using a brand-new id that only exists on A, then reconcile with strfry sync.
SYNC_NONCE="ready-efe-sync-$(date +%s)-$RANDOM"
SYNC_JSON="$("$NAK" event --sec "$SK" -k 1 -c "$SYNC_NONCE" "$RELAY_A_URL" 2>/dev/null)"
SYNC_ID="$(printf '%s' "$SYNC_JSON" | jq -r '.id' | head -1)"
info "event on relay-a only: $SYNC_ID"
sleep 1
# Confirm it is on A but NOT yet on B.
[ "$(read_back "$RELAY_A_URL" "$SYNC_ID")" = "$SYNC_ID" ] || fail "sync seed not on relay-a"
if [ "$(read_back "$RELAY_B_URL" "$SYNC_ID")" = "$SYNC_ID" ]; then
  fail "sync seed already on relay-b — cannot demonstrate reconciliation"
fi
# Run strfry sync ON relay-b, pulling from relay-a via Negentropy. Runs as the
# baron user (owns the LMDB db) — NOT via sudo, which would create root-owned db
# files under the relay process.
info "running 'strfry sync' on relay-b pulling from relay-a (Negentropy)..."
ssh $SSH_OPTS "${RELAY_SSH_USER}@$RELAY_B_HOST" \
  "strfry --config=/etc/strfry.conf sync ${RELAY_A_URL} --dir=down --timeout=30" 2>&1 | tail -6 || true
sleep 2
GOT_SYNC_B="$(read_back "$RELAY_B_URL" "$SYNC_ID")"
[ "$GOT_SYNC_B" = "$SYNC_ID" ] || fail "strfry sync did not reconcile $SYNC_ID into relay-b"
pass "Negentropy sync reconciled $SYNC_ID from relay-a into relay-b"

echo
pass "ALL RELAY DEMO STEPS PASSED"
cat <<EOF

SUMMARY
  relay-a:            $RELAY_A_URL
  relay-b:            $RELAY_B_URL
  test event id:      $EVENT_ID
  nip-65 event id:    $NIP65_ID
  negentropy sync id: $SYNC_ID
EOF
