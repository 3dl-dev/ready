// Confidential-board content envelope, browser side. This file is the MERGE of
// two independently-built ports (ready-35b's fold-facing rules and ready-c4b's
// reader-facing AEAD), unified in ready-56b so there is exactly one place the
// browser decides whether sealed bytes become visible text.
//
// It mirrors pkg/sync/envelope.go against the FROZEN wire contract in
// docs/design/confidential-boards-envelope.md §3 (see also board-fold-spec.md
// §11 for the fold rules):
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
// ON THE AEAD IMPLEMENTATION. ready-c4b took @noble/ciphers as a runtime
// dependency for the open; this merge keeps ./chacha20poly1305 instead, because
// that hand-rolled implementation is the one fold.vectors.test.ts drives with
// Go-GENERATED confidential vectors (confidential_* in internal/foldvectors) —
// i.e. it is already proven byte-for-byte against the writer, which is stronger
// evidence than an unproven swap would carry. envelope.test.ts's four tamper
// mutations (flipped ciphertext byte, flipped tag byte, wrong CEK, truncated
// envelope) run against openContent regardless of which implementation backs
// it, so c4b's fail-closed property is unchanged.

import type { NostrEvent } from "./nostrevent";
import { tagValue } from "./nostrevent";
import { open as aeadOpen } from "./chacha20poly1305";
import { hexToBytes } from "./sha256";
import { hmacSha256 } from "./hmac";

export const KindBoard = 30301;

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

/** PLACEHOLDER_TEXT is ready-35b's spelling of the same constant (spec
 * §11.7-§11.8), kept so fold.ts and the vector suite read as the spec does. */
export const PLACEHOLDER_TEXT = PLACEHOLDER;

/** BoardDecryptor supplies per-board content-encryption keys. Mirrors
 * pkg/sync/envelope.go's BoardDecryptor interface. lib/keyring.ts's
 * BoardKeyring satisfies it structurally — that is how a reader's grant-derived
 * keys reach the fold. */
export interface BoardDecryptor {
  cek(boardCoord: string, epoch: number): Uint8Array | null;
}

/** EncryptedBoardSet marks which boards are confidential and their cutover
 * (unix seconds). Mirrors pkg/sync/envelope.go's EncryptedBoardSet. */
export interface EncryptedBoardSet {
  cutover(boardCoord: string): { cutover: number; ok: boolean };
}

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
  const nonce = raw.slice(0, NONCE_SIZE);
  const ct = raw.slice(NONCE_SIZE);
  // aeadOpen returns null on a tag mismatch. That is the AUTHENTICATION
  // failure — tampering or the wrong key — and both must fail. There is no
  // "probably fine" branch here and there must never be one.
  return aeadOpen(cek, nonce, ct);
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

/** boardCoordOf mirrors envelope.go's boardCoordOf: the "a" tag whose value
 * starts with the board-kind prefix (works for a card's sole "a" tag and a
 * status event's second "a" tag alike). */
export function boardCoordOf(e: NostrEvent): string {
  const prefix = `${KindBoard}:`;
  for (const t of e.tags) {
    if (t.length >= 2 && t[0] === "a" && t[1].startsWith(prefix)) return t[1];
  }
  return "";
}

/** shouldQuarantine mirrors envelope.go's fail-closed fold gate (spec §11.3). */
export function shouldQuarantine(e: NostrEvent, ebs: EncryptedBoardSet | null): boolean {
  if (ebs === null) return false;
  const { cutover, ok } = ebs.cutover(boardCoordOf(e));
  if (!ok) return false; // board is plaintext
  if (encWellFormed(e)) return false; // proper confidential event — keep
  // Grandfather ONLY a genuine pre-cutover plaintext event.
  if (e.created_at < cutover && !isConfidential(e)) return false;
  return true;
}

/** cekFor mirrors envelope.go's cekFor: resolves the CEK for a confidential
 * event, null on ANY failure (fail-closed). */
function cekFor(e: NostrEvent, dec: BoardDecryptor | null): Uint8Array | null {
  if (dec === null || tagValue(e, TAG_ENC) !== ENC_VERSION) return null;
  const epoch = cekEpochOf(e);
  if (epoch === null) return null;
  return dec.cek(boardCoordOf(e), epoch);
}

/** The fold's snake_case view of the sealed card blob (spec §3.1). Same bytes
 * as CardPlaintext; the two spellings exist because fold.ts speaks the spec's
 * field names and carditems.ts speaks the UI's. */
interface CardPayload {
  title: string;
  context?: string;
  waiting_on?: string;
  labels?: string[];
}

interface StatusPayload {
  reason: string;
}

/**
 * decryptCardPayload mirrors envelope.go's decryptCardPayload.
 *
 * Returns null unless the opened plaintext is a JSON object whose `title` is
 * a string — the same shape gate decodeCardPayload applies. A payload that
 * opens (AEAD succeeds) but does not parse to that shape is NOT a decryption
 * success: nothing downstream can tell "opened to garbage" from "opened to a
 * real card" once a non-null value comes back, and the fold (fold.ts's
 * itemFromCard) treats non-null as success, assigning item.title = pl.title
 * directly. An undefined title there renders a blank card title AND skips the
 * .sealed CSS class (board/render.ts gates both title and PLACEHOLDER
 * equality) — the reader loses the [encrypted] signal entirely. So this must
 * fail closed here, before the fold ever sees the value, exactly like
 * decodeCardPayload already does.
 */
export function decryptCardPayload(e: NostrEvent, dec: BoardDecryptor | null): CardPayload | null {
  const cek = cekFor(e, dec);
  if (cek === null) return null;
  const raw = openContent(cek, e.content);
  if (raw === null) return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(new TextDecoder().decode(raw));
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null || Array.isArray(parsed)) return null;
  const o = parsed as Record<string, unknown>;
  if (typeof o.title !== "string") return null;
  return {
    title: o.title,
    context: typeof o.context === "string" ? o.context : undefined,
    waiting_on: typeof o.waiting_on === "string" ? o.waiting_on : undefined,
    labels: Array.isArray(o.labels) ? o.labels.filter((l): l is string => typeof l === "string") : undefined,
  };
}

/** decryptStatusReason mirrors envelope.go's decryptStatusReason. */
export function decryptStatusReason(e: NostrEvent, dec: BoardDecryptor | null): string | null {
  const cek = cekFor(e, dec);
  if (cek === null) return null;
  const raw = openContent(cek, e.content);
  if (raw === null) return null;
  try {
    const pl = JSON.parse(new TextDecoder().decode(raw)) as StatusPayload;
    return pl.reason;
  } catch {
    return null;
  }
}

/** labelToken mirrors envelope.go's labelToken: lowercase-hex
 * HMAC-SHA256(LTK, label). A granted reader uses it to RESOLVE the opaque
 * `l` tokens on a confidential card back to label text; the fold itself never
 * needs to compute one. */
export function labelToken(ltk: Uint8Array, label: string): string {
  return Array.from(hmacSha256(ltk, new TextEncoder().encode(label)))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

export const __internal = { encWellFormed, hexToBytes };
