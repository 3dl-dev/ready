// NIP-44 v2 REFERENCE IMPLEMENTATION — TEST SCAFFOLDING, NOT SHIPPED CODE.
//
// Nothing reachable from index.html -> main.ts imports this module, so it is
// never bundled into dist/. It exists for one reason: to stand in for a NIP-07
// browser extension in the test suite.
//
// WHY IT HAS TO EXIST. In production the page never does NIP-44 — it cannot,
// because NIP-44 needs ECDH and the secret key lives in the signer (see
// keyunwrap.ts). The page calls window.nostr.nip44.decrypt and gets a string
// back. That is the right architecture and it is also untestable end-to-end
// unless SOMETHING in the test process can play the extension's part over real
// Go-produced wraps. This is that something.
//
// WHY IT IS KAT-VALIDATED. A fake extension that is merely self-consistent
// would let a wrong production adapter pass: if this file and the adapter made
// the same mistake about, say, what a returned plaintext looks like, the suite
// would be green and the real extension would still fail. nip44ref.test.ts runs
// this implementation against the OFFICIAL NIP-44 v2 vectors — the very file
// pkg/nip44's Go tests use, read from pkg/nip44/testdata/nip44.vectors.json
// rather than copied, so the two implementations can never be validated against
// different values. Once this is pinned to the spec, "the adapter works against
// this" means "the adapter works against a spec-correct signer".
//
// Spec: https://github.com/nostr-protocol/nips/blob/master/44.md
// Structure mirrors pkg/nip44/nip44.go so the two read side by side.

import { chacha20 } from "@noble/ciphers/chacha.js";
import { secp256k1 } from "@noble/curves/secp256k1.js";
import { expand, extract } from "@noble/hashes/hkdf.js";
import { hmac } from "@noble/hashes/hmac.js";
import { sha256 } from "@noble/hashes/sha2.js";
import { decodeBase64Std } from "./envelope";
import { hexToBytes } from "./sha256";

const VERSION = 0x02;
const NONCE_SIZE = 32;
const MAC_SIZE = 32;
const MIN_PLAINTEXT = 1;
const MAX_PLAINTEXT = 65535;

const CONV_SALT = new TextEncoder().encode("nip44-v2");

/**
 * conversationKeyFromShared = HKDF-Extract(salt="nip44-v2", IKM=shared_x).
 * shared_x is the RAW 32-byte shared X coordinate — NOT sha256(X). Getting this
 * wrong breaks decryption silently, which is why the vectors derive through it.
 */
export function conversationKeyFromShared(sharedX: Uint8Array): Uint8Array {
  return extract(sha256, sharedX, CONV_SALT);
}

/** conversationKey performs the secp256k1 ECDH and returns the NIP-44 v2
 * conversation key. counterpartyXOnlyHex is a 32-byte x-only pubkey; NIP-44
 * lifts it to the even-Y point, which is the 02-prefixed compressed form. */
export function conversationKey(secretHex: string, counterpartyXOnlyHex: string): Uint8Array {
  const shared = secp256k1.getSharedSecret(hexToBytes(secretHex), hexToBytes("02" + counterpartyXOnlyHex));
  return conversationKeyFromShared(shared.subarray(1)); // drop the 0x02 prefix -> raw X
}

/** messageKeys expands the conversation key into (chacha_key, chacha_nonce,
 * hmac_key) via HKDF-Expand with info=nonce and L=76. */
export function messageKeys(convKey: Uint8Array, nonce: Uint8Array): {
  chachaKey: Uint8Array;
  chachaNonce: Uint8Array;
  hmacKey: Uint8Array;
} {
  if (nonce.length !== NONCE_SIZE) throw new Error(`nip44ref: nonce must be ${NONCE_SIZE} bytes`);
  const okm = expand(sha256, convKey, nonce, 76);
  return {
    chachaKey: okm.subarray(0, 32),
    chachaNonce: okm.subarray(32, 44),
    hmacKey: okm.subarray(44, 76),
  };
}

/** calcPaddedLen is the spec's padding curve, mirroring pkg/nip44's Go port. */
export function calcPaddedLen(unpadded: number): number {
  if (unpadded <= 32) return 32;
  // nextPower = 1 << (floor(log2(unpadded-1)) + 1)
  const nextPower = 1 << (32 - Math.clz32(unpadded - 1));
  const chunk = nextPower > 256 ? nextPower / 8 : 32;
  return chunk * (Math.floor((unpadded - 1) / chunk) + 1);
}

function pad(plaintext: Uint8Array): Uint8Array {
  const n = plaintext.length;
  if (n < MIN_PLAINTEXT || n > MAX_PLAINTEXT) throw new Error(`nip44ref: invalid plaintext length ${n}`);
  const out = new Uint8Array(2 + calcPaddedLen(n));
  out[0] = (n >> 8) & 0xff;
  out[1] = n & 0xff;
  out.set(plaintext, 2);
  return out;
}

function unpad(padded: Uint8Array): Uint8Array {
  if (padded.length < 2) throw new Error("nip44ref: invalid padding (too short)");
  const n = (padded[0] << 8) | padded[1];
  if (n < MIN_PLAINTEXT || n > MAX_PLAINTEXT) throw new Error("nip44ref: invalid padding (declared length out of range)");
  if (padded.length !== 2 + calcPaddedLen(n)) throw new Error("nip44ref: invalid padding (total length mismatch)");
  return padded.subarray(2, 2 + n);
}

function hmacAAD(key: Uint8Array, message: Uint8Array, aad: Uint8Array): Uint8Array {
  const h = hmac.create(sha256, key);
  h.update(aad);
  h.update(message);
  return h.digest();
}

function equalCT(a: Uint8Array, b: Uint8Array): boolean {
  if (a.length !== b.length) return false;
  let diff = 0;
  for (let i = 0; i < a.length; i++) diff |= a[i] ^ b[i];
  return diff === 0;
}

/** encryptWithNonce is the deterministic seam the vectors drive. Production
 * code never calls it (this whole module is test scaffolding); it exists so the
 * KAT can pin byte-exact conformance with the vector's fixed nonce. */
export function encryptWithNonce(convKey: Uint8Array, nonce: Uint8Array, plaintext: Uint8Array): string {
  const { chachaKey, chachaNonce, hmacKey } = messageKeys(convKey, nonce);
  const ct = chacha20(chachaKey, chachaNonce, pad(plaintext));
  const mac = hmacAAD(hmacKey, ct, nonce);
  const payload = new Uint8Array(1 + nonce.length + ct.length + mac.length);
  payload[0] = VERSION;
  payload.set(nonce, 1);
  payload.set(ct, 1 + nonce.length);
  payload.set(mac, 1 + nonce.length + ct.length);
  return btoa(String.fromCharCode(...payload));
}

/**
 * decryptWithConversationKey verifies the HMAC in constant time BEFORE
 * decrypting, then strips the padding. It THROWS on every reject class the
 * official vectors define — unknown version, bad base64, bad MAC, invalid
 * padding — because encrypt-then-MAC exists precisely so those are refusals
 * rather than garbage plaintext.
 */
export function decryptWithConversationKey(convKey: Uint8Array, payload: string): Uint8Array {
  if (payload.length === 0) throw new Error("nip44ref: empty payload");
  if (payload[0] === "#") throw new Error("nip44ref: unknown version marker");
  const raw = decodeBase64Std(payload);
  if (raw === null) throw new Error("nip44ref: invalid base64");
  if (raw.length < 1 + NONCE_SIZE + MAC_SIZE) throw new Error("nip44ref: payload too short");
  if (raw[0] !== VERSION) throw new Error(`nip44ref: unknown version ${raw[0]}`);

  const nonce = raw.subarray(1, 1 + NONCE_SIZE);
  const ct = raw.subarray(1 + NONCE_SIZE, raw.length - MAC_SIZE);
  const mac = raw.subarray(raw.length - MAC_SIZE);

  const { chachaKey, chachaNonce, hmacKey } = messageKeys(convKey, nonce);
  if (!equalCT(hmacAAD(hmacKey, ct, nonce), mac)) throw new Error("nip44ref: invalid MAC");
  return unpad(chacha20(chachaKey, chachaNonce, ct));
}

/** seal / open are the two operations pkg/nip44 exposes, over hex key material.
 * `open` returns the plaintext BYTES; a NIP-07 extension would hand the page a
 * UTF-8 string of these, which is what fakeSigner (in the tests) does. */
export function seal(secretHex: string, counterpartyXOnlyHex: string, plaintext: Uint8Array): string {
  const nonce = new Uint8Array(NONCE_SIZE);
  crypto.getRandomValues(nonce);
  return encryptWithNonce(conversationKey(secretHex, counterpartyXOnlyHex), nonce, plaintext);
}

export function open(secretHex: string, counterpartyXOnlyHex: string, payload: string): Uint8Array {
  return decryptWithConversationKey(conversationKey(secretHex, counterpartyXOnlyHex), payload);
}
