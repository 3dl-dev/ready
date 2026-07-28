// TS port of pkg/state.Item / HistoryEntry and the status lattice constants
// (pkg/state/state.go:28-171). This is the value type the fold (fold.ts)
// materializes and pkg/views' predicates (views.ts) operate on — spec §1.3,
// §5, §7.
//
// created_at / updated_at are unix-nanosecond int64 in the Go type. A bare
// JS `number` cannot hold an arbitrary int64 exactly (Number.MAX_SAFE_INTEGER
// = 2^53-1), so this port carries them as `bigint` throughout — never
// `number` — matching the vector file's decimal-string encoding (spec §4.8,
// board-fold-spec.md). Item is otherwise a straight field-for-field mirror
// of state.Item's JSON tags; see the table at board-fold-spec.md §5.1.

export interface HistoryEntry {
  timestamp: string;
  from_status: string;
  to_status: string;
  changed_by: string;
  note?: string;
}

export interface Item {
  id: string;
  msg_id: string;
  // campfire_id intentionally omitted: never set by the nostr fold (§5.3),
  // and the JSON surface must not carry it (omitempty on the Go side).

  title: string;
  context?: string;
  description?: string;
  type: string;
  level?: string;
  project?: string;
  for: string;
  by?: string;

  priority: string;
  status: string;
  eta?: string;
  due?: string;

  parent_id?: string;
  blocked_by?: string[];
  blocks?: string[];

  gate?: string;

  waiting_on?: string;
  waiting_type?: string;
  waiting_since?: string;

  gate_msg_id?: string;

  created_at: bigint;
  updated_at: bigint;

  history?: HistoryEntry[];

  labels?: string[];
  label_warnings?: string[]; // never populated by the nostr fold (§10.2) — carried only so the field can be asserted absent.

  cross_campfire_warnings?: string[]; // never populated by the nostr fold (§8.9/§14.9).
}

// Status lattice (pkg/state/state.go:28-37, spec §7.1).
export const StatusInbox = "inbox";
export const StatusActive = "active";
export const StatusScheduled = "scheduled";
export const StatusWaiting = "waiting";
export const StatusBlocked = "blocked";
export const StatusDone = "done";
export const StatusCancelled = "cancelled";
export const StatusFailed = "failed";

// TerminalStatuses (pkg/state/state.go:78-82, spec §7.2).
const TERMINAL: ReadonlySet<string> = new Set([StatusDone, StatusCancelled, StatusFailed]);

export function isTerminal(item: Item): boolean {
  return TERMINAL.has(item.status);
}

export function isBlocked(item: Item): boolean {
  return item.status === StatusBlocked;
}

/** encodeItem mirrors state.Item's Go JSON encoding (field tags in
 * pkg/state/state.go:86-156) INCLUDING omitempty — an empty string or
 * zero-length array is dropped from the output, exactly as Go's
 * encoding/json does — and internal/foldvectors.EncodeItem's decimal-string
 * re-encoding of created_at/updated_at (spec §4.8). This is the single
 * function both vectors.test.ts (comparing against the committed vector
 * file) and scripts/live-parity.mjs (comparing against `rd list --json`)
 * must go through, so the two proofs can never silently diverge on encoding. */
export function encodeItem(item: Item): Record<string, unknown> {
  const out: Record<string, unknown> = {};
  const str = (key: string, v: string | undefined, alwaysInclude = false): void => {
    if (alwaysInclude || (v !== undefined && v !== "")) out[key] = v ?? "";
  };
  const arr = (key: string, v: string[] | undefined): void => {
    if (v && v.length > 0) out[key] = v;
  };

  out.id = item.id;
  out.msg_id = item.msg_id;
  str("title", item.title, true);
  str("context", item.context);
  str("description", item.description);
  out.type = item.type;
  str("level", item.level);
  str("project", item.project);
  out.for = item.for ?? "";
  str("by", item.by);
  out.priority = item.priority;
  out.status = item.status;
  str("eta", item.eta);
  str("due", item.due);
  str("parent_id", item.parent_id);
  arr("blocked_by", item.blocked_by);
  arr("blocks", item.blocks);
  str("gate", item.gate);
  str("waiting_on", item.waiting_on);
  str("waiting_type", item.waiting_type);
  str("waiting_since", item.waiting_since);
  str("gate_msg_id", item.gate_msg_id);
  out.created_at = item.created_at.toString(10);
  out.updated_at = item.updated_at.toString(10);
  if (item.history && item.history.length > 0) {
    out.history = item.history.map((h) => {
      const he: Record<string, unknown> = {
        timestamp: h.timestamp,
        from_status: h.from_status,
        to_status: h.to_status,
        changed_by: h.changed_by,
      };
      if (h.note !== undefined && h.note !== "") he.note = h.note;
      return he;
    });
  }
  arr("labels", item.labels);
  arr("label_warnings", item.label_warnings);
  arr("cross_campfire_warnings", item.cross_campfire_warnings);
  return out;
}
