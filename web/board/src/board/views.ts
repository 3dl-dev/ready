// Port of pkg/views/views.go's predicates (board-fold-spec.md §13, normative:
// "These predicates ARE the board's columns; an independent client that
// renders different membership has diverged even if its item state is
// identical" — §13 preamble). This is the named bug this item exists to
// avoid: "in progress" is NOT `status === "active"` reimplemented from
// scratch, it IS WorkFilter, ported faithfully including its exact
// conjuncts. Every function below cites the Go line range it mirrors.
import { isBlocked, isTerminal, type GateType, type Item } from "./types";

export type Filter = (item: Item) => boolean;

/** Mirror of views.ReadyFilter (pkg/views/views.go:60-73, §13.3). NOT
 * terminal, NOT blocked, status !== "scheduled". ETA sorts, it never filters. */
export function readyFilter(): Filter {
  return (item) => {
    if (isTerminal(item)) return false;
    if (isBlocked(item)) return false;
    if (item.status === "scheduled") return false;
    return true;
  };
}

/** Mirror of views.WorkFilter (pkg/views/views.go:76-80, §13.4): status === "active", nothing else. */
export function workFilter(): Filter {
  return (item) => item.status === "active";
}

/** Mirror of views.PendingFilter (pkg/views/views.go:83-91, §13.5):
 * status in {waiting, scheduled, blocked}. */
export function pendingFilter(): Filter {
  return (item) => item.status === "waiting" || item.status === "scheduled" || item.status === "blocked";
}

/** Mirror of views.OverdueFilter (pkg/views/views.go:94-109, §13.6). Not
 * terminal, ETA non-empty, ETA parses, ETA < now. An unparseable ETA is
 * excluded, never treated as overdue. `now` is captured at construction. */
export function overdueFilter(now: Date = new Date()): Filter {
  const nowMs = now.getTime();
  return (item) => {
    if (isTerminal(item)) return false;
    if (!item.eta) return false;
    const t = Date.parse(item.eta);
    if (Number.isNaN(t)) return false;
    return t < nowMs;
  };
}

/** Mirror of views.GatesFilter (pkg/views/views.go:175-181, §13.10): status
 * === "waiting" AND waitingType === "gate" AND gateMsgId non-empty. All three
 * conjuncts matter (§13.10) — this is the gate rail's membership predicate. */
export function gatesFilter(): Filter {
  return (item) => item.status === "waiting" && item.waitingType === "gate" && !!item.gateMsgId;
}

/** Mirror of views.FocusFilter (pkg/views/views.go:185-196, §13.11):
 * ReadyFilter AND (gateType === "" OR item.gate === gateType). */
export function focusFilter(gateType: GateType | "" = ""): Filter {
  const ready = readyFilter();
  return (item) => {
    if (!ready(item)) return false;
    if (gateType === "") return true;
    return item.gate === gateType;
  };
}

/** Mirror of views.LabelFilter (pkg/views/views.go:202-211, §13.12): exact
 * match against a member of Labels, no substring/glob. Caller AND-composes
 * multiple label filters (§13.12, "one Apply per atom"). */
export function labelFilter(atom: string): Filter {
  return (item) => (item.labels ?? []).includes(atom);
}

/** Mirror of views.Apply (pkg/views/views.go:214-222): filters items,
 * preserving input order. */
export function apply(items: Item[], filter: Filter): Item[] {
  return items.filter(filter);
}

/**
 * The board's THREE columns (Ready / Moving / Blocked — the "Waiting on you"
 * column that used to exist was cut because it duplicates the gate rail).
 * This is a UI-layer PARTITION over the same three fold predicates above, not
 * a fourth predicate: every item lands in exactly one column because the
 * three source predicates are pairwise exclusive on the fields that matter
 * (WorkFilter needs status==="active"; PendingFilter's three statuses are
 * disjoint from "active"; ReadyFilter alone would double up with WorkFilter,
 * so "ready" here is readyFilter() MINUS workFilter() to keep the partition a
 * partition). Terminal items are members of none of the three columns.
 */
export interface Columns {
  ready: Item[];
  moving: Item[];
  blocked: Item[];
}

export function columnize(items: Item[]): Columns {
  const ready = readyFilter();
  const moving = workFilter();
  const blocked = pendingFilter();
  const out: Columns = { ready: [], moving: [], blocked: [] };
  for (const item of items) {
    if (moving(item)) {
      out.moving.push(item);
    } else if (blocked(item)) {
      out.blocked.push(item);
    } else if (ready(item)) {
      out.ready.push(item);
    }
    // else: terminal (or an unrecognized status that also fails readyFilter's
    // conjuncts) — not shown in any column, matching "Done (recent terminal)"
    // being explicitly out of the three-column design.
  }
  return out;
}
