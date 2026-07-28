// NIP-44 v2 authenticated encryption (secp256k1 ECDH -> HKDF-SHA256 ->
// ChaCha20 + HMAC-SHA256, encrypt-then-MAC), TS port of the same algorithm
// rd's pkg/nip44 implements (pkg/nip44/nip44.go, itself vendored from
// dontguess). This is what a confidential board's owner-signed 39301 role
// grant uses to wrap the per-board CEK/LTK to a member (keydist.go
// WrapKey/unwrapKey, board-fold-spec.md §11.10-§11.12).
//
// SCOPE — this module is DELIBERATELY NOT part of the shipped board bundle's
// import graph (nothing reachable from index.html -> main.ts imports it, same
// pattern as boardevents.fixtures.ts). It takes a raw secp256k1 secret-key
// scalar as input, which is exactly the "no secret-key handling in the
// browser" boundary secp256k1.ts's header documents for the shipped verify
// path. It exists for scripts/live-parity.mjs (ready-35b's live-parity
// proof), which needs to derive a CEK from a role grant to decrypt a real
// confidential board's card content and compare against `rd list --json`.
// Production in-browser decrypt would instead delegate to a NIP-07
// extension's own nip44.decrypt (which holds the key), never to this module.
//
// Correctness is checked in nip44.test.ts against a SUBSET of the official
// NIP-44 v2 known-answer vectors (paulmillr/nip44), copied from rd's own
// pkg/nip44/testdata/nip44.vectors.json — the same file pkg/nip44/nip44_test.go
// validates the Go implementation against.

import { hmacSha256 } from "./hmac";
import { hkdfExtract, hkdfExpand } from "./hkdf";
import { chacha20Xor } from "./chacha20";
import { liftX, scalarMultiply, toAffine, bytesToBigInt, bigIntTo32Bytes } from "./secp256k1";
import { hexToBytes, bytesToHex } from "./sha256";

const VERSION = 0x02;
const MIN_PLAINTEXT_SIZE = 1;
const MAX_PLAINTEXT_SIZE = 65535;
const NONCE_SIZE = 32;
const MAC_SIZE = 32;
const CONV_SALT = new TextEncoder().encode("nip44-v2");

function concatBytes(...parts: Uint8Array[]): Uint8Array {
  const total = parts.reduce((n, p) => n + p.length, 0);
  const out = new Uint8Array(total);
  let offset = 0;
  for (const p of parts) {
    out.set(p, offset);
    offset += p.length;
  }
  return out;
}

/** ecdhRawX performs secp256k1 ECDH between a raw secret-key scalar and a
 * counterparty's x-only pubkey, returning the RAW 32-byte big-endian shared X
 * coordinate — NOT sha256(X) (pkg/nostr/ecdh.go's ECDH, the value NIP-44 v2
 * consumes). counterpartyXOnlyHex is lifted to its even-Y point (BIP-340)
 * before the scalar multiply, matching liftXOnlyToEvenY on the Go side. */
export function ecdhRawX(secretHex: string, counterpartyXOnlyHex: string): Uint8Array {
  const secret = bytesToBigInt(hexToBytes(secretHex));
  const pub = liftX(hexToBytes(counterpartyXOnlyHex));
  if (!pub) throw new Error("nip44: ecdh: counterparty pubkey does not lift to a valid curve point");
  const product = scalarMultiply(secret, { x: pub.x, y: pub.y, z: 1n });
  const affine = toAffine(product);
  if (!affine) throw new Error("nip44: ecdh: shared point is the point at infinity");
  return bigIntTo32Bytes(affine.x);
}

/** conversationKeyFromShared = HKDF-Extract(salt="nip44-v2", ikm=sharedX). */
function conversationKeyFromShared(sharedX: Uint8Array): Uint8Array {
  return hkdfExtract(CONV_SALT, sharedX);
}

function conversationKey(secretHex: string, counterpartyXOnlyHex: string): Uint8Array {
  return conversationKeyFromShared(ecdhRawX(secretHex, counterpartyXOnlyHex));
}

interface MessageKeys {
  chachaKey: Uint8Array;
  chachaNonce: Uint8Array;
  hmacKey: Uint8Array;
}

/** messageKeys expands the conversation key into the per-message
 * (chacha_key[32], chacha_nonce[12], hmac_key[32]) triple via
 * HKDF-Expand(info=nonce, L=76). */
function messageKeys(convKey: Uint8Array, nonce: Uint8Array): MessageKeys {
  if (nonce.length !== NONCE_SIZE) throw new Error(`nip44: nonce must be ${NONCE_SIZE} bytes`);
  const okm = hkdfExpand(convKey, nonce, 76);
  return {
    chachaKey: okm.slice(0, 32),
    chachaNonce: okm.slice(32, 44),
    hmacKey: okm.slice(44, 76),
  };
}

/** calcPaddedLen returns the NIP-44 v2 power-of-two padded length for an
 * unpadded plaintext of `unpadded` bytes (spec-ported verbatim). */
function calcPaddedLen(unpadded: number): number {
  if (unpadded <= 32) return 32;
  const nextPower = 1 << (32 - Math.clz32(unpadded - 1));
  const chunk = nextPower > 256 ? nextPower / 8 : 32;
  return chunk * (Math.floor((unpadded - 1) / chunk) + 1);
}

function pad(plaintext: Uint8Array): Uint8Array {
  const n = plaintext.length;
  if (n < MIN_PLAINTEXT_SIZE || n > MAX_PLAINTEXT_SIZE) {
    throw new Error(`nip44: invalid plaintext length ${n}`);
  }
  const padded = new Uint8Array(2 + calcPaddedLen(n));
  new DataView(padded.buffer).setUint16(0, n, false);
  padded.set(plaintext, 2);
  return padded;
}

function unpad(padded: Uint8Array): Uint8Array | null {
  if (padded.length < 2) return null;
  const unpaddedLen = new DataView(padded.buffer, padded.byteOffset, padded.byteLength).getUint16(0, false);
  if (unpaddedLen < MIN_PLAINTEXT_SIZE || unpaddedLen > MAX_PLAINTEXT_SIZE) return null;
  if (padded.length !== 2 + calcPaddedLen(unpaddedLen)) return null;
  return padded.slice(2, 2 + unpaddedLen);
}

function hmacAAD(key: Uint8Array, message: Uint8Array, aad: Uint8Array): Uint8Array {
  return hmacSha256(key, concatBytes(aad, message));
}

function base64Encode(b: Uint8Array): string {
  let bin = "";
  for (let i = 0; i < b.length; i++) bin += String.fromCharCode(b[i]);
  return btoa(bin);
}

function base64Decode(s: string): Uint8Array {
  const bin = atob(s);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}

function constantTimeEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
  return diff === 0;
}

/** encryptWithNonce is the byte-exact NIP-44 v2 encrypt core (test seam —
 * public `seal` draws a real random nonce instead). */
export function encryptWithNonce(convKey: Uint8Array, nonce: Uint8Array, plaintext: Uint8Array): string {
  if (nonce.length !== NONCE_SIZE) throw new Error(`nip44: nonce must be ${NONCE_SIZE} bytes`);
  const { chachaKey, chachaNonce, hmacKey } = messageKeys(convKey, nonce);
  const padded = pad(plaintext);
  const ciphertext = chacha20Xor(chachaKey, 0, chachaNonce, padded);
  const mac = hmacAAD(hmacKey, ciphertext, nonce);
  return base64Encode(concatBytes(Uint8Array.of(VERSION), nonce, ciphertext, mac));
}

/** decryptWithConversationKey is the byte-exact NIP-44 v2 decrypt core.
 * Returns null (never throws) on ANY malformed input or MAC failure —
 * verifies the HMAC BEFORE decrypting, per the encrypt-then-MAC design. */
export function decryptWithConversationKey(convKey: Uint8Array, payload: string): Uint8Array | null {
  const plen = payload.length;
  if (plen < 132 || plen > 87472) return null;
  if (payload[0] === "#") return null;
  let data: Uint8Array;
  try {
    data = base64Decode(payload);
  } catch {
    return null;
  }
  const dlen = data.length;
  if (dlen < 99 || dlen > 65603) return null;
  if (data[0] !== VERSION) return null;
  const nonce = data.slice(1, 1 + NONCE_SIZE);
  const ciphertext = data.slice(1 + NONCE_SIZE, dlen - MAC_SIZE);
  const mac = data.slice(dlen - MAC_SIZE);

  const { chachaKey, chachaNonce, hmacKey } = messageKeys(convKey, nonce);
  const calcMac = hmacAAD(hmacKey, ciphertext, nonce);
  if (!constantTimeEqual(calcMac, mac)) return null;

  const padded = chacha20Xor(chachaKey, 0, chachaNonce, ciphertext);
  return unpad(padded);
}

/** seal encrypts plaintext to counterpartyXOnlyHex using our secret key, with
 * a fresh crypto-random nonce. */
export function seal(secretHex: string, counterpartyXOnlyHex: string, plaintext: Uint8Array): string {
  const convKey = conversationKey(secretHex, counterpartyXOnlyHex);
  const nonce = new Uint8Array(NONCE_SIZE);
  crypto.getRandomValues(nonce);
  return encryptWithNonce(convKey, nonce, plaintext);
}

/** open decrypts a NIP-44 v2 payload addressed to us FROM counterparty (i.e.
 * counterparty sealed it to our pubkey), using our secret key. Returns null
 * (never throws) on any malformed/tampered payload — see keydist.go's
 * unwrapKey, whose sole caller (DeriveBoardKeyring) treats a failed unwrap as
 * "no key for this epoch", not an error. */
export function open(secretHex: string, counterpartyXOnlyHex: string, payload: string): Uint8Array | null {
  const convKey = conversationKey(secretHex, counterpartyXOnlyHex);
  return decryptWithConversationKey(convKey, payload);
}

export const __internal = { calcPaddedLen, conversationKeyFromShared, messageKeys, bytesToHex };
