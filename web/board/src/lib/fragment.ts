// URL fragment parsing for the two shapes cmd/rd/board.go's `rd board` /
// `rd board share` emit (see that file's header comment for the full
// rationale) plus the bare rd1_ claim-nonce token cmd/rd/nostr_invite.go
// mints:
//
//   #rd1_<base64url>              -- claim-nonce token (unknown-key share,
//                                     ready-dbf done condition 6). Decodes to
//                                     nostrClaimPayload (nostr_invite.go).
//   #board=<coord>&relays=<list>  -- own-board / known-key-share link
//                                     (ownBoardURL in board.go). No claim.
//   #board=...&pk=<hex>&cek=<epoch>:<hex>[,...]
//                                 -- the same own-board link minted by
//                                     `rd board --with-key` (ready-df0): it
//                                     also carries the viewer's PUBLIC pubkey
//                                     (so nothing has to be pasted) and the
//                                     board read key(s) that pubkey already
//                                     holds, so a plain browser with NO NIP-07
//                                     extension can decrypt a confidential
//                                     board. `rd board share` NEVER mints this
//                                     shape — third-party read access is the
//                                     owner-signed kind-39301 grant, which
//                                     wraps the key to one specific grantee.
//   ...&ltk=<hex>                 -- LEGACY, PARSED BUT NOT EMITTED. `rd board
//                                     --with-key` used to append the board's
//                                     label-token key. It was dropped on
//                                     least-privilege grounds: nothing in this
//                                     app reads an LTK — BoardKeyring.ltk() and
//                                     envelope.labelToken() have no caller
//                                     outside their own tests, because labels
//                                     are filtered client-side on decrypted
//                                     plaintext and the relay-side `#l` filter
//                                     path (spec §7) has not been built. A link
//                                     minted by an older build still carries
//                                     one and must keep working, so decodeKeyParams
//                                     still accepts and validates ltk=. When an
//                                     LTK consumer lands, re-add EMISSION in
//                                     cmd/rd/board.go (see its LEAST PRIVILEGE
//                                     note) — nothing has to change here.
//   (empty)                       -- plain visit, no board/claim context —
//                                     the own-boards discovery path (done
//                                     condition 3) with relays from config.
//
// A fragment is NEVER sent to a server, but it also should not linger in the
// address bar / browser history after being consumed (done condition 6:
// "the fragment is STRIPPED from the URL after parsing") — history.
// replaceState removes it without a navigation/reload. That property is what
// makes the key-bearing shape above tolerable at all, so it is now doubly
// load-bearing: see parseAndStripFragment's `finally`.
//
// MALFORMED KEY MATERIAL THROWS, it is not silently dropped. A truncated
// cek=/ltk=/pk= means the LINK was damaged in transit, and the honest outcome
// is main.ts's "ask whoever shared it for a fresh link" notice — not a board
// full of placeholders the reader cannot explain. Rejection here is also the
// fail-closed direction: no partially-decoded key ever reaches the keyring.

import { hexToBytes } from "./sha256";

const RD1_PREFIX = "rd1_";
const NOSTR_CLAIM_VERSION = 3;
const HEX64 = /^[0-9a-f]{64}$/;

/** Mirrors cmd/rd/nostr_invite.go's nostrClaimPayload JSON shape exactly. */
export interface NostrClaimPayload {
  v: number;
  board: string; // "30301:<ownerPubkey>:<boardD>"
  relays: string[];
  claim: string;
  iat: number;
  exp: number;
  iss: string;
}

/**
 * FragmentKeys is the key material an `rd board --with-key` link carries for
 * the ONE board coordinate named in the same fragment (ready-df0).
 *
 * EVERY held epoch travels, not just the newest. A board that has rotated its
 * CEK has cards sealed under older epochs; shipping only the current key would
 * leave those cards showing the placeholder in the browser even though the same
 * key opens them in the CLI. Mirrors cmd/rd/board.go's boardKeyFragment.
 */
export interface FragmentKeys {
  ceks: { epoch: number; key: Uint8Array }[];
  /** LEGACY, from links minted before the LTK was dropped from emission (see the
   * module header). Still parsed so those links keep opening; nothing reads it. */
  ltk?: Uint8Array;
}

export type ParsedFragment =
  | { kind: "claim"; payload: NostrClaimPayload }
  | {
      kind: "board";
      board: string;
      relays: string[];
      /** PUBLIC pubkey the page should open as, from `pk=`. Present only on a
       * `rd board --with-key` link; when present the page skips the login form
       * entirely, which is the whole point ("no extension, nothing to paste"). */
      viewer?: string;
      /** Secret key material from `cek=` (and legacy `ltk=`). Absent for a
       * non-confidential board (nothing to decrypt) and for every default/share
       * link. */
      keys?: FragmentKeys;
    }
  | { kind: "none" };

function base64UrlDecode(s: string): string {
  const padded = s + "=".repeat((4 - (s.length % 4)) % 4);
  const b64 = padded.replace(/-/g, "+").replace(/_/g, "/");
  const binary = atob(b64);
  const bytes = new Uint8Array(binary.length);
  for (let i = 0; i < binary.length; i++) bytes[i] = binary.charCodeAt(i);
  return new TextDecoder().decode(bytes);
}

function decodeClaimToken(token: string): NostrClaimPayload {
  if (token.length <= RD1_PREFIX.length) {
    throw new Error("fragment: rd1_ token too short");
  }
  const decoded = base64UrlDecode(token.slice(RD1_PREFIX.length));
  const payload = JSON.parse(decoded) as NostrClaimPayload;
  if (payload.v !== NOSTR_CLAIM_VERSION) {
    throw new Error(`fragment: unsupported rd1_ token version ${payload.v}`);
  }
  if (!payload.board || !payload.claim) {
    throw new Error("fragment: rd1_ token missing board or claim-nonce");
  }
  return payload;
}

/** parseFragment reads (but does not strip) the given location.hash value. */
export function parseFragment(hash: string): ParsedFragment {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  if (raw === "") return { kind: "none" };

  if (raw.startsWith(RD1_PREFIX)) {
    return { kind: "claim", payload: decodeClaimToken(raw) };
  }

  const params = new URLSearchParams(raw);
  const board = params.get("board");
  if (board) {
    const relaysParam = params.get("relays") ?? "";
    const relays = relaysParam
      .split(",")
      .map((r) => r.trim())
      .filter((r) => r !== "");
    const out: ParsedFragment = { kind: "board", board, relays };
    const viewer = params.get("pk");
    if (viewer !== null) out.viewer = decodeHexKeyParam("pk", viewer);
    const keys = decodeKeyParams(params.get("cek"), params.get("ltk"));
    if (keys) out.keys = keys;
    return out;
  }

  return { kind: "none" };
}

/** decodeHexKeyParam validates one 64-lowercase-hex fragment parameter. The
 * exact-length check is what makes a truncated link a visible error instead of
 * a half-key that would fail every AEAD for no stated reason. */
function decodeHexKeyParam(name: string, value: string): string {
  const v = value.trim().toLowerCase();
  if (!HEX64.test(v)) {
    throw new Error(`fragment: ${name}= is not 64 hex characters`);
  }
  return v;
}

/**
 * decodeKeyParams parses `cek=<epoch>:<hex>[,<epoch>:<hex>...]` and the legacy
 * `ltk=<hex>` into raw key bytes, or returns null when neither parameter is
 * present (the default link, and any non-confidential board).
 *
 * ltk= is no longer emitted by `rd board --with-key` (module header). It is
 * still accepted, and still validated to the same 64-hex standard, so a link
 * from an older build opens instead of tripping the malformed-fragment notice.
 *
 * An epoch must be a positive integer, matching Go's DeriveBoardKeyring, which
 * refuses any grant with cek_epoch < 1 rather than binding a key to epoch 0.
 */
function decodeKeyParams(cekParam: string | null, ltkParam: string | null): FragmentKeys | null {
  if (cekParam === null && ltkParam === null) return null;

  const ceks: { epoch: number; key: Uint8Array }[] = [];
  if (cekParam !== null) {
    for (const entry of cekParam.split(",")) {
      const trimmed = entry.trim();
      if (trimmed === "") continue;
      const sep = trimmed.indexOf(":");
      if (sep < 0) throw new Error("fragment: cek= entry is not <epoch>:<hex>");
      const epoch = Number(trimmed.slice(0, sep));
      if (!Number.isSafeInteger(epoch) || epoch < 1) {
        throw new Error(`fragment: cek= epoch ${JSON.stringify(trimmed.slice(0, sep))} is not a positive integer`);
      }
      ceks.push({ epoch, key: hexToBytes(decodeHexKeyParam("cek", trimmed.slice(sep + 1))) });
    }
    if (ceks.length === 0) throw new Error("fragment: cek= is present but carries no key");
  }

  const out: FragmentKeys = { ceks };
  if (ltkParam !== null) out.ltk = hexToBytes(decodeHexKeyParam("ltk", ltkParam));
  return out;
}

/**
 * parseAndStripFragment reads window.location.hash and immediately removes it
 * via history.replaceState (no navigation, no reload) so a claim-nonce or
 * board coordinate never lingers in the address bar / browser history after
 * being consumed. Call once, at startup.
 */
export function parseAndStripFragment(loc: Location = window.location): ParsedFragment {
  // ready-62d1: strip in a `finally`, so a malformed fragment is removed from
  // the address bar and history even though parsing it throws. Stripping used
  // to run only after a successful parse, which meant a corrupted claim-nonce
  // -- the exact case where a token most wants removing -- stayed in the URL.
  try {
    return parseFragment(loc.hash);
  } finally {
    if (loc.hash !== "") {
      const url = loc.pathname + loc.search;
      window.history.replaceState(null, "", url);
    }
  }
}
