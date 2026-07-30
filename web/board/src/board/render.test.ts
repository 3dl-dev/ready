// @vitest-environment jsdom
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { makeItem } from "./testitem";
import { mountBoardWorkspace, type BoardWorkspace } from "./render";
import type { BoardWriter } from "./write";
import { unimplementedWriter, WriteNotImplementedError } from "./write";

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

  it("says 'Waiting on you' and states the count against the open total", () => {
    const gated = makeItem({ id: "gated1", status: "waiting", waitingType: "gate", gateMsgId: "m1", gate: "design" });
    const other = makeItem({ id: "o1", status: "active" });
    const closed = makeItem({ id: "o2", status: "done" });
    ws = mountBoardWorkspace(container, [gated, other, closed]);
    expect(container.querySelector(".gate-rail-heading")?.textContent).toBe("Waiting on you");
    // "of 2", not "of 3": the closed item is not open work.
    expect(container.querySelector(".gate-rail-sub")?.textContent).toBe(
      "1 of 2 — the only ones that cannot move without your decision.",
    );
  });
});

// ready-56b. None of the chrome below existed on the deployed page: no mark, no
// tally, no search, no sort note, no card cap, no footer. Each is asserted by
// its rendered STRING, because "the element exists" is what a grep of the
// bundle proved while the page still looked like a debug dump.
describe("page chrome (docs/design/board-prototype.html)", () => {
  it("renders the mark, the live tally, the search box and the New item button", () => {
    const items = [
      makeItem({ id: "a", status: "active", project: "ready" }),
      makeItem({ id: "b", status: "inbox", project: "galtrader" }),
      makeItem({ id: "c", status: "done", project: "ready" }),
    ];
    ws = mountBoardWorkspace(container, items, {
      boards: [
        { coord: "30301:owner:ready", title: "ready" },
        { coord: "30301:owner:galtrader", title: "galtrader" },
      ],
    });
    expect(container.querySelector(".mark")?.textContent).toBe("ready / board");
    // 2 open (the done one is not), 2 projects, and 2 SHOWN — "shown" counts
    // cards on the board, and a terminal item has no column, so it is neither
    // open nor shown even though no filter excluded it.
    expect(container.querySelector(".tally")?.textContent).toBe("2 open · 2 projects · 2 shown");
    expect(container.querySelector("input.find")?.getAttribute("placeholder")).toBe(
      "Filter by word or id",
    );
    expect(container.querySelector(".newbtn")?.textContent).toBe("New item");
  });

  it("states the sort rule under the filter bar", () => {
    ws = mountBoardWorkspace(container, [makeItem({ status: "active" })]);
    expect(container.querySelector(".sortnote")?.textContent).toBe(
      "Sorted by priority, then by how much other work it frees, then by longest untouched.",
    );
  });

  it("offers the two flag chips the design calls for", () => {
    ws = mountBoardWorkspace(container, [makeItem({ status: "active" })]);
    const chips = [...container.querySelectorAll(".filter-bar .chip")].map((c) => c.textContent);
    expect(chips).toContain("Untouched 7+ days");
    expect(chips).toContain("Unblocks others");
  });

  it("caps a column at 6 cards and discloses the rest behind 'Show all N'", () => {
    const items = Array.from({ length: 9 }, (_, i) =>
      makeItem({ id: `i${i}`, status: "active", project: "ready" }),
    );
    ws = mountBoardWorkspace(container, items);
    const cards = () => container.querySelectorAll('.column[data-column="moving"] .card').length;
    expect(cards()).toBe(6);
    const more = container.querySelector('.column[data-column="moving"] .more') as HTMLElement;
    expect(more.textContent).toBe("Show all 9");
    more.click();
    expect(cards()).toBe(9);
    expect(container.querySelector('.column[data-column="moving"] .more')?.textContent).toBe(
      "Show fewer",
    );
  });

  it("renders a footer", () => {
    ws = mountBoardWorkspace(container, [makeItem({ status: "active" })], {
      boards: [{ coord: "30301:owner:ready", title: "ready" }],
    });
    expect(container.querySelector(".board-foot")?.textContent).toContain("1 board");
  });
});

describe("board identity: names, never coordinates", () => {
  const COORD = "30301:a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25:dontguess";

  it("a swimlane head shows the board's TITLE, and the coordinate is never printed", () => {
    const item = makeItem({ id: "x", status: "active", boardCoord: COORD, project: "" });
    ws = mountBoardWorkspace(container, [item], { boards: [{ coord: COORD, title: "dontguess" }] });
    expect(container.querySelector(".swimlane .lane-name")?.textContent).toBe("dontguess");
    expect(container.textContent).not.toContain(COORD);
  });

  it("a verified board with no items still gets a tree node carrying its coordinate", () => {
    ws = mountBoardWorkspace(container, [], { boards: [{ coord: COORD, title: "dontguess" }] });
    const node = container.querySelector(`.left-tree .node[data-board-coord="${COORD}"]`);
    expect(node?.querySelector(".nm")?.textContent).toBe("dontguess");
    expect(node?.querySelector(".ct")?.textContent).toBe("0");
    expect(container.textContent).not.toContain(COORD);
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
    // The priority chip is upper-cased on the card (prototype: `it.p.toUpperCase()`).
    expect(card.querySelector(".card-priority")?.textContent).toBe("P0");
    expect(card.querySelector(".card-id")?.textContent).toBe("blocker");
    expect(card.querySelector(".card-title")?.textContent).toBe("Fix the thing");
    const labelTexts = [...card.querySelectorAll(".card-labels .label-pill")].map((n) => n.textContent);
    expect(labelTexts).toEqual(["a", "b", "c", "+1"]);
    expect(card.querySelector(".card-status-line")?.textContent).toBe("agent working");
    expect(card.querySelector(".card-frees")?.textContent).toBe("frees 1");
  });
});

describe("epics vs labels styling", () => {
  // ready-56b. The shipped board got BOTH halves of this wrong in a way the
  // previous version of this test could not see: it filled the whole left-tree
  // row in a saturated colour, and it filled the card token at full
  // saturation too, off an eight-colour rainbow palette. The design says the
  // tree epic is a 7px DOT and the card token is a 13%-opacity wash of one of
  // five muted project tones. Both are asserted below by VALUE, not by
  // "a background is set", because "a background is set" is exactly what the
  // garish version satisfied.
  it("a tree epic is a coloured DOT beside the name, not a filled row", () => {
    const epic = makeItem({ id: "epic1", title: "Epic One", project: "ready" });
    const child = makeItem({ id: "child1", parentId: "epic1", project: "ready" });
    ws = mountBoardWorkspace(container, [epic, child]);
    const row = container.querySelector(".left-tree .epic-node .node") as HTMLElement;
    expect(row).toBeTruthy();
    // The ROW itself is unpainted…
    expect(row.style.backgroundColor).toBe("");
    // …and the dot carries the project's tone (#7A4FB5 for "ready").
    const dot = row.querySelector(".dot") as HTMLElement;
    expect(dot).toBeTruthy();
    expect(dot.style.background).toBe("rgb(122, 79, 181)");
  });

  it("a card's epic token is a 13% tint of its tone, and label pills stay OUTLINED", () => {
    const epic = makeItem({ id: "epic1", title: "Epic One", project: "ready" });
    const child = makeItem({ id: "child1", parentId: "epic1", labels: ["bug"], project: "ready" });
    ws = mountBoardWorkspace(container, [epic, child]);
    const epicToken = container.querySelector(".card .epic-token") as HTMLElement;
    const labelPill = container.querySelector(".left-tree .label-pill") as HTMLElement;
    expect(epicToken).toBeTruthy();
    expect(labelPill).toBeTruthy();
    expect(epicToken.classList.contains("label-pill")).toBe(false);
    expect(labelPill.classList.contains("epic-token")).toBe(false);
    // Filled — but at 13%, with a 34% border and the tone as the text colour.
    expect(epicToken.style.backgroundColor).toBe("rgba(122, 79, 181, 0.13)");
    expect(epicToken.style.borderColor).toBe("rgba(122, 79, 181, 0.34)");
    expect(epicToken.style.color).toBe("rgb(122, 79, 181)");
    // Outlined: the label pill sets no background at all.
    expect(labelPill.style.backgroundColor).toBe("");
  });

  it("an unresolved HMAC label token is not rendered as pill text", () => {
    // A confidential card the reader holds no LTK for carries `l` tags that are
    // still 64-hex HMACs (pkg/sync/envelope.go labelToken). A 64-character pill
    // is not a label — it is layout damage. Hide it until it resolves.
    const hmac = "793ced294e2d146a02e7040578fbe96bbda73f193fc7f8abefd5bf733206126e";
    const item = makeItem({ id: "conf", labels: [hmac, "bug"], status: "active" });
    ws = mountBoardWorkspace(container, [item]);
    const texts = [...container.querySelectorAll(".label-pill")].map((n) => n.textContent ?? "");
    expect(texts.some((t) => t.includes(hmac))).toBe(false);
    expect(container.textContent).not.toContain(hmac);
    // ...and the resolvable label beside it still renders, so this is a filter
    // and not a "drop every label on a confidential card" bail-out.
    expect(container.querySelector(".card .label-pill")?.textContent).toBe("bug");
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
    // 5px is the drag grip, which only exists while the detail track does.
    expect(grid()).toBe("206px minmax(0, 1fr) 5px 340px");
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
  // ready-56b: the filter bar is the prototype's — a swimlane segmented control
  // plus four chips (P0, P1, Untouched 7+ days, Unblocks others). The old
  // open-ended facet rows are gone; the assignee one in particular rendered a
  // chip per full 64-character pubkey and blew the row across the page.
  it("toggling the P1 chip hides non-matching cards, and toggling it back restores them", () => {
    const withPrio = makeItem({ id: "p1item", priority: "p1", status: "active" });
    const noPrio = makeItem({ id: "noprio", priority: "", status: "active" });
    ws = mountBoardWorkspace(container, [withPrio, noPrio]);
    const cardIds = () => [...container.querySelectorAll(".card")].map((c) => c.getAttribute("data-id"));
    expect(cardIds().sort()).toEqual(["noprio", "p1item"]);

    (container.querySelector('.chip[data-pri="p1"]') as HTMLElement).click();
    expect(cardIds()).toEqual(["p1item"]);

    (container.querySelector('.chip[data-pri="p1"]') as HTMLElement).click();
    expect(cardIds().sort()).toEqual(["noprio", "p1item"]);
  });

  it("the 'Unblocks others' chip keeps only items something else waits on", () => {
    const blocker = makeItem({ id: "blocker", status: "active" });
    const dependent = makeItem({ id: "dependent", status: "active", blockedBy: ["blocker"] });
    const loner = makeItem({ id: "loner", status: "active" });
    ws = mountBoardWorkspace(container, [blocker, dependent, loner]);
    (container.querySelector('.chip[data-flag="lever"]') as HTMLElement).click();
    expect([...container.querySelectorAll(".card")].map((c) => c.getAttribute("data-id"))).toEqual([
      "blocker",
    ]);
  });

  it("the header search filters by word or id", () => {
    const a = makeItem({ id: "aaa-1", title: "Relay must be open infra", status: "active" });
    const b = makeItem({ id: "bbb-2", title: "Something else entirely", status: "active" });
    ws = mountBoardWorkspace(container, [a, b]);
    const find = container.querySelector("input.find") as HTMLInputElement;
    expect(find.placeholder).toBe("Filter by word or id");
    ws.setQuery("open infra");
    expect([...container.querySelectorAll(".card")].map((c) => c.getAttribute("data-id"))).toEqual([
      "aaa-1",
    ]);
    ws.setQuery("bbb-2");
    expect([...container.querySelectorAll(".card")].map((c) => c.getAttribute("data-id"))).toEqual([
      "bbb-2",
    ]);
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
      ...unimplementedWriter,
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

/** columnOf reports which column a card is currently rendered in — the only
 * observation that actually witnesses "the card moved" / "the card went back". */
function columnOf(container: HTMLElement, id: string): string | undefined {
  const card = [...container.querySelectorAll(".card")].find(
    (c) => c.querySelector(".card-id")?.textContent?.trim() === id,
  );
  return (card?.closest(".column") as HTMLElement | null)?.dataset.column;
}

describe("optimistic write, and the revert when it is refused (ready-b2b DC4)", () => {
  it("the card moves IMMEDIATELY, before the publish resolves", async () => {
    let release: () => void = () => {};
    const writer: BoardWriter = {
      ...unimplementedWriter,
      moveStatus: () => new Promise<void>((res) => (release = res)),
    };
    const item = makeItem({ id: "moveme", status: "inbox" });
    ws = mountBoardWorkspace(container, [item], { writer });
    expect(columnOf(container, "moveme")).toBe("ready");

    const pending = ws.handleDrop("moveme", "active");
    // Not awaited yet: the publish has NOT resolved, and the card has already moved.
    expect(columnOf(container, "moveme")).toBe("moving");
    expect(container.querySelector(".transient-error")).toBeNull();

    release();
    await pending;
    expect(columnOf(container, "moveme")).toBe("moving");
  });

  it("a REJECTED write returns the card to its prior column and says what happened", async () => {
    const writer: BoardWriter = {
      ...unimplementedWriter,
      moveStatus: async () => {
        throw new Error("the relay rejected this change: restricted: pubkey is not admitted");
      },
    };
    const item = makeItem({ id: "moveme", status: "inbox" });
    ws = mountBoardWorkspace(container, [item], { writer });
    await ws.handleDrop("moveme", "active");
    expect(columnOf(container, "moveme")).toBe("ready");
    expect(container.querySelector(".transient-error")?.textContent).toContain("restricted");
  });

  it("a rejected retitle restores the ORIGINAL title, not a half-applied one", async () => {
    const writer: BoardWriter = {
      ...unimplementedWriter,
      setTitle: async () => {
        throw new Error("nope");
      },
    };
    const item = makeItem({ id: "t1", title: "Original" });
    ws = mountBoardWorkspace(container, [item], { writer });
    await ws.handleRetitle("t1", "Rewritten");
    ws.selectItem("t1");
    expect(container.querySelector(".detail h2")?.textContent ?? container.textContent).toContain("Original");
  });
});

describe("the detail pane's write affordances (the five that are not a drag or a gate)", () => {
  it("renders claim / close / rename / priority / label for a writable board", () => {
    const writer: BoardWriter = { ...unimplementedWriter, whyReadOnly: () => undefined };
    ws = mountBoardWorkspace(container, [makeItem({ id: "a1" })], { writer });
    ws.selectItem("a1");
    expect(container.querySelector(".act-claim")).not.toBeNull();
    expect(container.querySelector(".act-close")).not.toBeNull();
    expect(container.querySelector(".act-title-save")).not.toBeNull();
    expect(container.querySelector(".act-priority")).not.toBeNull();
    expect(container.querySelector(".act-label-add")).not.toBeNull();
  });

  it("a read-only board renders NO write affordance and states the reason instead", () => {
    const writer: BoardWriter = {
      ...unimplementedWriter,
      whyReadOnly: () => "Read-only: this key holds no write grant on board proj.",
    };
    ws = mountBoardWorkspace(container, [makeItem({ id: "a1" })], { writer });
    ws.selectItem("a1");
    expect(container.querySelector(".act-claim")).toBeNull();
    expect(container.querySelector(".act-priority")).toBeNull();
    expect(container.querySelector(".read-only-note")?.textContent).toContain("no write grant");
  });
});
