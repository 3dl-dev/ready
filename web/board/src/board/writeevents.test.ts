// writeevents.test.ts — the browser's CONFIDENTIAL write path (ready-191).
//
// `rd init` DEFAULTS to a confidential board, so until this existed the browser
// write path worked on `--public` boards ONLY and every real project board was
// read-only in the page. What is asserted here is the seal half of the envelope
// whose open half already shipped: the same wire contract
// (docs/design/confidential-boards-envelope.md §3), run backwards.
//
// WHY NOT A VECTOR FILE. testdata/write.vectors.json cannot cover this and says
// so in its own `known_gaps`: every vector forces Public:true because a sealed
// Content is nondeterministic (a fresh nonce per seal) and therefore not
// byte-comparable the way a plaintext tag array is. So conformance here is
// proven by ROUND TRIP in both directions instead of by byte equality:
//
//   rd seals  → the browser opens   — this file, over the Go-GENERATED
//                                     confidential.fixtures.ts (real bytes out
//                                     of pkg/sync's writer, pkg/nip44 and the
//                                     real content envelope), then republishes
//                                     that item and opens its own card again.
//   browser seals → rd opens        — web/board/confidential_write_test.go,
//                                     which runs the REAL buildWrite below
//                                     through vite-node and folds the events it
//                                     produces with pkg/sync.ProjectItems.
//
// Neither direction is a self-consistency check: the fixture's ciphertext, its
// HMAC label tokens and its event ids all came out of Go, and the Go test folds
// bytes this module produced.
//
// THE NO-PLAINTEXT CLAIM IS ASSERTED ON THE PUBLISHED EVENT, not on the
// projection. A projection that renders the right title proves the reader works;
// it says nothing about what went on the wire. So the tests below serialize the
// events exactly as lib/publish.ts would hand them to a relay and scan those
// bytes for the free text.

import { describe, expect, it } from "vitest";
import * as fx from "../lib/confidential.fixtures";
import {
  decodeCardPayload,
  labelToken,
  openContent,
  type BoardDecryptor,
  type SealEnvelope,
} from "../lib/envelope";
import { KindCard, KindIssue, KindStatusOpen, KindStatusResolved, projectItems } from "../lib/fold";
import type { NostrEvent } from "../lib/nostrevent";
import { tagValue } from "../lib/nostrevent";
import { signNostrEvent } from "../lib/schnorrsign";
import { hexToBytes } from "../lib/sha256";
import type { Item } from "../lib/state";
import { StatusInbox } from "../lib/state";
import { buildWrite, cardCoord, WriteRefusedError, type BuiltEvent, type WriteEnv } from "./writeevents";

const CEK1 = hexToBytes(fx.CEK_EPOCH1);
const LTK = hexToBytes(fx.LTK);

/** conf-001's plaintext, as the Go generator sealed it. Read out of the
 * fixture's own expectedPlaintext rather than re-typed, so the assertions
 * cannot drift from what the writer actually put inside the ciphertext. */
const CONF1 = fx.expectedPlaintext.find((e) => e.id === "conf-001")!;

/** A decryptor holding exactly the epoch-1 CEK for this board — the shape
 * lib/keyring.ts's BoardKeyring presents to the fold. */
const epoch1Only: BoardDecryptor = {
  cek: (coord, epoch) => (coord === fx.BOARD_COORD && epoch === 1 ? CEK1 : null),
};

const encEpoch1: SealEnvelope = { cek: CEK1, epoch: 1, ltk: LTK };

function project(events: NostrEvent[], dec: BoardDecryptor | null): Map<string, Item> {
  return projectItems(events, {
    trusted: null,
    maintainers: null,
    pinnedBoard: fx.BOARD_COORD,
    decryptor: dec,
    encryptedBoards: null,
  });
}

function envFor(items: Map<string, Item>, over: Partial<WriteEnv> = {}): WriteEnv {
  return {
    signer: fx.OWNER_PUB,
    boardAuthor: fx.OWNER_PUB,
    boardD: fx.BOARD_D,
    boardTitle: "Confidential Board",
    items,
    issueEventIds: new Map(),
    createdAt: 1750001000,
    confidential: true,
    enc: encEpoch1,
    ...over,
  };
}

/** onTheWire is what lib/publish.ts sends: the signed event, serialized. Every
 * no-plaintext assertion scans THIS, never the projection. */
function onTheWire(events: BuiltEvent[]): string {
  return JSON.stringify(events.map((e) => signNostrEvent(e, fx.OWNER_SEC)));
}

function openCard(e: BuiltEvent) {
  const raw = openContent(CEK1, e.content);
  expect(raw).not.toBeNull();
  return decodeCardPayload(raw!);
}

function openReason(e: BuiltEvent): unknown {
  const raw = openContent(CEK1, e.content);
  expect(raw).not.toBeNull();
  return JSON.parse(new TextDecoder().decode(raw!));
}

describe("rd sealed it, the browser republishes it, and it is still sealed", () => {
  // The starting point is a REAL Go-authored confidential card (conf-001 out of
  // confidential.fixtures.ts: sealed by pkg/sync's writer under CEK_EPOCH1, with
  // its labels HMAC-tokenized under the real LTK). Everything below is the
  // browser acting on bytes it did not produce.
  const before = project([fx.boardEvent, fx.cardEpoch1A], epoch1Only);

  it("reads rd's sealed card back to exact plaintext before touching it", () => {
    const item = before.get("conf-001")!;
    expect(item.title).toBe(CONF1.title);
    expect(item.context).toBe(CONF1.context);
    expect(item.waiting_on).toBe(CONF1.waiting_on);
    expect(item.labels).toEqual([...CONF1.labels]);
    // Not the [encrypted] placeholder: a redacted item is refused by the write
    // path, so this is also the precondition for everything below.
    expect(item.redacted).toBeFalsy();
  });

  const built = buildWrite(envFor(before), {
    op: "update_status",
    itemId: "conf-001",
    statusTo: "done",
    note: "shipped from the browser",
  });

  it("emits a sealed card and a sealed status event — and NO kind:1621 issue event", () => {
    expect(built.map((e) => e.kind)).toEqual([KindCard, KindStatusResolved]);
    // The suppression is the point, not an accident of this op: the issue root's
    // `subject` tag is the title in the clear and its content is the context in
    // the clear. rd's Publisher.ensureIssueEvent returns early for exactly this
    // reason; a browser that kept minting them would leak both fields on every
    // first status change.
    expect(built.some((e) => e.kind === KindIssue)).toBe(false);
  });

  it("the card carries every routing tag in the clear and NEITHER free-text tag", () => {
    const card = built[0];
    expect(card.tags).toEqual([
      ["d", "conf-001"],
      // The item's TRUE creation time, carried forward from the card rd wrote.
      ["created", "1750000200"],
      ["a", fx.BOARD_COORD],
      ["s", "done"],
      ["rank", "p1"],
      ["priority", "p1"],
      ["itype", "task"],
      // Labels as owner-keyed HMAC tokens — see the byte-equality check below.
      ["l", labelToken(LTK, "crypto")],
      ["l", labelToken(LTK, "board")],
      ["enc", "1"],
      ["cek_epoch", "1"],
    ]);
    expect(tagValue(card, "title")).toBe("");
    expect(tagValue(card, "waiting_on")).toBe("");
  });

  it("its label tokens are byte-identical to the ones Go emitted for the same labels", () => {
    // The fixture's l tags were produced by pkg/sync's labelToken (HMAC-SHA256
    // under the board LTK). If the TS HMAC, the key handling or the encoding
    // drifted, a relay's #l equality filter would stop matching rd's own cards
    // and nothing on the read path would notice.
    const goTokens = fx.cardEpoch1A.tags.filter((t) => t[0] === "l").map((t) => t[1]);
    const browserTokens = built[0].tags.filter((t) => t[0] === "l").map((t) => t[1]);
    expect(browserTokens).toEqual(goTokens);
  });

  it("the card's sealed body carries the whole free-text blob, unchanged", () => {
    expect(openCard(built[0])).toEqual({
      title: CONF1.title,
      context: CONF1.context,
      waitingOn: CONF1.waiting_on,
      labels: [...CONF1.labels],
    });
  });

  it("the status event seals its reason and orders its markers exactly as rd does", () => {
    const status = built[1];
    expect(status.tags).toEqual([
      ["a", cardCoord(fx.OWNER_PUB, "conf-001")],
      ["d", "conf-001"],
      ["status", "done"],
      ["e", built[0].id],
      // BuildStatusEventWithIssueRoot appends the markers BEFORE the issue-root
      // "e" tag and BEFORE the board "a" tag. The issue anchor is suppressed on
      // a confidential board, so the board coordinate follows the markers.
      ["enc", "1"],
      ["cek_epoch", "1"],
      ["a", fx.BOARD_COORD],
    ]);
    expect(openReason(status)).toEqual({ reason: "shipped from the browser" });
  });

  it("NOTHING plaintext reaches the wire — asserted on the serialized events", () => {
    const wire = onTheWire(built);
    for (const secret of [CONF1.title, CONF1.context, CONF1.waiting_on, "crypto", "shipped from the browser"]) {
      expect(wire).not.toContain(secret);
    }
    // "board" is a substring of the board coordinate itself ("…:confboard"), so
    // it gets the exact-value check the blanket scan cannot give it: no TAG
    // VALUE anywhere equals the label.
    const values = built.flatMap((e) => e.tags.flat());
    expect(values).not.toContain("board");
  });

  it("an independent projection of the published log reads the intended state", () => {
    const signed = built.map((e) => signNostrEvent(e, fx.OWNER_SEC));
    // The builder's precomputed id must be the id the signature produced — the
    // status event's "e" anchor points at it.
    signed.forEach((s, i) => expect(s.id).toBe(built[i].id));
    const after = project([fx.boardEvent, fx.cardEpoch1A, ...signed], epoch1Only);
    const item = after.get("conf-001")!;
    expect(item.status).toBe("done");
    expect(item.title).toBe(CONF1.title);
    expect(item.context).toBe(CONF1.context);
    expect(item.labels).toEqual([...CONF1.labels]);
    // waiting_on is DERIVED away on a terminal item (§9.8: a done item is not
    // waiting on anything) even though the sealed body still carries it — see
    // the sealed-blob assertion above, which is where the "nothing was lost in
    // the seal" claim actually lives. Asserting it here instead would be
    // asserting the fold's terminal rule, not this write path's fidelity.
    expect(item.waiting_on).toBeUndefined();
    expect(item.redacted).toBeFalsy();
    expect(item.history?.at(-1)?.note).toBe("shipped from the browser");
  });

  it("a reader WITHOUT the key sees a placeholder, not the text — same as rd's cards", () => {
    const signed = built.map((e) => signNostrEvent(e, fx.OWNER_SEC));
    const stranger = project([fx.boardEvent, ...signed], null);
    const item = stranger.get("conf-001")!;
    expect(item.redacted).toBe(true);
    expect(item.title).toBe("[encrypted]");
    expect(item.context).toBe("[encrypted]");
  });
});

describe("a fresh item created on a confidential board", () => {
  // Sentinels rather than the fixture's text: this test owns every free-text
  // string in the write, so "does this byte appear on the wire" has exactly one
  // possible source and no incidental substring can mask a leak.
  const TITLE = "SENTINEL-TITLE-4f2a";
  const CONTEXT = "SENTINEL-CONTEXT-9c7d";
  const built = buildWrite(envFor(new Map()), {
    op: "create",
    id: "conf-new",
    title: TITLE,
    context: CONTEXT,
    type: "task",
    priority: "p1",
  });

  it("publishes board + card + status, and still no issue event", () => {
    expect(built.map((e) => e.kind)).toEqual([30301, KindCard, KindStatusOpen]);
    expect(built.some((e) => e.kind === KindIssue)).toBe(false);
  });

  it("seals both free-text fields and leaks neither", () => {
    expect(openCard(built[1])).toEqual({ title: TITLE, context: CONTEXT, waitingOn: "", labels: [] });
    const wire = onTheWire(built);
    expect(wire).not.toContain(TITLE);
    expect(wire).not.toContain(CONTEXT);
  });

  it("round-trips through the fold to the item that was asked for", () => {
    const signed = built.map((e) => signNostrEvent(e, fx.OWNER_SEC));
    const item = project(signed, epoch1Only).get("conf-new")!;
    expect(item.title).toBe(TITLE);
    expect(item.context).toBe(CONTEXT);
    expect(item.status).toBe(StatusInbox);
    expect(item.priority).toBe("p1");
    expect(item.type).toBe("task");
  });
});

describe("labels with no LTK in hand", () => {
  const before = project([fx.boardEvent, fx.cardEpoch1A], epoch1Only);
  const built = buildWrite(envFor(before, { enc: { cek: CEK1, epoch: 1, ltk: null } }), {
    op: "claim",
    itemId: "conf-001",
    reason: "",
  });

  it("emits NO l tag at all rather than a plaintext one", () => {
    // BuildCardEvent's `case spec.Enc != nil:` branch: a confidential board with
    // no LTK drops the clear l tag entirely. rd filters labels client-side off
    // the decrypted blob, so nothing is lost — and a plaintext l tag on a
    // confidential board would be a leak with no upside.
    expect(built[0].tags.filter((t) => t[0] === "l")).toEqual([]);
    expect(onTheWire(built)).not.toContain("crypto");
  });

  it("but the labels still ride inside the sealed body, so they survive the fold", () => {
    expect(openCard(built[0])!.labels).toEqual([...CONF1.labels]);
    const signed = built.map((e) => signNostrEvent(e, fx.OWNER_SEC));
    const after = project([fx.boardEvent, fx.cardEpoch1A, ...signed], epoch1Only);
    expect(after.get("conf-001")!.labels).toEqual([...CONF1.labels]);
  });
});

describe("the refusals that must survive the seal path", () => {
  it("a confidential board with NO key held is still refused outright", () => {
    // This is ready-b2b's refusal, narrowed rather than removed. With no CEK the
    // only card this page could build is a plaintext one, which leaks the title
    // and context and silently diverges from every rd-authored card on the board.
    const before = project([fx.boardEvent, fx.cardEpoch1A], epoch1Only);
    const env = envFor(before, { enc: null });
    expect(() => buildWrite(env, { op: "claim", itemId: "conf-001" })).toThrow(WriteRefusedError);
    try {
      buildWrite(env, { op: "claim", itemId: "conf-001" });
    } catch (e) {
      expect((e as WriteRefusedError).code).toBe("confidential");
    }
  });

  it("an item this reader could NOT decrypt is refused even though a key IS held", () => {
    // conf-003 is sealed under epoch 2; this reader holds epoch 1 only, so the
    // fold marks it redacted. Holding SOME key is not permission to republish an
    // item whose real content was never in hand — rebuilding the card from the
    // "[encrypted]" placeholders would re-seal them as the item's content and
    // destroy it irreversibly (cmd/rd's refuseRedactedRepublish, ready-76b).
    const before = project([fx.boardEvent, fx.cardEpoch2], epoch1Only);
    expect(before.get("conf-003")!.redacted).toBe(true);
    let thrown: unknown;
    try {
      buildWrite(envFor(before), { op: "claim", itemId: "conf-003" });
    } catch (e) {
      thrown = e;
    }
    expect(thrown).toBeInstanceOf(WriteRefusedError);
    expect((thrown as WriteRefusedError).code).toBe("redacted");
  });
});

describe("a PUBLIC board is completely unaffected by any of this", () => {
  it("writes plaintext tags and content exactly as before, with its issue event", () => {
    const item: Item = {
      id: "pub-1",
      msg_id: "",
      title: "a public title",
      context: "a public context",
      type: "task",
      priority: "p2",
      status: StatusInbox,
      for: fx.OWNER_PUB,
      labels: ["ops"],
      created_at: 0n,
      updated_at: 0n,
    };
    const built = buildWrite(
      envFor(new Map([["pub-1", item]]), { confidential: false, enc: null }),
      { op: "claim", itemId: "pub-1", reason: "mine" },
    );
    expect(built.map((e) => e.kind)).toEqual([KindCard, KindIssue, KindStatusOpen]);
    expect(tagValue(built[0], "title")).toBe("a public title");
    expect(built[0].content).toBe("a public context");
    expect(built[0].tags.filter((t) => t[0] === "l")).toEqual([["l", "ops"]]);
    expect(built[0].tags.some((t) => t[0] === "enc")).toBe(false);
    expect(built[2].content).toBe("mine");
  });
});
