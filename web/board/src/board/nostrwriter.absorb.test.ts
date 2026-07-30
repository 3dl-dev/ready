// nostrwriter.absorb.test.ts — the writer's half of the live subscription
// (ready-4359).
//
// THE DEFECT THIS EXISTS TO CATCH, stated as a scenario: the board is open, the
// rd CLI on another machine renames an item, the live subscription re-folds and
// the browser now SHOWS the new name — and then the human claims that item. Every
// write republishes the whole card from the writer's own snapshot, so a writer
// that never learned about the rename publishes the OLD title back. The board
// would be displaying the truth and silently writing a revert of it, which is the
// exact client-and-rd-disagree failure epic ready-9f5 exists to prevent.
//
// No relay and no browser here: the seam under test is `absorb` -> `items()` ->
// `buildWrite`, and the events are REAL signed events so the fold that reads them
// verifies for the same reason it does in production.
import { describe, expect, it } from "vitest";
import { signNostrEvent, xOnlyPubkey } from "../lib/schnorrsign";
import type { NostrEvent } from "../lib/nostrevent";
import type { Nip07Signer } from "../lib/publish";
import { LEVEL_MAINTAINER } from "../lib/rolegrant";
import type { Item } from "../lib/state";
import { NostrBoardWriter } from "./nostrwriter";
import { buildFullCreate, buildWrite, type WriteEnv } from "./writeevents";

const SECRET = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
const OWNER = xOnlyPubkey(SECRET);
const BOARD_D = "proj";
const COORD = `30301:${OWNER}:${BOARD_D}`;
const ITEM = "seed-1";
const RELAY = "wss://relay.example/";

function sign(b: { created_at: number; kind: number; tags: string[][]; content: string }): NostrEvent {
  return signNostrEvent({ created_at: b.created_at, kind: b.kind, tags: b.tags, content: b.content }, SECRET);
}

const signer: Nip07Signer = { async signEvent(e) { return sign(e); } };

/** A relay socket that accepts everything and records what it was offered. */
function recordingRelay(published: NostrEvent[]): (url: string) => WebSocket {
  return () => {
    const sock: Record<string, unknown> = {
      send(raw: string) {
        const [, ev] = JSON.parse(raw) as [string, NostrEvent];
        published.push(ev);
        setTimeout(() => {
          (sock.onmessage as (m: { data: string }) => void)?.({
            data: JSON.stringify(["OK", ev.id, true, ""]),
          });
        }, 0);
      },
      close() {},
    };
    setTimeout(() => (sock.onopen as (() => void) | undefined)?.(), 0);
    return sock as unknown as WebSocket;
  };
}

const seedItem: Item = {
  id: ITEM,
  msg_id: "",
  title: "Seed",
  context: "",
  type: "task",
  priority: "p2",
  status: "inbox",
  for: OWNER,
  created_at: 0n,
  updated_at: 0n,
};

function env(items: Map<string, Item>, createdAt: number): WriteEnv {
  return {
    signer: OWNER,
    boardAuthor: OWNER,
    boardD: BOARD_D,
    boardTitle: BOARD_D,
    items,
    issueEventIds: new Map(),
    createdAt,
  };
}

const SNAPSHOT: NostrEvent[] = buildFullCreate(env(new Map(), 1_780_000_000), seedItem).map(sign);

/** The events "another machine" published: the rd CLI renaming the same item.
 * Built by the SAME writeevents builder rd's own vectors pin, so this is the
 * shape that really arrives on the wire, not a hand-rolled approximation. */
function externalRename(title: string, createdAt: number): NostrEvent[] {
  const items = new Map<string, Item>([[ITEM, { ...seedItem }]]);
  return buildWrite(env(items, createdAt), { op: "update_fields", itemId: ITEM, title }).map(sign);
}

function makeWriter(published: NostrEvent[] = []): NostrBoardWriter {
  return new NostrBoardWriter({
    signerPubkey: OWNER,
    signer,
    board: { ownerPubkey: OWNER, boardD: BOARD_D, title: BOARD_D },
    relays: [RELAY],
    snapshot: SNAPSHOT,
    grantLevels: new Map([[OWNER, LEVEL_MAINTAINER]]),
    publishOptions: { socketFactory: recordingRelay(published), timeoutMs: 2000 },
  });
}

const titleOf = (e: NostrEvent): string | undefined => e.tags.find((t) => t[0] === "title")?.[1];

describe("absorb: a live event changes what the NEXT write is built from", () => {
  it("a claim after an external rename carries the NEW title, not the stale one", async () => {
    const published: NostrEvent[] = [];
    const w = makeWriter(published);
    expect(w.items().get(ITEM)?.title).toBe("Seed");

    w.absorb(externalRename("renamed by rd on another machine", 1_780_000_100));
    expect(w.items().get(ITEM)?.title).toBe("renamed by rd on another machine");

    await w.claim(ITEM, "picked it up in the browser");

    const card = published.find((e) => e.kind === 30302);
    expect(card, "the claim republished a card").toBeDefined();
    // WITHOUT absorb this reads "Seed" — the browser silently reverting a change
    // it was, at that moment, displaying.
    expect(titleOf(card!)).toBe("renamed by rd on another machine");
    // …and the claim itself still happened.
    expect(card!.tags.find((t) => t[0] === "s")?.[1]).toBe("active");
  });

  it("stamps the next write AFTER the absorbed event, so the later intent wins", async () => {
    const published: NostrEvent[] = [];
    const w = makeWriter(published);
    // An external write from the FUTURE relative to this machine's clock: §17.2's
    // per-item monotonic bump has to see it, or the browser's write lands on or
    // before it and the read-side (created_at, id) tiebreak decides by coin flip.
    const future = Math.floor(Date.now() / 1000) + 600;
    w.absorb(externalRename("from the future", future));

    await w.claim(ITEM, "after the future write");
    const card = published.find((e) => e.kind === 30302);
    expect(card!.created_at).toBeGreaterThan(future);
  });

  it("is content-deduplicated, so the writer's OWN echo does not grow the log", async () => {
    const published: NostrEvent[] = [];
    const w = makeWriter(published);
    await w.claim(ITEM, "first");
    expect(published.length).toBeGreaterThan(0);

    // Exactly what the live subscription delivers moments later: the events this
    // writer just published, served back by the relay.
    expect(w.absorb(published)).toBe(0);
    // A genuinely new event still counts.
    expect(w.absorb(externalRename("new", 1_780_000_200))).toBeGreaterThan(0);
    // And re-absorbing it changes nothing.
    expect(w.absorb(externalRename("new", 1_780_000_200))).toBe(0);
  });

  it("cannot widen authority: absorbing a grant does not make a read-only writer writable", () => {
    const w = new NostrBoardWriter({
      signerPubkey: OWNER,
      signer,
      board: { ownerPubkey: OWNER, boardD: BOARD_D, title: BOARD_D },
      relays: [RELAY],
      snapshot: SNAPSHOT,
      // No grant for OWNER: deriveLevels' implicit maintainer seat is bypassed
      // because this map is supplied directly, so the writer is read-only.
      grantLevels: new Map(),
    });
    expect(w.whyReadOnly()).toMatch(/no write grant/i);

    // A role grant addressed to this key, arriving live. Authority is derived at
    // load from an owner-signed authority snapshot; the live ITEM stream must not
    // be able to promote anyone.
    const grant = sign({
      created_at: 1_780_000_300,
      kind: 39301,
      tags: [
        ["d", `${BOARD_D}:${OWNER}`],
        ["a", COORD],
        ["p", OWNER],
        ["role", "maintainer"],
      ],
      content: "",
    });
    w.absorb([grant]);
    expect(w.whyReadOnly()).toMatch(/no write grant/i);
  });
});
