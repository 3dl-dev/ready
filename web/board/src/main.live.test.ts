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
import { beforeEach, describe, expect, it, vi } from "vitest";
import {
  afterLogin,
  defaultDeps,
  loadBoardItems,
  startLiveUpdates,
  type BoardDeps,
  type Identity,
} from "./main";
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
    // ready-e51: and the CURSOR main.ts chose — the newest instant the load
    // already folded, so a reconnect asks for the gap rather than replaying the
    // whole board. relaylive.test.ts pins what subscribeToRelays does with a
    // `since` it is GIVEN; this is the only place that observes which one
    // main.ts gives it, and dropping it left every other case green.
    const newestLoaded = SNAPSHOT.reduce((max, e) => (e.created_at > max ? e.created_at : max), 0);
    expect(newestLoaded).toBeGreaterThan(0); // not trivially satisfiable by `undefined === undefined`
    expect(live.since).toBe(newestLoaded);
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

describe("ready-e51: an item the rd CLI created after load is WRITABLE, not merely visible", () => {
  // ready-4359 states this as a delivered property: "the item->board map kept
  // current so a CLI-created item is writable, not just visible", implemented as
  // one line inside afterLogin's onItems —
  //   for (const i of next) if (i.boardCoord) itemBoard.set(i.id, i.boardCoord);
  // Deleting that line left 832/832 vitest green and the live harness green too
  // (scripts/live-roundtrip-both-ways.mjs asserts the CLI-created item APPEARS,
  // then performs its convergence write on an item that existed at load). The
  // case above proves the card renders; nothing proved it could be acted on.
  //
  // WHAT BREAKS WITHOUT IT: boardScopedWriter routes by the item->board map, so
  // an item that arrived live has no entry and every action on it is refused
  // with "no writable board is loaded for item …" — the page showing work it
  // silently cannot act on, which is the one failure mode the line exists to
  // prevent.
  //
  // THE ASSERTION IS THE SIGNER'S CALL, not the DOM. A refusal from
  // boardScopedWriter is thrown by pick() BEFORE the writer builds or signs
  // anything, so "was the extension asked to sign an event for THIS item" is the
  // exact discriminator, and it cannot be satisfied by an optimistic patch.
  it("claiming it reaches the signer with an event addressed to that item", async () => {
    const { deps, handle } = liveDeps([...SNAPSHOT]);
    // The board owner is a maintainer by construction (rolegrant.deriveLevels),
    // so authority is not what this case is about. The signer REJECTS after
    // recording the call: the write is refused at the extension instead of
    // dialling a relay that never answers OK, which keeps the test hermetic and
    // fast while still proving the writer was reached.
    const signEvent = vi.fn(async (_e: { kind: number; tags: string[][] }) => {
      throw new Error("extension declined (test)");
    });
    window.nostr = { getPublicKey: async () => OWNER, signEvent } as unknown as Window["nostr"];
    const signing: Identity = { pubkey: OWNER, auth: authTransition({ type: "login", method: "extension" }) };
    try {
      await afterLogin(root, signing, { kind: "board", board: COORD, relays: [RELAY] }, deps);
      expect(cardIds()).toEqual(["live-1"]);

      for (const e of created("live-writable", "Created by rd after load", 1_780_000_200)) handle.push(e);
      await settleLive();
      expect(cardIds()).toContain("live-writable");

      // Act on it exactly as a human would: select the new card, click Claim.
      (root.querySelector('.card[data-id="live-writable"]') as HTMLElement).click();
      const claim = root.querySelector(".act-claim") as HTMLElement | null;
      expect(claim, "the new card's detail pane must offer Claim").not.toBeNull();
      claim!.click();
      await settleLive();

      // ROUTED: the extension was asked to sign a card for THIS item. Without
      // the live itemBoard update the write is refused before this point.
      expect(signEvent).toHaveBeenCalled();
      const signedFor = signEvent.mock.calls.map((c) => c[0].tags.find((t) => t[0] === "d")?.[1]);
      expect(signedFor).toContain("live-writable");
      // …and the refusal the missing map entry produces is demonstrably absent.
      expect(root.querySelector(".transient-error")?.textContent ?? "").not.toContain("no writable board is loaded");

      // ANTI-TAUTOLOGY: the same click on an item that WAS present at load
      // routes too, so "the signer gets called for anything" is not what is
      // being observed — the discriminator is the item id, not the call count.
      signEvent.mockClear();
      (root.querySelector('.card[data-id="live-1"]') as HTMLElement).click();
      (root.querySelector(".act-claim") as HTMLElement).click();
      await settleLive();
      expect(signEvent.mock.calls.map((c) => c[0].tags.find((t) => t[0] === "d")?.[1])).toContain("live-1");
    } finally {
      delete (window as { nostr?: unknown }).nostr;
    }
  });
});

describe("ready-e51: an event the initial load already folded is not folded a second time", () => {
  // The per-board `if (b.seen.has(key)) return;` guard in startLiveUpdates was
  // unwitnessed — disabling it left the whole suite green, INCLUDING the case
  // above named "does not double-apply the events the initial load already
  // folded". That case asserts on rendered card ids, and the fold dedups by
  // event id (§3.2), so the PROJECTION is identical either way. What the guard
  // actually buys is a bounded LiveBoard.events: the live REQ's `since` is the
  // newest instant already folded and NIP-01 `since` is INCLUSIVE, so the
  // subscription is re-served that instant the moment it opens and again on
  // every reconnect. Without the guard each copy is appended, forever, on a page
  // left open — and every fold thereafter re-verifies a signature list that only
  // grows.
  //
  // THE EVENT LIST IS AN EXPORTED SEAM. An earlier revision of this case
  // hand-built a fake board, on the claim that b.events/b.seen are internal to
  // startLiveUpdates and nothing else exposes them. That was false, and it was
  // false about the module under test: loadBoardItems is EXPORTED and RETURNS
  // the LiveBoard array — the real events, the real `seen` set, the real
  // `newest` cursor, the real ItemSource and the real NostrBoardWriter, in
  // exactly the state a real load leaves them. So the case drives that, and the
  // only substitute left is the socket.
  const board = { coord: COORD, ownerPubkey: OWNER, boardD: BOARD_D, title: "Live Board" };

  it("is not appended to the board's event list and does not re-fold it", async () => {
    const { deps, handle } = liveDeps([...SNAPSHOT]);
    const { live } = await loadBoardItems([board], [RELAY], SNAPSHOT, identity, deps, () => {});

    // PRECONDITIONS, asserted rather than assumed: one live board, whose event
    // list and cursor are what the REAL load produced from the REAL fetch.
    expect(live).toHaveLength(1);
    const b = live[0];
    const loaded = b.events.length;
    // Exactly the board's ITEM stream, which is a strict subset of SNAPSHOT: the
    // 30301 definitions and the kind-1621 issue root are outside BOARD_KINDS /
    // carry no board "a" tag, so the production filter never fetched them.
    expect(new Set(b.events.map((e) => e.id))).toEqual(
      new Set(SNAPSHOT.filter((e) => e.kind === 30302 || e.kind === 1630).map((e) => e.id)),
    );
    expect(loaded).toBeGreaterThan(0);
    expect(b.items.map((i) => i.id)).toEqual(["live-1"]);
    const newestLoaded = b.events.reduce((max, e) => (e.created_at > max ? e.created_at : max), 0);
    expect(b.newest).toBe(newestLoaded);

    const emitted: string[][] = [];
    const sub = startLiveUpdates({
      boards: live,
      relays: [RELAY],
      subscribe: deps.subscribeEvents!,
      onItems: (next) => emitted.push(next.map((i) => i.id)),
      coalesceMs: 5,
    });
    try {
      await settleLive();
      // The live REQ's OWN backfill has already happened here: `since` is
      // inclusive, so the relay answered it with the newest instant the load had
      // folded. Nothing was appended and nothing was re-folded.
      expect(b.events).toHaveLength(loaded);
      expect(emitted).toEqual([]);

      // …and again as a reconnect or a re-publish would deliver it. NOT
      // TRIVIALLY DEAF: the relay reports it reached exactly one OPEN
      // subscription, so "nothing happened" is not a socket that never spoke.
      const alreadyFolded = b.events.find((e) => e.kind === 30302 && e.created_at === newestLoaded)!;
      expect(handle.push(alreadyFolded)).toBe(1);
      await settleLive();
      expect(b.events).toHaveLength(loaded);
      expect(emitted).toEqual([]);
      // (Nothing is asserted about the writer here: NostrBoardWriter.absorb
      // content-dedups its own log, so a duplicate reaching it is invisible at
      // this seam by construction. The unbounded growth the guard prevents is in
      // b.events, which is the list asserted above.)

      // THE OTHER HALF: a genuinely new event on the same subscription IS
      // appended, folded, and absorbed into the writer whose snapshot the next
      // write is built from.
      // Same subset the load fetched — a 30301 or a 1621 published alongside is
      // outside this subscription's filter and would be delivered to nobody.
      const fresh = created("live-2", "Created by rd after load", 1_780_000_200).filter(
        (e) => e.kind === 30302 || e.kind === 1630,
      );
      expect(fresh).toHaveLength(2);
      for (const e of fresh) expect(handle.push(e)).toBe(1);
      await settleLive();
      expect(b.events).toHaveLength(loaded + fresh.length);
      expect(emitted.length).toBeGreaterThan(0);
      expect([...emitted.at(-1)!].sort()).toEqual(["live-1", "live-2"]);
      expect(b.writer.items().has("live-2")).toBe(true);
    } finally {
      sub.close();
    }
  });
});

describe("ready-4359: a multi-board view re-folds only what changed", () => {
  // Driven through the exported startLiveUpdates rather than afterLogin, because
  // the assertion is about which board's FOLD ran — an observation the DOM cannot
  // make (both projections would look identical either way). A `--portfolio` link
  // is genuinely multi-board (ready-4d9) and every fold re-verifies every
  // signature on the board it folds, so this is the difference between one busy
  // board costing its own fold and costing all of them.
  const fakeBoard = (coord: string, id: string) => {
    let folds = 0;
    let authorityRefreshes = 0;
    const absorbed: NostrEvent[] = [];
    const board = {
      coord,
      events: [] as NostrEvent[],
      seen: new Set<string>(),
      newest: 0,
      // ready-48f: the read-side authority the live path may re-derive. Counted
      // rather than implemented, so the cases below can assert that an ITEM
      // event does not pay for a derivation only a GRANT needs.
      authority: [] as NostrEvent[],
      granted: false,
      refreshAuthority: async () => {
        authorityRefreshes++;
      },
      // The projection the LOAD produced, as loadBoardItems supplies it.
      items: [{ id, title: id, status: "inbox", boardCoord: coord }] as never[],
      src: {
        loadItems: () => {
          folds++;
          return [{ id, title: id, status: "inbox", boardCoord: coord }] as never[];
        },
      },
      // Only `absorb` is reached from the live path; the rest of the writer is
      // exercised by nostrwriter.absorb.test.ts against the real class.
      writer: { absorb: (es: NostrEvent[]) => absorbed.push(...es) } as never,
    };
    return { board, folds: () => folds, absorbed, authorityRefreshes: () => authorityRefreshes };
  };

  it("folds the board that received the event, reuses the other, and absorbs into the right writer", async () => {
    const a = fakeBoard(`30301:${OWNER}:a`, "item-a");
    const b = fakeBoard(`30301:${OWNER}:b`, "item-b");
    const { ctor, handle } = makeNip01Relay({ events: [] });

    const emitted: string[][] = [];
    const sub = startLiveUpdates({
      boards: [a.board, b.board],
      relays: [RELAY],
      subscribe: (relays, filter, opts) => subscribeToRelays(relays, filter, { ...opts, webSocketCtor: ctor }),
      onItems: (items) => emitted.push(items.map((i) => i.id)),
      coalesceMs: 5,
    });
    await settleLive();
    const [foldsA, foldsB] = [a.folds(), b.folds()];

    // One event, addressed to board A only.
    handle.push(
      sign({
        created_at: 1_780_000_500,
        kind: 30302,
        tags: [
          ["d", "item-a"],
          ["a", a.board.coord],
        ],
        content: "",
      }),
    );
    await settleLive();

    expect(a.folds()).toBe(foldsA + 1);
    expect(b.folds()).toBe(foldsB); // untouched
    expect(a.absorbed).toHaveLength(1);
    expect(b.absorbed).toHaveLength(0);
    // …and the emitted projection still carries BOTH boards' items: reusing a
    // board's last fold must not drop it from the view.
    expect(emitted.at(-1)).toEqual(["item-a", "item-b"]);
    sub.close();
  });

  it("a board whose fold throws keeps its last projection and does not stop the others", async () => {
    const good = fakeBoard(`30301:${OWNER}:good`, "item-good");
    const bad = fakeBoard(`30301:${OWNER}:bad`, "item-bad");
    bad.board.src = {
      loadItems: () => {
        throw new Error("this board cannot fold");
      },
    } as never;
    const { ctor, handle } = makeNip01Relay({ events: [] });

    const emitted: string[][] = [];
    const sub = startLiveUpdates({
      boards: [good.board, bad.board],
      relays: [RELAY],
      subscribe: (relays, filter, opts) => subscribeToRelays(relays, filter, { ...opts, webSocketCtor: ctor }),
      onItems: (items) => emitted.push(items.map((i) => i.id)),
      coalesceMs: 5,
    });
    await settleLive();

    for (const coord of [good.board.coord, bad.board.coord]) {
      handle.push(
        sign({
          created_at: 1_780_000_600,
          kind: 30302,
          tags: [
            ["d", `d-${coord}`],
            ["a", coord],
          ],
          content: "",
        }),
      );
    }
    await settleLive();

    // The broken board still contributes what it last projected, and the healthy
    // one updated — an exception in one fold must not empty the view.
    expect(emitted.at(-1)).toEqual(["item-good", "item-bad"]);
    expect(good.folds()).toBeGreaterThan(0);
    sub.close();
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
