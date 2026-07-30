// schnorrsign.ts — BIP-340 SIGNING. TEST SCAFFOLDING AND OFF-PAGE TOOLING ONLY.
//
// NOTHING REACHABLE FROM index.html -> main.ts IMPORTS THIS, and nothing ever
// may. The board's security model is that the user's secret key never enters the
// page: signing goes through window.nostr.signEvent (NIP-07), supplied by a
// browser extension that holds the key. A signing primitive inside the bundle
// would be the first half of a "paste your nsec" fallback, and there is no such
// fallback — see main.ts and lib/nip07.ts. dist_test.go's bundle scan plus
// nostorage.test.ts keep that honest; this file's own test asserts the shipped
// entrypoint does not reach it.
//
// WHY IT EXISTS AT ALL (ready-bff's ruling, 2026-07-30): the write-path proof
// needs a REAL signer driving a REAL browser against the LIVE relay. A bare
// headless Chromium exposes no window.nostr, and provisioning an unlocked
// extension unattended is not available today. The ruled substitute is to inject
// a GENUINE secp256k1 schnorr signer as window.nostr over CDP — genuinely
// signed, genuinely published, genuinely read back by an independent rd. This is
// that signer. It is NOT a stub: it computes a real BIP-340 signature that the
// relay (strfry) and rd both verify before accepting. What it does not exercise
// is the extension handshake, which ready-35a carries as a human release gate.
//
// The verification half of BIP-340 lives in secp256k1.ts and is validated
// against the official bitcoin/bips test vectors. This file reuses that module's
// curve arithmetic and tagged hash, so a signature produced here is checked by
// the same code path that checks a relay's events.

import { sha256 } from "./sha256";
import { computeEventId, type NostrEvent } from "./nostrevent";
import {
  N,
  __internal,
  bigIntTo32Bytes,
  bytesToBigInt,
  scalarMultiply,
  toAffine,
  type JacobianPoint,
} from "./secp256k1";

const { taggedHash } = __internal;

const Gx = 0x79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798n;
const Gy = 0x483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8n;
const G: JacobianPoint = { x: Gx, y: Gy, z: 1n };

function modN(a: bigint): bigint {
  const r = a % N;
  return r >= 0n ? r : r + N;
}

export function hexToBytes(hex: string): Uint8Array {
  if (hex.length % 2 !== 0) throw new Error("schnorrsign: odd-length hex");
  const out = new Uint8Array(hex.length / 2);
  for (let i = 0; i < out.length; i++) out[i] = parseInt(hex.slice(i * 2, i * 2 + 2), 16);
  return out;
}

export function bytesToHex(b: Uint8Array): string {
  let s = "";
  for (const x of b) s += x.toString(16).padStart(2, "0");
  return s;
}

/** xOnlyPubkey returns the 32-byte x-only public key for a 32-byte secret. */
export function xOnlyPubkey(secretHex: string): string {
  const d0 = bytesToBigInt(hexToBytes(secretHex));
  if (d0 === 0n || d0 >= N) throw new Error("schnorrsign: secret key out of range");
  const P = toAffine(scalarMultiply(d0, G));
  if (!P) throw new Error("schnorrsign: secret key maps to infinity");
  return bytesToHex(bigIntTo32Bytes(P.x));
}

/**
 * schnorrSign implements BIP-340 "Default Signing" verbatim:
 *   d = d0 or n-d0 so that P has even y
 *   t = d XOR tagged_hash("BIP0340/aux", a)
 *   rand = tagged_hash("BIP0340/nonce", t || bytes(P.x) || m)
 *   k = rand mod n (must be non-zero), R = kG, k = n-k if R.y is odd
 *   e = tagged_hash("BIP0340/challenge", bytes(R.x) || bytes(P.x) || m) mod n
 *   sig = bytes(R.x) || bytes((k + e*d) mod n)
 * auxRand defaults to 32 zero bytes, which the BIP explicitly permits and which
 * makes signing deterministic — the property the round-trip tests rely on.
 */
export function schnorrSign(msg: Uint8Array, secretHex: string, auxRand?: Uint8Array): Uint8Array {
  const d0 = bytesToBigInt(hexToBytes(secretHex));
  if (d0 === 0n || d0 >= N) throw new Error("schnorrsign: secret key out of range");
  const Paff = toAffine(scalarMultiply(d0, G));
  if (!Paff) throw new Error("schnorrsign: secret key maps to infinity");
  const d = Paff.y % 2n === 0n ? d0 : N - d0;
  const px = bigIntTo32Bytes(Paff.x);

  const aux = auxRand ?? new Uint8Array(32);
  const t = bigIntTo32Bytes(d);
  const auxHash = taggedHash("BIP0340/aux", aux);
  for (let i = 0; i < 32; i++) t[i] ^= auxHash[i];

  const rand = taggedHash("BIP0340/nonce", t, px, msg);
  const k0 = modN(bytesToBigInt(rand));
  if (k0 === 0n) throw new Error("schnorrsign: nonce is zero (retry with different aux)");
  const Raff = toAffine(scalarMultiply(k0, G));
  if (!Raff) throw new Error("schnorrsign: nonce maps to infinity");
  const k = Raff.y % 2n === 0n ? k0 : N - k0;
  const rx = bigIntTo32Bytes(Raff.x);

  const e = modN(bytesToBigInt(taggedHash("BIP0340/challenge", rx, px, msg)));
  const s = modN(k + e * d);

  const sig = new Uint8Array(64);
  sig.set(rx, 0);
  sig.set(bigIntTo32Bytes(s), 32);
  return sig;
}

/** signNostrEvent stamps the pubkey, derives the NIP-01 event id, and signs it.
 * Returns a fully-formed, verifiable NostrEvent. */
export function signNostrEvent(
  e: Pick<NostrEvent, "created_at" | "kind" | "tags" | "content">,
  secretHex: string,
): NostrEvent {
  const pubkey = xOnlyPubkey(secretHex);
  const unsigned = {
    pubkey,
    created_at: e.created_at,
    kind: e.kind,
    tags: e.tags,
    content: e.content,
  };
  const id = computeEventId(unsigned);
  const sig = bytesToHex(schnorrSign(hexToBytes(id), secretHex));
  return { ...unsigned, id, sig };
}

/** randomSecretHex draws a fresh 32-byte secret from the platform CSPRNG. Used
 * by the live round-trip harness to mint a throwaway board identity — never by
 * anything the browser bundle can reach. */
export function randomSecretHex(): string {
  const b = new Uint8Array(32);
  crypto.getRandomValues(b);
  b[0] &= 0x7f; // keep it comfortably below n without a rejection loop
  if (b.every((x) => x === 0)) b[31] = 1;
  return bytesToHex(b);
}

/** sha256Hex is exported for the harness's convenience (item ids, board ds). */
export function sha256Hex(s: string): string {
  return bytesToHex(sha256(new TextEncoder().encode(s)));
}
