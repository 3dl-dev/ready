// localsigner.ts — a NIP-07-shaped signer backed by a secret this page holds,
// for the EXTENSION-FREE write path (ready-f947).
//
// This is the one and only place the board signs from a secret key rather than
// from a NIP-07 extension. It exists because the owner opted out of extensions
// ("browser plugins are DOA") and into carrying their own key — a deliberate
// reversal of the "the board never accepts a secret key" posture the rest of
// this app was built around, and one that MUST be gated by a security review and
// the loud bearer-credential warning `rd board` already prints, because a page
// holding a signing key is a page whose whole security rests on where that key
// came from and where it can go.
//
// What it is NOT: a way in for a pasted `nsec` from an untrusted origin. The
// secret must arrive from the owner's own `rd` (the link/session the owner
// minted), the same provenance the read-side `keys=` already trusts. Every other
// module still reaches signing only through the Nip07Signer interface and cannot
// tell a local signer from an extension — the seam does not widen.
//
// The key never leaves the page: signEvent computes the schnorr signature here,
// against the same KAT-validated primitive (schnorrsign.ts) that rd signs with.
import { signNostrEvent } from "./schnorrsign";
import type { Nip07Signer } from "./publish";
import type { Nip44Provider } from "./keyunwrap";
import { open } from "./nip44";

export function localSigner(secretHex: string): Nip07Signer {
  return {
    async signEvent(event) {
      // Sign EXACTLY the fields handed in — never restamp created_at. publish.ts's
      // assertSignedAsBuilt refuses a signer that rewrote any field, because a
      // status event anchors to the card published beside it by the id computed
      // from the UNSIGNED card; a rewritten field breaks that anchor. A local
      // signer has no excuse to rewrite anything, and this one does not.
      return signNostrEvent(
        { created_at: event.created_at, kind: event.kind, tags: event.tags, content: event.content },
        secretHex,
      );
    },
  };
}

/**
 * localNip44Provider is the read-side counterpart: it unwraps a confidential
 * board's grant with the owner's own secret, using nip44.open — the same NIP-44
 * v2 primitive the extension path reaches through window.nostr.nip44.decrypt. It
 * lets a write-capable (local-key) session decrypt grants with no extension,
 * exactly as it signs with none. Returns the plaintext string keyunwrap.ts
 * expects; a decrypt that fails throws, which keyunwrap treats as "not for us".
 */
export function localNip44Provider(secretHex: string): Nip44Provider {
  return {
    async decrypt(counterpartyPubHex, ciphertext) {
      const plaintext = open(secretHex, counterpartyPubHex, ciphertext);
      if (plaintext === null) throw new Error("nip44: decryption failed");
      return new TextDecoder().decode(plaintext);
    },
  };
}
