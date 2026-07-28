// TS port of pkg/views/views.go — the board's column predicates
// (board-fold-spec.md §13). These predicates ARE the board's columns; a
// client rendering different membership has diverged even with an identical
// item map.

import type { Item } from "./state";
import { StatusActive, StatusScheduled, StatusWaiting, StatusBlocked, isTerminal, isBlocked } from "./state";

export const ViewReady = "ready";
export const ViewWork = "work";
export const ViewPending = "pending";
export const ViewOverdue = "overdue";
export const ViewDelegated = "delegated";
export const ViewMyWork = "my-work";
export const ViewGates = "gates";
export const ViewFocus = "focus";

export type Filter = (item: Item) => boolean;

/** readyFilter mirrors views.go's ReadyFilter (spec §13.3). */
export function readyFilter(): Filter {
  return (item) => {
    if (isTerminal(item)) return false;
    if (isBlocked(item)) return false;
    if (item.status === StatusScheduled) return false;
    return true;
  };
}

/** workFilter mirrors views.go's WorkFilter (spec §13.4). */
export function workFilter(): Filter {
  return (item) => item.status === StatusActive;
}

/** pendingFilter mirrors views.go's PendingFilter (spec §13.5). */
export function pendingFilter(): Filter {
  return (item) => item.status === StatusWaiting || item.status === StatusScheduled || item.status === StatusBlocked;
}

/** overdueFilter mirrors views.go's OverdueFilter (spec §13.6). `now` is
 * captured at CONSTRUCTION time, not per-item — matching the Go closure's
 * `now := time.Now()` and the open question in spec §15.6. */
export function overdueFilter(now: Date = new Date()): Filter {
  const nowMs = now.getTime();
  return (item) => {
    if (isTerminal(item)) return false;
    if (!item.eta) return false;
    const parsed = parseRFC3339(item.eta);
    if (parsed === null) return false;
    return parsed < nowMs;
  };
}

/** parseRFC3339 returns the parsed time in epoch ms, or null if `s` is not a
 * valid RFC3339 timestamp — mirrors Go's time.Parse(time.RFC3339, ...)
 * failing closed (an unparseable ETA is excluded, never overdue). Rejects
 * anything Date.parse would accept more loosely than RFC3339 (e.g. a bare
 * date with no time component). */
function parseRFC3339(s: string): number | null {
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})$/.test(s)) return null;
  const ms = Date.parse(s);
  return Number.isNaN(ms) ? null : ms;
}

function identitySet(identity: string): Set<string> | null {
  return identity === "" ? null : new Set([identity]);
}

/** delegatedFilterSet mirrors views.go's DelegatedFilterSet (spec §13.7). */
export function delegatedFilterSet(idset: Set<string> | null): Filter {
  return (item) => {
    if (!idset || idset.size === 0) return false;
    return idset.has(item.for) && item.by !== undefined && item.by !== "" && !idset.has(item.by) && item.status === StatusActive;
  };
}

export function delegatedFilter(identity: string): Filter {
  return delegatedFilterSet(identitySet(identity));
}

/** myWorkFilterSet mirrors views.go's MyWorkFilterSet (spec §13.8). */
export function myWorkFilterSet(idset: Set<string> | null): Filter {
  return (item) => {
    if (!idset || idset.size === 0) return false;
    return !!item.by && idset.has(item.by) && !isTerminal(item);
  };
}

export function myWorkFilter(identity: string): Filter {
  return myWorkFilterSet(identitySet(identity));
}

/** gatesFilter mirrors views.go's GatesFilter (spec §13.10). */
export function gatesFilter(): Filter {
  return (item) => item.status === StatusWaiting && item.waiting_type === "gate" && !!item.gate_msg_id;
}

/** focusFilter mirrors views.go's FocusFilter (spec §13.11). */
export function focusFilter(gateType: string): Filter {
  const ready = readyFilter();
  return (item) => {
    if (!ready(item)) return false;
    if (gateType === "") return true;
    return item.gate === gateType;
  };
}

/** labelFilter mirrors views.go's LabelFilter (spec §13.12) — exact match,
 * no substring/glob. */
export function labelFilter(atom: string): Filter {
  return (item) => (item.labels ?? []).includes(atom);
}

/** named mirrors views.go's Named: dispatches the eight view names, nil (here
 * null) for an unknown name (spec §13.2). Note ViewFocus wires to
 * focusFilter("") — the un-parameterized form. */
export function named(viewName: string, identity: string): Filter | null {
  switch (viewName) {
    case ViewReady:
      return readyFilter();
    case ViewWork:
      return workFilter();
    case ViewPending:
      return pendingFilter();
    case ViewOverdue:
      return overdueFilter();
    case ViewDelegated:
      return delegatedFilter(identity);
    case ViewMyWork:
      return myWorkFilter(identity);
    case ViewGates:
      return gatesFilter();
    case ViewFocus:
      return focusFilter("");
    default:
      return null;
  }
}

/** allNames mirrors views.go's AllNames. */
export function allNames(): string[] {
  return [ViewReady, ViewWork, ViewPending, ViewOverdue, ViewDelegated, ViewMyWork, ViewGates, ViewFocus];
}

/** apply mirrors views.go's Apply: filters items, preserving input order. */
export function apply(items: Item[], f: Filter): Item[] {
  return items.filter(f);
}
