/**
 * throwaway-board.test.mjs — the CI-executable half of ready-153.
 *
 * The item's done condition is "a full run of every live harness leaves the
 * owner's portfolio with the same board count it started with". Twice that was
 * argued in a commit message from a manual before/after count. A number in a
 * commit message is not a test: it does not run again, and nothing goes red
 * when the cleanup stops running. This suite is the assertion.
 *
 * WHAT IS REAL HERE. The code under test — the relay walk's paging and dedupe,
 * latest-wins per coordinate, the archived-marker read, which boards get
 * archived, what argv `rd board archive` is invoked with, whether a failure is
 * a failure, and the before/after board-count bracket — is the real
 * throwaway-board.mjs, unmodified.
 *
 * WHAT IS FAKED, AND WHY THAT IS NOT FAKING THE THING UNDER TEST. Two
 * collaborators only:
 *
 *   - the relay socket (`deps.req`), replaced by an in-memory relay that
 *     applies REAL nostr semantics: it honours `kinds`, `limit` and `until`,
 *     serves newest-first, and stores an archive as a REPUBLISH of the same
 *     coordinate at a later created_at — which is exactly what makes
 *     latest-wins load-bearing. The paging loop really pages against it.
 *   - the `rd` binary (`exec`), replaced by a fake that behaves like the real
 *     one: given ["board","archive",coord] it publishes the archived
 *     republish to the fake relay. Each failure mode this suite cares about is
 *     produced by making that fake behave the way a broken `rd` would (throw,
 *     or exit 0 without publishing) — the failure is never simulated at the
 *     level of the module's own return value.
 *
 * A live relay and a real signing key cannot run in CI; the harnesses that use
 * them need Chromium, a Go toolchain and the owner's key. The contract those
 * harnesses depend on is what runs here, on every PR.
 */

import { describe, expect, test } from "vitest";
import {
  KIND_BOARD,
  PAGE_LIMIT,
  archiveBoard,
  fetchBoardEvents,
  latestBoardsOwnedBy,
  openThrowawayBoardGuard,
  ownedBoardCount,
  reportCleanup,
} from "./throwaway-board.mjs";

const OWNER = "a".repeat(64);
const STRANGER = "b".repeat(64);
const RELAY = "wss://relay.example.invalid";

/**
 * fakeRelay is an in-memory nostr relay that implements the parts of NIP-01
 * this walk depends on: a stored event list, `kinds`/`limit`/`until`
 * filtering, and newest-first delivery. It records every filter it is asked
 * for, so a test can assert what was (and was not) sent.
 */
function fakeRelay(events = []) {
  const store = [...events];
  const filters = [];
  let nextTs = 2_000_000;
  return {
    store,
    filters,
    publish(e) {
      store.push(e);
      return e;
    },
    /** board() mints a kind-30301 board event, as `rd init` publishes one. */
    board({ pubkey = OWNER, d, archived = false, created_at = nextTs++ } = {}) {
      const tags = [["d", d]];
      if (archived) tags.push(["archived", "2026-07-30T00:00:00Z"]);
      return this.publish({ id: `${pubkey}:${d}:${created_at}`, pubkey, kind: KIND_BOARD, created_at, tags });
    },
    /** archiveOnRelay is what a WORKING `rd board archive` does: republish the
     * same coordinate, later, with the archived tag. */
    archiveOnRelay(coord) {
      const [, pubkey, d] = coord.split(":");
      return this.board({ pubkey, d, archived: true, created_at: nextTs++ });
    },
    req(_relay, filter) {
      filters.push(structuredClone(filter));
      const matched = store
        .filter((e) => (filter.kinds ? filter.kinds.includes(e.kind) : true))
        .filter((e) => (filter.until === undefined ? true : e.created_at <= filter.until))
        .sort((a, b) => b.created_at - a.created_at);
      return Promise.resolve(matched.slice(0, filter.limit ?? matched.length));
    },
  };
}

/** fakeRd stands in for the compiled `rd` binary. `onArchive` decides how it
 * behaves; the default is a correct one that publishes the archive. */
function fakeRd(relay, onArchive) {
  const calls = [];
  const exec = (bin, args) => {
    calls.push({ bin, args });
    if (args[0] === "board" && args[1] === "archive") {
      return (onArchive ?? ((coord) => relay.archiveOnRelay(coord)))(args[2]);
    }
    throw new Error(`fake rd: unexpected argv ${args.join(" ")}`);
  };
  return { calls, exec };
}

/** noWait collapses the read-back poll's backoff so the suite stays fast
 * without changing which conditions the loop tests. */
const noWait = { wait: async () => {}, readBackTimeoutMs: 50, readBackIntervalMs: 1 };

const RD = { rdBin: "/tmp/rd", cwd: "/tmp/proj", home: "/tmp/home" };

describe("the relay walk (ready-153 / relay measurement discipline)", () => {
  test("pages backwards with `until` and never sends an `authors` filter", async () => {
    const relay = fakeRelay();
    // 1,200 boards is well past one page, so the walk MUST page to see them
    // all — the shape that made a one-page walk under-report.
    for (let i = 0; i < 1200; i++) relay.board({ d: `board-${i}` });

    const events = await fetchBoardEvents(RELAY, { req: relay.req.bind(relay) });

    expect(events.length).toBe(1200);
    expect(relay.filters.length).toBeGreaterThan(1);
    for (const f of relay.filters) {
      // An `authors` filter silently under-returns on wss://relay.3dl.network
      // (42/56 vs 56/56 for the same walk, ready-5c5). Ownership is filtered
      // client-side, after the walk.
      expect(f).not.toHaveProperty("authors");
      expect(f.kinds).toEqual([KIND_BOARD]);
      expect(f.limit).toBeGreaterThanOrEqual(PAGE_LIMIT);
    }
    expect(relay.filters.slice(1).every((f) => typeof f.until === "number")).toBe(true);
  });

  test("latest-wins per coordinate: an archive republish beats the original", () => {
    const relay = fakeRelay();
    relay.board({ d: "b4359x", created_at: 100 });
    relay.board({ d: "b4359x", archived: true, created_at: 200 });

    const boards = latestBoardsOwnedBy(relay.store, OWNER);

    expect(boards.size).toBe(1);
    expect(boards.get(`${KIND_BOARD}:${OWNER}:b4359x`).archived).toBe(true);
  });

  test("an older archive republish does NOT un-archive a newer live board", () => {
    const relay = fakeRelay();
    relay.board({ d: "b4359x", archived: true, created_at: 100 });
    relay.board({ d: "b4359x", created_at: 200 });

    const boards = latestBoardsOwnedBy(relay.store, OWNER);

    expect(boards.get(`${KIND_BOARD}:${OWNER}:b4359x`).archived).toBe(false);
  });

  test("counts only this key's unarchived boards", async () => {
    const relay = fakeRelay();
    relay.board({ d: "live-1" });
    relay.board({ d: "live-2" });
    relay.board({ d: "gone", archived: true });
    relay.board({ pubkey: STRANGER, d: "someone-elses" });

    expect(await ownedBoardCount(RELAY, OWNER, { req: relay.req.bind(relay) })).toBe(2);
  });
});

describe("archiveBoard runs the real `rd board archive`", () => {
  test("invokes the binary with exactly ['board','archive',coord] in the project dir", () => {
    const relay = fakeRelay();
    const rd = fakeRd(relay);

    archiveBoard({ ...RD, relay: RELAY, coord: `${KIND_BOARD}:${OWNER}:b4359x`, exec: rd.exec });

    expect(rd.calls).toEqual([{ bin: "/tmp/rd", args: ["board", "archive", `${KIND_BOARD}:${OWNER}:b4359x`] }]);
  });

  test("a missing rd binary is an error, not a silent skip", () => {
    expect(() => archiveBoard({ ...RD, rdBin: undefined, relay: RELAY, coord: "c", exec: () => {} })).toThrow(
      /no rd binary/,
    );
  });
});

describe("the guard's cleanup contract", () => {
  /** run models a harness: open the guard, register the board-d, let `create`
   * decide what the run actually publishes, then close. */
  async function run({ relay, create, onArchive, rdBin = RD.rdBin, boardD = "b4359run" }) {
    const req = relay.req.bind(relay);
    const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: OWNER, deps: { req } });
    guard.expect(boardD);
    create?.(boardD);
    const rd = fakeRd(relay, onArchive);
    const result = await guard.close({ ...RD, rdBin, exec: rd.exec, ...noWait });
    return { guard, rd, result };
  }

  test("the happy path: the run's board is archived and the count comes back level", async () => {
    const relay = fakeRelay();
    relay.board({ d: "unrelated-real-board" });

    const { rd, result } = await run({ relay, create: (d) => relay.board({ d }) });

    expect(result.failures).toEqual([]);
    expect(result.ok).toBe(true);
    expect(result.before).toBe(1);
    expect(result.after).toBe(1);
    expect(result.archived).toEqual([`${KIND_BOARD}:${OWNER}:b4359run`]);
    expect(rd.calls.map((c) => c.args[0] + " " + c.args[1])).toEqual(["board archive"]);
  });

  // THIS IS THE ONE THE REVIEW ASKED FOR: delete the archive call and this
  // goes red. `onArchive: () => ""` is a `rd board archive` that runs and
  // publishes nothing — indistinguishable, from the harness's point of view,
  // from the call never having been made at all.
  test("cleanup that does not actually archive turns the run RED", async () => {
    const relay = fakeRelay();

    const { result } = await run({ relay, create: (d) => relay.board({ d }), onArchive: () => "" });

    expect(result.ok).toBe(false);
    expect(result.before).toBe(0);
    expect(result.after).toBe(1);
    expect(result.failures.join("\n")).toMatch(/archived marker for 30301:a+:b4359run is NOT on/);
    expect(result.failures.join("\n")).toMatch(/0 before, 1 after — it left a stray behind/);
  });

  test("`rd board archive` exiting 0 without the marker landing is NOT success", async () => {
    const relay = fakeRelay();
    // A `rd` that returns cleanly — the exit code says everything worked — but
    // whose event never reaches the relay. Only a read-back can tell.
    const { rd, result } = await run({ relay, create: (d) => relay.board({ d }), onArchive: () => "archived\n" });

    expect(rd.calls.length).toBe(1); // the command DID run and DID succeed
    expect(result.ok).toBe(false);
    expect(result.failures.join("\n")).toMatch(/archived marker for .* is NOT on/);
  });

  test("a failing `rd board archive` is a failure, not a warning", async () => {
    const relay = fakeRelay();

    const { result } = await run({
      relay,
      create: (d) => relay.board({ d }),
      onArchive: () => {
        throw new Error("relay refused: rate-limited");
      },
    });

    expect(result.ok).toBe(false);
    expect(result.failures.join("\n")).toMatch(/could not archive throwaway board .*rate-limited/);
    expect(result.failures.join("\n")).toMatch(/it left a stray behind/);
  });

  // Leak 3 from throwaway-board.mjs's header: the previous cleanup was guarded
  // on `if (coord && rdBin)`, and `coord` only ever got a value from
  // JSON.parse(rd init --json). A board published by an `rd init` that then
  // failed leaked forever — the exact case the finally block exists for.
  test("a board published by an `rd init` that then FAILED is still cleaned up", async () => {
    const relay = fakeRelay();
    const req = relay.req.bind(relay);
    const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: OWNER, deps: { req } });

    // The harness registers the board-d BEFORE running `rd init`...
    guard.expect("s48fcrash");
    // ...`rd init` publishes the board and then dies. No coordinate is ever
    // returned to the harness, no local variable is assigned.
    relay.board({ d: "s48fcrash" });

    const rd = fakeRd(relay);
    const result = await guard.close({ ...RD, exec: rd.exec, ...noWait });

    expect(result.archived).toEqual([`${KIND_BOARD}:${OWNER}:s48fcrash`]);
    expect(result.failures).toEqual([]);
    expect(result.after).toBe(result.before);
  });

  test("a run that failed BEFORE publishing anything passes without calling rd", async () => {
    const relay = fakeRelay();
    relay.board({ d: "unrelated-real-board" });

    const { rd, result } = await run({ relay, create: undefined, rdBin: null });

    expect(rd.calls).toEqual([]);
    expect(result.ok).toBe(true);
    expect(result.after).toBe(result.before);
  });

  test("no rd binary but a board WAS published is a failure, not a pass", async () => {
    const relay = fakeRelay();

    const { result } = await run({ relay, create: (d) => relay.board({ d }), rdBin: null });

    expect(result.ok).toBe(false);
    expect(result.failures.join("\n")).toMatch(/no rd binary available to archive with/);
  });

  test("an already-archived board is left alone rather than archived twice", async () => {
    const relay = fakeRelay();

    const { rd, result } = await run({ relay, create: (d) => relay.board({ d, archived: true }) });

    expect(rd.calls).toEqual([]);
    expect(result.ok).toBe(true);
  });

  test("every registered board is archived, and one failure does not skip the rest", async () => {
    const relay = fakeRelay();
    const req = relay.req.bind(relay);
    const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: OWNER, deps: { req } });
    guard.expect("b4359a");
    guard.expect("b4359b");
    relay.board({ d: "b4359a" });
    relay.board({ d: "b4359b" });

    const rd = fakeRd(relay, (coord) => {
      if (coord.endsWith("b4359a")) throw new Error("boom");
      return relay.archiveOnRelay(coord);
    });
    const result = await guard.close({ ...RD, exec: rd.exec, ...noWait });

    expect(rd.calls.length).toBe(2);
    expect(result.archived).toEqual([`${KIND_BOARD}:${OWNER}:b4359b`]);
    expect(result.failures.join("\n")).toMatch(/b4359a/);
  });

  test("a board the run did NOT create is never archived", async () => {
    const relay = fakeRelay();
    relay.board({ d: "the-owners-real-project" });

    const { rd, result } = await run({ relay, create: (d) => relay.board({ d }) });

    expect(rd.calls.map((c) => c.args[2])).toEqual([`${KIND_BOARD}:${OWNER}:b4359run`]);
    expect(result.ok).toBe(true);
  });

  test("the marker read-back is polled, so a relay that is slow to serve it still passes", async () => {
    const relay = fakeRelay();
    const req = relay.req.bind(relay);
    const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: OWNER, deps: { req } });
    guard.expect("b4359slow");
    relay.board({ d: "b4359slow" });

    // The archive lands on the relay two poll cycles after the command
    // returns — a real relay's propagation, not an error.
    let pending;
    const rd = fakeRd(relay, (coord) => {
      pending = coord;
      return "";
    });
    let waits = 0;
    const result = await guard.close({
      ...RD,
      exec: rd.exec,
      readBackTimeoutMs: 10_000,
      readBackIntervalMs: 1,
      wait: async () => {
        if (++waits === 2 && pending) relay.archiveOnRelay(pending);
      },
    });

    expect(waits).toBe(2);
    expect(result.ok).toBe(true);
  });

  test("a relay that goes away before cleanup fails the run rather than passing it", async () => {
    const relay = fakeRelay();
    let live = true;
    const req = (r, f) => (live ? relay.req(r, f) : Promise.reject(new Error("connection failed")));
    const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: OWNER, deps: { req } });
    guard.expect("b4359gone");
    relay.board({ d: "b4359gone" });

    // The board is published, then the relay stops answering — cleanup cannot
    // even establish what needs archiving. Fail closed: an unverifiable
    // cleanup is not a clean one.
    live = false;
    const rd = fakeRd(relay);
    const result = await guard.close({ ...RD, exec: rd.exec, ...noWait });

    expect(rd.calls).toEqual([]);
    expect(result.ok).toBe(false);
    expect(result.failures.join("\n")).toMatch(/could not read this key's boards back off/);
  });

  test("reportCleanup returns the number a harness must add to its failure count", () => {
    const lines = [];
    expect(reportCleanup({ ok: true, failures: [] }, (l) => lines.push(l))).toBe(0);
    expect(reportCleanup({ ok: false, failures: ["a", "b"] }, (l) => lines.push(l))).toBe(2);
    expect(lines[0]).toMatch(/PASS/);
    expect(lines[1]).toMatch(/FAIL/);
  });
});
