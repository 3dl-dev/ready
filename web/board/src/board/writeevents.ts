// writeevents.ts — the browser's WRITE-side event construction (ready-b2b).
//
// This is the TS counterpart of pkg/sync/nostrwire.go's BuildBoardEvent /
// BuildCardEvent / BuildIssueEvent / BuildStatusEventWithIssueRoot plus
// cmd/rd/nostrwrite.go's run*Nostr command bodies. It is PURE: it takes the
// item projection the fold already produced (lib/fold.ts), an intent, and the
// board context, and returns UNSIGNED events. Signing (window.nostr.signEvent
// only — the secret key never enters the page) and publishing live in
// nostrwriter.ts / lib/publish.ts.
//
// WHY PURE, AND WHY IT COMPUTES ITS OWN EVENT IDS: a NIP-34 status event
// anchors to the 30302 card published in the SAME operation by that card's
// concrete event id ("e" tag). A nostr event id is sha256 over the canonical
// [0,pubkey,created_at,kind,tags,content] serialization — it does NOT depend on
// the signature — so the anchor can be computed before anything is signed, and
// the whole builder stays testable without a signer. nostrwriter.ts re-checks
// that the signer returned exactly the id we computed, so an extension that
// silently rewrote created_at (or anything else) fails the write loudly instead
// of publishing a status event anchored to a card that does not exist.
//
// TAG ORDER IS NORMATIVE (board-fold-spec.md §19.5). Every tag below is emitted
// in the exact order BuildCardEvent emits it; testdata/write.vectors.json — the
// same file cmd/rd/writevectors_test.go replays through rd's REAL writer — pins
// it byte-for-byte, and writeevents.vectors.test.ts replays it through this
// module. A divergence here is precisely the silent client/rd disagreement
// ready-b2b exists to catch: do not "fix" a vector to match this file.
//
// CONFIDENTIAL BOARDS ARE REFUSED, NOT DOWNGRADED. rd's writer seals free text
// under the board CEK on a confidential board (pkg/sync/envelope.go). This
// module has no seal path (lib/envelope.ts is open-only), so publishing a card
// here for a confidential board would emit the title/context in the CLEAR — a
// data leak, and a silent divergence from every rd-authored card on that board.
// buildWrite therefore refuses when the board is confidential. See
// WriteRefusedError and the `confidential` refusal below.

import { computeEventId } from "../lib/nostrevent";
import {
  KindBoard,
  KindCard,
  KindIssue,
  KindStatusClosed,
  KindStatusOpen,
  KindStatusResolved,
} from "../lib/fold";
import type { Item } from "../lib/state";
import {
  StatusActive,
  StatusBlocked,
  StatusCancelled,
  StatusDone,
  StatusFailed,
  StatusInbox,
  StatusWaiting,
} from "../lib/state";

/** An event with everything the id derivation needs, but no id and no sig. */
export interface UnsignedEvent {
  pubkey: string;
  created_at: number;
  kind: number;
  tags: string[][];
  content: string;
}

/** UnsignedEvent plus the id it will carry once signed (id is sig-independent). */
export interface BuiltEvent extends UnsignedEvent {
  id: string;
}

/** WriteRefusedError is a CLIENT-SIDE refusal: the write never reaches a signer
 * or a relay. `code` lets the UI distinguish "you cannot do this" (no grant,
 * terminal item) from a relay rejection, which arrives later and is a different
 * message. The messages deliberately mirror the rd CLI's own wording so the two
 * surfaces do not describe the same refusal differently. */
export class WriteRefusedError extends Error {
  readonly code: string;
  constructor(code: string, message: string) {
    super(message);
    this.name = "WriteRefusedError";
    this.code = code;
  }
}

/** The board/signer context a write is performed in. */
export interface WriteEnv {
  /** hex pubkey of the key that will sign (the NIP-07 extension's identity). */
  signer: string;
  /** hex pubkey that authored the 30301 board the card joins (BP-4). */
  boardAuthor: string;
  /** the board's "d" value. */
  boardD: string;
  /** the board's title — only used by the create op's board republish. */
  boardTitle?: string;
  /** the current projection (fold.ts's projectItems output). */
  items: Map<string, Item>;
  /** itemId -> already-published kind:1621 issue-root event id, from the log. */
  issueEventIds: Map<string, string>;
  /** unix SECONDS for every event in this write (NIP-01). */
  createdAt: number;
  /** true when the board seals free text. Every write is refused (see header). */
  confidential?: boolean;
}

export type WriteOp =
  | { op: "create"; id: string; title: string; context?: string; type?: string; priority?: string }
  | { op: "claim"; itemId: string; reason?: string }
  | { op: "close"; itemId: string; resolution?: string; reason?: string }
  | { op: "delegate"; itemId: string; to: string; reason?: string }
  | { op: "gate_open"; itemId: string; gateType: string; description?: string }
  | { op: "gate_approve"; itemId: string; reason?: string }
  | { op: "gate_reject"; itemId: string; reason?: string }
  | { op: "dep_add"; itemId: string; blockerId: string }
  | { op: "dep_remove"; itemId: string; blockerId: string }
  | { op: "label_add"; itemId: string; label: string }
  | { op: "label_remove"; itemId: string; label: string }
  | {
      op: "update_fields";
      itemId: string;
      title?: string;
      context?: string;
      priority?: string;
      eta?: string;
      due?: string;
      level?: string;
      parentId?: string;
    }
  | {
      op: "update_status";
      itemId: string;
      statusTo: string;
      waitingOn?: string;
      waitingType?: string;
      note?: string;
    };

// ── coordinates ─────────────────────────────────────────────────────────────

export function boardCoord(owner: string, boardD: string): string {
  return `${KindBoard}:${owner}:${boardD}`;
}

export function cardCoord(author: string, itemId: string): string {
  return `${KindCard}:${author}:${itemId}`;
}

/** statusKindFor mirrors nostrwire.go's statusKindFor: the exact rd status
 * still rides in the "status" tag, the kind only carries NIP-34's
 * open/resolved/closed family. */
export function statusKindFor(rdStatus: string): number {
  if (rdStatus === StatusDone) return KindStatusResolved;
  if (rdStatus === StatusCancelled || rdStatus === StatusFailed) return KindStatusClosed;
  return KindStatusOpen;
}

const TERMINAL = new Set<string>([StatusDone, StatusCancelled, StatusFailed]);

function isTerminal(item: Item): boolean {
  return TERMINAL.has(item.status);
}

/** nonDerivedStatus mirrors pkg/sync.NonDerivedStatus (ready-500): "blocked" is
 * DERIVED every fold from a live blocker and is NEVER an authored write target,
 * so a republish of an item the fold marked blocked must carry the item's last
 * authoritative non-blocked status instead — walking back past any number of
 * already-burned-in "blocked" history entries, and defaulting to inbox when
 * there is nothing left to recover. */
export function nonDerivedStatus(item: Item): string {
  if (item.status !== StatusBlocked) return item.status;
  const history = item.history ?? [];
  for (let i = history.length - 1; i >= 0; i--) {
    if (history[i].to_status !== StatusBlocked) return history[i].to_status;
  }
  return StatusInbox;
}

// ── builders (mirror pkg/sync/nostrwire.go exactly) ─────────────────────────

function withId(e: UnsignedEvent): BuiltEvent {
  return { ...e, id: computeEventId(e) };
}

/** buildBoardEvent mirrors BuildBoardEvent: d, title, then one "p" per
 * maintainer. */
export function buildBoardEvent(
  env: WriteEnv,
  spec: { boardD: string; title: string; maintainers: string[] },
): BuiltEvent {
  const tags: string[][] = [
    ["d", spec.boardD],
    ["title", spec.title],
  ];
  for (const m of spec.maintainers) if (m !== "") tags.push(["p", m]);
  return withId({
    pubkey: env.signer,
    created_at: env.createdAt,
    kind: KindBoard,
    tags,
    content: "",
  });
}

/** buildCardEvent mirrors BuildCardEvent's plaintext branch, tag for tag, in
 * order: d, title, a(board), s, rank, priority, itype, p(assignee), i*(deps),
 * gate, waiting_type, waiting_on, l*(labels), eta, level, for, parent, due.
 * Content is the item's context. */
export function buildCardEvent(env: WriteEnv, item: Item): BuiltEvent {
  if (item.id === "") throw new WriteRefusedError("empty_item_id", "card event: empty item id");
  const tags: string[][] = [["d", item.id]];
  tags.push(["title", item.title ?? ""]);
  if (env.boardD !== "") tags.push(["a", boardCoord(env.boardAuthor || env.signer, env.boardD)]);
  const status = nonDerivedStatus(item);
  if (status !== "") tags.push(["s", status]);
  if (item.priority) {
    tags.push(["rank", item.priority]);
    tags.push(["priority", item.priority]);
  }
  if (item.type) tags.push(["itype", item.type]);
  if (item.by) tags.push(["p", item.by]);
  for (const dep of item.blocked_by ?? []) if (dep !== "") tags.push(["i", dep]);
  if (item.gate) tags.push(["gate", item.gate]);
  if (item.waiting_type) tags.push(["waiting_type", item.waiting_type]);
  if (item.waiting_on) tags.push(["waiting_on", item.waiting_on]);
  for (const label of item.labels ?? []) if (label !== "") tags.push(["l", label]);
  if (item.eta) tags.push(["eta", item.eta]);
  if (item.level) tags.push(["level", item.level]);
  if (item.for) tags.push(["for", item.for]);
  if (item.parent_id) tags.push(["parent", item.parent_id]);
  if (item.due) tags.push(["due", item.due]);
  return withId({
    pubkey: env.signer,
    created_at: env.createdAt,
    kind: KindCard,
    tags,
    content: item.context ?? "",
  });
}

/** buildIssueEvent mirrors BuildIssueEvent (NIP-34 kind:1621 issue root, minted
 * once per item). */
export function buildIssueEvent(env: WriteEnv, item: Item): BuiltEvent {
  return withId({
    pubkey: env.signer,
    created_at: env.createdAt,
    kind: KindIssue,
    tags: [
      ["d", item.id],
      ["subject", item.title ?? ""],
    ],
    content: item.context ?? "",
  });
}

/** buildStatusEvent mirrors BuildStatusEventWithIssueRoot: the card-coordinate
 * "a" anchor FIRST (rd's projection reads only the first match), then d, status,
 * the card's concrete "e" id, the NIP-10 root-marked issue "e" id, and finally
 * the board-membership "a" coordinate (ready-7ec, so a board-scoped negentropy
 * filter matches status events too). */
export function buildStatusEvent(
  env: WriteEnv,
  args: { itemId: string; status: string; cardEventId: string; issueEventId: string; reason: string },
): BuiltEvent {
  if (args.itemId === "") throw new WriteRefusedError("empty_item_id", "status event: empty item id");
  if (args.status === "") throw new WriteRefusedError("empty_status", "status event: empty status");
  const tags: string[][] = [
    ["a", cardCoord(env.signer, args.itemId)],
    ["d", args.itemId],
    ["status", args.status],
  ];
  if (args.cardEventId !== "") tags.push(["e", args.cardEventId]);
  if (args.issueEventId !== "") tags.push(["e", args.issueEventId, "", "root"]);
  if (env.boardD !== "") tags.push(["a", boardCoord(env.boardAuthor || env.signer, env.boardD)]);
  return withId({
    pubkey: env.signer,
    created_at: env.createdAt,
    kind: statusKindFor(args.status),
    tags,
    content: args.reason,
  });
}

// ── op composition (mirrors Publisher.PublishItem / PublishStatusChange /
//    PublishCardEdit and the run*Nostr bodies) ──────────────────────────────

/** publishCardEdit: a card-only republish. No status event — the hybrid model's
 * invariant that editing the addressable card never adds to, or erases,
 * history. */
function publishCardEdit(env: WriteEnv, item: Item): BuiltEvent[] {
  return [buildCardEvent(env, item)];
}

/** publishStatusChange: refreshed card + (issue root, once per item) + status
 * event, in exactly Publisher.PublishStatusChange's order. */
function publishStatusChange(env: WriteEnv, item: Item, reason: string): BuiltEvent[] {
  const card = buildCardEvent(env, item);
  const events: BuiltEvent[] = [card];
  let issueId = env.issueEventIds.get(item.id) ?? "";
  if (issueId === "") {
    const issue = buildIssueEvent(env, item);
    issueId = issue.id;
    events.push(issue);
  }
  events.push(
    buildStatusEvent(env, {
      itemId: item.id,
      status: nonDerivedStatus(item),
      cardEventId: card.id,
      issueEventId: issueId,
      reason,
    }),
  );
  return events;
}

/**
 * buildFullCreate mirrors cmd/rd's publishItemFullCreateNostr →
 * Publisher.PublishItem: the owner's 30301 board (re-published on every create
 * when the signer IS the board author, §16.6), the 30302 card materializing the
 * WHOLE item, the NIP-34 kind:1621 issue root (minted once per item), and the
 * kind:163x status event anchored to both.
 *
 * Exported because it is also how the conformance harness seeds a vector's
 * pre-existing item — the same reason cmd/rd/writevectors_test.go's seedItem
 * calls the REAL create writer instead of injecting a synthetic card.
 */
export function buildFullCreate(env: WriteEnv, item: Item): BuiltEvent[] {
  const events: BuiltEvent[] = [];
  if (env.signer === (env.boardAuthor || env.signer)) {
    events.push(
      buildBoardEvent(env, {
        boardD: env.boardD,
        title: env.boardTitle ?? env.boardD,
        maintainers: [env.boardAuthor || env.signer],
      }),
    );
  }
  const card = buildCardEvent(env, item);
  events.push(card);
  const issue = buildIssueEvent(env, item);
  events.push(issue);
  events.push(
    buildStatusEvent(env, {
      itemId: item.id,
      status: nonDerivedStatus(item),
      cardEventId: card.id,
      issueEventId: issue.id,
      reason: "",
    }),
  );
  return events;
}

function requireItem(env: WriteEnv, itemId: string): Item {
  const it = env.items.get(itemId);
  if (it) return { ...it };
  // Unique-prefix resolution, mirroring nostrResolveItem's fallback.
  let match: Item | undefined;
  for (const [id, candidate] of env.items) {
    if (id.startsWith(itemId)) {
      if (match) {
        throw new WriteRefusedError(
          "ambiguous_item",
          `item prefix ${JSON.stringify(itemId)} is ambiguous in the nostr projection`,
        );
      }
      match = candidate;
    }
  }
  if (!match) {
    throw new WriteRefusedError(
      "unknown_item",
      `item ${JSON.stringify(itemId)} not found in the nostr projection`,
    );
  }
  return { ...match };
}

function refuseRedacted(item: Item): void {
  // Mirrors cmd/rd/confidential_guard.go's refuseRedactedRepublish. A card write
  // rebuilds the WHOLE card from the projected item, so republishing an item
  // this reader could not decrypt would re-seal the "[encrypted]" placeholder as
  // the item's real content and destroy it irreversibly. It has happened.
  if (item.redacted) {
    throw new WriteRefusedError(
      "redacted",
      `item ${item.id}'s confidential content could not be decrypted here — refusing to republish a ` +
        `placeholder over the real content`,
    );
  }
}

function appendUnique(arr: string[] | undefined, v: string): string[] {
  const out = arr ? arr.slice() : [];
  if (!out.includes(v)) out.push(v);
  return out;
}

function removeAll(arr: string[] | undefined, v: string): string[] {
  return (arr ?? []).filter((x) => x !== v);
}

/** closeResolutionToStatus mirrors cmd/rd/nostr.go's closeResolutionToStatus. */
function closeResolutionToStatus(resolution: string): string {
  if (resolution === StatusCancelled) return StatusCancelled;
  if (resolution === StatusFailed) return StatusFailed;
  return StatusDone;
}

function hasPendingGate(item: Item): boolean {
  return (item.gate_msg_id ?? "") !== "" || (item.gate ?? "") !== "" || item.waiting_type === "gate";
}

/**
 * buildWrite turns one user intent into the exact event sequence rd's writer
 * would append for the same command, unsigned and id-stamped.
 *
 * It throws WriteRefusedError for every case rd itself refuses (terminal item,
 * unknown item/blocker, no pending gate, an explicit `blocked` status target),
 * BEFORE anything is signed or published — a refusal must never partially
 * publish, which is exactly what write.vectors.json's four `expect_error`
 * vectors assert.
 */
export function buildWrite(env: WriteEnv, op: WriteOp): BuiltEvent[] {
  if (env.confidential) {
    throw new WriteRefusedError(
      "confidential",
      "this board seals its free text (confidential board) and the browser cannot seal a card yet — " +
        "writing here would publish the title and context in the clear. Use the rd CLI for this board.",
    );
  }
  switch (op.op) {
    case "create": {
      if (env.items.has(op.id)) {
        throw new WriteRefusedError("duplicate_item", `item ${op.id} already exists`);
      }
      const item: Item = {
        id: op.id,
        msg_id: "",
        title: op.title,
        context: op.context ?? "",
        type: op.type ?? "",
        priority: op.priority ?? "",
        status: StatusInbox,
        for: env.signer,
        created_at: 0n,
        updated_at: 0n,
      };
      return buildFullCreate(env, item);
    }
    case "claim": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      if (isTerminal(item)) {
        throw new WriteRefusedError("terminal", `item ${item.id} is already ${item.status}`);
      }
      item.status = StatusActive;
      item.by = env.signer;
      return publishStatusChange(env, item, op.reason ?? "");
    }
    case "close": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      if (isTerminal(item)) {
        throw new WriteRefusedError("terminal", `item ${item.id} is already ${item.status}`);
      }
      item.status = closeResolutionToStatus(op.resolution ?? StatusDone);
      const events = publishStatusChange(env, item, op.reason ?? "");
      // Implicit-unblock parity (publishImplicitUnblockNostrNative): every item
      // this one was blocking gets its card republished. `blocked` itself is
      // derived, so this changes no tag — it exists so the other cards' latest
      // -wins materialization is refreshed exactly as the CLI refreshes it.
      for (const blockedId of item.blocks ?? []) {
        const other = env.items.get(blockedId);
        if (!other || other.redacted) continue;
        events.push(buildCardEvent(env, other));
      }
      return events;
    }
    case "delegate": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      if (isTerminal(item)) {
        throw new WriteRefusedError("terminal", `item ${item.id} is already ${item.status}`);
      }
      item.by = op.to;
      return publishStatusChange(env, item, op.reason ?? "");
    }
    case "gate_open": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      if (isTerminal(item)) {
        throw new WriteRefusedError("terminal", `item ${item.id} is already ${item.status}`);
      }
      item.status = StatusWaiting;
      item.gate = op.gateType;
      item.waiting_type = "gate";
      item.waiting_on = op.description ?? "";
      return publishStatusChange(env, item, op.description ?? "");
    }
    case "gate_approve":
    case "gate_reject": {
      const verb = op.op === "gate_approve" ? "approve" : "reject";
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      if (!hasPendingGate(item)) {
        throw new WriteRefusedError(
          "no_gate",
          `item ${item.id} has no pending gate to ${verb}`,
        );
      }
      if (item.status !== StatusWaiting && item.status !== StatusBlocked) {
        throw new WriteRefusedError(
          "not_waiting",
          `item ${item.id} is not waiting or blocked (status=${item.status})`,
        );
      }
      if (op.op === "gate_approve") {
        item.status = StatusActive;
        item.gate = undefined;
        item.waiting_type = undefined;
        item.waiting_on = undefined;
        item.waiting_since = undefined;
        item.gate_msg_id = undefined;
      } else {
        // A rejected gate stays OPEN, and status is forced to waiting so a
        // blocked-and-gated reject can never burn in a derived "blocked".
        item.status = StatusWaiting;
      }
      return publishStatusChange(env, item, op.reason ?? "");
    }
    case "dep_add": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      const blocker = requireItem(env, op.blockerId);
      item.blocked_by = appendUnique(item.blocked_by, blocker.id);
      return publishCardEdit(env, item);
    }
    case "dep_remove": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      const blocker = requireItem(env, op.blockerId);
      item.blocked_by = removeAll(item.blocked_by, blocker.id);
      return publishCardEdit(env, item);
    }
    case "label_add": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      item.labels = appendUnique(item.labels, op.label);
      return publishCardEdit(env, item);
    }
    case "label_remove": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      item.labels = removeAll(item.labels, op.label);
      return publishCardEdit(env, item);
    }
    case "update_fields": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      if (isTerminal(item)) {
        throw new WriteRefusedError("terminal", `item ${item.id} is already ${item.status}`);
      }
      if (op.title) item.title = op.title;
      if (op.context) item.context = op.context;
      if (op.priority) item.priority = op.priority;
      if (op.eta) item.eta = op.eta;
      if (op.due) item.due = op.due;
      if (op.level) item.level = op.level;
      if (op.parentId) {
        if (!env.items.has(op.parentId)) {
          throw new WriteRefusedError(
            "unknown_item",
            `item ${JSON.stringify(op.parentId)} not found in the nostr projection`,
          );
        }
        item.parent_id = op.parentId;
      }
      return publishCardEdit(env, item);
    }
    case "update_status": {
      const item = requireItem(env, op.itemId);
      refuseRedacted(item);
      if (op.statusTo === StatusBlocked) {
        throw new WriteRefusedError(
          "derived_status",
          `status "blocked" cannot be set directly on ${item.id}: it is derived from dependencies, ` +
            `not a write target (block it with a dependency, or close the blocker to unblock)`,
        );
      }
      item.status = op.statusTo;
      if (op.waitingOn) item.waiting_on = op.waitingOn;
      if (op.waitingType) item.waiting_type = op.waitingType;
      return publishStatusChange(env, item, op.note ?? "");
    }
  }
}
