// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { awaitNip07Extension, hasNip07Extension, loginWithExtension, type Nip07Provider } from "./nip07";

function fakeWindow(nostr?: Nip07Provider): Window {
  return { nostr } as unknown as Window;
}

describe("nip07", () => {
  it("hasNip07Extension is false when window.nostr is absent", () => {
    expect(hasNip07Extension(fakeWindow(undefined))).toBe(false);
  });

  it("hasNip07Extension is true when window.nostr.getPublicKey exists", () => {
    expect(hasNip07Extension(fakeWindow({ getPublicKey: async () => "a".repeat(64) }))).toBe(true);
  });

  it("loginWithExtension resolves the lower-cased pubkey", async () => {
    const PK = "A".repeat(64);
    const pubkey = await loginWithExtension(fakeWindow({ getPublicKey: async () => PK }));
    expect(pubkey).toBe("a".repeat(64));
  });

  it("loginWithExtension throws when no extension is present", async () => {
    await expect(loginWithExtension(fakeWindow(undefined))).rejects.toThrow(/no browser extension/);
  });

  it("loginWithExtension throws on a malformed pubkey", async () => {
    await expect(loginWithExtension(fakeWindow({ getPublicKey: async () => "not-hex" }))).rejects.toThrow(
      /malformed pubkey/,
    );
  });
});

/**
 * ready-48f — THE ARRIVAL RACE, which only a real extension exposes.
 *
 * A NIP-07 extension injects window.nostr asynchronously: nos2x's content script
 * runs at document_end and appends a <script src=chrome-extension://…> that must
 * still be fetched and executed. Measured on a cold Chromium profile with nos2x
 * 2.5.2 loaded unpacked, the board's own deferred module won that race on 6 loads
 * out of 6, and the page — which sampled hasNip07Extension() once, at render —
 * left its only NIP-07 login control disabled for the life of the document.
 *
 * A CDP-injected window.nostr is installed before any page script runs, so no
 * amount of automation against an injected signer can observe this. These cases
 * are the deterministic half; the live half is scripts/live-stranger-walk.mjs,
 * which asserts the button becomes clickable in ONE document with no reload.
 */
describe("awaitNip07Extension", () => {
  it("resolves true immediately when the provider is already there", async () => {
    await expect(awaitNip07Extension(fakeWindow({ getPublicKey: async () => "a".repeat(64) }))).resolves.toBe(
      true,
    );
  });

  it("resolves true when the provider is injected AFTER the page asked", async () => {
    const win = fakeWindow(undefined);
    // The load order the race produces: the caller checks first, the extension
    // lands second. Without polling this is indistinguishable from "no
    // extension installed", which is exactly the wrong conclusion to reach.
    expect(hasNip07Extension(win)).toBe(false);
    const pending = awaitNip07Extension(win, 2000, 5);
    setTimeout(() => {
      win.nostr = { getPublicKey: async () => "b".repeat(64) };
    }, 30);
    await expect(pending).resolves.toBe(true);
  });

  it("resolves false when nothing ever arrives, rather than waiting forever", async () => {
    const started = Date.now();
    await expect(awaitNip07Extension(fakeWindow(undefined), 60, 5)).resolves.toBe(false);
    // Bounded: a page with genuinely no extension must stop looking. The bound
    // is loose because timer scheduling is not a thing to assert precisely.
    expect(Date.now() - started).toBeLessThan(3000);
  });
});
