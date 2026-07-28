import { describe, expect, it } from "vitest";
import { poly1305Mac } from "./poly1305";
import { hexToBytes, bytesToHex } from "./sha256";

// RFC 8439 §2.5.2 "Poly1305 Example and Test Vector" — the official
// known-answer vector, fetched verbatim from
// https://www.rfc-editor.org/rfc/rfc8439.txt.
describe("poly1305Mac", () => {
  it("matches the RFC 8439 §2.5.2 known-answer tag", () => {
    const key = hexToBytes("85d6be7857556d337f4452fe42d506a80103808afb0db2fd4abff6af4149f51b");
    const msg = new TextEncoder().encode("Cryptographic Forum Research Group");
    expect(bytesToHex(poly1305Mac(msg, key))).toBe("a8061dc1305136c6c22b8baf0c0127a9");
  });
});
