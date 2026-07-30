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
 *     serves newest-first, stores an archive as a REPUBLISH of the same
 *     coordinate at a later created_at — which is exactly what makes
 *     latest-wins load-bearing — and can WITHHOLD an event it has accepted
 *     for the next few reads, because a relay that answers a just-published
 *     board with "no such board" is the ordinary case cleanup exists for, and
 *     a fake that always serves the run's own board on the very next read can
 *     never exercise the path where the archive does not happen.
 *   - `wsReq`'s socket itself, in the last describe block, replaced by a fake
 *     `WebSocket` — so the EOSE-versus-silence distinction that keeps the
 *     board-count invariant from passing on a dead relay is tested on the
 *     real `wsReq`, not on a stand-in for it.
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

import { afterEach, describe, expect, test, vi } from "vitest";
import {
  KIND_BOARD,
  PAGE_LIMIT,
  archiveBoard,
  fetchBoardEvents,
  latestBoardsOwnedBy,
  openThrowawayBoardGuard,
  ownedBoardCount,
  reportCleanup,
  wsReq,
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
  const withheld = new Map();
  let nextTs = 2_000_000;
  return {
    store,
    filters,
    /**
     * withhold makes this relay answer the next `reads` REQs as though it had
     * never heard of board-d `d`, while still holding the event — a relay that
     * has ACCEPTED a write and has not propagated it yet. Real, ordinary, and
     * the reason cleanup cannot take a single snapshot of what to archive.
     *
     * `reads` is counted in REQs; every walk in these tests fits in one page,
     * so one read = one walk = one `ownedBoards` call. Pass `Infinity` for a
     * relay that never serves the board at all.
     */
    withhold(d, reads) {
      withheld.set(d, reads);
      return d;
    },
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
      const hidden = new Set();
      for (const [d, reads] of withheld) {
        if (reads > 0) hidden.add(d);
        withheld.set(d, reads - 1);
      }
      const matched = store
        .filter((e) => !hidden.has(e.tags.find((t) => t[0] === "d")?.[1]))
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

/**
 * polls() gives the read-back loop a VIRTUAL clock: `wait` advances time
 * instead of spending it, so a test that has to let the whole read-back window
 * expire (a relay that never serves the board) costs a handful of iterations
 * rather than 30 real seconds — and costs exactly the same number of them
 * every run, which a wall clock would not.
 *
 * PROOF that the budget is real and not a way of dodging the poll loop: the
 * "a relay that never serves the run's board" test below asserts the walk was
 * retried, and "the marker read-back is polled" runs on the real clock.
 */
const POLLS = 5;
function polls({ intervalMs = 1000 } = {}) {
  let t = 1_000_000;
  return {
    readBackTimeoutMs: intervalMs * POLLS,
    readBackIntervalMs: intervalMs,
    now: () => t,
    wait: async (ms) => {
      t += ms;
    },
  };
}

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
    const result = await guard.close({ ...RD, rdBin, exec: rd.exec, ...polls() });
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
    const result = await guard.close({ ...RD, exec: rd.exec, ...polls() });

    expect(result.archived).toEqual([`${KIND_BOARD}:${OWNER}:s48fcrash`]);
    expect(result.failures).toEqual([]);
    expect(result.after).toBe(result.before);
  });

  // ── the relay is not instantaneous ──────────────────────────────────────
  //
  // A relay that serves this run's board on the very next read is a relay
  // that can never make the cleanup fail, and a fake that only models that
  // relay tests nothing about the case cleanup exists for. These three drive
  // the fake's `withhold`: a board it has accepted and is not serving yet.

  test("a board the relay is not serving YET is archived when it appears", async () => {
    const relay = fakeRelay();
    relay.board({ d: "unrelated-real-board" });
    const req = relay.req.bind(relay);
    const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: OWNER, deps: { req } });
    guard.expect("b4359lag");

    // The run publishes its board and the relay takes it — but the next two
    // reads answer as though it were not there. A cleanup that decides what to
    // archive from a single snapshot archives NOTHING here, and then agrees
    // with itself: the marker read-back has nothing to prove and the count
    // invariant cannot see the board either. 1 before, 1 after, green, and a
    // permanent stray the moment the relay catches up.
    relay.board({ d: "b4359lag" });
    relay.withhold("b4359lag", 2);

    const rd = fakeRd(relay);
    const result = await guard.close({ ...RD, exec: rd.exec, ...polls() });

    expect(result.archived).toEqual([`${KIND_BOARD}:${OWNER}:b4359lag`]);
    expect(rd.calls.map((c) => c.args)).toEqual([["board", "archive", `${KIND_BOARD}:${OWNER}:b4359lag`]]);
    expect(result.failures).toEqual([]);
    expect(result.ok).toBe(true);
    expect(result.after).toBe(result.before);
  });

  test("a board that appears late is archived ONCE, not once per poll", async () => {
    const relay = fakeRelay();
    const req = relay.req.bind(relay);
    const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: OWNER, deps: { req } });
    guard.expect("b4359once");
    relay.board({ d: "b4359once" });
    relay.withhold("b4359once", 1);

    // The archive republish is withheld for a poll too, so the loop keeps
    // going after the command has already run. Re-issuing `rd board archive`
    // every poll would spam the relay with republishes of a board it is simply
    // slow to serve.
    const rd = fakeRd(relay, (coord) => {
      relay.archiveOnRelay(coord);
      relay.withhold(coord.split(":")[2], 1);
      return "";
    });
    const result = await guard.close({ ...RD, exec: rd.exec, ...polls() });

    expect(rd.calls.length).toBe(1);
    expect(result.ok).toBe(true);
  });

  test("a relay that NEVER serves the run's board fails the run rather than passing it clean", async () => {
    const relay = fakeRelay();
    relay.board({ d: "unrelated-real-board" });
    const req = relay.req.bind(relay);
    const guard = await openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: OWNER, deps: { req } });
    guard.expect("b4359dark");

    // Published, accepted, and never served back inside the read-back window.
    // From here this is indistinguishable from a run that died before it ever
    // published — and the two have opposite consequences, so the guard is not
    // allowed to guess. The count invariant AGREES (1 before, 1 after): it
    // cannot see a board the relay will not show it, which is exactly why the
    // count alone is not enough to call a run clean.
    relay.board({ d: "b4359dark" });
    relay.withhold("b4359dark", Infinity);

    const readsBefore = relay.filters.length;
    const rd = fakeRd(relay);
    const result = await guard.close({ ...RD, exec: rd.exec, ...polls() });

    expect(relay.filters.length - readsBefore).toBe(POLLS + 1); // it really waited the window out
    expect(rd.calls).toEqual([]);
    expect(result.after).toBe(result.before); // the invariant passes...
    expect(result.ok).toBe(false); // ...and the run is red anyway
    expect(result.failures.join("\n")).toMatch(/never served 30301:a+:b4359dark, the board this run registered/);
  });

  test("a run that published nothing is reported, not quietly passed", async () => {
    const relay = fakeRelay();
    relay.board({ d: "unrelated-real-board" });

    // The honest cost of the rule above: a run that died before `rd init`
    // published anything looks the same from the relay and is reported the
    // same way. That run has already failed for its own reason; one more loud
    // line on a red run is the deliberate trade against a silent stray.
    const { rd, result } = await run({ relay, create: undefined, rdBin: null });

    expect(rd.calls).toEqual([]);
    expect(result.after).toBe(result.before);
    expect(result.ok).toBe(false);
    expect(result.failures.join("\n")).toMatch(/never served .* the board this run registered/);
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
    const result = await guard.close({ ...RD, exec: rd.exec, ...polls() });

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
    const result = await guard.close({ ...RD, exec: rd.exec, ...polls() });

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

  test("a dead relay cannot open the guard at all, so the count is never invented", async () => {
    // The BEFORE measurement is where a silent relay does its worst damage: it
    // would hand back 0, the run would publish a board, cleanup would read 0
    // again, and 0 === 0 would call it clean. The read must propagate.
    const dead = { req: () => Promise.reject(new Error("no EOSE within 45000ms")) };
    await expect(openThrowawayBoardGuard({ relay: RELAY, ownerPubkey: OWNER, deps: dead })).rejects.toThrow(
      /no EOSE/,
    );

    // ...and the difference that has to survive: a LIVE relay this key simply
    // owns nothing on opens the guard and reports a real 0.
    const empty = fakeRelay();
    empty.board({ pubkey: STRANGER, d: "someone-elses" });
    const guard = await openThrowawayBoardGuard({
      relay: RELAY,
      ownerPubkey: OWNER,
      deps: { req: empty.req.bind(empty) },
    });
    expect(guard.before).toBe(0);
  });
});

/**
 * wsReq is the one function in the module that touches a socket, so the fake
 * here is the `WebSocket` class itself and the code under test is the real
 * `wsReq` — its timeout, its EOSE handling, its CLOSE.
 *
 * WHY THIS BLOCK EXISTS. `wsReq` used to `resolve(out)` on timeout. Every
 * caller above then read an unresponsive relay as an answer: `fetchBoardEvents`
 * returned a short page, `ownedBoardCount` returned a number, and the guard's
 * whole board-count bracket read 0 before and 0 after and reported a clean
 * portfolio for a relay that had said nothing at all. An invariant that passes
 * when the measurement fails is the same defect class this item was filed to
 * catch, so the distinction is asserted rather than assumed.
 */
describe("wsReq tells an empty answer from no answer", () => {
  afterEach(() => vi.unstubAllGlobals());

  /** fakeSocket installs a `WebSocket` whose behaviour on REQ is `script`. */
  function fakeSocket(script) {
    const sockets = [];
    class FakeWebSocket {
      constructor(url) {
        this.url = url;
        this.sent = [];
        this.closed = false;
        sockets.push(this);
        // A MICROTASK, not a 0ms timer. `wsReq` assigns `onopen` after this
        // constructor returns, so the open cannot be synchronous — but a timer
        // would queue behind the timeout timer's macrotask if the event loop
        // were blocked, and the "events arrived, EOSE did not" test would then
        // reject for the wrong reason on a loaded runner. A microtask always
        // runs before any timer.
        queueMicrotask(() => this.onopen?.());
      }
      send(raw) {
        this.sent.push(raw);
        const [verb, sub, filter] = JSON.parse(raw);
        // Only a REQ produces an answer. A real relay does not re-serve the
        // subscription when the client CLOSEs it.
        if (verb === "REQ") script(this, sub, filter);
      }
      close() {
        this.closed = true;
      }
    }
    vi.stubGlobal("WebSocket", FakeWebSocket);
    return sockets;
  }

  const deliver = (ws, sub, e) => ws.onmessage({ data: JSON.stringify(["EVENT", sub, e]) });
  const eose = (ws, sub) => ws.onmessage({ data: JSON.stringify(["EOSE", sub]) });

  test("a live relay with nothing to say resolves empty, and closes the subscription", async () => {
    const sockets = fakeSocket((ws, sub) => eose(ws, sub));

    await expect(wsReq(RELAY, { kinds: [KIND_BOARD], limit: PAGE_LIMIT })).resolves.toEqual([]);
    expect(JSON.parse(sockets[0].sent[0])[0]).toBe("REQ");
    expect(JSON.parse(sockets[0].sent[1])[0]).toBe("CLOSE");
  });

  test("a live relay's events are returned once EOSE says it has finished", async () => {
    const sockets = fakeSocket((ws, sub) => {
      deliver(ws, sub, { id: "e1" });
      deliver(ws, sub, { id: "e2" });
      eose(ws, sub);
    });

    await expect(wsReq(RELAY, { kinds: [KIND_BOARD] })).resolves.toEqual([{ id: "e1" }, { id: "e2" }]);
    expect(sockets[0].closed).toBe(true);
  });

  test("a relay that never answers REJECTS — it is not a key that owns no boards", async () => {
    const sockets = fakeSocket(() => {
      /* accepts the REQ and says nothing, ever */
    });

    await expect(wsReq(RELAY, { kinds: [KIND_BOARD] }, { timeoutMs: 20 })).rejects.toThrow(/no EOSE within 20ms/);
    expect(sockets[0].closed).toBe(true);
  });

  test("a page that arrives without EOSE is rejected, not resolved as if complete", async () => {
    // The nastier half: some events DID arrive. Resolving the partial page
    // would under-report the walk, and an under-reported walk is a stray the
    // cleanup never sees.
    fakeSocket((ws, sub) => {
      deliver(ws, sub, { id: "e1" });
      deliver(ws, sub, { id: "e2" });
    });

    await expect(wsReq(RELAY, { kinds: [KIND_BOARD] }, { timeoutMs: 20 })).rejects.toThrow(/2 event\(s\) received/);
  });

  test("the rejection propagates all the way to the board count, which never returns a number", async () => {
    fakeSocket(() => {});

    // fetchBoardEvents/ownedBoardCount default to the real wsReq: a dead relay
    // must not be able to produce a count at all.
    await expect(ownedBoardCount(RELAY, OWNER, { req: (r, f) => wsReq(r, f, { timeoutMs: 20 }) })).rejects.toThrow(
      /no EOSE/,
    );
  });
});
