// Ported unmodified from github.com/3dl-dev/moot's auth.test.ts (MIT
// License, Copyright (c) 2026 Third Division Labs), converted from
// node:test/node:assert to vitest so it runs under the same test runner as
// the rest of web/board. Kept as a faithful copy of the upstream test so
// auth.ts's port stays provably byte-for-byte behaviourally identical to the
// source it was vendored from (see auth.ts's header for the divergence note).
import { describe, expect, it } from "vitest";
import { authTransition, canSign } from "./auth";

describe("authTransition / canSign (vendored from moot)", () => {
  it("logout clears identity and read-only", () => {
    expect(authTransition({ type: "logout" })).toEqual({ loggedIn: false, readOnly: false });
  });

  it("read-only npub logs in but cannot sign", () => {
    expect(authTransition({ type: "login", method: "readOnly" })).toEqual({
      loggedIn: true,
      readOnly: true,
    });
  });

  it("local-key signup is a full signing identity", () => {
    expect(authTransition({ type: "signup", method: "local" })).toEqual({
      loggedIn: true,
      readOnly: false,
    });
  });

  it("NIP-46 and extension logins can sign", () => {
    for (const method of ["connect", "extension"] as const) {
      expect(authTransition({ type: "login", method }).readOnly).toBe(false);
      expect(authTransition({ type: "login", method }).loggedIn).toBe(true);
    }
  });

  it("a login with no method reported defaults to signing", () => {
    expect(authTransition({ type: "login" })).toEqual({ loggedIn: true, readOnly: false });
  });

  it("canSign gates compose UI: only attached, non-read-only identities sign", () => {
    expect(canSign({ loggedIn: false, readOnly: false })).toBe(false);
    expect(canSign({ loggedIn: true, readOnly: true })).toBe(false);
    expect(canSign({ loggedIn: true, readOnly: false })).toBe(true);
  });

  it("canSign is false for every read-only auth event, true for signing methods", () => {
    expect(canSign(authTransition({ type: "login", method: "readOnly" }))).toBe(false);
    expect(canSign(authTransition({ type: "logout" }))).toBe(false);
    for (const method of ["connect", "extension", "local", "otp"] as const) {
      expect(canSign(authTransition({ type: "login", method }))).toBe(true);
    }
  });
});
