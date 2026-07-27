// NIP-19 example vector (nostr-protocol/nips/19.md): the hex pubkey
// 3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d
// translates to npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6.
import { describe, expect, it } from "vitest";
import { decodeNpub, encodeNpub } from "./npub";

const HEX = "3bf0c63fcb93463407af97a5e5ee64fa883d107ef9e558472c4eb9aaaefa459d";
const NPUB = "npub180cvv07tjdrrgpa0j7j7tmnyl2yr6yr7l8j4s3evf6u64th6gkwsyjh6w6";

describe("npub (NIP-19)", () => {
  it("decodes the NIP-19 example npub to its hex pubkey", () => {
    expect(decodeNpub(NPUB)).toBe(HEX);
  });

  it("encodes the hex pubkey back to the same npub", () => {
    expect(encodeNpub(HEX)).toBe(NPUB);
  });

  it("round-trips an arbitrary 32-byte pubkey", () => {
    const hex = "247640daeb6e29711f8a8982aa78622c52a9605f0cb38ced142e8532353916a4";
    expect(decodeNpub(encodeNpub(hex))).toBe(hex);
  });

  it("rejects a non-npub bech32 string (valid bech32, wrong hrp)", () => {
    // BIP-173 official test vector: valid checksum, hrp "abcdef".
    expect(() => decodeNpub("abcdef1qpzry9x8gf2tvdw0s3jn54khce6mua7lmqqqxw")).toThrow();
  });

  it("rejects garbage input", () => {
    expect(() => decodeNpub("not-an-npub")).toThrow();
  });
});
