// Hermetic relay-client tests: NO real network, NO real WebSocket (rule 11 —
// a live-network test inside this unit suite would be a flakiness hazard,
// especially against a scale-to-zero relay with a ~12s cold start). A fake
// WebSocket lets the test script the exact protocol sequences ready-634 and
// done condition 0 describe: a relay that is blocked/unreachable (mixed
// content, LAN address) must not stall the others, and a cold-start relay
// that is merely slow must be retried, not given up on immediately.
//
// The REAL network + REAL browser proof (done condition 0) is a separate,
// one-off manual verification against wss://relay.3dl.network recorded in
// the item's ground_truth_evidence — not an automated test here.
import { describe, expect, it, vi } from "vitest";
import { fetchEventsFromRelays } from "./relay";
import type { NostrEvent } from "./nostrevent";

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  static reset(): void {
    FakeWebSocket.instances = [];
  }
  url: string;
  onopen: (() => void) | null = null;
  onerror: ((ev?: unknown) => void) | null = null;
  onclose: ((ev?: unknown) => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  sent: string[] = [];
  closed = false;

  constructor(url: string) {
    this.url = url;
    FakeWebSocket.instances.push(this);
  }
  send(data: string): void {
    this.sent.push(data);
    // ready-5c5: relay.ts now WALKS `until` backwards until a page adds nothing
    // new (see relay.ts's PAGING note), so every fetch ends with one follow-up
    // REQ carrying a cursor. This stub is a relay with nothing older: it EOSEs
    // that page immediately with no events, which is what ends the walk. Tests
    // below that drive the first page by hand are unaffected — they never send
    // an `until` themselves.
    const frame = JSON.parse(data) as [string, string, { until?: number }?];
    if (frame[0] !== "REQ" || frame[2]?.until === undefined) return;
    const sub = frame[1];
    queueMicrotask(() => this.onmessage?.({ data: JSON.stringify(["EOSE", sub]) }));
  }
  close(): void {
    this.closed = true;
  }

  /** subId of the LATEST REQ frame the client sent — with paging there is more
   * than one, and the hand-driven emitEvent/emitEose below always target the
   * page currently in flight. */
  subId(): string {
    for (let i = this.sent.length - 1; i >= 0; i--) {
      const frame = JSON.parse(this.sent[i]) as [string, string, unknown];
      if (frame[0] === "REQ") return frame[1];
    }
    throw new Error("FakeWebSocket: no REQ frame was sent");
  }

  /** Every REQ filter the client sent, in wire order. */
  reqFilters(): Record<string, unknown>[] {
    return this.sent
      .map((s) => JSON.parse(s) as [string, string, Record<string, unknown>?])
      .filter((f) => f[0] === "REQ")
      .map((f) => f[2] ?? {});
  }

  emitEvent(e: NostrEvent): void {
    this.onmessage?.({ data: JSON.stringify(["EVENT", this.subId(), e]) });
  }
  emitEose(): void {
    this.onmessage?.({ data: JSON.stringify(["EOSE", this.subId()]) });
  }
}

function boardEvent(id: string, createdAt = 1700000000): NostrEvent {
  return {
    id,
    pubkey: "a".repeat(64),
    created_at: createdAt,
    kind: 30301,
    tags: [["d", id]],
    content: "",
    sig: "b".repeat(128),
  };
}

/**
 * A relay that applies `until` and caps EVERY REQ at `maxLimit`, answering
 * newest-first — the behaviour measured on wss://relay.3dl.network on
 * 2026-07-29, where an unbounded `REQ {kinds:[30302]}` returned exactly 500 of
 * the 5648 events it held and reported nothing about the rest.
 */
class CappedRelayWebSocket {
  static store: NostrEvent[] = [];
  static maxLimit = 2;
  static filters: { until?: number; limit?: number }[] = [];
  static reset(store: NostrEvent[], maxLimit: number): void {
    CappedRelayWebSocket.store = store;
    CappedRelayWebSocket.maxLimit = maxLimit;
    CappedRelayWebSocket.filters = [];
  }

  onopen: (() => void) | null = null;
  onerror: ((ev?: unknown) => void) | null = null;
  onclose: ((ev?: unknown) => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;

  constructor(public url: string) {
    queueMicrotask(() => this.onopen?.());
  }

  send(data: string): void {
    const frame = JSON.parse(data) as [string, string, { until?: number; limit?: number }?];
    if (frame[0] !== "REQ") return;
    const [, sub, filter = {}] = frame;
    CappedRelayWebSocket.filters.push(filter);
    const served = CappedRelayWebSocket.store
      .filter((e) => filter.until === undefined || e.created_at <= filter.until)
      .sort((a, b) => b.created_at - a.created_at)
      .slice(0, Math.min(filter.limit ?? Infinity, CappedRelayWebSocket.maxLimit));
    queueMicrotask(() => {
      for (const e of served) this.onmessage?.({ data: JSON.stringify(["EVENT", sub, e]) });
      this.onmessage?.({ data: JSON.stringify(["EOSE", sub]) });
    });
  }

  close(): void {
    /* nothing to tear down */
  }
}

describe("fetchEventsFromRelays", () => {
  it("merges de-duped events across relays, tolerating one that is blocked (mixed content / unreachable)", async () => {
    FakeWebSocket.reset();
    const promise = fetchEventsFromRelays(
      ["ws://192.168.2.40:7777", "wss://relay.3dl.network"],
      { kinds: [30301] },
      { webSocketCtor: FakeWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 5000 },
    );
    const [blocked, good] = FakeWebSocket.instances;
    // A browser blocks ws:// on an https page as mixed content: the socket
    // never opens, onerror fires immediately.
    blocked.onerror?.();

    good.onopen?.();
    good.emitEvent(boardEvent("event-1"));
    good.emitEvent(boardEvent("event-2"));
    good.emitEvent(boardEvent("event-1")); // duplicate id — must collapse
    good.emitEose();

    const events = await promise;
    expect(events.map((e) => e.id).sort()).toEqual(["event-1", "event-2"]);
  });

  it("ready-dd5: a forgery reusing a genuine id does NOT evict the genuine event", async () => {
    // This transport has verified NOTHING — no signature is checked until the
    // fold. Deduping on the self-declared id therefore let an untrusted relay
    // DELETE a genuine event by asserting its id on a tampered copy (last write
    // won the map). Both must survive to the fold, which verifies BEFORE it
    // records an id as seen; only byte-identical copies may collapse.
    FakeWebSocket.reset();
    const promise = fetchEventsFromRelays(
      ["wss://honest.example", "wss://hostile.example"],
      { kinds: [30301] },
      { webSocketCtor: FakeWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 5000 },
    );
    const [honest, hostile] = FakeWebSocket.instances;
    const genuine = boardEvent("event-1");
    const forged: NostrEvent = { ...genuine, tags: [["d", "event-1"], ["title", "tampered"]] };

    honest.onopen?.();
    honest.emitEvent(genuine);
    honest.emitEose();

    hostile.onopen?.();
    hostile.emitEvent(forged);
    hostile.emitEose();

    const events = await promise;
    expect(events).toContainEqual(genuine);
    expect(events).toContainEqual(forged);
    expect(events.filter((e) => e.id === "event-1")).toHaveLength(2);
  });

  it("ready-dd5: a SINGLE hostile relay cannot evict a genuine event via a same-id copy", async () => {
    // THE RESIDUAL the first fix missed. fetchEventsFromRelays' cross-relay
    // merge was switched to dedupeExact, but fetchFromOneRelay's own paging
    // dedup still collapsed on the self-declared id — one layer EARLIER, and
    // before any signature is checked. So a single relay in the set was still a
    // delete primitive requiring no valid signature: emit a tampered copy
    // asserting a genuine event's id FIRST, and the genuine event that follows
    // is dropped inside fetchFromOneRelay, before the cross-relay merge (and
    // therefore before the fold) can ever see it.
    //
    // ONE relay, ONE page, TWO frames — this is the shape the two-socket test
    // above structurally could not reach.
    FakeWebSocket.reset();
    const promise = fetchEventsFromRelays(
      ["wss://hostile.example"],
      { kinds: [30301] },
      { webSocketCtor: FakeWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 5000 },
    );
    const hostile = FakeWebSocket.instances[0];
    const genuine = boardEvent("event-1");
    const forged: NostrEvent = { ...genuine, tags: [["d", "event-1"], ["title", "tampered"]] };

    hostile.onopen?.();
    hostile.emitEvent(forged); // tampered copy FIRST — first-write-wins the id slot
    hostile.emitEvent(genuine);
    hostile.emitEose();

    const events = await promise;
    expect(events).toContainEqual(genuine);
    expect(events).toContainEqual(forged);
    expect(events.filter((e) => e.id === "event-1")).toHaveLength(2);
  });

  it("ready-dd5: a same-id copy on a LATER page cannot evict the genuine event", async () => {
    // The paging walk re-serves boundary events, so the dedup spans pages. A
    // hostile relay could therefore also land the collision on page 2 — the
    // genuine event arrives first there, and an id-keyed map with
    // first-write-wins would drop the forgery (fine) while `added === 0` would
    // silently end the walk. Under content-keyed dedup both survive to the
    // fold, which is the only layer entitled to adjudicate between them.
    FakeWebSocket.reset();
    const promise = fetchEventsFromRelays(
      ["wss://hostile.example"],
      { kinds: [30301] },
      { webSocketCtor: FakeWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 5000 },
    );
    const ws = FakeWebSocket.instances[0];
    const genuine = boardEvent("event-1", 1700000000);
    const forged: NostrEvent = { ...genuine, tags: [["d", "event-1"], ["title", "tampered"]] };

    ws.onopen?.();
    ws.emitEvent(boardEvent("event-2", 1700000100));
    ws.emitEvent(genuine);
    ws.emitEose(); // page 1 -> walk continues with until=1700000000

    // Page 2 re-serves the boundary event (byte-identical: must collapse) plus
    // a tampered same-id copy (must NOT collapse).
    ws.emitEvent(genuine);
    ws.emitEvent(forged);
    ws.emitEose();

    const events = await promise;
    expect(events.filter((e) => e.id === "event-1")).toHaveLength(2);
    expect(events).toContainEqual(genuine);
    expect(events).toContainEqual(forged);
    // The byte-identical re-serve of `genuine` collapsed: 2x event-1 total, not 3.
    expect(events).toHaveLength(3);
  });

  it("retries a relay that closes before EOSE and succeeds on the second attempt", async () => {
    FakeWebSocket.reset();
    const promise = fetchEventsFromRelays(
      ["wss://relay.3dl.network"],
      { kinds: [30301] },
      { webSocketCtor: FakeWebSocket as unknown as typeof WebSocket, retries: 1, timeoutMs: 5000 },
    );
    const first = FakeWebSocket.instances[0];
    first.onopen?.();
    first.onclose?.(); // closed before EOSE -> attempt 1 fails

    // Give the retry's async continuation a turn to construct attempt 2.
    await Promise.resolve();
    await Promise.resolve();
    expect(FakeWebSocket.instances).toHaveLength(2);
    const second = FakeWebSocket.instances[1];
    second.onopen?.();
    second.emitEvent(boardEvent("event-1"));
    second.emitEose();

    const events = await promise;
    expect(events.map((e) => e.id)).toEqual(["event-1"]);
  });

  it("rejects when every relay fails", async () => {
    FakeWebSocket.reset();
    const promise = fetchEventsFromRelays(
      ["ws://192.168.2.40:7777", "ws://192.168.2.41:7777"],
      { kinds: [30301] },
      { webSocketCtor: FakeWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 5000 },
    );
    for (const ws of FakeWebSocket.instances) ws.onerror?.();
    await expect(promise).rejects.toThrow(/could not reach any relay/);
  });

  it("times out a relay that never responds while still returning another relay's events", async () => {
    FakeWebSocket.reset();
    const onStatus = vi.fn();
    const promise = fetchEventsFromRelays(
      ["wss://slow.example", "wss://relay.3dl.network"],
      { kinds: [30301] },
      { webSocketCtor: FakeWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 30, onStatus },
    );
    const [slow, good] = FakeWebSocket.instances;
    void slow; // never call onopen/onmessage — simulates the cold-start relay that never answers
    good.onopen?.();
    good.emitEvent(boardEvent("event-1"));
    good.emitEose();

    const events = await promise;
    expect(events.map((e) => e.id)).toEqual(["event-1"]);
  });

  it("returns an empty array when relays is empty", async () => {
    await expect(
      fetchEventsFromRelays([], { kinds: [30301] }, { webSocketCtor: FakeWebSocket as unknown as typeof WebSocket }),
    ).resolves.toEqual([]);
  });
});

/**
 * ready-5c5 — PAGING. A single REQ does not return "all matching events", it
 * returns at most the relay's own cap, and NIP-01 gives the client no way to
 * learn that cap or to notice it was applied. main.ts's discovery queries are
 * kind-scoped (they must be: the relay's author index under-returns), so they
 * are exactly the broad queries that reach it.
 */
describe("fetchEventsFromRelays — until-paging past a relay's server-side cap", () => {
  const FIVE = [1, 2, 3, 4, 5].map((n) => boardEvent(`event-${n}`, 1700000000 + n));

  it("walks `until` backwards and returns every event a capped relay holds", async () => {
    CappedRelayWebSocket.reset(FIVE, 2);

    const events = await fetchEventsFromRelays(
      ["wss://relay.3dl.network"],
      { kinds: [30301] },
      { webSocketCtor: CappedRelayWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 2000 },
    );

    // ANTI-VACUITY: the cap really did bite — one REQ could only ever have
    // returned 2 of these 5.
    expect(CappedRelayWebSocket.filters.length).toBeGreaterThan(1);
    expect(events.map((e) => e.id).sort()).toEqual(FIVE.map((e) => e.id).sort());
  });

  it("sends no `limit` of its own — the cap is the relay's business, and asking over it is a CLOSED", async () => {
    // wss://relay.3dl.network answers `limit:5000` with
    // CLOSED "requested limit 5000 exceeds this relay's max of 500". A client
    // that guesses a page size can therefore be refused outright; this one
    // never guesses.
    CappedRelayWebSocket.reset(FIVE, 2);
    await fetchEventsFromRelays(
      ["wss://relay.3dl.network"],
      { kinds: [30301] },
      { webSocketCtor: CappedRelayWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 2000 },
    );
    for (const f of CappedRelayWebSocket.filters) expect(f.limit).toBeUndefined();
  });

  it("does NOT page when the caller pinned an explicit limit — that is a bounded sample, not 'everything'", async () => {
    CappedRelayWebSocket.reset(FIVE, 2);

    const events = await fetchEventsFromRelays(
      ["wss://relay.3dl.network"],
      { kinds: [30301], limit: 2 },
      { webSocketCtor: CappedRelayWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 2000 },
    );

    expect(CappedRelayWebSocket.filters).toHaveLength(1);
    expect(events).toHaveLength(2);
  });

  it("stops at maxPages rather than walking forever against a relay that never advances", async () => {
    // Every event shares one created_at, so the inclusive `until` cursor can
    // never move past it. The walk must terminate on "this page added nothing
    // new" — not spin, and not hang until the timeout.
    const stuck = [1, 2, 3].map((n) => boardEvent(`stuck-${n}`, 1700000000));
    CappedRelayWebSocket.reset(stuck, 2);

    const events = await fetchEventsFromRelays(
      ["wss://relay.3dl.network"],
      { kinds: [30301] },
      {
        webSocketCtor: CappedRelayWebSocket as unknown as typeof WebSocket,
        retries: 0,
        timeoutMs: 2000,
        maxPages: 4,
      },
    );

    expect(CappedRelayWebSocket.filters.length).toBeLessThanOrEqual(4);
    expect(events).toHaveLength(2); // truncated, but TERMINATED — see relay.ts's KNOWN LIMIT
  });

  it("fails fast on a CLOSED frame instead of waiting out the whole timeout", async () => {
    FakeWebSocket.reset();
    const promise = fetchEventsFromRelays(
      ["wss://relay.3dl.network"],
      { kinds: [30301], limit: 5000 },
      { webSocketCtor: FakeWebSocket as unknown as typeof WebSocket, retries: 0, timeoutMs: 60000 },
    );
    const ws = FakeWebSocket.instances[0];
    ws.onopen?.();
    ws.onmessage?.({
      data: JSON.stringify([
        "CLOSED",
        ws.subId(),
        "invalid: requested limit 5000 exceeds this relay's max of 500",
      ]),
    });

    // 60s timeout, yet this resolves now: the CLOSED was acted on, not ignored.
    await expect(promise).rejects.toThrow(/exceeds this relay's max of 500/);
  });
});
