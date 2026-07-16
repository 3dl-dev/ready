# scripts/lib/relay-probe.sh — shared LIVE-relay reachability probe for the nostr
# demo scripts (ready-6cf).
#
# The nostr-native demos prove two independent things:
#   1. the LOCAL append-only signed-event log is authoritative and works with
#      every relay offline (this leg MUST run and pass with no network); and
#   2. the relay is a replaceable cache (reconcile/round-trip legs) — these need
#      LAN access to a live relay.
#
# relay_reachable lets a demo gate leg (2) behind an actual TCP probe so the
# script degrades to a DOCUMENTED SKIP when the relay is unreachable instead of
# failing. It never gates leg (1).
#
# Usage:
#   source "$REPO_ROOT/scripts/lib/relay-probe.sh"
#   if relay_reachable; then ...live leg...; else info "SKIP (relay unreachable)"; fi
#
# Resolution: explicit arg > $RD_NOSTR_RELAY_URL > the pkg/rdconfig default
# relay-a (ws://relay-a.internal:7777). Only a TCP connect is attempted — a full
# websocket/strfry handshake is out of scope for a cheap reachability gate.

relay_reachable() {  # relay_reachable [ws-url]
  local url="${1:-${RD_NOSTR_RELAY_URL:-ws://relay-a.internal:7777}}"
  local hostport="${url#*://}"; hostport="${hostport%%/*}"
  local host="${hostport%:*}" port="${hostport##*:}"
  [ "$port" = "$host" ] && port=7777
  timeout 3 bash -c "cat < /dev/null > /dev/tcp/$host/$port" 2>/dev/null
}
