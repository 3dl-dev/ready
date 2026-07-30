#!/usr/bin/env node
// archive-stray-boards.mjs — ready-153's sweep for boards this repo's own tests
// left in the owner's portfolio.
//
// WHAT CHANGED IN THE CLOSING ROUND, and why both changes are the same defect:
//
//  1. THE WALK IS NO LONGER RE-IMPLEMENTED HERE. This file used to carry its
//     own copy of the relay walk, and that copy's REQ helper resolved with a
//     partial page on its 45s timeout instead of rejecting. A relay that
//     answered nothing therefore printed "0 unarchived stray(s) … nothing to
//     archive" and exited 0 — a clean bill of health from a dead relay, the
//     exact silent-relay defect throwaway-board.mjs's `wsReq` was fixed for.
//     Two copies of a walk means one of them is always the unfixed one, so now
//     there is one: `fetchBoardEvents`/`latestBoardsOwnedBy` are imported.
//
//  2. THE STRAY SET IS DERIVED FROM THE TREE, not typed here. It used to be a
//     five-prefix list — the files that round's diff had touched. Walked live
//     on 2026-07-30 that list covered 7 of 17 strays and missed 10
//     `ready-livetest-*`, which come from a Go live-relay test nobody had
//     looked at. See stray-boards.mjs.
//
// Both are the same mistake: scoping a hygiene check to what the author already
// knew about, so it reports clean for the wrong reason.
//
// DRY RUN BY DEFAULT. Pass --archive to actually publish the archive events.
// Every board this key owns is printed with its classification and, for a
// stray, the source line that claims it — so the archive list is auditable
// before anything is written. `rd board unarchive <coord>` reverses any of it.
//
// Usage: node scripts/archive-stray-boards.mjs [--relay wss://…] [--archive]

import { execFileSync } from "node:child_process";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, rmSync } from "node:fs";
import os from "node:os";
import path from "node:path";

import { archiveBoard, fetchBoardEvents, latestBoardsOwnedBy } from "./throwaway-board.mjs";
import { classify, strayPatterns } from "./stray-boards.mjs";

const REPO_ROOT = path.resolve(import.meta.dirname, "../../..");
const argv = process.argv.slice(2);
const RELAY = argv.includes("--relay") ? argv[argv.indexOf("--relay") + 1] : "wss://relay.3dl.network";
const DO_ARCHIVE = argv.includes("--archive");

const log = (...a) => console.log(...a);

function rdHome() {
  if (process.env.RD_HOME) return process.env.RD_HOME;
  const xdg = process.env.XDG_CONFIG_HOME || path.join(os.homedir(), ".config");
  return path.join(xdg, "rd");
}

/**
 * protectedBoardDs are board-ds this sweep will never call a stray, whatever a
 * derived pattern says. `pkg/sync/nostroutbound.go` names this repo's own
 * production board in `reservedProductionBoardD`, and several live-relay tests
 * write that same literal into a `BoardD:` field — so the derivation legitimately
 * produces `^ready$`, and without this shield the sweep's first act would be to
 * archive the board it is being run to clean up.
 */
export function protectedBoardDs(repoRoot) {
  const src = readFileSync(path.join(repoRoot, "pkg/sync/nostroutbound.go"), "utf8");
  const m = /reservedProductionBoardD\s*=\s*"([^"]+)"/.exec(src);
  if (!m) throw new Error("pkg/sync/nostroutbound.go: could not read reservedProductionBoardD — refusing to sweep blind");
  return [m[1]];
}

async function main() {
  const idPath = path.join(rdHome(), "nostr-identity.json");
  const identity = JSON.parse(readFileSync(idPath, "utf8"));
  const owner = identity.pubkey_hex;

  const patterns = strayPatterns(REPO_ROOT).filter((p) => p.usable);
  const shielded = protectedBoardDs(REPO_ROOT);
  log(`${patterns.length} board-d pattern(s) derived from the tree; protected: ${shielded.join(", ")}`);

  log(`\nwalking ${RELAY} for every kind-30301 board owned by ${owner}`);
  // fetchBoardEvents REJECTS on a relay that never sends EOSE — a silent relay
  // must never read as "this key owns nothing".
  const owned = latestBoardsOwnedBy(await fetchBoardEvents(RELAY), owner);
  const unarchived = [...owned.values()].filter((b) => !b.archived).sort((a, b) => a.boardD.localeCompare(b.boardD));
  log(`${owned.size} board(s) for this key, ${unarchived.length} unarchived`);

  const { strays, unclaimed } = classify(unarchived, patterns, shielded);

  log(`\n${unclaimed.length} unarchived board(s) NOT claimed by any harness in this tree — left alone:`);
  for (const b of unclaimed) log(`  ${b.boardD}${b.protected ? "   (protected)" : ""}`);

  log(`\n${strays.length} unarchived stray(s) claimed by a harness in this tree:`);
  for (const b of strays) log(`  ${b.boardD}   <- ${b.claimedBy.map((p) => p.source).join(", ")}`);

  if (strays.length === 0) {
    log("\nnothing to archive.");
    return;
  }
  if (!DO_ARCHIVE) {
    log("\ndry run — pass --archive to publish the archive events.");
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
        archiveBoard({ rdBin, cwd: dir, home, relay: RELAY, coord: b.coord });
        log(`  archived ${b.coord}`);
      } catch (err) {
        failures++;
        console.error(`  FAILED to archive ${b.coord}: ${err.message}`);
      }
    }

    // READ THE MARKERS BACK. `rd board archive` exiting 0 says the command ran,
    // not that the relay took the event — the same read-back the per-run guard
    // does, for the same reason.
    const after = latestBoardsOwnedBy(await fetchBoardEvents(RELAY), owner);
    const stillLive = strays.filter((b) => !after.get(b.coord)?.archived);
    for (const b of stillLive) console.error(`  NOT ARCHIVED ON THE RELAY: ${b.coord}`);
    failures += stillLive.length;

    const nowUnarchived = [...after.values()].filter((b) => !b.archived).length;
    log(`\nowner's unarchived board count: ${unarchived.length} before, ${nowUnarchived} after`);
    if (failures > 0) {
      console.error(`${failures} archive(s) failed`);
      process.exit(1);
    }
    log(`archived ${strays.length}/${strays.length} stray board(s)`);
  } finally {
    rmSync(tmp, { recursive: true, force: true });
  }
}

main().catch((err) => {
  console.error(err.stack ?? err);
  process.exit(1);
});
