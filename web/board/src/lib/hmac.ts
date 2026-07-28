// HMAC-SHA256 (FIPS 198-1), built on sha256.ts. Needed by nip44.ts for
// conversation-key derivation (HKDF-Extract, which for SHA-256 is exactly
// HMAC-SHA256(salt, ikm)) and per-message key expansion (HKDF-Expand).

import { sha256 } from "./sha256";

const BLOCK_SIZE = 64; // SHA-256 block size in bytes

function xorPad(key: Uint8Array, pad: number): Uint8Array {
  const out = new Uint8Array(BLOCK_SIZE);
  for (let i = 0; i < BLOCK_SIZE; i++) {
    out[i] = (i < key.length ? key[i] : 0) ^ pad;
  }
  return out;
}

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

/** hmacSha256 computes HMAC-SHA256(key, msg), 32 bytes. */
export function hmacSha256(key: Uint8Array, msg: Uint8Array): Uint8Array {
  const k = key.length > BLOCK_SIZE ? sha256(key) : key;
  const ipad = xorPad(k, 0x36);
  const opad = xorPad(k, 0x5c);
  const inner = sha256(concatBytes(ipad, msg));
  return sha256(concatBytes(opad, inner));
}
