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
import { subscribeToRelays } from "./lib/relay";
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
} from "./lib/confidential.fixtures";

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
  const { ctor, handle } = makeNip01Relay({ events: [] });
  const deps: BoardDeps = {
    loadRelays: async () => [RELAY],
    fetchEvents: async () => LOAD_SNAPSHOT,
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
