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

// NOTE — DECRYPTION IS NOT HERE, ON PURPOSE. A write-capable link carries the
// board CEKs in keys= (exactly as the read link does), so a local-key session
// decrypts confidential boards through applyFragmentKeys and seals its writes
// with those CEKs — it never needs the raw-secret NIP-44 derivation (nip44.ts)
// the extension path uses for grant unwrapping. Keeping nip44.ts out of a local
// signer keeps it out of the shipped bundle, which is the property
// dist_test.go's TestDist_NoRawSecretKeyCryptoInBundle guards. A local session's
// keyUnwrapper is therefore neverUnwraps (main.ts), the same as a read-only link.
