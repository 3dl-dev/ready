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

/** How long awaitNip07Extension keeps looking, and how often. Long enough for a
 * content script's dynamically-appended provider script to be fetched and run
 * on a cold profile; short enough that a page with genuinely no extension stops
 * polling almost immediately in human terms. */
export const NIP07_ARRIVAL_TIMEOUT_MS = 3000;
const NIP07_ARRIVAL_POLL_MS = 100;

/**
 * awaitNip07Extension resolves true as soon as a NIP-07 provider is present, or
 * false once it has waited `timeoutMs` without one appearing. It resolves
 * SYNCHRONOUSLY-ish (on the first check) when the provider is already there, so
 * the common case costs one predicate call and no timer.
 *
 * WHY THIS EXISTS — ready-48f, measured against a REAL extension. `window.nostr`
 * is injected ASYNCHRONOUSLY: nos2x's content script runs at document_end and
 * appends a `<script src="chrome-extension://…/nostr-provider.js">`, which must
 * then be fetched and executed. The board's own bundle is a deferred module and
 * routinely wins that race — measured 6 loads out of 6 on a cold Chromium
 * profile with nos2x 2.5.2 loaded unpacked. hasNip07Extension() was sampled ONCE
 * at render, so the only NIP-07 login control on the page was left permanently
 * disabled on a load where the extension was installed, enabled and about to
 * work; the human's only recovery was a reload that might lose the race again.
 *
 * A CDP-INJECTED window.nostr — the substitute every automated proof used before
 * ready-48f — is installed before any page script runs and therefore cannot
 * observe this at all. That is why it took a real-extension walk to find it.
 */
export function awaitNip07Extension(
  win: Window = window,
  timeoutMs: number = NIP07_ARRIVAL_TIMEOUT_MS,
  pollMs: number = NIP07_ARRIVAL_POLL_MS,
): Promise<boolean> {
  if (hasNip07Extension(win)) return Promise.resolve(true);
  return new Promise<boolean>((resolve) => {
    const deadline = Date.now() + timeoutMs;
    const timer = setInterval(() => {
      if (hasNip07Extension(win)) {
        clearInterval(timer);
        resolve(true);
      } else if (Date.now() >= deadline) {
        clearInterval(timer);
        resolve(false);
      }
    }, pollMs);
  });
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
