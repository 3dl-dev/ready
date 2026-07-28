// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { makeItem } from "./testitem";
import { mountBoardWorkspace, type BoardWorkspace } from "./render";
import type { BoardWriter } from "./write";
import { WriteNotImplementedError } from "./write";

let container: HTMLElement;
let ws: BoardWorkspace | undefined;

beforeEach(() => {
  container = document.createElement("div");
  document.body.append(container);
});

afterEach(() => {
  ws?.destroy();
  ws = undefined;
  container.remove();
});

describe("gate rail", () => {
  it("collapses to a single quiet line when nothing is gated", () => {
    ws = mountBoardWorkspace(container, [makeItem({ status: "active" })]);
    const rail = container.querySelector(".gate-rail");
    expect(rail?.classList.contains("empty")).toBe(true);
    expect(rail?.textContent).toBe("Nothing needs you right now");
  });

  it("lists ONLY items matching GatesFilter, not every waiting item", () => {
    const gated = makeItem({ id: "gated1", status: "waiting", waitingType: "gate", gateMsgId: "m1", gate: "design" });
    const waitingNotGated = makeItem({ id: "wait1", status: "waiting", waitingType: "human" });
    ws = mountBoardWorkspace(container, [gated, waitingNotGated]);
    const rail = container.querySelector(".gate-rail");
    expect(rail?.classList.contains("empty")).toBe(false);
    const ids = [...container.querySelectorAll(".gate-item")].map((el) => el.getAttribute("data-id"));
    expect(ids).toEqual(["gated1"]);
  });
});

describe("columns render in the DOM", () => {
  it("puts each item's card under its column", () => {
    const items = [
      makeItem({ id: "r1", status: "inbox" }),
      makeItem({ id: "m1", status: "active" }),
      makeItem({ id: "b1", status: "blocked" }),
    ];
    ws = mountBoardWorkspace(container, items, { viewerId: "" });
    const cardIn = (colKey: string) =>
      [...container.querySelectorAll(`.column[data-column="${colKey}"] .card`)].map((el) => el.getAttribute("data-id"));
    expect(cardIn("ready")).toEqual(["r1"]);
    expect(cardIn("moving")).toEqual(["m1"]);
    expect(cardIn("blocked")).toEqual(["b1"]);
  });

  it("shows priority, id, age, title, up to 3 labels + N, status line, and frees N on a card", () => {
    const blocker = makeItem({
      id: "blocker",
      status: "active",
      priority: "p0",
      title: "Fix the thing",
      labels: ["a", "b", "c", "d"],
    });
    const dep = makeItem({ id: "dep", blockedBy: ["blocker"] });
    ws = mountBoardWorkspace(container, [blocker, dep]);
    const card = container.querySelector('.card[data-id="blocker"]')!;
    expect(card.querySelector(".card-priority")?.textContent).toBe("p0");
    expect(card.querySelector(".card-id")?.textContent).toBe("blocker");
    expect(card.querySelector(".card-title")?.textContent).toBe("Fix the thing");
    const labelTexts = [...card.querySelectorAll(".card-labels .label-pill")].map((n) => n.textContent);
    expect(labelTexts).toEqual(["a", "b", "c", "+1"]);
    expect(card.querySelector(".card-status-line")?.textContent).toBe("agent working");
    expect(card.querySelector(".card-frees")?.textContent).toBe("frees 1");
  });
});

describe("epics vs labels styling", () => {
  it("epic tokens are FILLED (own colour token), label pills stay OUTLINED — never the same class", () => {
    const epic = makeItem({ id: "epic1", title: "Epic One" });
    const child = makeItem({ id: "child1", parentId: "epic1", labels: ["bug"] });
    ws = mountBoardWorkspace(container, [epic, child]);
    const epicToken = container.querySelector(".epic-token");
    const labelPill = container.querySelector(".left-tree .label-pill");
    expect(epicToken).toBeTruthy();
    expect(labelPill).toBeTruthy();
    expect(epicToken?.classList.contains("label-pill")).toBe(false);
    expect(labelPill?.classList.contains("epic-token")).toBe(false);
    // epic token carries a background colour (filled); label pill does not set one.
    expect((epicToken as HTMLElement).style.backgroundColor).not.toBe("");
  });

  it("an item with no children is NOT an epic, even if it carries level=epic", () => {
    const leaf = makeItem({ id: "leaf", level: "epic" });
    ws = mountBoardWorkspace(container, [leaf]);
    expect(container.querySelector(".epic-node")).toBeNull();
  });
});

describe("selection and detail pane", () => {
  it("clicking a card opens the detail pane with title, context, deps, and board coord", () => {
    const other = makeItem({ id: "other", title: "Other item" });
    const item = makeItem({
      id: "sel",
      title: "Selected item",
      context: "Some context",
      blockedBy: ["other"],
      project: "ready",
    });
    ws = mountBoardWorkspace(container, [item, other]);
    (container.querySelector('.card[data-id="sel"]') as HTMLElement).click();

    const pane = container.querySelector(".detail-pane");
    expect(pane).toBeTruthy();
    expect(pane?.querySelector("h2")?.textContent).toBe("Selected item");
    expect(pane?.querySelector(".detail-context")?.textContent).toBe("Some context");
    expect(pane?.textContent).toContain("ready");
    expect(pane?.querySelector(".detail-dep-item")?.textContent).toContain("other");
  });

  it("cross-board (unresolvable) dep ids render as non-blocking, not as an error", () => {
    const item = makeItem({ id: "sel", blockedBy: ["other-project-xyz"] });
    ws = mountBoardWorkspace(container, [item]);
    (container.querySelector('.card[data-id="sel"]') as HTMLElement).click();
    const depItem = container.querySelector(".detail-dep-item.cross-board");
    expect(depItem?.textContent).toContain("cross-board, non-blocking");
  });

  it("gate banner names the gate type when Gate is set", () => {
    const item = makeItem({ id: "sel", status: "waiting", waitingType: "gate", gateMsgId: "m1", gate: "budget" });
    ws = mountBoardWorkspace(container, [item]);
    (container.querySelector('.card[data-id="sel"]') as HTMLElement).click();
    expect(container.querySelector(".gate-banner")?.textContent).toContain("budget");
  });

  it("closes via the × button", () => {
    const item = makeItem({ id: "sel" });
    ws = mountBoardWorkspace(container, [item]);
    ws.selectItem("sel");
    expect(container.querySelector(".detail-pane")).toBeTruthy();
    (container.querySelector(".detail-close") as HTMLElement).click();
    expect(container.querySelector(".detail-pane")).toBeNull();
  });

  it("closes via Escape", () => {
    const item = makeItem({ id: "sel" });
    ws = mountBoardWorkspace(container, [item]);
    ws.selectItem("sel");
    expect(container.querySelector(".detail-pane")).toBeTruthy();
    document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape" }));
    expect(container.querySelector(".detail-pane")).toBeNull();
  });

  it("the grid drops to two columns when nothing is selected, three when selected", () => {
    const item = makeItem({ id: "sel" });
    ws = mountBoardWorkspace(container, [item]);
    const grid = () => (container.querySelector(".board-workspace") as HTMLElement).style.gridTemplateColumns;
    expect(grid()).toBe("206px minmax(0, 1fr)");
    ws.selectItem("sel");
    expect(grid()).toBe("206px minmax(0, 1fr) 340px");
  });
});

describe("detail width clamp", () => {
  it("clamps to the 180px minimum", () => {
    ws = mountBoardWorkspace(container, [makeItem({ id: "sel" })]);
    ws.selectItem("sel");
    ws.setDetailWidth(10);
    expect(ws.getState().detailWidth).toBe(180);
  });

  it("defaults to 340px", () => {
    ws = mountBoardWorkspace(container, [makeItem({ id: "sel" })]);
    expect(ws.getState().detailWidth).toBe(340);
  });
});

describe("filters compose in the UI", () => {
  it("toggling a priority facet chip hides non-matching cards without dropping no-priority cards from the option list", () => {
    const withPrio = makeItem({ id: "p1item", priority: "p1", status: "active" });
    const noPrio = makeItem({ id: "noprio", priority: "", status: "active" });
    ws = mountBoardWorkspace(container, [withPrio, noPrio]);
    expect([...container.querySelectorAll('.facet-priority .facet-chip')].map((c) => c.textContent)).toContain(
      "No priority",
    );
    (container.querySelector('.facet-priority .facet-chip[data-value="p1"]') as HTMLElement).click();
    const cardIds = [...container.querySelectorAll(".card")].map((c) => c.getAttribute("data-id"));
    expect(cardIds).toEqual(["p1item"]);
  });

  it("clicking a label pill in the left tree filters the board to that label", () => {
    const tagged = makeItem({ id: "tagged", labels: ["security"], status: "active" });
    const untagged = makeItem({ id: "untagged", status: "active" });
    ws = mountBoardWorkspace(container, [tagged, untagged]);
    (container.querySelector(".left-tree .label-pill") as HTMLElement).click();
    const cardIds = [...container.querySelectorAll(".board-center .card")].map((c) => c.getAttribute("data-id"));
    expect(cardIds).toEqual(["tagged"]);
  });
});

describe("swimlanes", () => {
  it("off mode renders a single lane with a project chip per card", () => {
    const item = makeItem({ id: "a", project: "ready", status: "active" });
    ws = mountBoardWorkspace(container, [item]);
    ws.setSwimlane("off");
    expect(container.querySelectorAll(".swimlane")).toHaveLength(1);
    expect(container.querySelector(".card .project-chip")?.textContent).toBe("ready");
  });

  it("project mode groups into one lane per project", () => {
    const items = [
      makeItem({ id: "a", project: "ready", status: "active" }),
      makeItem({ id: "b", project: "galtrader", status: "active" }),
    ];
    ws = mountBoardWorkspace(container, items);
    expect(container.querySelectorAll(".swimlane")).toHaveLength(2);
  });

  it("epic mode groups descendants under their epic and shows the closed/total rollup", () => {
    const epic = makeItem({ id: "epicA", title: "Epic A" });
    const child = makeItem({ id: "childA", parentId: "epicA", status: "done" });
    ws = mountBoardWorkspace(container, [epic, child]);
    ws.setSwimlane("epic");
    const header = container.querySelector('.swimlane[data-lane="epicA"] .swimlane-header');
    expect(header?.textContent).toContain("Epic A");
    expect(header?.textContent).toContain("1/1");
  });
});

describe("write affordance (drag-and-drop)", () => {
  it("handleDrop calls the writer and shows a transient error on rejection (no silent success)", async () => {
    const item = makeItem({ id: "moveme", status: "inbox" });
    ws = mountBoardWorkspace(container, [item]);
    await ws.handleDrop("moveme", "active");
    expect(container.querySelector(".transient-error")?.textContent).toBeTruthy();
    expect(container.querySelector(".transient-error")?.textContent).toContain("moveStatus");
  });

  it("a custom writer is used when supplied — the interface is genuinely pluggable", async () => {
    let called: [string, string] | undefined;
    const writer: BoardWriter = {
      moveStatus: async (id, to) => {
        called = [id, to];
      },
      resolveGate: async () => {
        throw new WriteNotImplementedError("resolveGate");
      },
    };
    const item = makeItem({ id: "moveme", status: "inbox" });
    ws = mountBoardWorkspace(container, [item], { writer });
    await ws.handleDrop("moveme", "active");
    expect(called).toEqual(["moveme", "active"]);
    expect(container.querySelector(".transient-error")).toBeNull();
  });
});
