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
# Two independent portfolio identities (separate RD_HOME => separate secp256k1
# keys) both publish a card for the SAME item id to the LIVE relay:
#   - VICTIM (identity A) creates the item legit/active/p1. Signs with the
#     machine's ALLOWLISTED portfolio key (ready-266), resolved the same way
#     pkg/sync's liveRelayKey test helper does (scripts/lib/nostr-demo-key.sh).
#   - ATTACKER (identity B) publishes a LATER forged card HIJACKED/done/p0 for the
#     same item id (a takeover attempt: later timestamp would win latest-wins).
#
# ready-266 locked BOTH relays' write-allowlist at the relay layer — a demo of
# the CLIENT-side d53 gate needs the attacker's write to actually LAND on the
# relay (that is the whole point: Verify+relay-accept is not enough; rd's own
# trust set is the real authority check). So this demo TEMPORARILY admits the
# attacker's key to the live relay allowlist via `rd nostr grant` + `sync-
# allowlist --apply` (ready-84e/BP-5 — same mechanism scripts/nostr-grant-revoke-
# demo.sh already exercises live), proving the relay-level allowlist and rd's
# client-side trust set are TWO INDEPENDENT gates: the attacker key can be
# relay-admitted yet still untrusted by the victim's local rd config.
# The attacker's grant is REVOKED (and originals restored via trap) before the
# script exits, on success or failure — the live relays are never left
# admitting the throwaway demo key.
#
#   1. ATTACKER's own node (which trusts B) reconciles + shows HIJACKED — proving
#      the forged event is genuinely LIVE on the relay and passes Verify.
#   2. VICTIM wipes its local log and `rd nostr show --reconcile` — reconciling
#      BOTH cards from the relay. Its trust set is {A}, so:
#         * the trusted-key card IS APPLIED  (item = legit/active), and
#         * the untrusted-key card IS IGNORED (never HIJACKED/done, and it never
#           poisoned the local authoritative log).
#
# Endpoints come from pkg/rdconfig defaults (never hardcoded); override the write
# target with RD_NOSTR_RELAY_URL. Requires: Go toolchain, jq, nak, ssh access to
# the relay VMs (for the temporary grant/revoke), LAN access to a relay.
#
# Usage: scripts/nostr-trust-gate-demo.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"
GO="${GO:-go}"
NAK="${NAK:-nak}"
source "$REPO_ROOT/scripts/lib/nostr-demo-key.sh"
source "$REPO_ROOT/scripts/lib/relay-probe.sh"

pass() { printf '\033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$1"; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$1"; }

# nak requirement is enforced inside the LIVE layer gate below (it is only needed
# to mint the throwaway attacker key). The offline LAYER B proof needs no nak.

WORK="$(mktemp -d)"

# ---- relay write-allowlist safety net (mirrors scripts/nostr-grant-revoke-demo.sh) --
RELAY_USER="baron"
RELAYS=(192.168.2.40 192.168.2.41)
REMOTE_PATH=/etc/strfry/write-allowlist.json
SSH_OPTS=(-o StrictHostKeyChecking=no -o ConnectTimeout=8)
declare -A ORIG
capture_originals() {
  for R in "${RELAYS[@]}"; do
    ORIG[$R]="$WORK/orig-$R.json"
    ssh "${SSH_OPTS[@]}" "$RELAY_USER@$R" "cat $REMOTE_PATH" > "${ORIG[$R]}" \
      || fail "could not capture original allowlist from $R"
  done
}
restore_originals() {
  for R in "${RELAYS[@]}"; do
    [ -f "${ORIG[$R]:-}" ] || continue
    scp "${SSH_OPTS[@]}" "${ORIG[$R]}" "$RELAY_USER@$R:/tmp/rd-restore.json" >/dev/null 2>&1 \
      && ssh "${SSH_OPTS[@]}" "$RELAY_USER@$R" "sudo install -m 0644 /tmp/rd-restore.json $REMOTE_PATH && rm -f /tmp/rd-restore.json" >/dev/null 2>&1 \
      && echo "  restored $R" || echo "  WARNING: restore of $R may have failed — check manually"
  done
}
cleanup() {
  info "cleanup: revoking attacker grant (if any) and restoring original relay allowlists"
  if [ -n "${ATTACK_PK:-}" ] && [ -n "${VICTIM_HOME:-}" ]; then
    ( cd "$VICTIM_PROJ" && RD_HOME="$VICTIM_HOME" "$RD" nostr revoke "$ATTACK_PK" --label "trust-gate demo attacker (ready-d53)" >/dev/null 2>&1 ) || true
    ( cd "$VICTIM_PROJ" && RD_HOME="$VICTIM_HOME" "$RD" nostr sync-allowlist --file "$WORK/allowlist.json" --apply >/dev/null 2>&1 ) || true
  fi
  restore_originals
  rm -rf "$WORK"
}
trap cleanup EXIT

info "building rd"
"$GO" build -o "$WORK/rd" ./cmd/rd
RD="$WORK/rd"

echo
info "LAYER B (offline, authoritative): deterministic d53 trust-gate unit proofs — no relay, no ssh, no nak"
"$GO" test ./pkg/sync/ -run 'TestProjection_TrustGate_DropsUntrustedTakeover|TestProjection_TrustGate_AppliesTrustedEvent|TestProjection_TrustGate_DropsUntrustedNewItem|TestMergeFrom_TrustGate_RejectsUntrustedAuthor' -count=1 -v 2>&1 | sed 's/^/    /'
[ "${PIPESTATUS[0]}" = 0 ] || fail "deterministic d53 trust-gate proofs failed"
pass "trust gate proven offline: untrusted takeover DROPPED, trusted event APPLIED, untrusted new item DROPPED, merge REJECTS untrusted author"

echo
# LAYER A is the full LIVE proof. It TEMPORARILY rewrites the LOCKED PRODUCTION relay
# write-allowlist via ssh+sudo on ${RELAYS[*]} (then restores it on exit), and needs
# nak to mint the throwaway attacker key. Because it mutates shared production infra,
# it is OFF by default: it runs ONLY with explicit opt-in (RD_NOSTR_LIVE_RELAY=1) AND
# nak on PATH AND a reachable relay. Otherwise it SKIPs — LAYER B above is the
# authoritative, deterministic proof of the same d53 gate.
if [ "${RD_NOSTR_LIVE_RELAY:-0}" != "1" ] || ! command -v "$NAK" >/dev/null 2>&1 || ! relay_reachable "ws://${RELAYS[0]}:7777"; then
  why=""
  [ "${RD_NOSTR_LIVE_RELAY:-0}" != "1" ] && why="$why not opted in (set RD_NOSTR_LIVE_RELAY=1);" || true
  command -v "$NAK" >/dev/null 2>&1 || why="$why nak absent;"
  relay_reachable "ws://${RELAYS[0]}:7777" || why="$why relay ${RELAYS[0]} unreachable;"
  info "SKIP LAYER A live trust-gate proof —$why it rewrites the production relay write-allowlist (ssh+sudo) and needs nak. LAYER B (above) is the authoritative offline proof of the d53 gate."
  echo
  pass "ready-d53 TRUST-GATE proof complete (LAYER B deterministic gate proofs green; LAYER A live path skipped)"
  exit 0
fi

info "LAYER A (live): full end-to-end trust-gate against the locked production relays"
# Two isolated identities: distinct RD_HOME => distinct portfolio secp256k1 key.
# VICTIM signs with the machine's ALLOWLISTED portfolio key (ready-266) so its
# writes land on the locked relays without any grant dance. ATTACKER is a FRESH,
# deliberately NOT-yet-admitted key — its whole purpose is to prove the
# client-side trust gate independently of relay admission (see header).
VICTIM_HOME="$WORK/victim-rdhome";   VICTIM_PROJ="$WORK/victim-proj"
ATTACK_HOME="$WORK/attacker-rdhome"; ATTACK_PROJ="$WORK/attacker-proj"
mkdir -p "$VICTIM_HOME" "$VICTIM_PROJ" "$ATTACK_HOME" "$ATTACK_PROJ"

materialize_allowlisted_key "$VICTIM_HOME/nostr-identity.json" || info "no allowlisted portfolio key — using rd's own generated identity (local-log proof valid; live relay writes may be rejected)"

ATTACK_SEC="$("$NAK" key generate)"
ATTACK_PK="$("$NAK" key public "$ATTACK_SEC")"
python3 -c "
import json, sys
sec, pub, path = sys.argv[1], sys.argv[2], sys.argv[3]
json.dump({'version': 1, 'secret_hex': sec, 'pubkey_hex': pub}, open(path, 'w'), indent=2)
" "$ATTACK_SEC" "$ATTACK_PK" "$ATTACK_HOME/nostr-identity.json"
chmod 600 "$ATTACK_HOME/nostr-identity.json"
info "attacker pubkey (fresh, NOT yet relay-admitted): $ATTACK_PK"

( cd "$VICTIM_PROJ" && RD_HOME="$VICTIM_HOME" "$RD" init >/dev/null )
( cd "$ATTACK_PROJ" && RD_HOME="$ATTACK_HOME" "$RD" init >/dev/null )

echo
info "capturing original live relay allowlists (restored on exit, success or failure)"
capture_originals
pass "originals captured"

echo
info "STEP 0: TEMPORARILY grant the attacker's key onto the live relay allowlist"
# This does NOT weaken the proof: it isolates the variable under test. The relay-
# level write-allowlist (ready-266) and rd's CLIENT-side web-of-trust gate (d53)
# are independent defenses; granting the attacker relay admission lets the forged
# write actually LAND, so the demo proves the CLIENT gate catches what the relay
# alone does not (the victim never grants/trusts the attacker).
( cd "$VICTIM_PROJ" && RD_HOME="$VICTIM_HOME" "$RD" nostr grant "$ATTACK_PK" contributor --label "trust-gate demo attacker (ready-d53)" ) || fail "grant attacker failed"
( cd "$VICTIM_PROJ" && RD_HOME="$VICTIM_HOME" "$RD" nostr sync-allowlist --file "$WORK/allowlist.json" --apply ) || fail "sync-allowlist apply (grant) failed"
sleep 2 # let the strfry plugin observe the mtime change
pass "attacker key temporarily admitted to both relays (relay-layer gate satisfied for the test)"

echo
info "STEP 1: VICTIM (identity A, allowlisted) creates the item legit/active/p1 on the LIVE relay"
ID="$(cd "$VICTIM_PROJ" && RD_HOME="$VICTIM_HOME" "$RD" create "legit item" --type task --priority p1 --context "ready-d53 trust proof" 2>"$WORK/create.err" | tail -1)"
cat "$WORK/create.err" >&2 || true
[ -n "$ID" ] || fail "rd create produced no item id"
info "victim item id: $ID"
VICTIM_PK="$(jq -r '.pubkey' "$VICTIM_PROJ/.ready/nostr-log.jsonl" | head -1)"
info "victim pubkey:  $VICTIM_PK"
[ "$VICTIM_PK" != "$ATTACK_PK" ] || fail "victim and attacker share a key — test is meaningless"
pass "victim published its trusted card to the live relay"

echo
info "STEP 2: ATTACKER (identity B, relay-admitted but victim-UNTRUSTED) forges a LATER card for the SAME id: $ID"
# Use `rd nostr put` under the attacker's RD_HOME => attacker's portfolio key
# signs the forged 30302 card. --status done + --priority p0 is the takeover.
ATTACK_OUT="$(cd "$ATTACK_PROJ" && RD_HOME="$ATTACK_HOME" "$RD" nostr put "$ID" --title "HIJACKED" --status done --priority p0 --context "seized by an untrusted key" 2>&1)"
printf '%s\n' "$ATTACK_OUT" | sed 's/^/    /'
ATTACK_PK_LOGGED="$(jq -r '.pubkey' "$ATTACK_PROJ/.ready/nostr-log.jsonl" | head -1)"
[ "$ATTACK_PK_LOGGED" = "$ATTACK_PK" ] || fail "attacker log pubkey mismatch"
grep -q "relay-accepted=true" <<<"$ATTACK_OUT" || fail "attacker's forged event was NOT accepted by the relay (grant/sync-allowlist did not take effect?)"
pass "attacker's forged card was accepted by the relay (temporarily granted) — signed by a foreign, victim-untrusted key"

echo
info "STEP 3: CONTRAST — the attacker's OWN node (trusts B) reconciles + shows the forged state, proving it is genuinely LIVE on the relay"
sleep 1 # let the relay index
rm -f "$ATTACK_PROJ/.ready/nostr-log.jsonl"
ATTACK_VIEW="$(cd "$ATTACK_PROJ" && RD_HOME="$ATTACK_HOME" "$RD" nostr show "$ID" --reconcile 2>&1)"
printf '%s\n' "$ATTACK_VIEW" | sed 's/^/    /'
grep -q "title:    HIJACKED" <<<"$ATTACK_VIEW" || fail "attacker node did not see its own forged event — relay/publish problem, not a gate proof"
pass "forged event is LIVE on the relay and passes Verify (the attacker's node applies it because B trusts B)"

echo
info "STEP 4: TRUST GATE — VICTIM wipes its local log and reconciles BOTH cards from the relay; trust set = {A}"
rm -f "$VICTIM_PROJ/.ready/nostr-log.jsonl"
[ ! -f "$VICTIM_PROJ/.ready/nostr-log.jsonl" ] || fail "victim log not wiped"
sleep 1
VICTIM_VIEW="$(cd "$VICTIM_PROJ" && RD_HOME="$VICTIM_HOME" "$RD" nostr show "$ID" --reconcile 2>&1)"
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
  relay posture:      LOCKED (ready-266); attacker was TEMPORARILY granted for
                      this test only, then revoked on exit (see cleanup trap)
  ingestion gate:     PASS (foreign-key event dropped before merge; log unpoisoned)
  projection gate:    PASS (trusted card applied = legit/active; forged card ignored)
  invariant:          Verify proves consistency; relay admission proves nothing about
                      AUTHORITY from the victim's point of view — the client-side
                      trust allowlist is the real authority check
EOF
