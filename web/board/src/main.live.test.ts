// @vitest-environment jsdom
//
// main.live.test.ts — THE REVERSE DIRECTION, at the composition layer
// (ready-4359 done condition 4): a change made somewhere else reaches the OPEN
// page with no reload.
//
// Before this item the board folded ONCE at load. Everything under it — the
// relay client, the fold, the renderer — could each be perfectly live and the
// page would still be a snapshot, because nothing wired them together after the
// first render. That is the layer this file tests, and it is the layer where a
// one-line edit disables a property every underlying suite still proves.
//
// WHAT IS REAL HERE: the real afterLogin, the real subscribeToRelays, the real
// fold, the real renderer, and REAL signed events (the fold verifies every
// signature, so a hand-written card would simply vanish and the assertions would
// be about an item that does not exist). The only substitute is the socket —
// makeNip01Relay, which applies the REQ filter it is sent and pushes a published
// event ONLY to subscriptions that are still open. A client that closed its
// subscription at EOSE, as the one-shot path does, receives nothing from it.
//
// The live relay + real browser + real rd CLI version of this same proof is
// scripts/live-roundtrip-both-ways.mjs.
import { beforeEach, describe, expect, it } from "vitest";
import { afterLogin, defaultDeps, type BoardDeps, type Identity } from "./main";
import { authTransition } from "./lib/auth";
import { neverUnwraps } from "./lib/keyunwrap";
import { fetchEventsFromRelays, subscribeToRelays } from "./lib/relay";
import { makeNip01Relay, type Nip01RelayHandle } from "./lib/nip01relay.fixtures";
import type { NostrEvent } from "./lib/nostrevent";
import { signNostrEvent, xOnlyPubkey } from "./lib/schnorrsign";
import type { Item } from "./lib/state";
import { buildFullCreate, buildWrite, type WriteEnv } from "./board/writeevents";

const SECRET = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
const OWNER = xOnlyPubkey(SECRET);
const BOARD_D = "liveboard";
const COORD = `30301:${OWNER}:${BOARD_D}`;
const RELAY = "wss://relay.test";

const identity: Identity = {
  pubkey: OWNER,
  auth: authTransition({ type: "login", method: "readOnly" }),
};

const sign = (b: { created_at: number; kind: number; tags: string[][]; content: string }): NostrEvent =>
  signNostrEvent({ created_at: b.created_at, kind: b.kind, tags: b.tags, content: b.content }, SECRET);

const boardEvent = sign({
  created_at: 1_780_000_000,
  kind: 30301,
  tags: [
    ["d", BOARD_D],
    ["title", "Live Board"],
  ],
  content: "",
});

function seed(id: string, title: string): Item {
  return {
    id,
    msg_id: "",
    title,
    context: "",
    type: "task",
    priority: "p2",
    status: "inbox",
    for: OWNER,
    created_at: 0n,
    updated_at: 0n,
  };
}

function env(items: Map<string, Item>, createdAt: number): WriteEnv {
  return {
    signer: OWNER,
    boardAuthor: OWNER,
    boardD: BOARD_D,
    boardTitle: "Live Board",
    items,
    issueEventIds: new Map(),
    createdAt,
  };
}

const created = (id: string, title: string, at: number): NostrEvent[] =>
  buildFullCreate(env(new Map(), at), seed(id, title)).map(sign);

/** What the rd CLI publishes when it renames an item: the SAME builder rd's own
 * vector file pins, so these are the bytes that really arrive on the wire. */
const renamed = (id: string, from: string, to: string, at: number): NostrEvent[] =>
  buildWrite(env(new Map([[id, seed(id, from)]]), at), { op: "update_fields", itemId: id, title: to }).map(sign);

const SNAPSHOT: NostrEvent[] = [boardEvent, ...created("live-1", "As it was at load", 1_780_000_100)];

let root: HTMLElement;

beforeEach(() => {
  document.body.replaceChildren();
  root = document.createElement("div");
  root.id = "app";
  document.body.append(root);
});

/** deps wired to ONE relay that honours filters, for both the initial fetch and
 * the live subscription — the same socket a browser would use for both. */
function liveDeps(events: NostrEvent[]): { deps: BoardDeps; handle: Nip01RelayHandle } {
  const { ctor, handle } = makeNip01Relay({ events });
  return {
    handle,
    deps: {
      keyUnwrapper: () => neverUnwraps,
      loadRelays: async () => [RELAY],
      fetchEvents: (relays, filter, opts) =>
        fetchEventsFromRelays(relays, filter, { ...opts, webSocketCtor: ctor, retries: 0, timeoutMs: 2000 }),
      subscribeEvents: (relays, filter, opts) => subscribeToRelays(relays, filter, { ...opts, webSocketCtor: ctor }),
    },
  };
}

const cardIds = (): string[] =>
  [...root.querySelectorAll(".card-id")].map((n) => n.textContent?.trim() ?? "").sort();
const titles = (): string[] => [...root.querySelectorAll(".card-title")].map((n) => n.textContent?.trim() ?? "");

/** The live path coalesces a burst before folding (LIVE_COALESCE_MS), so a test
 * has to let that timer run. Real timers, one short wait — the alternative is
 * faking timers around code that also awaits real promises. */
const settleLive = () => new Promise((r) => setTimeout(r, 400));

describe("ready-4359: the OPEN board reflects a change made elsewhere, with no reload", () => {
  it("renders an item that did not exist when the page loaded", async () => {
    const { deps, handle } = liveDeps([...SNAPSHOT]);
    await afterLogin(root, identity, { kind: "board", board: COORD, relays: [RELAY] }, deps);
    expect(cardIds()).toEqual(["live-1"]);
    const mountedBoard = root.querySelector(".board-root");

    // Somebody else — the rd CLI on another machine — creates an item.
    for (const e of created("live-2", "Created by rd after load", 1_780_000_200)) handle.push(e);
    await settleLive();

    expect(cardIds()).toEqual(["live-1", "live-2"]);
    expect(titles()).toContain("Created by rd after load");
    // NO REMOUNT: the same workspace node is still in place. afterLogin was
    // called once and the board updated in situ, which is the difference between
    // a live subscription and a reload.
    expect(root.querySelector(".board-root")).toBe(mountedBoard);
  });

  it("re-folds a rename rather than patching it — the page shows what rd would project", async () => {
    const { deps, handle } = liveDeps([...SNAPSHOT]);
    await afterLogin(root, identity, { kind: "board", board: COORD, relays: [RELAY] }, deps);
    expect(titles()).toEqual(["As it was at load"]);

    for (const e of renamed("live-1", "As it was at load", "Renamed by rd", 1_780_000_300)) handle.push(e);
    await settleLive();

    expect(titles()).toEqual(["Renamed by rd"]);
    expect(cardIds()).toEqual(["live-1"]);
  });

  it("subscribes with the SAME kinds the backfill asked for, scoped by #a and never by authors", async () => {
    const { deps, handle } = liveDeps([...SNAPSHOT]);
    await afterLogin(root, identity, { kind: "board", board: COORD, relays: [RELAY] }, deps);

    const itemReqs = handle.requests.filter((f) => f.kinds?.includes(30302));
    expect(itemReqs.length).toBeGreaterThanOrEqual(2); // the backfill's, and the live one
    const backfill = itemReqs[0];
    const live = itemReqs.at(-1)!;
    expect(live.kinds).toEqual(backfill.kinds);
    expect(live["#a"]).toEqual([COORD]);
    expect(live.authors).toBeUndefined();
    // A live REQ that is still open is the whole point.
    expect(handle.openSubscriptions()).toBeGreaterThan(0);
  });

  it("does not double-apply the events the initial load already folded", async () => {
    const { deps, handle } = liveDeps([...SNAPSHOT]);
    await afterLogin(root, identity, { kind: "board", board: COORD, relays: [RELAY] }, deps);
    // Re-serve the whole snapshot, as an inclusive `since` boundary or a
    // reconnect would.
    for (const e of SNAPSHOT) handle.push(e);
    await settleLive();
    expect(cardIds()).toEqual(["live-1"]);
    expect(titles()).toEqual(["As it was at load"]);
  });

  it("closes the previous subscription when the page mounts another board", async () => {
    const { deps, handle } = liveDeps([...SNAPSHOT]);
    await afterLogin(root, identity, { kind: "board", board: COORD, relays: [RELAY] }, deps);
    const first = handle.openSubscriptions();
    expect(first).toBeGreaterThan(0);

    await afterLogin(root, identity, { kind: "board", board: COORD, relays: [RELAY] }, deps);
    // Still exactly one live subscription — not two racing to re-fold into a
    // workspace only one of them owns.
    expect(handle.openSubscriptions()).toBe(first);
  });
});

describe("ready-4359: the production wiring", () => {
  it("defaultDeps carries the live subscriber — omitting it means a board that never updates", () => {
    expect(defaultDeps.subscribeEvents).toBeDefined();
  });

  it("a deps object with no subscribeEvents opens NO socket beyond the initial fetch", async () => {
    const { ctor, handle } = makeNip01Relay({ events: [...SNAPSHOT] });
    const deps: BoardDeps = {
      keyUnwrapper: () => neverUnwraps,
      loadRelays: async () => [RELAY],
      fetchEvents: (relays, filter, opts) =>
        fetchEventsFromRelays(relays, filter, { ...opts, webSocketCtor: ctor, retries: 0, timeoutMs: 2000 }),
    };
    await afterLogin(root, identity, { kind: "board", board: COORD, relays: [RELAY] }, deps);
    expect(cardIds()).toEqual(["live-1"]);
    // Every backfill subscription was CLOSEd at EOSE and no live one replaced
    // it: the pre-ready-4359 behaviour, still available to a caller that does
    // not ask for liveness, and provably not opening sockets behind its back.
    expect(handle.openSubscriptions()).toBe(0);
    for (const e of created("never-seen", "should not appear", 1_780_000_400)) handle.push(e);
    await settleLive();
    expect(cardIds()).toEqual(["live-1"]);
  });
});
