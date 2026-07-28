// RFC 8439 ChaCha20-Poly1305 AEAD, built from chacha20.ts + poly1305.ts. This
// is the TS port of golang.org/x/crypto/chacha20poly1305, which
// pkg/sync/envelope.go's sealContent/openContent use for a confidential
// board's card/status Content (board-fold-spec.md §11.9):
//
//	Content = base64Std( nonce(12) || ChaCha20-Poly1305(CEK, nonce, plaintext) )
//
// Only `open` is needed by the fold (a read-only client never seals content),
// but both are implemented and tested — `seal` is what the parity-check
// script (scripts/live-parity.mjs) round-trips against to build confidence
// independent of the RFC vector alone.
//
// Correctness is checked in chacha20poly1305.test.ts against the RFC 8439
// §2.8.2 known-answer AEAD vector — this is CEK/content decryption, one of
// the fold's non-negotiable security surfaces (fail-closed on any tamper).

import { chacha20Block, chacha20Xor } from "./chacha20";
import { poly1305Mac } from "./poly1305";

export const NONCE_SIZE = 12;
export const TAG_SIZE = 16; // "Overhead" in the Go API

function constantTimeEqual(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
  return diff === 0;
}

function u64le(n: number): Uint8Array {
  const out = new Uint8Array(8);
  const view = new DataView(out.buffer);
  // All lengths this envelope ever handles fit in 32 bits; write the high
  // word as zero rather than pretending to support a 64-bit length.
  view.setUint32(0, n >>> 0, true);
  view.setUint32(4, 0, true);
  return out;
}

/** aeadMac computes the RFC 8439 §2.8 Poly1305 tag over (empty AAD,
 * ciphertext) — this envelope never carries additional authenticated data. */
function aeadMac(polyKey: Uint8Array, ciphertext: Uint8Array): Uint8Array {
  // No AAD, so its length-prefixed block is empty; only ciphertext (+ its
  // padding) and the two 8-byte length fields are MACed.
  const ctPad = (16 - (ciphertext.length % 16)) % 16;
  const macData = new Uint8Array(ciphertext.length + ctPad + 8 + 8);
  let off = 0;
  macData.set(ciphertext, off);
  off += ciphertext.length + ctPad;
  macData.set(u64le(0), off); // aad length = 0
  off += 8;
  macData.set(u64le(ciphertext.length), off); // ciphertext length
  return poly1305Mac(macData, polyKey);
}

/** seal encrypts plaintext under (key, nonce) and returns ciphertext||tag. */
export function seal(key: Uint8Array, nonce: Uint8Array, plaintext: Uint8Array): Uint8Array {
  if (key.length !== 32) throw new Error(`chacha20poly1305.seal: key must be 32 bytes, got ${key.length}`);
  if (nonce.length !== NONCE_SIZE) {
    throw new Error(`chacha20poly1305.seal: nonce must be ${NONCE_SIZE} bytes, got ${nonce.length}`);
  }
  const polyKey = chacha20Block(key, 0, nonce).slice(0, 32);
  const ciphertext = chacha20Xor(key, 1, nonce, plaintext);
  const tag = aeadMac(polyKey, ciphertext);
  const out = new Uint8Array(ciphertext.length + TAG_SIZE);
  out.set(ciphertext, 0);
  out.set(tag, ciphertext.length);
  return out;
}

/** open decrypts ciphertext||tag under (key, nonce). Returns null (never
 * throws) on a tampered tag or malformed input — the fold's confidential
 * decrypt path fail-closes to a placeholder on ANY error here (§11.7-11.8),
 * so this deliberately reports failure as a value, not an exception. */
export function open(key: Uint8Array, nonce: Uint8Array, ciphertextAndTag: Uint8Array): Uint8Array | null {
  if (key.length !== 32) return null;
  if (nonce.length !== NONCE_SIZE) return null;
  if (ciphertextAndTag.length < TAG_SIZE) return null;
  const ciphertext = ciphertextAndTag.slice(0, ciphertextAndTag.length - TAG_SIZE);
  const tag = ciphertextAndTag.slice(ciphertextAndTag.length - TAG_SIZE);

  const polyKey = chacha20Block(key, 0, nonce).slice(0, 32);
  const wantTag = aeadMac(polyKey, ciphertext);
  if (!constantTimeEqual(tag, wantTag)) return null; // verify BEFORE decrypting

  return chacha20Xor(key, 1, nonce, ciphertext);
}
