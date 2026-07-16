#!/usr/bin/env bash
# lock-relays.sh — install the rd write-allowlist writePolicy on the live strfry
# relays (ready-266). Reproducible from scratch: re-runnable, idempotent.
#
# Locks relay-a (relay-a.internal) and relay-b (relay-b.internal) so that only ADMITTED
# portfolio pubkeys may WRITE. Reads stay open (writePolicy governs the write path
# only). Enforcement is by the event's author pubkey — strfry verifies the schnorr
# signature before the plugin runs, so an allowlisted pubkey field proves key
# possession; no NIP-42 AUTH challenge is needed. See docs/relay-runbook.md.
#
# For each relay it:
#   1. copies scripts/relay-policy/write-allowlist.py  -> /etc/strfry/write-allowlist.py
#   2. copies scripts/relay-policy/write-allowlist.json -> /etc/strfry/write-allowlist.json
#      (the SOURCE OF TRUTH allowlist: the admitted portfolio pubkeys, mirroring
#       rd's client-side rdconfig.Config.TrustedPubkeys + self)
#   3. sets  relay.writePolicy.plugin = "/etc/strfry/write-allowlist.py" in
#      /etc/strfry.conf
#   4. restarts strfry and verifies it came back active
#
# Runs from the workshop VM; needs ssh baron@<relay> (workshop.pub authorized by
# mk-relay.sh) and passwordless sudo on the relays (cloud-init default).
#
# Usage: scripts/lock-relays.sh [relay-ip ...]   (default: both relays)
set -uo pipefail

RELAYS=("${@:-relay-a.internal relay-b.internal}")
# shellcheck disable=SC2206
RELAYS=(${RELAYS[@]})
REPO_ROOT="$(cd "$(dirname "$0")/.." && pwd)"
PLUGIN_SRC="$REPO_ROOT/scripts/relay-policy/write-allowlist.py"
ALLOW_SRC="$REPO_ROOT/scripts/relay-policy/write-allowlist.json"
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=8"

PLUGIN_DST="/etc/strfry/write-allowlist.py"
ALLOW_DST="/etc/strfry/write-allowlist.json"
CONF="/etc/strfry.conf"

[ -f "$PLUGIN_SRC" ] || { echo "ERROR: missing $PLUGIN_SRC"; exit 1; }
[ -f "$ALLOW_SRC" ]  || { echo "ERROR: missing $ALLOW_SRC"; exit 1; }
python3 -c "import json,sys; json.load(open('$ALLOW_SRC'))" \
  || { echo "ERROR: $ALLOW_SRC is not valid JSON"; exit 1; }

for R in "${RELAYS[@]}"; do
  echo "==> Locking relay $R"
  # Ship the plugin + allowlist to a staging area, then sudo-install into /etc/strfry.
  scp $SSH_OPTS "$PLUGIN_SRC" "baron@${R}:/tmp/rd-write-allowlist.py" || { echo "FAIL scp plugin $R"; exit 1; }
  scp $SSH_OPTS "$ALLOW_SRC"  "baron@${R}:/tmp/rd-write-allowlist.json" || { echo "FAIL scp allowlist $R"; exit 1; }

  ssh $SSH_OPTS "baron@${R}" CONF="$CONF" PLUGIN_DST="$PLUGIN_DST" ALLOW_DST="$ALLOW_DST" 'bash -s' <<'REMOTE'
set -euo pipefail
sudo mkdir -p /etc/strfry
sudo install -m 0755 /tmp/rd-write-allowlist.py  "$PLUGIN_DST"
sudo install -m 0644 /tmp/rd-write-allowlist.json "$ALLOW_DST"
rm -f /tmp/rd-write-allowlist.py /tmp/rd-write-allowlist.json

# Point writePolicy at the plugin. The stock config ships `plugin = ""`; make the
# edit idempotent (re-runs land on the same value).
if ! grep -q "plugin = \"$PLUGIN_DST\"" "$CONF"; then
  sudo sed -i "s|plugin = \"\"|plugin = \"$PLUGIN_DST\"|" "$CONF"
fi
echo "writePolicy line: $(grep -n 'plugin = ' "$CONF" | head)"

# Sanity: plugin must be executable and parse.
python3 -c "import ast; ast.parse(open('$PLUGIN_DST').read())"

sudo systemctl restart strfry
sleep 2
sudo systemctl is-active strfry
echo "locked: $(hostname)"
REMOTE
  rc=$?
  [ "$rc" -eq 0 ] || { echo "FAIL locking $R (rc=$rc)"; exit 1; }
  echo "==> relay $R locked"
  echo
done

echo "All relays locked. Prove with scripts/relay-writepolicy-demo.sh"
