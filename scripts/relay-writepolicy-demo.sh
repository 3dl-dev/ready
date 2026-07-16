#!/usr/bin/env bash
# relay-writepolicy-demo.sh — LIVE ground-source proof for ready-266.
#
# Proves the strfry write-allowlist on BOTH locked relays, no mocks:
#   (a) an ALLOWLISTED key (the workshop VM portfolio key) publishes -> ACCEPTED;
#   (b) a random UNTRUSTED key attempts to publish -> REJECTED by the relay, with
#       the relay's own OK,false block reason shown;
#   (c) reads stay OPEN (an unauthenticated REQ still returns events).
# Proven on relay-a (relay-a.internal) AND relay-b (relay-b.internal).
#
# Enforcement is by the event's AUTHOR pubkey: strfry verifies the schnorr
# signature before the writePolicy plugin runs, so an allowlisted pubkey field is
# proof of key possession — no NIP-42 AUTH challenge is needed. This is
# defence-in-depth ON TOP of rd's client-side web-of-trust ingestion gate (d53).
#
# Run from the workshop VM (the portfolio key lives at ~/.cf/nostr-identity.json,
# which is on the relay allowlist). Requires Go. Idempotent (unique item ids).
#
# Usage: scripts/relay-writepolicy-demo.sh
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
RELAY_A="${RELAY_A:-ws://relay-a.internal:7777}"
RELAY_B="${RELAY_B:-ws://relay-b.internal:7777}"
KEY_PATH="${KEY_PATH:-$HOME/.cf/nostr-identity.json}"
OUT_DIR="${OUT_DIR:-$REPO_ROOT/docs}"
OUT="$OUT_DIR/relay-writepolicy-demo-output.txt"
mkdir -p "$OUT_DIR"

pass() { printf '\033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$1"; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$1"; }

run() {
  info "Building the publish probe"
  PROBE="$(mktemp)"
  ( cd "$REPO_ROOT" && go build -o "$PROBE" ./scripts/relay-policy/probe/ ) || fail "probe build"

  [ -f "$KEY_PATH" ] || fail "no portfolio key at $KEY_PATH (materialize it with rd, then add its pubkey to the allowlist)"
  ALLOWED_PUB="$(python3 -c "import json;print(json.load(open('$KEY_PATH'))['pubkey_hex'])")"
  info "Allowlisted portfolio pubkey: $ALLOWED_PUB"

  for R in "$RELAY_A" "$RELAY_B"; do
    echo
    info "=== Relay $R ==="

    # (a) allowlisted -> ACCEPTED (probe exits 0 on accept)
    if "$PROBE" "$R" allowlisted "$KEY_PATH"; then
      pass "allowlisted key ACCEPTED by $R"
    else
      fail "allowlisted key was REJECTED by $R (is its pubkey in /etc/strfry/write-allowlist.json?)"
    fi

    # (b) random untrusted -> REJECTED (probe exits 1 on reject)
    if "$PROBE" "$R" random; then
      fail "untrusted random key was ACCEPTED by $R — write-allowlist NOT enforced"
    else
      pass "untrusted random key REJECTED by $R"
    fi

    # (c) reads stay OPEN — unauthenticated REQ returns an EVENT or EOSE.
    host="${R#ws://}"; host="${host%%:*}"; port="${R##*:}"
    if python3 - "$host" "$port" <<'PY'
import socket, os, struct, sys, base64
host, port = sys.argv[1], int(sys.argv[2])
key = base64.b64encode(os.urandom(16)).decode()
s = socket.create_connection((host, port), timeout=8)
s.sendall(("GET / HTTP/1.1\r\nHost: %s:%d\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n"
           "Sec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\n\r\n" % (host, port, key)).encode())
assert b"101" in s.recv(4096)
def sendtext(msg):
    b = msg.encode(); mask = os.urandom(4); hdr = bytearray([0x81])
    ln = len(b)
    if ln < 126: hdr.append(0x80 | ln)
    else: hdr.append(0x80 | 126); hdr += struct.pack('>H', ln)
    hdr += mask
    s.sendall(bytes(hdr) + bytes(bb ^ mask[i % 4] for i, bb in enumerate(b)))
sendtext('["REQ","r1",{"kinds":[30302],"limit":1}]')
s.settimeout(6); data = s.recv(65536)
i = 2; ln = data[1] & 0x7f
if ln == 126: ln = struct.unpack('>H', data[2:4])[0]; i = 4
payload = data[i:i+ln]
sys.exit(0 if payload.startswith(b'["EVENT"') or payload.startswith(b'["EOSE"') else 1)
PY
    then
      pass "reads stay OPEN on $R (unauthenticated REQ served)"
    else
      fail "reads appear blocked on $R — writePolicy must only gate writes"
    fi
  done

  rm -f "$PROBE"
  echo
  pass "ready-266 PROVEN on both relays: allowlisted writes accepted, untrusted writes rejected, reads open"
}

run 2>&1 | tee "$OUT"
echo "captured: $OUT"
