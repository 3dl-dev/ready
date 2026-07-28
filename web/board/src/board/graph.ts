// Dep-graph and parent/child derivations that live at the UI layer, not the
// fold (board-fold-spec.md §13.14: "List order is NOT part of the fold" —
// the same holds for the two structural views below, which are board-445's
// own signals, not rd conventions).
import { isTerminal, type Item } from "./types";

/**
 * freesCount(id) = the number of OTHER items whose blockedBy carries `id`
 * (i.e. that this item's completion would unblock, in whole or in part).
 * "frees N" is a first-class signal per the design: an item blocking 9 others
 * outranks its own priority as a reason to work it. Built once over the
 * whole item set so per-card lookups are O(1); see FreesIndex.
 */
export type FreesIndex = ReadonlyMap<string, number>;

export function buildFreesIndex(items: Item[]): FreesIndex {
  const counts = new Map<string, number>();
  for (const item of items) {
    for (const blockerId of item.blockedBy ?? []) {
      counts.set(blockerId, (counts.get(blockerId) ?? 0) + 1);
    }
  }
  return counts;
}

export function freesCount(index: FreesIndex, id: string): number {
  return index.get(id) ?? 0;
}

/**
 * Epics are derived STRUCTURALLY: an item with children. Do NOT key off the
 * level tag — across ready's 761 item events only 4 carry level=epic while
 * 576 carry a parent (item spec, ready-445). An item is an epic iff at least
 * one other item's parentId equals its id.
 */
export interface EpicRollup {
  epic: Item;
  /** Direct AND indirect descendants (recursive), for the closed/total rollup. */
  descendants: Item[];
  closed: number;
  total: number;
}

export function deriveEpics(items: Item[]): EpicRollup[] {
  const childrenOf = new Map<string, Item[]>();
  for (const item of items) {
    if (!item.parentId) continue;
    const list = childrenOf.get(item.parentId) ?? [];
    list.push(item);
    childrenOf.set(item.parentId, list);
  }

  function collectDescendants(id: string, seen: Set<string>): Item[] {
    const direct = childrenOf.get(id) ?? [];
    const out: Item[] = [];
    for (const child of direct) {
      if (seen.has(child.id)) continue; // guards a malformed parent cycle
      seen.add(child.id);
      out.push(child);
      out.push(...collectDescendants(child.id, seen));
    }
    return out;
  }

  const epics: EpicRollup[] = [];
  for (const item of items) {
    if (!childrenOf.has(item.id)) continue;
    const descendants = collectDescendants(item.id, new Set([item.id]));
    const closed = descendants.filter(isTerminal).length;
    epics.push({ epic: item, descendants, closed, total: descendants.length });
  }
  return epics;
}

/** True iff `item` has at least one descendant, i.e. iff it would appear in
 * deriveEpics' output. Cheaper than deriveEpics(items).some(...) for a
 * single-item check (e.g. a card deciding whether to render the epic token). */
export function hasChildren(items: Item[], id: string): boolean {
  return items.some((i) => i.parentId === id);
}

/** Nests epics under their nearest epic ancestor (nested epics), so the left
 * tree can render "epics (nested)" per the item spec. An epic whose parent is
 * not itself an epic (or has no parent) is a root. */
export interface EpicTreeNode {
  rollup: EpicRollup;
  children: EpicTreeNode[];
}

export function buildEpicTree(epics: EpicRollup[]): EpicTreeNode[] {
  const byId = new Map(epics.map((r) => [r.epic.id, r]));
  const nodes = new Map<string, EpicTreeNode>();
  for (const rollup of epics) nodes.set(rollup.epic.id, { rollup, children: [] });

  const roots: EpicTreeNode[] = [];
  for (const rollup of epics) {
    const node = nodes.get(rollup.epic.id)!;
    const parentId = rollup.epic.parentId;
    const parentNode = parentId ? nodes.get(parentId) : undefined;
    if (parentNode && byId.has(parentId!)) {
      parentNode.children.push(node);
    } else {
      roots.push(node);
    }
  }
  return roots;
}
