// A WebSocket stand-in that behaves like a REAL nostr relay: it APPLIES the
// REQ filter it is sent (kinds, authors, #-tags, since/until), answers
// newest-first, and caps one REQ at its own server-side maximum.
//
// WHY THIS EXISTS (ready-5c5). The board suite's other two relay fakes both
// ignore the filter entirely:
//
//   - main.test.ts's FakeRelayWebSocket replays every scripted event whatever
//     it was asked for ("HOSTILE by construction"). That is the right model for
//     proving client-side verification catches a relay serving too MUCH.
//   - the UnderReturningRelayWebSocket added earlier in ready-5c5 only looked at
//     whether `authors` was present, and ignored tag filters too.
//
// A stub that ignores tag filters CANNOT DISTINGUISH A CORRECT FILTER FROM ONE
// THAT MATCHES NOTHING. That is not a theoretical gap: main.ts's single-board
// query was changed to `{kinds:[30301,39301], "#a":[coord]}`, 531 tests stayed
// green, and against a relay that honours tag filters the same code rendered
// ZERO boards — a kind-30301 board definition carries no "a" tag, so the filter
// matched only grants. The suite asserted the SHAPE of the filter and then
// asserted a render the stub would have produced for any filter at all.
//
// Everything modelled here was measured against wss://relay.3dl.network on
// 2026-07-29:
//   REQ {kinds:[30301]}                          -> 57 events (2 authors)
//   REQ {kinds:[30301,39301], "#a":[boardCoord]} -> grants only, 0 definitions
//   REQ {kinds:[30301], "#d":[boardD]}           -> the definition
//   REQ {kinds:[30302]}                          -> exactly 500 (its cap)
//   REQ {kinds:[30302], limit:5000}              -> CLOSED, over-cap
//   the same walked with `until`                 -> 5648 over 13 REQs

import type { NostrEvent } from "./nostrevent";
import type { NostrFilter } from "./relay";

export interface Nip01RelayHandle {
  /** Every REQ filter the relay received, in wire order — paging pages
   * included, so a test can assert the client really walked `until`. */
  requests: NostrFilter[];
  /** Every URL a socket was opened for, in order (duplicates kept). */
  urls: string[];
}

export interface Nip01RelayConfig {
  /** Everything the relay stores. */
  events: NostrEvent[];
  /**
   * The most events ONE REQ may answer with, whatever the client asked for.
   * Defaults to Infinity (an uncapped relay). Set it low to prove a client
   * pages: wss://relay.3dl.network's real value is 500.
   */
  maxLimit?: number;
  /**
   * The production author-index defect, faithfully: when a REQ carries a
   * non-empty `authors`, the relay answers out of THIS store instead of
   * `events`. Measured shape — a paged `{kinds:[30301], authors:[owner]}` REQ
   * served 42 of an owner's 56 boards while a paged `{kinds:[30301]}` REQ,
   * same relay same run, served all 56.
   */
  authorIndex?: NostrEvent[];
  /**
   * Answer a REQ whose EXPLICIT `limit` exceeds `maxLimit` with a CLOSED frame
   * rather than clamping, as wss://relay.3dl.network does ("requested limit
   * 5000 exceeds this relay's max of 500 — no silent truncation").
   */
  closedOverLimit?: boolean;
}

const TAG_KEYS = ["#d", "#a", "#p", "#e"] as const;

/** matchesFilter is NIP-01 filter semantics: every condition present must hold
 * (AND), and each condition is satisfied by ANY of its listed values (OR). */
export function matchesFilter(e: NostrEvent, f: NostrFilter): boolean {
  if (f.kinds && !f.kinds.includes(e.kind)) return false;
  if (f.authors && !f.authors.includes(e.pubkey)) return false;
  if (f.since !== undefined && e.created_at < f.since) return false;
  if (f.until !== undefined && e.created_at > f.until) return false;
  for (const key of TAG_KEYS) {
    const wanted = (f as Record<string, unknown>)[key] as string[] | undefined;
    if (!wanted) continue;
    const tagName = key.slice(1);
    const have = (e.tags ?? []).filter((t) => t[0] === tagName).map((t) => t[1]);
    if (!have.some((v) => wanted.includes(v))) return false;
  }
  return true;
}

/** Relays answer newest-first; the `until` walk depends on it. */
function newestFirst(a: NostrEvent, b: NostrEvent): number {
  if (a.created_at !== b.created_at) return b.created_at - a.created_at;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

/**
 * makeNip01Relay returns a `webSocketCtor`-compatible class plus a handle
 * recording what the client actually sent. Each call makes an INDEPENDENT
 * relay, so tests never share static state.
 */
export function makeNip01Relay(config: Nip01RelayConfig): {
  ctor: typeof WebSocket;
  handle: Nip01RelayHandle;
} {
  const handle: Nip01RelayHandle = { requests: [], urls: [] };
  const maxLimit = config.maxLimit ?? Number.POSITIVE_INFINITY;

  class Nip01RelayWebSocket {
    url: string;
    onopen: (() => void) | null = null;
    onerror: ((ev?: unknown) => void) | null = null;
    onclose: ((ev?: unknown) => void) | null = null;
    onmessage: ((ev: { data: string }) => void) | null = null;

    constructor(url: string) {
      this.url = url;
      handle.urls.push(url);
      // Deferred: relay.ts assigns its handlers immediately AFTER `new WS(url)`
      // returns.
      queueMicrotask(() => this.onopen?.());
    }

    send(data: string): void {
      const frame = JSON.parse(data) as [string, string, NostrFilter?];
      if (frame[0] !== "REQ") return; // CLOSE and anything else: nothing to do
      const [, subId, filter = {}] = frame;
      handle.requests.push(filter);

      if (config.closedOverLimit && filter.limit !== undefined && filter.limit > maxLimit) {
        queueMicrotask(() =>
          this.onmessage?.({
            data: JSON.stringify([
              "CLOSED",
              subId,
              `invalid: requested limit ${filter.limit} exceeds this relay's max of ${maxLimit}`,
            ]),
          }),
        );
        return;
      }

      const usesAuthorIndex = Array.isArray(filter.authors) && filter.authors.length > 0;
      const store = usesAuthorIndex && config.authorIndex ? config.authorIndex : config.events;
      const matched = store.filter((e) => matchesFilter(e, filter)).sort(newestFirst);
      const cap = Math.min(filter.limit ?? Number.POSITIVE_INFINITY, maxLimit);
      const served = Number.isFinite(cap) ? matched.slice(0, cap) : matched;

      queueMicrotask(() => {
        for (const e of served) {
          this.onmessage?.({ data: JSON.stringify(["EVENT", subId, e]) });
        }
        this.onmessage?.({ data: JSON.stringify(["EOSE", subId]) });
      });
    }

    close(): void {
      /* nothing to tear down */
    }
  }

  return { ctor: Nip01RelayWebSocket as unknown as typeof WebSocket, handle };
}
