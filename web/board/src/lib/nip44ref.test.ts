// Known-answer tests for nip44ref.ts against the OFFICIAL NIP-44 v2 vectors
// (ready-c4b done condition 5).
//
// THE VECTOR FILE IS NOT COPIED. It is read from pkg/nip44/testdata/
// nip44.vectors.json — the same bytes pkg/nip44's Go tests use. A copy under
// web/board/ would be a second source of truth that silently drifts the day
// someone updates one of them; reading the original makes drift impossible and
// makes a moved file a loud failure instead of a stale pass.
//
// WHAT THIS BUYS. nip44ref.ts is the fake NIP-07 signer the confidential-board
// tests are driven through. Without a KAT it would only prove the TypeScript
// agrees with itself. With one, it is pinned to the spec, so every downstream
// test that says "a member unwraps the CEK" is really saying "a member unwraps
// the CEK through a spec-correct NIP-44 v2 signer" — which is the claim that
// matters, because in production that signer is somebody else's extension.
//
// PRODUCTION DOES NOT RUN THIS CODE. The page delegates NIP-44 to the signer and
// only decodes the returned string (keyunwrap.ts). What is validated here is the
// contract the signer is held to.

import { readFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { schnorr, secp256k1 } from "@noble/curves/secp256k1.js";
import { describe, expect, it } from "vitest";
import {
  calcPaddedLen,
  conversationKey,
  conversationKeyFromShared,
  decryptWithConversationKey,
  encryptWithNonce,
  messageKeys,
} from "./nip44ref";
import { bytesToHex, hexToBytes } from "./sha256";

const VECTORS_PATH = fileURLToPath(new URL("../../../../pkg/nip44/testdata/nip44.vectors.json", import.meta.url));

interface Vectors {
  v2: {
    valid: {
      get_conversation_key: { sec1: string; pub2: string; conversation_key: string }[];
      get_message_keys: {
        conversation_key: string;
        keys: { nonce: string; chacha_key: string; chacha_nonce: string; hmac_key: string }[];
      };
      calc_padded_len: [number, number][];
      encrypt_decrypt: {
        sec1: string;
        sec2: string;
        conversation_key: string;
        nonce: string;
        plaintext: string;
        payload: string;
      }[];
    };
    invalid: {
      decrypt: { conversation_key: string; nonce: string; plaintext: string; payload: string; note: string }[];
      get_conversation_key: { sec1: string; pub2: string; note: string }[];
    };
  };
}

const vectors: Vectors = JSON.parse(readFileSync(VECTORS_PATH, "utf8"));

describe("nip44 v2 known-answer vectors", () => {
  it("reads the same vector file pkg/nip44's Go tests read", () => {
    // A guard against a vacuous suite: if the path ever stops resolving, or the
    // file loses its shape, every test below would silently iterate an empty
    // array and pass.
    expect(vectors.v2.valid.get_conversation_key.length).toBeGreaterThan(10);
    expect(vectors.v2.valid.encrypt_decrypt.length).toBeGreaterThan(5);
    expect(vectors.v2.invalid.decrypt.length).toBeGreaterThan(5);
  });

  it("derives every valid conversation key", () => {
    for (const v of vectors.v2.valid.get_conversation_key) {
      expect(bytesToHex(conversationKey(v.sec1, v.pub2)), JSON.stringify(v)).toBe(v.conversation_key);
    }
  });

  it("expands every message-key triple", () => {
    const convKey = hexToBytes(vectors.v2.valid.get_message_keys.conversation_key);
    for (const v of vectors.v2.valid.get_message_keys.keys) {
      const mk = messageKeys(convKey, hexToBytes(v.nonce));
      expect(bytesToHex(mk.chachaKey)).toBe(v.chacha_key);
      expect(bytesToHex(mk.chachaNonce)).toBe(v.chacha_nonce);
      expect(bytesToHex(mk.hmacKey)).toBe(v.hmac_key);
    }
  });

  it("matches every calc_padded_len row", () => {
    for (const [unpadded, padded] of vectors.v2.valid.calc_padded_len) {
      expect(calcPaddedLen(unpadded), `calc_padded_len(${unpadded})`).toBe(padded);
    }
  });

  it("encrypts to the exact payload and decrypts back, both directions of every row", () => {
    for (const v of vectors.v2.valid.encrypt_decrypt) {
      // The conversation key is symmetric: derived from (sec1, pub2) it must
      // equal the vector's own value.
      const pub2 = bytesToHex(schnorr.getPublicKey(hexToBytes(v.sec2)));
      expect(bytesToHex(conversationKey(v.sec1, pub2))).toBe(v.conversation_key);

      const convKey = hexToBytes(v.conversation_key);
      const plaintext = new TextEncoder().encode(v.plaintext);
      expect(encryptWithNonce(convKey, hexToBytes(v.nonce), plaintext)).toBe(v.payload);
      expect(new TextDecoder().decode(decryptWithConversationKey(convKey, v.payload))).toBe(v.plaintext);
    }
  });

  it("REJECTS every invalid-decrypt row rather than returning garbage", () => {
    for (const v of vectors.v2.invalid.decrypt) {
      expect(
        () => decryptWithConversationKey(hexToBytes(v.conversation_key), v.payload),
        `must reject: ${v.note}`,
      ).toThrow();
    }
  });

  it("HKDF-Extract over the raw shared X is the conversation key, not sha256(X)", () => {
    // Pinned separately because it is the one construction detail that fails
    // SILENTLY when wrong — every payload just stops decrypting, with no error
    // that names the cause. pkg/nostr/ecdh.go's header makes the same point.
    const v = vectors.v2.valid.get_conversation_key[0];
    expect(bytesToHex(conversationKeyFromShared(sharedX(v.sec1, v.pub2)))).toBe(v.conversation_key);
  });
});

// --- helpers -------------------------------------------------------------

/** sharedX is the RAW secp256k1 shared X coordinate — the value NIP-44 v2
 * consumes, and the value pkg/nostr/ecdh.go's ECDH returns. */
function sharedX(sec1: string, pub2: string): Uint8Array {
  return secp256k1.getSharedSecret(hexToBytes(sec1), hexToBytes("02" + pub2)).subarray(1);
}
