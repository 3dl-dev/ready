#!/usr/bin/env bash
# unlock-relays.sh — OPEN the strfry relays (ready-5fd; reverses lock-relays.sh /
# ready-266). Removes the rd write-allowlist writePolicy so the relay is a dumb
# pipe: WRITES ARE OPEN (strfry still verifies the schnorr signature on every
# event, so forgery is impossible — only a real key-holder can publish). Trust is
# enforced IN-PRODUCT: each app filters on read by its own trusted-pubkey set
# (rd: rdconfig.TrustedPubkeys; dontguess: the operator applyPut/TrustChecker
# admission gate). A per-identity relay-edge allowlist was redundant with that and
# coupled every portfolio app's onboarding to an rd relay change — the wrong model.
#
# Reproducible from scratch: re-runnable, idempotent. Runs from the workshop VM;
# needs ssh baron@<relay> + passwordless sudo (same access lock-relays.sh uses).
#
# Usage: scripts/unlock-relays.sh [relay-host ...]   (default: both relays by IP)
set -uo pipefail

RELAYS=("${@:-192.168.2.40 192.168.2.41}")
# shellcheck disable=SC2206
RELAYS=(${RELAYS[@]})
SSH_OPTS="-o StrictHostKeyChecking=no -o ConnectTimeout=8"
CONF="/etc/strfry.conf"
PLUGIN_DST="/etc/strfry/write-allowlist.py"

for R in "${RELAYS[@]}"; do
  echo "==> Opening relay $R"
  ssh $SSH_OPTS "baron@${R}" CONF="$CONF" PLUGIN_DST="$PLUGIN_DST" 'bash -s' <<'REMOTE'
set -euo pipefail
# Reset writePolicy to open (stock value ""). Idempotent — re-runs land on "".
sudo sed -i "s|plugin = \"${PLUGIN_DST}\"|plugin = \"\"|" "$CONF"
echo "writePolicy line: $(grep -n 'plugin = ' "$CONF" | head -1)"
sudo systemctl restart strfry
sleep 2
state=$(sudo systemctl is-active strfry)
echo "strfry: ${state}"
[ "$state" = "active" ] || { echo "ERROR: strfry not active after restart"; exit 1; }
echo "opened: $(hostname)"
REMOTE
  rc=$?
  [ "$rc" -eq 0 ] || { echo "FAIL opening $R (rc=$rc)"; exit 1; }
  echo "==> relay $R OPEN"
  echo
done

echo "All relays open (write-open, sig-verified). Trust is now in-product."
echo "Re-fence (not recommended) with scripts/lock-relays.sh."
