/**
 * throwaway-board.mjs — the cleanup contract every live harness that runs
 * `rd init` is bound to (ready-153).
 *
 * THE PROBLEM. A live harness provisions a fresh kind-30301 board per run so it
 * has something disposable to write to. Before this module, those boards were
 * never removed: 44 of the 75 nodes in the owner's portfolio were per-run
 * throwaway boards published by the test suite itself, and every future run
 * added another, permanently, to the product's primary view.
 *
 * WHY THIS IS A MODULE AND NOT A LINE IN EACH HARNESS. The first two attempts
 * at this fix were a `finally`-block one-liner per script. Both were untestable
 * in CI (a live harness needs Chromium, a real relay and a real key), both
 * drifted — one harness merged with no cleanup at all — and both had the same
 * three silent leak paths:
 *
 *   1. a failed `rd board archive` was logged as a WARNING and the run still
 *      exited 0, leaving the stray behind exactly as if no cleanup existed;
 *   2. nothing read the archived marker back, so an `rd board archive` that
 *      exits 0 without the event reaching the relay looked like success;
 *   3. the cleanup was guarded on a local `coord` variable assigned from
 *      `JSON.parse(rd init --json)`, so any failure at or before that parse —
 *      the exact case a `finally` block exists for — skipped the archive while
 *      the board itself was already published and permanent.
 *
 * All three are closed here, in ONE place, by deriving everything from the
 * RELAY rather than from the harness's local variables:
 *
 *   - what to archive is whatever the relay says this key owns under the
 *     board-ds this run registered — no local `coord` required, so a run that
 *     dies mid-`rd init` still cleans up (leak 3);
 *   - `rd board archive` failing is a recorded failure, never a warning
 *     (leak 1);
 *   - the archived marker is READ BACK off the relay after the command, and
 *     the run fails if it is not there, whatever the command's exit code
 *     said (leak 2);
 *   - and the whole run is bracketed by a count of this key's unarchived
 *     boards taken before it starts and after cleanup finishes. They must
 *     agree. That is the item's done condition as an executable assertion.
 *
 * Every one of those behaviours is covered hermetically by
 * throwaway-board.test.mjs (vitest, in CI via board-ci.yml) against a fake
 * relay and a fake `rd`. Delete the `exec(...)` call in archiveBoard below and
 * that suite goes red — which is the point: before this module, deleting the
 * cleanup turned nothing red anywhere.
 *
 * RELAY MEASUREMENT DISCIPLINE. The walk below is kind-only, paged backwards
 * with `until` at limit 500, and NEVER uses an `authors` filter: an `authors`
 * filter silently under-returns on wss://relay.3dl.network (measured 42/56 vs
 * 56/56 for the same walk, ready-5c5). Ownership is filtered client-side after
 * the walk instead, and the test suite asserts every filter this module sends
 * carries no `authors` key.
 */

import { execFileSync } from "node:child_process";

export const KIND_BOARD = 30301;

/** PAGE_LIMIT/MAX_PAGES: 500 per page (the relay's practical cap) walked
 * backwards by `until`; 40 pages is 20k board events, far past this key's
 * real count, and bounds a pathological relay. */
export const PAGE_LIMIT = 500;
export const MAX_PAGES = 40;

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/**
 * wsReq issues ONE REQ against a relay and resolves with everything received
 * up to EOSE. This is the only part of the walk that touches a socket, and it
 * is injectable (`deps.req`) so the paging, dedupe, latest-wins and
 * archived-marker logic above it can be exercised without a network.
 */
export async function wsReq(relay, filter, { timeoutMs = 45000 } = {}) {
  return new Promise((resolve, reject) => {
    const ws = new WebSocket(relay);
    const out = [];
    const sub = `tbg${Math.random().toString(36).slice(2, 10)}`;
    const t = setTimeout(() => {
      try {
        ws.close();
      } catch {
        /* already closed */
      }
      resolve(out);
    }, timeoutMs);
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
          /* already closed */
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
 * fetchBoardEvents walks every kind-30301 event the relay will serve, paging
 * backwards with `until`. Kind-only by design — see the header's relay
 * measurement discipline note.
 */
export async function fetchBoardEvents(relay, { req = wsReq } = {}) {
  const seen = new Map();
  let until;
  for (let page = 0; page < MAX_PAGES; page++) {
    const filter = { kinds: [KIND_BOARD], limit: PAGE_LIMIT };
    if (until !== undefined) filter.until = until;
    const got = await req(relay, filter);
    let added = 0;
    let oldest = until;
    for (const e of got) {
      if (!seen.has(e.id)) {
        seen.set(e.id, e);
        added++;
      }
      if (oldest === undefined || e.created_at < oldest) oldest = e.created_at;
    }
    if (added === 0 || got.length < PAGE_LIMIT) break;
    if (oldest === undefined) break;
    until = oldest - 1;
  }
  return [...seen.values()];
}

/**
 * latestBoardsOwnedBy applies the two rules the board page itself applies to a
 * kind-30301 stream: latest-wins per coordinate (they are replaceable events,
 * and an archive is a REPUBLISH of the same coordinate), then read the
 * `archived` marker off that winner. Returns a Map coord -> descriptor.
 */
export function latestBoardsOwnedBy(events, ownerPubkey) {
  const byCoord = new Map();
  for (const e of events) {
    if (e.pubkey !== ownerPubkey) continue;
    const d = (e.tags ?? []).find((t) => t[0] === "d")?.[1];
    if (!d) continue;
    const coord = `${KIND_BOARD}:${e.pubkey}:${d}`;
    const prev = byCoord.get(coord);
    if (!prev || e.created_at > prev.createdAt) {
      byCoord.set(coord, {
        coord,
        boardD: d,
        createdAt: e.created_at,
        archived: ((e.tags ?? []).find((t) => t[0] === "archived")?.[1] ?? "") !== "",
      });
    }
  }
  return byCoord;
}

/** ownedBoards: the live relay's answer to "which boards does this key own,
 * and which of them are archived, right now". */
export async function ownedBoards(relay, ownerPubkey, deps = {}) {
  return latestBoardsOwnedBy(await fetchBoardEvents(relay, deps), ownerPubkey);
}

/** ownedBoardCount: how many of them are still UNARCHIVED — i.e. how many
 * nodes the owner's portfolio shows. This is the number ready-153's done
 * condition is written in terms of. */
export async function ownedBoardCount(relay, ownerPubkey, deps = {}) {
  let n = 0;
  for (const b of (await ownedBoards(relay, ownerPubkey, deps)).values()) if (!b.archived) n++;
  return n;
}

/**
 * archiveBoard runs the REAL `rd board archive` against a throwaway board.
 *
 * This one call is the entire cleanup action, deliberately in a module a
 * hermetic test can drive: delete the `exec(...)` line and every read-back and
 * board-count assertion in throwaway-board.test.mjs goes red.
 *
 * `rd board archive` republishes only the board's own kind-30301 definition
 * with an `archived` tag — every card and status event already written to the
 * board is untouched.
 */
export function archiveBoard({ rdBin, cwd, home, relay, coord, exec = execFileSync }) {
  if (!rdBin) throw new Error("no rd binary available to archive with");
  return exec(rdBin, ["board", "archive", coord], {
    cwd,
    env: { ...process.env, RD_HOME: home, RD_NOSTR_RELAY_URL: relay },
    encoding: "utf8",
    stdio: ["ignore", "pipe", "pipe"],
  });
}

/**
 * openThrowawayBoardGuard takes the BEFORE measurement and hands back the
 * handle a harness closes in its `finally` clause.
 *
 * Usage, and the shape scripts_cleanup.test.mjs enforces on every live
 * harness that runs `rd init`:
 *
 *   const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey, log });
 *   guard.expect(BOARD_D);                       // BEFORE `rd init` runs
 *   try { ...the run... } finally {
 *     failures += (await guard.close({ rdBin, cwd: projectDir, home: writerHome })).failures.length;
 *   }
 *
 * `expect` takes the board-d, not the coordinate, precisely so it can be
 * called before `rd init` — the run that dies inside `rd init` is the one that
 * leaks, and it is the one with no coordinate to hand.
 */
export async function openThrowawayBoardGuard({ relay, ownerPubkey, log = () => {}, boardDs = [], deps = {} }) {
  const expected = new Set(boardDs);
  const before = await ownedBoardCount(relay, ownerPubkey, deps);
  log(`  ready-153: ${before} unarchived board(s) owned by this key, before this run`);

  return {
    before,
    expected,
    expect(boardD) {
      expected.add(boardD);
      return boardD;
    },

    /**
     * close archives every board this run put on the relay, PROVES each one
     * carries the archived marker by reading it back, and re-checks the
     * owner's unarchived board count against the before measurement.
     *
     * Deriving the work from the relay is also what makes "the run died early"
     * correct rather than merely safe. `rd init` appends the kind-30301 board
     * event to the LOCAL log only — measured 2026-07-30: init plus `rd relay
     * flush` leaves nothing on the relay, and the board first appears there on
     * the run's first write. So a run that died before writing anything has no
     * board in the owner's portfolio to clean up, and this correctly archives
     * nothing instead of publishing an archive for a board that never existed.
     *
     * Returns { ok, before, after, archived, failures } — `failures` is a list
     * of human-readable reasons, and a harness MUST add its length to its own
     * failure count. Nothing here throws for a cleanup problem: the caller is
     * a `finally` clause and must not lose the original error.
     */
    async close({
      rdBin,
      cwd,
      home,
      exec = execFileSync,
      readBackTimeoutMs = 30000,
      readBackIntervalMs = 2000,
      now = () => Date.now(),
      wait = sleep,
    } = {}) {
      const failures = [];
      const archived = [];

      // WHAT TO ARCHIVE COMES FROM THE RELAY, never from a local `coord`
      // variable: a run that failed inside `rd init` has no coordinate but may
      // very well have published the board. Leak 3 in this file's header.
      let owned;
      try {
        owned = await ownedBoards(relay, ownerPubkey, deps);
      } catch (err) {
        failures.push(`could not read this key's boards back off ${relay}: ${err.message}`);
        return { ok: false, before, after: undefined, archived, failures };
      }
      const mine = [...owned.values()].filter((b) => expected.has(b.boardD));
      const stray = mine.filter((b) => !b.archived);

      for (const b of stray) {
        try {
          archiveBoard({ rdBin, cwd, home, relay, coord: b.coord, exec });
          archived.push(b.coord);
          log(`  ready-153: archived throwaway board ${b.coord}`);
        } catch (err) {
          // A FAILURE, not a warning. An error that is only logged lets the
          // process exit 0 with the stray still in the owner's portfolio —
          // the exact outcome this guard exists to prevent.
          failures.push(`could not archive throwaway board ${b.coord}: ${err.message}`);
        }
      }

      // READ THE MARKER BACK. `rd board archive` exiting 0 says the command
      // ran, not that the relay took the event. The only acceptable evidence
      // is the relay's own copy carrying the archived tag.
      //
      // Polled rather than slept: a relay takes an event when it takes it, and
      // a fixed sleep is either flaky or slow. The loop exits the moment both
      // conditions hold, and gives up at the deadline.
      const deadline = now() + readBackTimeoutMs;
      let after;
      let unproven = [];
      for (;;) {
        try {
          owned = await ownedBoards(relay, ownerPubkey, deps);
        } catch (err) {
          failures.push(`could not read this key's boards back off ${relay}: ${err.message}`);
          break;
        }
        unproven = mine.filter((b) => !owned.get(b.coord)?.archived).map((b) => b.coord);
        after = 0;
        for (const b of owned.values()) if (!b.archived) after++;
        if (unproven.length === 0 && after === before) break;
        if (now() >= deadline) break;
        await wait(readBackIntervalMs);
      }

      for (const coord of unproven) {
        failures.push(
          `the archived marker for ${coord} is NOT on ${relay} — the board is still in the owner's portfolio`,
        );
      }

      // THE DONE CONDITION, AS AN ASSERTION: a full run leaves the owner's
      // portfolio with the board count it started with. It is deliberately the
      // WHOLE portfolio's count and not just this run's board — a cleanup that
      // only checks its own coordinate cannot see a board it forgot to
      // register. The corollary is that these harnesses must be run one at a
      // time on a given key: a concurrent run's board is, correctly, a stray
      // from this run's point of view.
      if (after === undefined) {
        /* the read-back never completed; the failure is already recorded */
      } else if (after !== before) {
        failures.push(
          `the run changed the owner's unarchived board count: ${before} before, ${after} after — it left a stray behind`,
        );
      } else {
        log(`  ready-153: board count unchanged, ${before} before and ${after} after`);
      }

      return { ok: failures.length === 0, before, after, archived, failures };
    },
  };
}

/** reportCleanup prints a guard result the way the harnesses' own summaries
 * print theirs, and returns the number to add to `failures`. */
export function reportCleanup(result, log = console.log) {
  log(`  ${result.ok ? "PASS" : "FAIL"}  ready-153: the run left the owner's board count exactly where it started`);
  for (const f of result.failures) console.error(`FAILURE: ${f}`);
  return result.failures.length;
}
