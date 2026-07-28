import { describe, expect, it } from "vitest";
import { makeItem } from "./testitem";
import { applyFilters, assigneeBuckets, NO_PRIORITY, priorityBuckets, UNASSIGNED } from "./filters";

describe("applyFilters", () => {
  it("composes across dimensions (AND)", () => {
    const items = [
      makeItem({ id: "a", status: "active", priority: "p1" }),
      makeItem({ id: "b", status: "active", priority: "p2" }),
      makeItem({ id: "c", status: "waiting", priority: "p1" }),
    ];
    const out = applyFilters(items, { status: ["active"], priority: ["p1"] });
    expect(out.map((i) => i.id)).toEqual(["a"]);
  });

  it("unassigned is a first-class bucket: assignee filter matches empty `for`", () => {
    const items = [makeItem({ id: "assigned", for: "alice" }), makeItem({ id: "empty", for: "" })];
    const out = applyFilters(items, { assignee: [UNASSIGNED] });
    expect(out.map((i) => i.id)).toEqual(["empty"]);
  });

  it("priority filter does not silently vanish no-priority items — NO_PRIORITY is selectable", () => {
    const items = [makeItem({ id: "p1item", priority: "p1" }), makeItem({ id: "noprio", priority: "" })];
    const out = applyFilters(items, { priority: [NO_PRIORITY] });
    expect(out.map((i) => i.id)).toEqual(["noprio"]);
  });

  it("label filter AND-composes multiple atoms (§13.12)", () => {
    const items = [
      makeItem({ id: "both", labels: ["bug", "security"] }),
      makeItem({ id: "one", labels: ["bug"] }),
    ];
    const out = applyFilters(items, { label: ["bug", "security"] });
    expect(out.map((i) => i.id)).toEqual(["both"]);
  });

  it("an empty filter dimension matches everything", () => {
    const items = [makeItem({ id: "a" }), makeItem({ id: "b" })];
    expect(applyFilters(items, {}).map((i) => i.id)).toEqual(["a", "b"]);
  });
});

describe("priorityBuckets / assigneeBuckets", () => {
  it("include the NO_PRIORITY / UNASSIGNED bucket only when present, sorted last", () => {
    const items = [makeItem({ priority: "p1", for: "alice" }), makeItem({ priority: "", for: "" })];
    expect(priorityBuckets(items)).toEqual(["p1", NO_PRIORITY]);
    expect(assigneeBuckets(items)).toEqual(["alice", UNASSIGNED]);
  });

  it("omit NO_PRIORITY/UNASSIGNED when every item has a value", () => {
    const items = [makeItem({ priority: "p1", for: "alice" }), makeItem({ priority: "p2", for: "bob" })];
    expect(priorityBuckets(items)).toEqual(["p1", "p2"]);
    expect(assigneeBuckets(items)).toEqual(["alice", "bob"]);
  });
});
