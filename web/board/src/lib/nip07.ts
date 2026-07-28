// Thin wrapper over the NIP-07 `window.nostr` shim a browser extension (e.g.
// nos2x, Alby) injects. Two calls, both read-side: getPublicKey() to learn who
// is logged in, and nip44.decrypt() to unwrap a confidential board's CEK
// (ready-c4b). There is still no signing and no publish path (SCOPE — see
// main.ts).
//
// THE SECRET KEY NEVER ENTERS THE PAGE. nip44.decrypt is the whole reason this
// module exists in its current shape: the extension does the ECDH and hands back
// a plaintext STRING, so the page holds a board key only for the lifetime of the
// tab and never holds an identity key at all. keyunwrap.ts owns what happens to
// that string; this file only reaches the provider.
//
// Kept as its own module so main.ts's orchestration is testable without a real
// extension (inject a fake `window.nostr` in tests).

import type { Nip44Provider } from "./keyunwrap";

export interface Nip07Provider {
  getPublicKey(): Promise<string>;
  /** Optional: not every extension implements NIP-44. Its absence is a normal
   * state — the board renders placeholders — not an error. */
  nip44?: Nip44Provider;
}

declare global {
  interface Window {
    nostr?: Nip07Provider;
  }
}

export function hasNip07Extension(win: Window = window): boolean {
  return typeof win.nostr?.getPublicKey === "function";
}

/** nip44Provider returns the extension's NIP-44 namespace, or undefined when
 * there is no extension or it does not implement NIP-44. keyunwrap.ts turns
 * undefined into an unwrapper that holds no keys. */
export function nip44Provider(win: Window = window): Nip44Provider | undefined {
  const ns = win.nostr?.nip44;
  return ns && typeof ns.decrypt === "function" ? ns : undefined;
}

/** loginWithExtension resolves the extension-held identity's hex pubkey.
 * Throws if no NIP-07 provider is present or it rejects. */
export async function loginWithExtension(win: Window = window): Promise<string> {
  if (!hasNip07Extension(win)) {
    throw new Error("nip07: no browser extension found (window.nostr is not present)");
  }
  const pubkey = await win.nostr!.getPublicKey();
  if (!/^[0-9a-f]{64}$/i.test(pubkey)) {
    throw new Error(`nip07: extension returned a malformed pubkey: ${JSON.stringify(pubkey)}`);
  }
  return pubkey.toLowerCase();
}
