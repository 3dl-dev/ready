#!/usr/bin/env bash
# relay-backfill.sh — re-send every project's already-signed board events to ONE
# named relay, then prove the result by reading it back (ready-260).
#
# WHY THIS IS NEEDED. Relay endpoints are global (~/.config/rd/rd.json), so a
# public relay is already a write target for every project. But an event only
# reaches a relay as a SIDE EFFECT of a write, so a project untouched since that
# relay was added has never sent it anything. Nothing is misconfigured — the
# history simply predates the relay. Until something explicitly re-sends, a
# browser client reading that relay sees a fraction of the portfolio and has no
# way to tell "this board is empty" from "this board was never published here".
#
# WHAT IT DOES NOT DO. It never re-signs, rebuilds, or re-stamps anything. Every
# event goes out with its original id, created_at and signature (rd log publish
# --board reads them straight out of .ready/nostr-log.jsonl), so a relay that
# already holds one answers "duplicate:" and the whole run is idempotent and
# safe to repeat. Any path that re-signed would mint new event ids and fork the
# history.
#
# SAFE BY DEFAULT: with no --apply it is a DRY RUN — it reports, per project,
# what would be sent and how many events, and contacts no relay for the publish
# step at all.
#
# VERIFICATION IS A READ-BACK, NOT A REPORT. rd's publish result says "accepted"
# when ANY write relay accepts (pkg/sync/relayclass.go reduceEventOutcome), so
# with several relays configured a total rejection by one of them is invisible.
# --relay pins the publish to a single relay so its verdict is the whole verdict,
# and `rd relay audit` then re-reads that relay with fresh subscriptions and
# compares the PROJECTION (addressable coordinates + status-event ids), not raw
# counts.
#
# WHY IT RETRIES, AND WHY THAT IS SAFE. The production relay is backed by a
# throttled store: under load it answers a share of writes with a transient
# "error: save …", which rd classifies as retryable and buffers rather than
# dead-letters. A single pass therefore lands most, not all, of a large board.
# Because every event is re-sent verbatim, another pass is a no-op for what
# already landed and a repair for what did not — so the script simply repeats
# passes over the projects whose read-back has not yet matched, and the AUDIT,
# not the publish report, decides when a project is done.
#
# Usage:
#   scripts/relay-backfill.sh --relay wss://relay.example [--apply] [--root DIR]
#                             [--jobs N] [--passes N] [--rounds N] [--audit-only] [project ...]
#
# Exit code is non-zero if any project's read-back still does not match.
set -uo pipefail

RELAY=""
APPLY=0
AUDIT_ONLY=0
JOBS=3
PASSES=6
ROUNDS=5
ROOT="${HOME}/projects"
RD_BIN="${RD_BIN:-}"
PROJECTS=()

while [ $# -gt 0 ]; do
  case "$1" in
    --relay) RELAY="$2"; shift 2 ;;
    --apply) APPLY=1; shift ;;
    --audit-only) AUDIT_ONLY=1; APPLY=1; shift ;;
    --jobs) JOBS="$2"; shift 2 ;;
    --passes) PASSES="$2"; shift 2 ;;
    --rounds) ROUNDS="$2"; shift 2 ;;
    --root) ROOT="$2"; shift 2 ;;
    --rd) RD_BIN="$2"; shift 2 ;;
    -h|--help) sed -n '2,45p' "$0"; exit 0 ;;
    *) PROJECTS+=("$1"); shift ;;
  esac
done

[ -n "$RELAY" ] || { echo "error: --relay <url> is required" >&2; exit 2; }
if [ -z "$RD_BIN" ]; then
  # Default to the rd built from THIS checkout, not whatever is on PATH: the
  # --dry-run / --relay / audit surface this script drives is ready-260 code.
  RD_BIN="$(cd "$(dirname "$0")/.." && pwd)/rd"
fi
[ -x "$RD_BIN" ] || { echo "error: rd binary not found or not executable: $RD_BIN (build with: go build -o rd ./cmd/rd)" >&2; exit 2; }

if [ "${#PROJECTS[@]}" -eq 0 ]; then
  # Every project under ROOT with a .ready state directory. A directory whose
  # .ready holds no signed-event log has nothing to republish, and saying so per
  # project is more useful than skipping it silently.
  while IFS= read -r d; do PROJECTS+=("$(basename "$d")"); done < <(find "$ROOT" -maxdepth 2 -type d -name .ready | sort | xargs -n1 dirname)
fi

WORK="$(mktemp -d -t rd-backfill-XXXXXX)"
trap 'rm -rf "$WORK"' EXIT

echo "relay:   $RELAY"
if [ "$APPLY" -eq 1 ]; then
  if [ "$AUDIT_ONLY" -eq 1 ]; then MODE="AUDIT ONLY"; else MODE="APPLY"; fi
else
  MODE="DRY RUN (no relay writes)"
fi
echo "mode:    $MODE"
echo "rd:      $RD_BIN   jobs=$JOBS passes=$PASSES rounds=$ROUNDS"
echo

# ---- plan pass (always runs, never touches a relay) -------------------------
PENDING=()
for p in "${PROJECTS[@]}"; do
  dir="$ROOT/$p"
  if [ ! -f "$dir/.ready/nostr-log.jsonl" ]; then
    echo "SKIP  $p — no .ready/nostr-log.jsonl (never initialised on nostr; nothing signed to republish)"
    continue
  fi
  plan="$(cd "$dir" && "$RD_BIN" log publish --board --dry-run --json --relay "$RELAY" 2>&1)" || {
    echo "FAIL  $p — dry run: $plan"
    continue
  }
  summary="$(printf '%s' "$plan" | python3 -c '
import json,sys
p=json.load(sys.stdin)
kinds=" ".join(f"{k}:{v}" for k,v in sorted(p["by_kind"].items()))
print(f'"'"'{p["board_coord"].split(":")[-1]:<24} events={p["events"]:<6} items={p["items"]:<5} def={str(p["has_board_definition"]):<5} {kinds}'"'"')
')" || summary="(unparseable plan)"
  echo "PLAN  $p  $summary"
  PENDING+=("$p")
done

[ "$APPLY" -eq 1 ] || exit 0

# one_project drives ONE board to convergence on the relay and writes its
# done-marker only when the READ-BACK matched — the publish's own report is never
# the criterion.
#
# It uses `rd relay repair`, not `rd log publish --board`: repair measures the
# gap first and re-sends only that, so a board with nothing on the relay still
# gets everything (the gap IS the whole board) while a board that is nearly there
# costs almost nothing. With a throttled relay that drops a share of every burst,
# whole-board re-sends make every round as expensive as the first.
one_project() {
  local p="$1" dir="$ROOT/$1" out
  if [ "$AUDIT_ONLY" -eq 1 ]; then
    if out="$(cd "$dir" && "$RD_BIN" relay audit --relay "$RELAY" 2>&1 | head -1)"; then
      printf 'done\n' > "$WORK/$p.state"
      echo "OK    $p  $out"
    else
      echo "WAIT  $p  $out"
    fi
    return
  fi
  if out="$(cd "$dir" && "$RD_BIN" relay repair --relay "$RELAY" --rounds "$ROUNDS" 2>&1 | tail -1)"; then
    printf 'done\n' > "$WORK/$p.state"
    echo "OK    $p  $out"
  else
    echo "WAIT  $p  $out"
  fi
}

for pass in $(seq 1 "$PASSES"); do
  [ "${#PENDING[@]}" -gt 0 ] || break
  echo
  echo "=== pass $pass — ${#PENDING[@]} project(s) still unverified"
  running=0
  for p in "${PENDING[@]}"; do
    one_project "$p" &
    running=$((running + 1))
    if [ "$running" -ge "$JOBS" ]; then wait -n 2>/dev/null || wait; running=$((running - 1)); fi
  done
  wait
  NEXT=()
  for p in "${PENDING[@]}"; do
    [ -f "$WORK/$p.state" ] || NEXT+=("$p")
  done
  PENDING=("${NEXT[@]}")
done

echo
if [ "${#PENDING[@]}" -gt 0 ]; then
  echo "UNVERIFIED after $PASSES pass(es): ${PENDING[*]}"
  exit 1
fi
echo "every project's read-back matched the local log on $RELAY"
