// Card sort order (item spec): priority, then frees-N descending, then
// longest-untouched first. Applies in every swimlane mode. This is a
// board-445 UI concern, not a fold concern — board-fold-spec.md §15.7 notes
// even rd's own CLI sort ("sortByPriorityETA") is informative/CLI-side, not
// part of the fold, so there is no "the" sort to port; this is board-445's
// own, spec'd in its item text.
import { buildFreesIndex, freesCount, type FreesIndex } from "./graph";
import type { Item } from "./types";

/** Mirror of cmd/rd/ready.go's priorityOrder (p0=0 ... p3=3, anything else
 * (including "") = 9) — the exact ranking already used by rd's own CLI sort,
 * so p0 outranks p3 outranks "no priority" but "no priority" still ranks
 * (never excluded — the ~46% of items with no priority must not vanish). */
export function priorityRank(priority: string): number {
  switch (priority) {
    case "p0":
      return 0;
    case "p1":
      return 1;
    case "p2":
      return 2;
    case "p3":
      return 3;
    default:
      return 9;
  }
}

export function sortCards(items: Item[]): Item[] {
  const index = buildFreesIndex(items);
  return [...items].sort((a, b) => compareCards(a, b, index));
}

export function compareCards(a: Item, b: Item, freesIndex: FreesIndex): number {
  const pa = priorityRank(a.priority);
  const pb = priorityRank(b.priority);
  if (pa !== pb) return pa - pb;

  const fa = freesCount(freesIndex, a.id);
  const fb = freesCount(freesIndex, b.id);
  if (fa !== fb) return fb - fa; // higher frees-N first

  // Longest-untouched first: smaller (older) updatedAt sorts first.
  if (a.updatedAt !== b.updatedAt) return a.updatedAt - b.updatedAt;

  // Stable, total-order tiebreak so two items can never compare equal and
  // leave ordering to array-sort implementation details.
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}
