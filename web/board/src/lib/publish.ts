// publish.ts — NIP-01 EVENT publish, the write-side counterpart of relay.ts's
// REQ/EOSE read client (ready-b2b).
//
// SIGNING IS NIP-07 ONLY. This module never sees, asks for, or accepts a secret
// key: it hands an UNSIGNED event to `window.nostr.signEvent` and gets a signed
// one back. There is no nsec fallback anywhere in this page, by design (see
// nip07.ts and main.ts) — a board with no NIP-07 provider is read-only and says
// so, which is the security model, not a limitation to route around.
//
// WHAT IT VERIFIES BEFORE PUBLISHING, and why: a status event anchors to the
// card published in the same operation by that card's concrete event id. The
// caller computed that id from the UNSIGNED card (the id is sig-independent), so
// a signer that quietly rewrote any field — created_at is the realistic one,
// some providers stamp their own — would produce a card whose real id no longer
// matches the anchor, and rd would fold a status event pointing at nothing.
// assertSignedAsBuilt refuses that case loudly instead of publishing a
// half-broken pair.
//
// RELAY OUTCOMES ARE NOT COSMETIC. NIP-01 says a relay answers every EVENT with
// ["OK", <id>, <accepted>, <message>]. The board's optimistic UI reverts on
// rejection and shows the relay's own message, so this client waits for that
// frame rather than assuming a send is a success — "the socket didn't error" is
// not acceptance, and a write-policy relay (rd's own relays enforce one) will
// send OK=false with a reason like "blocked: not on the allowlist".

import type { NostrEvent } from "./nostrevent";
import { computeEventId } from "./nostrevent";

/** The NIP-07 signing surface. Deliberately its own interface: nip07.ts's
 * Nip07Provider is the READ surface (getPublicKey/nip44) and stays usable by a
 * provider that cannot sign. */
export interface Nip07Signer {
  signEvent(event: {
    created_at: number;
    kind: number;
    tags: string[][];
    content: string;
    pubkey?: string;
  }): Promise<NostrEvent>;
}

export class SignerMissingError extends Error {
  constructor() {
    super(
      "no NIP-07 signer: this board is read-only in this browser. Install a nostr extension " +
        "(nos2x, Alby, …) and unlock it — the board never accepts a secret key.",
    );
    this.name = "SignerMissingError";
  }
}

export class SignerMismatchError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "SignerMismatchError";
  }
}

export class RelayRejectedError extends Error {
  readonly eventId: string;
  readonly relayMessages: string[];
  constructor(eventId: string, relayMessages: string[]) {
    super(
      relayMessages.length > 0
        ? `the relay rejected this change: ${relayMessages.join("; ")}`
        : `no relay accepted this change (all ${0} relays timed out or failed)`,
    );
    this.name = "RelayRejectedError";
    this.eventId = eventId;
    this.relayMessages = relayMessages;
  }
}

export function hasSigner(win: Pick<Window, "nostr"> = window): boolean {
  return typeof (win.nostr as Partial<Nip07Signer> | undefined)?.signEvent === "function";
}

/**
 * signWith hands one unsigned event to the extension and validates what comes
 * back: the id must be the id the caller computed, the signer's pubkey must be
 * the identity the page is acting as, and every field must be untouched.
 */
export async function signWith(
  signer: Nip07Signer,
  built: { id: string; pubkey: string; created_at: number; kind: number; tags: string[][]; content: string },
): Promise<NostrEvent> {
  const signed = await signer.signEvent({
    pubkey: built.pubkey,
    created_at: built.created_at,
    kind: built.kind,
    tags: built.tags,
    content: built.content,
  });
  assertSignedAsBuilt(built, signed);
  return signed;
}

export function assertSignedAsBuilt(
  built: { id: string; pubkey: string; created_at: number; kind: number },
  signed: NostrEvent,
): void {
  if (signed.pubkey.toLowerCase() !== built.pubkey.toLowerCase()) {
    throw new SignerMismatchError(
      `the extension signed as ${signed.pubkey} but this board is acting as ${built.pubkey} — ` +
        `switch the extension's active key, or reload the board as that identity`,
    );
  }
  if (signed.created_at !== built.created_at) {
    throw new SignerMismatchError(
      `the extension changed created_at (${built.created_at} -> ${signed.created_at}); the change was ` +
        `NOT published, because the accompanying status event anchors to the card's original id`,
    );
  }
  const recomputed = computeEventId(signed);
  if (recomputed !== signed.id || signed.id !== built.id) {
    throw new SignerMismatchError(
      `the extension returned an event whose id does not match what was signed ` +
        `(built ${built.id}, returned ${signed.id}, recomputed ${recomputed}); nothing was published`,
    );
  }
}

export interface PublishOptions {
  /** Per-relay wait for the OK frame. The production relay scales to zero and a
   * cold connect has been measured at ~12s, so this sits above that with
   * margin — same bound relay.ts uses for reads. */
  timeoutMs?: number;
  /** Injectable for tests; defaults to the platform WebSocket. */
  socketFactory?: (url: string) => WebSocket;
}

export interface RelayOutcome {
  relay: string;
  accepted: boolean;
  message: string;
}

const DEFAULT_TIMEOUT_MS = 20000;

/**
 * publishToRelay opens one socket, sends every event in order, and resolves
 * with the OK outcome per event id. Events are sent on a single connection
 * because a status event that lands before its card leaves a window where a
 * reader sees a status for an unknown card; same-socket ordering keeps rd's own
 * publish order.
 */
export function publishToRelay(
  url: string,
  events: readonly NostrEvent[],
  opts: PublishOptions = {},
): Promise<Map<string, RelayOutcome>> {
  const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const make = opts.socketFactory ?? ((u: string) => new WebSocket(u));
  return new Promise((resolve) => {
    const outcomes = new Map<string, RelayOutcome>();
    let settled = false;
    let ws: WebSocket;

    const finish = (fallback: string) => {
      if (settled) return;
      settled = true;
      for (const e of events) {
        if (!outcomes.has(e.id)) outcomes.set(e.id, { relay: url, accepted: false, message: fallback });
      }
      try {
        ws?.close();
      } catch {
        /* already closed */
      }
      clearTimeout(timer);
      resolve(outcomes);
    };

    const timer = setTimeout(() => finish(`${url}: timed out after ${timeoutMs}ms`), timeoutMs);

    try {
      ws = make(url);
    } catch (err) {
      finish(`${url}: ${err instanceof Error ? err.message : String(err)}`);
      return;
    }

    ws.onopen = () => {
      for (const e of events) ws.send(JSON.stringify(["EVENT", e]));
    };
    ws.onmessage = (ev: MessageEvent) => {
      let frame: unknown;
      try {
        frame = JSON.parse(typeof ev.data === "string" ? ev.data : "");
      } catch {
        return;
      }
      if (!Array.isArray(frame)) return;
      if (frame[0] === "OK" && typeof frame[1] === "string") {
        outcomes.set(frame[1], {
          relay: url,
          accepted: frame[2] === true,
          message: typeof frame[3] === "string" ? frame[3] : "",
        });
        if (outcomes.size >= events.length) finish("");
      }
      // A NOTICE is advisory in NIP-01 and carries no event id; it is not an
      // outcome, so it never resolves or fails a publish on its own.
    };
    ws.onerror = () => finish(`${url}: connection failed`);
    ws.onclose = () => finish(`${url}: connection closed before the relay answered`);
  });
}

export interface PublishResult {
  /** Per event id, the relays that answered and how. */
  outcomes: Map<string, RelayOutcome[]>;
}

/**
 * publishEvents publishes to every relay and succeeds only when EVERY event was
 * accepted by AT LEAST ONE relay. Anything else throws RelayRejectedError
 * carrying the relays' own words, which is what the board shows the user when
 * it puts the card back where it came from.
 */
export async function publishEvents(
  relays: readonly string[],
  events: readonly NostrEvent[],
  opts: PublishOptions = {},
): Promise<PublishResult> {
  if (relays.length === 0) throw new RelayRejectedError(events[0]?.id ?? "", ["no relays are configured"]);
  const perRelay = await Promise.all(relays.map((r) => publishToRelay(r, events, opts)));
  const outcomes = new Map<string, RelayOutcome[]>();
  for (const e of events) {
    outcomes.set(
      e.id,
      perRelay.map((m) => m.get(e.id) ?? { relay: "?", accepted: false, message: "no answer" }),
    );
  }
  for (const e of events) {
    const got = outcomes.get(e.id)!;
    if (!got.some((o) => o.accepted)) {
      throw new RelayRejectedError(
        e.id,
        got.map((o) => `${o.relay}: ${o.message || "rejected"}`),
      );
    }
  }
  return { outcomes };
}
