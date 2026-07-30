/**
 * harness-cleanup.test.mjs — ready-153's "EVERY live harness" clause, as a
 * check that runs on every PR.
 *
 * throwaway-board.test.mjs proves the cleanup contract WORKS. This file proves
 * every harness is BOUND to it. Both are needed, and the second is the one the
 * previous two attempts lacked: live-stranger-walk.mjs merged (2640e40) with a
 * `rd init` at line 590 and a `finally` clause that archived nothing, three
 * days after the cleanup was "added to every harness". Nothing anywhere
 * noticed, because the invariant lived in a commit message.
 *
 * The harness list is DERIVED FROM THE TREE, never from a list maintained
 * here — a new live-*.mjs that provisions a board is covered the moment it is
 * written, which is the failure mode this file exists for.
 *
 * These are source-shape assertions, deliberately. A live harness needs
 * Chromium, a Go toolchain, the owner's signing key and a real relay; it can
 * never run in CI. What CAN run in CI is "this file creates a board and does
 * not hand it to the guard", and that is exactly the regression that shipped.
 */

import { readFileSync, readdirSync } from "node:fs";
import path from "node:path";
import { describe, expect, test } from "vitest";

const SCRIPTS = path.resolve(import.meta.dirname);

const harnesses = readdirSync(SCRIPTS)
  .filter((f) => f.startsWith("live-") && f.endsWith(".mjs"))
  .sort()
  .map((f) => ({ name: f, src: readFileSync(path.join(SCRIPTS, f), "utf8") }));

/** A harness provisions a throwaway board iff it runs `rd init` — that is the
 * only command in the CLI that publishes a new kind-30301 board definition. */
const createsBoard = (h) => /(^|[[,]\s*)"init"/m.test(h.src);

/** Everything after the LAST `finally {` in the file: a cleanup that is not in
 * the finally clause does not run when the harness fails partway, which is
 * precisely the run that leaks. */
function finallyClause(src) {
  const i = src.lastIndexOf("} finally {");
  return i === -1 ? "" : src.slice(i);
}

test("the harness list is derived from the tree and is not empty", () => {
  expect(harnesses.map((h) => h.name)).toContain("live-stranger-walk.mjs");
  expect(harnesses.length).toBeGreaterThanOrEqual(5);
});

/** Every harness known to provision a board. Deliberately a FLOOR, not an
 * exact list: a harness added tomorrow is covered by the describe.each below
 * the moment it is written, but a harness that DROPS out of `createsBoard`
 * because the detector drifted must fail here rather than quietly stop being
 * checked. */
const KNOWN_BOARD_CREATORS = [
  "live-cache.mjs",
  "live-roundtrip-both-ways.mjs",
  "live-stranger-walk.mjs",
  "live-write-roundtrip.mjs",
];

test("every harness known to provision a board is still detected as one", () => {
  const detected = harnesses.filter(createsBoard).map((h) => h.name);
  for (const name of KNOWN_BOARD_CREATORS) expect(detected).toContain(name);
});

/**
 * A SECOND, INDEPENDENT DETECTOR — because the first one was scoped to what its
 * author already knew about. `createsBoard` looks for the `rd init` call;
 * `namesABoard` looks for the `BOARD_D` the harness gives that board, which is
 * the same fact reached by a different shape and is what stray-boards.mjs reads
 * to recognise the strays on the relay.
 *
 * They must agree. A harness that names a board but never inits it (or the
 * reverse) means one of the two detectors has drifted, and a drifted detector
 * is how "EVERY live harness is covered" comes to be true by definition: the
 * set shrinks to the files that still match, and the check goes green over a
 * harness it stopped looking at.
 */
const namesABoard = (h) => /\bBOARD_D\s*=/.test(h.src);

test("the `rd init` detector and the BOARD_D detector name the same harnesses", () => {
  expect(harnesses.filter(namesABoard).map((h) => h.name)).toEqual(harnesses.filter(createsBoard).map((h) => h.name));
});

describe.each(harnesses.filter(createsBoard).map((h) => [h.name, h]))(
  "%s runs `rd init`, so it is bound to the ready-153 cleanup contract",
  (_name, h) => {
    test("imports the shared guard rather than hand-rolling cleanup", () => {
      expect(h.src).toMatch(/import\s*{[^}]*openThrowawayBoardGuard[^}]*}\s*from\s*"\.\/throwaway-board\.mjs"/);
    });

    test("opens the guard, which takes the BEFORE board count", () => {
      expect(h.src).toMatch(/await openThrowawayBoardGuard\(/);
    });

    test("registers its board-d BEFORE `rd init` runs, so a crashed init still cleans up", () => {
      const register = h.src.search(/guard\.expect\(/);
      const init = h.src.search(/(^|[[,]\s*)"init"/m);
      expect(register).toBeGreaterThan(-1);
      expect(init).toBeGreaterThan(-1);
      expect(register).toBeLessThan(init);
    });

    test("closes the guard inside the `finally` clause, not on the happy path", () => {
      expect(finallyClause(h.src)).toMatch(/guard\.close\(/);
    });

    test("counts the guard's failures toward its own exit code", () => {
      // reportCleanup returns the failure count; a harness that ignores it
      // exits 0 with a stray on the relay, which is the whole bug.
      expect(finallyClause(h.src)).toMatch(/failures \+= reportCleanup\(/);
      expect(h.src).toMatch(/process\.exit\(failures === 0 \? 0 : 1\)/);
    });

    test("has no leftover hand-rolled archive call that could drift from the contract", () => {
      // The single `rd board archive` invocation lives in throwaway-board.mjs
      // (archiveBoard). A second copy here is how the three harnesses'
      // cleanups diverged in the first place.
      expect(h.src).not.toMatch(/"board",\s*"archive"/);
    });
  },
);
