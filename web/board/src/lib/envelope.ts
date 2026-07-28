// TS port of pkg/sync/envelope.go's confidential-board fold rules —
// board-fold-spec.md §11. Consumes a caller-supplied CEK per (board
// coordinate, epoch); it does NOT derive that CEK from a role grant (that is
// key distribution, keydist.go's domain — see nip44.ts's header for where
// that lives in this port and why it is out of this module's scope).

import type { NostrEvent } from "./nostrevent";
import { tagValue } from "./nostrevent";
import { open as aeadOpen } from "./chacha20poly1305";
import { hexToBytes } from "./sha256";
import { hmacSha256 } from "./hmac";

export const KindBoard = 30301;

/** placeholderText is what confidential free text renders as when it cannot
 * be decrypted (spec §11.7-§11.8). */
export const PLACEHOLDER_TEXT = "[encrypted]";

const ENC_VERSION = "1";
const TAG_ENC = "enc";
const TAG_CEK_EPOCH = "cek_epoch";

/** BoardDecryptor supplies per-board content-encryption keys. Mirrors
 * pkg/sync/envelope.go's BoardDecryptor interface. */
export interface BoardDecryptor {
  cek(boardCoord: string, epoch: number): Uint8Array | null;
}

/** EncryptedBoardSet marks which boards are confidential and their cutover
 * (unix seconds). Mirrors pkg/sync/envelope.go's EncryptedBoardSet. */
export interface EncryptedBoardSet {
  cutover(boardCoord: string): { cutover: number; ok: boolean };
}

/** isConfidential mirrors envelope.go's isConfidential: carries an enc marker
 * of ANY version. */
export function isConfidential(e: NostrEvent): boolean {
  return tagValue(e, TAG_ENC) !== "";
}

function base64DecodeStd(s: string): Uint8Array | null {
  try {
    const bin = atob(s);
    const out = new Uint8Array(bin.length);
    for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
    return out;
  } catch {
    return null;
  }
}

/** encWellFormed mirrors envelope.go's encWellFormed: a STRUCTURAL check that
 * cannot (and must not) verify the AEAD. */
function encWellFormed(e: NostrEvent): boolean {
  if (tagValue(e, TAG_ENC) !== ENC_VERSION) return false;
  const epochTag = tagValue(e, TAG_CEK_EPOCH);
  if (!/^-?\d+$/.test(epochTag)) return false;
  const raw = base64DecodeStd(e.content);
  if (raw === null) return false;
  return raw.length >= 12 + 16; // nonce(12) + Poly1305 tag(16)
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
 * event, ok=false on ANY failure (fail-closed). */
function cekFor(e: NostrEvent, dec: BoardDecryptor | null): Uint8Array | null {
  if (dec === null || tagValue(e, TAG_ENC) !== ENC_VERSION) return null;
  const epochTag = tagValue(e, TAG_CEK_EPOCH);
  if (!/^-?\d+$/.test(epochTag)) return null;
  return dec.cek(boardCoordOf(e), Number(epochTag));
}

/** openContent mirrors envelope.go's openContent: base64-decode, split
 * nonce||ciphertext, ChaCha20-Poly1305 open. Returns null on ANY error. */
function openContent(cek: Uint8Array, payload: string): Uint8Array | null {
  const raw = base64DecodeStd(payload);
  if (raw === null) return null;
  if (raw.length < 12 + 16) return null;
  const nonce = raw.slice(0, 12);
  const ct = raw.slice(12);
  return aeadOpen(cek, nonce, ct);
}

interface CardPayload {
  title: string;
  context?: string;
  waiting_on?: string;
  labels?: string[];
}

interface StatusPayload {
  reason: string;
}

/** decryptCardPayload mirrors envelope.go's decryptCardPayload. */
export function decryptCardPayload(e: NostrEvent, dec: BoardDecryptor | null): CardPayload | null {
  const cek = cekFor(e, dec);
  if (cek === null) return null;
  const raw = openContent(cek, e.content);
  if (raw === null) return null;
  try {
    return JSON.parse(new TextDecoder().decode(raw)) as CardPayload;
  } catch {
    return null;
  }
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
 * HMAC-SHA256(LTK, label). Exported for completeness with the spec table;
 * the fold itself never needs to COMPUTE a token — it only ever compares
 * (or, for a granted reader, replaces) already-tagged label values. */
export function labelToken(ltk: Uint8Array, label: string): string {
  return Array.from(hmacSha256(ltk, new TextEncoder().encode(label)))
    .map((b) => b.toString(16).padStart(2, "0"))
    .join("");
}

export const __internal = { encWellFormed, hexToBytes };
