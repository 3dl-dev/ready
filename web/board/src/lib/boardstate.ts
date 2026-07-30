// BoardLoadState — ready-27b's per-board answer, extracted to a module of its
// own by ready-fe4.
//
// WHY IT IS NOT IN main.ts ANY MORE: the cache (lib/boardcache.ts) stores a
// board's load outcome alongside its admitted items, because a cached tree node
// that says "open" about a board that was withholding is exactly the lie
// ready-27b closed. main.ts imports boardcache, so boardcache cannot import
// main.ts — the type has to live below both. Nothing else moved: boardStatusOf,
// which DECIDES the state, stays in main.ts where the keyring, the
// confidentiality verdict and the session's signing capability are all in scope
// at once.

/**
 * BoardLoadState is the per-board answer the portfolio view owes its reader
 * (ready-27b). A portfolio is a list of boards from different owners with
 * different keys, and the aggregate notices (confidentialNotice and friends)
 * state portfolio-wide totals — "N of M titles were decrypted" — which is the
 * right sentence for the view and the wrong one for a board. A reader looking at
 * a project's node has one question, "is what I am seeing this project's work?",
 * and only a per-board answer can answer it.
 *
 *  - "open": the board's items are here as they are on the relay. Public, or
 *    confidential with a key this session holds.
 *  - "withholding": the board is confidential and this page could NOT establish
 *    WHEN it became confidential, so the fold withholds every card on it that is
 *    not a sealed envelope (encryptedBoardsOf on "unknown"). The count beside
 *    such a board is LOWER than the board's real item count and nothing else
 *    about the node says so. MEASURED, not hypothetical: on 2026-07-30 the live
 *    `ready` board served 536 distinct cards, 369 of them sealed, and the page
 *    showed 369 under a node reading "open" while `rd list --json` in that
 *    project said 536.
 *  - "sealed": confidential, and no key for it reached this session. Its cards
 *    render as [encrypted] placeholders.
 *  - "unreadable-grant": confidential, and an owner-signed grant NAMING THIS KEY
 *    reached the page but this browser could not open it. Distinct from "sealed"
 *    because the reader is entitled to this board and the fix is not a new grant
 *    — see BoardKeyring.granteeGrants.
 *  - "failed": the board's own event fetch or fold threw. NOTHING is known about
 *    its contents, including whether it is empty.
 */
export type BoardLoadState = "open" | "withholding" | "sealed" | "unreadable-grant" | "failed";

/** BoardStatus is one board's load outcome, in the reader's own words. `name` is
 * the board's signed title (never its coordinate — a coordinate is provenance,
 * not a name), falling back to its d-tag when the title is itself sealed. */
export interface BoardStatus {
  coord: string;
  name: string;
  state: BoardLoadState;
  /** One sentence a person can act on. Empty for "open". */
  detail: string;
}
