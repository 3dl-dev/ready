import { describe, expect, it } from "vitest";
import { chacha20Block, chacha20Xor } from "./chacha20";
import { hexToBytes, bytesToHex } from "./sha256";

// RFC 8439 §2.3.2 "Test Vector for the ChaCha20 Block Function" — the
// official known-answer vector, transcribed from
// https://www.rfc-editor.org/rfc/rfc8439.txt (fetched verbatim, not
// hand-typed from memory).
describe("chacha20Block", () => {
  it("matches the RFC 8439 §2.3.2 known-answer block", () => {
    const key = hexToBytes("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f");
    const nonce = hexToBytes("000000090000004a00000000");
    const block = chacha20Block(key, 1, nonce);
    expect(bytesToHex(block)).toBe(
      "10f1e7e4d13b5915500fdd1fa32071c4c7d1f4c733c068030422aa9ac3d46c4" +
        "ed2826446079faa0914c2d705d98b02a2b5129cd1de164eb9cbd083e8a2503c4e",
    );
  });
});

// RFC 8439 §2.4.2 "Example and Test Vector for the ChaCha20 Cipher" — encrypts
// the "Ladies and Gentlemen..." plaintext with counter=1.
describe("chacha20Xor", () => {
  it("matches the RFC 8439 §2.4.2 known-answer ciphertext", () => {
    const key = hexToBytes("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f");
    const nonce = hexToBytes("000000000000004a00000000");
    const plaintext = new TextEncoder().encode(
      "Ladies and Gentlemen of the class of '99: If I could offer you only one tip for the future, " +
        "sunscreen would be it.",
    );
    const ciphertext = chacha20Xor(key, 1, nonce, plaintext);
    expect(bytesToHex(ciphertext)).toBe(
      "6e2e359a2568f98041ba0728dd0d6981e97e7aec1d4360c20a27afccfd9fae0" +
        "bf91b65c5524733ab8f593dabcd62b3571639d624e65152ab8f530c359f0861d807ca0dbf50" +
        "0d6a6156a38e088a22b65e52bc514d16ccf806818ce91ab77937365af90bbf74a35be6b40b8" +
        "eedf2785e42874d",
    );
  });

  it("is its own inverse (decrypting the ciphertext recovers the plaintext)", () => {
    const key = hexToBytes("000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f");
    const nonce = hexToBytes("000000000000004a00000000");
    const plaintext = new TextEncoder().encode("round trip me please");
    const ciphertext = chacha20Xor(key, 1, nonce, plaintext);
    const back = chacha20Xor(key, 1, nonce, ciphertext);
    expect(new TextDecoder().decode(back)).toBe("round trip me please");
  });
});

// RFC 8439 §2.6.2 "Poly1305 Key Generation Test Vector": the first 32 bytes
// of chacha20Block(key, counter=0, nonce) is the one-time Poly1305 key —
// exactly the first step of chacha20poly1305.ts's seal/open.
describe("chacha20Block as the Poly1305 key generator", () => {
  it("matches the RFC 8439 §2.6.2 known-answer key", () => {
    const key = hexToBytes("808182838485868788898a8b8c8d8e8f909192939495969798999a9b9c9d9e9f");
    const nonce = hexToBytes("000000000001020304050607");
    const block = chacha20Block(key, 0, nonce);
    expect(bytesToHex(block.slice(0, 32))).toBe(
      "8ad5a08b905f81cc815040274ab29471a833b637e3fd0da508dbb8e2fdd1a646",
    );
  });
});
