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
import { dedupeExact, eventIdentity } from "./nostrevent";

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
   * client's BoardSyncFilter (pkg/sync/nostrinbound.go:84). A confidential
   * board keeps every routing tag in the clear precisely so this REQ keeps
   * working unchanged (envelope spec §0). */
  ["#a"]?: string[];
  ["#p"]?: string[];
  since?: number;
  /** Upper bound (inclusive) on created_at. Callers do not set this: it is the
   * paging cursor fetchFromOneRelay walks backwards (see PAGING below). */
  until?: number;
  /** "Give me at most N" — a BOUNDED SAMPLE. Setting it disables paging (see
   * PAGING below), so a caller that wants every matching event must leave it
   * unset. No caller in this app sets it today. */
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
  /** Hard stop on the `until` walk (default 200). A backstop against a relay
   * that answers every page with the same events, NOT a tuning knob: the walk
   * normally terminates on its own the first time a page adds nothing new. */
  maxPages?: number;
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

const DEFAULT_MAX_PAGES = 200;

/**
 * fetchFromOneRelay resolves with every event this relay serves for `filter`,
 * or rejects on error/timeout/close-before-EOSE/CLOSED. Never throws
 * synchronously.
 *
 * PAGING (ready-5c5). A single REQ does NOT return "all matching events" — it
 * returns at most as many as the relay's own cap allows, whether or not the
 * client asked for a limit, and NIP-01 gives the client no way to learn that
 * cap. Measured on wss://relay.3dl.network 2026-07-29:
 *
 *     REQ {kinds:[30302]}                -> exactly 500 events
 *     REQ {kinds:[30302], limit:5000}    -> CLOSED "requested limit 5000
 *                                           exceeds this relay's max of 500"
 *     the same filter walked with `until` -> 5648 events over 13 REQs
 *
 * So an unpaged REQ returned 8.8% of the matching events and said nothing
 * about it. That is silent, unbounded data loss for any query broad enough to
 * reach the cap — which the kind-scoped discovery queries in ../main.ts are.
 *
 * The walk therefore:
 *   - sends NO `limit` of its own. The cap is the relay's business and asking
 *     for more than it allows is a CLOSED, not a bigger page. Letting the relay
 *     clamp is the only cap-agnostic option.
 *   - moves `until` back to the oldest created_at of the page just received.
 *     `until` is INCLUSIVE, so each page re-serves the events sitting exactly
 *     on the boundary; those exact re-serves are de-duplicated by FULL CONTENT
 *     (eventIdentity), never by the self-declared id — see the SECURITY note on
 *     `collected` below.
 *   - stops the first time a page contributes NO new event. That is the only
 *     termination signal available: "page smaller than requested" cannot be
 *     used when no limit was requested, and a relay that clamps to a cap below
 *     what we asked for would trip it falsely.
 *
 * Cost of that rule is one extra REQ at the end of every walk (the page that
 * proves there is nothing older). It rides the SAME socket — one connection
 * per relay per attempt, N REQ/CLOSE frames on it — so the extra page is one
 * round trip, not one more TLS handshake.
 *
 * KNOWN LIMIT: if more events share one created_at than the relay's cap, the
 * cursor cannot advance past that second and the walk stops there rather than
 * looping forever. Escaping it needs `since`-side bisection, which no query in
 * this app is anywhere near needing (the whole relay holds ~100 authority
 * events).
 *
 * A caller that sets `filter.limit` is asking for a bounded sample, not for
 * everything, so paging is disabled for it and exactly one REQ is sent.
 */
function fetchFromOneRelay(
  relay: string,
  filter: NostrFilter,
  timeoutMs: number,
  WS: typeof WebSocket,
  maxPages: number,
): Promise<NostrEvent[]> {
  return new Promise((resolve, reject) => {
    // SECURITY (ready-dd5). `collected` is keyed on eventIdentity — the FULL
    // signed content plus the signature — NOT on the self-declared `id`. This
    // layer has verified NOTHING: the signature is not checked until the fold.
    // An id-keyed map here made a SINGLE hostile relay a delete primitive
    // needing no valid signature: it emits a tampered copy asserting a genuine
    // event's id, wins the id slot, and the genuine event that follows is
    // dropped inside this function — before the cross-relay merge, and so
    // before anything could verify either copy. Content keying means dedup only
    // ever collapses byte-identical re-serves (the boundary events the `until`
    // walk re-delivers, and the same event served twice); every adversarial
    // near-copy survives to the fold, which verifies BEFORE it records an id as
    // seen (fold.ts §3.2/§3.3). The cross-relay merge in
    // fetchEventsFromRelays applies the same rule — BOTH sites must, or the
    // earlier one silently reinstates the defect.
    //
    // No signature is verified here on purpose: that would double the schnorr
    // cost of every page load, and every consumer (discoverOwnerBoards,
    // deriveBoardKeyring, projectItems) verifies already. Nor was id keying
    // ever a volume defence — one relay can already emit unlimited events with
    // distinct ids; `maxPages` is the volume bound.
    const seen = new Set<string>();
    const collected: NostrEvent[] = [];
    const paging = filter.limit === undefined;
    let until = filter.until;
    let pages = 0;
    let subId = "";
    let pageEvents: NostrEvent[] = [];
    let settled = false;
    let ws: WebSocket;

    let timer = setTimeout(expire, timeoutMs);
    function expire(): void {
      finish(() => reject(new Error(`relay ${relay}: timed out after ${timeoutMs}ms`)));
    }

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

    function sendPage(): void {
      pages++;
      subId = randomSubId();
      pageEvents = [];
      const req: NostrFilter = { ...filter };
      if (until !== undefined) req.until = until;
      // The timeout is PER PAGE: a relay that keeps answering is making
      // progress and must not be killed by a clock that started at connect.
      clearTimeout(timer);
      timer = setTimeout(expire, timeoutMs);
      try {
        ws.send(JSON.stringify(["REQ", subId, req]));
      } catch (err) {
        finish(() => reject(err instanceof Error ? err : new Error(String(err))));
      }
    }

    function onEose(): void {
      // Close this subscription before opening the next one on the same socket,
      // so the relay is not left holding N live subs for one walk.
      try {
        ws.send(JSON.stringify(["CLOSE", subId]));
      } catch {
        /* best effort — the walk is about to end anyway if the socket is gone */
      }
      let added = 0;
      let oldest = Number.POSITIVE_INFINITY;
      for (const e of pageEvents) {
        if (!e || typeof e.id !== "string") continue;
        const key = eventIdentity(e);
        if (!seen.has(key)) {
          seen.add(key);
          collected.push(e);
          added++;
        }
        if (typeof e.created_at === "number" && e.created_at < oldest) oldest = e.created_at;
      }
      if (!paging || added === 0 || oldest === Number.POSITIVE_INFINITY || pages >= maxPages) {
        finish(() => resolve(collected));
        return;
      }
      until = oldest;
      sendPage();
    }

    try {
      ws = new WS(relay);
    } catch (err) {
      clearTimeout(timer);
      reject(err instanceof Error ? err : new Error(String(err)));
      return;
    }

    ws.onopen = () => {
      sendPage();
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
        pageEvents.push(rest[1] as NostrEvent);
      } else if (type === "EOSE" && rest[0] === subId) {
        onEose();
      } else if (type === "CLOSED" && rest[0] === subId) {
        // A refused subscription must fail FAST and loudly. Ignoring it (the
        // pre-ready-5c5 behaviour) meant waiting out the full per-attempt
        // timeout for an answer the relay had already declined to give —
        // wss://relay.3dl.network answers an over-cap `limit` with exactly
        // this frame.
        const detail = typeof rest[1] === "string" ? rest[1] : JSON.stringify(rest[1] ?? "");
        finish(() => reject(new Error(`relay ${relay}: subscription closed: ${detail}`)));
      }
      // NOTICE / other frame types are still ignored.
    };
  });
}

/**
 * fetchEventsFromRelays queries every relay in `relays` in parallel, retrying
 * each independently up to `retries` times, and returns the union of every
 * event seen before that relay's EOSE with EXACT duplicates collapsed. A relay
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
  const maxPages = opts.maxPages ?? DEFAULT_MAX_PAGES;
  const WS = opts.webSocketCtor ?? (globalThis as unknown as { WebSocket?: typeof WebSocket }).WebSocket;
  if (!WS) {
    throw new Error("relay: no WebSocket implementation available");
  }

  const perRelay = relays.map(async (relay) => {
    let lastErr: unknown;
    for (let attempt = 0; attempt <= retries; attempt++) {
      opts.onStatus?.({ relay, status: attempt === 0 ? "connecting" : "connecting", attempt });
      try {
        const events = await fetchFromOneRelay(relay, filter, timeoutMs, WS, maxPages);
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
  // ready-dd5: this transport is UNVERIFIED — no signature has been checked
  // yet — so it must not dedup on the self-declared event id. `byId.set(e.id,
  // e)` let a forgery asserting a genuine event's id EVICT the genuine event
  // (last write won), after which the fold rejected the forgery and the real
  // event was simply gone. dedupeExact collapses byte-identical copies only
  // (the same event served by two relays), so adversarial near-copies all
  // survive to the fold, which verifies BEFORE recording an id as seen.
  //
  // fetchFromOneRelay's paging dedup enforces the SAME rule one layer earlier.
  // BOTH sites are load-bearing: with only this one fixed, a single hostile
  // relay still deleted the genuine event inside its own walk, so nothing
  // reached this merge to be preserved.
  const collected: NostrEvent[] = [];
  let anySucceeded = false;
  for (const result of settled) {
    if (result.status === "fulfilled") {
      anySucceeded = true;
      for (const e of result.value) collected.push(e);
    }
  }
  if (!anySucceeded && relays.length > 0) {
    const reasons = settled
      .filter((r): r is PromiseRejectedResult => r.status === "rejected")
      .map((r) => (r.reason instanceof Error ? r.reason.message : String(r.reason)));
    throw new Error(`relay: could not reach any relay: ${reasons.join("; ")}`);
  }
  return dedupeExact(collected);
}

// ── LIVE SUBSCRIPTION (ready-4359) ──────────────────────────────────────────
//
// fetchEventsFromRelays above is a ONE-SHOT: it walks `until` backwards, EOSEs,
// closes the socket and resolves. That was the whole read path the board had, so
// the page folded ONCE at load and never again — a change made anywhere else
// (the rd CLI on another machine, a second browser) stayed invisible until the
// human pressed reload. This is the other half: a REQ that is deliberately NOT
// closed at EOSE, so the relay keeps pushing every later event that matches.
//
// WHY IT IS A SEPARATE FUNCTION AND NOT A FLAG ON THE ONE ABOVE. The two have
// opposite contracts on every axis that matters:
//
//   backfill                          live
//   ────────                          ────
//   resolves once, then done          never resolves; runs until close()
//   pages `until` backwards           no paging — see PAGING below
//   a dead relay is dropped silently  a dead relay is RECONNECTED, forever
//   rejects if every relay failed     never rejects — by the time it runs there
//                                     is no caller left to reject to
//
// Folding them would mean one function whose behaviour on error, on EOSE and on
// termination is decided by a boolean, which is how a live path silently
// inherits "resolve and close the socket".
//
// PAGING IS DELIBERATELY ABSENT. `until`-walking answers "give me everything you
// already hold", which the initial load has just done. A live REQ carries
// `since` — the newest instant already folded — and asks for what comes after
// it; the events the relay pushes post-EOSE arrive one at a time and no cap
// applies to a push. The cap DOES apply to the backlog a RECONNECT asks for
// (measured on wss://relay.3dl.network: 500 events per REQ), so a page whose
// socket was down for longer than 500 board events would lose the oldest of that
// backlog. That bound is stated rather than hidden: a reload recovers it, and
// nothing below claims otherwise.
//
// NO `authors` FILTER, EVER — the same rule the backfill follows (see the
// comments in ../main.ts): wss://relay.3dl.network's author index under-returns
// deterministically. A live board subscription is scoped by `#a`, the board
// coordinate, which every card, status and grant event carries in the CLEAR even
// on a confidential board (envelope spec §0).

/** LiveSubscription is the handle subscribeToRelays returns: one call,
 * `close()`, which stops every socket and every pending reconnect. Idempotent —
 * a page that closes twice (unmount, then navigation) must not throw. */
export interface LiveSubscription {
  close(): void;
}

export interface LiveSubscriptionOptions {
  /**
   * Called once per DISTINCT event, in arrival order, and never after close().
   *
   * Duplicates — the same event served by two relays, or re-served by a
   * reconnect whose `since` boundary is inclusive — are collapsed by FULL
   * CONTENT (eventIdentity), never by the self-declared id, for exactly the
   * reason fetchFromOneRelay's `collected` map is not id-keyed: nothing at this
   * layer has verified a signature, so id-keyed dedup would let a forgery
   * asserting a genuine event's id suppress the genuine event BEFORE the fold
   * could reject the forgery.
   */
  onEvent: (e: NostrEvent) => void;
  onStatus?: (e: RelayStatusEvent) => void;
  /** Injectable WebSocket constructor — the real one in the browser, a fake in
   * tests. Same seam, and same reason, as FetchEventsOptions'. */
  webSocketCtor?: typeof WebSocket;
  /** Delay before a dropped socket is re-opened (default 3000ms). A relay that
   * is unreachable is retried at this interval indefinitely: the page may be
   * open for hours, a relay that is down now may be up later, and there is no
   * user-visible operation to fail. */
  reconnectMs?: number;
}

const DEFAULT_RECONNECT_MS = 3000;

/**
 * subscribeToRelays opens a LIVE NIP-01 subscription on every relay in `relays`
 * and calls `onEvent` for each distinct matching event, including every one the
 * relay pushes AFTER EOSE. It returns immediately, never throws for a relay that
 * is unreachable, and never stops on its own.
 *
 * `filter.since` is the caller's cursor — normally the newest created_at it has
 * already folded. It is advanced internally as events arrive, so a reconnect
 * asks only for what happened while the socket was down instead of replaying the
 * board. `since` is INCLUSIVE in NIP-01, so the boundary event comes back on
 * every reconnect; the content-keyed dedup absorbs it.
 */
export function subscribeToRelays(
  relays: readonly string[],
  filter: NostrFilter,
  opts: LiveSubscriptionOptions,
): LiveSubscription {
  const WS = opts.webSocketCtor ?? (globalThis as unknown as { WebSocket?: typeof WebSocket }).WebSocket;
  if (!WS) throw new Error("relay: no WebSocket implementation available");
  const reconnectMs = opts.reconnectMs ?? DEFAULT_RECONNECT_MS;

  const seen = new Set<string>();
  const sockets = new Set<WebSocket>();
  const timers = new Set<ReturnType<typeof setTimeout>>();
  let since = filter.since;
  let closed = false;

  const later = (relay: string, attempt: number): void => {
    const t = setTimeout(() => {
      timers.delete(t);
      connect(relay, attempt + 1);
    }, reconnectMs);
    timers.add(t);
  };

  const connect = (relay: string, attempt: number): void => {
    if (closed) return;
    let ws: WebSocket;
    let subId = "";
    let done = false; // this socket has already scheduled its reconnect
    opts.onStatus?.({ relay, status: "connecting", attempt });

    const retry = (detail: string): void => {
      if (done) return;
      done = true;
      sockets.delete(ws);
      if (closed) return;
      opts.onStatus?.({ relay, status: "error", attempt, detail });
      later(relay, attempt);
    };

    try {
      ws = new WS(relay);
    } catch (err) {
      // A constructor that throws (a scheme this origin refuses outright) is
      // still retried on the same schedule — one call per reconnectMs, and the
      // retry logic stays in one place. There is no socket to register.
      opts.onStatus?.({ relay, status: "error", attempt, detail: err instanceof Error ? err.message : String(err) });
      if (!closed) later(relay, attempt);
      return;
    }
    sockets.add(ws);

    ws.onopen = () => {
      if (closed) {
        try {
          ws.close();
        } catch {
          /* already closing */
        }
        return;
      }
      subId = randomSubId();
      const req: NostrFilter = { ...filter };
      if (since !== undefined) req.since = since;
      // NO `limit` and NO `until`: see PAGING above. Either one turns the live
      // REQ into a bounded sample and silences everything past it.
      delete req.until;
      delete req.limit;
      try {
        ws.send(JSON.stringify(["REQ", subId, req]));
        opts.onStatus?.({ relay, status: "open", attempt });
      } catch (err) {
        retry(err instanceof Error ? err.message : String(err));
      }
    };

    ws.onerror = () => retry("socket error");
    ws.onclose = () => retry("closed");

    ws.onmessage = (msg: MessageEvent) => {
      if (closed) return;
      let parsed: unknown;
      try {
        parsed = JSON.parse(typeof msg.data === "string" ? msg.data : String(msg.data));
      } catch {
        return; // ignore malformed frames from an untrusted relay
      }
      if (!Array.isArray(parsed) || parsed.length === 0) return;
      const [type, ...rest] = parsed as [string, ...unknown[]];
      if (type === "EVENT" && rest[0] === subId) {
        const e = rest[1] as NostrEvent;
        if (!e || typeof e.id !== "string") return;
        const key = eventIdentity(e);
        if (seen.has(key)) return;
        seen.add(key);
        if (typeof e.created_at === "number" && (since === undefined || e.created_at > since)) {
          since = e.created_at;
        }
        opts.onEvent(e);
      } else if (type === "EOSE" && rest[0] === subId) {
        // The subscription STAYS OPEN. EOSE means "that is my backlog", not
        // "that is all there will ever be" — everything this function exists for
        // arrives after it.
        opts.onStatus?.({ relay, status: "eose", attempt });
      } else if (type === "CLOSED" && rest[0] === subId) {
        const detail = typeof rest[1] === "string" ? rest[1] : JSON.stringify(rest[1] ?? "");
        try {
          ws.close();
        } catch {
          /* already closing */
        }
        retry(`subscription closed: ${detail}`);
      }
    };
  };

  for (const relay of relays) connect(relay, 0);

  return {
    close(): void {
      closed = true;
      for (const t of timers) clearTimeout(t);
      timers.clear();
      for (const ws of sockets) {
        try {
          ws.close();
        } catch {
          /* already closed */
        }
      }
      sockets.clear();
    },
  };
}
