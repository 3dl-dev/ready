// @vitest-environment jsdom
import { describe, expect, it } from "vitest";
import { hasNip07Extension, loginWithExtension, type Nip07Provider } from "./nip07";

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
