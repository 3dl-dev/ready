// receipt.mjs — a committed, machine-readable record that a live script really ran
// (ready-cc2, epic ready-35a).
//
// THE PROBLEM THIS SOLVES. M2's load-bearing done conditions — the whole gate loop
// (ready-fd2), the confidential wire assertions (ready-191), the gate rail's six
// clauses (ready-186), the reverse direction (ready-4359) — are proven by
// scripts/live-*.mjs against wss://relay.3dl.network. Board CI deliberately does NOT
// run them: a live-network test inside a hermetic suite is a flakiness hazard, and
// that policy is correct and is not what this file disputes.
//
// The residual it DOES address: before this, the only record that those scripts ever
// passed was transcript text pasted into commit messages. Prose is not evidence — a
// reviewer cannot tell a real run from a recalled one, and nothing re-runs the script
// so a regression in a live-only property stays invisible until a human happens to
// run it by hand. ready-e51 measured one instance of exactly that blind spot.
//
// WHAT A RECEIPT IS AND IS NOT. It is a claim, signed by nothing, that a named script
// passed a named set of checks at a named commit on a named host against a named
// relay. It does NOT make the claim true — a determined author could hand-write one.
// What it does is make the claim STRUCTURED, so it can be checked mechanically
// instead of read charitably:
//
//   - `commit` must be a real commit in this repository (receipts_test.go verifies
//     it resolves), so a receipt cannot cite a run that never had a tree.
//   - `dirty` records whether the working tree was modified at run time. A pass on
//     uncommitted code proves less than a pass on a committed tree, and hiding that
//     distinction is how a receipt becomes decoration.
//   - `checks` carries every assertion's name and outcome, so a script that quietly
//     STOPPED asserting a clause produces a receipt whose shape no longer matches its
//     own source — which receipts_test.go catches without touching the network.
//
// FRESHNESS IS FOR THE REVIEWER, NOT FOR CI. The receipt records the commit it ran
// at; how far main has moved since is a judgement call a human makes on a PR. It is
// deliberately NOT a red test, because failing every PR until someone re-runs a
// network script is precisely the gating the board-ci policy rules out.

import { execFileSync } from "node:child_process";
import { mkdirSync, writeFileSync } from "node:fs";
import os from "node:os";
import path from "node:path";

/**
 * writeReceipt records one live script run under web/board/receipts/.
 *
 * @param {object} o
 * @param {string} o.script      script filename, e.g. "live-write-roundtrip.mjs"
 * @param {string} o.repoRoot    absolute path to the repo root
 * @param {string} o.boardDir    absolute path to web/board
 * @param {string} o.relay       relay URL the run went against
 * @param {string} o.boardCoord  the throwaway board coordinate the run used
 * @param {string} [o.mode]      free-form run mode, e.g. "public" | "confidential"
 * @param {Array<{name: string, ok: boolean}>} o.checks
 * @returns {string} the path written
 */
export function writeReceipt({ script, repoRoot, boardDir, relay, boardCoord, mode, checks }) {
  const git = (...args) => {
    try {
      return execFileSync("git", args, { cwd: repoRoot, encoding: "utf8" }).trim();
    } catch {
      return "";
    }
  };
  const receipt = {
    script,
    // Written by the script itself at the end of a passing run; regenerate by
    // re-running the script, never by hand.
    ran_at: new Date().toISOString(),
    commit: git("rev-parse", "HEAD"),
    // A pass on a modified tree is weaker evidence than a pass on a clean one.
    dirty: git("status", "--porcelain").length > 0,
    host: os.hostname(),
    relay,
    board_coord: boardCoord,
    mode: mode ?? "",
    checks: checks.map((c) => ({ name: c.name, ok: !!c.ok })),
    passed: checks.filter((c) => c.ok).length,
    total: checks.length,
  };
  const dir = path.join(boardDir, "receipts");
  mkdirSync(dir, { recursive: true });
  const out = path.join(dir, script.replace(/\.mjs$/, "") + ".json");
  writeFileSync(out, JSON.stringify(receipt, null, 2) + "\n");
  return out;
}
