// relaylive.test.ts — the LIVE half of the relay client (ready-4359).
//
// Hermetic, like relay.test.ts: no real network, no real WebSocket. The relay
// here is makeNip01Relay, which APPLIES the filter it is sent and — new for this
// item — can push an event to the subscriptions that are still OPEN. That
// distinction is what makes these tests falsifiable: the one-shot client CLOSEs
// every subscription as it EOSEs, so nothing this fixture pushes could ever
// reach it. A liveness assertion against a fixture that pushed to closed
// subscriptions too would pass for a client with no live path at all.
//
// The REAL relay + REAL browser proof of the same property is
// scripts/live-roundtrip-both-ways.mjs, which changes an item with the rd CLI
// and watches the open board's DOM change. This file is what CI can run.
import { describe, expect, it, vi } from "vitest";
import { subscribeToRelays } from "./relay";
import { makeNip01Relay } from "./nip01relay.fixtures";
import type { NostrEvent } from "./nostrevent";

const COORD = "30301:" + "a".repeat(64) + ":proj";

function card(id: string, createdAt: number, title = id): NostrEvent {
  return {
    id: id.padEnd(64, "0"),
    pubkey: "a".repeat(64),
    created_at: createdAt,
    kind: 30302,
    tags: [
      ["d", id],
      ["title", title],
      ["a", COORD],
    ],
    content: "",
    sig: "b".repeat(128),
  };
}

/** settle lets the fixture's queueMicrotask deliveries run. */
const settle = () => new Promise((r) => setTimeout(r, 0));

describe("subscribeToRelays: the subscription STAYS OPEN past EOSE", () => {
  it("delivers the backlog AND everything published afterwards", async () => {
    const seeded = card("seed", 1000);
    const { ctor, handle } = makeNip01Relay({ events: [seeded] });
    const seen: NostrEvent[] = [];

    const sub = subscribeToRelays(["wss://relay.test"], { kinds: [30302], "#a": [COORD] }, {
      onEvent: (e) => seen.push(e),
      webSocketCtor: ctor,
    });
    await settle();
    expect(seen.map((e) => e.tags[0][1])).toEqual(["seed"]);
    // The subscription is still registered — the one-shot client would have
    // CLOSEd it at EOSE.
    expect(handle.openSubscriptions()).toBe(1);

    const later = card("published-after-load", 1001);
    expect(handle.push(later)).toBe(1);
    await settle();
    expect(seen.map((e) => e.tags[0][1])).toEqual(["seed", "published-after-load"]);

    sub.close();
    expect(handle.openSubscriptions()).toBe(0);
  });

  it("stops delivering the moment close() is called", async () => {
    const { ctor, handle } = makeNip01Relay({ events: [] });
    const seen: NostrEvent[] = [];
    const sub = subscribeToRelays(["wss://relay.test"], { kinds: [30302] }, {
      onEvent: (e) => seen.push(e),
      webSocketCtor: ctor,
    });
    await settle();
    sub.close();
    expect(handle.push(card("after-close", 2000))).toBe(0);
    await settle();
    expect(seen).toEqual([]);
    // Idempotent: a page that closes twice must not throw.
    expect(() => sub.close()).not.toThrow();
  });
});

describe("subscribeToRelays: what it puts on the wire", () => {
  it("sends `since` and NO `limit`/`until` — a live REQ is not a bounded sample", async () => {
    const { ctor, handle } = makeNip01Relay({ events: [] });
    const sub = subscribeToRelays(
      ["wss://relay.test"],
      { kinds: [30302], "#a": [COORD], since: 1700 },
      { onEvent: () => {}, webSocketCtor: ctor },
    );
    await settle();
    expect(handle.requests).toHaveLength(1);
    const req = handle.requests[0];
    expect(req.since).toBe(1700);
    expect(req.limit).toBeUndefined();
    expect(req.until).toBeUndefined();
    // ready-4359 / RELAY MEASUREMENT DISCIPLINE: never an `authors` filter — the
    // production relay's author index under-returns. Scope is the coordinate.
    expect(req.authors).toBeUndefined();
    expect(req["#a"]).toEqual([COORD]);
    sub.close();
  });

  it("re-opens a dropped socket and asks only for what it missed", async () => {
    vi.useFakeTimers();
    try {
      const { ctor, handle } = makeNip01Relay({ events: [card("seed", 1000)] });
      const seen: NostrEvent[] = [];
      const sub = subscribeToRelays(["wss://relay.test"], { kinds: [30302] }, {
        onEvent: (e) => seen.push(e),
        webSocketCtor: ctor,
        reconnectMs: 50,
      });
      await vi.advanceTimersByTimeAsync(1);
      expect(seen).toHaveLength(1);

      handle.dropSockets();
      expect(handle.openSubscriptions()).toBe(0);
      await vi.advanceTimersByTimeAsync(60);
      expect(handle.openSubscriptions()).toBe(1);

      // The cursor moved to the newest event already delivered, so the reconnect
      // asks for the gap rather than replaying the board.
      expect(handle.requests.at(-1)?.since).toBe(1000);
      // …and the boundary event the inclusive `since` re-serves is NOT delivered
      // twice.
      expect(seen).toHaveLength(1);

      handle.push(card("during-uptime", 1001));
      await vi.advanceTimersByTimeAsync(1);
      expect(seen.map((e) => e.tags[0][1])).toEqual(["seed", "during-uptime"]);
      sub.close();
    } finally {
      vi.useRealTimers();
    }
  });

  it("keeps retrying a relay that is never reachable, without disturbing a healthy one", async () => {
    vi.useFakeTimers();
    try {
      const { ctor: goodCtor, handle } = makeNip01Relay({ events: [] });
      const attempts: string[] = [];
      // A ctor that routes by URL: one working relay, one that throws on
      // construction the way a browser refuses a scheme it will not open.
      const ctor = function (url: string) {
        attempts.push(url);
        if (url.includes("dead")) throw new Error("mixed content blocked");
        return new (goodCtor as unknown as new (u: string) => WebSocket)(url);
      } as unknown as typeof WebSocket;

      const seen: NostrEvent[] = [];
      const sub = subscribeToRelays(["wss://dead.test", "wss://good.test"], { kinds: [30302] }, {
        onEvent: (e) => seen.push(e),
        webSocketCtor: ctor,
        reconnectMs: 50,
      });
      await vi.advanceTimersByTimeAsync(1);
      expect(handle.openSubscriptions()).toBe(1); // the good one is live

      await vi.advanceTimersByTimeAsync(120); // two more attempts at the dead one
      expect(attempts.filter((u) => u.includes("dead")).length).toBeGreaterThan(1);
      expect(handle.openSubscriptions()).toBe(1); // and the good one was untouched

      handle.push(card("still-flowing", 3000));
      await vi.advanceTimersByTimeAsync(1);
      expect(seen).toHaveLength(1);
      sub.close();
    } finally {
      vi.useRealTimers();
    }
  });
});

describe("subscribeToRelays: de-duplication is CONTENT-keyed, never id-keyed", () => {
  it("collapses the same event served by two relays into one onEvent call", async () => {
    const shared = card("same", 1000);
    const a = makeNip01Relay({ events: [shared] });
    const b = makeNip01Relay({ events: [shared] });
    const ctor = function (url: string) {
      const pick = url.includes("a.") ? a.ctor : b.ctor;
      return new (pick as unknown as new (u: string) => WebSocket)(url);
    } as unknown as typeof WebSocket;

    const seen: NostrEvent[] = [];
    const sub = subscribeToRelays(["wss://a.test", "wss://b.test"], { kinds: [30302] }, {
      onEvent: (e) => seen.push(e),
      webSocketCtor: ctor,
    });
    await settle();
    expect(seen).toHaveLength(1);
    sub.close();
  });

  it("does NOT let a forgery reusing a genuine id suppress the genuine event", async () => {
    // The ready-dd5 property, at the live layer: a hostile relay asserts a
    // genuine event's id on different content. Both must reach the caller — the
    // fold verifies signatures and is the only layer entitled to drop either.
    const genuine = card("real", 1000, "the real title");
    const forged: NostrEvent = { ...genuine, tags: [...genuine.tags.slice(0, 1), ["title", "TAMPERED"], genuine.tags[2]] };
    const { ctor, handle } = makeNip01Relay({ events: [genuine] });
    const seen: NostrEvent[] = [];
    const sub = subscribeToRelays(["wss://relay.test"], { kinds: [30302] }, {
      onEvent: (e) => seen.push(e),
      webSocketCtor: ctor,
    });
    await settle();
    handle.push(forged);
    await settle();
    expect(seen).toHaveLength(2);
    expect(seen.map((e) => e.tags[1][1])).toEqual(["the real title", "TAMPERED"]);
    sub.close();
  });
});
