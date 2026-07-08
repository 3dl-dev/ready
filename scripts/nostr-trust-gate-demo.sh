#!/usr/bin/env bash
# nostr-trust-gate-demo.sh — LIVE ground-source proof for ready-d53.
#
# Proves rd's read-side WEB-OF-TRUST gate with NO MOCKS against the LIVE
# self-hosted strfry relays. nostr's Event.Verify() proves an event is internally
# consistent (id + schnorr sig) but NOT that its author is AUTHORIZED to write —
# any generated key produces events that Verify. So rd must consult a trusted-
# pubkey allowlist at INGESTION and PROJECTION: only events from admitted
# identities may mutate projected work-item state.
#
# Two independent portfolio identities (separate CF_HOME => separate secp256k1
# keys) both publish a card for the SAME item id to the LIVE relay:
#   - VICTIM (identity A) creates the item legit/active/p1.
#   - ATTACKER (identity B) publishes a LATER forged card HIJACKED/done/p0 for the
#     same item id (a takeover attempt: later timestamp would win latest-wins).
#
# The permissive mainframe relays accept BOTH (the relay-side write-allowlist is
# the SEPARATE item ready-266) — which is exactly why the CLIENT-side gate must
# catch it. Then:
#   1. ATTACKER's own node (which trusts B) reconciles + shows HIJACKED — proving
#      the forged event is genuinely LIVE on the relay and passes Verify.
#   2. VICTIM wipes its local log and `rd nostr show --reconcile` — reconciling
#      BOTH cards from the relay. Its trust set is {A}, so:
#         * the trusted-key card IS APPLIED  (item = legit/active), and
#         * the untrusted-key card IS IGNORED (never HIJACKED/done, and it never
#           poisoned the local authoritative log).
#
# Endpoints come from pkg/rdconfig defaults (never hardcoded); override the write
# target with RD_NOSTR_RELAY_URL. Requires: Go toolchain, jq, LAN access to a relay.
#
# Usage: scripts/nostr-trust-gate-demo.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"
GO="${GO:-go}"

pass() { printf '\033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$1"; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$1"; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

info "building rd"
"$GO" build -o "$WORK/rd" ./cmd/rd
RD="$WORK/rd"

# Two isolated identities: distinct CF_HOME => distinct portfolio secp256k1 key.
VICTIM_HOME="$WORK/victim-cfhome";   VICTIM_PROJ="$WORK/victim-proj"
ATTACK_HOME="$WORK/attacker-cfhome"; ATTACK_PROJ="$WORK/attacker-proj"
mkdir -p "$VICTIM_HOME" "$VICTIM_PROJ" "$ATTACK_HOME" "$ATTACK_PROJ"

( cd "$VICTIM_PROJ" && CF_HOME="$VICTIM_HOME" "$RD" init --offline >/dev/null )
( cd "$ATTACK_PROJ" && CF_HOME="$ATTACK_HOME" "$RD" init --offline >/dev/null )

echo
info "STEP 1: VICTIM (identity A) creates the item legit/active/p1 on the LIVE relay"
ID="$(cd "$VICTIM_PROJ" && CF_HOME="$VICTIM_HOME" RD_NOSTR=1 "$RD" create "legit item" --type task --priority p1 --context "ready-d53 trust proof" 2>"$WORK/create.err" | tail -1)"
cat "$WORK/create.err" >&2 || true
[ -n "$ID" ] || fail "rd create produced no item id"
info "victim item id: $ID"
VICTIM_PK="$(jq -r '.pubkey' "$VICTIM_PROJ/.ready/nostr-log.jsonl" | head -1)"
info "victim pubkey:  $VICTIM_PK"
pass "victim published its trusted card to the live relay"

echo
info "STEP 2: ATTACKER (identity B, a DIFFERENT key) forges a LATER card for the SAME id: $ID"
# Use `rd nostr put` under the attacker's CF_HOME => attacker's portfolio key
# signs the forged 30302 card. --status done + --priority p0 is the takeover.
ATTACK_OUT="$(cd "$ATTACK_PROJ" && CF_HOME="$ATTACK_HOME" RD_NOSTR=1 "$RD" nostr put "$ID" --title "HIJACKED" --status done --priority p0 --context "seized by an untrusted key" 2>&1)"
printf '%s\n' "$ATTACK_OUT" | sed 's/^/    /'
ATTACK_PK="$(jq -r '.pubkey' "$ATTACK_PROJ/.ready/nostr-log.jsonl" | head -1)"
info "attacker pubkey: $ATTACK_PK"
[ "$ATTACK_PK" != "$VICTIM_PK" ] || fail "attacker and victim share a key — test is meaningless"
grep -q "relay-accepted=true" <<<"$ATTACK_OUT" || fail "attacker's forged event was NOT accepted by the relay — cannot prove the client gate (is ready-266's write-allowlist already active?)"
pass "attacker's forged card was accepted by the PERMISSIVE relay (as expected pre-ready-266) and signed by a foreign key"

echo
info "STEP 3: CONTRAST — the attacker's OWN node (trusts B) reconciles + shows the forged state, proving it is genuinely LIVE on the relay"
sleep 1 # let the relay index
rm -f "$ATTACK_PROJ/.ready/nostr-log.jsonl"
ATTACK_VIEW="$(cd "$ATTACK_PROJ" && CF_HOME="$ATTACK_HOME" RD_NOSTR=1 "$RD" nostr show "$ID" --reconcile 2>&1)"
printf '%s\n' "$ATTACK_VIEW" | sed 's/^/    /'
grep -q "title:    HIJACKED" <<<"$ATTACK_VIEW" || fail "attacker node did not see its own forged event — relay/publish problem, not a gate proof"
pass "forged event is LIVE on the relay and passes Verify (the attacker's node applies it because B trusts B)"

echo
info "STEP 4: TRUST GATE — VICTIM wipes its local log and reconciles BOTH cards from the relay; trust set = {A}"
rm -f "$VICTIM_PROJ/.ready/nostr-log.jsonl"
[ ! -f "$VICTIM_PROJ/.ready/nostr-log.jsonl" ] || fail "victim log not wiped"
sleep 1
VICTIM_VIEW="$(cd "$VICTIM_PROJ" && CF_HOME="$VICTIM_HOME" RD_NOSTR=1 "$RD" nostr show "$ID" --reconcile 2>&1)"
printf '%s\n' "$VICTIM_VIEW" | sed 's/^/    /'

# (a) trusted-key event APPLIED: item reconstructs as the victim's legit card
# (rd create publishes status=inbox), NOT the attacker's done takeover.
grep -q "title:    legit item" <<<"$VICTIM_VIEW" || fail "trusted card was not applied (lost the legit title)"
grep -q "status:   inbox"      <<<"$VICTIM_VIEW" || fail "trusted card was not applied (expected the victim's inbox status)"
# (b) untrusted-key event IGNORED: never HIJACKED / done.
grep -q "HIJACKED" <<<"$VICTIM_VIEW" && fail "PROJECTION GATE FAILED: untrusted forged card influenced projected state"
grep -q "status:   done" <<<"$VICTIM_VIEW" && fail "PROJECTION GATE FAILED: untrusted forged done-status was applied"
pass "PROJECTION GATE: trusted card applied (legit/active); untrusted forged card IGNORED"

# (c) INGESTION GATE: the attacker's key never entered the victim's authoritative log.
if [ -f "$VICTIM_PROJ/.ready/nostr-log.jsonl" ]; then
  if jq -r '.pubkey' "$VICTIM_PROJ/.ready/nostr-log.jsonl" | grep -qx "$ATTACK_PK"; then
    fail "INGESTION GATE FAILED: attacker-authored event poisoned the victim's local authoritative log"
  fi
  POISON_KEYS="$(jq -r '.pubkey' "$VICTIM_PROJ/.ready/nostr-log.jsonl" | sort -u | tr '\n' ' ')"
  info "author pubkeys in the victim's authoritative log after reconcile: $POISON_KEYS"
fi
pass "INGESTION GATE: the victim's local authoritative log holds ZERO attacker-authored events"

echo
pass "ALL ready-d53 TRUST-GATE STEPS PASSED"
cat <<EOF

SUMMARY
  item id:            $ID
  victim (trusted):   $VICTIM_PK
  attacker (foreign): $ATTACK_PK
  relay posture:      PERMISSIVE (write-allowlist is the separate item ready-266)
  ingestion gate:     PASS (foreign-key event dropped before merge; log unpoisoned)
  projection gate:    PASS (trusted card applied = legit/active; forged card ignored)
  invariant:          Verify proves consistency; the trust allowlist proves AUTHORITY
EOF
