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
//   (empty)                       -- plain visit, no board/claim context —
//                                     the own-boards discovery path (done
//                                     condition 3) with relays from config.
//
// A fragment is NEVER sent to a server, but it also should not linger in the
// address bar / browser history after being consumed (done condition 6:
// "the fragment is STRIPPED from the URL after parsing") — history.
// replaceState removes it without a navigation/reload.

const RD1_PREFIX = "rd1_";
const NOSTR_CLAIM_VERSION = 3;

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

export type ParsedFragment =
  | { kind: "claim"; payload: NostrClaimPayload }
  | { kind: "board"; board: string; relays: string[] }
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
    return { kind: "board", board, relays };
  }

  return { kind: "none" };
}

/**
 * parseAndStripFragment reads window.location.hash and immediately removes it
 * via history.replaceState (no navigation, no reload) so a claim-nonce or
 * board coordinate never lingers in the address bar / browser history after
 * being consumed. Call once, at startup.
 */
export function parseAndStripFragment(loc: Location = window.location): ParsedFragment {
  const parsed = parseFragment(loc.hash);
  if (loc.hash !== "") {
    const url = loc.pathname + loc.search;
    window.history.replaceState(null, "", url);
  }
  return parsed;
}
