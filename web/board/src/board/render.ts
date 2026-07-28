// The board workspace: gate rail + left tree + swimlaned columns + detail
// pane. Layout pattern (two/three-pane list+detail, resizable/closeable
// detail track) is vendored from moot's Feed.tsx / PostRow.tsx /
// CommentColumn.tsx (github.com/3dl-dev/moot, MIT, same owner) — ported as a
// LAYOUT PATTERN, not a literal file copy, because this app is vanilla
// TS+DOM (see main.ts's `el()` helper) and moot's components are React/JSX.
// What is vendored: the list-selects-into-detail interaction model, the
// closeable/resizable right track that reclaims width when nothing is
// selected, and Escape-to-close. See ready-61c for the attribution review.
import { applyFilters, assigneeBuckets, labelFrequency, NO_PRIORITY, priorityBuckets, UNASSIGNED, type FilterState } from "./filters";
import { buildEpicTree, buildFreesIndex, deriveEpics, freesCount, type EpicRollup, type EpicTreeNode } from "./graph";
import { columnize, gatesFilter } from "./views";
import { sortCards } from "./sort";
import { statusLine, daysSince } from "./statusline";
import { isTerminal, type Item } from "./types";
import { unimplementedWriter, type BoardWriter } from "./write";

export type SwimlaneMode = "project" | "epic" | "priority" | "off";

export interface WorkspaceOptions {
  /** The logged-in identity, for the "yours" status-line/detail rendering. */
  viewerId?: string;
  writer?: BoardWriter;
  detailWidth?: number;
}

const DETAIL_MIN = 180;
const DETAIL_DEFAULT = 340;
const LEFT_WIDTH = 206;
const RESPONSIVE_BREAKPOINT = 1000;

const EPIC_PALETTE = ["#7c9cff", "#ff9d7c", "#7cffb2", "#ffd27c", "#d67cff", "#7cf0ff", "#ff7ca0", "#c4ff7c"];

function colorForId(id: string): string {
  let h = 0;
  for (let i = 0; i < id.length; i++) h = (h * 31 + id.charCodeAt(i)) >>> 0;
  return EPIC_PALETTE[h % EPIC_PALETTE.length];
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

export class BoardWorkspace {
  private items: Item[];
  private readonly container: HTMLElement;
  private readonly writer: BoardWriter;
  private readonly viewerId?: string;

  private swimlane: SwimlaneMode = "project";
  private filters: FilterState = {};
  private scopeEpicId?: string;
  private selectedId?: string;
  private detailWidth: number;
  private transientError?: string;
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
    this.detailWidth = clampDetailWidth(options.detailWidth ?? DETAIL_DEFAULT);
    document.addEventListener("keydown", this.onKeydown);
    this.render();
  }

  destroy(): void {
    document.removeEventListener("keydown", this.onKeydown);
  }

  setItems(items: Item[]): void {
    this.items = items;
    this.render();
  }

  getState(): {
    swimlane: SwimlaneMode;
    filters: FilterState;
    scopeEpicId?: string;
    selectedId?: string;
    detailWidth: number;
  } {
    return {
      swimlane: this.swimlane,
      filters: this.filters,
      scopeEpicId: this.scopeEpicId,
      selectedId: this.selectedId,
      detailWidth: this.detailWidth,
    };
  }

  setSwimlane(mode: SwimlaneMode): void {
    this.swimlane = mode;
    this.render();
  }

  setFilters(filters: FilterState): void {
    this.filters = filters;
    this.render();
  }

  toggleLabelFilter(atom: string): void {
    const current = this.filters.label ?? [];
    const next = current.includes(atom) ? current.filter((l) => l !== atom) : [...current, atom];
    this.setFilters({ ...this.filters, label: next });
  }

  toggleEpicScope(epicId: string): void {
    this.scopeEpicId = this.scopeEpicId === epicId ? undefined : epicId;
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

  /** The single write call site. Always goes through `writer`; on rejection
   * (today, always — see write.ts) the card never moves and a transient
   * error is shown, rather than faking an optimistic success. */
  async handleDrop(itemId: string, toStatus: Item["status"]): Promise<void> {
    try {
      await this.writer.moveStatus(itemId, toStatus);
      this.transientError = undefined;
    } catch (err) {
      this.transientError = err instanceof Error ? err.message : String(err);
    }
    this.render();
  }

  async handleGateResolve(itemId: string, approve: boolean): Promise<void> {
    try {
      await this.writer.resolveGate(itemId, approve);
      this.transientError = undefined;
    } catch (err) {
      this.transientError = err instanceof Error ? err.message : String(err);
    }
    this.render();
  }

  private render(): void {
    this.container.replaceChildren();
    this.container.classList.add("board-root");

    this.container.append(this.buildGateRail());

    const workspace = el("div", { className: "board-workspace" });
    const detailOpen = this.selectedId !== undefined && byId(this.items).has(this.selectedId);
    workspace.classList.toggle("detail-open", detailOpen);
    workspace.style.gridTemplateColumns = detailOpen
      ? `${LEFT_WIDTH}px minmax(0, 1fr) ${this.detailWidth}px`
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
  }

  private buildGateRail(): HTMLElement {
    const gated = this.items.filter(gatesFilter());
    if (gated.length === 0) {
      return el("div", { className: "gate-rail empty", textContent: "Nothing needs you right now" });
    }
    const rail = el("div", { className: "gate-rail" }, [
      el("h2", { className: "gate-rail-heading", textContent: `${gated.length} awaiting your decision` }),
    ]);
    const list = el("ul", { className: "gate-list" });
    for (const item of gated) {
      const li = el("li", { className: "gate-item", dataset: { id: item.id } });
      li.append(
        el("span", { className: "gate-type", textContent: item.gate || item.waitingType || "gate" }),
        el("span", { className: "gate-item-id", textContent: item.id }),
        el("span", { className: "gate-item-title", textContent: item.title }),
      );
      li.addEventListener("click", () => this.selectItem(item.id));
      list.append(li);
    }
    rail.append(list);
    return rail;
  }

  private buildLeftTree(): HTMLElement {
    const tree = el("div", { className: "left-tree" });
    const epics = deriveEpics(this.items);
    const epicTree = buildEpicTree(epics);

    tree.append(el("h3", { className: "left-tree-heading", textContent: "Epics" }));
    const epicList = el("ul", { className: "epic-list" });
    for (const node of epicTree) epicList.append(this.buildEpicNode(node));
    tree.append(epicList);

    tree.append(el("h3", { className: "left-tree-heading", textContent: "Labels" }));
    const labelList = el("ul", { className: "label-list" });
    for (const { label, count } of labelFrequency(this.items)) {
      const li = el("li", { className: "label-list-item" });
      const active = (this.filters.label ?? []).includes(label);
      const pill = el("span", {
        className: `label-pill${active ? " active" : ""}`,
        textContent: `${label} (${count})`,
      });
      pill.addEventListener("click", () => this.toggleLabelFilter(label));
      li.append(pill);
      labelList.append(li);
    }
    tree.append(labelList);
    return tree;
  }

  private buildEpicNode(node: EpicTreeNode): HTMLElement {
    const { rollup } = node;
    const li = el("li", { className: "epic-node", dataset: { epicId: rollup.epic.id } });
    const active = this.scopeEpicId === rollup.epic.id;
    const row = el("div", { className: `epic-token${active ? " active" : ""}` });
    row.style.backgroundColor = colorForId(rollup.epic.id);
    row.append(
      el("span", { className: "epic-title", textContent: rollup.epic.title }),
      el("span", { className: "epic-rollup", textContent: `${rollup.closed}/${rollup.total}` }),
    );
    row.addEventListener("click", () => this.toggleEpicScope(rollup.epic.id));
    li.append(row);
    if (node.children.length > 0) {
      const childList = el("ul", { className: "epic-children" });
      for (const child of node.children) childList.append(this.buildEpicNode(child));
      li.append(childList);
    }
    return li;
  }

  private scopedItems(): Item[] {
    let items = this.items;
    if (this.scopeEpicId) {
      const epics = deriveEpics(this.items);
      const scoped = epics.find((r) => r.epic.id === this.scopeEpicId);
      if (scoped) {
        const ids = new Set([scoped.epic.id, ...scoped.descendants.map((d) => d.id)]);
        items = items.filter((i) => ids.has(i.id));
      }
    }
    return applyFilters(items, this.filters);
  }

  private buildCenter(): HTMLElement {
    const center = el("div", { className: "board-center" });
    center.append(this.buildFilterBar());

    const filtered = this.scopedItems();
    const lanes = this.groupIntoLanes(filtered);
    const lanesEl = el("div", { className: "swimlanes" });
    for (const lane of lanes) lanesEl.append(this.buildLane(lane.key, lane.label, lane.items, lane.rollup));
    center.append(lanesEl);
    return center;
  }

  private buildFilterBar(): HTMLElement {
    const bar = el("div", { className: "filter-bar" });

    const swimlaneSelect = el("select", { className: "swimlane-select" });
    for (const mode of ["project", "epic", "priority", "off"] as SwimlaneMode[]) {
      const opt = el("option", { value: mode, textContent: mode });
      if (mode === this.swimlane) opt.selected = true;
      swimlaneSelect.append(opt);
    }
    swimlaneSelect.addEventListener("change", () => this.setSwimlane(swimlaneSelect.value as SwimlaneMode));
    bar.append(el("label", { className: "filter-label", textContent: "Swimlanes " }, [swimlaneSelect]));

    bar.append(this.buildFacet("priority", priorityBuckets(this.items), this.filters.priority ?? [], (v) =>
      v === NO_PRIORITY ? "No priority" : v,
    ));
    bar.append(this.buildFacet("assignee", assigneeBuckets(this.items), this.filters.assignee ?? [], (v) =>
      v === UNASSIGNED ? "Unassigned" : v,
    ));
    bar.append(
      this.buildFacet(
        "status",
        [...new Set(this.items.map((i) => i.status))].sort(),
        this.filters.status ?? [],
        (v) => v,
      ),
    );
    bar.append(
      this.buildFacet(
        "gate",
        [...new Set(this.items.map((i) => i.gate).filter((g): g is string => !!g))].sort(),
        this.filters.gate ?? [],
        (v) => v,
      ),
    );

    if (this.scopeEpicId || (this.filters.label ?? []).length > 0) {
      const clear = el("button", { className: "clear-filters", textContent: "Clear filters" });
      clear.addEventListener("click", () => {
        this.scopeEpicId = undefined;
        this.setFilters({});
      });
      bar.append(clear);
    }

    return bar;
  }

  private buildFacet(
    dimension: keyof FilterState,
    buckets: string[],
    active: readonly string[],
    labelFor: (v: string) => string,
  ): HTMLElement {
    const wrap = el("div", { className: `facet facet-${dimension}` }, [
      el("span", { className: "facet-name", textContent: dimension }),
    ]);
    for (const bucket of buckets) {
      const isActive = active.includes(bucket);
      const chip = el("button", {
        className: `facet-chip${isActive ? " active" : ""}`,
        textContent: labelFor(bucket),
        dataset: { dimension, value: bucket },
      });
      chip.addEventListener("click", () => {
        const current = (this.filters[dimension] as readonly string[] | undefined) ?? [];
        const next = current.includes(bucket) ? current.filter((v) => v !== bucket) : [...current, bucket];
        this.setFilters({ ...this.filters, [dimension]: next });
      });
      wrap.append(chip);
    }
    return wrap;
  }

  private groupIntoLanes(items: Item[]): { key: string; label: string; items: Item[]; rollup?: EpicRollup }[] {
    if (this.swimlane === "off") {
      return [{ key: "off", label: "All items", items }];
    }
    if (this.swimlane === "priority") {
      const buckets = priorityBuckets(items);
      return buckets.map((b) => ({
        key: b,
        label: b === NO_PRIORITY ? "No priority" : b,
        items: items.filter((i) => (i.priority || NO_PRIORITY) === b),
      }));
    }
    if (this.swimlane === "project") {
      const groups = new Map<string, Item[]>();
      for (const item of items) {
        const key = item.project || item.boardCoord || "(unknown project)";
        const list = groups.get(key) ?? [];
        list.push(item);
        groups.set(key, list);
      }
      return [...groups.entries()]
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([key, its]) => ({ key, label: key, items: its }));
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
      .map(([key, its]) => ({
        key,
        label: key === noEpicKey ? noEpicKey : rollupById.get(key)?.epic.title ?? key,
        items: its,
        rollup: rollupById.get(key),
      }));
  }

  private buildLane(key: string, label: string, items: Item[], rollup?: EpicRollup): HTMLElement {
    const lane = el("div", { className: "swimlane", dataset: { lane: key } });
    const header = el("div", { className: "swimlane-header" }, [el("span", { textContent: label })]);
    if (rollup) header.append(el("span", { className: "swimlane-rollup", textContent: `${rollup.closed}/${rollup.total}` }));
    lane.append(header);

    const columns = columnize(items);
    const freesIndex = buildFreesIndex(this.items);
    const laneColumns = el("div", { className: "lane-columns" });
    for (const [colKey, colItems, targetStatus, colLabel] of [
      ["ready", columns.ready, "inbox", "Ready"],
      ["moving", columns.moving, "active", "Moving"],
      ["blocked", columns.blocked, "blocked", "Blocked"],
    ] as const) {
      laneColumns.append(this.buildColumn(colKey, colLabel, sortCards(colItems), freesIndex, targetStatus));
    }
    lane.append(laneColumns);
    return lane;
  }

  private buildColumn(
    key: string,
    label: string,
    items: Item[],
    freesIndex: ReturnType<typeof buildFreesIndex>,
    targetStatus: Item["status"],
  ): HTMLElement {
    const col = el("div", { className: "column", dataset: { column: key } });
    col.append(el("h4", { className: "column-header", textContent: `${label} (${items.length})` }));
    const cardList = el("div", { className: "card-list" });
    col.append(cardList);

    col.addEventListener("dragover", (e) => e.preventDefault());
    col.addEventListener("drop", (e) => {
      e.preventDefault();
      const itemId = e.dataTransfer?.getData("text/plain");
      if (itemId) void this.handleDrop(itemId, targetStatus);
    });

    for (const item of items) cardList.append(this.buildCard(item, freesIndex));
    return col;
  }

  private buildCard(item: Item, freesIndex: ReturnType<typeof buildFreesIndex>): HTMLElement {
    const card = el("div", {
      className: "card",
      draggable: true,
      dataset: { id: item.id, status: item.status },
    });
    card.addEventListener("dragstart", (e) => {
      e.dataTransfer?.setData("text/plain", item.id);
    });
    card.addEventListener("click", () => this.selectItem(item.id));

    const top = el("div", { className: "card-top" }, [
      el("span", { className: "card-priority", textContent: item.priority || "—" }),
      el("span", { className: "card-id", textContent: item.id }),
      el("span", { className: "card-age", textContent: `${daysSince(item.createdAt)}d` }),
    ]);
    card.append(top);

    if (this.swimlane === "off" && (item.project || item.boardCoord)) {
      card.append(el("span", { className: "project-chip", textContent: item.project || item.boardCoord! }));
    }

    card.append(el("div", { className: "card-title", textContent: item.title }));

    const labels = item.labels ?? [];
    if (labels.length > 0) {
      const labelWrap = el("div", { className: "card-labels" });
      for (const label of labels.slice(0, 3)) {
        labelWrap.append(el("span", { className: "label-pill outlined", textContent: label }));
      }
      if (labels.length > 3) {
        labelWrap.append(el("span", { className: "label-pill outlined more", textContent: `+${labels.length - 3}` }));
      }
      card.append(labelWrap);
    }

    card.append(el("div", { className: "card-status-line", textContent: statusLine(item, this.viewerId) }));

    const frees = freesCount(freesIndex, item.id);
    if (frees > 0) {
      card.append(el("span", { className: "card-frees", textContent: `frees ${frees}` }));
    }

    return card;
  }

  private buildResizer(): HTMLElement {
    const resizer = el("div", { className: "detail-resizer" });
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
    const pane = el("div", { className: "detail-pane" });
    const header = el("div", { className: "detail-header" }, [
      el("h2", { textContent: item.title }),
    ]);
    const closeBtn = el("button", { className: "detail-close", textContent: "×", ["aria-label" as "title"]: "Close" });
    closeBtn.addEventListener("click", () => this.closeDetail());
    header.append(closeBtn);
    pane.append(header);

    pane.append(el("p", { className: "detail-id", textContent: `${item.id} · ${item.project ?? item.boardCoord ?? ""}` }));

    if (item.gate && gatesFilter()(item)) {
      const banner = el("div", { className: "gate-banner" }, [
        el("span", { textContent: `Gate: ${item.gate}` }),
      ]);
      const approve = el("button", { className: "gate-approve", textContent: "Approve" });
      approve.addEventListener("click", () => void this.handleGateResolve(item.id, true));
      const deny = el("button", { className: "gate-deny", textContent: "Deny" });
      deny.addEventListener("click", () => void this.handleGateResolve(item.id, false));
      banner.append(approve, deny);
      pane.append(banner);
    }

    if (item.context) {
      pane.append(el("p", { className: "detail-context", textContent: item.context }));
    }

    const idIndex = byId(this.items);
    pane.append(this.buildDepSection("Blocked by", item.blockedBy ?? [], idIndex));
    pane.append(this.buildDepSection("Blocks", item.blocks ?? [], idIndex));

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
    const section = el("div", { className: "detail-deps" });
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

export { RESPONSIVE_BREAKPOINT };
