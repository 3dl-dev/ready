// NIP-19 npub <-> hex pubkey, ported from cmd/rd/follow.go's decodeNpub (the
// same function `rd follow npub1...` and `rd board share npub1...` use) so
// the browser accepts exactly the identities the CLI does. bech32.ts carries
// the generic bech32 primitives; this file is the npub-specific 5-bit<->8-bit
// conversion and hrp check.

import { bech32Decode, convertBits } from "./bech32";
import { bytesToHex, hexToBytes } from "./sha256";

/** decodeNpub turns "npub1..." into a 64-char lowercase hex pubkey. */
export function decodeNpub(npub: string): string {
  const { hrp, data } = bech32Decode(npub);
  if (hrp !== "npub") {
    throw new Error(`npub: not an npub (bech32 hrp=${JSON.stringify(hrp)})`);
  }
  const conv = convertBits(data, 5, 8, false);
  if (conv.length !== 32) {
    throw new Error(`npub: decodes to ${conv.length} bytes, want a 32-byte pubkey`);
  }
  return bytesToHex(Uint8Array.from(conv));
}

const CHARSET = "qpzry9x8gf2tvdw0s3jn54khce6mua7l";

function polymod(values: number[]): number {
  const gen = [0x3b6a57b2, 0x26508e6d, 0x1ea119fa, 0x3d4233dd, 0x2a1462b3];
  let chk = 1;
  for (const v of values) {
    const b = chk >>> 25;
    chk = ((chk & 0x1ffffff) << 5) ^ v;
    for (let i = 0; i < 5; i++) {
      if ((b >>> i) & 1) chk ^= gen[i];
    }
  }
  return chk >>> 0;
}

function hrpExpand(hrp: string): number[] {
  const out: number[] = [];
  for (const c of hrp) out.push(c.charCodeAt(0) >> 5);
  out.push(0);
  for (const c of hrp) out.push(c.charCodeAt(0) & 31);
  return out;
}

function createChecksum(hrp: string, data: number[]): number[] {
  const values = [...hrpExpand(hrp), ...data, 0, 0, 0, 0, 0, 0];
  const mod = polymod(values) ^ 1;
  const out: number[] = [];
  for (let i = 0; i < 6; i++) out.push((mod >>> (5 * (5 - i))) & 31);
  return out;
}

/** encodeNpub turns a 64-char hex pubkey into "npub1...". Used to render the
 * logged-in identity for the awaiting-authorization state (done condition 6)
 * without exposing raw hex in the UI. */
export function encodeNpub(pubkeyHex: string): string {
  const bytes = hexToBytes(pubkeyHex);
  if (bytes.length !== 32) {
    throw new Error(`npub: pubkey must be 32 bytes, got ${bytes.length}`);
  }
  const data = convertBits(Array.from(bytes), 8, 5, true);
  const checksum = createChecksum("npub", data);
  const combined = [...data, ...checksum];
  return "npub1" + combined.map((d) => CHARSET[d]).join("");
}
