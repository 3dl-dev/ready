// @vitest-environment jsdom
//
// main.live.confidential.test.ts — ready-e51 (veracity audit of the M2 wave).
//
// WHAT THIS FILE EXISTS TO CATCH, measured, not hypothesised. ready-191 rework 4
// established that `encryptedBoards` — the post-cutover cleartext quarantine —
// must be threaded into everything that projects a confidential board, and it
// pinned that argument on the WRITER (main.test.ts's "the WRITER quarantines
// what the page quarantines"). ready-4359 then added a SECOND projection seam
// one line below it: LiveBoard.src, the ItemSource the live subscription
// re-folds through every time the relay pushes an event.
//
// That second seam was witnessed by nothing. Measured on this tree at
// 832/832 green:
//
//   live.push({ … src: foldItemSource({ …, decryptor: keyring,
//                                       encryptedBoards: null }, b.coord) })
//     -> 832/832 vitest PASS, go test ./... clean.
//
// Consequence with that mutation applied, on the shipped confidential fixture:
// cardSmuggledCleartext (conf-005 — OWNER-SIGNED, so signature verification
// passes; POST-cutover; free text in the clear) is quarantined out of the LOAD
// projection and then walks straight into the OPEN page the moment any relay
// pushes it. No reload, no second visit, nothing on screen to distinguish it
// from a card the owner really sealed. Same laundering vector rework 4 closed,
// one seam over, and reachable by any relay against an already-open board.
//
// Every case below drives the REAL loadBoardItems (real grant -> NIP-44 unwrap
// -> keyring -> confidentiality decision -> fold) and the REAL startLiveUpdates
// over the LiveBoard array loadBoardItems produced. The only substitute is the
// socket (makeNip01Relay, which applies the REQ filter it is sent and pushes
// only to subscriptions that are still open).
import { describe, expect, it } from "vitest";
import { loadBoardItems, startLiveUpdates, type BoardDeps, type Identity } from "./main";
import { authTransition } from "./lib/auth";
import { fakeNip44Signer } from "./lib/fakesigner";
import { nip07KeyUnwrapper } from "./lib/keyunwrap";
import { makeNip01Relay } from "./lib/nip01relay.fixtures";
import type { DiscoveredBoard } from "./lib/boarddiscovery";
import { fetchEventsFromRelays, subscribeToRelays } from "./lib/relay";
import type { NostrEvent } from "./lib/nostrevent";
import type { Item } from "./board/types";
import {
  BOARD_COORD as CONF_COORD,
  BOARD_D as CONF_BOARD_D,
  OWNER_PUB as CONF_OWNER,
  OWNER_SEC as CONF_OWNER_SEC,
  CUTOVER as CONF_CUTOVER,
  boardEvent as confBoardEvent,
  cardEpoch1A,
  cardEpoch2,
  cardSmuggledCleartext,
  expectedPlaintext as confExpected,
  grants as confGrants,
  grantEpoch1Late,
  LATECOMER_PUB,
  LATECOMER_SEC,
  STRANGER_SEC,
} from "./lib/confidential.fixtures";
import { signNostrEvent } from "./lib/schnorrsign";

const RELAY = "wss://relay.test";

const board: DiscoveredBoard = {
  coord: CONF_COORD,
  ownerPubkey: CONF_OWNER,
  boardD: CONF_BOARD_D,
  title: "Confidential Board",
};

/** The owner's own session: an extension identity that can unwrap the board's
 * CEK, which is the only session for which the live path folds anything but
 * placeholders. */
const identity: Identity = {
  pubkey: CONF_OWNER,
  auth: authTransition({ type: "login", method: "extension" }),
};

/** THE LOAD SNAPSHOT deliberately withholds two of the fixture's cards so they
 * can arrive LIVE instead:
 *   cardEpoch2 (conf-003)            — a genuinely sealed card, the anti-tautology
 *   cardSmuggledCleartext (conf-005) — post-cutover cleartext, the quarantine case
 * Everything else the board needs (the 30301 definition and the owner-signed
 * grants that establish the cutover) is present, so confidentiality is
 * ESTABLISHED at load and `encryptedBoards` is a real gate rather than a
 * fail-closed stub. */
const LOAD_SNAPSHOT: NostrEvent[] = [confBoardEvent, ...confGrants, cardEpoch1A];

const CONF3 = confExpected.find((e) => e.id === "conf-003")!;
const CONF1 = confExpected.find((e) => e.id === "conf-001")!;

/** settle lets the coalesce timer (5ms here) and the fixture's microtask
 * deliveries run. */
const settle = () => new Promise((r) => setTimeout(r, 60));

async function openLiveBoard(): Promise<{
  emitted: Item[][];
  push: (e: NostrEvent) => number;
  close: () => void;
  loaded: Item[];
}> {
  // ONE relay, honouring filters, for BOTH the load fetch and the live
  // subscription — the same socket a browser uses for both. An earlier revision
  // of this file claimed "the only substitute is the socket" while handing
  // loadBoardItems a `fetchEvents: async () => LOAD_SNAPSHOT` stub, which is a
  // second substitute and a more consequential one: it hands the load a set no
  // production filter would have returned. Now the load really asks
  // {kinds: BOARD_KINDS, "#a": [coord]} of a relay that applies it.
  const { ctor, handle } = makeNip01Relay({ events: [...LOAD_SNAPSHOT] });
  const deps: BoardDeps = {
    loadRelays: async () => [RELAY],
    fetchEvents: (relays, filter, opts) =>
      fetchEventsFromRelays(relays, filter, { ...opts, webSocketCtor: ctor, retries: 0, timeoutMs: 2000 }),
    // The real grant -> NIP-44 unwrap -> CEK path, with the spec-validated
    // NIP-44 v2 reference standing in for the extension (nip44ref.test.ts).
    keyUnwrapper: () => nip07KeyUnwrapper(fakeNip44Signer(CONF_OWNER_SEC)),
  };
  const { items, live, confidential } = await loadBoardItems(
    [board],
    [RELAY],
    confGrants,
    identity,
    deps,
    () => {},
  );
  // PRECONDITIONS, asserted rather than assumed: the board really is
  // confidential in this session, the session really decrypted (so a live
  // "[encrypted]" below would be a regression and not the fixture), and there
  // really is one live board to subscribe.
  expect(confidential).toBe(true);
  expect(live).toHaveLength(1);
  expect(items.find((i) => i.id === "conf-001")?.title).toBe(CONF1.title);

  const emitted: Item[][] = [];
  const sub = startLiveUpdates({
    boards: live,
    relays: [RELAY],
    subscribe: (relays, filter, opts) => subscribeToRelays(relays, filter, { ...opts, webSocketCtor: ctor }),
    onItems: (next) => emitted.push(next),
    coalesceMs: 5,
  });
  return {
    emitted,
    push: (e) => handle.push(e),
    close: () => sub.close(),
    loaded: items,
  };
}

describe("ready-e51: the LIVE re-fold carries the same quarantine and the same keys as the load", () => {
  it("a post-cutover CLEARTEXT card pushed onto an open confidential board never reaches the view", async () => {
    const b = await openLiveBoard();
    try {
      // The fixture is what it claims to be — the quarantine has something to
      // bite on, and this is not a test about a card that could never leak.
      expect(cardSmuggledCleartext.pubkey).toBe(CONF_OWNER); // signature verifies
      expect(cardSmuggledCleartext.created_at).toBeGreaterThan(CONF_CUTOVER); // post-cutover
      expect(cardSmuggledCleartext.tags).toContainEqual(["title", "SMUGGLED CLEARTEXT TITLE"]);
      expect(b.loaded.some((i) => i.id === "conf-005")).toBe(false); // absent at load

      // NOT TRIVIALLY ZERO: the relay really delivered both to a live
      // subscription. Oldest first — the live REQ's `since` cursor advances to
      // the newest event delivered, so pushing the newer card first would make
      // the fixture's own filter drop the older one and the anti-tautology
      // below would be measuring the cursor, not the fold.
      expect(b.push(cardEpoch2)).toBe(1);
      expect(b.push(cardSmuggledCleartext)).toBe(1);
      await settle();

      expect(b.emitted.length).toBeGreaterThan(0);
      const latest = b.emitted.at(-1)!;

      // THE ANTI-TAUTOLOGY: the live path IS alive and IS decrypting. Without
      // this the quarantine assertion below would pass on a page that folds
      // nothing at all.
      const conf3 = latest.find((i) => i.id === "conf-003");
      expect(conf3, "the sealed card pushed live must reach the view").toBeDefined();
      expect(conf3!.title).toBe(CONF3.title);
      expect(conf3!.redacted).toBeFalsy();

      // THE PROPERTY: the smuggled card is quarantined on the live path exactly
      // as it is on the load path.
      expect(latest.some((i) => i.id === "conf-005")).toBe(false);
      expect(latest.map((i) => i.title)).not.toContain("SMUGGLED CLEARTEXT TITLE");
      expect(latest.map((i) => i.context)).not.toContain("SMUGGLED CLEARTEXT BODY");
    } finally {
      b.close();
    }
  });
});


/**
 * ready-48f — A GRANT THAT ARRIVES LIVE OPENS THE BOARD, WITH NO RELOAD.
 *
 * THE DEFECT THIS PINS, measured end to end before it was fixed
 * (scripts/live-stranger-walk.mjs, a real nos2x extension on a fresh Chromium
 * profile against wss://relay.3dl.network): the owner ran
 * `rd grant --claim <nonce> <pubkey>`, the kind-39301 really arrived on the open
 * page — 39301 has always been in BOARD_KINDS, so the subscription carried it —
 * the board re-folded, and every title stayed "[encrypted]" for the full 45s
 * deadline. LiveBoard.src, and with it the keyring the fold decrypts through,
 * was the one derived at LOAD; nothing re-derived it, so the CEK inside the
 * grant that had just landed was never unwrapped.
 *
 * WHY IT IS DRIVEN THROUGH THE REAL loadBoardItems + startLiveUpdates: the bug
 * lived exactly in the seam between them — what the load hands the subscription,
 * and what the subscription is allowed to redo. A hand-built LiveBoard would
 * have asserted the fix against a fixture of the fix.
 *
 * THE ANTI-TAUTOLOGY is the load half of every case: the same identity, the same
 * relay and the same card render the PLACEHOLDER first. Without that, "the title
 * is right after the grant" would pass on a session that could read all along.
 */
describe("ready-48f: an owner-signed grant arriving LIVE unwraps the CEK with no reload", () => {
  /** The latecomer: a key with NO grant in the load snapshot, whose grant
   * (grantEpoch1Late, created_at 1750000600 — later than everything the load
   * folds, so the live REQ's `since` cursor cannot hide it) arrives afterwards. */
  const latecomer: Identity = {
    pubkey: LATECOMER_PUB,
    auth: authTransition({ type: "login", method: "extension" }),
  };

  async function openAsLatecomer() {
    const { ctor, handle } = makeNip01Relay({ events: [...LOAD_SNAPSHOT] });
    const deps: BoardDeps = {
      loadRelays: async () => [RELAY],
      fetchEvents: (relays, filter, opts) =>
        fetchEventsFromRelays(relays, filter, { ...opts, webSocketCtor: ctor, retries: 0, timeoutMs: 2000 }),
      keyUnwrapper: () => nip07KeyUnwrapper(fakeNip44Signer(LATECOMER_SEC)),
      subscribeEvents: (relays, filter, opts) => subscribeToRelays(relays, filter, { ...opts, webSocketCtor: ctor }),
    };
    const { items, live, confidential } = await loadBoardItems(
      [board],
      [RELAY],
      // The authority the load sees: the board definition and the grants that
      // establish the cutover. NONE of them names the latecomer.
      [confBoardEvent, ...confGrants],
      latecomer,
      deps,
      () => {},
    );
    expect(confidential).toBe(true);
    expect(live).toHaveLength(1);
    return { items, live, handle, deps };
  }

  it("the ungranted session shows placeholders, and the SAME document shows the titles once the grant lands", async () => {
    const { items, live, handle, deps } = await openAsLatecomer();

    // ANTI-TAUTOLOGY 1 — the load really is sealed for this key, and really did
    // project the card. A blank board would satisfy "no plaintext" for free.
    const loadedConf1 = items.find((i) => i.id === "conf-001");
    expect(loadedConf1, "the ungranted reader must still see the CARD").toBeDefined();
    expect(loadedConf1!.title).toBe("[encrypted]");
    expect(live[0].granted, "the latecomer holds no grant at load").toBe(false);

    const emitted: Item[][] = [];
    const sub = startLiveUpdates({
      boards: live,
      relays: [RELAY],
      subscribe: deps.subscribeEvents!,
      onItems: (next) => emitted.push(next),
      coalesceMs: 5,
    });
    try {
      // Let the subscription's socket finish opening before pushing: a push to
      // a relay with no open REQ would be delivered to nobody, and the assertion
      // below would then be measuring the fixture rather than the page.
      await settle();
      // The fixture is what this case needs it to be: an OWNER-signed grant, to
      // THIS key, newer than anything the load folded.
      expect(grantEpoch1Late.pubkey).toBe(CONF_OWNER);
      expect(grantEpoch1Late.tags).toContainEqual(["p", LATECOMER_PUB]);
      expect(handle.push(grantEpoch1Late)).toBe(1);
      await settle();

      const latest = emitted.at(-1);
      expect(latest, "the live path must emit after a grant arrives").toBeDefined();
      const conf1 = latest!.find((i) => i.id === "conf-001");
      expect(conf1, "the card must still be there").toBeDefined();
      expect(conf1!.title).toBe(CONF1.title);
      expect(conf1!.redacted).toBeFalsy();
      // The page's own record of this key's standing moved with it — that is
      // what the awaiting-authorization panel is driven by.
      expect(live[0].granted).toBe(true);
    } finally {
      sub.close();
    }
  });

  it("a grant NOT signed by the board owner changes nothing — the same derivation drops it live and at load", async () => {
    const { items, live, handle, deps } = await openAsLatecomer();
    expect(items.find((i) => i.id === "conf-001")!.title).toBe("[encrypted]");

    const emitted: Item[][] = [];
    const sub = startLiveUpdates({
      boards: live,
      relays: [RELAY],
      subscribe: deps.subscribeEvents!,
      onItems: (next) => emitted.push(next),
      coalesceMs: 5,
    });
    try {
      await settle();
      // The SAME grant, re-signed by a key that is not the board owner. Every
      // byte that matters to the fold is identical — same grantee, same wrapped
      // CEK, same epoch, same coordinate, a VALID signature — and only the
      // AUTHOR differs. If re-deriving authority live were driven by "a 39301
      // arrived" rather than by who signed it, this is the event that would open
      // a confidential board to anyone holding a relay connection.
      const forged = signNostrEvent(
        {
          created_at: grantEpoch1Late.created_at + 1,
          kind: grantEpoch1Late.kind,
          tags: grantEpoch1Late.tags,
          content: grantEpoch1Late.content,
        },
        STRANGER_SEC,
      );
      expect(forged.pubkey).not.toBe(CONF_OWNER);
      expect(handle.push(forged)).toBe(1);
      await settle();

      // Whether it triggers a re-fold is an efficiency question. What must hold
      // is that nothing it carried was believed.
      const latest = emitted.at(-1) ?? items;
      expect(latest.find((i) => i.id === "conf-001")!.title).toBe("[encrypted]");
      expect(live[0].granted).toBe(false);
    } finally {
      sub.close();
    }
  });
});
