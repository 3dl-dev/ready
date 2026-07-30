// @vitest-environment jsdom
//
// render.liveupdate.test.ts — what setItems has to survive now that it is a LIVE
// path (ready-4359).
//
// setItems used to be called by nobody: the page folded once at load and never
// again, and the method existed for tests. It is now called every time the relay
// pushes something — a change made by the rd CLI on another machine, or by a
// second browser. render() rebuilds the whole subtree with replaceChildren(), so
// a fold arriving mid-keystroke would destroy the one thing on this page that
// exists NOWHERE else: what the human has typed and not yet published.
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { makeItem } from "./testitem";
import { mountBoardWorkspace, type BoardWorkspace } from "./render";

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

const gated = (id: string) =>
  makeItem({ id, status: "waiting", waitingType: "gate", gateMsgId: "m1", gate: "design" });

describe("a live update re-renders the board", () => {
  it("shows an item that did not exist at load, with no second mount", () => {
    ws = mountBoardWorkspace(container, [makeItem({ id: "first", title: "First" })]);
    expect([...container.querySelectorAll(".card-id")].map((n) => n.textContent?.trim())).toEqual(["first"]);

    ws.setItems([makeItem({ id: "first", title: "First" }), makeItem({ id: "second", title: "Second" })]);
    expect([...container.querySelectorAll(".card-id")].map((n) => n.textContent?.trim()).sort()).toEqual([
      "first",
      "second",
    ]);
  });

  it("keeps the selected item's detail pane open across the update", () => {
    ws = mountBoardWorkspace(container, [makeItem({ id: "a", title: "A" })]);
    ws.selectItem("a");
    expect(container.querySelector(".detail-pane")).not.toBeNull();

    ws.setItems([makeItem({ id: "a", title: "A renamed elsewhere" })]);
    expect(container.querySelector(".detail-pane")).not.toBeNull();
    expect(container.querySelector(".detail-pane")?.textContent).toContain("A renamed elsewhere");
  });
});

describe("a live update does not destroy unpublished keystrokes", () => {
  it("preserves a half-typed gate reason, and the caret, in the rail", () => {
    ws = mountBoardWorkspace(container, [gated("g1"), makeItem({ id: "other" })]);
    const input = container.querySelector<HTMLInputElement>('.gate-item[data-id="g1"] .gate-reason-input');
    expect(input).not.toBeNull();
    input!.focus();
    input!.value = "approved because";
    input!.setSelectionRange(8, 8);

    // Something unrelated lands from the relay.
    ws.setItems([gated("g1"), makeItem({ id: "other" }), makeItem({ id: "brand-new" })]);

    const after = container.querySelector<HTMLInputElement>('.gate-item[data-id="g1"] .gate-reason-input');
    expect(after).not.toBeNull();
    // A NEW node — the DOM really was rebuilt, so this is not passing by the
    // element having survived untouched.
    expect(after).not.toBe(input);
    expect(after!.value).toBe("approved because");
    expect(document.activeElement).toBe(after);
    expect(after!.selectionStart).toBe(8);
  });

  it("preserves a half-typed rename in the detail pane", () => {
    ws = mountBoardWorkspace(container, [makeItem({ id: "a", title: "A" })]);
    ws.selectItem("a");
    const input = container.querySelector<HTMLInputElement>(".act-title-input");
    input!.focus();
    input!.value = "a better name";

    ws.setItems([makeItem({ id: "a", title: "A" }), makeItem({ id: "b" })]);

    const after = container.querySelector<HTMLInputElement>(".act-title-input");
    expect(after!.value).toBe("a better name");
    expect(document.activeElement).toBe(after);
  });

  it("restores nothing when the item being typed into is gone from the new projection", () => {
    ws = mountBoardWorkspace(container, [gated("g1")]);
    const input = container.querySelector<HTMLInputElement>('.gate-item[data-id="g1"] .gate-reason-input');
    input!.focus();
    input!.value = "half a thought";

    // Somebody else resolved that gate. There is no field to restore into, and
    // the board must not resurrect one.
    ws.setItems([makeItem({ id: "g1", status: "active" })]);
    expect(container.querySelector('.gate-item[data-id="g1"]')).toBeNull();
    expect(container.querySelector(".gate-reason-input")).toBeNull();
  });

  it("leaves focus alone when nothing on the board had it", () => {
    ws = mountBoardWorkspace(container, [makeItem({ id: "a" })]);
    const outside = document.createElement("input");
    document.body.append(outside);
    outside.focus();

    ws.setItems([makeItem({ id: "a" }), makeItem({ id: "b" })]);
    expect(document.activeElement).toBe(outside);
    outside.remove();
  });
});
