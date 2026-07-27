// NIST/FIPS 180-4 test vectors, not hand-picked examples.
import { describe, expect, it } from "vitest";
import { sha256Hex } from "./sha256";

function utf8(s: string): Uint8Array {
  return new TextEncoder().encode(s);
}

describe("sha256", () => {
  it('hashes the empty string', () => {
    expect(sha256Hex(utf8(""))).toBe(
      "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855",
    );
  });

  it('hashes "abc" (FIPS 180-4 one-block message)', () => {
    expect(sha256Hex(utf8("abc"))).toBe(
      "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad",
    );
  });

  it("hashes the FIPS 180-4 two-block message", () => {
    expect(sha256Hex(utf8("abcdbcdecdefdefgefghfghighijhijkijkljklmklmnlmnomnopnopq"))).toBe(
      "248d6a61d20638b8e5c026930c3e6039a33ce45964ff2167f6ecedd419db06c1",
    );
  });

  it("hashes one million repeated 'a' bytes", () => {
    expect(sha256Hex(utf8("a".repeat(1_000_000)))).toBe(
      "cdc76e5c9914fb9281a1c7e284d73e67f1809a48a497200e046d39ccc7112cd0",
    );
  });
});
