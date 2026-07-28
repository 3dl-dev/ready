// decodePortfolioKeys (ready-4d9) — the browser half of the `keys=` blob.
//
// TWO KINDS OF ASSERTION LIVE HERE, and they answer different questions.
//
// 1. CONFORMANCE. Every blob decoded below was emitted by the REAL Go encoder
//    (pkg/sync.EncodePortfolioKeyBlob) and committed to
//    web/board/testdata/portfolio-key-vectors.json. Go asserts encode(input) ==
//    blob; this file asserts decode(blob) == input. Neither implementation can
//    move without its own suite going red, which is the only way two
//    implementations of a URL format stay in agreement. A hand-written
//    TypeScript fixture would prove only that TypeScript agrees with itself.
//
// 2. TRUNCATION, EXHAUSTIVELY. The reason this format is binary rather than a
//    longer cek= list is that a truncated comma-delimited list still parses, as
//    a shorter well-formed list, and the reader opens a portfolio that looks
//    complete while boards are silently missing. The claim made in
//    portfoliokeys.ts's header is that NO PROPER PREFIX OF A VALID BLOB IS A
//    VALID BLOB. That is a claim about every prefix, so it is tested against
//    every prefix — all 1551 of them for the 24-board vector — not a sampled
//    few.

import { describe, expect, it } from "vitest";
import { decodePortfolioKeys, PORTFOLIO_BLOB_VERSION } from "./portfoliokeys";
import { portfolioKeyVectors, type PortfolioKeyVector } from "./portfoliovectors.fixtures";
import { bytesToHex } from "./sha256";

const vectors: PortfolioKeyVector[] = portfolioKeyVectors;

/** decoded(blob) re-expresses a decode result in the vectors' own JSON shape, so
 * the comparison below is against the committed INPUT and not against a
 * re-derivation of it. */
function decoded(blob: string): Record<string, Record<string, string>> {
  const out: Record<string, Record<string, string>> = {};
  for (const [coord, keys] of decodePortfolioKeys(blob)) {
    out[coord] = {};
    for (const { epoch, key } of keys.ceks) out[coord][String(epoch)] = bytesToHex(key);
  }
  return out;
}

/** base64url of the given bytes, matching Go's base64.RawURLEncoding. */
function toBase64Url(b: Uint8Array): string {
  let s = "";
  for (const byte of b) s += String.fromCharCode(byte);
  return btoa(s).replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");
}

function rawBytes(blob: string): Uint8Array {
  const padded = blob + "=".repeat((4 - (blob.length % 4)) % 4);
  const binary = atob(padded.replace(/-/g, "+").replace(/_/g, "/"));
  const out = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) out[i] = binary.charCodeAt(i);
  return out;
}

describe("cross-language conformance with the Go encoder", () => {
  it("has the committed vector set", () => {
    // Guards against a silently-empty or truncated vectors file turning every
    // case below into a no-op.
    expect(vectors.length).toBeGreaterThanOrEqual(8);
  });

  for (const v of vectors) {
    it(`decodes: ${v.name}`, () => {
      expect(decoded(v.blob)).toEqual(v.boards);
    });
  }

  it("the 24-board vector really is 24 boards, each with a real key", () => {
    // The size case earns its place only if it is actually the big one.
    const big = vectors[vectors.length - 1];
    const map = decodePortfolioKeys(big.blob);
    expect(map.size).toBe(24);
    for (const [coord, keys] of map) {
      expect(keys.ceks.length, coord).toBe(1);
      expect(keys.ceks[0].key.length, coord).toBe(32);
    }
  });
});

describe("TRUNCATION — every proper prefix of a valid blob is rejected", () => {
  // The property, stated over every vector rather than a chosen one.
  for (const v of vectors) {
    it(`every prefix of "${v.name}" throws`, () => {
      let checked = 0;
      for (let n = 0; n < v.blob.length; n++) {
        expect(() => decodePortfolioKeys(v.blob.slice(0, n)), `prefix of length ${n}`).toThrow();
        checked++;
      }
      // Anti-vacuity: the loop really ran, and the full blob really decodes.
      expect(checked).toBe(v.blob.length);
      expect(decodePortfolioKeys(v.blob).size).toBeGreaterThan(0);
    });
  }

  it("truncation at a BYTE boundary is rejected too, not just at a base64 boundary", () => {
    // Slicing the base64 string can land mid-byte, which a lenient decoder might
    // reject for the wrong reason. Re-encoding a truncated BYTE array produces
    // clean base64 that is nonetheless an incomplete record — the case that
    // actually matters.
    const big = vectors[vectors.length - 1];
    const raw = rawBytes(big.blob);
    for (let n = 0; n < raw.length; n++) {
      expect(() => decodePortfolioKeys(toBase64Url(raw.slice(0, n))), `byte prefix ${n}`).toThrow();
    }
    expect(decodePortfolioKeys(toBase64Url(raw)).size).toBe(24);
  });

  it("a blob with TRAILING bytes is rejected", () => {
    // The other half of the property: extra bytes mean the link is damaged too,
    // and accepting them would let two byte strings mean the same key set.
    const raw = rawBytes(vectors[0].blob);
    const longer = new Uint8Array(raw.length + 1);
    longer.set(raw);
    expect(() => decodePortfolioKeys(toBase64Url(longer))).toThrow(/trailing/i);
  });
});

describe("malformed key material is rejected, never partially applied", () => {
  const oneBoard = () => rawBytes(vectors[0].blob);

  it("an unrecognized version rejects the WHOLE link", () => {
    const raw = oneBoard();
    raw[0] = PORTFOLIO_BLOB_VERSION + 1;
    expect(() => decodePortfolioKeys(toBase64Url(raw))).toThrow(/version/i);
  });

  it("a declared owner count larger than the data throws", () => {
    const raw = oneBoard();
    raw[1] = 5; // one owner's worth of bytes, five owners declared
    expect(() => decodePortfolioKeys(toBase64Url(raw))).toThrow(/truncated/i);
  });

  it("a zero owner count throws rather than yielding an empty keyring", () => {
    // An empty keys= is not "no keys"; it is a link that says it carries keys
    // and does not. `rd` omits keys= entirely when there is nothing to carry.
    const raw = new Uint8Array([PORTFOLIO_BLOB_VERSION, 0]);
    expect(() => decodePortfolioKeys(toBase64Url(raw))).toThrow(/0 owners/);
  });

  it("epoch 0 throws — cards seal under epoch >= 1", () => {
    const raw = oneBoard();
    // Layout: version, ownerCount, 32-byte owner, boardCount, dLen, d..., epochCount, epoch(4)
    const dLenAt = 1 + 1 + 32 + 1;
    const epochAt = dLenAt + 1 + raw[dLenAt] + 1;
    raw[epochAt] = 0;
    raw[epochAt + 1] = 0;
    raw[epochAt + 2] = 0;
    raw[epochAt + 3] = 0;
    expect(() => decodePortfolioKeys(toBase64Url(raw))).toThrow(/epoch/i);
  });

  it("a zero-length board d-tag throws", () => {
    const raw = oneBoard();
    raw[1 + 1 + 32 + 1] = 0;
    expect(() => decodePortfolioKeys(toBase64Url(raw))).toThrow(/d-tag/i);
  });

  it("a d-tag that is not valid UTF-8 throws", () => {
    const raw = oneBoard();
    const dLenAt = 1 + 1 + 32 + 1;
    // 0xff can never appear in well-formed UTF-8.
    raw[dLenAt + 1] = 0xff;
    expect(() => decodePortfolioKeys(toBase64Url(raw))).toThrow();
  });

  it("a character outside the base64url alphabet throws", () => {
    expect(() => decodePortfolioKeys(vectors[0].blob.slice(0, -1) + "+")).toThrow(/base64url/);
  });

  it("the same board twice throws instead of last-wins", () => {
    // Two entries for one coordinate means the link is not what its minter
    // emitted; silently keeping one of them would hide that.
    const one = rawBytes(vectors[0].blob);
    const dLenAt = 1 + 1 + 32 + 1;
    const boardRecord = one.slice(dLenAt, one.length);
    const dup = new Uint8Array(one.length + boardRecord.length);
    dup.set(one);
    dup.set(boardRecord, one.length);
    dup[1 + 1 + 32] = 2; // boardCount = 2
    expect(() => decodePortfolioKeys(toBase64Url(dup))).toThrow(/twice/);
  });

  it("the same epoch twice for one board throws", () => {
    const two = rawBytes(vectors[1].blob); // one board, epochs 1 and 2
    const dLenAt = 1 + 1 + 32 + 1;
    const epochCountAt = dLenAt + 1 + two[dLenAt];
    expect(two[epochCountAt]).toBe(2);
    const secondEpochAt = epochCountAt + 1 + 36;
    // Rewrite the second epoch number to 1, colliding with the first.
    two[secondEpochAt] = 0;
    two[secondEpochAt + 1] = 0;
    two[secondEpochAt + 2] = 0;
    two[secondEpochAt + 3] = 1;
    expect(() => decodePortfolioKeys(toBase64Url(two))).toThrow(/twice/);
  });

  it("an empty keys= parameter throws rather than decoding to nothing", () => {
    expect(() => decodePortfolioKeys("")).toThrow();
  });
});

describe("the decoded key material is usable as-is", () => {
  it("keys are 32 raw bytes, filed under the full board coordinate", () => {
    const v = vectors.find((x) => Object.keys(x.boards).length === 2 && x.name.includes("two owners"))!;
    const map = decodePortfolioKeys(v.blob);
    expect(map.size).toBe(2);
    for (const coord of Object.keys(v.boards)) {
      const keys = map.get(coord);
      expect(keys, `missing ${coord}`).toBeDefined();
      for (const { key } of keys!.ceks) expect(key.length).toBe(32);
    }
    // Filed by COORDINATE, so two boards that shared a d-tag under different
    // owners could never collide.
    for (const coord of map.keys()) expect(coord.startsWith("30301:")).toBe(true);
  });

  it("epochs above 65535 survive (the field is 4 bytes)", () => {
    const v = vectors.find((x) => x.name.includes("65535"))!;
    const map = decodePortfolioKeys(v.blob);
    const [keys] = [...map.values()];
    expect(keys.ceks.map((c) => c.epoch)).toEqual([70000]);
  });

  it("a non-ASCII board d-tag round-trips", () => {
    const v = vectors.find((x) => x.name.includes("non-ASCII"))!;
    const coord = Object.keys(v.boards)[0];
    expect([...decodePortfolioKeys(v.blob).keys()]).toEqual([coord]);
  });
});
