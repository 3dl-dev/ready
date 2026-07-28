// Pure, dependency-free ChaCha20 (RFC 8439 §2.3-2.4), the IETF variant: 32-bit
// block counter + 96-bit nonce. Hand-written from the spec for the same
// no-third-party-bundle reason as sha256.ts (dist_test.go's bare-"//" scan) —
// see that file's header. This module is the shared stream-cipher core for:
//
//   - chacha20poly1305.ts (RFC 8439 AEAD) — decrypts a confidential card/
//     status event's Content under the per-board CEK (board-fold-spec.md
//     §11.9; Go side golang.org/x/crypto/chacha20poly1305).
//   - nip44.ts — the UNAUTHENTICATED ChaCha20 stream inside the NIP-44 v2
//     envelope that wraps a CEK/LTK into a role grant (Go side
//     golang.org/x/crypto/chacha20.NewUnauthenticatedCipher).
//
// Correctness is checked in chacha20.test.ts against the RFC 8439 §2.3.2 /
// §2.4.2 known-answer test vectors, not hand-picked examples.

const SIGMA = [0x61707865, 0x3320646e, 0x79622d32, 0x6b206574]; // "expand 32-byte k"

function rotl(x: number, n: number): number {
  return ((x << n) | (x >>> (32 - n))) >>> 0;
}

function quarterRound(s: Uint32Array, a: number, b: number, c: number, d: number): void {
  s[a] = (s[a] + s[b]) >>> 0;
  s[d] = rotl(s[d] ^ s[a], 16);
  s[c] = (s[c] + s[d]) >>> 0;
  s[b] = rotl(s[b] ^ s[c], 12);
  s[a] = (s[a] + s[b]) >>> 0;
  s[d] = rotl(s[d] ^ s[a], 8);
  s[c] = (s[c] + s[d]) >>> 0;
  s[b] = rotl(s[b] ^ s[c], 7);
}

/** chacha20Block computes one 64-byte keystream block for the given 32-byte
 * key, 32-bit little-endian counter, and 12-byte nonce (RFC 8439 §2.3). */
export function chacha20Block(key: Uint8Array, counter: number, nonce: Uint8Array): Uint8Array {
  if (key.length !== 32) throw new Error(`chacha20Block: key must be 32 bytes, got ${key.length}`);
  if (nonce.length !== 12) throw new Error(`chacha20Block: nonce must be 12 bytes, got ${nonce.length}`);

  const keyView = new DataView(key.buffer, key.byteOffset, key.byteLength);
  const nonceView = new DataView(nonce.buffer, nonce.byteOffset, nonce.byteLength);

  const init = new Uint32Array(16);
  init.set(SIGMA, 0);
  for (let i = 0; i < 8; i++) init[4 + i] = keyView.getUint32(i * 4, true);
  init[12] = counter >>> 0;
  for (let i = 0; i < 3; i++) init[13 + i] = nonceView.getUint32(i * 4, true);

  const s = init.slice();
  for (let round = 0; round < 10; round++) {
    // Column rounds.
    quarterRound(s, 0, 4, 8, 12);
    quarterRound(s, 1, 5, 9, 13);
    quarterRound(s, 2, 6, 10, 14);
    quarterRound(s, 3, 7, 11, 15);
    // Diagonal rounds.
    quarterRound(s, 0, 5, 10, 15);
    quarterRound(s, 1, 6, 11, 12);
    quarterRound(s, 2, 7, 8, 13);
    quarterRound(s, 3, 4, 9, 14);
  }

  const out = new Uint8Array(64);
  const outView = new DataView(out.buffer);
  for (let i = 0; i < 16; i++) {
    outView.setUint32(i * 4, (s[i] + init[i]) >>> 0, true);
  }
  return out;
}

/** chacha20Xor encrypts (or decrypts — the stream cipher is its own inverse)
 * `data` under (key, nonce), keystream blocks starting at `counter`. */
export function chacha20Xor(key: Uint8Array, counter: number, nonce: Uint8Array, data: Uint8Array): Uint8Array {
  const out = new Uint8Array(data.length);
  let block = new Uint8Array(0);
  for (let i = 0; i < data.length; i++) {
    const blockIndex = i % 64;
    if (blockIndex === 0) {
      block = chacha20Block(key, counter + Math.floor(i / 64), nonce);
    }
    out[i] = data[i] ^ block[blockIndex];
  }
  return out;
}
