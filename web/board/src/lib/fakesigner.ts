// A stand-in NIP-07 signer for tests — TEST SCAFFOLDING, NOT SHIPPED CODE.
// Nothing reachable from index.html -> main.ts imports this, so it never lands
// in dist/.
//
// It behaves the way a real extension behaves at the two points that matter to
// the confidential read path:
//
//  1. It holds the secret key. The page never sees it — the page only ever gets
//     back a STRING from nip44.decrypt. That asymmetry is the architectural
//     premise of the epic, so the fake reproduces it exactly rather than handing
//     tests a shortcut that would let a design regression pass.
//  2. It returns plaintext as a UTF-8 STRING, decoded the same lossy way every
//     real extension does. That is why pkg/sync/keydist.go's WrapKey seals hex:
//     drive this fake with a raw-byte wrap and the key comes back mangled, which
//     keyunwrap.test.ts pins.
//
// The crypto underneath is nip44ref.ts, which is validated against the official
// NIP-44 v2 vectors (nip44ref.test.ts). So "the production adapter works against
// this fake" means "the production adapter works against a spec-correct signer".

import type { Nip44Provider } from "./keyunwrap";
import { open } from "./nip44ref";

export interface FakeSignerOptions {
  /** Reject every decrypt, the way an extension does when the user declines the
   * prompt or the payload was not addressed to this key. */
  alwaysReject?: boolean;
  /** Called with each (counterpartyPubHex, payload) the page asks about — lets a
   * test assert the page did not spam the signer with wraps addressed to other
   * people. */
  onDecrypt?: (counterpartyPubHex: string, payload: string) => void;
}

/**
 * fakeNip44Signer builds the `nip44` namespace of a NIP-07 provider for a key.
 *
 * NOTE THE RETURN TYPE: `Promise<string>`, matching NIP-07. The decode back to
 * bytes happens on the PAGE side (keyunwrap.ts), which is exactly where it
 * happens in production.
 */
export function fakeNip44Signer(secretHex: string, opts: FakeSignerOptions = {}): Nip44Provider {
  return {
    async decrypt(counterpartyPubHex: string, ciphertext: string): Promise<string> {
      opts.onDecrypt?.(counterpartyPubHex, ciphertext);
      if (opts.alwaysReject) throw new Error("fake signer: user declined");
      // Throws on a MAC failure / wrong recipient, exactly as an extension does.
      const plaintextBytes = open(secretHex, counterpartyPubHex, ciphertext);
      // The lossy string boundary every extension has. Non-fatal on purpose: a
      // real TextDecoder substitutes U+FFFD rather than throwing, and that
      // silent corruption is the failure mode the hex wrap exists to avoid.
      return new TextDecoder("utf-8").decode(plaintextBytes);
    },
  };
}
