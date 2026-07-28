// HKDF (RFC 5869) Extract/Expand over HMAC-SHA256, built on hmac.ts. Used
// only by nip44.ts: conversation_key = HKDF-Extract(salt="nip44-v2",
// ikm=shared_x), and per-message keys = HKDF-Expand(conversation_key,
// info=nonce, L=76).

import { hmacSha256 } from "./hmac";

/** hkdfExtract(salt, ikm) = HMAC-SHA256(salt, ikm) (RFC 5869 §2.2). */
export function hkdfExtract(salt: Uint8Array, ikm: Uint8Array): Uint8Array {
  return hmacSha256(salt, ikm);
}

/** hkdfExpand(prk, info, length) per RFC 5869 §2.3. length must be
 * <= 255 * 32 (this module never needs more than 76 bytes). */
export function hkdfExpand(prk: Uint8Array, info: Uint8Array, length: number): Uint8Array {
  const hashLen = 32;
  const n = Math.ceil(length / hashLen);
  if (n > 255) throw new Error(`hkdfExpand: requested length ${length} exceeds 255*${hashLen}`);
  const out = new Uint8Array(n * hashLen);
  let prev = new Uint8Array(0);
  for (let i = 1; i <= n; i++) {
    const input = new Uint8Array(prev.length + info.length + 1);
    input.set(prev, 0);
    input.set(info, prev.length);
    input[prev.length + info.length] = i;
    prev = hmacSha256(prk, input);
    out.set(prev, (i - 1) * hashLen);
  }
  return out.slice(0, length);
}
