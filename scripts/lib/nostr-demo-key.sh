# scripts/lib/nostr-demo-key.sh — shared allowlisted-key resolution for the LIVE
# nostr demo scripts (ready-b87).
#
# After the ready-266 relay write-allowlist lockdown, a locked strfry relay
# REJECTS any event whose author pubkey is not admitted. The demo scripts used to
# let `rd` (or the nostr-demo Go helper) generate a fresh, throwaway secp256k1 key
# on first use — harmless against the OLD permissive relays, but now a guaranteed
# REJECT. This mirrors pkg/sync's liveRelayKey test helper (see
# pkg/sync/live_relay_key_test.go) so shell demos sign with the SAME admitted
# portfolio key the Go live-relay tests use.
#
# Resolution order (identical to liveRelayKey):
#   1. RD_NOSTR_TEST_SECRET_HEX — 32-byte hex secret of an admitted key.
#   2. RD_NOSTR_TEST_KEY_PATH   — path to a SaveKeyFile-format key file.
#   3. $HOME/.cf/nostr-identity.json — this machine's persistent portfolio key
#      (the workshop VM's key is on the relay allowlist).
#   4. $RD_HOME/nostr-identity.json (if RD_HOME is already set and populated) —
#      lets a script reuse a key materialized earlier in the same run.
#
# Usage:
#   source "$REPO_ROOT/scripts/lib/nostr-demo-key.sh"
#   materialize_allowlisted_key "$RD_HOME/nostr-identity.json"
#
# materialize_allowlisted_key writes a SaveKeyFile-format JSON (version,
# secret_hex, pubkey_hex) to the given path with 0600 perms, creating parent
# dirs as needed. It calls `nak key public` to derive pubkey_hex for the
# informational field (rd re-derives the pubkey from secret_hex on load
# regardless — see pkg/nostr LoadKeyFile). Fails loudly (message to stderr,
# return 1) if no allowlisted key can be resolved anywhere.

_nostr_demo_key_secret_hex() {
  if [ -n "${RD_NOSTR_TEST_SECRET_HEX:-}" ]; then
    printf '%s' "$RD_NOSTR_TEST_SECRET_HEX"
    return 0
  fi
  local path="${RD_NOSTR_TEST_KEY_PATH:-}"
  if [ -z "$path" ] && [ -f "$HOME/.cf/nostr-identity.json" ]; then
    path="$HOME/.cf/nostr-identity.json"
  fi
  if [ -z "$path" ] && [ -n "${RD_HOME:-}" ] && [ -f "$RD_HOME/nostr-identity.json" ]; then
    path="$RD_HOME/nostr-identity.json"
  fi
  if [ -n "$path" ] && [ -f "$path" ]; then
    python3 -c "import json,sys; print(json.load(open(sys.argv[1]))['secret_hex'])" "$path"
    return 0
  fi
  return 1
}

materialize_allowlisted_key() {  # materialize_allowlisted_key <target-key-path>
  local target="$1"
  local sec
  if ! sec="$(_nostr_demo_key_secret_hex)"; then
    echo "ERROR: no allowlisted portfolio key available for the LIVE relay demo." >&2
    echo "  Set RD_NOSTR_TEST_SECRET_HEX, or RD_NOSTR_TEST_KEY_PATH, or materialize" >&2
    echo "  \$HOME/.cf/nostr-identity.json (the ready-266 relay write-allowlist" >&2
    echo "  rejects events from any other key)." >&2
    return 1
  fi
  local pub=""
  if command -v "${NAK:-nak}" >/dev/null 2>&1; then
    pub="$("${NAK:-nak}" key public "$sec" 2>/dev/null || true)"
  fi
  mkdir -p "$(dirname "$target")"
  python3 -c "
import json, sys
sec, pub, path = sys.argv[1], sys.argv[2], sys.argv[3]
json.dump({'version': 1, 'secret_hex': sec, 'pubkey_hex': pub}, open(path, 'w'), indent=2)
" "$sec" "$pub" "$target"
  chmod 600 "$target"
}
