// The signer boundary for confidential-board key material (ready-c4b).
//
// THE ARCHITECTURAL PREMISE OF THE WHOLE EPIC: the secret key never enters the
// page. The board client cannot do ECDH, so it cannot derive a NIP-44
// conversation key, so it cannot unwrap a CEK by itself. It asks the NIP-07
// signer to, through `window.nostr.nip44.decrypt(counterpartyPubkey, payload)`.
// The extension holds the secret, does the ECDH, and hands back a plaintext.
// If you ever find yourself wanting an nsec in this file, the design has been
// lost — stop.
//
// THE STRING BOUNDARY, AND WHY THE WRAP PAYLOAD IS HEX. NIP-07's nip44.decrypt
// returns a STRING; every extension finishes with a UTF-8 TextDecoder. A raw
// 32-byte CEK is essentially never valid UTF-8, and a non-fatal decoder
// substitutes U+FFFD for each malformed run — so the key would come back
// corrupted, with no error raised anywhere, and every card would fail its AEAD
// for a reason no log would explain. pkg/sync/keydist.go's WrapKey therefore
// seals 64 lowercase-hex characters, which survive that boundary byte-for-byte.
// See TestKeydistWrapPayloadIsHexForBrowserSigners, which fails if Go goes back
// to sealing raw bytes.
//
// decodeWrappedKey accepts ONLY that hex form. It does NOT try to recover a raw
// payload out of the returned string, because by the time a raw payload has
// been through TextDecoder there is nothing left to recover, and a decoder that
// guessed would hand the caller 32 wrong bytes instead of nothing — trading a
// visible placeholder for a silent mis-decrypt. Grants minted before ready-c4b
// carry raw payloads; a browser cannot read those, and the honest outcome is
// the placeholder. (The Go CLI still reads them — unwrapKey accepts both.)

import { hexToBytes } from "./sha256";

/**
 * KeyUnwrapper turns a NIP-44-wrapped 32-byte key from a grant into raw bytes,
 * or null when this reader cannot open it. It is a SEAM: production passes
 * nip07KeyUnwrapper(), tests pass a spec-correct NIP-44 v2 implementation
 * standing in for an extension. Rejection is always null, never a throw — the
 * caller treats null as "the reader holds no such key", which is the
 * fail-closed direction.
 */
export type KeyUnwrapper = (counterpartyPubHex: string, wrapped: string) => Promise<Uint8Array | null>;

/** neverUnwraps is the unwrapper for an identity that cannot decrypt at all —
 * a read-only npub login, or an extension without NIP-44 support. It holds no
 * keys, so every confidential title renders as the placeholder. This is a
 * legitimate, expected state, not an error. */
export const neverUnwraps: KeyUnwrapper = async () => null;

const HEX64 = /^[0-9a-fA-F]{64}$/;

/**
 * decodeWrappedKey converts the signer's returned plaintext into the 32 raw key
 * bytes, or null if it is not exactly 64 hex characters. See the module header
 * for why nothing else is accepted.
 */
export function decodeWrappedKey(plaintext: string): Uint8Array | null {
  if (!HEX64.test(plaintext)) return null;
  try {
    return hexToBytes(plaintext.toLowerCase());
  } catch {
    return null;
  }
}

/** Nip44Provider is the subset of NIP-07's nip44 namespace this client uses.
 * `encrypt` is deliberately absent: the board is read-only (see main.ts SCOPE),
 * so there is no code path that seals anything. */
export interface Nip44Provider {
  decrypt(pubkey: string, ciphertext: string): Promise<string>;
}

/**
 * nip07KeyUnwrapper builds a KeyUnwrapper over a NIP-07 provider's nip44
 * namespace. A missing namespace, a rejected promise (the user declined the
 * prompt, the payload was not addressed to this key, the MAC failed) and a
 * plaintext that is not 64 hex characters all resolve to null.
 *
 * Every one of those is the same outcome for the UI — the placeholder — because
 * from the page's side they are the same fact: this key does not open this
 * wrap.
 */
export function nip07KeyUnwrapper(nip44: Nip44Provider | undefined): KeyUnwrapper {
  if (!nip44 || typeof nip44.decrypt !== "function") return neverUnwraps;
  return async (counterpartyPubHex: string, wrapped: string): Promise<Uint8Array | null> => {
    let plaintext: string;
    try {
      plaintext = await nip44.decrypt(counterpartyPubHex, wrapped);
    } catch {
      return null;
    }
    if (typeof plaintext !== "string") return null;
    return decodeWrappedKey(plaintext);
  };
}
