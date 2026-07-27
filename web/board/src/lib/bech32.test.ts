// Official BIP-173 test vectors (bitcoin/bips/bip-0173.mediawiki appendix).
import { describe, expect, it } from "vitest";
import { bech32Decode, convertBits } from "./bech32";

describe("bech32Decode (BIP-173 test vectors)", () => {
  it.each([
    "A12UEL5L",
    "a12uel5l",
    "an83characterlonghumanreadablepartthatcontainsthenumber1andtheexcludedcharactersbio1tt5tgs",
    "abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw",
    "11qqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqqc8247j",
    "split1checkupstagehandshakeupstreamerranterredcaperred2y9e3w",
  ])("decodes valid bech32 %s", (s) => {
    expect(() => bech32Decode(s)).not.toThrow();
  });

  it.each([
    ["pzry9x0s0muk", "no separator character"],
    ["1pzry9x0s0muk", "empty HRP"],
    ["x1b4n0q5v", "invalid data character"],
    ["li1dgmt3", "too short checksum"],
    ["A1G7SGD8", "checksum calculated with uppercase form of HRP"],
  ])("rejects invalid bech32 %s (%s)", (s) => {
    expect(() => bech32Decode(s)).toThrow();
  });

  it("a12uel5l decodes to hrp 'a' with empty data", () => {
    const { hrp, data } = bech32Decode("a12uel5l");
    expect(hrp).toBe("a");
    expect(data).toEqual([]);
  });
});

describe("convertBits", () => {
  it("round-trips 8-bit bytes through 5-bit groups and back", () => {
    const bytes = [0, 1, 2, 253, 254, 255];
    const fiveBit = convertBits(bytes, 8, 5, true);
    const back = convertBits(fiveBit, 5, 8, false);
    expect(back).toEqual(bytes);
  });
});
