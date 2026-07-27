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
  }
  close(): void {
    this.closed = true;
  }

  /** subId the client used in its REQ frame, extracted from what it sent. */
  subId(): string {
    const [, subId] = JSON.parse(this.sent[0]) as [string, string, unknown];
    return subId;
  }

  emitEvent(e: NostrEvent): void {
    this.onmessage?.({ data: JSON.stringify(["EVENT", this.subId(), e]) });
  }
  emitEose(): void {
    this.onmessage?.({ data: JSON.stringify(["EOSE", this.subId()]) });
  }
}

function boardEvent(id: string): NostrEvent {
  return {
    id,
    pubkey: "a".repeat(64),
    created_at: 1700000000,
    kind: 30301,
    tags: [["d", id]],
    content: "",
    sig: "b".repeat(128),
  };
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
