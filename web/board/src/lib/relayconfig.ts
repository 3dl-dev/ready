// Relay set resolution — ready-dbf done condition 5: "Relay set comes from
// the token's relays[] (or config for the no-token own-boards path), NEVER a
// hardcoded public relay list — swap moot's DEFAULT_RELAYS out."
//
// moot's lib/ndk.ts hardcodes DEFAULT_RELAYS = [wss://relay.damus.io, ...] as
// a literal array baked into the JS bundle. This app does the opposite for
// its own-boards (no-token) path: the relay list lives in a same-origin
// runtime JSON file (public/relays.json, copied verbatim into dist/ by
// Vite's public-dir handling, i.e. NOT part of any .js/.html/.css) fetched at
// startup. Two independent reasons this shape was chosen over a hardcoded
// TS constant:
//   1. Ops can repoint the own-boards relay set without a rebuild/redeploy.
//   2. web/board/dist_test.go's TestDist_NoExternalReferences scans every
//      shipped .html/.js/.css for a bare "//" substring — a hardcoded
//      `"wss://relay.3dl.network"` string literal in source would land in
//      the built bundle and trip that guard (a WebSocket URL legitimately
//      contains "//" right after its scheme, same as any http(s) URL the
//      test is built to catch). A .json asset is outside that scan's file
//      extensions entirely, so the relay list is treated as DATA, not code —
//      which is also just the more honest shape for something ops-editable.
//
// The token path (fragment.ts's ParsedFragment "claim"/"board" cases) never
// touches this file: those relay lists come from the token/link itself, per
// done condition 5's first clause.

export interface RelayConfig {
  ownBoardsRelays: string[];
}

/**
 * loadOwnBoardsRelays fetches the runtime relay config for the no-token
 * "log in and see all your own boards" path. `path` is same-origin-relative
 * (resolves under wherever the page is served, e.g. /board/relays.json in
 * production) so this never hardcodes the deploy host either.
 */
export async function loadOwnBoardsRelays(
  fetchFn: typeof fetch = fetch,
  path = "relays.json",
): Promise<string[]> {
  const res = await fetchFn(path);
  if (!res.ok) {
    throw new Error(`relayconfig: fetch ${path}: HTTP ${res.status}`);
  }
  const parsed = (await res.json()) as Partial<RelayConfig>;
  if (!Array.isArray(parsed.ownBoardsRelays) || parsed.ownBoardsRelays.length === 0) {
    throw new Error(`relayconfig: ${path} missing non-empty "ownBoardsRelays"`);
  }
  return parsed.ownBoardsRelays;
}
