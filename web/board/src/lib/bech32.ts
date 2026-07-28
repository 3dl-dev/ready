// bech32 (BIP-173) decode, ported from cmd/rd/follow.go's decodeNpub /
// bech32Decode / convertBits (that Go code has its own header explaining why
// rd carries no bech32 dependency: "The repo carries no bech32 dependency, so
// `rd follow npub1...` decodes it here"). Same reasoning applies here, plus
// the dist_test.go zero-external-comment guard discussed in sha256.ts —
// hand-porting avoids pulling in a bech32 npm package whose bundled output
// might carry a license-banner comment. Ported 1:1 (charset, generator
// polynomial, checksum verification), not reinvented, so this decodes exactly
// what the Go CLI decodes. Tested against BIP-173 + NIP-19 vectors in
// bech32.test.ts.

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

function verifyChecksum(hrp: string, data: number[]): boolean {
  return polymod([...hrpExpand(hrp), ...data]) === 1;
}

export interface Bech32Decoded {
  hrp: string;
  data: number[]; // 5-bit groups, checksum already stripped
}

export function bech32Decode(s: string): Bech32Decoded {
  if (s !== s.toLowerCase() && s !== s.toUpperCase()) {
    throw new Error("bech32: mixed-case string");
  }
  const lower = s.toLowerCase();
  const pos = lower.lastIndexOf("1");
  if (pos < 1 || pos + 7 > lower.length) {
    throw new Error("bech32: no separator or bad length");
  }
  const hrp = lower.slice(0, pos);
  const data: number[] = [];
  for (const c of lower.slice(pos + 1)) {
    const idx = CHARSET.indexOf(c);
    if (idx < 0) throw new Error(`bech32: invalid character ${JSON.stringify(c)}`);
    data.push(idx);
  }
  if (!verifyChecksum(hrp, data)) {
    throw new Error("bech32: bad checksum");
  }
  return { hrp, data: data.slice(0, data.length - 6) };
}

/** convertBits re-groups a bit stream between fromBits- and toBits-wide groups. */
export function convertBits(data: number[], fromBits: number, toBits: number, pad: boolean): number[] {
  let acc = 0;
  let bits = 0;
  const out: number[] = [];
  const maxv = (1 << toBits) - 1;
  for (const value of data) {
    if (value < 0 || value >> fromBits !== 0) {
      throw new Error("bech32: invalid data range");
    }
    acc = (acc << fromBits) | value;
    bits += fromBits;
    while (bits >= toBits) {
      bits -= toBits;
      out.push((acc >> bits) & maxv);
    }
  }
  if (pad) {
    if (bits > 0) out.push((acc << (toBits - bits)) & maxv);
  } else if (bits >= fromBits || ((acc << (toBits - bits)) & maxv) !== 0) {
    throw new Error("bech32: invalid padding");
  }
  return out;
}
