import { describe, expect, it } from "vitest";
import { ecdhRawX, encryptWithNonce, decryptWithConversationKey, __internal } from "./nip44";
import { hexToBytes, bytesToHex } from "./sha256";
// Static JSON import of the SAME KAT file rd's own Go implementation
// (pkg/nip44/nip44_test.go) validates against — github.com/paulmillr/nip44's
// official NIP-44 v2 known-answer vectors. This is not a parallel fixture
// set: it is the committed file, resolved like any other module, so a
// divergence here is a divergence from the same ground truth the Go side is
// held to.
import nip44VectorsJSON from "../../../../pkg/nip44/testdata/nip44.vectors.json";

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
      decrypt: { conversation_key: string; nonce: string; payload: string; note: string }[];
    };
  };
}

function loadVectors(): Vectors {
  return nip44VectorsJSON as unknown as Vectors;
}

describe("nip44 conversation key (official KAT, get_conversation_key)", () => {
  const v = loadVectors();
  it("has vectors loaded", () => {
    expect(v.v2.valid.get_conversation_key.length).toBeGreaterThan(0);
  });
  it.each(v.v2.valid.get_conversation_key)(
    "sec1=%s pub2=%s",
    (tc: { sec1: string; pub2: string; conversation_key: string }) => {
      const sharedX = ecdhRawX(tc.sec1, tc.pub2);
      const got = __internal.conversationKeyFromShared(sharedX);
      expect(bytesToHex(got)).toBe(tc.conversation_key);
    },
  );
});

describe("nip44 message keys (official KAT, get_message_keys)", () => {
  const v = loadVectors();
  const convKey = hexToBytes(v.v2.valid.get_message_keys.conversation_key);
  it.each(v.v2.valid.get_message_keys.keys)("nonce=%s", (k) => {
    const { chachaKey, chachaNonce, hmacKey } = __internal.messageKeys(convKey, hexToBytes(k.nonce));
    expect(bytesToHex(chachaKey)).toBe(k.chacha_key);
    expect(bytesToHex(chachaNonce)).toBe(k.chacha_nonce);
    expect(bytesToHex(hmacKey)).toBe(k.hmac_key);
  });
});

describe("nip44 calc_padded_len (official KAT)", () => {
  const v = loadVectors();
  it.each(v.v2.valid.calc_padded_len)("calcPaddedLen(%i) === %i", (unpadded, want) => {
    expect(__internal.calcPaddedLen(unpadded)).toBe(want);
  });
});

describe("nip44 encrypt/decrypt (official KAT, encrypt_decrypt)", () => {
  const v = loadVectors();
  it.each(v.v2.valid.encrypt_decrypt)("sec1=%s sec2=%s", (tc) => {
    // The ECDH pipeline itself (liftX + scalarMultiply + HKDF-Extract) is
    // exercised end-to-end by the get_conversation_key suite above via
    // ecdhRawX; this suite covers the encrypt/decrypt core (pad, ChaCha20,
    // HMAC, framing) using the vector's published conversation_key directly.
    const convKey = hexToBytes(tc.conversation_key);
    const nonce = hexToBytes(tc.nonce);
    const plaintext = new TextEncoder().encode(tc.plaintext);

    const gotPayload = encryptWithNonce(convKey, nonce, plaintext);
    expect(gotPayload).toBe(tc.payload);

    const gotPlain = decryptWithConversationKey(convKey, tc.payload);
    expect(gotPlain).not.toBeNull();
    expect(new TextDecoder().decode(gotPlain!)).toBe(tc.plaintext);
  });
});

describe("nip44 decrypt fail-closed (official KAT, invalid.decrypt)", () => {
  const v = loadVectors();
  it("has negative vectors loaded", () => {
    expect(v.v2.invalid.decrypt.length).toBeGreaterThan(0);
  });
  it.each(v.v2.invalid.decrypt)("$note", (tc) => {
    const convKey = hexToBytes(tc.conversation_key);
    expect(decryptWithConversationKey(convKey, tc.payload)).toBeNull();
  });
});
