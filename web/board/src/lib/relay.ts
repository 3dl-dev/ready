// Minimal NIP-01 relay client: open a WebSocket, send REQ, collect EVENT
// frames until EOSE. No publish path (SCOPE — see ../main.ts), no NDK/relay
// pool dependency: for the single "give me kind-30301 events" query this app
// needs, a hand-rolled client is a few dozen lines and keeps
// TestDist_NoExternalReferences' zero-bundled-license-comment risk (see
// sha256.ts's header) at zero for the networking layer too.
//
// ready-dbf done condition 0 / ready-634 constraints this MUST honour:
//   - Some relay URLs in the wild are ws:// on RFC1918 LAN addresses. A
//     browser on an https page blocks those as mixed content — the socket
//     never opens, `onerror`/`onclose` fires immediately. That is a NORMAL,
//     EXPECTED outcome for those entries, not a reason to stall: this client
//     tolerates a failed relay and keeps waiting on the others.
//   - The production relay runs minReplicas=0 (deliberate scale-to-zero). A
//     cold first request can take up to ~12s to respond. DEFAULT_TIMEOUT_MS
//     is set above that observed bound with margin, and a caller in "no
//     events yet" state should render a connecting/retrying indicator (see
//     main.ts) rather than conclude the relay is broken.

import type { NostrEvent } from "./nostrevent";

export type RelayStatus = "connecting" | "open" | "eose" | "error" | "timeout";

export interface RelayStatusEvent {
  relay: string;
  status: RelayStatus;
  attempt: number;
  detail?: string;
}

export interface NostrFilter {
  kinds?: number[];
  authors?: string[];
  ["#d"]?: string[];
  /** Board-coordinate tag scope ("30301:<owner>:<d>"), mirroring the Go
   * client's BoardSyncFilter (pkg/sync/nostrinbound.go:84). */
  ["#a"]?: string[];
  limit?: number;
}

export interface FetchEventsOptions {
  /** Per-attempt timeout before the attempt is abandoned (default 15000ms —
   * above the observed ~12s cold-start bound with margin). */
  timeoutMs?: number;
  /** Retries per relay after a failed/timed-out attempt (default 1, i.e. 2
   * total attempts — enough to ride out one cold start without hammering a
   * relay that is genuinely unreachable, e.g. a blocked mixed-content ws://
   * LAN entry that will never succeed). */
  retries?: number;
  /** Injectable WebSocket constructor — real `WebSocket` in the browser, a
   * fake in tests. Keeping this a parameter (not a module-level import) is
   * what makes relay.test.ts hermetic: no real network, no real browser
   * WebSocket global required in the Vitest/Node environment. */
  webSocketCtor?: typeof WebSocket;
  onStatus?: (e: RelayStatusEvent) => void;
}

function randomSubId(): string {
  const bytes = new Uint8Array(8);
  if (typeof crypto !== "undefined" && crypto.getRandomValues) {
    crypto.getRandomValues(bytes);
  } else {
    for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
  }
  return Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("");
}

/** fetchFromOneRelay resolves with the events seen before EOSE, or rejects on
 * error/timeout/close-before-eose. Never throws synchronously. */
function fetchFromOneRelay(
  relay: string,
  filter: NostrFilter,
  timeoutMs: number,
  WS: typeof WebSocket,
): Promise<NostrEvent[]> {
  return new Promise((resolve, reject) => {
    const subId = randomSubId();
    const events: NostrEvent[] = [];
    let settled = false;
    let ws: WebSocket;

    const timer = setTimeout(() => {
      finish(() => reject(new Error(`relay ${relay}: timed out after ${timeoutMs}ms`)));
    }, timeoutMs);

    function finish(action: () => void): void {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      try {
        ws.close();
      } catch {
        /* already closed/never opened */
      }
      action();
    }

    try {
      ws = new WS(relay);
    } catch (err) {
      clearTimeout(timer);
      reject(err instanceof Error ? err : new Error(String(err)));
      return;
    }

    ws.onopen = () => {
      try {
        ws.send(JSON.stringify(["REQ", subId, filter]));
      } catch (err) {
        finish(() => reject(err instanceof Error ? err : new Error(String(err))));
      }
    };

    ws.onerror = () => {
      finish(() => reject(new Error(`relay ${relay}: socket error`)));
    };

    ws.onclose = () => {
      finish(() => reject(new Error(`relay ${relay}: closed before EOSE`)));
    };

    ws.onmessage = (msg: MessageEvent) => {
      let parsed: unknown;
      try {
        parsed = JSON.parse(typeof msg.data === "string" ? msg.data : String(msg.data));
      } catch {
        return; // ignore malformed frames from an untrusted relay
      }
      if (!Array.isArray(parsed) || parsed.length === 0) return;
      const [type, ...rest] = parsed as [string, ...unknown[]];
      if (type === "EVENT" && rest[0] === subId) {
        events.push(rest[1] as NostrEvent);
      } else if (type === "EOSE" && rest[0] === subId) {
        finish(() => resolve(events));
      }
      // CLOSED / NOTICE / other frame types are ignored — a relay that sends
      // CLOSED instead of EOSE will still hit ws.onclose above and reject.
    };
  });
}

/**
 * fetchEventsFromRelays queries every relay in `relays` in parallel, retrying
 * each independently up to `retries` times, and returns the de-duplicated
 * (by event id) union of every event seen before that relay's EOSE. A relay
 * that never succeeds (blocked mixed content, unreachable, cold-start
 * timeout exhausted) is dropped silently from the result — it never makes
 * the whole call reject, per the ready-634 tolerate-unusable-relays
 * requirement. Only if EVERY relay fails does this reject, so the caller can
 * distinguish "no boards" from "could not reach any relay".
 */
export async function fetchEventsFromRelays(
  relays: string[],
  filter: NostrFilter,
  opts: FetchEventsOptions = {},
): Promise<NostrEvent[]> {
  const timeoutMs = opts.timeoutMs ?? 15000;
  const retries = opts.retries ?? 1;
  const WS = opts.webSocketCtor ?? (globalThis as unknown as { WebSocket?: typeof WebSocket }).WebSocket;
  if (!WS) {
    throw new Error("relay: no WebSocket implementation available");
  }

  const perRelay = relays.map(async (relay) => {
    let lastErr: unknown;
    for (let attempt = 0; attempt <= retries; attempt++) {
      opts.onStatus?.({ relay, status: attempt === 0 ? "connecting" : "connecting", attempt });
      try {
        const events = await fetchFromOneRelay(relay, filter, timeoutMs, WS);
        opts.onStatus?.({ relay, status: "eose", attempt });
        return events;
      } catch (err) {
        lastErr = err;
        opts.onStatus?.({
          relay,
          status: "error",
          attempt,
          detail: err instanceof Error ? err.message : String(err),
        });
      }
    }
    throw lastErr instanceof Error ? lastErr : new Error(String(lastErr));
  });

  const settled = await Promise.allSettled(perRelay);
  const byId = new Map<string, NostrEvent>();
  let anySucceeded = false;
  for (const result of settled) {
    if (result.status === "fulfilled") {
      anySucceeded = true;
      for (const e of result.value) {
        if (e && typeof e.id === "string") byId.set(e.id, e);
      }
    }
  }
  if (!anySucceeded && relays.length > 0) {
    const reasons = settled
      .filter((r): r is PromiseRejectedResult => r.status === "rejected")
      .map((r) => (r.reason instanceof Error ? r.reason.message : String(r.reason)));
    throw new Error(`relay: could not reach any relay: ${reasons.join("; ")}`);
  }
  return Array.from(byId.values());
}
