/**
 * stray-boards.test.mjs — the sweep's classifier, checked against the boards
 * that were ACTUALLY on wss://relay.3dl.network (ready-153).
 *
 * WHY THE FIXTURE IS A REAL WALK. The previous version of the sweep carried a
 * hand-typed prefix list and no test at all, and it "worked" — it archived
 * every board it had been told about and reported the portfolio clean, while
 * 17 strays sat in it, 10 of them from a Go live test the list's author had
 * never looked at. A classifier tested only against examples its author
 * invented reproduces exactly that blind spot.
 *
 * So the fixture below is the verbatim output of a live paged kind-30301 walk
 * of the owner's key on 2026-07-30 (42 unarchived boards), and the expected
 * split is the one an operator independently walked and reported. The patterns
 * are NOT in the fixture: they are re-derived from this repo's tree on every
 * run, so a harness whose board-d naming changes, or a new live test that
 * starts publishing boards, is exercised against real board names rather than
 * against a story about them.
 */

import path from "node:path";
import { describe, expect, test } from "vitest";

import { boardDPatterns, classify, harnessSources, strayPatterns, MIN_LITERAL_PREFIX } from "./stray-boards.mjs";

const REPO_ROOT = path.resolve(import.meta.dirname, "../../..");
const OWNER = "a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25";

/** The owner's 25 real project boards, as walked live on 2026-07-30. */
const REAL_PROJECTS = [
  "3dl",
  "3dlbooks",
  "agenticinternet",
  "agenticinternetops",
  "analyst0",
  "augur",
  "automataisland",
  "dontguess",
  "enterpriseaiframework",
  "forge",
  "galtrader",
  "mainframe",
  "mallcoppro",
  "nalu",
  "nostrrelay",
  "olmo3dl",
  "os",
  "pcjsvax",
  "producer",
  "proj",
  "ready",
  "resonant",
  "social",
  "vat",
  "website",
];

/** The 17 test-generated boards in the same walk. */
const STRAYS = [
  "fe4ms7w3eif",
  "fe4ms7xd7ir",
  "fe4ms7z4rp7",
  "ready-7ec-live-1785435017380425687",
  "ready-7ec-live-1785435017380425687-other",
  "ready-82c-live",
  "ready-866-live-1785435016895244229",
  "ready-livetest-1785434740827693994",
  "ready-livetest-1785434742353373698",
  "ready-livetest-1785434896387861513",
  "ready-livetest-1785435020678889082",
  "ready-livetest-1785435023904464777",
  "ready-livetest-1785435030107422940",
  "ready-livetest-1785435031736030172",
  "ready-livetest-1785435055151740338",
  "ready-livetest-1785435329509598406",
  "ready-livetest-1785435332073358502",
];

const board = (d) => ({ boardD: d, coord: `30301:${OWNER}:${d}`, archived: false });
const patterns = () => strayPatterns(REPO_ROOT).filter((p) => p.usable);
const run = () => classify([...REAL_PROJECTS, ...STRAYS].map(board), patterns(), ["ready"]);

describe("the stray set is derived from the tree, against a real live walk", () => {
  test("every one of the 17 boards the live walk found is claimed by a source line in this tree", () => {
    const { strays } = run();
    expect(strays.map((b) => b.boardD).sort()).toEqual([...STRAYS].sort());
  });

  test("each stray names the file and line that made it, so an archive is auditable", () => {
    for (const b of run().strays) {
      expect(b.claimedBy.length).toBeGreaterThan(0);
      for (const c of b.claimedBy) expect(c.source).toMatch(/^[\w./-]+:\d+$/);
    }
  });

  test("no real project board is classified as a stray", () => {
    const { strays, unclaimed } = run();
    expect(strays.map((b) => b.boardD).filter((d) => REAL_PROJECTS.includes(d))).toEqual([]);
    expect(unclaimed.map((b) => b.boardD).sort()).toEqual([...REAL_PROJECTS].sort());
  });

  test("the Go live tests are in scope, not just the .mjs harnesses this item's diff touched", () => {
    // The whole failure this file exists for: 10 of the 17 came from
    // pkg/sync/live_relay_key_test.go, and the previous prefix list — scoped to
    // web/board/scripts — did not mention it.
    const sources = new Set(run().strays.flatMap((b) => b.claimedBy.map((c) => c.source.split(":")[0])));
    expect([...sources].some((f) => f.endsWith("_test.go"))).toBe(true);
    expect([...sources].some((f) => f.startsWith("web/board/scripts/"))).toBe(true);
  });
});

describe("the shield, which is load-bearing and not decorative", () => {
  test("this repo's own production board-d IS derived as a pattern — the shield is what stops it", () => {
    // Several live-relay tests write `BoardD: "ready"`, so the derivation
    // legitimately produces ^ready$. Without the protected list, the sweep's
    // first act would be to archive the board it is run to clean up.
    expect(patterns().some((p) => p.pattern.test("ready"))).toBe(true);
    expect(classify([board("ready")], patterns(), ["ready"]).strays).toEqual([]);
    expect(classify([board("ready")], patterns(), []).strays.map((b) => b.boardD)).toEqual(["ready"]);
  });
});

describe("pattern derivation", () => {
  test("a ternary board-d expands to one pattern per branch, never to a bare wildcard", () => {
    const found = boardDPatterns({
      file: "web/board/scripts/live-x.mjs",
      src: 'const BOARD_D = `${CONFIDENTIAL ? "c191live" : "b2blive"}${RUN}`;',
    });
    expect(found.map((p) => p.body).sort()).toEqual(["b2blive.*", "c191live.*"]);
    expect(found.every((p) => p.usable)).toBe(true);
    // The dangerous derivation this replaced: collapsing the ternary to `.*`
    // too would give ^.*.*$, which matches every board the key owns.
    expect(found.some((p) => p.pattern.test("galtrader"))).toBe(false);
  });

  test("a Go format verb becomes the wildcard, and the literal before it is kept", () => {
    const found = boardDPatterns({
      file: "pkg/sync/x_test.go",
      src: '\tboardD := fmt.Sprintf("ready-7ec-live-%d", run)\n',
    });
    expect(found.map((p) => p.body)).toEqual(["ready-7ec-live-.*"]);
    expect(found[0].pattern.test("ready-7ec-live-1785435017380425687")).toBe(true);
    expect(found[0].pattern.test("ready")).toBe(false);
  });

  test("a pattern with too little literal text to be evidence is not usable", () => {
    const found = boardDPatterns({
      file: "pkg/sync/x_test.go",
      src: '\tboardD := fmt.Sprintf("%s-live-%d", who, run)\n',
    });
    expect(found[0].usable).toBe(false);
    expect(MIN_LITERAL_PREFIX).toBeGreaterThan(0);
  });

  test("harness sources are found by shape: live-*.mjs, and Go tests gated on RD_NOSTR_LIVE_RELAY", () => {
    const files = harnessSources(REPO_ROOT).map((s) => s.file);
    expect(files).toContain("web/board/scripts/live-cache.mjs");
    expect(files).toContain("pkg/sync/live_relay_key_test.go");
    // A Go test that never touches a live relay must not contribute patterns —
    // hermetic tests use invented board names freely.
    expect(files.some((f) => f.endsWith("pkg/sync/boardarchive_test.go"))).toBe(false);
  });
});
