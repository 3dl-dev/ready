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
// CONFIDENTIAL BOARDS ARE SEALED, AND STILL REFUSED WHEN THEY CANNOT BE
// (ready-191). rd's writer seals free text under the board CEK on a confidential
// board (pkg/sync/envelope.go); `rd init` DEFAULTS to confidential, so a page
// that cannot seal is read-only on every board created the normal way. This
// module now seals: WriteEnv.enc carries the injected {CEK, epoch, LTK} and the
// builders below take exactly the branches BuildCardEvent /
// BuildStatusEventWithIssueRoot / ensureIssueEvent take on the Go side —
//
//   - the clear `title` and `waiting_on` tags DISAPPEAR (their values move into
//     the sealed Content blob);
//   - `l` labels become owner-keyed HMAC tokens under the LTK, or — with no LTK
//     held — are OMITTED entirely rather than emitted in the clear;
//   - Content becomes base64(nonce ‖ AEAD) over {title,context,waiting_on,labels}
//     for a card, and over {reason} for a status event;
//   - the two clear markers ["enc","1"] and ["cek_epoch","<n>"] are appended;
//   - the NIP-34 kind:1621 issue event is SUPPRESSED, exactly as
//     Publisher.ensureIssueEvent suppresses it — its clear `subject` tag and
//     Content are the item's title and context, i.e. the two most sensitive
//     fields the envelope exists to seal.
//
// THE REFUSAL REMAINS, and is now precisely scoped: a confidential board with NO
// enc envelope (the page holds no CEK — no grant, no key-bearing link) is still
// refused outright. Downgrading such a card to plaintext would leak the title
// and context AND silently diverge from every rd-authored card on the board.
// See WriteRefusedError and the `confidential` refusal below. Do not "simplify"
// that branch away: `enc` being present is the ONLY thing that makes writing
// here safe.

import { computeEventId } from "../lib/nostrevent";
import {
  encMarkerTags,
  labelToken,
  sealCardPayload,
  sealStatusPayload,
  type SealEnvelope,
} from "../lib/envelope";
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
  /** true when the board seals free text. Without `enc` every write is refused
   * (see header); with `enc` every write is SEALED. */
  confidential?: boolean;
  /** the injected sealing material for a confidential board — the CEK this page
   * holds for the board's current epoch, that epoch, and the LTK when held.
   * Absent/null on a plaintext board, and absent on a confidential board this
   * reader holds no key for (which is what the refusal is for). */
  enc?: SealEnvelope | null;
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

/** buildCardEvent mirrors BuildCardEvent tag for tag, in order: d, created?,
 * title, a(board), s, rank, priority, itype, p(assignee), i*(deps), gate,
 * waiting_type, waiting_on, l*(labels), eta, level, for, parent, due. Content is
 * the item's context.
 *
 * CONFIDENTIAL MODE (env.enc set) takes the same three deviations BuildCardEvent
 * takes and no others: the clear `title` tag is dropped, the clear `waiting_on`
 * tag is dropped, and each `l` label becomes an LTK HMAC token (or is dropped
 * entirely when no LTK is held — never emitted in the clear). Content becomes
 * the sealed {title,context,waiting_on,labels} blob and the two clear markers
 * are APPENDED LAST, after `due`, exactly where encMarkerTags lands them in Go.
 * Every other routing tag is byte-identical to the plaintext branch — that is
 * the whole point of the envelope: relay-indexed routing stays plaintext. */
export function buildCardEvent(env: WriteEnv, item: Item): BuiltEvent {
  if (item.id === "") throw new WriteRefusedError("empty_item_id", "card event: empty item id");
  const enc = env.enc ?? null;
  const tags: string[][] = [["d", item.id]];
  // TRUE CREATION TIME (ready-4ec): carry the item's own creation time forward
  // as a "created" tag so a browser-authored republish does not reset it —
  // mirrors CardSpecFromItem's itemCreatedAtSecs (pkg/sync/nostrmigrate.go).
  // Zero (genesis / unknown) emits no tag; the fold then falls back to this
  // event's own created_at, correct for exactly the one card a fresh item has
  // never republished yet.
  if (item.created_at > 0n) tags.push(["created", (item.created_at / 1_000_000_000n).toString(10)]);
  if (enc === null) tags.push(["title", item.title ?? ""]);
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
  if (item.waiting_on && enc === null) tags.push(["waiting_on", item.waiting_on]);
  for (const label of item.labels ?? []) {
    if (label === "") continue;
    if (enc === null) {
      tags.push(["l", label]);
    } else if (enc.ltk) {
      // Confidential + tokenization: the clear l value is an owner-keyed HMAC
      // token (equality-filterable at a relay, not readable); the plaintext
      // label rides inside the sealed Content for member-side rendering.
      tags.push(["l", labelToken(enc.ltk, label)]);
    }
    // Confidential with NO LTK: emit NO l tag. A plaintext label tag would leak
    // the label value, and rd filters labels client-side off the decrypted
    // payload anyway — so there is nothing to lose and a leak to avoid.
  }
  if (item.eta) tags.push(["eta", item.eta]);
  if (item.level) tags.push(["level", item.level]);
  if (item.for) tags.push(["for", item.for]);
  if (item.parent_id) tags.push(["parent", item.parent_id]);
  if (item.due) tags.push(["due", item.due]);
  let content = item.context ?? "";
  if (enc !== null) {
    content = sealCardPayload(enc, {
      title: item.title ?? "",
      context: item.context ?? "",
      waitingOn: item.waiting_on ?? "",
      labels: (item.labels ?? []).filter((l) => l !== ""),
    });
    tags.push(...encMarkerTags(enc));
  }
  return withId({
    pubkey: env.signer,
    created_at: env.createdAt,
    kind: KindCard,
    tags,
    content,
  });
}

/** buildIssueEvent mirrors BuildIssueEvent (NIP-34 kind:1621 issue root, minted
 * once per item).
 *
 * NEVER CALLED ON A CONFIDENTIAL BOARD. Its `subject` tag is the item's title in
 * the clear and its Content is the item's context in the clear — the two fields
 * the envelope seals. Publisher.ensureIssueEvent returns ("", nil, nil) for
 * `card.Enc != nil` for exactly that reason, and publishStatusChange /
 * buildFullCreate below suppress it on the same condition. A generic NIP-34
 * client cannot read a confidential board's cards anyway, so the interop anchor
 * buys nothing there and costs everything. */
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
 * filter matches status events too).
 *
 * CONFIDENTIAL MODE seals the close/change reason — free text, and on a status
 * event the ONLY free text — into Content as {"reason":...} and appends the two
 * clear markers. Their POSITION is normative and non-obvious: Go appends them
 * inside BuildStatusEventWithIssueRoot BEFORE the issue-root "e" tag and BEFORE
 * the board "a" tag, so the order is a(card), d, status, e(card), enc,
 * cek_epoch, [e(issue,root)], a(board). On a confidential board the issue anchor
 * is suppressed, so what actually ships is that list without the e(issue). */
export function buildStatusEvent(
  env: WriteEnv,
  args: { itemId: string; status: string; cardEventId: string; issueEventId: string; reason: string },
): BuiltEvent {
  if (args.itemId === "") throw new WriteRefusedError("empty_item_id", "status event: empty item id");
  if (args.status === "") throw new WriteRefusedError("empty_status", "status event: empty status");
  const enc = env.enc ?? null;
  const tags: string[][] = [
    ["a", cardCoord(env.signer, args.itemId)],
    ["d", args.itemId],
    ["status", args.status],
  ];
  if (args.cardEventId !== "") tags.push(["e", args.cardEventId]);
  let content = args.reason;
  if (enc !== null) {
    content = sealStatusPayload(enc, args.reason);
    tags.push(...encMarkerTags(enc));
  }
  if (args.issueEventId !== "") tags.push(["e", args.issueEventId, "", "root"]);
  if (env.boardD !== "") tags.push(["a", boardCoord(env.boardAuthor || env.signer, env.boardD)]);
  return withId({
    pubkey: env.signer,
    created_at: env.createdAt,
    kind: statusKindFor(args.status),
    tags,
    content,
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
  // CONFIDENTIAL: no issue event, and no issue anchor — ensureIssueEvent returns
  // ("", nil, nil) for card.Enc != nil WITHOUT even looking one up, so a board
  // that carries issue events from a plaintext era does not get its status
  // events re-anchored to them either. Mirror that: do not consult
  // env.issueEventIds at all.
  let issueId = "";
  if (!env.enc) {
    issueId = env.issueEventIds.get(item.id) ?? "";
    if (issueId === "") {
      const issue = buildIssueEvent(env, item);
      issueId = issue.id;
      events.push(issue);
    }
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
  // CONFIDENTIAL: the kind:1621 issue root is suppressed (see buildIssueEvent's
  // doc), so the status event carries no issue-root anchor.
  let issueId = "";
  if (!env.enc) {
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
  // FAIL CLOSED WHEN THERE IS NOTHING TO SEAL WITH. A confidential board with an
  // enc envelope is written SEALED by the builders above; a confidential board
  // WITHOUT one means this page holds no CEK for it (no grant addressed to this
  // key, no key-bearing link), and the only card it could produce is a plaintext
  // one. That is a leak of the title and context, and a silent divergence from
  // every rd-authored card on the board. Refuse instead — the same refusal
  // ready-b2b shipped, now narrowed to the case that still warrants it.
  if (env.confidential && !env.enc) {
    throw new WriteRefusedError(
      "confidential",
      // See NostrBoardWriter.whyReadOnly for why the remedy is a GRANT and not a
      // key-bearing link: a link supplies the CEK but opens read-only, so it
      // cannot make this board writable.
      "this board seals its free text (confidential board) and no read key for it reached this " +
        "session, so the browser cannot seal a card — writing here would publish the title and " +
        "context in the clear. Use the rd CLI for this board, or ask its owner to grant this key " +
        "(rd grant <npub>).",
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
