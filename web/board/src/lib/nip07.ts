// Thin wrapper over the NIP-07 `window.nostr` shim a browser extension (e.g.
// nos2x, Alby) injects. Read-only here: getPublicKey() is the only call this
// app makes (SCOPE — no signing/publish, see main.ts). Kept as its own module
// so main.ts's orchestration logic is testable without a real extension
// (inject a fake `window.nostr` in tests).

export interface Nip07Provider {
  getPublicKey(): Promise<string>;
}

declare global {
  interface Window {
    nostr?: Nip07Provider;
  }
}

export function hasNip07Extension(win: Window = window): boolean {
  return typeof win.nostr?.getPublicKey === "function";
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
