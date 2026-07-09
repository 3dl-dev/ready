#!/usr/bin/env bash
# nostr-sign-demo.sh — LIVE ground-source proof for ready-41d.
#
# Proves the generic nostr sign -> publish -> relay-accept -> verify loop end to
# end against the LIVE self-hosted strfry relays (NO MOCKS), using rd's OWN Go
# signer/publisher (pkg/nostr, exercised via scripts/nostr-demo), and cross-
# checks the canonical event id + BIP-340 schnorr signature against the `nak`
# reference nostr client for byte-exact agreement.
#
# Steps:
#   1. Cross-check id+sig: sign a fixed (sec, ts, kind, tags, content) with the
#      Go signer AND with nak; assert BOTH id and sig match byte-for-byte. Pure
#      local computation, no relay involved -- the throwaway dev key is fine here.
#   2. Live loop: with an ALLOWLISTED key (ready-266 -- locked relays reject any
#      other author), build+sign an event in Go, publish it to a live relay via
#      the Go publisher (relay answers OK,true), read it back, independently
#      Verify (ACCEPT), then tamper a byte and Verify (REJECT).
#
# Relay endpoints are discovered from pkg/rdconfig defaults (scripts/nostr-demo
# `relays`), never hardcoded. Requires: Go toolchain, `nak` on PATH, LAN access
# to the relays. NEVER commits a secret — the fixed sec below is a throwaway dev
# key used only to make the STEP 1 cross-check reproducible (no relay write).
# STEP 2's live publish resolves an ALLOWLISTED portfolio key the same way
# pkg/sync's liveRelayKey test helper does (see scripts/lib/nostr-demo-key.sh).
#
# Usage: scripts/nostr-sign-demo.sh
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

GO="${GO:-go}"
NAK="${NAK:-nak}"
DEMO=( "$GO" run ./scripts/nostr-demo )

pass() { printf '\033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$1"; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$1"; }

command -v "$NAK" >/dev/null 2>&1 || fail "nak not found on PATH (go install github.com/fiatjaf/nak@latest)"
source "$REPO_ROOT/scripts/lib/nostr-demo-key.sh"

# ---- Throwaway dev key (NOT a real secret; committed to NOTHING) ------------
# Used only so the Go<->nak cross-check is reproducible. Generate your own with
# `nak key generate` — this one is a fixed test vector, never a portfolio key.
DEV_SEC="3cf18b1c855044728c4ade9d12a89c1cec9f1c3014d4060b18a8f59f3962d600"
TS=1700000000
CONTENT_FILE="$(mktemp)"
trap 'rm -f "$CONTENT_FILE"' EXIT
printf 'ready-41d cross-check <>&" line1\nline2\ttab' > "$CONTENT_FILE"
CONTENT="$(cat "$CONTENT_FILE")"

echo
info "STEP 1: cross-check Go signer vs nak (id + schnorr sig must match byte-for-byte)"
GO_JSON="$("${DEMO[@]}" sign --sec "$DEV_SEC" --ts "$TS" --content "$CONTENT" \
  --tag "t=rd" --tag "client=rd-nostr")"
GO_ID="$(printf '%s' "$GO_JSON"  | jq -r .id)"
GO_SIG="$(printf '%s' "$GO_JSON" | jq -r .sig)"

NAK_JSON="$("$NAK" event --sec "$DEV_SEC" -k 1 --ts "$TS" \
  -t "t=rd" -t "client=rd-nostr" -c "@${CONTENT_FILE}")"
NAK_ID="$(printf '%s' "$NAK_JSON"  | jq -r .id)"
NAK_SIG="$(printf '%s' "$NAK_JSON" | jq -r .sig)"

info "go  id=$GO_ID"
info "nak id=$NAK_ID"
[ "$GO_ID" = "$NAK_ID" ]   || fail "event id mismatch between Go and nak"
[ "$GO_SIG" = "$NAK_SIG" ] || fail "schnorr sig mismatch between Go and nak"
pass "Go signer and nak agree on id AND sig (canonical NIP-01 serialization + BIP-340 correct)"

echo
info "STEP 2: LIVE loop against a real strfry relay via the Go publisher"
RELAY="${RD_NOSTR_RELAY_URL:-}"
if [ -z "$RELAY" ]; then
  RELAY="$("${DEMO[@]}" relays | head -1)"
fi
[ -n "$RELAY" ] || fail "no relay URL (pkg/rdconfig returned none)"
info "relay: $RELAY (discovered from pkg/rdconfig, not hardcoded)"

LIVE_SEC="$(_nostr_demo_key_secret_hex)" || fail "no allowlisted portfolio key available (set RD_NOSTR_TEST_SECRET_HEX or materialize ~/.cf/nostr-identity.json; ready-266 rejects any other author)"
PROVE_OUT="$("${DEMO[@]}" prove --relay "$RELAY" --sec "$LIVE_SEC")"
printf '%s\n' "$PROVE_OUT"
grep -q '^RELAY_OK true'      <<<"$PROVE_OUT" || fail "relay did not accept (no OK,true)"
grep -q '^VERIFY_ACCEPT ok'   <<<"$PROVE_OUT" || fail "independent verify did not accept relay-served event"
grep -q '^VERIFY_REJECT ok'   <<<"$PROVE_OUT" || fail "tamper was not rejected"
pass "LIVE relay accepted the Go-signed event; independent verify ACCEPTED; tamper REJECTED"

echo
pass "ALL NOSTR SIGN/PUBLISH/VERIFY PROOF STEPS PASSED"
EVENT_ID_LIVE="$(grep '^EVENT_ID ' <<<"$PROVE_OUT" | awk '{print $2}')"
cat <<EOF

SUMMARY
  relay:                 $RELAY
  cross-check id (Go=nak): $GO_ID
  live event id:         $EVENT_ID_LIVE
EOF
