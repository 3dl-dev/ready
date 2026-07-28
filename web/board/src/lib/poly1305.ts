// Pure, dependency-free Poly1305 MAC (RFC 8439 §2.5), implemented with native
// BigInt for the mod-2^130-5 field arithmetic. Same no-third-party-bundle
// rationale as sha256.ts / chacha20.ts. This is the authenticator half of
// ChaCha20-Poly1305 AEAD (chacha20poly1305.ts) — the confidential-board
// content decrypt path (board-fold-spec.md §11.9). Correctness is checked in
// poly1305.test.ts against the RFC 8439 §2.5.2 known-answer vector.

const P1305 = (1n << 130n) - 5n;

function leBytesToBigInt(b: Uint8Array): bigint {
  let v = 0n;
  for (let i = b.length - 1; i >= 0; i--) {
    v = (v << 8n) | BigInt(b[i]);
  }
  return v;
}

function bigIntToLeBytes(v: bigint, len: number): Uint8Array {
  const out = new Uint8Array(len);
  let x = v;
  for (let i = 0; i < len; i++) {
    out[i] = Number(x & 0xffn);
    x >>= 8n;
  }
  return out;
}

/** poly1305Mac computes the 16-byte Poly1305 tag of `msg` under the 32-byte
 * one-time key (r(16) || s(16)), RFC 8439 §2.5.1. */
export function poly1305Mac(msg: Uint8Array, key: Uint8Array): Uint8Array {
  if (key.length !== 32) throw new Error(`poly1305Mac: key must be 32 bytes, got ${key.length}`);

  // Clamp r per RFC 8439 §2.5.1: r &= 0x0ffffffc0ffffffc0ffffffc0fffffff (LE).
  const rBytes = key.slice(0, 16).slice();
  rBytes[3] &= 15;
  rBytes[7] &= 15;
  rBytes[11] &= 15;
  rBytes[15] &= 15;
  rBytes[4] &= 252;
  rBytes[8] &= 252;
  rBytes[12] &= 252;
  const r = leBytesToBigInt(rBytes);
  const s = leBytesToBigInt(key.slice(16, 32));

  let acc = 0n;
  for (let off = 0; off < msg.length; off += 16) {
    const chunk = msg.slice(off, off + 16);
    const padded = new Uint8Array(chunk.length + 1);
    padded.set(chunk);
    padded[chunk.length] = 1; // append the 0x01 byte per block (RFC 8439 §2.5.1)
    const n = leBytesToBigInt(padded);
    acc = ((acc + n) * r) % P1305;
  }
  acc = (acc + s) % (1n << 128n);
  return bigIntToLeBytes(acc, 16);
}
