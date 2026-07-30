// TS port of pkg/sync.ProjectItems (pkg/sync/nostrproject.go) — the LIVE fold
// (board-fold-spec.md §1.1, §2-§11). This is the one `rd ready`/`rd list`/
// `rd show` read through; pkg/state.DeriveAll is DEAD campfire-era code and
// is deliberately NOT ported here (spec §1.2, §14).
//
// Gate order in the main replay loop is NORMATIVE (spec §3) — reordering
// disagrees with the Go fold on edge cases (a duplicate of an untrusted
// event, a board event that would fail the board-pin test). This file
// preserves that order clause-for-clause; each block below cites the spec
// clause and the pkg/sync/nostrproject.go line range it mirrors.

import type { NostrEvent } from "./nostrevent";
import { verifiedEvents, tagValue, tagValues } from "./nostrevent";
import type { Item, HistoryEntry } from "./state";
import { StatusBlocked, StatusWaiting, isTerminal } from "./state";
import { deriveLevels, parseBoardCoord, boardCoord, AUTHORITATIVE_FOREVER } from "./rolegrant";
import {
  isConfidential,
  shouldQuarantine,
  decryptCardPayload,
  decryptStatusReason,
  PLACEHOLDER_TEXT,
} from "./envelope";
import type { BoardDecryptor, EncryptedBoardSet } from "./envelope";

export const KindBoard = 30301;
export const KindCard = 30302;
export const KindStatusOpen = 1630;
export const KindStatusResolved = 1631;
export const KindStatusClosed = 1632;
export const KindStatusDraft = 1633;
export const KindIssue = 1621;
export const KindRoleGrant = 39301;

/** isStatusKind mirrors nostrwire.go's isStatusKind (ready-816): an explicit
 * allowlist of the kinds rd itself writes and folds authoritatively —
 * {1630,1631,1632} — NOT a range test. KindStatusDraft (1633) sits inside the
 * NIP-34 1630-1633 range but rd never emits it, and §15.4 ruled it EXCLUDED:
 * a foreign client's kind-1633 draft, even signed by an already-trusted key,
 * must stay inert (spec §2.3, §14.10, §15.4). Keep this in lockstep with the
 * Go isStatusKind (pkg/sync/nostrwire.go) — a Go/TS divergence here is
 * invisible to the Go test suite and only the board vitest suite catches it. */
function isStatusKind(kind: number): boolean {
  return kind === KindStatusOpen || kind === KindStatusResolved || kind === KindStatusClosed;
}

/** itemIDForEvent mirrors nostrwire.go's itemIDForEvent. */
function itemIDForEvent(e: NostrEvent): string {
  if (e.kind === KindCard) return tagValue(e, "d");
  if (isStatusKind(e.kind)) {
    const d = tagValue(e, "d");
    if (d !== "") return d;
    const a = tagValue(e, "a");
    if (a !== "") {
      const parts = a.split(":");
      if (parts.length === 3) return parts[2];
      // a "d"-carrying boardD with embedded ':' would break a naive split(":")
      // into >3 parts; nostrwire.go's SplitN(a, ":", 3) keeps the third field
      // intact in that case. Match that exactly.
      if (parts.length > 3) return parts.slice(2).join(":");
    }
  }
  return "";
}

/** newerThan mirrors nostrproject.go's newerThan: greater created_at wins; on
 * a tie the lexicographically LOWEST event id wins (spec §4.1). */
function newerThan(a: NostrEvent, b: NostrEvent): boolean {
  if (a.created_at !== b.created_at) return a.created_at > b.created_at;
  return a.id < b.id;
}

export interface ProjectOptions {
  /** null disables the read-trust gate entirely (spec §3.4). */
  trusted: Set<string> | null;
  /** Explicit supplementary status-authority set (spec §6.3). */
  maintainers: Set<string> | null;
  /** "30301:<owner>:<boardD>" or "" (gates inert, spec §3.8). */
  pinnedBoard: string;
  decryptor: BoardDecryptor | null;
  encryptedBoards: EncryptedBoardSet | null;
}

function trusts(opts: ProjectOptions, pubkey: string): boolean {
  return opts.trusted === null ? true : opts.trusted.has(pubkey);
}

function grantTrusts(levels: Map<string, number>, pubkey: string): boolean {
  return levels.has(pubkey);
}

function firstNonEmpty(a: string, b: string): string {
  return a !== "" ? a : b;
}

function appendUniqueStr(arr: string[] | undefined, val: string): string[] {
  const out = arr ? arr.slice() : [];
  if (!out.includes(val)) out.push(val);
  return out;
}

/** itemFromCard mirrors nostrproject.go's itemFromCard (spec §5). */
function itemFromCard(e: NostrEvent, dec: BoardDecryptor | null): Item {
  const tsNano = BigInt(e.created_at) * 1_000_000_000n;
  const item: Item = {
    id: tagValue(e, "d"),
    msg_id: e.id,
    title: tagValue(e, "title"),
    status: tagValue(e, "s"),
    priority: firstNonEmpty(tagValue(e, "priority"), tagValue(e, "rank")),
    type: tagValue(e, "itype"),
    context: e.content,
    description: e.content,
    created_at: tsNano,
    updated_at: tsNano,
    blocked_by: tagValues(e, "i"),
    gate: tagValue(e, "gate") || undefined,
    waiting_type: tagValue(e, "waiting_type") || undefined,
    waiting_on: tagValue(e, "waiting_on") || undefined,
    labels: tagValues(e, "l"),
    eta: tagValue(e, "eta") || undefined,
    level: tagValue(e, "level") || undefined,
    for: tagValue(e, "for"),
    parent_id: tagValue(e, "parent") || undefined,
    due: tagValue(e, "due") || undefined,
    // claimed_during (ready-0103) — additive rd-extension tag; empty/absent on
    // old cards or a create with zero/ambiguous active claims.
    claimed_during: tagValue(e, "claimed_during") || undefined,
  };
  const p = tagValue(e, "p");
  if (p !== "") item.by = p;

  if (isConfidential(e)) {
    const pl = decryptCardPayload(e, dec);
    if (pl !== null) {
      item.title = pl.title;
      item.context = pl.context;
      item.description = pl.context;
      if (pl.waiting_on !== undefined && pl.waiting_on !== "") item.waiting_on = pl.waiting_on;
      else item.waiting_on = undefined;
      if (pl.labels && pl.labels.length > 0) item.labels = pl.labels;
    } else {
      // FAIL-CLOSED, AND MARKED — mirroring nostrproject.go:636-644. `redacted`
      // is the in-band signal that stops a future write path from re-sealing this
      // placeholder as the item's real content (state.ts's Item.redacted has the
      // full argument); the read substitution alone is safe, the
      // read-then-republish round trip is what destroys data.
      item.redacted = true;
      item.title = PLACEHOLDER_TEXT;
      item.context = PLACEHOLDER_TEXT;
      item.description = PLACEHOLDER_TEXT;
      item.waiting_on = undefined;
    }
  }
  return item;
}

/** projectItems mirrors pkg/sync.ProjectItems end to end. `events` is the
 * signed-event log slice (append order); a `null` element exercises spec
 * §3.1. Returns a Map keyed by rd item id, matching Go's map[string]*state.Item. */
export function projectItems(events: (NostrEvent | null)[], opts: ProjectOptions): Map<string, Item> {
  // §3.1 (null skip) + §3.3 (signature) HOISTED out of the replay loop, once,
  // for the whole snapshot — and everything downstream, including
  // deriveLevels, reads only `verified` (ready-75a). Previously deriveLevels
  // was handed the RAW array while verifyEvent lived in the loop below, so an
  // unsigned kind-39301 spoofing the owner's pubkey could undo a revocation.
  //
  // Hoisting §3.3 above §3.2 (dedup) does NOT change the fold's output, so the
  // normative gate order (spec §3) is preserved observably: verifyEvent is a
  // pure function of the event, and an event that fails it took `continue`
  // without touching `seen` or any accumulator. So for any input, the set of
  // events that reach §3.4 and the order they reach it in are identical —
  // including the duplicate-id cases (a forgery reusing a genuine id is
  // dropped here instead of at §3.3, and the genuine event still lands first
  // in `seen`).
  const verified = verifiedEvents(events);

  let levels = new Map<string, number>();
  let until = new Map<string, number>();
  if (opts.pinnedBoard !== "") {
    const { owner, boardD, ok } = parseBoardCoord(opts.pinnedBoard);
    if (ok) {
      const derived = deriveLevels(verified, owner, boardD);
      levels = derived.levels;
      until = derived.until;
    }
  }

  const winningCard = new Map<string, NostrEvent>();
  const statusEvents = new Map<string, NostrEvent[]>();
  const boardMaintainers = new Map<string, Set<string>>();
  const addBoardMaintainer = (coord: string, pubkey: string): void => {
    if (coord === "" || pubkey === "") return;
    let set = boardMaintainers.get(coord);
    if (!set) {
      set = new Set();
      boardMaintainers.set(coord, set);
    }
    set.add(pubkey);
  };
  const winningBoard = new Map<string, NostrEvent>();
  const seen = new Set<string>();

  for (const e of verified) {
    // §3.1 (null) and §3.3 (signature) were applied by verifiedEvents above —
    // `verified` contains no nulls and nothing that failed schnorr.
    // §3.2 — dedup by event id, first occurrence authoritative.
    if (seen.has(e.id)) continue;
    // §3.4 — read-trust.
    if (!trusts(opts, e.pubkey) && !grantTrusts(levels, e.pubkey)) continue;
    // §3.5 — point-in-time read-trust (prospective revocation).
    if (until.has(e.pubkey) && e.created_at >= (until.get(e.pubkey) as number)) continue;

    // §3.6 — board branch.
    if (e.kind === KindBoard) {
      seen.add(e.id);
      const coord = boardCoord(e.pubkey, tagValue(e, "d"));
      const cur = winningBoard.get(coord);
      if (!cur || newerThan(e, cur)) winningBoard.set(coord, e);
      continue;
    }

    // §3.7 — item-id guard.
    const itemID = itemIDForEvent(e);
    if (itemID === "") continue;

    // §3.8 — board pinning (cards only).
    if (opts.pinnedBoard !== "" && e.kind === KindCard && tagValue(e, "a") !== opts.pinnedBoard) continue;

    // §3.9 — fail-closed fold gate (confidential boards).
    if ((e.kind === KindCard || isStatusKind(e.kind)) && shouldQuarantine(e, opts.encryptedBoards)) continue;

    // §3.10 — classification.
    seen.add(e.id);
    if (e.kind === KindCard) {
      const cur = winningCard.get(itemID);
      if (!cur || newerThan(e, cur)) winningCard.set(itemID, e);
    } else if (isStatusKind(e.kind)) {
      const list = statusEvents.get(itemID);
      if (list) list.push(e);
      else statusEvents.set(itemID, [e]);
    }
  }

  // §4.5, §6.1 — maintainers from the WINNING board per coordinate only.
  for (const [coord, b] of winningBoard) {
    addBoardMaintainer(coord, b.pubkey);
    for (const m of tagValues(b, "p")) addBoardMaintainer(coord, m);
  }
  // §6.2 — grant-derived maintainers for the pinned coordinate.
  if (opts.pinnedBoard !== "") {
    for (const [k, lvl] of levels) {
      if (lvl >= 2) addBoardMaintainer(opts.pinnedBoard, k);
    }
  }

  const out = new Map<string, Item>();
  for (const [itemID, card] of winningCard) {
    const author = card.pubkey;
    const item = itemFromCard(card, opts.decryptor);

    // §6.4 — status-authority set.
    const maintainerSigners = new Set<string>();
    const coord = tagValue(card, "a");
    if (coord !== "") {
      for (const m of boardMaintainers.get(coord) ?? []) maintainerSigners.add(m);
    }
    for (const m of opts.maintainers ?? []) maintainerSigners.add(m);

    const authoritative = (statusEvents.get(itemID) ?? []).filter(
      (s) => s.pubkey === author || maintainerSigners.has(s.pubkey),
    );
    // §4.2 — deterministic ordering: (created_at asc, event-id asc).
    authoritative.sort((a, b) => (a.created_at !== b.created_at ? a.created_at - b.created_at : a.id < b.id ? -1 : a.id > b.id ? 1 : 0));

    let prevStatus = "";
    const history: HistoryEntry[] = [];
    for (const s of authoritative) {
      let toStatus = tagValue(s, "status");
      if (toStatus === "") toStatus = prevStatus;

      // §6.7 — 'by' spoof guard.
      let changedBy = s.pubkey;
      const by = tagValue(s, "by");
      if (by !== "" && maintainerSigners.has(s.pubkey)) changedBy = by;

      // §6.8 — Note.
      let note = s.content;
      if (isConfidential(s)) {
        const reason = decryptStatusReason(s, opts.decryptor);
        note = reason !== null ? reason : PLACEHOLDER_TEXT;
      }

      history.push({
        timestamp: unixSecToRFC3339(s.created_at),
        from_status: prevStatus,
        to_status: toStatus,
        changed_by: changedBy,
        note: note !== "" ? note : undefined,
      });
      const candidateUpdatedAt = BigInt(s.created_at) * 1_000_000_000n;
      if (candidateUpdatedAt > item.updated_at) item.updated_at = candidateUpdatedAt;
      prevStatus = toStatus;
    }
    if (authoritative.length > 0) {
      item.history = history;
      item.status = prevStatus;
    }
    out.set(itemID, item);
  }

  applyDepAndGateStatus(out);
  return out;
}

function unixSecToRFC3339(sec: number): string {
  return new Date(sec * 1000).toISOString().replace(/\.\d{3}Z$/, "Z");
}

/** applyDepAndGateStatus mirrors nostrproject.go's applyDepAndGateStatus —
 * the final projection pass (spec §8, §9). */
function applyDepAndGateStatus(items: Map<string, Item>): void {
  interface Edge {
    blockerID: string;
    blockedID: string;
  }
  const edges: Edge[] = [];
  for (const [id, item] of items) {
    for (const dep of item.blocked_by ?? []) edges.push({ blockerID: dep, blockedID: id });
    item.blocked_by = undefined; // rebuilt below from validated edges only
  }
  for (const e of edges) {
    const blocker = items.get(e.blockerID);
    const blocked = items.get(e.blockedID);
    if (!blocker || !blocked) continue;
    if (isTerminal(blocked)) continue;
    if (!isTerminal(blocker)) blocked.status = StatusBlocked;
    blocked.blocked_by = appendUniqueStr(blocked.blocked_by, e.blockerID);
    blocker.blocks = appendUniqueStr(blocker.blocks, e.blockedID);
  }

  for (const item of items.values()) {
    const declaresGate = !!item.waiting_type || !!item.waiting_on || !!item.gate;
    if (item.status !== StatusBlocked && !isTerminal(item) && declaresGate) {
      item.status = StatusWaiting;
    }
    if (isTerminal(item)) {
      item.waiting_on = undefined;
      item.waiting_type = undefined;
      item.waiting_since = undefined;
      item.gate_msg_id = undefined;
    } else if (declaresGate) {
      if (!item.waiting_since) {
        item.waiting_since = unixNanoToRFC3339(item.updated_at);
      }
      item.gate_msg_id = item.waiting_type === "gate" ? item.msg_id : undefined;
    } else {
      item.waiting_on = undefined;
      item.waiting_type = undefined;
      item.waiting_since = undefined;
      item.gate_msg_id = undefined;
    }
  }
}

function unixNanoToRFC3339(nano: bigint): string {
  const ms = Number(nano / 1_000_000n);
  return new Date(ms).toISOString().replace(/\.\d{3}Z$/, "Z");
}

export { AUTHORITATIVE_FOREVER };
