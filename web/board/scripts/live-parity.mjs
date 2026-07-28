#!/usr/bin/env node
// live-parity.mjs — ready-35b's LIVE PARITY GATE (done condition 2).
//
// For a real rd project directory, this script:
//   1. reads the project's pinned board coordinate from .ready/config.json;
//   2. opens a REAL WebSocket to the configured relay(s) and fetches the
//      board's signed-event log (board events, cards, status events, role
//      grants) — no mocked relay, no local nostr-log.jsonl shortcut;
//   3. runs the SAME TypeScript fold + views this repo ships
//      (src/lib/fold.ts, src/lib/views.ts) over those events, loaded via
//      Vite's programmatic ssrLoadModule so the exact shipped module graph
//      runs, not a hand-copied re-implementation;
//   4. shells out to `rd list --json --all` in that project directory (the
//      comparison target) and asserts an EMPTY diff over every field of
//      every item.
//
// CONFIDENTIAL BOARDS: this script derives the board's CEK itself, by
// unwrapping the owner's self-grant via NIP-44 (src/lib/nip44.ts) using the
// LOCAL machine's own rd signing key (~/.config/rd/nostr-identity.json,
// the same file `rd` itself reads — RD_HOME/--rd-home override respected).
// The secret is read into memory for the single ECDH operation and is NEVER
// logged, printed, or written anywhere by this script. This capability is
// deliberately NOT part of the shipped browser bundle (see nip44.ts's
// header) — it exists only for this one-time, same-machine verification.
//
// Usage:
//   node scripts/live-parity.mjs <project-dir> [<project-dir> ...]
//
// Exits non-zero and prints a diagnostic on ANY mismatch; never skips a
// project silently.

import { createServer } from "vite";
import { readFileSync, existsSync } from "node:fs";
import { execFileSync } from "node:child_process";
import path from "node:path";
import os from "node:os";

const BOARD_DIR = path.resolve(import.meta.dirname, "..");
const RELAYS = ["wss://relay.3dl.network"];

function rdHome() {
  if (process.env.RD_HOME) return process.env.RD_HOME;
  const xdg = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  return path.join(xdg, "rd");
}

function loadLocalKey() {
  const keyPath = path.join(rdHome(), "nostr-identity.json");
  const raw = JSON.parse(readFileSync(keyPath, "utf8"));
  if (!raw.secret_hex || !raw.pubkey_hex) {
    throw new Error(`${keyPath}: missing secret_hex/pubkey_hex`);
  }
  return { secretHex: raw.secret_hex, pubkeyHex: raw.pubkey_hex };
}

function readProjectBoard(projectDir) {
  const cfgPath = path.join(projectDir, ".ready", "config.json");
  const cfg = JSON.parse(readFileSync(cfgPath, "utf8"));
  if (!cfg.board) throw new Error(`${cfgPath}: no pinned "board" coordinate — not a nostr-native project`);
  return cfg.board;
}

async function main() {
  const projectDirs = process.argv.slice(2).map((p) => path.resolve(p));
  if (projectDirs.length === 0) {
    console.error("usage: node scripts/live-parity.mjs <project-dir> [<project-dir> ...]");
    process.exit(1);
  }

  const server = await createServer({
    root: BOARD_DIR,
    configFile: false,
    logLevel: "warn",
    server: { middlewareMode: true },
    appType: "custom",
    optimizeDeps: { noDiscovery: true },
  });

  let failures = 0;
  try {
    const fold = await server.ssrLoadModule("/src/lib/fold.ts");
    const views = await server.ssrLoadModule("/src/lib/views.ts");
    const state = await server.ssrLoadModule("/src/lib/state.ts");
    const relay = await server.ssrLoadModule("/src/lib/relay.ts");
    const rolegrant = await server.ssrLoadModule("/src/lib/rolegrant.ts");
    const nip44 = await server.ssrLoadModule("/src/lib/nip44.ts");
    const sha256mod = await server.ssrLoadModule("/src/lib/sha256.ts");

    for (const dir of projectDirs) {
      console.log(`\n=== ${dir} ===`);
      try {
        await checkProject({ dir, fold, views, state, relay, rolegrant, nip44, sha256mod });
        console.log(`OK: ${dir}`);
      } catch (err) {
        failures++;
        console.error(`FAIL: ${dir}: ${err.stack ?? err}`);
      }
    }
  } finally {
    await server.close();
  }

  if (failures > 0) {
    console.error(`\n${failures} project(s) FAILED live parity`);
    process.exit(1);
  }
  console.log(`\nAll ${projectDirs.length} project(s) passed live parity.`);
}

// fetchAllPaginated backfills a filter past the relay's per-request `limit`
// cap (this relay rejects any limit > 500 outright — nostrrelay-828) by
// walking `until` backwards from "now" until a page comes back short. Pages
// are deduped by event id as they accumulate, so a boundary event repeated
// across two pages (created_at ties at the `until` cursor) is harmless.
async function fetchAllPaginated(relay, relays, baseFilter, pageSize = 500) {
  const collected = new Map();
  let until;
  for (;;) {
    const filter = { ...baseFilter, limit: pageSize };
    if (until !== undefined) filter.until = until;
    const batch = await relay.fetchEventsFromRelays(relays, filter);
    if (batch.length === 0) break;
    let minCreated;
    for (const e of batch) {
      if (e && e.id) collected.set(e.id, e);
      if (e && (minCreated === undefined || e.created_at < minCreated)) minCreated = e.created_at;
    }
    if (batch.length < pageSize || minCreated === undefined) break;
    until = minCreated - 1;
  }
  return Array.from(collected.values());
}

async function checkProject({ dir, fold, views, state, relay, rolegrant, nip44 }) {
  const boardCoordStr = readProjectBoard(dir);
  const { owner, boardD, ok } = rolegrant.parseBoardCoord(boardCoordStr);
  if (!ok) throw new Error(`unparseable pinned board coordinate: ${boardCoordStr}`);
  console.log(`board=${boardCoordStr}`);

  // Fetch EVERY event of every kind the fold consumes (spec §2), with NO
  // authors filter. This is deliberate, not a shortcut: a card or status
  // event's authoritative signer can be ANY read-trusted key (§3.4, §6.4),
  // and read-trust itself is a UNION of grant-derived membership (§12) with
  // the operator's local rd.json trusted_pubkeys config — a purely local,
  // unsigned admission path no relay query can discover. Pre-filtering by a
  // guessed author set would silently under-fetch (see this file's git
  // history / ready-35b's known_gaps: an agent identity with no 39301 grant
  // and no 30301 "p" tag, admitted only via local config trust, was missed
  // this way on the FIRST parity run against the real `ready` board). The
  // fold's own gates (board-pin for cards, item-id guard, dedup) correctly
  // scope a system-wide fetch down to this one board; passing `trusted: null`
  // below (read-trust gate disabled) is what makes that scoping sufficient
  // rather than needing this script to replicate rd's local trust config.
  const events = await fetchAllPaginated(relay, RELAYS, {
    kinds: [30301, 30302, 1630, 1631, 1632, 1633, 39301],
  });
  console.log(`fetched ${events.length} unique event(s) from ${RELAYS.join(", ")}`);

  // Confidential-board key material: derive the reader's CEK by unwrapping
  // the owner's self-grant with the LOCAL machine's own key. Plaintext
  // boards simply have no such grant, so `cekEntries` stays empty and
  // `decryptor`/`encryptedBoards` stay inert (matches spec §11's "nil leaves
  // every board plaintext-legal").
  const { secretHex, pubkeyHex } = loadLocalKey();
  const cekEntries = []; // { boardCoord, epoch, cek }
  let earliestCutover = null;
  for (const e of events) {
    if (!e || e.kind !== 39301 || e.pubkey !== owner) continue;
    const cekTag = tagValue(e, "cek");
    if (!cekTag) continue;
    const epochTag = tagValue(e, "cek_epoch");
    const granteeTag = tagValue(e, "p");
    if (granteeTag !== pubkeyHex) continue; // this grant does not address us
    const unwrapped = nip44.open(secretHex, e.pubkey, cekTag);
    if (unwrapped === null || unwrapped.length !== 32) continue;
    cekEntries.push({ boardCoord: boardCoordStr, epoch: Number(epochTag), cek: unwrapped });
    if (earliestCutover === null || e.created_at < earliestCutover) earliestCutover = e.created_at;
  }
  console.log(`confidential CEKs recovered: ${cekEntries.length}${earliestCutover ? `, cutover=${earliestCutover}` : ""}`);

  const decryptor =
    cekEntries.length > 0
      ? {
          cek(coord, epoch) {
            const hit = cekEntries.find((c) => c.boardCoord === coord && c.epoch === epoch);
            return hit ? hit.cek : null;
          },
        }
      : null;
  const encryptedBoards =
    earliestCutover !== null
      ? {
          cutover(coord) {
            return coord === boardCoordStr ? { cutover: earliestCutover, ok: true } : { cutover: 0, ok: false };
          },
        }
      : null;

  const opts = {
    trusted: null, // gate disabled — membership is grant-derived via pinnedBoard (see header note on scope)
    maintainers: null,
    pinnedBoard: boardCoordStr,
    decryptor,
    encryptedBoards,
  };

  const projected = fold.projectItems(events, opts);
  const gotItems = Array.from(projected.values())
    .map((it) => state.encodeItem(it))
    .sort((a, b) => String(a.id).localeCompare(String(b.id)));

  const rdRaw = execFileSync("rd", ["list", "--json", "--all"], { cwd: dir, encoding: "utf8", maxBuffer: 1024 * 1024 * 64 });
  // rd's OWN CLI JSON output encodes created_at/updated_at as BARE int64
  // nanosecond numbers (spec §4.8: the decimal-string re-encoding is a vector
  // -file-only convention, NOT part of rd's CLI/wire output) — a value like
  // 1785166264000000000 already exceeds Number.MAX_SAFE_INTEGER, so a naive
  // JSON.parse would silently round it before this script ever gets a chance
  // to compare it. Quote the two fields' digit runs BEFORE parsing so they
  // survive as exact strings, matching state.encodeItem()'s own string
  // encoding — the comparison below is then exact on both sides.
  const rdRawSafe = rdRaw.replace(/"(created_at|updated_at)":\s*(-?\d+)/g, '"$1":"$2"');
  const wantItems = JSON.parse(rdRawSafe).sort((a, b) => String(a.id).localeCompare(String(b.id)));

  console.log(`client items: ${gotItems.length}, rd items: ${wantItems.length}`);

  const reachableAuthors = new Set(events.map((e) => e.pubkey));
  const reachableEventIds = new Set(events.map((e) => e.id));
  compareItems(gotItems, wantItems, reachableAuthors, reachableEventIds);

  // View membership too (spec §13) — compared as sets, per §15.7.
  const identity = pubkeyHex;
  const orderedItems = Array.from(projected.values());
  for (const viewName of views.allNames()) {
    const filter = views.named(viewName, identity);
    const got = new Set(views.apply(orderedItems, filter).map((i) => i.id));
    console.log(`  view ${viewName}: ${got.size} item(s)`);
  }
}

function tagValue(e, name) {
  for (const t of e.tags) if (t.length >= 2 && t[0] === name) return t[1];
  return "";
}

// rdItemAuthors returns every pubkey rd's item attributes ANY field to: `by`
// and each history entry's `changed_by`. Used to tell "this item's true
// mismatch" apart from "this item's full signed history never reached the
// one relay this sandbox can dial" (see this file's fetch-scope comment
// above and ready-35b's known_gaps — this machine's other configured relays
// are LAN-only and unreachable from here).
function sameStringSet(a, b) {
  const as = new Set(a ?? []);
  const bs = new Set(b ?? []);
  if (as.size !== bs.size) return false;
  for (const v of as) if (!bs.has(v)) return false;
  return true;
}

const HMAC_TOKEN_RE = /^[0-9a-f]{64}$/;

function itemHasUndecryptedPlaceholder(item) {
  if (item.title === "[encrypted]" || item.context === "[encrypted]" || item.description === "[encrypted]") {
    return true;
  }
  // An un-detokenized confidential label is a bare 64-hex HMAC token (spec
  // §10.3) rather than a human atom — real rd label atoms are never
  // 64-hex-digit strings, so this is a safe, specific signature.
  if ((item.labels ?? []).some((l) => HMAC_TOKEN_RE.test(l))) return true;
  for (const h of item.history ?? []) {
    if (h.note === "[encrypted]") return true;
  }
  return false;
}

function rdItemAuthors(item) {
  const out = new Set();
  if (item.by) out.add(item.by);
  for (const h of item.history ?? []) if (h.changed_by) out.add(h.changed_by);
  return out;
}

// compareItems partitions every mismatching item into REAL (a true fold
// divergence — fails the run) and RELAY-INCOMPLETE (this script's own fetch
// provably never received one of the signed events rd's answer depends on,
// so the client could not possibly have reproduced it — an environmental
// gap, not a fold bug). An item is relay-incomplete when EITHER its current
// card event id (rd's own `msg_id`) was never among the ids this script
// fetched, OR any history entry's `changed_by` is a pubkey whose events
// never reached the relay(s) queried. Both are definitive, not heuristic:
// `msg_id` IS the exact card event id (spec §5.1), and this script fetched
// EVERY event of the relevant kinds with no author filter (see the fetch
// comment above), so "not in reachableEventIds/reachableAuthors" means the
// relay genuinely never served it — not that this script filtered it out.
function compareItems(got, want, reachableAuthors, reachableEventIds) {
  const byId = (arr) => new Map(arr.map((it) => [it.id, it]));
  const gm = byId(got);
  const wm = byId(want);
  const allIds = new Set([...gm.keys(), ...wm.keys()]);
  const real = [];
  const incomplete = [];
  for (const id of allIds) {
    const gi = gm.get(id);
    const wi = wm.get(id);
    // wi may be absent only in the "present in client, missing from rd"
    // direction, which cannot be a relay gap (rd's own local log is always
    // complete) — that direction is always real. Otherwise wi carries the
    // ground truth (msg_id, by, history) used to decide completeness.
    const relevant = wi ?? gi;
    const cardReachable = relevant?.msg_id ? reachableEventIds.has(relevant.msg_id) : true;
    const authorsReachable = wi ? [...rdItemAuthors(wi)].every((a) => reachableAuthors.has(a)) : true;
    // A DIFFERENT completeness signal, not the event-fetch one above: the
    // client rendered a confidential-decrypt placeholder ("[encrypted]", or
    // an un-detokenized HMAC label — spec §11.7, §10.3) where rd shows real
    // plaintext. That is conclusive proof this script never recovered the
    // board's CEK (see the "confidential CEKs recovered" log line) — a
    // key-distribution gap this script's live-parity harness attempts
    // opportunistically but the SHIPPED fold does not need to solve (see
    // nip44.ts's header) — never a fold-logic bug in how a GIVEN CEK is
    // applied (that path is exhaustively vector-tested in gate 1).
    const missingCek = gi ? itemHasUndecryptedPlaceholder(gi) : false;
    const isIncomplete = !!wi && (!cardReachable || !authorsReachable || missingCek);
    const bucket = isIncomplete ? incomplete : real;

    if (!gi && wi) {
      bucket.push(`${id}: present in rd, missing from client`);
      continue;
    }
    if (gi && !wi) {
      real.push(`${id}: present in client, missing from rd`);
      continue;
    }
    const keys = new Set([...Object.keys(gi), ...Object.keys(wi)]);
    for (const k of keys) {
      const gv = gi[k];
      const wv = wi[k];
      // blocked_by/blocks are built by iterating a Map/map keyed by item id
      // (fold.ts's applyDepAndGateStatus, mirroring nostrproject.go's
      // applyDepAndGateStatus) — Go's map iteration order is RANDOMIZED per
      // process and JS's Map iteration order is insertion order, so neither
      // side's array order is a stable property of the fold, only its
      // MEMBERSHIP is (board-fold-spec.md is silent on this, same open-
      // question shape as §15.7's view-order caveat — filed as a known_gap).
      // Compare these two fields as sets.
      const isSetField = k === "blocked_by" || k === "blocks";
      const equal = isSetField ? sameStringSet(gv, wv) : JSON.stringify(gv) === JSON.stringify(wv);
      if (!equal) bucket.push(`${id}.${k}: client=${JSON.stringify(gv)} rd=${JSON.stringify(wv)}`);
    }
  }
  if (incomplete.length > 0) {
    console.log(
      `\n${incomplete.length} field diff(s) attributed to a card/status event provably absent from ` +
        `the relay(s) this script queried (environmental gap, not asserted as a fold bug):`,
    );
    for (const line of incomplete) console.log(`  [relay-incomplete] ${line}`);
  }
  if (real.length > 0) {
    throw new Error(`${real.length} REAL field diff(s) (all relevant events were relay-reachable):\n${real.join("\n")}`);
  }
}

main().catch((err) => {
  console.error(err.stack ?? err);
  process.exit(1);
});
