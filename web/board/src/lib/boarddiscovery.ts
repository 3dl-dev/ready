// Owner-board discovery, ported from pkg/sync/boarddiscovery.go's
// DiscoverOwnerBoards (ready-636). See that file's header for the full
// rationale; the short version: a kind-30301 board event's "d" tag names an
// addressable board, and a relay is UNTRUSTED, so every candidate event is
// schnorr-verified before it mints a coordinate — ready-dbf done condition 4
// (the security property) is THIS check, not an afterthought.
//
// KIND_BOARD = 30301, matching pkg/sync/nostrwire.go's KindBoard constant.
export const KIND_BOARD = 30301;

import type { NostrEvent } from "./nostrevent";
import { tagValue, verifyEvent } from "./nostrevent";

export function boardCoord(pubkey: string, boardD: string): string {
  return `${KIND_BOARD}:${pubkey}:${boardD}`;
}

/**
 * parseBoardCoord splits a "30301:<owner>:<d>" board coordinate, ported from
 * pkg/sync/rolegrant.go's parseBoardCoord (ParseBoardCoord). Uses split-into-3
 * (not split-on-every-colon) so a boardD that itself contains ':' survives
 * intact, matching the Go implementation's splitN3 behaviour exactly.
 */
export function parseBoardCoord(coord: string): { owner: string; boardD: string } | null {
  const first = coord.indexOf(":");
  if (first < 0) return null;
  const second = coord.indexOf(":", first + 1);
  if (second < 0) return null;
  const kindPart = coord.slice(0, first);
  const owner = coord.slice(first + 1, second);
  const boardD = coord.slice(second + 1);
  if (kindPart !== String(KIND_BOARD)) return null;
  if (owner === "" || boardD === "") return null;
  return { owner, boardD };
}

export interface DiscoveredBoard {
  coord: string;
  ownerPubkey: string;
  boardD: string;
  /** "title" tag value, or "" when absent (confidential board — see main.ts's
   * placeholder-title rendering, ready-dbf SCOPE note). */
  title: string;
}

/** newerBoardEvent mirrors fold.ts's newerThan / pkg/sync's newerThan
 * (board-fold-spec.md §4.5) EXACTLY: greatest created_at wins; on a tie the
 * lexicographically LOWEST event id wins. Board discovery needs its own
 * latest-wins pass — separate from fold.ts's, which folds ITEMS for one
 * already-known coordinate — because a relay snapshot here can hold more than
 * one kind-30301 event per coordinate (a multi-relay merge where relays
 * disagree on which definition is current), and which one wins decides
 * whether the "archived" marker (ready-a9b) is honoured or missed. */
function newerBoardEvent(a: NostrEvent, b: NostrEvent): boolean {
  if (a.created_at !== b.created_at) return a.created_at > b.created_at;
  return a.id < b.id;
}

/** isArchivedBoard mirrors pkg/sync.IsBoardArchived: any non-empty "archived"
 * tag value counts, matching isConfidential's presence-based style so a later
 * marker version does not need a matching release to keep being honoured. */
function isArchivedBoard(e: NostrEvent): boolean {
  return tagValue(e, "archived") !== "";
}

/**
 * discoverOwnerBoards mirrors DiscoverOwnerBoards, extended by two rules
 * DiscoverOwnerBoards does not need (it enumerates coordinates for `rd
 * follow`'s one-time bind, never renders a board list): given a relay event
 * snapshot and the set of owner pubkeys the caller resolved the follow target
 * to, return every 30301 board coordinate those owners published — sorted,
 * de-duplicated by coordinate, filtered to boardD when non-empty, and:
 *
 *   - latest-wins per coordinate (newerBoardEvent) when the snapshot holds
 *     more than one kind-30301 event for it, instead of first-seen-in-array —
 *     the array order is relay-answer order, not a trust signal, and picking
 *     a stale definition would un-hide an archived board or misrender a
 *     retitled one depending on which relay answered first.
 *   - a coordinate whose WINNING definition carries the archived marker
 *     (ready-a9b, isArchivedBoard) is dropped from the result entirely: this
 *     is the browser-side half of `rd board archive`'s contract (the CLI
 *     portfolio gather is the other half, cmd/rd/board_portfolio.go's
 *     filterArchivedBoards).
 *
 * Every candidate is schnorr-verified (verifyEvent) before it contributes a
 * coordinate: a forged/tampered kind-30301 served by a hostile relay, or one
 * authored by a pubkey NOT in ownerPubkeys, must never appear in the result.
 * This is the negative-test security property (done condition 4) — see
 * boarddiscovery.test.ts's forged-signature and wrong-author cases, which
 * fail (a forged board appears) if the verifyEvent call below is removed.
 */
export function discoverOwnerBoards(
  events: NostrEvent[],
  ownerPubkeys: string[],
  boardD = "",
): DiscoveredBoard[] {
  const owners = new Set(ownerPubkeys.filter((pk) => pk !== ""));
  if (owners.size === 0) return [];

  const winners = new Map<string, NostrEvent>();

  for (const e of events) {
    if (!e || e.kind !== KIND_BOARD) continue;
    if (!owners.has(e.pubkey)) continue;
    // SECURITY: a relay is untrusted. Do not let a forged signature or a
    // tampered id mint a coordinate. Removing this line is exactly the
    // mutation boarddiscovery.test.ts's negative tests catch.
    if (!verifyEvent(e)) continue;

    const d = tagValue(e, "d");
    if (d === "") continue;
    if (boardD !== "" && d !== boardD) continue;

    const coord = boardCoord(e.pubkey, d);
    const cur = winners.get(coord);
    if (!cur || newerBoardEvent(e, cur)) winners.set(coord, e);
  }

  const out: DiscoveredBoard[] = [];
  for (const [coord, e] of winners) {
    if (isArchivedBoard(e)) continue;
    out.push({ coord, ownerPubkey: e.pubkey, boardD: tagValue(e, "d"), title: tagValue(e, "title") });
  }

  out.sort((a, b) => (a.coord < b.coord ? -1 : a.coord > b.coord ? 1 : 0));
  return out;
}
