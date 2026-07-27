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
 * verifyEvent mirrors event.go's (*Event).Verify: re-derives the id from the
 * canonical serialization AND checks the BIP-340 schnorr signature against
 * the event's own pubkey. Returns false (never throws) for any malformed or
 * tampered event — a relay is untrusted, so this must fail closed on garbage
 * input, not crash the discovery loop that calls it once per relay event.
 */
export function verifyEvent(e: NostrEvent): boolean {
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

export function tagValue(e: Pick<NostrEvent, "tags">, name: string): string {
  for (const t of e.tags) {
    if (t.length >= 2 && t[0] === name) return t[1];
  }
  return "";
}
