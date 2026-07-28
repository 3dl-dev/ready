import { describe, expect, it } from "vitest";
import { makeItem } from "./testitem";
import { buildEpicTree, buildFreesIndex, deriveEpics, freesCount } from "./graph";

describe("buildFreesIndex / freesCount", () => {
  it("counts how many OTHER items list this id in blockedBy", () => {
    const items = [
      makeItem({ id: "blocker" }),
      makeItem({ id: "dep1", blockedBy: ["blocker"] }),
      makeItem({ id: "dep2", blockedBy: ["blocker", "other"] }),
      makeItem({ id: "unrelated" }),
    ];
    const index = buildFreesIndex(items);
    expect(freesCount(index, "blocker")).toBe(2);
    expect(freesCount(index, "unrelated")).toBe(0);
  });
});

describe("deriveEpics", () => {
  it("derives epics STRUCTURALLY (has children), not from a level tag", () => {
    const items = [
      makeItem({ id: "epic1", level: "task" }), // no level=epic tag, still an epic
      makeItem({ id: "child1", parentId: "epic1", status: "done" }),
      makeItem({ id: "child2", parentId: "epic1", status: "active" }),
      makeItem({ id: "leaf", level: "epic" }), // level=epic tag but no children — NOT an epic
    ];
    const epics = deriveEpics(items);
    expect(epics.map((e) => e.epic.id)).toEqual(["epic1"]);
    expect(epics[0]?.total).toBe(2);
    expect(epics[0]?.closed).toBe(1);
  });

  it("rolls up over ALL descendants, recursively, not just direct children", () => {
    const items = [
      makeItem({ id: "grandparent" }),
      makeItem({ id: "parent", parentId: "grandparent" }),
      makeItem({ id: "child", parentId: "parent", status: "done" }),
    ];
    const epics = deriveEpics(items);
    const grandparentRollup = epics.find((e) => e.epic.id === "grandparent");
    expect(grandparentRollup?.total).toBe(2); // parent + child
    expect(grandparentRollup?.closed).toBe(1);
  });

  it("does not infinite-loop on a malformed parent cycle", () => {
    const items = [makeItem({ id: "a", parentId: "b" }), makeItem({ id: "b", parentId: "a" })];
    expect(() => deriveEpics(items)).not.toThrow();
  });
});

describe("buildEpicTree", () => {
  it("nests an epic under its nearest epic ancestor", () => {
    const items = [
      makeItem({ id: "root" }),
      makeItem({ id: "sub", parentId: "root" }),
      makeItem({ id: "leaf", parentId: "sub" }),
    ];
    const epics = deriveEpics(items);
    const tree = buildEpicTree(epics);
    expect(tree.map((n) => n.rollup.epic.id)).toEqual(["root"]);
    expect(tree[0]?.children.map((n) => n.rollup.epic.id)).toEqual(["sub"]);
  });
});
