// The BROWSER half of ready-ed4's data-preservation guarantee.
//
// buildCardEvent writes item.context, and the fold has already reduced
// item.context to the base description — so a browser card edit (retitle,
// reprioritise, a label toggle, a drag between columns) COMPACTS a legacy item's
// card as a side effect. That is the desired outcome, but only if the notes the
// compaction drops out of the card are minted as their own events in the SAME
// batch. Without that, a single retitle in the browser would delete an item's
// entire progress trail from every relay's copy — the exact continuity loss
// ready-ed4 exists to prevent, arriving from the browser instead of the CLI.
//
// These tests pin that: what the browser emits, in what order, sealed on a
// confidential board, and NOT emitted on the common path.
import { describe, expect, it } from "vitest";
import * as fx from "../lib/confidential.fixtures";
import { openContent, type SealEnvelope } from "../lib/envelope";
import { hexToBytes } from "../lib/sha256";
import { KindCard, KindNote } from "../lib/fold";
import { tagValue } from "../lib/nostrevent";
import { signNostrEvent } from "../lib/schnorrsign";
import type { Item } from "../lib/state";
import { StatusActive } from "../lib/state";
import { buildWrite, pendingNoteEvents, type BuiltEvent, type WriteEnv } from "./writeevents";

const CEK1 = hexToBytes(fx.CEK_EPOCH1);
const encEpoch1: SealEnvelope = { cek: CEK1, epoch: 1, ltk: hexToBytes(fx.LTK) };

function legacyItem(over: Partial<Item> = {}): Item {
  return {
    id: "ready-brick",
    msg_id: "cardid",
    title: "Bricked item",
    context: "the base description",
    description: "the base description",
    type: "task",
    for: "",
    priority: "p1",
    status: StatusActive,
    created_at: 1_750_000_000_000_000_000n,
    updated_at: 1_750_000_000_000_000_000n,
    // Two notes recovered from the legacy card's inline content (no msg_id) and
    // one that already has its own event — only the first two need minting.
    notes: [
      { at: "2026-07-30T10:00Z", text: "legacy note one" },
      { at: "2026-07-30T11:00Z", text: "legacy note two" },
      { at: "2026-07-30T12:00Z", text: "already an event", msg_id: "existing-event-id" },
    ],
    ...over,
  };
}

function envFor(items: Map<string, Item>, over: Partial<WriteEnv> = {}): WriteEnv {
  return {
    signer: fx.OWNER_PUB,
    boardAuthor: fx.OWNER_PUB,
    boardD: fx.BOARD_D,
    boardTitle: "Board",
    items,
    issueEventIds: new Map(),
    createdAt: 1750001000,
    confidential: false,
    enc: null,
    ...over,
  };
}

describe("pendingNoteEvents", () => {
  it("mints an event for exactly the notes that have none", () => {
    const item = legacyItem();
    const env = envFor(new Map([[item.id, item]]));
    const events = pendingNoteEvents(env, item);
    expect(events.map((e) => e.content)).toEqual(["legacy note one", "legacy note two"]);
    for (const e of events) {
      expect(e.kind).toBe(KindNote);
      expect(tagValue(e, "d")).toBe("ready-brick");
    }
    expect(events[0] && tagValue(events[0], "ts")).toBe("2026-07-30T10:00Z");
  });

  it("emits NOTHING for an item whose trail is already event-backed", () => {
    // The common path: every card written after ready-ed4. If this ever became
    // non-empty, every browser write would re-mint the item's whole history,
    // forever.
    const item = legacyItem({ notes: [{ at: "2026-07-30T10:00Z", text: "n", msg_id: "id" }] });
    expect(pendingNoteEvents(envFor(new Map([[item.id, item]])), item)).toEqual([]);
  });

  it("emits NOTHING for an item with no notes at all", () => {
    const item = legacyItem({ notes: undefined });
    expect(pendingNoteEvents(envFor(new Map([[item.id, item]])), item)).toEqual([]);
  });

  it("gives each note its own created_at, so identical notes cannot collide into one event", () => {
    // Two textually identical notes bearing the same `ts` would hash to the SAME
    // event id at the same created_at, and the second would vanish as a
    // duplicate. "Retried the same thing" is a genuinely common note.
    const item = legacyItem({
      notes: [
        { at: "2026-07-30T10:00Z", text: "retrying" },
        { at: "2026-07-30T10:00Z", text: "retrying" },
      ],
    });
    const events = pendingNoteEvents(envFor(new Map([[item.id, item]])), item);
    expect(events).toHaveLength(2);
    expect(events[0].created_at).not.toBe(events[1].created_at);
    expect(events[0].id).not.toBe(events[1].id);
  });

  it("carries the BOARD coordinate, or a board-scoped reader never fetches the note", () => {
    const item = legacyItem();
    const env = envFor(new Map([[item.id, item]]));
    const [first] = pendingNoteEvents(env, item);
    const coords = first.tags.filter((t) => t[0] === "a").map((t) => t[1]);
    expect(coords).toContain(`30301:${fx.OWNER_PUB}:${fx.BOARD_D}`);
    // ...and the CARD coordinate must still be FIRST — rd's projection reads
    // only the first "a" match.
    expect(coords[0]).toBe(`30302:${fx.OWNER_PUB}:ready-brick`);
  });

  it("seals the note text on a confidential board", () => {
    const item = legacyItem();
    const env = envFor(new Map([[item.id, item]]), { confidential: true, enc: encEpoch1 });
    const events = pendingNoteEvents(env, item);
    const wire = JSON.stringify(events.map((e) => signNostrEvent(e, fx.OWNER_SEC)));
    expect(wire).not.toContain("legacy note one");
    expect(wire).not.toContain("legacy note two");
    for (const e of events) {
      expect(tagValue(e, "enc")).toBe("1");
      expect(tagValue(e, "cek_epoch")).toBe("1");
    }
    // ...and it opens back to the exact plaintext under the right key.
    const raw = openContent(CEK1, events[0].content);
    expect(raw).not.toBeNull();
    expect(JSON.parse(new TextDecoder().decode(raw as Uint8Array))).toEqual({ text: "legacy note one" });
  });
});

describe("a browser card edit cannot silently drop a legacy trail", () => {
  const kinds = (events: BuiltEvent[]): number[] => events.map((e) => e.kind);

  it("publishes the pending notes BEFORE the compacted card", () => {
    // Order is the guarantee: at no instant does a relay hold the compacted card
    // without also holding the events carrying what was compacted out of it.
    const item = legacyItem();
    const env = envFor(new Map([[item.id, item]]));
    const events = buildWrite(env, { op: "update_fields", itemId: item.id, title: "Renamed" });
    expect(kinds(events)).toEqual([KindNote, KindNote, KindCard]);
    const card = events[events.length - 1];
    // The card the browser writes carries ONLY the base description — the trail
    // is gone from it, which is the point, and is exactly why the two note
    // events above have to exist.
    expect(card.content).toBe("the base description");
    expect(card.content).not.toContain("legacy note one");
  });

  it("a healthy item's card edit is EXACTLY one card event, as before", () => {
    const item = legacyItem({ notes: undefined });
    const env = envFor(new Map([[item.id, item]]));
    const events = buildWrite(env, { op: "update_fields", itemId: item.id, title: "Renamed" });
    expect(kinds(events)).toEqual([KindCard]);
  });
});
