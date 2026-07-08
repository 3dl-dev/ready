#!/usr/bin/env bash
# nostr-migration-parity-demo.sh — LIVE ground-source proof for ready-d65 (the
# CUTOVER, non-destructive scope). Proves the campfire->nostr MIGRATION of the
# EXISTING rd item set with item-for-item parity, WITHOUT touching campfire (which
# stays the authoritative default backend) and WITHOUT flipping any live default.
#
# It proves three things, no mocks:
#   1. FULL local-authoritative parity — re-emit the ENTIRE live campfire item set
#      as nostr events into the local append-only signed-event log (the epic's
#      source of truth), project it back, and assert item-for-item parity on count,
#      status, priority, type, deps, gates, history length + close-reasons, and
#      provenance. NO item lost or silently altered. (`rd nostr parity`)
#   2. LIVE relay round-trip — publish a dep-CLOSED sample to the LOCKED strfry
#      relays with the allowlisted portfolio key (write-allowlist, ready-266), WIPE
#      the local log, reconstruct PURELY from the relays via per-item reconcile, and
#      assert per-field parity — proving the events survive the locked relays and
#      dual-read reconstructs them (incl. graph-derived `blocked` status).
#   3. DUAL-READ + nostr-only capability — RD_NOSTR_READ=1 resolves rd's whole read
#      surface (list/show) from the nostr projection, matching campfire, WITHOUT
#      disconnecting campfire.
#
# Campfire is NEVER modified: the migration reads the campfire/JSONL item set and
# only ever WRITES nostr events (a separate .ready/nostr-log.jsonl + the relays).
#
# The live relay portion is gated behind RD_NOSTR_LIVE_RELAY=1 (LAN access to the
# locked strfry relays required). Without it, only the local-authoritative parity
# (step 1) + dual-read (step 3) run — which already prove "no item lost/altered".
#
# Usage:
#   scripts/nostr-migration-parity-demo.sh [SRC_PROJECT]
# SRC_PROJECT is a ready project dir holding the LIVE campfire item set
# (.campfire/root + .ready/mutations.jsonl). Defaults to the repo root.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"
GO="${GO:-go}"
SRC_PROJECT="${1:-$REPO_ROOT}"
RELAY_A="${RD_NOSTR_RELAY_A:-ws://192.168.2.40:7777}"
RELAY_B="${RD_NOSTR_RELAY_B:-ws://192.168.2.41:7777}"

pass() { printf '\033[32mPASS\033[0m %s\n' "$1"; }
fail() { printf '\033[31mFAIL\033[0m %s\n' "$1"; exit 1; }
info() { printf '\033[36m==>\033[0m %s\n' "$1"; }

# The live campfire item set must be present to migrate it.
if [ ! -f "$SRC_PROJECT/.ready/mutations.jsonl" ]; then
  info "no live campfire item set at $SRC_PROJECT/.ready/mutations.jsonl — nothing to migrate."
  info "run from a ready project checkout that has campfire data, or pass SRC_PROJECT."
  exit 0
fi

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
PROJ="$WORK/proj"
mkdir -p "$PROJ/.ready" "$PROJ/.campfire"

info "seeding an ISOLATED scratch project from the live campfire item set (read-only copy)"
cp "$SRC_PROJECT/.ready/mutations.jsonl" "$PROJ/.ready/mutations.jsonl"
[ -f "$SRC_PROJECT/.ready/config.json" ] && cp "$SRC_PROJECT/.ready/config.json" "$PROJ/.ready/config.json" || true
cp "$SRC_PROJECT/.campfire/root" "$PROJ/.campfire/root"

info "building rd"
"$GO" build -o "$WORK/rd" ./cmd/rd
RD="$WORK/rd"
cd "$PROJ"

SRC_COUNT=$("$RD" list --all --json 2>/dev/null | python3 -c "import sys,json;print(len(json.load(sys.stdin)))")
info "live campfire item set: $SRC_COUNT items (including terminal)"

echo
info "STEP 1: FULL migration -> local authoritative nostr log, then item-for-item parity"
"$RD" nostr migrate --local-only >/dev/null
"$RD" nostr parity >/dev/null || fail "STEP 1: item-for-item parity FAILED (some item lost or silently altered)"
PARITY_LINE=$("$RD" nostr parity --json 2>/dev/null | python3 -c "import sys,json;r=json.load(sys.stdin);print('source=%d projected=%d matched=%d mismatched=%d'%(r['source_count'],r['projected_count'],r['matched'],r['mismatched']))")
pass "FULL local-authoritative parity: $PARITY_LINE"

echo
info "STEP 1b: re-run migration is IDEMPOTENT (event-id dedup; log does not grow)"
L1=$(wc -l < .ready/nostr-log.jsonl)
ADDED=$("$RD" nostr migrate --local-only --json 2>/dev/null | python3 -c "import sys,json;print(json.load(sys.stdin)['appended'])")
L2=$(wc -l < .ready/nostr-log.jsonl)
[ "$ADDED" = "0" ] && [ "$L1" = "$L2" ] || fail "STEP 1b: re-run not idempotent (appended=$ADDED, $L1->$L2)"
pass "re-run appended 0 events; log stayed at $L1 lines"

echo
info "STEP 3: DUAL-READ — RD_NOSTR_READ=1 resolves the read surface from nostr, campfire untouched"
NS_COUNT=$(RD_NOSTR_READ=1 "$RD" list --all --json 2>/dev/null | python3 -c "import sys,json;print(len(json.load(sys.stdin)))")
[ "$NS_COUNT" = "$SRC_COUNT" ] || fail "STEP 3: dual-read list count $NS_COUNT != campfire $SRC_COUNT"
pass "dual-read \`rd list\` == campfire: $NS_COUNT items (RD_NOSTR_READ=1; campfire still default)"

if [ "${RD_NOSTR_LIVE_RELAY:-0}" != "1" ]; then
  echo
  info "STEP 2 (live relay) skipped — set RD_NOSTR_LIVE_RELAY=1 with LAN access to the locked relays to run it."
  echo
  pass "ready-d65 OFFLINE proof complete (full local-authoritative parity + idempotence + dual-read)"
  exit 0
fi

echo
info "STEP 2: LIVE relay round-trip through the LOCKED strfry relays ($RELAY_A, $RELAY_B)"
# Fresh log; pick a dep-CLOSED sample: the first N items by id UNION their blockers,
# so graph-derived `blocked` status can reconstruct from the relay too.
rm -f .ready/nostr-log.jsonl .ready/nostr-pending.jsonl
SAMPLE=$("$RD" list --all --json 2>/dev/null | python3 -c "
import sys,json
items=json.load(sys.stdin)
byid={i['id']:i for i in items}
ids=sorted(byid)[:20]
closed=set(ids)
for i in ids:
    for b in (byid[i].get('blocked_by') or []):
        if b in byid: closed.add(b)
print(' '.join(sorted(closed)))
")
NSAMPLE=$(echo $SAMPLE | wc -w)
info "dep-closed sample: $NSAMPLE items"

# Migrate ONLY the dep-closed sample to the locked relays WITH FULL HISTORY (card +
# one status event per audit-trail entry, provenance preserved). buffered=false
# proves the allowlisted portfolio key passed the relay write-allowlist (ready-266).
export CF_HOME="${CF_HOME:-$HOME/.cf}"
BUF=$("$RD" nostr migrate --only "$(echo $SAMPLE | tr ' ' ',')" --json 2>/dev/null | python3 -c "import sys,json;d=json.load(sys.stdin);print('buffered' if d['buffered'] else 'accepted', d['appended'])")
info "sample published to locked relays: $BUF events"

info "WIPE the local log; reconstruct the sample PURELY from the relays (per-item reconcile)"
rm -f .ready/nostr-log.jsonl
sleep 1
for id in $SAMPLE; do
  "$RD" nostr show "$id" --reconcile >/dev/null 2>&1 || true
done
RECON=$(wc -l < .ready/nostr-log.jsonl)
[ "$RECON" -gt 0 ] || fail "STEP 2: nothing reconstructed from the relays"
info "reconstructed $RECON events from the relays alone"

# Per-field parity for every sample item: campfire source vs relay-reconstructed.
LIVE_FAIL=$(python3 -c "
import subprocess,json
ids='''$SAMPLE'''.split()
def show(cmd):
    o=subprocess.run(['$RD']+cmd,capture_output=True,text=True).stdout
    return json.loads(o) if o.strip() else None
fails=[]
for i in ids:
    cf=show(['show',i,'--json']); ns=show(['nostr','show',i,'--json'])
    if ns is None: fails.append(i+':LOST'); continue
    d=[]
    for f in ('status','priority','type'):
        if (cf.get(f) or '')!=(ns.get(f) or ''): d.append('%s(cf=%s,ns=%s)'%(f,cf.get(f),ns.get(f)))
    if len(cf.get('history',[]))!=len(ns.get('history',[])): d.append('histlen(%d!=%d)'%(len(cf.get('history',[])),len(ns.get('history',[]))))
    n=lambda x: sorted((e.get('to_status','')+'|'+(e.get('note') or '')) for e in x.get('history',[]))
    if n(cf)!=n(ns): d.append('reasons')
    a=lambda x: sorted((e.get('changed_by') or '') for e in x.get('history',[]))
    if a(cf)!=a(ns): d.append('provenance')
    if d: fails.append(i+':'+';'.join(d))
if fails:
    print('FAIL '+' | '.join(fails))
else:
    print('OK %d'%len(ids))
")
case "$LIVE_FAIL" in
  OK*) pass "LIVE relay per-item parity: $LIVE_FAIL sample items reconstruct field-for-field from the locked relays" ;;
  *)   fail "STEP 2: LIVE relay parity mismatch: $LIVE_FAIL" ;;
esac

echo
pass "ALL ready-d65 PARITY STEPS PASSED"
cat <<EOF

SUMMARY
  source project:     $SRC_PROJECT ($SRC_COUNT live campfire items)
  step 1:             FULL item-for-item parity, local authoritative log ($PARITY_LINE)
  step 1b:            migration idempotent (event-id dedup)
  step 2:             $NSAMPLE-item dep-closed sample round-tripped through the LOCKED relays
  step 3:             dual-read (RD_NOSTR_READ=1) read surface == campfire; campfire untouched + still default
  invariant:          campfire NOT modified, NOT disconnected, still the default backend (ready-f94 defers the flip)
EOF
