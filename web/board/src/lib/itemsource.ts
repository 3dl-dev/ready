// The seam between verified relay events and the board's Item[] (see
// ../board/types.ts's header comment for why this is a seam and not a
// second fold implementation). ready-35b owns the conformant projection
// (docs/design/board-fold-spec.md, vector gate + live parity gate); this
// module is the one place main.ts wires that projection in, without the
// board UI knowing anything about nostr events.
//
// ready-bad: loadItems used to always reject, because ready-445 (the UI) and
// ready-35b (the fold) were built in parallel and the fold had not landed. It
// has (PR #141): projectItems passes all 36 conformance vectors byte-for-byte
// and matched `rd list --json --all` across three real boards — ready 421
// items, dontguess 630, forge 200 — with zero unexplained field diffs. This
// module now adapts its output to the UI's Item shape.
import type { Item as UIItem, HistoryEntry as UIHistoryEntry, Status } from "../board/types";
import type { Item as FoldItem, HistoryEntry as FoldHistoryEntry } from "./state";
import { projectItems, type ProjectOptions } from "./fold";
import type { NostrEvent } from "./nostrevent";

export class ItemProjectionNotImplementedError extends Error {
  constructor() {
    super(
      "event -> Item[] projection not implemented in the client yet — see ready-35b " +
        "(docs/design/board-fold-spec.md); wire ItemSource.loadItems there, not here",
    );
    this.name = "ItemProjectionNotImplementedError";
  }
}

export interface ItemSource {
  loadItems(events: NostrEvent[]): UIItem[];
}

/**
 * The fold's HistoryEntry names the actor `changed_by` (matching state.go and
 * spec §6.7, where it is the SIGNER's pubkey after the `by`-tag spoof guard at
 * nostrproject.go:401-404). The UI calls the same field `actor`. Renaming here
 * rather than in either type keeps both honest to their own domain.
 */
function toUIHistory(h: FoldHistoryEntry): UIHistoryEntry {
  return {
    timestamp: h.timestamp,
    fromStatus: h.from_status,
    toStatus: h.to_status,
    actor: h.changed_by,
    note: h.note,
  };
}

/**
 * toUIItem maps the fold's snake_case, spec-shaped Item onto the board UI's
 * camelCase one. A pure rename plus one narrowing, deliberately kept dumb —
 * any real projection logic belongs in fold.ts, where the vector suite sees it.
 *
 * TIMESTAMP NARROWING, stated because it resembles a bug we just fixed: the
 * fold carries created_at/updated_at as BIGINT unix nanoseconds precisely
 * because they exceed Number.MAX_SAFE_INTEGER and lose precision through
 * JSON.parse (ready-414). Converting to `number` here re-introduces that loss —
 * but only in the UI layer, and only at ~hundreds-of-nanoseconds scale, which
 * cannot change an age readout or a longest-untouched ordering. The
 * conformance-critical value stays bigint through fold.ts and the vector suite.
 * Do NOT "fix" this by making the fold emit numbers.
 */
export function toUIItem(f: FoldItem, boardCoord?: string): UIItem {
  return {
    id: f.id,
    msgId: f.msg_id,
    title: f.title,
    context: f.context,
    type: f.type,
    level: f.level,
    project: f.project,
    boardCoord,
    for: f.for,
    by: f.by,
    priority: f.priority,
    status: f.status as Status,
    eta: f.eta,
    due: f.due,
    parentId: f.parent_id,
    blockedBy: f.blocked_by,
    blocks: f.blocks,
    gate: f.gate,
    waitingOn: f.waiting_on,
    waitingType: f.waiting_type,
    waitingSince: f.waiting_since,
    gateMsgId: f.gate_msg_id,
    createdAt: Number(f.created_at),
    updatedAt: Number(f.updated_at),
    history: f.history?.map(toUIHistory),
    labels: f.labels,
    labelWarnings: f.label_warnings,
    crossBoardWarnings: f.cross_campfire_warnings,
  };
}

/**
 * foldItemSource projects verified events through ready-35b's conformant fold.
 *
 * On `opts.trusted: null` — which disables the read-trust gate (spec §3.4) —
 * that is correct HERE and only here: every event reaching this point has
 * already been schnorr-verified and author-filtered by discoverOwnerBoards
 * (ready-dbf done condition 4). The gate exists for callers holding unverified
 * bytes; re-applying it would put a weaker check in front of a stronger one.
 * Callers that have NOT verified must pass a real trusted set.
 */
export function foldItemSource(opts: ProjectOptions, boardCoord?: string): ItemSource {
  return {
    loadItems(events: NostrEvent[]): UIItem[] {
      const byId = projectItems(events, opts);
      return [...byId.values()].map((f) => toUIItem(f, boardCoord));
    },
  };
}

export const unimplementedItemSource: ItemSource = {
  loadItems(_events: NostrEvent[]): UIItem[] {
    throw new ItemProjectionNotImplementedError();
  },
};
