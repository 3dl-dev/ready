#!/usr/bin/env node
// archive-stray-boards.mjs — ready-153's one-shot cleanup for the live
// harnesses' own throwaway boards.
//
// THE PROBLEM: live-write-roundtrip.mjs and live-roundtrip-both-ways.mjs each
// provision a fresh, PUBLIC-visible board on every run and (as of ready-153)
// archive it themselves in a `finally` clause. Every run BEFORE that fix left
// its board behind, permanently, in the owner's `rd board` portfolio — 44 of
// them, measured 2026-07-30.
//
// WHAT THIS DOES: walks wss://relay.3dl.network for every kind-30301 board
// definition owned by the LOCAL machine's rd key, finds the ones whose "d" tag
// matches one of the naming prefixes those harnesses (and their earlier,
// renamed incarnations) have used, and archives every one that is not already
// archived — via the real `rd board archive`, so the marker is the exact
// signed event the CLI itself publishes; nothing is invented here.
//
// NO `authors` FILTER (relay measurement discipline: wss://relay.3dl.network
// silently under-returns on one — measured 42/56 vs 56/56 for a paged
// kind-only walk, same relay, same run, ready-5c5). The walk below is
// kind-only, paged backwards with `until` at limit 500, exactly as
// live-portfolio.mjs's own oracle does; ownership is applied CLIENT-SIDE on
// the events that came back.
//
// THE PREFIX LIST IS THE NAMING CONVENTION ITSELF, not a stray count: every
// throwaway board these harnesses (and their prior item-numbered names) have
// ever provisioned derives its "d" tag from one of these five prefixes. HOW
// MANY boards match is always counted live against the relay below — never
// asserted as a fixed number here or anywhere this script's output is read.
//
// Usage: node scripts/archive-stray-boards.mjs [--relay wss://…] [--dry-run]
// Exits non-zero if any matched board fails to archive.

import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";

const REPO_ROOT = path.resolve(import.meta.dirname, "../../..");
const argv = process.argv.slice(2);
const RELAY = argv.includes("--relay") ? argv[argv.indexOf("--relay") + 1] : "wss://relay.3dl.network";
const DRY_RUN = argv.includes("--dry-run");

const KIND_BOARD = 30301;
// The relay caps a single REQ at 500 (measured); paging less would just page
// more often.
const PAGE_LIMIT = 500;

// b2blive/c191live: live-write-roundtrip.mjs (public/confidential, ready-b2b
//   then ready-191). b4359/c4359: live-roundtrip-both-ways.mjs (ready-4359,
//   same public/confidential split). s48f: live-stranger-walk.mjs (ready-48f,
//   not yet merged to main but already run against the live relay).
const STRAY_PREFIXES = ["b2blive", "b4359", "c191live", "c4359", "s48f"];

const log = (...a) => console.log(...a);

function rdHome() {
  if (process.env.RD_HOME) return process.env.RD_HOME;
  const xdg = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  return path.join(xdg, "rd");
}

function reqOnce(relay, filter) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(relay);
    const out = [];
    const sub = `strays${Math.random().toString(36).slice(2, 10)}`;
    const t = setTimeout(() => {
      try {
        ws.close();
      } catch {
        /* closed */
      }
      resolve(out);
    }, 45000);
    ws.onopen = () => ws.send(JSON.stringify(["REQ", sub, filter]));
    ws.onmessage = (m) => {
      const f = JSON.parse(m.data);
      if (f[0] === "EVENT" && f[1] === sub) out.push(f[2]);
      else if (f[0] === "EOSE" && f[1] === sub) {
        clearTimeout(t);
        try {
          ws.send(JSON.stringify(["CLOSE", sub]));
          ws.close();
        } catch {
          /* closed */
        }
        resolve(out);
      }
    };
    ws.onerror = () => {
      clearTimeout(t);
      reject(new Error(`relay ${relay}: connection failed`));
    };
  });
}

/**
 * discoverBoards walks the relay for EVERY kind-30301 event (no `authors`
 * filter — see this file's header) and returns the latest-per-coordinate
 * definition for each one authored by `ownerPubkey`.
 */
async function discoverBoards(relay, ownerPubkey) {
  const seen = new Map();
  let until;
  for (let page = 0; page < 40; page++) {
    const filter = { kinds: [KIND_BOARD], limit: PAGE_LIMIT };
    if (until !== undefined) filter.until = until;
    const got = await reqOnce(relay, filter);
    let added = 0;
    let oldest = until;
    for (const e of got) {
      if (!seen.has(e.id)) {
        seen.set(e.id, e);
        added++;
      }
      if (oldest === undefined || e.created_at < oldest) oldest = e.created_at;
    }
    log(`  page ${page + 1}: ${got.length} events, ${added} new, ${seen.size} total`);
    if (added === 0 || got.length < PAGE_LIMIT) break;
    if (oldest === undefined) break;
    until = oldest - 1;
  }
  const mine = [];
  for (const e of seen.values()) {
    if (e.pubkey !== ownerPubkey) continue;
    const d = (e.tags ?? []).find((t) => t[0] === "d")?.[1];
    if (!d) continue;
    const archived = ((e.tags ?? []).find((t) => t[0] === "archived")?.[1] ?? "") !== "";
    mine.push({ coord: `${KIND_BOARD}:${e.pubkey}:${d}`, boardD: d, archived, createdAt: e.created_at });
  }
  // Latest-wins per coordinate — an addressable (30301) event means the relay
  // itself only ever serves one per (kind, pubkey, d), but this walk pages
  // backwards through history and can see a superseded copy too.
  const byCoord = new Map();
  for (const b of mine) {
    const prev = byCoord.get(b.coord);
    if (!prev || b.createdAt > prev.createdAt) byCoord.set(b.coord, b);
  }
  return [...byCoord.values()];
}

async function main() {
  const idPath = path.join(rdHome(), "nostr-identity.json");
  const identity = JSON.parse(readFileSync(idPath, "utf8"));
  const owner = identity.pubkey_hex;

  log(`walking ${RELAY} for every kind-30301 board owned by ${owner}`);
  const all = await discoverBoards(RELAY, owner);
  log(`\n${all.length} board(s) total on the relay for this key`);

  const strays = all.filter((b) => !b.archived && STRAY_PREFIXES.some((p) => b.boardD.startsWith(p)));
  log(`${strays.length} unarchived stray(s) matching [${STRAY_PREFIXES.join(", ")}]`);
  for (const b of strays) log(`  ${b.boardD}`);

  if (strays.length === 0) {
    log("\nnothing to archive.");
    return;
  }

  if (DRY_RUN) {
    log("\n--dry-run: not archiving.");
    return;
  }

  // A real nostr-native project directory is required to run `rd board
  // archive` (it supplies the signing key, local durability log, and
  // configured relays) but need NOT pin any of the stray boards' own
  // coordinates — the command takes its target as an explicit argument, and
  // the archive event for a foreign board-d lands harmlessly in this
  // scratch project's own local log (see board_archive.go's header comment).
  const tmp = mkdtempSync(path.join(os.tmpdir(), "rd-archive-strays-"));
  try {
    const rdBin = path.join(tmp, "rd");
    execFileSync("go", ["build", "-o", rdBin, "./cmd/rd"], { cwd: REPO_ROOT, stdio: "inherit" });

    const home = path.join(tmp, "home");
    mkdirSync(home, { recursive: true });
    writeFileSync(path.join(home, "nostr-identity.json"), JSON.stringify(identity), { mode: 0o600 });

    const dir = path.join(tmp, "scratch");
    mkdirSync(path.join(dir, ".ready"), { recursive: true });
    writeFileSync(
      path.join(dir, ".ready", "config.json"),
      JSON.stringify({ project_name: "archive-strays-scratch", board: strays[0].coord, public: true }, null, 2),
    );

    let failures = 0;
    for (const b of strays) {
      try {
        execFileSync(rdBin, ["board", "archive", b.coord], {
          cwd: dir,
          env: { ...process.env, RD_HOME: home, RD_NOSTR_RELAY_URL: RELAY },
          encoding: "utf8",
          stdio: ["ignore", "pipe", "pipe"],
        });
        log(`  archived ${b.coord}`);
      } catch (err) {
        failures++;
        console.error(`  FAILED to archive ${b.coord}: ${err.message}`);
      }
    }
    if (failures > 0) {
      console.error(`\n${failures}/${strays.length} archive(s) failed`);
      process.exit(1);
    }
    log(`\narchived ${strays.length}/${strays.length} stray board(s)`);
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err.stack ?? err);
  process.exit(1);
});
