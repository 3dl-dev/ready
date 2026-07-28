// Auth state derivation for nostr-login-style identity attachment.
//
// VENDORED from github.com/3dl-dev/moot, file lib/auth.ts, unmodified
// (MIT License, Copyright (c) 2026 Third Division Labs — see moot's LICENSE),
// per ready-dbf done condition 2 ("reuse moot's lib/auth.ts canSign reducer").
// moot is a public repo, not vendored as a git submodule/dependency here —
// this file is a direct copy of its pure, dependency-free reducer, fetched
// from https://raw.githubusercontent.com/3dl-dev/moot/main/lib/auth.ts.
// ready-61c reviews this attribution + the divergence noted below.
//
// DIVERGENCE from moot's original: NONE in logic. moot's version is written
// against nostr-login (a browser extension shim that emits `onAuth(npub,
// options)` events for local-key / NIP-46 / read-only / extension logins);
// this board app talks to a real NIP-07 `window.nostr` extension directly
// and a hand-rolled read-only npub form (see nip07.ts), not nostr-login. The
// two auth events this app actually produces map onto moot's NlAuthOptions
// exactly: extension login -> {type:"login", method:"extension"}; read-only
// npub -> {type:"login", method:"readOnly"}; logout -> {type:"logout"}. The
// "connect"/"local"/"otp" methods moot supports (NIP-46, local key, one-time
// passcode) are out of scope here (see SCOPE note in ../main.ts) but the
// reducer is kept intact rather than trimmed, so it stays a faithful,
// diffable copy of the upstream file moot may evolve.

/** Methods nostr-login can authenticate with. `readOnly` = browse-only npub. */
export type NlAuthMethod = "connect" | "extension" | "readOnly" | "local" | "otp";

/** Shape of the `options` argument nostr-login passes to `onAuth`. */
export interface NlAuthOptions {
  type: "login" | "signup" | "logout";
  method?: NlAuthMethod;
  pubkey?: string;
}

/** Resulting moot auth state after an auth event. */
export interface AuthTransition {
  /** True once an identity is attached (signing or read-only). */
  loggedIn: boolean;
  /** True when the identity is a view-only npub and cannot sign events. */
  readOnly: boolean;
}

/**
 * Pure reducer: given a nostr-login auth event, what auth state results?
 *
 * `login` and `signup` both attach an identity; only the `readOnly` method
 * cannot sign. `logout` clears everything.
 */
export function authTransition(o: NlAuthOptions): AuthTransition {
  if (o.type === "logout") return { loggedIn: false, readOnly: false };
  return { loggedIn: true, readOnly: o.method === "readOnly" };
}

/**
 * Can this identity actually sign events? True only when an identity is attached
 * and it is not a view-only npub. Every compose/publish control gates on this so
 * a read-only login never sees signing UI it can't use (see app/providers.tsx).
 */
export function canSign(t: AuthTransition): boolean {
  return t.loggedIn && !t.readOnly;
}
