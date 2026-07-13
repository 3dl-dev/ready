#!/usr/bin/env bash
# nostr-grant-revoke-demo.sh — ground-source proof for ready-84e / BP-5.
#
# ONE SIGNED ACT admits/revokes an actor across BOTH rd's client trust set and the
# LIVE relay write-allowlist. Proven end-to-end against the real locked strfry relays
# (relay-a 192.168.2.40, relay-b 192.168.2.41):
#
#   (a) create a fresh agent key
#   (b) its write is REJECTED at the relay (not yet granted)
#   (c) rd grant <agent> contributor + rd relay sync-allowlist --apply
#   (d) the agent's write now LANDS at the relay
#   (e) rd revoke <agent> + rd relay sync-allowlist --apply
#   (f) the agent's next write is REJECTED again
#   (g) throughout, the OWNER (P1) and machine-2 (P2) stay admitted (P1 write lands)
#
# SAFETY: the ORIGINAL live allowlists are captured up front and RESTORED on exit
# (even on failure) via a trap — the relays are never left locking out P1/P2/tenant.
# NO MOCKS: every publish/reject is a real event against the live locked relays.
set -uo pipefail

REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
NAK="$HOME/go/bin/nak"
GO="${GO:-/usr/local/go/bin/go}"
RELAYS=(192.168.2.40 192.168.2.41)
RELAY_USER=baron
REMOTE_PATH=/etc/strfry/write-allowlist.json
SSH_OPTS=(-o StrictHostKeyChecking=no -o ConnectTimeout=8)

# Known live identities (see scripts/relay-policy/write-allowlist.json + live drift).
P1=a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25   # owner / workshop
P2=48ea98a915f44a28810c33c017c43dc7d5595f3541522c3bc8c90327ec9df497   # machine-2 rd-node

WORK="$(mktemp -d)"
RD_HOME_DIR="$WORK/rdhome"
PROJ="$WORK/proj"
ALLOWFILE="$WORK/write-allowlist.json"
mkdir -p "$RD_HOME_DIR" "$PROJ/.ready"

export RD_HOME="$RD_HOME_DIR"
export RD_NOSTR=1

RD="$WORK/rd"

fail() { echo "DEMO FAIL: $*" >&2; exit 1; }
hr() { echo "======================================================================"; }

# --- capture + restore original live allowlists (trap) ---------------------------
declare -A ORIG
capture_originals() {
  for R in "${RELAYS[@]}"; do
    ORIG[$R]="$WORK/orig-$R.json"
    ssh "${SSH_OPTS[@]}" "$RELAY_USER@$R" "cat $REMOTE_PATH" > "${ORIG[$R]}" \
      || fail "could not capture original allowlist from $R"
  done
}
restore_originals() {
  echo; hr; echo "RESTORE: pushing original allowlists back to the relays"
  for R in "${RELAYS[@]}"; do
    [ -f "${ORIG[$R]:-}" ] || continue
    scp "${SSH_OPTS[@]}" "${ORIG[$R]}" "$RELAY_USER@$R:/tmp/rd-restore.json" >/dev/null 2>&1 \
      && ssh "${SSH_OPTS[@]}" "$RELAY_USER@$R" "sudo install -m 0644 /tmp/rd-restore.json $REMOTE_PATH && rm -f /tmp/rd-restore.json" >/dev/null 2>&1 \
      && echo "  restored $R" || echo "  WARNING: restore of $R may have failed — check manually"
  done
  rm -rf "$WORK"
}
trap restore_originals EXIT

fetch() { ssh "${SSH_OPTS[@]}" "$RELAY_USER@$1" "cat $REMOTE_PATH"; }

admitted() {  # admitted <relay> <pubkey>  -> prints yes/no
  if fetch "$1" | grep -q "$2"; then echo yes; else echo no; fi
}

# publish a kind-1 note with a given secret; prints ACCEPTED / REJECTED
publish_probe() {  # publish_probe <sec> <relay> <content>
  local out
  out="$("$NAK" event --sec "$1" -k 1 -c "$3" "ws://$2:7777" 2>&1)"
  if echo "$out" | grep -q "success"; then
    echo "ACCEPTED"
  elif echo "$out" | grep -qi "blocked\|failed\|rejected"; then
    echo "REJECTED"
  else
    echo "UNKNOWN"
    echo "$out" >&2
  fi
}

hr; echo "BUILD rd + capture original relay state"; hr
( cd "$REPO_ROOT" && "$GO" build -o "$RD" ./cmd/rd ) || fail "build rd"
[ -f "$HOME/.cf/nostr-identity.json" ] || fail "owner key ~/.cf/nostr-identity.json missing"
cp "$HOME/.cf/nostr-identity.json" "$RD_HOME_DIR/nostr-identity.json"
chmod 600 "$RD_HOME_DIR/nostr-identity.json"
OWNER_PUB="$(python3 -c "import json;print(json.load(open('$RD_HOME_DIR/nostr-identity.json'))['pubkey_hex'])")"
OWNER_SEC="$(python3 -c "import json;print(json.load(open('$RD_HOME_DIR/nostr-identity.json'))['secret_hex'])")"
[ "$OWNER_PUB" = "$P1" ] || fail "loaded owner pubkey $OWNER_PUB != expected P1 $P1"
echo "owner (P1) = $OWNER_PUB"
capture_originals
echo "original relay allowlists captured (will be restored on exit)"

cd "$PROJ"
"$RD" pin-board --owner "$P1" --board-d ready || fail "pin-board"

hr; echo "(a) fresh agent key"; hr
AGENT_SEC="$("$NAK" key generate)"
AGENT_PUB="$("$NAK" key public "$AGENT_SEC")"
echo "agent pubkey = $AGENT_PUB"

hr; echo "(b) agent write BEFORE grant -> expect REJECTED on both relays"; hr
for R in "${RELAYS[@]}"; do
  V="$(publish_probe "$AGENT_SEC" "$R" "ready-84e agent pre-grant probe")"
  echo "  relay $R: agent write = $V"
  [ "$V" = "REJECTED" ] || fail "agent write should be REJECTED pre-grant on $R (got $V)"
done

hr; echo "(g0) OWNER (P1) write -> expect ACCEPTED (stays admitted)"; hr
for R in "${RELAYS[@]}"; do
  V="$(publish_probe "$OWNER_SEC" "$R" "ready-84e P1 owner probe")"
  echo "  relay $R: P1 write = $V"
  [ "$V" = "ACCEPTED" ] || fail "P1 write should be ACCEPTED on $R (got $V)"
done

hr; echo "(c) rd grant agent contributor + seed P2 maintainer + sync-allowlist --apply"; hr
"$RD" grant "$AGENT_PUB" contributor --label "demo agent (ready-84e)" || fail "grant agent"
"$RD" grant "$P2" maintainer --label "machine-2 rd-node portfolio key" || fail "grant P2"
echo "--- dry-run diff ---"
"$RD" relay sync-allowlist --file "$ALLOWFILE" || fail "sync-allowlist dry-run"
echo "--- apply ---"
"$RD" relay sync-allowlist --file "$ALLOWFILE" --apply || fail "sync-allowlist apply"
sleep 2   # let the strfry plugin observe the mtime change

hr; echo "(d) agent write AFTER grant -> expect ACCEPTED on both relays"; hr
for R in "${RELAYS[@]}"; do
  V="$(publish_probe "$AGENT_SEC" "$R" "ready-84e agent post-grant probe")"
  echo "  relay $R: agent write = $V"
  [ "$V" = "ACCEPTED" ] || fail "agent write should be ACCEPTED post-grant on $R (got $V)"
  echo "    P1 admitted=$(admitted "$R" "$P1")  P2 admitted=$(admitted "$R" "$P2")"
  [ "$(admitted "$R" "$P1")" = yes ] || fail "P1 dropped on $R after grant apply"
  [ "$(admitted "$R" "$P2")" = yes ] || fail "P2 dropped on $R after grant apply"
done

hr; echo "(e) rd revoke agent + sync-allowlist --apply"; hr
"$RD" revoke "$AGENT_PUB" --label "demo agent (ready-84e)" || fail "revoke agent"
"$RD" relay sync-allowlist --file "$ALLOWFILE" --apply || fail "sync-allowlist apply (revoke)"
sleep 2

hr; echo "(f) agent write AFTER revoke -> expect REJECTED; P1/P2 still ACCEPTED"; hr
for R in "${RELAYS[@]}"; do
  V="$(publish_probe "$AGENT_SEC" "$R" "ready-84e agent post-revoke probe")"
  echo "  relay $R: agent write = $V"
  [ "$V" = "REJECTED" ] || fail "agent write should be REJECTED post-revoke on $R (got $V)"
  VP="$(publish_probe "$OWNER_SEC" "$R" "ready-84e P1 owner post-revoke probe")"
  echo "  relay $R: P1 write = $VP  (P2 admitted=$(admitted "$R" "$P2"))"
  [ "$VP" = "ACCEPTED" ] || fail "P1 write should still be ACCEPTED on $R (got $VP)"
  [ "$(admitted "$R" "$P2")" = yes ] || fail "P2 dropped on $R after revoke apply"
done

hr; echo "DEMO PASSED — grant->lands, revoke->rejected, P1/P2 admitted throughout"; hr
