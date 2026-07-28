import { describe, expect, it } from "vitest";
import { makeItem } from "./testitem";
import { priorityRank, sortCards } from "./sort";

describe("priorityRank", () => {
  it("mirrors cmd/rd/ready.go's priorityOrder: p0=0 .. p3=3, unset/unknown=9", () => {
    expect(priorityRank("p0")).toBe(0);
    expect(priorityRank("p1")).toBe(1);
    expect(priorityRank("p2")).toBe(2);
    expect(priorityRank("p3")).toBe(3);
    expect(priorityRank("")).toBe(9);
    expect(priorityRank("bogus")).toBe(9);
  });
});

describe("sortCards", () => {
  it("sorts by priority first", () => {
    const items = [makeItem({ id: "low", priority: "p3" }), makeItem({ id: "high", priority: "p0" })];
    expect(sortCards(items).map((i) => i.id)).toEqual(["high", "low"]);
  });

  it("does not vanish no-priority items — they sort last, not excluded", () => {
    const items = [makeItem({ id: "none", priority: "" }), makeItem({ id: "p2item", priority: "p2" })];
    const sorted = sortCards(items);
    expect(sorted.map((i) => i.id)).toEqual(["p2item", "none"]);
    expect(sorted).toHaveLength(2);
  });

  it("within equal priority, sorts by frees-N descending — an item blocking more outranks", () => {
    const blocker9 = makeItem({ id: "blocker9", priority: "p1" });
    const blocker1 = makeItem({ id: "blocker1", priority: "p1" });
    const dependents9 = Array.from({ length: 9 }, (_, i) => makeItem({ id: `dep9-${i}`, blockedBy: ["blocker9"] }));
    const dependents1 = [makeItem({ id: "dep1-0", blockedBy: ["blocker1"] })];
    const allItems = [blocker1, ...dependents1, blocker9, ...dependents9];
    const sorted = sortCards(allItems).filter((i) => i.id === "blocker1" || i.id === "blocker9");
    expect(sorted.map((i) => i.id)).toEqual(["blocker9", "blocker1"]);
  });

  it("priority outranks frees-N (sort order is priority-major, not frees-major)", () => {
    const highPriorityNoFrees = makeItem({ id: "hp", priority: "p0" });
    const lowPriorityHighFrees = makeItem({ id: "lp", priority: "p3" });
    const dependents = Array.from({ length: 9 }, (_, i) => makeItem({ id: `d${i}`, blockedBy: ["lp"] }));
    const sorted = sortCards([lowPriorityHighFrees, highPriorityNoFrees, ...dependents]);
    expect(sorted[0]?.id).toBe("hp");
  });

  it("within equal priority and frees-N, sorts longest-untouched first (oldest updatedAt)", () => {
    const older = makeItem({ id: "older", priority: "p1", updatedAt: 1000 });
    const newer = makeItem({ id: "newer", priority: "p1", updatedAt: 2000 });
    expect(sortCards([newer, older]).map((i) => i.id)).toEqual(["older", "newer"]);
  });
});
