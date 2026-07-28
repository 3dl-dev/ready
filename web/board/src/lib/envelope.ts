// Confidential-board content envelope, browser side (ready-c4b). Mirror of
// pkg/sync/envelope.go's openContent / decryptCardPayload against the FROZEN
// wire contract in docs/design/confidential-boards-envelope.md §3:
//
//	event.Content = base64Std( nonce(12) ‖ ChaCha20-Poly1305(CEK, nonce, plaintext) )
//
// This AEAD (the CONTENT body, under the per-board CEK) is DISTINCT from the
// NIP-44 v2 envelope that wraps the 32-byte CEK itself. NIP-44 never happens in
// this file — it happens in the signer, behind keyunwrap.ts. Do not conflate them.
//
// FAIL CLOSED IS THE WHOLE POINT OF THIS MODULE. ChaCha20-Poly1305 is an AEAD:
// a Poly1305 tag mismatch means the ciphertext was tampered with OR the key is
// wrong, and there is no third possibility worth guessing at. So every function
// here returns null on ANY defect — bad base64, a body too short to hold a nonce
// and a tag, a wrong CEK, a flipped ciphertext byte, a flipped tag byte, JSON
// that does not parse, a payload that is not a JSON object. Nothing here ever
// returns a partial result, a best-effort string, or the raw ciphertext, and
// nothing here throws: a null propagates to the placeholder in the UI.
//
// WHY A DEPENDENCY FOR THE AEAD (and not a hand-roll like sha256.ts /
// secp256k1.ts / bech32.ts). Those three were hand-written for one reason,
// recorded in their headers: dist_test.go's external-reference scan rejected
// every "//" in the shipped bundle, so a dependency's `// @license MIT
// https://...` banner could not be bundled at all. ready-8c5 fixed that guard
// (it now exempts the trailing legal-comment region esbuild emits under
// legalComments: "eof"), so the reason is gone. ChaCha20-Poly1305 is also a
// worse hand-roll candidate than a verifier: it is the CONFIDENTIALITY boundary
// rather than an authenticity check, its failure mode is silent rather than
// loud, and Poly1305 needs constant-time 130-bit arithmetic that JavaScript
// does not make easy to get right. @noble/ciphers is audited, dependency-free,
// and its own test suite runs the RFC 8439 vectors — so it is taken as a
// runtime dependency and TestDist_ExternalRefScanToleratesBannersAndRelayURLs
// is what keeps the bundle honest about it.

import { chacha20poly1305 } from "@noble/ciphers/chacha.js";
import type { NostrEvent } from "./nostrevent";
import { tagValue } from "./nostrevent";

/** The clear envelope-version discriminator; matches pkg/sync/envelope.go's
 * encVersion. A reader version-dispatches on it and NEVER guesses: an unknown
 * version is malformed, not "probably compatible". */
export const ENC_VERSION = "1";

export const TAG_ENC = "enc";
export const TAG_CEK_EPOCH = "cek_epoch";

/** NONCE_SIZE / TAG_SIZE are ChaCha20-Poly1305 (IETF) fixed sizes and match
 * chacha20poly1305.NonceSize / .Overhead on the Go side. */
export const NONCE_SIZE = 12;
export const TAG_SIZE = 16;

/**
 * PLACEHOLDER is what the UI shows in place of confidential free text the
 * reader cannot decrypt. It is byte-identical to pkg/sync/envelope.go's
 * placeholderText so `rd list` and the board page say the same thing about the
 * same item.
 *
 * It must be VISIBLY a placeholder — not blank, not an empty string, not a
 * truncated attempt at the real title. A blank cell reads as "this item has no
 * title"; "[encrypted]" reads as "you are not holding the key for this one",
 * which is the true statement.
 */
export const PLACEHOLDER = "[encrypted]";

/** isConfidential reports whether the event carries an enc marker AT ALL (any
 * version) — the read path's placeholder-vs-plaintext discriminator, mirroring
 * envelope.go's isConfidential. A card carrying an unknown enc version is still
 * confidential; it just can never be opened. */
export function isConfidential(e: Pick<NostrEvent, "tags">): boolean {
  return tagValue(e, TAG_ENC) !== "";
}

/** cekEpochOf parses the clear cek_epoch marker, or null when it is absent or
 * not an integer. Mirrors the strconv.Atoi in envelope.go's cekFor: an
 * unparseable epoch is a defect, not epoch 0. */
export function cekEpochOf(e: Pick<NostrEvent, "tags">): number | null {
  const raw = tagValue(e, TAG_CEK_EPOCH);
  if (!/^-?\d+$/.test(raw)) return null;
  const n = Number(raw);
  return Number.isSafeInteger(n) ? n : null;
}

const BASE64_STD = /^[A-Za-z0-9+/]*={0,2}$/;

/**
 * decodeBase64Std decodes STANDARD base64 (Go's base64.StdEncoding), returning
 * null rather than throwing on anything malformed.
 *
 * It validates the alphabet and the length before handing bytes to atob because
 * atob is more permissive than Go's decoder in ways that matter here: it
 * tolerates some unpadded inputs, so a truncated relay payload could decode to
 * a SHORTER byte string instead of failing. Since the length check below is
 * what rejects a truncated envelope, silently shortening the input is exactly
 * the wrong direction. Reject it here instead.
 */
export function decodeBase64Std(s: string): Uint8Array | null {
  if (s.length === 0 || s.length % 4 !== 0 || !BASE64_STD.test(s)) return null;
  try {
    const bin = atob(s);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}

/**
 * openContent reverses pkg/sync/envelope.go's sealContent: base64-decode, split
 * nonce‖ciphertext, ChaCha20-Poly1305 open under the CEK.
 *
 * Returns null — never a partial result, never the ciphertext, never a throw —
 * for a wrong CEK, a truncated payload, a flipped ciphertext byte or a flipped
 * Poly1305 tag byte. Those are the four cases envelope.test.ts mutation-proves,
 * and they are indistinguishable to the AEAD by construction, which is why they
 * all take the same exit.
 */
export function openContent(cek: Uint8Array, payload: string): Uint8Array | null {
  if (cek.length !== 32) return null;
  const raw = decodeBase64Std(payload);
  if (raw === null) return null;
  if (raw.length < NONCE_SIZE + TAG_SIZE) return null;
  const nonce = raw.subarray(0, NONCE_SIZE);
  const ct = raw.subarray(NONCE_SIZE);
  try {
    return chacha20poly1305(cek, nonce).decrypt(ct);
  } catch {
    // @noble/ciphers throws on a tag mismatch. That is the AUTHENTICATION
    // failure — tampering or the wrong key — and both must fail. There is no
    // "probably fine" branch here and there must never be one.
    return null;
  }
}

/** CardPlaintext is the decrypted free-text blob of a confidential card — the
 * exact struct pkg/sync/envelope.go's cardPayload marshals (spec §3.1). Write
 * and read agree byte-for-byte or nothing renders. */
export interface CardPlaintext {
  title: string;
  context: string;
  waitingOn: string;
  labels: string[];
}

/**
 * decodeCardPayload parses the sealed card blob. Returns null unless it is a
 * JSON object whose `title` is a string — a payload that decrypted but does not
 * carry a title is not a card payload, and rendering `undefined` for it would
 * be a silent fall-through rather than a fail-closed one.
 *
 * `context`, `waiting_on` and `labels` are omitempty on the Go side, so absent
 * is normal and becomes "" / []. A present-but-wrong-typed field is dropped
 * rather than coerced (String(obj) of a hostile value is how "[object Object]"
 * ends up in a UI).
 */
export function decodeCardPayload(raw: Uint8Array): CardPlaintext | null {
  let parsed: unknown;
  try {
    parsed = JSON.parse(new TextDecoder("utf-8", { fatal: true }).decode(raw));
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return null;
  const o = parsed as Record<string, unknown>;
  if (typeof o.title !== "string") return null;
  return {
    title: o.title,
    context: typeof o.context === "string" ? o.context : "",
    waitingOn: typeof o.waiting_on === "string" ? o.waiting_on : "",
    labels: Array.isArray(o.labels) ? o.labels.filter((l): l is string => typeof l === "string") : [],
  };
}

/**
 * encWellFormed is the STRUCTURAL half of the fold gate, ported from
 * envelope.go's encWellFormed: a KNOWN enc version, a parseable cek_epoch, and
 * a Content that base64-decodes to at least nonce+tag.
 *
 * It deliberately does NOT verify the AEAD — the gate runs before any key is
 * resolved, and a member's read path does the AEAD verify. What it rules out is
 * a card that is merely enc-SHAPED: an unknown version, a missing epoch, an
 * empty body, or cleartext smuggled into Content behind an enc marker.
 */
export function encWellFormed(e: Pick<NostrEvent, "tags" | "content">): boolean {
  if (tagValue(e, TAG_ENC) !== ENC_VERSION) return false;
  if (cekEpochOf(e) === null) return false;
  const raw = decodeBase64Std(e.content);
  return raw !== null && raw.length >= NONCE_SIZE + TAG_SIZE;
}
