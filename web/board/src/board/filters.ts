// Composable board filters (item spec): status, gate (typed), priority,
// label, assignee. All compose (AND across dimensions, OR within a
// dimension's selected set — the usual facet-filter shape). Unassigned is a
// first-class bucket (assignee === UNASSIGNED matches item.for === "").
// Priority "none" is a first-class bucket too, so the ~46% of items with no
// priority stay selectable rather than silently vanishing when a priority
// filter is active.
import type { GateType, Item } from "./types";

export const UNASSIGNED = "__unassigned__";
export const NO_PRIORITY = "__no_priority__";

export interface FilterState {
  status?: readonly string[];
  gate?: readonly GateType[];
  priority?: readonly string[]; // "p0".."p3" or NO_PRIORITY
  label?: readonly string[]; // AND-composed (§13.12: one Apply per atom)
  assignee?: readonly string[]; // pubkey/email or UNASSIGNED
}

function matchesSet(value: string, set: readonly string[] | undefined): boolean {
  if (!set || set.length === 0) return true;
  return set.includes(value);
}

export function applyFilters(items: Item[], filters: FilterState): Item[] {
  return items.filter((item) => {
    if (!matchesSet(item.status, filters.status)) return false;
    if (filters.gate && filters.gate.length > 0 && !filters.gate.includes((item.gate ?? "") as GateType)) {
      return false;
    }
    if (filters.priority && filters.priority.length > 0) {
      const bucket = item.priority === "" || item.priority == null ? NO_PRIORITY : item.priority;
      if (!filters.priority.includes(bucket)) return false;
    }
    if (filters.label && filters.label.length > 0) {
      const labels = item.labels ?? [];
      for (const atom of filters.label) {
        if (!labels.includes(atom)) return false; // AND-composed, §13.12
      }
    }
    if (filters.assignee && filters.assignee.length > 0) {
      const bucket = item.for === "" || item.for == null ? UNASSIGNED : item.for;
      if (!filters.assignee.includes(bucket)) return false;
    }
    return true;
  });
}

/** Distinct priority buckets present in `items`, NO_PRIORITY included when at
 * least one item has an empty priority — feeds the filter UI's checklist so
 * "no priority" is always an offered, clickable option rather than an
 * implicit and easily-forgotten default. */
export function priorityBuckets(items: Item[]): string[] {
  const set = new Set<string>();
  for (const item of items) {
    set.add(item.priority === "" || item.priority == null ? NO_PRIORITY : item.priority);
  }
  return [...set].sort((a, b) => {
    if (a === NO_PRIORITY) return 1;
    if (b === NO_PRIORITY) return -1;
    return a.localeCompare(b);
  });
}

/** Distinct assignee buckets present in `items`, UNASSIGNED included when at
 * least one item has an empty `for`. */
export function assigneeBuckets(items: Item[]): string[] {
  const set = new Set<string>();
  for (const item of items) {
    set.add(item.for === "" || item.for == null ? UNASSIGNED : item.for);
  }
  return [...set].sort((a, b) => {
    if (a === UNASSIGNED) return 1;
    if (b === UNASSIGNED) return -1;
    return a.localeCompare(b);
  });
}

/** Label -> occurrence count across `items`, descending by frequency then
 * alphabetical — feeds the left tree's label list ("by frequency, with
 * counts"). */
export function labelFrequency(items: Item[]): { label: string; count: number }[] {
  const counts = new Map<string, number>();
  for (const item of items) {
    for (const label of item.labels ?? []) {
      counts.set(label, (counts.get(label) ?? 0) + 1);
    }
  }
  return [...counts.entries()]
    .map(([label, count]) => ({ label, count }))
    .sort((a, b) => b.count - a.count || a.label.localeCompare(b.label));
}
