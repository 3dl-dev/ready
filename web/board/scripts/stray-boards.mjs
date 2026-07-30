/**
 * stray-boards.mjs — "which of the boards this key owns were made by a test?",
 * answered from the TREE rather than from a list somebody typed (ready-153).
 *
 * WHY THIS EXISTS. The first version of the stray sweep carried a hand-written
 * prefix list — `["b2blive", "b4359", "c191live", "c4359", "s48f"]` — and that
 * list was exactly the set of files that round's diff had touched. Measured
 * against the live relay on 2026-07-30 it covered 7 of the owner's 17 stray
 * boards and missed 10 `ready-livetest-*` outright, because those come from a
 * Go live-relay test (pkg/sync/live_relay_key_test.go's `liveTestBoardD`) and
 * no one had thought about the Go side. A sweep whose scope is "the files I
 * edited" reports a clean portfolio and leaves the strays where they are.
 *
 * So the patterns are DERIVED. Every source file in this repo that can publish
 * a board to a live relay is found by shape, its board-d expression is read out
 * of the source, and the run-varying parts become wildcards:
 *
 *   web/board/scripts/live-*.mjs    `const BOARD_D = `fe4${RUN}`;`      -> ^fe4.*$
 *   *_test.go w/ RD_NOSTR_LIVE_RELAY  `fmt.Sprintf("ready-7ec-live-%d"` -> ^ready-7ec-live-.*$
 *
 * A harness added tomorrow is covered the day it is written, and a harness
 * whose board-d nobody here knows about still shows up — as an UNCLAIMED board,
 * printed rather than silently skipped, which is the only honest answer for a
 * board this repo cannot account for.
 *
 * NOTHING HERE ARCHIVES ANYTHING. This module only classifies; the caller
 * decides. See archive-stray-boards.mjs.
 */

import { readFileSync, readdirSync, statSync } from "node:fs";
import path from "node:path";

/**
 * A pattern must have this much literal text before its first wildcard to be
 * usable. `fmt.Sprintf("%s-live-%d")` would otherwise derive `^.*-live-.*$`,
 * which is not evidence about any particular board — it is a wildcard wearing a
 * pattern's clothes, and it would match real project boards.
 */
export const MIN_LITERAL_PREFIX = 3;

/** Source files that can put a board on a live relay, found by shape. */
export function harnessSources(repoRoot) {
  const out = [];

  const scripts = path.join(repoRoot, "web/board/scripts");
  for (const f of safeReaddir(scripts)) {
    if (!f.startsWith("live-") || !f.endsWith(".mjs")) continue;
    out.push({ file: path.relative(repoRoot, path.join(scripts, f)), src: readFileSync(path.join(scripts, f), "utf8") });
  }

  // A Go test only reaches a real relay if it is gated on RD_NOSTR_LIVE_RELAY —
  // that env var IS the live-test convention in this repo, so "contains it" is
  // the shape, not a list of directories.
  for (const p of walkGoTests(repoRoot)) {
    const src = readFileSync(p, "utf8");
    if (!src.includes("RD_NOSTR_LIVE_RELAY")) continue;
    out.push({ file: path.relative(repoRoot, p), src });
  }

  return out.sort((a, b) => a.file.localeCompare(b.file));
}

function safeReaddir(dir) {
  try {
    return readdirSync(dir).sort();
  } catch {
    return [];
  }
}

function* walkGoTests(dir, depth = 0) {
  if (depth > 6) return;
  for (const name of safeReaddir(dir)) {
    if (name === "node_modules" || name === ".git" || name === ".claude" || name === "dist") continue;
    const p = path.join(dir, name);
    let st;
    try {
      st = statSync(p);
    } catch {
      continue;
    }
    if (st.isDirectory()) yield* walkGoTests(p, depth + 1);
    else if (name.endsWith("_test.go")) yield p;
  }
}

/**
 * A `${...}` hole in a JS template literal expands to the string literals it
 * contains, and to a bare wildcard when it contains none:
 *
 *   `${CONFIDENTIAL ? "c191live" : "b2blive"}${RUN}` -> c191live.*  AND  b2blive.*
 *
 * Two separate patterns, not one alternation, because a pattern's usability is
 * judged on the literal text before its first wildcard, and `(c191live|b2blive).*`
 * has none. Collapsing the ternary to `.*` instead would derive `^.*.*$` — a
 * pattern that matches every board this key owns, the owner's real projects
 * included. That is the difference between a derived pattern and a dangerous
 * one.
 */
function holeOptions(inner) {
  const lits = [...inner.matchAll(/"([^"]*)"/g)].map((m) => m[1]).filter(Boolean);
  if (lits.length === 0) return [".*"];
  return lits.map(escapeRe);
}

const escapeRe = (s) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

/** A Go format verb (%d, %s, %v, %02d…) is a run-varying hole. */
function goFormatToRegex(lit) {
  let out = "";
  let i = 0;
  while (i < lit.length) {
    const m = /^%[-+ #0]*[\d.]*[a-zA-Z]/.exec(lit.slice(i));
    if (m) {
      out += ".*";
      i += m[0].length;
    } else {
      out += escapeRe(lit[i]);
      i += 1;
    }
  }
  return out;
}

/** jsTemplateToRegexes returns one regex body per combination of hole choices. */
function jsTemplateToRegexes(tpl) {
  let outs = [""];
  let i = 0;
  const append = (opts) => {
    outs = outs.flatMap((o) => opts.map((x) => o + x));
  };
  while (i < tpl.length) {
    if (tpl.startsWith("${", i)) {
      let depth = 1;
      let j = i + 2;
      for (; j < tpl.length && depth > 0; j++) {
        if (tpl[j] === "{") depth++;
        else if (tpl[j] === "}") depth--;
      }
      append(holeOptions(tpl.slice(i + 2, j - 1)));
      i = j;
    } else {
      append([escapeRe(tpl[i])]);
      i += 1;
    }
  }
  return outs;
}

const lineOf = (src, index) => src.slice(0, index).split("\n").length;

/**
 * boardDPatterns reads every board-d expression out of one harness source.
 *
 * Returns `{ pattern, body, source, raw }` records. `pattern` is anchored;
 * `body` is the un-anchored regex text, kept so a caller (and the test suite)
 * can see what was derived.
 */
export function boardDPatterns({ file, src }) {
  const found = [];
  const add = (bodies, raw, index) => {
    for (const body of bodies) {
      const prefix = /^((?:\\.|[^\\.([])*)/.exec(body)?.[1] ?? "";
      const literalPrefixLen = prefix.replace(/\\(.)/g, "$1").length;
      found.push({
        body,
        pattern: new RegExp(`^${body}$`),
        raw,
        source: `${file}:${lineOf(src, index)}`,
        usable: literalPrefixLen >= MIN_LITERAL_PREFIX,
        literalPrefixLen,
        exact: !body.includes(".*"),
      });
    }
  };

  // JS live harness: `const BOARD_D = `...`;` or `const BOARD_D = "...";`
  for (const m of src.matchAll(/\bBOARD_D\s*=\s*`([^`]*)`/g)) add(jsTemplateToRegexes(m[1]), m[0], m.index);
  for (const m of src.matchAll(/\bBOARD_D\s*=\s*"([^"]*)"/g)) add([escapeRe(m[1])], m[0], m.index);

  // Go live test: a board-d built with fmt.Sprintf, or written as a literal on
  // a BoardD field / boardD variable.
  for (const m of src.matchAll(/\b(?:boardD|BoardD|board|d)\s*(?::=|=|:)\s*fmt\.Sprintf\("([^"]+)"/g))
    add([goFormatToRegex(m[1])], m[0], m.index);
  for (const m of src.matchAll(/\b(?:boardD|BoardD)\s*(?::=|=|:)\s*"([^"]+)"/g)) add([escapeRe(m[1])], m[0], m.index);

  return found;
}

/**
 * strayPatterns is every usable board-d pattern this repo can produce, with the
 * source line each one came from — provenance, so a board about to be archived
 * can be traced back to the test that made it.
 */
export function strayPatterns(repoRoot) {
  const out = [];
  for (const s of harnessSources(repoRoot)) out.push(...boardDPatterns(s));
  return out;
}

/**
 * classify splits the boards a key owns into `strays` (a tree-derived pattern
 * claims it, with the source that claims it) and `unclaimed` (nothing in the
 * tree accounts for it — the owner's real projects, and any harness this repo
 * does not contain).
 *
 * `protectedDs` are board-ds that are NEVER strays whatever the patterns say.
 * A live test that writes `BoardD: "ready"` in a comparison would otherwise
 * derive `^ready$` and put this repo's own production board on the list.
 */
export function classify(boards, patterns, protectedDs = []) {
  const shielded = new Set(protectedDs);
  const strays = [];
  const unclaimed = [];
  for (const b of boards) {
    if (shielded.has(b.boardD)) {
      unclaimed.push({ ...b, protected: true });
      continue;
    }
    const claimedBy = patterns.filter((p) => p.usable && p.pattern.test(b.boardD));
    if (claimedBy.length > 0) strays.push({ ...b, claimedBy });
    else unclaimed.push({ ...b, protected: false });
  }
  return { strays, unclaimed };
}
