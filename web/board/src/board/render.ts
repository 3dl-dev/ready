// The board workspace: header + gate rail + left tree + swimlaned columns +
// detail pane.
//
// NORMATIVE REFERENCE: docs/design/board-prototype.html — the artifact the
// owner approved on 2026-07-27. Read THAT, not a prose paraphrase of it, before
// changing anything here. ready-56b exists because the first build of this file
// was written from a paraphrase: it shipped a page with no header, no tally, no
// search, no filter chips, no sort note, no card cap, no footer, a rainbow
// eight-colour epic palette rendered as full-saturation blocks, and a dark-only
// ground that was not even the design's dark ground. Every constant below that
// carries a hex value or a px value came out of the prototype's :root or its
// render functions; board.css mirrors its <style> block rule for rule.
//
// Layout pattern (two/three-pane list+detail, resizable/closeable detail track)
// is vendored from moot's Feed.tsx / PostRow.tsx / CommentColumn.tsx
// (github.com/3dl-dev/moot, MIT, same owner) — ported as a LAYOUT PATTERN, not a
// literal file copy, because this app is vanilla TS+DOM. What is vendored: the
// list-selects-into-detail interaction model, the closeable/resizable right
// track that reclaims width when nothing is selected, and Escape-to-close. See
// ready-61c for the attribution review.
import { applyFilters, labelFrequency, NO_PRIORITY, priorityBuckets, type FilterState } from "./filters";
import { buildEpicTree, buildFreesIndex, deriveEpics, freesCount, type EpicRollup, type EpicTreeNode } from "./graph";
import { columnize, gatesFilter } from "./views";
import { sortCards } from "./sort";
import { statusLine, daysSince, ageLabel } from "./statusline";
import { isTerminal, type Item } from "./types";
import { unimplementedWriter, type BoardWriter } from "./write";
import { PLACEHOLDER } from "../lib/envelope";

export type SwimlaneMode = "project" | "epic" | "priority" | "off";

export interface BoardRef {
  coord: string;
  title: string;
  /**
   * This board's own load outcome (main.ts's BoardLoadState), or undefined when
   * the caller has nothing to say about it — the single-board callers and every
   * test that predates ready-27b. Undefined renders exactly as "open" did: no
   * marker, no status row.
   *
   * It is a per-BOARD field and not a workspace-level notice because the
   * portfolio view's whole subject is several boards at once, and a portfolio-
   * wide sentence cannot answer "is what I am seeing THIS project's work?".
   */
  state?: "open" | "withholding" | "sealed" | "unreadable-grant" | "failed";
  /** One sentence a person can act on, shown in the board-status list. */
  detail?: string;
}

/** The short marker each non-open board carries in the left tree, beside its
 * count. Deliberately a WORD and not an icon or a colour: the count next to it
 * is a number the reader would otherwise read as this project's work. */
const BOARD_STATE_LABEL: Record<NonNullable<BoardRef["state"]>, string> = {
  open: "",
  // "items withheld" and not "partial": the reader has to know the number beside
  // it is smaller than the board, which "partial" leaves open to being read as
  // "some cards are sealed".
  withholding: "items withheld",
  sealed: "sealed",
  "unreadable-grant": "key unreadable",
  failed: "did not load",
};

export interface WorkspaceOptions {
  /** The logged-in identity, for the "yours" status-line/detail rendering. */
  viewerId?: string;
  writer?: BoardWriter;
  detailWidth?: number;
  /**
   * Every board that survived signature verification, whether or not it carries
   * items. They become the left tree's project nodes, each tagged with
   * data-board-coord — which is where the deleted coordinate dump's one real
   * job (recording, checkably, WHICH boards reached the DOM) now lives.
   */
  boards?: BoardRef[];
  /** "Logged in as npub1… (read-only)" — rendered in the header. */
  identityLine?: string;
  /** Shown in place of the lanes when `boards` is empty. */
  emptyBoardsNote?: string;
  /** Confidential-board explanation, when the viewer is looking at one. */
  notice?: string;
}

const DETAIL_MIN = 180;
const DETAIL_DEFAULT = 340;
const LEFT_WIDTH = 206;
const GRIP_WIDTH = 5;
const RESPONSIVE_BREAKPOINT = 1000;
/** Cards shown per column before the "Show all N" disclosure (prototype CAP). */
const CARD_CAP = 6;
/** An item untouched for this long reads as stale (prototype `stale()`). */
const STALE_DAYS = 7;

/**
 * FIVE MUTED TONES, one per project — not a rainbow, and not one colour per
 * epic. Colour on this board means exactly one thing: which project's epic this
 * is. The five hexes and their assignments are the prototype's EPICS table.
 * A project outside that table takes a stable tone by hash, so the palette
 * never grows past five.
 */
const PROJECT_TONES = ["#7A4FB5", "#2C7FA8", "#0F7A6E", "#B5711A", "#8A5A5A"] as const;
const KNOWN_PROJECT_TONE: ReadonlyMap<string, string> = new Map([
  ["ready", "#7A4FB5"],
  ["galtrader", "#2C7FA8"],
  ["forge", "#0F7A6E"],
  ["dontguess", "#B5711A"],
  ["3dl", "#8A5A5A"],
]);

/** An `l` tag that is still an HMAC label token — 64 lowercase hex characters
 * the reader holds no LTK for (pkg/sync/envelope.go's labelToken). A 64-hex
 * pill is strictly worse than no pill: it is not a label, it is not readable,
 * and it blows the card's layout out. Hide it until it resolves. */
const HMAC_LABEL = /^[0-9a-f]{64}$/;

function toneForProject(project: string): string {
  const known = KNOWN_PROJECT_TONE.get(project);
  if (known) return known;
  let h = 0;
  for (let i = 0; i < project.length; i++) h = (h * 31 + project.charCodeAt(i)) >>> 0;
  return PROJECT_TONES[h % PROJECT_TONES.length];
}

/** tint converts one of the five tones to an rgba wash — the prototype's
 * tint(). A card's epic token is FILLED at 13% of its colour with a 34% border,
 * never at full saturation; that is the difference between a designed token and
 * a highlighter. */
function tint(hex: string, alpha: number): string {
  const n = parseInt(hex.slice(1), 16);
  return `rgba(${(n >> 16) & 255},${(n >> 8) & 255},${n & 255},${alpha})`;
}

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Partial<HTMLElementTagNameMap[K]> & { dataset?: Record<string, string> } = {},
  children: (Node | string)[] = [],
): HTMLElementTagNameMap[K] {
  const { dataset, ...rest } = props;
  const node = document.createElement(tag);
  Object.assign(node, rest);
  if (dataset) for (const [k, v] of Object.entries(dataset)) node.dataset[k] = v;
  for (const c of children) node.append(c);
  return node;
}

function byId(items: Item[]): Map<string, Item> {
  return new Map(items.map((i) => [i.id, i]));
}

/** Labels safe to render. See HMAC_LABEL. */
function visibleLabels(item: Item): string[] {
  return (item.labels ?? []).filter((l) => !HMAC_LABEL.test(l));
}

/** Nearest ancestor of `item` that is itself an epic (has children), walking
 * parentId, or undefined if none. Guards against a malformed parent cycle. */
function nearestEpicAncestor(item: Item, itemsById: Map<string, Item>, epicIds: Set<string>): Item | undefined {
  const seen = new Set<string>([item.id]);
  let cursor = item.parentId ? itemsById.get(item.parentId) : undefined;
  while (cursor) {
    if (seen.has(cursor.id)) return undefined;
    seen.add(cursor.id);
    if (epicIds.has(cursor.id)) return cursor;
    cursor = cursor.parentId ? itemsById.get(cursor.parentId) : undefined;
  }
  return undefined;
}

/**
 * hasLiveBlocker mirrors the fold's §8.2 + §8.4 for ONE item: an edge whose
 * blocker is not present in this projection is dropped silently (§8.2), and a
 * surviving edge whose BLOCKER is non-terminal forces `blocked` (§8.4).
 *
 * It exists so the optimistic patch can answer "will the next fold call this
 * item blocked?" with the fold's own rule rather than a guess. Exported for the
 * same reason applyGateResolution is: the answer decides a status, and a status
 * the board shows that the fold will contradict is the failure mode §22.2 spells
 * out in words ("the item snaps back on the next read").
 */
export function hasLiveBlocker(item: Item, itemsById: Map<string, Item>): boolean {
  for (const blockerId of item.blockedBy ?? []) {
    const blocker = itemsById.get(blockerId);
    if (blocker === undefined) continue; // §8.2: unresolvable edge, dropped
    if (!isTerminal(blocker)) return true; // §8.4
  }
  return false;
}

/**
 * applyGateResolution is the OPTIMISTIC half of resolving a gate (ready-186):
 * what the board shows the instant the human rules, before the relay has said
 * anything. It mirrors board-fold-spec.md §22.2 / §22.3 field for field.
 *
 * §22.2 APPROVE clears ALL FIVE gate fields — Gate, WaitingType, WaitingOn,
 * WaitingSince, GateMsgID. Clearing only `status` would empty the rail (its
 * predicate reads `status`) while leaving an item the fold can never produce:
 * active AND still declaring a gate. The next projection to arrive would then
 * disagree with the board for reasons no test could name.
 *
 * THE RESULTING STATUS IS NOT UNCONDITIONALLY `active`. §22.2/§9.2: "If the item
 * is still blocked, §8.4 recomputes Status=blocked on the next fold regardless of
 * the published `active` — the gate clears, the block does not, until the blocker
 * itself closes." Blocked-and-gated is the ORDINARY shape of a design gate
 * (§9.7), so an unconditional `active` would slide the card into Moving and let
 * it snap back to Blocked on the next read — the page telling the human
 * something the fold never agreed to. `itemsById` is the projection the item was
 * read from, and it is REQUIRED rather than defaulted: a caller with no index is
 * a caller that cannot know, and silently answering "not blocked" for it is the
 * bug this parameter exists to make impossible.
 *
 * §22.3 REJECT changes NO field: the gate stays open and the ruling survives as
 * history, so the item stays in the rail exactly as `rd gates` still lists it.
 * Rejecting is a durable note, not a transition.
 *
 * EXPORTED so this field set is asserted DIRECTLY. Through the rendered board
 * only `status` is observable — the rail and the columns both read it and
 * nothing else — so a version that cleared `gate` and stopped would look
 * identical on screen. That is precisely the shape of unwitnessed selection
 * ready-191 shipped three of, and the export is what makes it measurable.
 */
export function applyGateResolution(item: Item, approve: boolean, itemsById: Map<string, Item>): void {
  if (!approve) return;
  item.status = hasLiveBlocker(item, itemsById) ? "blocked" : "active";
  item.gate = undefined;
  item.waitingType = undefined;
  item.waitingOn = undefined;
  item.waitingSince = undefined;
  item.gateMsgId = undefined;
}

/** The three status columns, with the swatch colour each column head carries. */
const COLUMNS = [
  { key: "ready", label: "Ready", swatch: "var(--ready)", target: "inbox" },
  { key: "moving", label: "Moving", swatch: "var(--flight)", target: "active" },
  { key: "blocked", label: "Blocked", swatch: "var(--blocked)", target: "blocked" },
] as const;

/** What setItems preserves across a live re-render: the text field that had
 * focus, identified by (class, owning item), plus its unpublished value and
 * caret. See BoardWorkspace.setItems. */
interface FocusedField {
  cls: string;
  itemId?: string;
  value: string;
  start: number | null;
  end: number | null;
}

export class BoardWorkspace {
  private items: Item[];
  private readonly container: HTMLElement;
  private readonly writer: BoardWriter;
  private readonly viewerId?: string;
  private readonly boards: BoardRef[];
  private readonly boardTitles: Map<string, string>;
  private readonly boardByCoord: Map<string, BoardRef>;
  private readonly identityLine?: string;
  private readonly emptyBoardsNote?: string;
  private readonly notice?: string;

  private swimlane: SwimlaneMode = "project";
  private filters: FilterState = {};
  private query = "";
  private flagStuck = false;
  private flagLever = false;
  private scopeEpicId?: string;
  private scopeProject?: string;
  private selectedId?: string;
  private detailWidth: number;
  private transientError?: string;
  private expanded = new Set<string>();
  private readonly onKeydown = (e: KeyboardEvent) => {
    if (e.key === "Escape" && this.selectedId !== undefined) {
      this.closeDetail();
    }
  };

  constructor(container: HTMLElement, items: Item[], options: WorkspaceOptions = {}) {
    this.container = container;
    this.items = items;
    this.writer = options.writer ?? unimplementedWriter;
    this.viewerId = options.viewerId;
    this.boards = options.boards ?? [];
    this.boardTitles = new Map(this.boards.map((b) => [b.coord, b.title]));
    this.boardByCoord = new Map(this.boards.map((b) => [b.coord, b]));
    this.identityLine = options.identityLine;
    this.emptyBoardsNote = options.emptyBoardsNote;
    this.notice = options.notice;
    this.detailWidth = clampDetailWidth(options.detailWidth ?? DETAIL_DEFAULT);
    document.addEventListener("keydown", this.onKeydown);
    this.render();
  }

  destroy(): void {
    document.removeEventListener("keydown", this.onKeydown);
  }

  /**
   * setItems replaces the projection the board is showing and re-renders.
   *
   * ready-4359 made this a LIVE path: it used to be called by nobody (the page
   * folded once at load and never again), and it is now called every time the
   * relay pushes something — a change made by the rd CLI on another machine, or
   * by a second browser. That turns render()'s replaceChildren() into a hazard
   * it never was before: a fold arriving while the human is halfway through
   * typing a gate reason would silently destroy what they had typed and move the
   * caret. Their unpublished keystrokes are the one thing on this page that
   * exists NOWHERE else, so the refresh preserves them across the rebuild —
   * value, caret and selection — for the text field that has focus.
   *
   * The field is re-found by (class, owning item id), which is stable across a
   * rebuild in a way that a node identity is not: every write control carries a
   * class, and the ones mounted per item hang under a [data-id] ancestor. If the
   * item it belonged to is gone from the new projection, nothing is restored —
   * there is no field to restore it into.
   */
  setItems(items: Item[]): void {
    const focus = this.captureFocusedField();
    this.items = items;
    this.render();
    this.restoreFocusedField(focus);
  }

  private captureFocusedField(): FocusedField | undefined {
    const active = document.activeElement;
    if (!(active instanceof HTMLInputElement) || !this.container.contains(active)) return undefined;
    const cls = [...active.classList].find((c) => c.startsWith("act-") || c.startsWith("gate-"));
    if (cls === undefined) return undefined;
    return {
      cls,
      itemId: (active.closest("[data-id]") as HTMLElement | null)?.dataset.id,
      value: active.value,
      start: active.selectionStart,
      end: active.selectionEnd,
    };
  }

  /**
   * NO SELECTOR INTERPOLATION. Item ids and class names are matched by
   * comparison, never spliced into a querySelector string: an id arrives from a
   * relay, so building a selector out of one is an injection primitive on a page
   * whose whole security model is that relay data is untrusted input.
   *
   * More than one element can carry the same data-id — the gate rail's entry and
   * the card both do — so every candidate is searched and the first that
   * actually holds the field wins.
   */
  private restoreFocusedField(f: FocusedField | undefined): void {
    if (!f) return;
    const scopes: Element[] =
      f.itemId === undefined
        ? [this.container]
        : [...this.container.querySelectorAll<HTMLElement>("[data-id]")].filter((n) => n.dataset.id === f.itemId);
    for (const scope of scopes) {
      const next = [...scope.querySelectorAll("input")].find((n) => n.classList.contains(f.cls));
      if (!next) continue;
      next.value = f.value;
      next.focus();
      if (f.start !== null && f.end !== null) next.setSelectionRange(f.start, f.end);
      return;
    }
  }

  getState(): {
    swimlane: SwimlaneMode;
    filters: FilterState;
    query: string;
    scopeEpicId?: string;
    selectedId?: string;
    detailWidth: number;
  } {
    return {
      swimlane: this.swimlane,
      filters: this.filters,
      query: this.query,
      scopeEpicId: this.scopeEpicId,
      selectedId: this.selectedId,
      detailWidth: this.detailWidth,
    };
  }

  setSwimlane(mode: SwimlaneMode): void {
    this.swimlane = mode;
    this.expanded.clear();
    this.render();
  }

  setFilters(filters: FilterState): void {
    this.filters = filters;
    this.expanded.clear();
    this.render();
  }

  setQuery(q: string): void {
    this.query = q;
    this.expanded.clear();
    this.render();
  }

  toggleLabelFilter(atom: string): void {
    const current = this.filters.label ?? [];
    const next = current.includes(atom) ? current.filter((l) => l !== atom) : [...current, atom];
    this.setFilters({ ...this.filters, label: next });
  }

  togglePriorityFilter(bucket: string): void {
    const current = this.filters.priority ?? [];
    const next = current.includes(bucket) ? current.filter((p) => p !== bucket) : [...current, bucket];
    this.setFilters({ ...this.filters, priority: next });
  }

  toggleEpicScope(epicId: string): void {
    this.scopeEpicId = this.scopeEpicId === epicId ? undefined : epicId;
    this.scopeProject = undefined;
    this.render();
  }

  selectItem(id: string | undefined): void {
    this.selectedId = id;
    this.render();
  }

  closeDetail(): void {
    this.selectedId = undefined;
    this.render();
  }

  setDetailWidth(px: number): void {
    this.detailWidth = clampDetailWidth(px);
    this.render();
  }

  /**
   * optimistic is the single write call site's shape (ready-b2b done condition
   * 4): apply the change to the card IMMEDIATELY so the board feels direct,
   * then publish. If the write is refused — client-side (no grant, terminal
   * item) or by the relay — put the card back exactly as it was and say, in
   * plain words, what happened. `patch` returns the undo.
   *
   * The revert restores a SNAPSHOT of the item taken before the patch, not an
   * inverse operation, so a partially-applied multi-field change cannot leave
   * the card in a state neither the user nor the relay ever saw.
   */
  /**
   * buildActions is the detail pane's write surface: the five operations that
   * are not a drag (claim, close) or the gate banner (approve/reject) —
   * retitle, re-prioritise, and label add/remove. Together with the drag
   * handler and the gate banner these are the seven writes v1 ships.
   *
   * When the writer says this identity cannot write, the actions are not
   * rendered at all and the reason is stated instead. A disabled-looking button
   * that fails on click is a worse answer than saying "read-only, and here is
   * why" — and it is the interface half of the security model: no NIP-07
   * provider means read-only, said plainly, never a prompt for a secret key.
   */
  private buildActions(item: Item): HTMLElement {
    const section = el("div", { className: "detail-section detail-actions" }, [
      el("h3", { textContent: "Actions" }),
    ]);
    const readOnly = this.writer.whyReadOnly?.();
    if (readOnly) {
      section.append(el("p", { className: "read-only-note", textContent: readOnly }));
      return section;
    }

    const row = el("div", { className: "action-row" });
    if (!isTerminal(item)) {
      const claim = el("button", { className: "act pri act-claim", textContent: "Claim" });
      claim.addEventListener("click", () => void this.handleClaim(item.id));
      const close = el("button", { className: "act act-close", textContent: "Close (done)" });
      close.addEventListener("click", () => void this.handleClose(item.id, "done"));
      row.append(claim, close);
    }
    section.append(row);

    const titleInput = el("input", { className: "act-title-input", type: "text", value: item.title });
    const titleSave = el("button", { className: "act act-title-save", textContent: "Rename" });
    titleSave.addEventListener("click", () => void this.handleRetitle(item.id, titleInput.value));
    section.append(el("div", { className: "action-row" }, [titleInput, titleSave]));

    const prioSelect = el("select", { className: "act-priority" });
    for (const p of ["p0", "p1", "p2", "p3"]) {
      const opt = el("option", { value: p, textContent: p.toUpperCase() });
      if (item.priority === p) opt.selected = true;
      prioSelect.append(opt);
    }
    prioSelect.addEventListener("change", () => void this.handleSetPriority(item.id, prioSelect.value));
    section.append(el("div", { className: "action-row" }, [el("span", { textContent: "Priority" }), prioSelect]));

    const labelInput = el("input", { className: "act-label-input", type: "text", placeholder: "label" });
    const labelAdd = el("button", { className: "act act-label-add", textContent: "Add label" });
    labelAdd.addEventListener("click", () => {
      const v = labelInput.value.trim();
      if (v !== "") void this.handleSetLabel(item.id, v, true);
    });
    const labelRemove = el("button", { className: "act act-label-remove", textContent: "Remove label" });
    labelRemove.addEventListener("click", () => {
      const v = labelInput.value.trim();
      if (v !== "") void this.handleSetLabel(item.id, v, false);
    });
    section.append(el("div", { className: "action-row" }, [labelInput, labelAdd, labelRemove]));

    return section;
  }

  private async optimistic(itemId: string, patch: (item: Item) => void, publish: () => Promise<void>): Promise<void> {
    const idx = this.items.findIndex((i) => i.id === itemId);
    if (idx < 0) {
      this.transientError = `unknown item ${itemId}`;
      this.render();
      return;
    }
    const before = this.items[idx];
    const after = { ...before };
    patch(after);
    this.items = this.items.slice();
    this.items[idx] = after;
    this.transientError = undefined;
    this.render();

    try {
      await publish();
    } catch (err) {
      this.items = this.items.slice();
      this.items[idx] = before;
      this.transientError = err instanceof Error ? err.message : String(err);
    }
    this.render();
  }

  async handleDrop(itemId: string, toStatus: Item["status"]): Promise<void> {
    await this.optimistic(
      itemId,
      (it) => {
        it.status = toStatus;
      },
      () => this.writer.moveStatus(itemId, toStatus),
    );
  }

  /**
   * handleGateResolve is the gate rail's ACTIONABLE half (ready-186).
   *
   * A REASON IS REQUIRED, AND THE REFUSAL IS LOCAL (done condition 4). rd's own
   * gate commands carry `--reason`, and a gate ruling with no reason is the one
   * thing the audit history cannot reconstruct later: §22.4 says approve and
   * reject are indistinguishable on the wire EXCEPT by that reason, so an empty
   * one publishes a status event whose Content is "" and leaves a reviewer with
   * a transition and no ruling. The refusal happens BEFORE the writer is called
   * — nothing is signed, nothing is published, and the optimistic patch is never
   * applied — so an empty-reason click cannot leave the board showing a
   * resolution that never happened.
   *
   * What the board shows the instant the human rules is applyGateResolution's
   * job (§22.2 / §22.3, above); the revert if the publish is refused is
   * optimistic()'s.
   */
  async handleGateResolve(itemId: string, approve: boolean, reason = ""): Promise<void> {
    const verb = approve ? "approve" : "reject";
    if (reason.trim() === "") {
      this.transientError =
        `a reason is required to ${verb} the gate on ${itemId} — the ruling IS the record ` +
        `(rd ${verb} --reason "…"). Nothing was published.`;
      this.render();
      return;
    }
    // The index is built from the CURRENT projection, before the patch, so
    // §8.4's "is a non-terminal blocker still standing" is answered against the
    // same items the fold would see.
    const itemsById = byId(this.items);
    await this.optimistic(
      itemId,
      (it) => applyGateResolution(it, approve, itemsById),
      () => this.writer.resolveGate(itemId, approve, reason.trim()),
    );
  }

  async handleClaim(itemId: string): Promise<void> {
    await this.optimistic(
      itemId,
      (it) => {
        it.status = "active";
        if (this.viewerId) it.by = this.viewerId;
      },
      () => this.writer.claim(itemId),
    );
  }

  async handleClose(itemId: string, resolution = "done"): Promise<void> {
    await this.optimistic(
      itemId,
      (it) => {
        it.status = resolution as Item["status"];
      },
      () => this.writer.close(itemId, resolution),
    );
  }

  async handleRetitle(itemId: string, title: string): Promise<void> {
    await this.optimistic(
      itemId,
      (it) => {
        it.title = title;
      },
      () => this.writer.setTitle(itemId, title),
    );
  }

  async handleSetPriority(itemId: string, priority: string): Promise<void> {
    await this.optimistic(
      itemId,
      (it) => {
        it.priority = priority;
      },
      () => this.writer.setPriority(itemId, priority),
    );
  }

  async handleSetLabel(itemId: string, label: string, present: boolean): Promise<void> {
    await this.optimistic(
      itemId,
      (it) => {
        const labels = (it.labels ?? []).filter((l) => l !== label);
        it.labels = present ? [...labels, label] : labels;
      },
      () => this.writer.setLabel(itemId, label, present),
    );
  }

  // ── naming ────────────────────────────────────────────────────────────────

  /**
   * The grouping key for an item's project lane. Board coordinate first, because
   * that is the only identifier every item carries in production (the fold sets
   * no `project` field — there is no project tag on a card).
   */
  private projectKeyOf(item: Item): string {
    return item.boardCoord || item.project || "(unknown project)";
  }

  /**
   * The DISPLAYED project name. ready-56b: swimlane heads used to print
   * "30301:a9f766ae…:dontguess" because projectKeyOf is a coordinate and nothing
   * translated it. The board's own signed `title` tag is the name; a coordinate
   * is never shown to a person.
   */
  private projectNameOf(key: string): string {
    return this.boardTitles.get(key) ?? key;
  }

  private projectNameOfItem(item: Item): string {
    return this.projectNameOf(this.projectKeyOf(item));
  }

  private toneForItem(item: Item): string {
    return toneForProject(this.projectNameOfItem(item));
  }

  // ── render ────────────────────────────────────────────────────────────────

  private render(): void {
    this.container.replaceChildren();
    this.container.classList.add("board-root");

    const top = el("div", { className: "board-top" });
    top.append(this.buildHeader());
    if (this.notice) {
      top.append(el("p", { className: "confidential-notice", textContent: this.notice }));
    }
    const boardStatus = this.buildBoardStatus();
    if (boardStatus) top.append(boardStatus);
    top.append(this.buildGateRail());
    this.container.append(top);

    const workspace = el("div", { className: "board-workspace" });
    const detailOpen = this.selectedId !== undefined && byId(this.items).has(this.selectedId);
    workspace.classList.toggle("detail-open", detailOpen);
    workspace.style.gridTemplateColumns = detailOpen
      ? `${LEFT_WIDTH}px minmax(0, 1fr) ${GRIP_WIDTH}px ${this.detailWidth}px`
      : `${LEFT_WIDTH}px minmax(0, 1fr)`;

    workspace.append(this.buildLeftTree());
    workspace.append(this.buildCenter());
    if (detailOpen) {
      workspace.append(this.buildResizer());
      workspace.append(this.buildDetailPane(byId(this.items).get(this.selectedId!)!));
    }

    this.container.append(workspace);

    if (this.transientError) {
      this.container.append(
        el("p", { className: "transient-error", textContent: this.transientError }),
      );
    }

    this.container.append(this.buildFooter());
  }

  /**
   * buildBoardStatus names EVERY board this view could not show in full, one row
   * each, with the reason (ready-27b). Returns null when every board opened, so
   * the ordinary portfolio grows no paragraph.
   *
   * WHY A LIST AND NOT A SENTENCE. The workspace already carries a `notice`, and
   * that notice is a portfolio-wide total by construction ("N of M titles were
   * decrypted"). Over ~24 boards from several owners those totals cannot answer
   * the only question a reader has in front of a project's node — is this
   * project's work all here? — and 14 of the live boards are in a state
   * (browser-unreadable grants) that a total renders as an anonymous fraction.
   * One row per board, named by its own title, is the smallest thing that says
   * WHICH project is affected and WHAT the reader can do about it.
   *
   * The row carries data-board-coord for the same reason the tree node does: what
   * the page says about a board must be checkable per board, from outside.
   */
  private buildBoardStatus(): HTMLElement | null {
    const degraded = this.boards.filter((b) => b.state !== undefined && b.state !== "open");
    if (degraded.length === 0) return null;
    const list = el("ul", { className: "board-status" });
    for (const b of degraded) {
      list.append(
        el("li", { className: `board-status-row board-status-${b.state!}`, dataset: { boardCoord: b.coord } }, [
          el("b", { className: "board-status-name", textContent: b.title }),
          " — ",
          el("span", { className: "board-status-detail", textContent: b.detail ?? "" }),
        ]),
      );
    }
    return list;
  }

  /** Open = every item that is not in a terminal status. The tally counts what
   * the viewer could act on, not what the fold happened to return. */
  private openCount(): number {
    return this.items.filter((i) => !isTerminal(i)).length;
  }

  private projectCount(): number {
    if (this.boards.length > 0) return this.boards.length;
    return new Set(this.items.map((i) => this.projectKeyOf(i))).size;
  }

  private buildHeader(): HTMLElement {
    const header = el("header", { className: "board-header" });

    const mark = el("div", { className: "mark", textContent: "ready " });
    mark.append(el("span", { textContent: "/ board" }));
    header.append(mark);

    // "shown" is what is ON THE BOARD, so it counts only items that land in one
    // of the three columns. Counting the filter result instead would report
    // "290 shown" on a board displaying 175 cards, because a terminal item
    // survives every filter and then has no column (views.ts columnize).
    const shown = this.scopedItems().filter((i) => !isTerminal(i)).length;
    const tally = el("div", { className: "tally" });
    tally.append(
      el("b", { textContent: String(this.openCount()) }),
      " open · ",
      el("b", { textContent: String(this.projectCount()) }),
      " projects · ",
      el("b", { textContent: String(shown) }),
      " shown",
    );
    header.append(tally);

    header.append(el("div", { className: "spacer" }));

    const find = el("input", {
      className: "find",
      type: "search",
      placeholder: "Filter by word or id",
      value: this.query,
    });
    find.setAttribute("aria-label", "Filter");
    find.addEventListener("input", () => {
      const caret = find.selectionStart;
      this.setQuery(find.value);
      const next = this.container.querySelector<HTMLInputElement>("input.find");
      if (next) {
        next.focus();
        if (caret !== null) next.setSelectionRange(caret, caret);
      }
    });
    header.append(find);

    const newBtn = el("button", { className: "newbtn", textContent: "New item" });
    newBtn.addEventListener("click", () => {
      // Honest, not fake: this board is read-only until the publish path lands
      // (write.ts). A button that pretended to create an item would be worse
      // than no button — the design calls for the affordance, so it is here,
      // and it says exactly what it can and cannot do.
      this.transientError =
        "Creating items from the board is not wired yet — this page publishes no events. Use `rd create` for now.";
      this.render();
    });
    header.append(newBtn);

    if (this.identityLine) {
      header.append(el("p", { className: "identity", textContent: this.identityLine }));
    }
    return header;
  }

  private buildGateRail(): HTMLElement {
    const gated = this.items.filter(gatesFilter());
    if (gated.length === 0) {
      // .empty's textContent is exactly the one quiet line — the dot and the
      // heading carry no other text (render.test.ts asserts the whole string).
      const clear = el("div", { className: "gate-rail empty" }, [
        el("div", { className: "gate-rail-head" }, [
          el("span", { className: "gate-rail-dot" }),
          el("h2", { className: "gate-rail-heading", textContent: "Nothing needs you right now" }),
        ]),
      ]);
      return clear;
    }

    const rail = el("div", { className: "gate-rail" });
    rail.append(
      el("div", { className: "gate-rail-head" }, [
        el("span", { className: "gate-rail-dot" }),
        el("h2", { className: "gate-rail-heading", textContent: "Waiting on you" }),
        el("span", {
          className: "gate-rail-sub",
          textContent: `${gated.length} of ${this.openCount()} — the only ones that cannot move without your decision.`,
        }),
      ]),
    );

    const list = el("ul", { className: "gate-list" });
    for (const item of gated) {
      const li = el("li", { className: "gate-item", dataset: { id: item.id } });
      const button = el("button", { className: "gate-item-button" }, [
        el("span", { className: "gate-item-id", textContent: item.id }),
        el("span", { className: "gate-item-title", textContent: item.title }),
        el("span", { className: "gate-item-age", textContent: ageLabel(item.updatedAt) }),
      ]);
      button.addEventListener("click", () => this.selectItem(item.id));
      li.append(button, this.buildGateResolve(item));
      list.append(li);
    }
    rail.append(list);
    return rail;
  }

  /**
   * buildGateResolve is the control that makes a pending gate RESOLVABLE from
   * wherever it is shown — the rail itself and the detail pane's banner both
   * mount this same element (ready-186).
   *
   * WHY IN THE RAIL AND NOT ONLY THE DETAIL PANE. Gates are the one thing in rd
   * that genuinely requires a human, and the rail is the page's signature
   * element precisely because of that. A rail you can only read sends the human
   * back to the terminal to type `rd approve`, which is the outcome this exists
   * to remove; the detail pane keeps its copy because that is where the context
   * for the ruling (dependencies, history, the gate's own description) is.
   *
   * THE REASON FIELD IS PART OF THE CONTROL, NOT A PROMPT. window.prompt() is
   * blocked in some embeddings and untestable in a headless browser, and a
   * modal would hide the item the ruling is about. The input carries no default
   * — an empty one is refused by handleGateResolve, which is the enforcement;
   * this is only where it is typed.
   *
   * A KEY THAT CANNOT WRITE GETS NO BUTTONS AND IS TOLD WHY (done condition 6),
   * the same shape buildActions uses: a disabled-looking button that fails on
   * click is a worse answer than saying "read-only, and here is why". This is
   * the CLIENT-SIDE half only — the read-side authority gate (§3.4 read-trust)
   * refuses an ungranted key's gate-resolve events independently, and neither
   * check substitutes for the other.
   */
  private buildGateResolve(item: Item): HTMLElement {
    const wrap = el("div", { className: "gate-resolve" });
    const readOnly = this.writer.whyReadOnly?.();
    if (readOnly) {
      wrap.append(el("p", { className: "read-only-note gate-read-only", textContent: readOnly }));
      return wrap;
    }
    const reason = el("input", {
      className: "gate-reason-input",
      type: "text",
      placeholder: "Reason (required)",
    });
    reason.setAttribute("aria-label", `Reason for resolving the gate on ${item.id}`);
    const approve = el("button", { className: "gate-approve act pri", textContent: "Approve" });
    approve.addEventListener("click", () => void this.handleGateResolve(item.id, true, reason.value));
    const deny = el("button", { className: "gate-deny act", textContent: "Reject" });
    deny.addEventListener("click", () => void this.handleGateResolve(item.id, false, reason.value));
    wrap.append(reason, approve, deny);
    return wrap;
  }

  // ── left tree ─────────────────────────────────────────────────────────────

  private buildLeftTree(): HTMLElement {
    const tree = el("nav", { className: "left-tree" });
    tree.setAttribute("aria-label", "Structure");

    tree.append(el("h3", { className: "left-tree-heading", textContent: "Structure" }));
    tree.append(
      el("p", { className: "side-sub", textContent: "Epics group work; a project may hold several." }),
    );

    tree.append(
      this.buildTreeNode({
        scope: "",
        name: "Everything",
        count: this.items.length,
        dotColour: "var(--ink-3)",
        current: !this.scopeProject && !this.scopeEpicId,
        onClick: () => {
          this.scopeProject = undefined;
          this.scopeEpicId = undefined;
          this.render();
        },
      }),
    );

    const epics = deriveEpics(this.items);
    const epicTree = buildEpicTree(epics);
    const countByProject = new Map<string, number>();
    for (const item of this.items) {
      const key = this.projectKeyOf(item);
      countByProject.set(key, (countByProject.get(key) ?? 0) + 1);
    }

    // Every VERIFIED board gets a node, even one holding no items: "this board
    // exists and is empty" is a different fact from "this board is not yours",
    // and the node is where data-board-coord records which boards reached the
    // DOM (see WorkspaceOptions.boards).
    const keys =
      this.boards.length > 0
        ? this.boards.map((b) => b.coord)
        : [...countByProject.keys()].sort((a, b) => a.localeCompare(b));

    const list = el("ul", { className: "project-list" });
    for (const key of keys) {
      const name = this.projectNameOf(key);
      const node = this.buildTreeNode({
        scope: `proj:${key}`,
        name,
        count: countByProject.get(key) ?? 0,
        dotColour: "var(--line)",
        twisty: "▾",
        boardCoord: this.boardTitles.has(key) ? key : undefined,
        boardState: this.boardByCoord.get(key)?.state,
        boardDetail: this.boardByCoord.get(key)?.detail,
        current: this.scopeProject === key,
        onClick: () => {
          this.scopeProject = this.scopeProject === key ? undefined : key;
          this.scopeEpicId = undefined;
          this.render();
        },
      });
      const li = el("li", { className: "project-node" }, [node]);
      const epicsHere = epicTree.filter((n) => this.projectKeyOf(n.rollup.epic) === key);
      if (epicsHere.length > 0) {
        const epicList = el("ul", { className: "epic-list" });
        for (const n of epicsHere) epicList.append(this.buildEpicNode(n, 0));
        li.append(epicList);
      }
      list.append(li);
    }
    // Epics whose project is not in the board list still have to appear.
    const orphanEpics = epicTree.filter((n) => !keys.includes(this.projectKeyOf(n.rollup.epic)));
    if (orphanEpics.length > 0) {
      const epicList = el("ul", { className: "epic-list" });
      for (const n of orphanEpics) epicList.append(this.buildEpicNode(n, 0));
      list.append(el("li", { className: "project-node" }, [epicList]));
    }
    tree.append(list);

    tree.append(el("h3", { className: "left-tree-heading labels-heading", textContent: "Labels" }));
    tree.append(
      el("p", { className: "side-sub", textContent: "Cross-cutting tags — pick one to filter." }),
    );
    const labelList = el("div", { className: "label-list" });
    const sanitized = this.items.map((i) => ({ ...i, labels: visibleLabels(i) }));
    for (const { label, count } of labelFrequency(sanitized)) {
      const active = (this.filters.label ?? []).includes(label);
      const pill = el("button", {
        className: `label-pill${active ? " active" : ""}${HOT_LABELS.has(label) && !active ? " hot" : ""}`,
        dataset: { label },
      });
      pill.append(label + " ", el("span", { className: "label-count", textContent: String(count) }));
      pill.addEventListener("click", () => this.toggleLabelFilter(label));
      labelList.append(pill);
    }
    tree.append(labelList);
    return tree;
  }

  private buildTreeNode(spec: {
    scope: string;
    name: string;
    count: number;
    dotColour: string;
    twisty?: string;
    boardCoord?: string;
    boardState?: NonNullable<BoardRef["state"]>;
    boardDetail?: string;
    current: boolean;
    onClick: () => void;
  }): HTMLElement {
    const dataset: Record<string, string> = { scope: spec.scope };
    if (spec.boardCoord) dataset.boardCoord = spec.boardCoord;
    // ready-27b: the state travels as an ATTRIBUTE as well as visible text, for
    // the same reason data-board-coord does — it is how a live run can check,
    // per board, that what the page says about a board is what the load found.
    if (spec.boardState) dataset.boardState = spec.boardState;
    const node = el("button", { className: "node", dataset });
    node.setAttribute("aria-current", String(spec.current));
    node.append(
      el("span", { className: "tw", textContent: spec.twisty ?? "" }),
      el("span", { className: "dot" }),
      el("span", { className: "nm", textContent: spec.name }),
    );
    // The marker sits BETWEEN the name and the count, so the count is never read
    // on its own: "0" beside "did not load" is not a claim about the board's
    // work, and "0" alone is.
    if (spec.boardState && spec.boardState !== "open") {
      const mark = el("span", {
        className: `board-state board-state-${spec.boardState}`,
        textContent: BOARD_STATE_LABEL[spec.boardState],
      });
      if (spec.boardDetail) mark.title = spec.boardDetail;
      node.append(mark);
    }
    node.append(el("span", { className: "ct", textContent: String(spec.count) }));
    (node.querySelector(".dot") as HTMLElement).style.background = spec.dotColour;
    node.addEventListener("click", spec.onClick);
    return node;
  }

  /**
   * An epic in the LEFT TREE is a 7px dot beside the name — never a filled
   * block. ready-56b: filling the whole row in a saturated colour is what made
   * the shipped page read as a highlighter rack. The dot carries the colour;
   * the row carries the name and the rollup.
   */
  private buildEpicNode(node: EpicTreeNode, depth: number): HTMLElement {
    const { rollup } = node;
    const li = el("li", { className: "epic-node", dataset: { epicId: rollup.epic.id } });
    const row = this.buildTreeNode({
      scope: rollup.epic.id,
      name: rollup.epic.title,
      count: rollup.total,
      dotColour: this.toneForItem(rollup.epic),
      current: this.scopeEpicId === rollup.epic.id,
      onClick: () => this.toggleEpicScope(rollup.epic.id),
    });
    row.classList.add("epic-node-row");
    if (depth > 0) row.classList.add("lvl2");
    row.style.paddingLeft = `${20 + depth * 13}px`;
    li.append(row);

    if (rollup.total > 0) {
      const moving = rollup.descendants.filter((d) => d.status === "active").length;
      const pct = Math.round((moving / rollup.total) * 100);
      const prog = el("div", { className: "prog" });
      prog.style.marginLeft = `${33 + depth * 13}px`;
      const bar = el("i");
      bar.style.width = `${pct}%`;
      prog.append(bar);
      li.append(prog);
    }

    if (node.children.length > 0) {
      const childList = el("ul", { className: "epic-children" });
      for (const child of node.children) childList.append(this.buildEpicNode(child, depth + 1));
      li.append(childList);
    }
    return li;
  }

  // ── centre ────────────────────────────────────────────────────────────────

  private isStale(item: Item): boolean {
    return daysSince(item.updatedAt) >= STALE_DAYS;
  }

  private matchesQuery(item: Item): boolean {
    if (this.query.trim() === "") return true;
    const haystack = `${item.title} ${item.id} ${this.projectNameOfItem(item)}`.toLowerCase();
    return haystack.includes(this.query.trim().toLowerCase());
  }

  private scopedItems(): Item[] {
    let items = this.items;
    if (this.scopeProject) {
      items = items.filter((i) => this.projectKeyOf(i) === this.scopeProject);
    }
    if (this.scopeEpicId) {
      const epics = deriveEpics(this.items);
      const scoped = epics.find((r) => r.epic.id === this.scopeEpicId);
      if (scoped) {
        const ids = new Set([scoped.epic.id, ...scoped.descendants.map((d) => d.id)]);
        items = items.filter((i) => ids.has(i.id));
      }
    }
    items = applyFilters(items, this.filters).filter((i) => this.matchesQuery(i));
    if (this.flagStuck) items = items.filter((i) => this.isStale(i));
    if (this.flagLever) {
      const frees = buildFreesIndex(this.items);
      items = items.filter((i) => freesCount(frees, i.id) > 0);
    }
    return items;
  }

  private buildCenter(): HTMLElement {
    const center = el("div", { className: "board-center" });
    center.append(this.buildFilterBar());
    center.append(
      el("div", {
        className: "sortnote",
        textContent:
          "Sorted by priority, then by how much other work it frees, then by longest untouched.",
      }),
    );

    const lanesEl = el("div", { className: "swimlanes" });
    if (this.boards.length === 0 && this.items.length === 0 && this.emptyBoardsNote) {
      lanesEl.append(el("div", { className: "empty", textContent: this.emptyBoardsNote }));
      center.append(lanesEl);
      return center;
    }

    const filtered = this.scopedItems();
    const lanes = this.groupIntoLanes(filtered);
    if (lanes.length === 0) {
      lanesEl.append(
        el("div", {
          className: "empty",
          textContent:
            this.items.length === 0
              ? "No items on these boards yet."
              : "Nothing matches. Clear a filter.",
        }),
      );
    }
    for (const lane of lanes) {
      lanesEl.append(this.buildLane(lane.key, lane.label, lane.items, lane.rollup, lane.dotColour));
    }
    center.append(lanesEl);
    return center;
  }

  private buildFilterBar(): HTMLElement {
    const bar = el("div", { className: "filter-bar" });
    bar.append(el("span", { className: "bar-label", textContent: "Swimlanes" }));

    const seg = el("div", { className: "seg" });
    seg.setAttribute("role", "group");
    seg.setAttribute("aria-label", "Swimlanes");
    for (const [mode, label] of [
      ["project", "Project"],
      ["epic", "Epic"],
      ["priority", "Priority"],
      ["off", "Off"],
    ] as [SwimlaneMode, string][]) {
      const btn = el("button", { textContent: label, dataset: { lane: mode } });
      btn.setAttribute("aria-pressed", String(this.swimlane === mode));
      btn.addEventListener("click", () => this.setSwimlane(mode));
      seg.append(btn);
    }
    bar.append(seg);
    bar.append(el("span", { className: "bar-sep" }));

    for (const bucket of ["p0", "p1"]) {
      const active = (this.filters.priority ?? []).includes(bucket);
      const chip = el("button", {
        className: `chip${active ? " active" : ""}`,
        textContent: bucket.toUpperCase(),
        dataset: { pri: bucket },
      });
      chip.setAttribute("aria-pressed", String(active));
      chip.addEventListener("click", () => this.togglePriorityFilter(bucket));
      bar.append(chip);
    }

    for (const [flag, label, get, set] of [
      ["stuck", "Untouched 7+ days", () => this.flagStuck, (v: boolean) => (this.flagStuck = v)],
      ["lever", "Unblocks others", () => this.flagLever, (v: boolean) => (this.flagLever = v)],
    ] as [string, string, () => boolean, (v: boolean) => void][]) {
      const active = get();
      const chip = el("button", {
        className: `chip${active ? " active" : ""}`,
        textContent: label,
        dataset: { flag },
      });
      chip.setAttribute("aria-pressed", String(active));
      chip.addEventListener("click", () => {
        set(!get());
        this.expanded.clear();
        this.render();
      });
      bar.append(chip);
    }

    const filtering =
      this.scopeEpicId !== undefined ||
      this.scopeProject !== undefined ||
      (this.filters.label ?? []).length > 0 ||
      (this.filters.priority ?? []).length > 0 ||
      this.flagStuck ||
      this.flagLever ||
      this.query !== "";
    if (filtering) {
      const clear = el("button", { className: "chip clear-filters", textContent: "Clear filters" });
      clear.addEventListener("click", () => {
        this.scopeEpicId = undefined;
        this.scopeProject = undefined;
        this.flagStuck = false;
        this.flagLever = false;
        this.query = "";
        this.setFilters({});
      });
      bar.append(clear);
    }

    return bar;
  }

  private groupIntoLanes(
    items: Item[],
  ): { key: string; label: string; items: Item[]; rollup?: EpicRollup; dotColour?: string }[] {
    if (this.swimlane === "off") {
      return [{ key: "off", label: "All work", items }];
    }
    if (this.swimlane === "priority") {
      const buckets = priorityBuckets(items);
      return buckets.map((b) => ({
        key: b,
        label: b === NO_PRIORITY ? "No priority" : b.toUpperCase(),
        items: items.filter((i) => (i.priority || NO_PRIORITY) === b),
      }));
    }
    if (this.swimlane === "project") {
      const groups = new Map<string, Item[]>();
      for (const item of items) {
        const key = this.projectKeyOf(item);
        const list = groups.get(key) ?? [];
        list.push(item);
        groups.set(key, list);
      }
      // Most gated first, then largest — the prototype's project-lane order:
      // the lane that needs a human decision should not be below the fold.
      const gateCount = (key: string): number =>
        items.filter((i) => this.projectKeyOf(i) === key && gatesFilter()(i)).length;
      return [...groups.entries()]
        .sort(([a, ia], [b, ib]) => gateCount(b) - gateCount(a) || ib.length - ia.length || a.localeCompare(b))
        .map(([key, its]) => ({ key, label: this.projectNameOf(key), items: its }));
    }
    // epic
    const allEpics = deriveEpics(this.items);
    const epicIds = new Set(allEpics.map((e) => e.epic.id));
    const idIndex = byId(this.items);
    const groups = new Map<string, Item[]>();
    const noEpicKey = "(no epic)";
    for (const item of items) {
      const ancestor = epicIds.has(item.id) ? undefined : nearestEpicAncestor(item, idIndex, epicIds);
      const key = ancestor ? ancestor.id : epicIds.has(item.id) ? item.id : noEpicKey;
      const list = groups.get(key) ?? [];
      list.push(item);
      groups.set(key, list);
    }
    const rollupById = new Map(allEpics.map((r) => [r.epic.id, r]));
    return [...groups.entries()]
      .sort(([a], [b]) => (a === noEpicKey ? 1 : b === noEpicKey ? -1 : a.localeCompare(b)))
      .map(([key, its]) => {
        const rollup = rollupById.get(key);
        return {
          key,
          label: key === noEpicKey ? "No epic" : rollup?.epic.title ?? key,
          items: its,
          rollup,
          dotColour: rollup ? this.toneForItem(rollup.epic) : undefined,
        };
      });
  }

  private buildLane(
    key: string,
    label: string,
    items: Item[],
    rollup?: EpicRollup,
    dotColour?: string,
  ): HTMLElement {
    const lane = el("div", { className: "swimlane", dataset: { lane: key } });
    const header = el("div", { className: "swimlane-header" });
    const name = el("span", { className: "lane-name" });
    if (dotColour) {
      const dot = el("span", { className: "lane-dot" });
      dot.style.background = dotColour;
      name.append(dot);
    }
    name.append(label);
    header.append(name);
    header.append(el("span", { className: "lane-count", textContent: String(items.length) }));
    if (rollup) {
      header.append(
        el("span", { className: "swimlane-rollup", textContent: `${rollup.closed}/${rollup.total}` }),
      );
    }
    const gated = items.filter(gatesFilter());
    if (gated.length > 0) {
      const pill = el("button", {
        className: "lane-gate",
        textContent: `${gated.length} waiting on you`,
      });
      pill.addEventListener("click", () => this.selectItem(gated[0].id));
      header.append(pill);
    }
    lane.append(header);

    const freesIndex = buildFreesIndex(this.items);
    const laneColumns = el("div", { className: "lane-columns" });
    const columns = columnize(items);
    const byKey: Record<string, Item[]> = {
      ready: columns.ready,
      moving: columns.moving,
      blocked: columns.blocked,
    };
    for (const col of COLUMNS) {
      laneColumns.append(
        this.buildColumn(
          `${key}/${col.key}`,
          col,
          sortCards(byKey[col.key]),
          freesIndex,
          this.swimlane !== "epic",
        ),
      );
    }
    lane.append(laneColumns);
    return lane;
  }

  private buildColumn(
    expandKey: string,
    col: (typeof COLUMNS)[number],
    items: Item[],
    freesIndex: ReturnType<typeof buildFreesIndex>,
    showEpic: boolean,
  ): HTMLElement {
    const column = el("div", { className: "column", dataset: { column: col.key } });
    const head = el("div", { className: "column-header" });
    const swatch = el("i");
    swatch.style.background = col.swatch;
    head.append(swatch, `${col.label} `, el("span", { className: "col-n", textContent: String(items.length) }));
    column.append(head);

    const cardList = el("div", { className: "card-list" });
    column.append(cardList);

    column.addEventListener("dragover", (e) => e.preventDefault());
    column.addEventListener("drop", (e) => {
      e.preventDefault();
      const itemId = e.dataTransfer?.getData("text/plain");
      if (itemId) void this.handleDrop(itemId, col.target);
    });

    if (items.length === 0) {
      cardList.append(el("div", { className: "empty", textContent: "None" }));
      return column;
    }

    const open = this.expanded.has(expandKey);
    const shown = open ? items : items.slice(0, CARD_CAP);
    for (const item of shown) cardList.append(this.buildCard(item, freesIndex, showEpic));
    if (items.length > CARD_CAP) {
      const more = el("button", {
        className: "more",
        textContent: open ? "Show fewer" : `Show all ${items.length}`,
        dataset: { more: expandKey },
      });
      more.addEventListener("click", () => {
        if (open) this.expanded.delete(expandKey);
        else this.expanded.add(expandKey);
        this.render();
      });
      cardList.append(more);
    }
    return column;
  }

  /**
   * CARD ANATOMY (prototype `card()`): border-left coloured by p0 / stale /
   * status; top row = priority chip + epic token + id + age; title; up to three
   * OUTLINED label pills plus a dashed "+N"; foot row = one status word and,
   * pushed right, the "frees N" leverage pill.
   */
  private buildCard(item: Item, freesIndex: ReturnType<typeof buildFreesIndex>, showEpic: boolean): HTMLElement {
    const card = el("div", {
      className: "card",
      draggable: true,
      dataset: { id: item.id, status: item.status },
    });
    if (item.title === PLACEHOLDER) card.classList.add("sealed");
    card.setAttribute("role", "button");
    card.setAttribute("tabindex", "0");
    card.setAttribute("aria-current", String(this.selectedId === item.id));
    card.style.borderLeftColor = this.edgeColour(item);
    card.addEventListener("dragstart", (e) => {
      e.dataTransfer?.setData("text/plain", item.id);
    });
    card.addEventListener("click", () => this.selectItem(item.id));

    const top = el("div", { className: "card-top" });
    top.append(
      el("span", {
        className: `card-priority pri-${item.priority || "none"}`,
        textContent: item.priority ? item.priority.toUpperCase() : "—",
      }),
    );

    const epic = this.epicOf(item);
    if (showEpic && epic) {
      const tone = this.toneForItem(epic);
      const token = el("span", { className: "epic-token" });
      token.style.color = tone;
      token.style.backgroundColor = tint(tone, 0.13);
      token.style.borderColor = tint(tone, 0.34);
      token.append(el("b"), el("span", { textContent: epic.title }));
      top.append(token);
    }

    top.append(
      el("span", { className: "card-id", textContent: item.id }),
      el("span", { className: "card-age", textContent: ageLabel(item.updatedAt) }),
    );
    card.append(top);

    if (this.swimlane === "off") {
      card.append(el("span", { className: "project-chip", textContent: this.projectNameOfItem(item) }));
    }

    card.append(el("div", { className: "card-title", textContent: item.title }));

    const labels = visibleLabels(item);
    if (labels.length > 0) {
      const labelWrap = el("div", { className: "card-labels" });
      for (const label of labels.slice(0, 3)) {
        const pill = el("button", {
          className: `label-pill${HOT_LABELS.has(label) ? " hot" : ""}`,
          textContent: label,
          dataset: { label },
        });
        pill.addEventListener("click", (e) => {
          e.stopPropagation();
          this.toggleLabelFilter(label);
        });
        labelWrap.append(pill);
      }
      if (labels.length > 3) {
        labelWrap.append(el("span", { className: "label-pill more", textContent: `+${labels.length - 3}` }));
      }
      card.append(labelWrap);
    }

    const foot = el("div", { className: "card-foot" });
    const line = statusLine(item, this.viewerId);
    foot.append(
      el("div", { className: `card-status-line ${statusLineClass(line)}`, textContent: line }),
    );
    const frees = freesCount(freesIndex, item.id);
    if (frees > 0) {
      const pill = el("span", { className: "card-frees", textContent: `frees ${frees}` });
      pill.title = `Finishing this frees ${frees} other item${frees === 1 ? "" : "s"}`;
      foot.append(pill);
    }
    card.append(foot);

    return card;
  }

  /** The 3px left edge: P0 first, then "nobody has touched this", then status. */
  private edgeColour(item: Item): string {
    if (item.priority === "p0") return "var(--p0)";
    if (this.isStale(item)) return "var(--line)";
    if (item.status === "active") return "var(--flight)";
    if (item.status === "blocked" || item.status === "waiting") return "var(--blocked)";
    return "var(--ready)";
  }

  private epicOf(item: Item): Item | undefined {
    const epics = deriveEpics(this.items);
    const epicIds = new Set(epics.map((e) => e.epic.id));
    if (epicIds.has(item.id)) return item;
    return nearestEpicAncestor(item, byId(this.items), epicIds);
  }

  private buildResizer(): HTMLElement {
    const resizer = el("div", { className: "detail-resizer" });
    resizer.setAttribute("role", "separator");
    resizer.setAttribute("aria-orientation", "vertical");
    resizer.setAttribute("aria-label", "Resize detail panel");
    resizer.addEventListener("pointerdown", (e) => {
      e.preventDefault();
      const startX = e.clientX;
      const startWidth = this.detailWidth;
      const onMove = (moveEvent: PointerEvent) => {
        const delta = startX - moveEvent.clientX;
        this.setDetailWidth(startWidth + delta);
      };
      const onUp = () => {
        document.removeEventListener("pointermove", onMove);
        document.removeEventListener("pointerup", onUp);
      };
      document.addEventListener("pointermove", onMove);
      document.addEventListener("pointerup", onUp);
    });
    return resizer;
  }

  private buildDetailPane(item: Item): HTMLElement {
    const pane = el("aside", { className: "detail-pane" });
    pane.setAttribute("aria-label", "Detail");

    const top = el("div", { className: "detail-header" });
    top.append(
      el("span", {
        className: `card-priority pri-${item.priority || "none"}`,
        textContent: item.priority ? item.priority.toUpperCase() : "—",
      }),
      el("span", { className: "card-id", textContent: item.id }),
    );
    const closeBtn = el("button", { className: "detail-close", textContent: "×" });
    closeBtn.setAttribute("aria-label", "Close");
    closeBtn.addEventListener("click", () => this.closeDetail());
    top.append(closeBtn);
    pane.append(top);

    const crumb = [this.projectNameOfItem(item)];
    const epic = this.epicOf(item);
    if (epic && epic.id !== item.id) crumb.push(epic.title);
    pane.append(el("p", { className: "detail-id crumb", textContent: crumb.join(" › ") }));
    pane.append(el("h2", { textContent: item.title }));

    if (item.gate && gatesFilter()(item)) {
      const banner = el("div", { className: "gate-banner" }, [
        el("span", { textContent: `Gate: ${item.gate}` }),
      ]);
      banner.append(this.buildGateResolve(item));
      pane.append(banner);
    }

    pane.append(this.buildActions(item));

    if (item.context) {
      pane.append(el("p", { className: "detail-context", textContent: item.context }));
    }

    const idIndex = byId(this.items);
    pane.append(this.buildDepSection("Blocked by", item.blockedBy ?? [], idIndex));
    pane.append(this.buildDepSection("Blocks", item.blocks ?? [], idIndex));

    const labels = visibleLabels(item);
    const labelSection = el("div", { className: "detail-section" }, [
      el("h3", { textContent: "Labels" }),
    ]);
    const labelWrap = el("div", { className: "card-labels" });
    if (labels.length === 0) {
      labelWrap.append(el("span", { className: "label-pill more", textContent: "none" }));
    }
    for (const label of labels) {
      labelWrap.append(
        el("span", { className: `label-pill${HOT_LABELS.has(label) ? " hot" : ""}`, textContent: label }),
      );
    }
    labelSection.append(labelWrap);
    pane.append(labelSection);

    if (item.crossBoardWarnings && item.crossBoardWarnings.length > 0) {
      const warn = el("div", { className: "cross-board-warnings" });
      for (const w of item.crossBoardWarnings) warn.append(el("p", { className: "cross-board-warning", textContent: w }));
      pane.append(warn);
    }

    if (item.history && item.history.length > 0) {
      const timeline = el("ul", { className: "detail-timeline" });
      for (const entry of item.history) {
        timeline.append(
          el("li", {
            className: "timeline-entry",
            textContent: `${entry.timestamp} · ${entry.fromStatus} → ${entry.toStatus}${entry.note ? ` — ${entry.note}` : ""}`,
          }),
        );
      }
      pane.append(el("h3", { textContent: "Timeline" }), timeline);
    }

    return pane;
  }

  private buildDepSection(label: string, ids: string[], idIndex: Map<string, Item>): HTMLElement {
    const section = el("div", { className: "detail-deps detail-section" });
    section.append(el("h3", { textContent: `${label} (${ids.length})` }));
    const list = el("ul", { className: "detail-dep-list" });
    for (const depId of ids) {
      const dep = idIndex.get(depId);
      const crossBoard = !dep; // not resolvable in this item set == cross-board or unknown
      const li = el("li", {
        className: `detail-dep-item${crossBoard ? " cross-board" : ""}`,
        textContent: dep ? `${dep.id} — ${dep.title}${isTerminal(dep) ? " (done)" : ""}` : `${depId} (cross-board, non-blocking)`,
      });
      list.append(li);
    }
    section.append(list);
    return section;
  }

  private buildFooter(): HTMLElement {
    const foot = el("div", { className: "board-foot" });
    const n = this.projectCount();
    foot.append(
      el("b", { textContent: "Live board." }),
      ` Reading ${n} board${n === 1 ? "" : "s"} of signed events straight from your relays. ` +
        "Nothing is cached in this browser — the page is rebuilt from those events on every load.",
    );
    return foot;
  }
}

/** Labels that always read as urgent — the prototype's HOT set. */
const HOT_LABELS: ReadonlySet<string> = new Set(["security", "bug", "test-flaky"]);

/** Which of the five foot-row treatments a status line gets. statusline.ts owns
 * the wording and is the only place the five cases are decided; this maps its
 * output onto the design's five treatments rather than re-deciding them, so the
 * two can never disagree about which case an item is in. */
function statusLineClass(line: string): string {
  if (line === "yours") return "who you";
  if (line.startsWith("waits on")) return "waiton";
  if (line === "agent working") return "who agent";
  if (line.startsWith("untouched")) return "stale";
  return "who";
}

function clampDetailWidth(px: number): number {
  const maxWidth = typeof window !== "undefined" ? window.innerWidth * 0.7 : 1000;
  return Math.min(Math.max(px, DETAIL_MIN), Math.max(maxWidth, DETAIL_MIN));
}

export function mountBoardWorkspace(
  container: HTMLElement,
  items: Item[],
  options: WorkspaceOptions = {},
): BoardWorkspace {
  return new BoardWorkspace(container, items, options);
}

export { RESPONSIVE_BREAKPOINT, CARD_CAP };
