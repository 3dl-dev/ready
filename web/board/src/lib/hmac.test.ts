import { describe, expect, it } from "vitest";
import { hmacSha256 } from "./hmac";
import { bytesToHex } from "./sha256";

// RFC 4231 §4.2 "Test Case 1" for HMAC-SHA-256.
describe("hmacSha256", () => {
  it("matches the RFC 4231 §4.2 known-answer HMAC", () => {
    const key = new Uint8Array(20).fill(0x0b);
    const data = new TextEncoder().encode("Hi There");
    expect(bytesToHex(hmacSha256(key, data))).toBe(
      "b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7",
    );
  });

  it("matches RFC 4231 §4.3 Test Case 2 (key shorter than block size, ASCII key)", () => {
    const key = new TextEncoder().encode("Jefe");
    const data = new TextEncoder().encode("what do ya want for nothing?");
    expect(bytesToHex(hmacSha256(key, data))).toBe(
      "5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843",
    );
  });
});
