// @vitest-environment jsdom
//
// ready-daf ROUND 2 — PARTIAL grant omission, and the two witnesses that catch it.
//
// THE DEFECT THIS PINS. Round 1 treated grant omission as ALL-OR-NOTHING: with
// every kind-39301 withheld, BoardKeyring.cutover() returned null, the state went
// "unknown" and the fold gate stayed on. But the cutover is a MINIMUM over the
// owner CEK grants that were SERVED (keyring.ts's noteCutover), so a relay does
// not have to omit everything. It omits the OLDEST grants and keeps the rest:
// cutover() then returns a non-null instant LATER than the truth, confidentialityOf
// reported "confidential" — ESTABLISHED — and every plaintext card authored between
// the real cutover and the manufactured one was grandfathered straight into the
// DOM. The round-1 mechanism was bypassed entirely, using nothing but selective
// silence.
//
//	tGrantE1  1750000100  the board really goes confidential here
//	conf-008  1750000205  owner-signed PLAINTEXT — post-cutover, must be withheld
//	tGrantE2  1750000310  the cutover a relay manufactures by dropping epoch 1
//
// THE TWO WITNESSES, both signature-verified and both relay-INDEPENDENT. A relay
// can drop a card but cannot forge one: only a holder of a CEK can produce a
// sealed body, and only the card's author key can sign it. So a sealed card is
// testimony a relay cannot fabricate and cannot alter — it can only suppress it,
// and suppressing the cards is the visible act round 1 already documented.
//
//	A. TIME. A verified sealed card OLDER than the derived cutover proves the
//	   board was already confidential before that instant, so the derived cutover
//	   is too late. Nothing about epochs is needed for this one.
//	B. EPOCH. A verified sealed card naming a cek_epoch BELOW the lowest epoch any
//	   served owner grant covers proves the grant that minted that epoch was not
//	   served — and since epochs increase with each rotation, that grant is OLDER
//	   than the ones that were, so again the derived cutover is too late.
//
// Neither subsumes the other, and each is isolated below on a fixture where the
// other is provably silent — a card sealed at epoch 1 AFTER the rotation for (B),
// a late-signed epoch-1 grant for (A). That is deliberate: a single fixture where
// both fire would let either guard be deleted with the suite still green.
//
// EVERY CASE IS PAIRED WITH ITS OPPOSITE. Serve the same board's grants in full
// and conf-008 must STILL be quarantined (it is post-cutover cleartext either
// way) while the page says nothing about an unestablished state; and a sealed card
// at an epoch ABOVE everything the grants cover must NOT trip the epoch witness,
// because omitting a LATER grant cannot move a minimum earlier.

import { beforeEach, describe, expect, it } from "vitest";
import { afterLogin, type BoardDeps, type Identity } from "./main";
import { authTransition } from "./lib/auth";
import { fakeNip44Signer } from "./lib/fakesigner";
import { nip07KeyUnwrapper } from "./lib/keyunwrap";
import { PLACEHOLDER } from "./lib/envelope";
import { hexToBytes } from "./lib/sha256";
import { tagValue, type NostrEvent } from "./lib/nostrevent";
import type { ParsedFragment } from "./lib/fragment";
import {
  BOARD_COORD,
  CEK_EPOCH1,
  CUTOVER,
  CUTOVER_IF_EPOCH1_WITHHELD,
  GAP_PLAINTEXT_AT,
  GAP_PLAINTEXT_BODY,
  EPOCH1_AFTER_ROTATION_TITLE,
  GAP_PLAINTEXT_TITLE,
  MEMBER_PUB,
  MEMBER_SEC,
  STRANGER_PUB,
  STRANGER_SEC,
  boardEvent,
  cardEpoch1A,
  cardEpoch1AfterRotation,
  cardEpochNobodyHolds,
  cardGapPlaintext,
  cardGrandfatheredPlaintext,
  cards,
  grantEpoch1Late,
  grants,
} from "./lib/confidential.fixtures";

/**
 * The grants a relay serves when it drops the OLDEST ones: everything except the
 * epoch-1 grants. Filtered by the signed cek_epoch tag rather than by index, so
 * it cannot silently stop dropping anything if the fixture order changes.
 *
 * What survives is the epoch-2 rotation pair and the CEK-less revocation — a
 * perfectly ordinary-looking answer. Note this is also the answer a NIP-01
 * CONFORMANT relay gives on its own: kind 39301 is addressable, its `d` tag is
 * `<board>:<grantee>`, so the epoch-2 grant REPLACES that grantee's epoch-1 grant
 * and the older one is not retained. Selective omission here is not only an
 * attack, it is the default state of a rotated board.
 */
const GRANTS_MINUS_EPOCH1 = grants.filter((g) => tagValue(g, "cek_epoch") !== "1");

let root: HTMLElement;

beforeEach(() => {
  document.body.replaceChildren();
  root = document.createElement("div");
  root.id = "app";
  document.body.append(root);
});

function signingIdentity(pubkey: string): Identity {
  return { pubkey, auth: authTransition({ type: "login", method: "extension" }) };
}

/** deps serves `snapshot` for every REQ — a relay honours no filter it does not
 * feel like honouring — and wires a real NIP-44 signer for `secretHex`. */
function deps(snapshot: NostrEvent[], secretHex: string): BoardDeps {
  return {
    loadRelays: async () => ["wss://relay.test"],
    fetchEvents: async () => snapshot,
    keyUnwrapper: () => nip07KeyUnwrapper(fakeNip44Signer(secretHex)),
  };
}

const boardFragment: ParsedFragment = { kind: "board", board: BOARD_COORD, relays: ["wss://relay.test"] };

function renderedItems(): { id: string; title: string }[] {
  return [...root.querySelectorAll(".card[data-id]")].map((card) => ({
    id: card.querySelector(".card-id")?.textContent ?? "",
    title: card.querySelector(".card-title")?.textContent ?? "",
  }));
}

const pageText = () => root.textContent ?? "";
const notice = () => root.querySelector(".confidential-notice")?.textContent ?? "";
/** The left tree's "Everything" count — the whole ADMITTED item set, including
 * terminal items that render no card. It is the number the fold gate moves. */
const everythingCount = () => root.querySelector('.left-tree .node[data-scope=""] .ct')?.textContent;

/** The three instants the exploit turns on must stand in this order, or every
 * assertion below is measuring something else. Asserted, not assumed, because
 * they come out of a GENERATOR that is free to be re-run. */
it("FIXTURE INVARIANT: the gap card sits strictly between the true and the manufactured cutover", () => {
  expect(CUTOVER).toBeLessThan(GAP_PLAINTEXT_AT);
  expect(GAP_PLAINTEXT_AT).toBeLessThan(CUTOVER_IF_EPOCH1_WITHHELD);
  expect(cardGapPlaintext.created_at).toBe(GAP_PLAINTEXT_AT);
  // It really is cleartext, and really is signed by the board owner: no forgery
  // is involved in this exploit, only omission.
  expect(tagValue(cardGapPlaintext, "title")).toBe(GAP_PLAINTEXT_TITLE);
  expect(tagValue(cardGapPlaintext, "enc")).toBe("");
  expect(cardGapPlaintext.pubkey).toBe(boardEvent.pubkey);
  // And the withheld set really is the epoch-1 grants, nothing else.
  expect(GRANTS_MINUS_EPOCH1.length).toBe(grants.length - 4);
  expect(GRANTS_MINUS_EPOCH1.some((g) => tagValue(g, "cek_epoch") === "2")).toBe(true);
});

describe("PARTIAL omission: dropping only the OLDEST grants moves the cutover LATER", () => {
  /** The exploit snapshot: the whole board, the gap card, and every grant EXCEPT
   * the epoch-1 ones. Nothing forged, nothing altered — four events not sent. */
  const EXPLOIT: NostrEvent[] = [boardEvent, ...GRANTS_MINUS_EPOCH1, ...cards, cardGapPlaintext];

  it("does not render the post-cutover cleartext the manufactured cutover grandfathers", async () => {
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(EXPLOIT, STRANGER_SEC));

    // THIS IS THE BYPASS. With the epoch-1 grants dropped the derived cutover is
    // 1750000310, conf-008 was authored at 1750000205, so shouldQuarantine's
    // grandfather clause admitted it — title and body, in clear, on a board the
    // page called "confidential" with a straight face.
    expect(renderedItems().map((i) => i.id)).not.toContain("conf-008");
    expect(pageText()).not.toContain(GAP_PLAINTEXT_TITLE);
    expect(pageText()).not.toContain(GAP_PLAINTEXT_BODY);
  });

  it("reports the state as UNESTABLISHED even though a grant WAS served", async () => {
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(EXPLOIT, STRANGER_SEC));

    // Round 1's "unknown" was reachable only when cutover() returned null. Here
    // it returns a real instant — just the wrong one — so this assertion is what
    // separates "no grant arrived" from "the grants that arrived are not the
    // earliest ones".
    expect(notice()).toContain("CONFIDENTIALITY STATE COULD NOT BE ESTABLISHED");
    expect(notice()).toContain("Confidential Board");
  });

  it("says the omission is PROVEN, not merely possible", async () => {
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(EXPLOIT, STRANGER_SEC));

    // The stronger, and true, statement: this is not "we cannot rule out
    // omission" (round 1's honest limit) — a signature-verified card on the board
    // contradicts the served grants outright.
    expect(notice()).toContain("PROVEN");
    expect(notice()).toContain("older than the earliest grant");
  });

  it("grandfathers NOTHING: the legitimate pre-cutover card is withheld too", async () => {
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(EXPLOIT, STRANGER_SEC));

    // Fail-closed is not selective. With the cutover unestablished, "authored
    // before the board went confidential" is not a claim this page can make about
    // ANY plaintext card, so conf-006 goes out with conf-008.
    //
    // Counted, not asserted as absent text: conf-006 is `done`, terminal items
    // have no column, so not.toContain("Legacy plaintext card") would pass either
    // way. ADMITTED: conf-001..004 (sealed, well-formed). WITHHELD: conf-005,
    // conf-006, conf-008 (plaintext). DROPPED: conf-007 (forged signature).
    expect(everythingCount()).toBe("4");
  });

  it("holds for the MEMBER, who still holds the epoch-2 key the served grants carry", async () => {
    // Not a keyless reader: this one's epoch-2 wrap really opens, so conf-003
    // renders its true title. Reading power and cutover knowledge are separate
    // things, and holding one must not manufacture the other.
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(EXPLOIT, MEMBER_SEC));

    expect(notice()).toContain("CONFIDENTIALITY STATE COULD NOT BE ESTABLISHED");
    expect(pageText()).not.toContain(GAP_PLAINTEXT_TITLE);
    expect(everythingCount()).toBe("4");
  });
});

describe("WITNESS A in isolation — a sealed card OLDER than the derived cutover", () => {
  /** One grant only, and it is an owner CEK grant for epoch 1 signed at
   * 1750000600 — a member added long after the rotation. So the epoch set the
   * served grants cover starts at 1, WITNESS B has nothing to say, and the
   * derived cutover is 1750000600: five hundred seconds after the truth. */
  const LATE_GRANT_ONLY: NostrEvent[] = [boardEvent, grantEpoch1Late, cardEpoch1A, cardGapPlaintext];

  it("FIXTURE INVARIANT: witness B is provably silent on this snapshot", () => {
    // The one served grant covers epoch 1; the one sealed card names epoch 1.
    // There is no uncovered epoch here, so anything this case detects, it detects
    // by TIME.
    expect(tagValue(grantEpoch1Late, "cek_epoch")).toBe("1");
    expect(tagValue(cardEpoch1A, "cek_epoch")).toBe("1");
    expect(grantEpoch1Late.created_at).toBeGreaterThan(cardEpoch1A.created_at);
    expect(grantEpoch1Late.created_at).toBeGreaterThan(GAP_PLAINTEXT_AT);
  });

  it("withholds the cleartext the late cutover would grandfather", async () => {
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(LATE_GRANT_ONLY, STRANGER_SEC));

    expect(pageText()).not.toContain(GAP_PLAINTEXT_TITLE);
    expect(notice()).toContain("CONFIDENTIALITY STATE COULD NOT BE ESTABLISHED");
    // conf-001 admitted (sealed, well-formed, placeholder), conf-008 withheld.
    expect(everythingCount()).toBe("1");
    expect(renderedItems().map((i) => i.title)).toEqual([PLACEHOLDER]);
  });
});

describe("WITNESS B in isolation — a sealed card at an epoch no served grant covers", () => {
  /** The epoch-1 grants are dropped, and the ONLY sealed card served is one
   * sealed under epoch 1 but published AFTER the rotation, at 1750000700. It is
   * NEWER than the derived cutover of 1750000310, so witness A cannot see it. The
   * only thing wrong with the snapshot is that a card names key epoch 1 while no
   * served grant covers epoch 1 — which is only possible if a grant is missing. */
  const STALE_EPOCH: NostrEvent[] = [boardEvent, ...GRANTS_MINUS_EPOCH1, cardEpoch1AfterRotation, cardGapPlaintext];

  it("FIXTURE INVARIANT: witness A is provably silent on this snapshot", () => {
    expect(cardEpoch1AfterRotation.created_at).toBeGreaterThan(CUTOVER_IF_EPOCH1_WITHHELD);
    expect(tagValue(cardEpoch1AfterRotation, "cek_epoch")).toBe("1");
    expect(tagValue(cardEpoch1AfterRotation, "enc")).toBe("1");
    // No served grant covers epoch 1 — that is the whole signal.
    expect(GRANTS_MINUS_EPOCH1.every((g) => tagValue(g, "cek_epoch") !== "1")).toBe(true);
    // And the only OTHER event served is the plaintext gap card, which carries no
    // epoch at all, so it cannot be the thing that fires the witness.
    expect(tagValue(cardGapPlaintext, "cek_epoch")).toBe("");
  });

  it("withholds the cleartext, on the epoch mismatch alone", async () => {
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(STALE_EPOCH, STRANGER_SEC));

    expect(pageText()).not.toContain(GAP_PLAINTEXT_TITLE);
    expect(notice()).toContain("CONFIDENTIALITY STATE COULD NOT BE ESTABLISHED");
    // conf-009 admitted as a placeholder, conf-008 withheld.
    expect(everythingCount()).toBe("1");
    expect(renderedItems().map((i) => i.title)).toEqual([PLACEHOLDER]);
  });

  it("names the epoch mismatch in the notice, so a reader can act on it", async () => {
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(STALE_EPOCH, STRANGER_SEC));
    expect(notice()).toContain("key epoch");
  });

  it("a link carrying the epoch-1 KEY does not disarm the witness", async () => {
    // The witness compares the cards' epochs against what the served GRANTS
    // cover. A link carries whatever epochs its minter held, so if those counted
    // towards that floor the reader could be handed the very key that makes the
    // contradiction disappear: floor 1, no card below it, cutover 1750000310
    // believed, gap card grandfathered. keyring.ts's applyFragmentKeys therefore
    // touches `grantEpochFloors` no more than it touches `cutovers`, and this is
    // the composition-level proof of it — the unit-level one is in
    // lib/keyring.test.ts.
    const withEpoch1Key: ParsedFragment = {
      kind: "board",
      board: BOARD_COORD,
      relays: ["wss://relay.test"],
      viewer: MEMBER_PUB,
      keys: { ceks: [{ epoch: 1, key: hexToBytes(CEK_EPOCH1) }] },
    };
    await afterLogin(root, signingIdentity(MEMBER_PUB), withEpoch1Key, deps(STALE_EPOCH, MEMBER_SEC));

    // The key really works — conf-009 is sealed under epoch 1 and its title opens,
    // so this is not passing because the reader was left blind.
    expect(pageText()).toContain(EPOCH1_AFTER_ROTATION_TITLE);
    // And the cleartext is still withheld and the state still unestablished.
    expect(pageText()).not.toContain(GAP_PLAINTEXT_TITLE);
    expect(notice()).toContain("PROVEN");
  });
});

describe("ANTI-TAUTOLOGY: complete grants still establish the cutover", () => {
  /** The exploit snapshot with the four epoch-1 grants PUT BACK. */
  const COMPLETE: NostrEvent[] = [boardEvent, ...grants, ...cards, cardGapPlaintext];

  it("conf-008 is STILL withheld — it was always post-cutover cleartext", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(COMPLETE, MEMBER_SEC));

    // The gap card is not evidence of anything; it is the thing being smuggled.
    // With the real cutover known it is quarantined by the ORDINARY rule, which
    // is why the exploit had to move the cutover rather than forge a card.
    expect(pageText()).not.toContain(GAP_PLAINTEXT_TITLE);
    expect(renderedItems().map((i) => i.id)).not.toContain("conf-008");
  });

  it("says nothing about an unestablished state, and grandfathers conf-006", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(COMPLETE, MEMBER_SEC));

    // If either witness fired here, the fix would be quarantining every rotated
    // board wholesale instead of reacting to a contradiction. ADMITTED:
    // conf-001..004 plus the genuinely pre-cutover conf-006.
    expect(notice()).not.toContain("COULD NOT BE ESTABLISHED");
    expect(notice()).toContain("titles were decrypted in your browser");
    expect(everythingCount()).toBe("5");
  });

  it("an epoch ABOVE every served grant is NOT a witness", async () => {
    // conf-004 is sealed under epoch 9 and NO grant in the fixture covers epoch 9
    // — not even with every grant served. A broad "any uncovered epoch means
    // omission" rule would fire here and quarantine an established board, so the
    // rule is deliberately narrower: only an epoch BELOW the lowest served one
    // says a grant older than the ones served is missing, and only an older grant
    // can move a MINIMUM. Omitting a later grant leaves the cutover correct.
    await afterLogin(
      root,
      signingIdentity(STRANGER_PUB),
      boardFragment,
      deps([boardEvent, ...grants, cardEpoch1A, cardEpochNobodyHolds, cardGrandfatheredPlaintext], STRANGER_SEC),
    );

    expect(notice()).not.toContain("COULD NOT BE ESTABLISHED");
    // conf-001 + conf-004 sealed, conf-006 grandfathered on the TRUE cutover.
    expect(everythingCount()).toBe("3");
  });
});
