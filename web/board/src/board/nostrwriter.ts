// nostrwriter.ts — the BoardWriter the shipped board actually uses (ready-b2b).
//
// It is the composition of three things that are each tested on their own:
//   writeevents.ts   what event(s) an intent becomes (pinned by the SAME
//                    testdata/write.vectors.json rd's own writer is pinned by)
//   lib/publish.ts   NIP-07 signing + NIP-01 publish, with the relay's OK frame
//                    treated as the outcome rather than "the socket didn't error"
//   this file        who is allowed to write, in what order, and what the local
//                    snapshot looks like afterwards
//
// TWO REFUSALS, BOTH REQUIRED, NEITHER SUFFICIENT (done condition 6). A write to
// a board this key holds no grant on is refused HERE, client-side, with a plain
// message — and it is ALSO refused by the relay's write policy. The client-side
// check exists because a user who cannot write should be told so instead of
// watching a card snap back; the relay check exists because a client-side check
// is advice, not enforcement. Neither one replaces the other, and this module
// must never be "simplified" by dropping the local check on the grounds that the
// relay enforces it anyway.
//
// THE LOCAL SNAPSHOT IS NOT AUTHORITY. The writer keeps the events it published
// so a second write in the same session sees the first (issue-root ids, and the
// per-item created_at monotonicity of §17.2). It is a cache for building the
// NEXT write, never evidence that a write converged: proving convergence means
// reading what the RELAY retains, from a different reader. That is what the
// live round-trip harness (scripts/live-write-roundtrip.mjs) does, and why it
// uses a clean RD_HOME with a fresh log instead of this snapshot or rd's own
// append-only local log.

import { projectItems, KindCard, KindIssue } from "../lib/fold";
import type { BoardDecryptor, EncryptedBoardSet, SealEnvelope } from "../lib/envelope";
import type { NostrEvent } from "../lib/nostrevent";
import { tagValue } from "../lib/nostrevent";
import {
  hasSigner,
  publishEvents,
  signWith,
  SignerMissingError,
  type Nip07Signer,
  type PublishOptions,
} from "../lib/publish";
import { LEVEL_CONTRIBUTOR } from "../lib/rolegrant";
import type { Item } from "../lib/state";
import type { Status } from "./types";
import { buildWrite, WriteRefusedError, type BuiltEvent, type WriteEnv, type WriteOp } from "./writeevents";
import type { BoardWriter } from "./write";

export interface NostrWriterDeps {
  /** hex pubkey the page is acting as (the extension's identity). */
  signerPubkey: string;
  /** window.nostr. Absent/incapable => every write is refused, read-only. */
  signer?: Nip07Signer;
  /** Board this writer writes to. */
  board: { ownerPubkey: string; boardD: string; title?: string };
  /** Write relays, in preference order. */
  relays: readonly string[];
  /** The board's signed-event snapshot as first fetched (cards, status, issues). */
  snapshot: readonly NostrEvent[];
  /** pubkey -> grant level, from deriveLevels over the board's 39301 grants. */
  grantLevels: Map<string, number>;
  /** True when the board seals free text. Combined with `enc` below: with a
   * seal envelope every write is SEALED; without one every write is refused
   * (writeevents.ts's header — a confidential board's card must never be
   * downgraded to plaintext). */
  confidential?: boolean;
  /** The board's sealing material — the CEK this session holds for the board's
   * current epoch, that epoch, and the LTK when held (ready-191). Derived by
   * main.ts from the BoardKeyring, which builds it from owner-signed grants
   * unwrapped through the NIP-07 signer or from a key-bearing link. Null on a
   * confidential board this reader holds no key for. */
  enc?: SealEnvelope | null;
  /** The board's key material, as an fold-facing decryptor (lib/keyring.ts's
   * BoardKeyring satisfies it). REQUIRED on a confidential board and ignored on
   * a plaintext one.
   *
   * WITHOUT IT EVERY CONFIDENTIAL WRITE IS REFUSED, and for a good reason that
   * is easy to mistake for a bug: this writer projects its own view of the board
   * (items() below) to build the next write from, so a projection told not to
   * decrypt marks every sealed card Redacted — and writeevents.ts's
   * refuseRedacted then refuses, correctly, because republishing a card built
   * from "[encrypted]" placeholders would overwrite the real content with them
   * irreversibly. The fix is to hand the writer the keys it already has, not to
   * soften that guard. Same shape of defect as ready-56b on the read side. */
  decryptor?: BoardDecryptor | null;
  /** The fold's confidentiality gate for this board (main.ts's
   * encryptedBoardsOf). Passed so the writer's own projection quarantines
   * exactly what the page's read projection quarantined — a writer that
   * rendered a card the reader withheld would hand the user a different board to
   * act on than the one they are looking at. */
  encryptedBoards?: EncryptedBoardSet | null;
  /** Called with the refreshed projection after a successful publish. */
  onApplied?: (items: Map<string, Item>) => void;
  publishOptions?: PublishOptions;
}

export class NotAuthorizedError extends Error {
  constructor(message: string) {
    super(message);
    this.name = "NotAuthorizedError";
  }
}

/** driftScope mirrors pkg/sync.DriftScope's item branch: a card/status/issue
 * event belongs to its item's causal chain. */
function driftScopeItem(e: NostrEvent): string {
  if (e.kind === KindCard || e.kind === KindIssue || (e.kind >= 1630 && e.kind <= 1632)) {
    return tagValue(e, "d");
  }
  return "";
}

export class NostrBoardWriter implements BoardWriter {
  private readonly deps: NostrWriterDeps;
  private log: NostrEvent[];
  /** Per-item write queue — see apply(). */
  private readonly chains = new Map<string, Promise<unknown>>();

  constructor(deps: NostrWriterDeps) {
    this.deps = deps;
    this.log = [...deps.snapshot];
  }

  /** whyReadOnly returns a plain sentence when this identity cannot write, or
   * undefined when it can. The UI shows it up front rather than only on the
   * first failed drag. */
  whyReadOnly(): string | undefined {
    // A confidential board is writable HERE only while this session holds the
    // board's key: the write path seals under it (ready-191). Holding no key is
    // not a reason to publish plaintext, it is a reason to stay read-only.
    if (this.deps.confidential && !this.deps.enc) {
      return (
        // THE REMEDY NAMED HERE IS A GRANT, NOT A LINK (ready-191). An earlier
        // revision offered "open the board with a key-bearing link", which is
        // false as advice: a `--with-key` link DOES supply the CEK, but it opens
        // read-only by construction (cek= implies pk= implies `method:
        // "readOnly"` implies no signer), so following it lands the reader on the
        // SIGNER refusal below rather than on a writable board. A grant addressed
        // to a key an extension can sign with is the only thing that makes a
        // confidential board writable here.
        "This board seals its free text (confidential) and no read key for it reached this session, so " +
        "the browser cannot seal what it writes. Editing it here would publish the title and context in " +
        "the clear, so writes are disabled — use the rd CLI, or ask the board owner to grant this key " +
        "(rd grant <npub>) and sign in with a NIP-07 extension."
      );
    }
    if (!this.deps.signer || !hasSigner({ nostr: this.deps.signer as unknown as Window["nostr"] })) {
      return (
        "Read-only: no NIP-07 extension is available to sign. The board never accepts a secret key — " +
        "install and unlock a nostr extension to make changes."
      );
    }
    const level = this.deps.grantLevels.get(this.deps.signerPubkey);
    if (level === undefined || level < LEVEL_CONTRIBUTOR) {
      return (
        `Read-only: this key holds no write grant on board ${this.deps.board.boardD}. Ask the board owner ` +
        `to grant it access (rd grant <npub>); the relay will refuse the write too.`
      );
    }
    return undefined;
  }

  /** items projects the writer's current view (snapshot + everything it has
   * published this session). */
  items(): Map<string, Item> {
    return projectItems(this.log, {
      trusted: null,
      maintainers: null,
      pinnedBoard: `30301:${this.deps.board.ownerPubkey}:${this.deps.board.boardD}`,
      decryptor: this.deps.decryptor ?? null,
      encryptedBoards: this.deps.encryptedBoards ?? null,
    });
  }

  private issueEventIds(): Map<string, string> {
    const out = new Map<string, string>();
    for (const e of this.log) {
      if (e.kind !== KindIssue) continue;
      const d = tagValue(e, "d");
      if (d !== "" && !out.has(d)) out.set(d, e.id);
    }
    return out;
  }

  /** nextCreatedAt mirrors cmd/rd's nostrNextCreatedAt, SCOPED to one item's
   * causal chain (§17.2, ready-be1): strictly after the newest event already in
   * that chain, so two writes to the same card inside one wall-clock second
   * still resolve in intent order under the read-side (created_at, id)
   * tiebreak — and an unrelated burst never drifts this card into the future. */
  private nextCreatedAt(itemId: string): number {
    const now = Math.floor(Date.now() / 1000);
    let max = 0;
    for (const e of this.log) {
      if (driftScopeItem(e) !== itemId) continue;
      if (e.created_at > max) max = e.created_at;
    }
    return max + 1 > now ? max + 1 : now;
  }

  private env(itemId: string): WriteEnv {
    return {
      signer: this.deps.signerPubkey,
      boardAuthor: this.deps.board.ownerPubkey,
      boardD: this.deps.board.boardD,
      boardTitle: this.deps.board.title ?? this.deps.board.boardD,
      items: this.items(),
      issueEventIds: this.issueEventIds(),
      createdAt: this.nextCreatedAt(itemId),
      confidential: this.deps.confidential,
      enc: this.deps.enc ?? null,
    };
  }

  /**
   * apply is the single write path: authorize, build, sign, publish, absorb.
   * Every step before the publish is local and cheap; nothing is absorbed into
   * the snapshot unless the relay accepted it.
   *
   * WRITES TO ONE ITEM ARE SERIALIZED (§17.2/§17.3, ready-b2b done condition 5).
   * created_at is stamped strictly after the newest event ALREADY IN THIS ITEM'S
   * CHAIN, and that chain only learns about a write once the relay has accepted
   * it. Two writes fired back to back — a double-click, a drag immediately
   * followed by a re-prioritise — would otherwise both read the same "newest"
   * and be stamped with the SAME created_at, at which point the read side breaks
   * the tie by lowest event id: a coin flip decides which intent survives, and
   * the user's later intent can silently lose. Queuing per item makes the second
   * write see the first, so it is stamped strictly later and genuinely wins.
   *
   * The queue is per ITEM, not global: writes to different cards are independent
   * causal chains (ready-be1) and must not wait on each other.
   */
  async apply(itemId: string, op: WriteOp): Promise<void> {
    const prior = this.chains.get(itemId) ?? Promise.resolve();
    const mine = prior.catch(() => undefined).then(() => this.applyNow(itemId, op));
    // The stored link must not reject unhandled; callers await `mine` itself.
    this.chains.set(
      itemId,
      mine.catch(() => undefined),
    );
    return mine;
  }

  private async applyNow(itemId: string, op: WriteOp): Promise<void> {
    const readOnly = this.whyReadOnly();
    if (readOnly) throw new NotAuthorizedError(readOnly);
    const signer = this.deps.signer;
    if (!signer) throw new SignerMissingError();

    const built: BuiltEvent[] = buildWrite(this.env(itemId), op);
    const signed: NostrEvent[] = [];
    for (const b of built) signed.push(await signWith(signer, b));

    await publishEvents(this.deps.relays, signed, this.deps.publishOptions);

    this.log = [...this.log, ...signed];
    this.deps.onApplied?.(this.items());
  }

  // ── the seven board operations ────────────────────────────────────────────

  moveStatus(itemId: string, toStatus: Status): Promise<void> {
    return this.apply(itemId, { op: "update_status", itemId, statusTo: toStatus });
  }

  resolveGate(itemId: string, approve: boolean, reason?: string): Promise<void> {
    return this.apply(itemId, {
      op: approve ? "gate_approve" : "gate_reject",
      itemId,
      reason: reason ?? "",
    });
  }

  claim(itemId: string, reason?: string): Promise<void> {
    return this.apply(itemId, { op: "claim", itemId, reason: reason ?? "" });
  }

  close(itemId: string, resolution?: string, reason?: string): Promise<void> {
    return this.apply(itemId, { op: "close", itemId, resolution: resolution ?? "done", reason: reason ?? "" });
  }

  setTitle(itemId: string, title: string): Promise<void> {
    if (title.trim() === "") {
      return Promise.reject(new WriteRefusedError("empty_title", "a title cannot be empty"));
    }
    return this.apply(itemId, { op: "update_fields", itemId, title });
  }

  setPriority(itemId: string, priority: string): Promise<void> {
    return this.apply(itemId, { op: "update_fields", itemId, priority });
  }

  setLabel(itemId: string, label: string, present: boolean): Promise<void> {
    return this.apply(itemId, present ? { op: "label_add", itemId, label } : { op: "label_remove", itemId, label });
  }
}
