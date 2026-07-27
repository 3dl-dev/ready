// Minimal, verify-only BIP-340 (Schnorr over secp256k1) implementation, hand
// written from the spec (bitcoin/bips bip-0340) using native BigInt — no
// third-party crypto dependency. Same rationale as sha256.ts: dist_test.go
// scans every shipped .js for a bare "//" substring, and a vendored crypto
// library's minified license banner ("// https://...") would trip that
// same-origin guard. This module implements ONLY signature verification
// (liftX + point add/double + scalar multiplication + the BIP-340 challenge)
// — no signing, no secret-key handling — because that is all the board app
// ever needs: pkg/sync/boarddiscovery.go's security property ("a relay is
// untrusted; verify every candidate event before it mints a coordinate")
// ported to the browser in boarddiscovery.ts.
//
// Correctness is checked in secp256k1.test.ts against the OFFICIAL BIP-340
// test vectors (bitcoin/bips/bip-0340/test-vectors.csv), not hand-picked
// examples.

import { sha256 } from "./sha256";

const P = 0xfffffffffffffffffffffffffffffffffffffffffffffffffffffffefffffc2fn;
const N = 0xfffffffffffffffffffffffffffffffebaaedce6af48a03bbfd25e8cd0364141n;
const Gx = 0x79be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798n;
const Gy = 0x483ada7726a3c4655da4fbfc0e1108a8fd17b448a68554199c47d08ffb10d4b8n;

interface JacobianPoint {
  x: bigint;
  y: bigint;
  z: bigint;
}

const INFINITY: JacobianPoint = { x: 0n, y: 1n, z: 0n };
const G: JacobianPoint = { x: Gx, y: Gy, z: 1n };

function mod(a: bigint, m: bigint = P): bigint {
  const r = a % m;
  return r >= 0n ? r : r + m;
}

// Modular inverse via Fermat's little theorem (P is prime): a^(p-2) mod p.
function modPow(base: bigint, exp: bigint, m: bigint): bigint {
  let result = 1n;
  let b = mod(base, m);
  let e = exp;
  while (e > 0n) {
    if (e & 1n) result = mod(result * b, m);
    b = mod(b * b, m);
    e >>= 1n;
  }
  return result;
}

function modInverse(a: bigint, m: bigint = P): bigint {
  return modPow(a, m - 2n, m);
}

function isInfinity(p: JacobianPoint): boolean {
  return p.z === 0n;
}

// Jacobian point doubling on y^2 = x^3 + 7 (secp256k1's a=0, b=7).
function jacobianDouble(p: JacobianPoint): JacobianPoint {
  if (isInfinity(p) || p.y === 0n) return INFINITY;
  const { x, y, z } = p;
  const a = mod(x * x);
  const b = mod(y * y);
  const c = mod(b * b);
  const d = mod(2n * (mod((x + b) * (x + b)) - a - c));
  const e = mod(3n * a);
  const f = mod(e * e);
  const x3 = mod(f - 2n * d);
  const y3 = mod(e * (d - x3) - 8n * c);
  const z3 = mod(2n * y * z);
  return { x: x3, y: y3, z: z3 };
}

function jacobianAdd(p1: JacobianPoint, p2: JacobianPoint): JacobianPoint {
  if (isInfinity(p1)) return p2;
  if (isInfinity(p2)) return p1;
  const z1z1 = mod(p1.z * p1.z);
  const z2z2 = mod(p2.z * p2.z);
  const u1 = mod(p1.x * z2z2);
  const u2 = mod(p2.x * z1z1);
  const s1 = mod(p1.y * p2.z * z2z2);
  const s2 = mod(p2.y * p1.z * z1z1);
  if (u1 === u2) {
    if (s1 !== s2) return INFINITY;
    return jacobianDouble(p1);
  }
  const h = mod(u2 - u1);
  const i = mod(4n * h * h);
  const j = mod(h * i);
  const r = mod(2n * (s2 - s1));
  const v = mod(u1 * i);
  const x3 = mod(r * r - j - 2n * v);
  const y3 = mod(r * (v - x3) - 2n * s1 * j);
  const z3 = mod(mod((p1.z + p2.z) * (p1.z + p2.z) - z1z1 - z2z2) * h);
  return { x: x3, y: y3, z: z3 };
}

function scalarMultiply(k: bigint, point: JacobianPoint): JacobianPoint {
  let result = INFINITY;
  let addend = point;
  let n = mod(k, N);
  while (n > 0n) {
    if (n & 1n) result = jacobianAdd(result, addend);
    addend = jacobianDouble(addend);
    n >>= 1n;
  }
  return result;
}

interface AffinePoint {
  x: bigint;
  y: bigint;
}

function toAffine(p: JacobianPoint): AffinePoint | null {
  if (isInfinity(p)) return null;
  const zInv = modInverse(p.z);
  const zInv2 = mod(zInv * zInv);
  const zInv3 = mod(zInv2 * zInv);
  return { x: mod(p.x * zInv2), y: mod(p.y * zInv3) };
}

function bytesToBigInt(b: Uint8Array): bigint {
  let hex = "";
  for (let i = 0; i < b.length; i++) hex += b[i].toString(16).padStart(2, "0");
  return hex.length === 0 ? 0n : BigInt("0x" + hex);
}

function bigIntTo32Bytes(n: bigint): Uint8Array {
  const out = new Uint8Array(32);
  let v = n;
  for (let i = 31; i >= 0; i--) {
    out[i] = Number(v & 0xffn);
    v >>= 8n;
  }
  return out;
}

function isOnCurve(x: bigint, y: bigint): boolean {
  const lhs = mod(y * y);
  const rhs = mod(mod(mod(x * x) * x) + 7n);
  return lhs === rhs;
}

/**
 * liftX per BIP-340: given a 32-byte x-only coordinate, return the point with
 * that x and an EVEN y, or null if x is not on the curve (or x >= p).
 */
function liftX(xBytes: Uint8Array): AffinePoint | null {
  const x = bytesToBigInt(xBytes);
  if (x >= P) return null;
  const ySq = mod(mod(mod(x * x) * x) + 7n);
  const y = modPow(ySq, (P + 1n) / 4n, P);
  if (!isOnCurve(x, y)) return null;
  const evenY = y % 2n === 0n ? y : mod(-y);
  return { x, y: evenY };
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

/** BIP-340 tagged hash: sha256(sha256(tag) || sha256(tag) || msg). */
function taggedHash(tag: string, ...msgs: Uint8Array[]): Uint8Array {
  const tagHash = sha256(new TextEncoder().encode(tag));
  return sha256(concatBytes(tagHash, tagHash, ...msgs));
}

/**
 * BIP-340 Schnorr verification. pubkeyXOnly is the 32-byte x-only pubkey,
 * msg is the message (in this app's use, per NIP-01, always the 32-byte
 * event id — see nostrevent.ts), sig is the 64-byte signature (r || s).
 * Returns false for any malformed input rather than
 * throwing — a hostile relay's tampered event must fail closed, never crash
 * the caller's discovery loop.
 */
export function schnorrVerify(pubkeyXOnly: Uint8Array, msg: Uint8Array, sig: Uint8Array): boolean {
  try {
    // BIP-340 itself allows an arbitrary-length message (see the official
    // test vectors added 2022-12 covering 0/1/17-byte messages) — only the
    // pubkey and signature have fixed sizes. This app always calls it with
    // msg = the 32-byte nostr event id (NIP-01), but the primitive stays
    // general so it matches the spec, not just this app's one caller.
    if (pubkeyXOnly.length !== 32 || sig.length !== 64) return false;

    const P_point = liftX(pubkeyXOnly);
    if (!P_point) return false;

    const r = bytesToBigInt(sig.slice(0, 32));
    const s = bytesToBigInt(sig.slice(32, 64));
    if (r >= P || s >= N) return false;

    const e = mod(
      bytesToBigInt(taggedHash("BIP0340/challenge", sig.slice(0, 32), pubkeyXOnly, msg)),
      N,
    );

    // R = sG - eP, checked via sG + (N-e)P for consistent scalar-mult signs.
    const sG = scalarMultiply(s, G);
    const negE = mod(N - e, N);
    const eP = scalarMultiply(negE, { x: P_point.x, y: P_point.y, z: 1n });
    const R = jacobianAdd(sG, eP);
    const affineR = toAffine(R);
    if (!affineR) return false; // point at infinity
    if (affineR.y % 2n !== 0n) return false; // has_even_y(R) must hold
    if (affineR.x !== r) return false;

    return true;
  } catch {
    return false;
  }
}

export const __internal = { liftX, bigIntTo32Bytes, taggedHash };
