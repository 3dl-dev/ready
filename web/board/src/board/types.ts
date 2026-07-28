// Item is the client-side mirror of pkg/state.Item's JSON surface
// (pkg/state/state.go:86-156), the value type the live fold
// (pkg/sync.ProjectItems, pkg/sync/nostrproject.go:146) returns. Field names
// are camelCased from the Go json tags; everything the board UI (ready-445)
// needs to build columns, swimlanes, filters and the detail pane is here.
//
// NOTE ON PROVENANCE (read this before wiring real data in): as of this
// writing there is no TypeScript projection from verified nostr events to
// Item[] — that fold, with its vector-gate + live-parity-gate conformance
// suite, is ready-35b's job (docs/design/board-fold-spec.md is the contract;
// see pkg/views/views.go for the predicates ported into ./views.ts below).
// Building a second, lighter-weight event->Item fold here would duplicate
// that conformance surface and risk silently diverging from it — the exact
// failure mode the spec exists to prevent. So this module, and every module
// in this directory, operates on Item[] handed to it; see ../lib/itemsource.ts
// for the seam that will eventually be wired to ready-35b's fold.
export interface HistoryEntry {
  timestamp: string;
  fromStatus: string;
  toStatus: string;
  note?: string;
  actor?: string;
}

export type Status =
  | "inbox"
  | "active"
  | "scheduled"
  | "waiting"
  | "blocked"
  | "done"
  | "cancelled"
  | "failed"
  | string;

/** Gate categories (state.go:116 comment): the escalation CATEGORY, set only
 * alongside status=waiting/waiting_type=gate (views.go:175-181, §13.10). */
export type GateType = "budget" | "design" | "scope" | "review" | "human" | "stall" | "";

export interface Item {
  id: string;
  msgId?: string;
  title: string;
  context?: string;
  type: string;
  level?: string;
  project?: string;
  /** Board coordinate ("30301:<owner>:<d>") this item was read from. Not part
   * of state.Item's JSON surface (a single-board rd process doesn't need it)
   * but the board UI is explicitly cross-board ("across all boards at once"),
   * so the client needs to know provenance per item. */
  boardCoord?: string;

  for: string;
  by?: string;

  priority: string;
  status: Status;
  eta?: string;
  due?: string;

  parentId?: string;
  blockedBy?: string[];
  blocks?: string[];

  gate?: GateType | string;
  waitingOn?: string;
  waitingType?: string;
  waitingSince?: string;
  gateMsgId?: string;

  /** Unix nanoseconds, matching state.Item.CreatedAt/UpdatedAt (state.go:129-131). */
  createdAt: number;
  updatedAt: number;

  history?: HistoryEntry[];
  labels?: string[];
  labelWarnings?: string[];
  /** Mirror of state.Item.CrossCampfireWarnings (state.go:155). Per
   * board-fold-spec.md §8.9/§15.2 the live nostr fold does NOT populate this
   * today (an open spec question) — it is carried here only so the detail
   * pane can render it the day the fold starts filling it in, without a
   * second round of UI work. Until then it is always empty. */
  crossBoardWarnings?: string[];
}

export const TERMINAL_STATUSES: ReadonlySet<string> = new Set(["done", "cancelled", "failed"]);

/** Mirror of state.IsTerminal (pkg/state/state.go:1040-1042). */
export function isTerminal(item: Item): boolean {
  return TERMINAL_STATUSES.has(item.status);
}

/** Mirror of state.IsBlocked (pkg/state/state.go:1035-1038). */
export function isBlocked(item: Item): boolean {
  return item.status === "blocked";
}
