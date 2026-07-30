// NIP-01 event id derivation + BIP-340 verification, ported from
// pkg/nostr/event.go (canonicalForID / serializeString / Verify) so the
// browser computes the exact same event id and accepts the exact same
// signatures the Go CLI does. This file does NOT sign (no secret-key
// handling here — see SCOPE note in boarddiscovery.ts); it only recomputes
// the id and verifies, which is all a READ-ONLY board-discovery client needs.

import { sha256, sha256Hex, hexToBytes } from "./sha256";
import { schnorrVerify } from "./secp256k1";

export interface NostrEvent {
  id: string;
  pubkey: string;
  created_at: number;
  kind: number;
  tags: string[][];
  content: string;
  sig: string;
}

// serializeString mirrors event.go's serializeString: NIP-01's minimal escape
// set (", \, \n, \r, \t, \b, \f, and \u00XX for other control chars below
// 0x20). Every other byte — including all of UTF-8 and characters like < > &
// — is emitted verbatim, unlike JSON.stringify which does the same for ASCII
// but this keeps the two implementations trivially comparable byte-for-byte.
function serializeString(s: string): string {
  let out = '"';
  for (let i = 0; i < s.length; i++) {
    const c = s[i];
    const code = s.charCodeAt(i);
    switch (c) {
      case '"':
        out += '\\"';
        break;
      case "\\":
        out += "\\\\";
        break;
      case "\n":
        out += "\\n";
        break;
      case "\r":
        out += "\\r";
        break;
      case "\t":
        out += "\\t";
        break;
      case "\b":
        out += "\\b";
        break;
      case "\f":
        out += "\\f";
        break;
      default:
        if (code < 0x20) {
          out += "\\u" + code.toString(16).padStart(4, "0");
        } else {
          out += c;
        }
    }
  }
  out += '"';
  return out;
}

/** canonicalForID mirrors event.go's canonicalForID: the exact NIP-01 byte
 * serialization the event id is the sha256 of. */
export function canonicalForID(e: Pick<NostrEvent, "pubkey" | "created_at" | "kind" | "tags" | "content">): string {
  let out = "[0,";
  out += serializeString(e.pubkey) + ",";
  out += String(e.created_at) + ",";
  out += String(e.kind) + ",";
  out += "[";
  for (let i = 0; i < e.tags.length; i++) {
    if (i > 0) out += ",";
    out += "[";
    const tag = e.tags[i];
    for (let j = 0; j < tag.length; j++) {
      if (j > 0) out += ",";
      out += serializeString(tag[j]);
    }
    out += "]";
  }
  out += "],";
  out += serializeString(e.content);
  out += "]";
  return out;
}

export function computeEventId(e: Pick<NostrEvent, "pubkey" | "created_at" | "kind" | "tags" | "content">): string {
  return sha256Hex(new TextEncoder().encode(canonicalForID(e)));
}

/**
 * VERIFICATION MEMO (ready-fe4). BIP-340 verification in this bundle is
 * hand-rolled BigInt curve arithmetic (secp256k1.ts, validated against the
 * official bitcoin/bips vectors), so ONE verifyEvent call costs ~2ms — and the
 * load path calls it on the same event several times over: the fold's
 * verifiedEvents, sealedEvidenceOf's own check, deriveBoardKeyring's CHECK 1,
 * deriveLevels' per-grant check, and the writer's re-projection each verify the
 * same snapshot independently, on purpose (each module refuses to trust its
 * caller). Measured against wss://relay.3dl.network 2026-07-30 over 12 of the
 * owner's boards: 69.1s to load, of which only 8.3s was the relay. The other
 * 60.8s was this function, run repeatedly on bytes it had already ruled on.
 *
 * THE MEMO REMOVES NO CHECK. It is keyed on eventIdentity — the FULL content
 * plus the signature, the same key dedupeExact uses and for the same ready-dd5
 * reason — so a forgery that reuses a genuine event's id but changes any other
 * field gets a different key and is verified on its own merits. Two events that
 * share a key are byte-identical, and verifyEvent is a pure function of those
 * bytes, so serving a remembered answer is indistinguishable from recomputing
 * it. Keying on `e.id` INSTEAD would be a real vulnerability (a forgery
 * inheriting a genuine event's verdict), which is why it is not the key.
 *
 * The cap is a memory bound, not a policy: a portfolio session folds tens of
 * thousands of events and this map would otherwise grow for the life of the
 * page. Overflow clears rather than evicting one entry, which costs a re-verify
 * and keeps this file free of an LRU.
 */
const VERIFY_MEMO_MAX = 250_000;
const verifyMemo = new Map<string, boolean>();

/** resetVerifyMemo drops every remembered verdict. EXPORTED FOR TESTS: a suite
 * that wants to observe the real cost, or that reuses one event object across
 * cases, must be able to start from empty. Production never calls it. */
export function resetVerifyMemo(): void {
  verifyMemo.clear();
}

/**
 * verifyEvent mirrors event.go's (*Event).Verify: re-derives the id from the
 * canonical serialization AND checks the BIP-340 schnorr signature against
 * the event's own pubkey. Returns false (never throws) for any malformed or
 * tampered event — a relay is untrusted, so this must fail closed on garbage
 * input, not crash the discovery loop that calls it once per relay event.
 *
 * Results are memoized by full content; see VERIFICATION MEMO above for why
 * that is not a weakening.
 */
export function verifyEvent(e: NostrEvent): boolean {
  let key: string | undefined;
  try {
    key = eventIdentity(e);
  } catch {
    key = undefined; // unserializable input: verify it the long way, memo nothing
  }
  if (key !== undefined) {
    const memo = verifyMemo.get(key);
    if (memo !== undefined) return memo;
  }
  const ok = verifyEventUncached(e);
  if (key !== undefined) {
    if (verifyMemo.size >= VERIFY_MEMO_MAX) verifyMemo.clear();
    verifyMemo.set(key, ok);
  }
  return ok;
}

function verifyEventUncached(e: NostrEvent): boolean {
  try {
    const wantId = computeEventId(e);
    if (e.id !== wantId) return false;
    if (!/^[0-9a-f]{64}$/.test(e.pubkey)) return false;
    if (!/^[0-9a-f]{128}$/.test(e.sig)) return false;
    const idBytes = sha256(new TextEncoder().encode(canonicalForID(e)));
    return schnorrVerify(hexToBytes(e.pubkey), idBytes, hexToBytes(e.sig));
  } catch {
    return false;
  }
}

declare const verifiedEventBrand: unique symbol;

/**
 * VerifiedEvent is a NostrEvent that HAS passed verifyEvent. The brand is a
 * phantom property no literal can satisfy, so the ONLY way to obtain one is
 * verifiedEvents() below — which actually runs the check. A derivation that
 * must not read an unverified event (deriveLevels, rolegrant.ts) takes
 * VerifiedEvent and is therefore STRUCTURALLY incapable of being handed the
 * raw relay array: `tsc` rejects the call, so the check cannot be dropped
 * silently by a future port the way it was in ready-75a.
 *
 * The brand is compile-time only (erased at runtime), so it is a guard against
 * MISTAKES, not against a deliberate cast. Runtime safety in rolegrant.ts is
 * still enforced by its own per-event verifyEvent call, mirroring Go's
 * deriveGrants. Two layers, two different failure modes: the brand stops a
 * caller from feeding unverified events in, the in-loop check stops the
 * damage if some future caller casts around the brand.
 */
export type VerifiedEvent = NostrEvent & { readonly [verifiedEventBrand]: true };

/**
 * verifiedEvents is the ONE mint for VerifiedEvent: it drops null elements
 * (board-fold-spec §3.1) and every event whose id/signature does not check
 * out (§3.3), preserving input order for the survivors. Callers that fold a
 * relay snapshot verify ONCE here and reuse the result for every downstream
 * derivation, so no derivation re-decides trust from raw input.
 */
export function verifiedEvents(events: readonly (NostrEvent | null)[]): VerifiedEvent[] {
  const out: VerifiedEvent[] = [];
  for (const e of events) {
    if (e === null || e === undefined) continue;
    if (!verifyEvent(e)) continue;
    out.push(e as VerifiedEvent);
  }
  return out;
}

/**
 * eventIdentity is a total, collision-free key for an event's FULL content:
 * every signed field plus the signature. Two byte-identical copies of one
 * event (the same event served by two relays) share a key; two events that
 * differ ANYWHERE — including a forgery that reuses a genuine event's id and
 * signature but tampers with the tags — do not.
 *
 * Deduping a relay snapshot on the self-declared `id` alone is unsafe before
 * verification (ready-dd5): a forgery asserting a genuine event's id EVICTS
 * the genuine event from the map, and the fold then rejects the forgery,
 * losing the real event entirely. Keying on the full content instead means
 * dedup only ever collapses exact duplicates; adversarial near-copies all
 * survive transport and are resolved by the fold, which verifies BEFORE it
 * records an id as seen (fold.ts §3.2/§3.3).
 */
export function eventIdentity(e: NostrEvent): string {
  return JSON.stringify([e.id, e.pubkey, e.created_at, e.kind, e.tags, e.content, e.sig]);
}

/** dedupeExact collapses byte-identical duplicates only, preserving first-seen
 * order. See eventIdentity for why the event id is NOT the dedup key. */
export function dedupeExact(events: readonly NostrEvent[]): NostrEvent[] {
  const seen = new Set<string>();
  const out: NostrEvent[] = [];
  for (const e of events) {
    if (!e || typeof e.id !== "string") continue;
    const key = eventIdentity(e);
    if (seen.has(key)) continue;
    seen.add(key);
    out.push(e);
  }
  return out;
}

export function tagValue(e: Pick<NostrEvent, "tags">, name: string): string {
  for (const t of e.tags) {
    if (t.length >= 2 && t[0] === name) return t[1];
  }
  return "";
}

/** tagValues returns the values of every tag whose name matches, in tag
 * order. Mirrors pkg/sync/nostrwire.go's tagValues (used for repeatable tags:
 * "i" per blocking dependency, "l" per label). */
export function tagValues(e: Pick<NostrEvent, "tags">, name: string): string[] {
  const out: string[] = [];
  for (const t of e.tags) {
    if (t.length >= 2 && t[0] === name) out.push(t[1]);
  }
  return out;
}
