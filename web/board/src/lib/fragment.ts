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
//   #pk=<hex>&relays=<list>[&keys=<base64url>]
//                                 -- WHOLE-PORTFOLIO link (`rd board
//                                     --portfolio [--with-key]`, ready-4d9).
//                                     NO board= — and that ABSENCE is the
//                                     discriminator: pk= with no board= means
//                                     "open every board this viewer can see",
//                                     which is exactly the own-boards discovery
//                                     path main.ts already had. keys= carries
//                                     the read keys for EVERY confidential board
//                                     the minting key holds, board-scoped, in
//                                     the compact binary format
//                                     portfoliokeys.ts decodes. `rd board share`
//                                     never mints this shape either.
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
// cek=/ltk=/pk=/keys= means the LINK was damaged in transit, and the honest
// outcome is main.ts's "ask whoever shared it for a fresh link" notice — not a
// board full of placeholders the reader cannot explain. Rejection here is also
// the fail-closed direction: no partially-decoded key ever reaches the keyring.
// For keys= that rejection is TOTAL by construction (portfoliokeys.ts): no
// proper prefix of a valid blob decodes, so a portfolio link cannot arrive
// half-eaten and render as a portfolio that merely looks small.

import { hexToBytes } from "./sha256";
import { decodePortfolioKeys, type PortfolioKeys } from "./portfoliokeys";

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
      /** ready-280: relay=candidates dropped by parseRelays because a browser
       * on THIS page's origin cannot open them (see parseRelays). Present only
       * when at least one entry was dropped, so the common case (every relay
       * usable) adds no field. main.ts surfaces this to the user instead of
       * silently trying (and failing) the dropped entries. */
      droppedRelays?: string[];
    }
  | {
      /**
       * PORTFOLIO — `rd board --portfolio` (ready-4d9). Every board this viewer
       * can see, in one link. There is no `board` field on purpose: the shape is
       * defined by pk= WITHOUT board=, and main.ts answers it with the
       * unfiltered own-boards discovery it already had.
       */
      kind: "portfolio";
      relays: string[];
      /** PUBLIC pubkey the page opens as, from `pk=`. Always present — it is
       * what makes the shape identifiable and what removes the login form. */
      viewer: string;
      /** Per-board secret key material from `keys=`, keyed by board coordinate.
       * Absent for `rd board --portfolio` without --with-key. */
      keys?: PortfolioKeys;
      /** ready-280, see the "board" variant's field of the same name. */
      droppedRelays?: string[];
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

/**
 * parseFragment reads (but does not strip) the given location.hash value.
 *
 * `secure` says whether THIS page's own origin is one a browser treats as a
 * secure context (i.e. NOT plain http://). It defaults to `true` — the
 * production shape (an https:// page) and the fail-closed choice for any
 * caller (tests included) that does not pass one explicitly. Only
 * parseAndStripFragment, which has a real `Location` to read, ever passes
 * `false` — and only when `loc.protocol === "http:"` (web/board's own dev
 * loop). See parseRelays for what this gates.
 */
export function parseFragment(hash: string, secure = true): ParsedFragment {
  const raw = hash.startsWith("#") ? hash.slice(1) : hash;
  if (raw === "") return { kind: "none" };

  if (raw.startsWith(RD1_PREFIX)) {
    return { kind: "claim", payload: decodeClaimToken(raw) };
  }

  const params = new URLSearchParams(raw);
  const board = params.get("board");
  const pk = params.get("pk");
  const portfolioParam = params.get("keys");

  if (board) {
    // A SINGLE-BOARD link must not also carry portfolio key material: the two
    // shapes scope keys differently (one implicit coordinate vs. explicit
    // coordinates), and silently honouring both would make "which board does
    // this key belong to" a question with two answers.
    if (portfolioParam !== null) {
      throw new Error("fragment: board= and keys= cannot both be present — a link is either one board or the whole portfolio");
    }
    const { kept, dropped } = parseRelays(params, secure);
    const out: ParsedFragment = { kind: "board", board, relays: kept };
    if (dropped.length > 0) out.droppedRelays = dropped;
    if (pk !== null) out.viewer = decodeHexKeyParam("pk", pk);
    const keys = decodeKeyParams(params.get("cek"), params.get("ltk"));
    if (keys) out.keys = keys;
    return out;
  }

  // PORTFOLIO: pk= with no board=. See the ParsedFragment "portfolio" variant.
  if (pk !== null) {
    const { kept, dropped } = parseRelays(params, secure);
    const out: ParsedFragment = {
      kind: "portfolio",
      relays: kept,
      viewer: decodeHexKeyParam("pk", pk),
    };
    if (dropped.length > 0) out.droppedRelays = dropped;
    if (portfolioParam !== null) out.keys = decodePortfolioKeys(portfolioParam);
    return out;
  }

  // keys= with no pk= is a damaged link, not a plain visit: the key material
  // arrived but the identity it was minted for did not, so the page could not
  // open the right boards even though it is holding their keys. Say so instead
  // of quietly showing a login form.
  if (portfolioParam !== null) {
    throw new Error("fragment: keys= is present but pk= is missing — the link is incomplete");
  }

  return { kind: "none" };
}

/**
 * parseRelays reads the shared `relays=<comma-list>` parameter and rejects
 * every entry a browser on THIS page's origin cannot open, rather than
 * handing it to the connector to fail on (ready-280).
 *
 * WHY THIS EXISTS. cmd/rd/board.go's boardLinkRelays/browserOpenableRelays
 * already narrow the relay set at MINT time, so a link minted today never
 * carries a ws:// entry. But the relay list rides in the URL FRAGMENT, which
 * is exactly the part of a link that is not versioned: a bookmark or a pasted
 * link minted by a build from BEFORE that mint-side fix carries whatever
 * scheme was current then, forever. ready-634's fix could not retroactively
 * repair a link already in someone's browser history. This function is the
 * reader-side backstop for that link, checked independently of what the
 * sender's rd build happened to filter.
 *
 * THE RULE MIRRORS cmd/rd/board.go's browserOpenableRelays, evaluated
 * against the READER's real origin instead of a `--host` guess, with one
 * addition that host-side function does not need: this reader also rejects
 * any scheme that is not ws:// or wss:// outright, secure or not — the link
 * is untrusted input (a bookmark, a forwarded URL, a hand-edited fragment),
 * and a garbage scheme is not a "reachability" question the connector should
 * ever be asked to resolve by trying it. Given that ws/wss floor:
 *   - a secure (https, or any non-"http:") page origin keeps only wss://
 *     entries — mixed content blocks ws:// with no click-through.
 *   - an insecure (http://, e.g. web/board's own `vite dev` loop) page origin
 *     may keep ws:// too — there is no mixed content to block there.
 * `secure` defaults to `true` in parseFragment, so an unspecified or
 * misdetected context fails CLOSED (ws:// dropped), never open.
 *
 * Dropped entries are reported, not silently discarded: the caller (see
 * ParsedFragment's `droppedRelays`) surfaces them so a link whose relay list
 * is quietly thinner than what it claimed is a visible fact, not a mystery
 * the connector's retry loop swallows.
 */
function parseRelays(params: URLSearchParams, secure: boolean): { kept: string[]; dropped: string[] } {
  const raw = (params.get("relays") ?? "")
    .split(",")
    .map((r) => r.trim())
    .filter((r) => r !== "");
  const kept: string[] = [];
  const dropped: string[] = [];
  for (const r of raw) {
    const lower = r.toLowerCase();
    const isWss = lower.startsWith("wss://");
    const isWs = lower.startsWith("ws://");
    if (isWss || (isWs && !secure)) {
      kept.push(r);
    } else {
      dropped.push(r);
    }
  }
  return { kept, dropped };
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
  //
  // ready-280: `secure` mirrors cmd/rd/board.go's browserOpenableRelays host
  // check — anything that is not explicitly "http:" is treated as a secure
  // context, so a scheme this code does not recognize fails CLOSED (relays
  // filtered to wss:// only) rather than open.
  const secure = loc.protocol !== "http:";
  try {
    return parseFragment(loc.hash, secure);
  } finally {
    if (loc.hash !== "") {
      const url = loc.pathname + loc.search;
      window.history.replaceState(null, "", url);
    }
  }
}
