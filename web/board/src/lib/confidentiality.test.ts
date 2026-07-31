// @vitest-environment jsdom
//
// §11.13a's OWNER-SIGNED CUTOVER ASSERTION (ready-475) in the browser —
// `confidential_since` on the board's own kind-30301 definition, honoured by
// confidentialityOf in preference to the grant-minimum heuristic.
//
// THE LIVE SHAPE THIS EXISTS FOR. The `ready` board carries three kind-1630
// status events sealed under a TEST-LOCAL CEK that was never a ready-board CEK
// (envelope_live_relay_test.go's fixtures, written to the production board before
// the ready-fce guard existed). Kind 1630 is a REGULAR event, so unlike a card
// they can never be superseded: they are permanent, they are older than the
// board's true cutover, and WITNESS A therefore fires on them forever. Measured
// 2026-07-30: the page showed 369 of the board's 536 cards to its own owner, on a
// board that is otherwise entirely healthy. `cardEpoch1A` plays that part below —
// a verified sealed card older than the derived cutover, which no witness can
// tell from real evidence and which the owner's own signature can.
//
// WHAT IS PINNED HERE, and each of the three security properties is verified RED
// with its own guard removed and green with it restored (one at a time — see each
// test's GUARD note):
//
//  1. A board with NO assertion behaves exactly as today.
//  2. An assertion signed by anyone but the board owner is IGNORED.
//  3. A relay OMITTING the assertion yields today's behaviour, not a wider one.
//
// THE SNAPSHOT IS THE EXPLOIT SNAPSHOT main.grantsomission.test.ts USES — the
// same fixture board with the epoch-1 grants dropped, which is what a
// NIP-01-conformant relay serves for a rotated board on its own. There the
// withheld verdict is correct and the suite pins that it holds; here the owner
// says out loud when the board went confidential, and the same snapshot must fold
// its pre-cutover history again WITHOUT folding a single post-cutover cleartext
// card. Both suites therefore have to keep passing, which is the real statement:
// the assertion is an extra input, not a hole in the witnesses.
//
// The Go half of the same contract is pkg/sync/keydist_confidentialsince_test.go,
// and the shared conformance vectors (internal/foldvectors/cases_confidentialsince.go)
// are what keep the two readers from drifting.

import { describe, expect, it, beforeEach } from "vitest";
import { afterLogin, type BoardDeps, type Identity } from "../main";
import { authTransition } from "./auth";
import { confidentialityOf, encryptedBoardsOf } from "./confidentiality";
import { assertedConfidentialSince, deriveBoardKeyring } from "./keyring";
import { projectItems } from "./fold";
import { fakeNip44Signer } from "./fakesigner";
import { nip07KeyUnwrapper } from "./keyunwrap";
import { signNostrEvent, xOnlyPubkey } from "./schnorrsign";
import { tagValue, type NostrEvent } from "./nostrevent";
import type { ParsedFragment } from "./fragment";
import {
  BOARD_COORD,
  BOARD_D,
  CUTOVER,
  CUTOVER_IF_EPOCH1_WITHHELD,
  GAP_PLAINTEXT_AT,
  GAP_PLAINTEXT_BODY,
  GAP_PLAINTEXT_TITLE,
  OWNER_PUB,
  OWNER_SEC,
  STRANGER_PUB,
  STRANGER_SEC,
  boardEvent,
  cardEpoch1A,
  cardForgedSignature,
  cardGapPlaintext,
  cardGrandfatheredPlaintext,
  cards,
  grants,
} from "./confidential.fixtures";

const BOARD_TITLE = "Confidential Board";
const GRANTS_MINUS_EPOCH1 = grants.filter((g) => tagValue(g, "cek_epoch") !== "1");
const GRANDFATHERED_ID = tagValue(cardGrandfatheredPlaintext, "d");
const GAP_ID = tagValue(cardGapPlaintext, "d");

/**
 * boardDefinition builds a kind-30301 definition for this fixture's board,
 * optionally carrying the assertion, signed by `secretHex`.
 *
 * Signed HERE rather than taken from the generated fixture file because the
 * point of half these cases is a definition the board's owner did NOT sign, or
 * did not sign in this shape — a fixture generator emits only well-formed events.
 * The cross-implementation agreement about the tag itself is pinned by the shared
 * conformance vectors, which are built by the Go writer and folded by both
 * readers; nothing here is claiming to stand in for that.
 */
function boardDefinition(secretHex: string, since: number | null, at = boardEvent.created_at + 1): NostrEvent {
  const tags: string[][] = [
    ["d", BOARD_D],
    ["title", BOARD_TITLE],
  ];
  if (since !== null) tags.push(["confidential_since", String(since)]);
  return signNostrEvent({ created_at: at, kind: 30301, tags, content: "" }, secretHex);
}

/** The owner's definition, asserting the instant the board really went
 * confidential — the same instant the epoch-1 grants would have established. */
const ASSERTED = boardDefinition(OWNER_SEC, CUTOVER);

/** The owner's definition with the tag added AFTER signing: the id and signature
 * no longer cover the tags, so nothing here may be believed. */
const TAMPERED: NostrEvent = {
  ...boardEvent,
  tags: [...boardEvent.tags, ["confidential_since", String(CUTOVER)]],
};

/** A STRANGER's board definition naming the same "d" tag. Correctly signed, and
 * about a different coordinate — 30301:<stranger>:confboard. */
const FOREIGN = boardDefinition(STRANGER_SEC, CUTOVER);

/**
 * EXPLOIT is main.grantsomission.test.ts's snapshot: the whole fixture board with
 * only the epoch-1 grants missing. `def` replaces the board definition, which is
 * the ONE event these cases vary.
 */
function exploit(def: NostrEvent, extra: NostrEvent[] = []): NostrEvent[] {
  return [def, ...GRANTS_MINUS_EPOCH1, ...cards, cardGapPlaintext, ...extra];
}

/** The complete, healthy answer: nothing omitted, so §11.13's derived instant is
 * the truth and no witness has anything to say. */
function healthy(def: NostrEvent): NostrEvent[] {
  return [def, ...grants, ...cards, cardGapPlaintext];
}

/**
 * fold runs the REAL production chain for one board — derive the keyring, decide
 * confidentiality, adapt it to the fold's gate, project — exactly as main.ts's
 * deriveRead does, and returns what each step said. Nothing is stubbed: the
 * events are the fixture's own signed events and the unwrap is a spec-correct
 * NIP-44 implementation standing in for the extension.
 */
async function fold(snapshot: NostrEvent[], readerSecret: string) {
  const keyring = await deriveBoardKeyring(
    snapshot,
    xOnlyPubkey(readerSecret),
    OWNER_PUB,
    BOARD_D,
    nip07KeyUnwrapper(fakeNip44Signer(readerSecret)),
  );
  const conf = confidentialityOf(keyring, BOARD_COORD, snapshot, false);
  const encryptedBoards = encryptedBoardsOf(keyring, conf.state);
  const items = projectItems(snapshot, {
    trusted: new Set([OWNER_PUB]),
    maintainers: null,
    pinnedBoard: BOARD_COORD,
    decryptor: keyring,
    encryptedBoards,
  });
  return { keyring, conf, gate: encryptedBoards.cutover(BOARD_COORD), items };
}

it("FIXTURE INVARIANT: the snapshot really is the withheld shape, and the assertion really is the truth", () => {
  // The three instants this whole file turns on, asserted rather than assumed:
  // they come out of a generator that is free to be re-run.
  expect(cardGrandfatheredPlaintext.created_at).toBeLessThan(CUTOVER);
  expect(CUTOVER).toBeLessThan(GAP_PLAINTEXT_AT);
  expect(GAP_PLAINTEXT_AT).toBeLessThan(CUTOVER_IF_EPOCH1_WITHHELD);
  // WITNESS A's testimony on this snapshot: a verified sealed card older than the
  // instant the surviving grants derive.
  expect(cardEpoch1A.created_at).toBeLessThan(CUTOVER_IF_EPOCH1_WITHHELD);
  expect(GRANTS_MINUS_EPOCH1.some((g) => tagValue(g, "cek_epoch") === "1")).toBe(false);
  // The assertion is owner-signed, and the foreign one is not.
  expect(ASSERTED.pubkey).toBe(OWNER_PUB);
  expect(FOREIGN.pubkey).toBe(STRANGER_PUB);
  expect(FOREIGN.pubkey).not.toBe(OWNER_PUB);
});

describe("the owner's assertion establishes the cutover the witness can only refute", () => {
  it("restores the board's pre-cutover history without grandfathering one post-cutover card", async () => {
    // CONTROL — the identical snapshot with today's definition is fully withheld.
    const before = await fold(exploit(boardEvent), STRANGER_SEC);
    expect(before.conf).toEqual({ state: "unknown", why: "grants-withheld" });
    expect(before.items.has(GRANDFATHERED_ID)).toBe(false);

    const after = await fold(exploit(ASSERTED), STRANGER_SEC);
    expect(after.conf).toEqual({ state: "confidential", why: null });
    expect(after.gate).toEqual({ cutover: CUTOVER, ok: true });
    // The 31%-invisible board's cards come back...
    expect(after.items.has(GRANDFATHERED_ID)).toBe(true);
    expect(after.items.get(GRANDFATHERED_ID)?.title).toBe("Legacy plaintext card");
    // ...and the fail-closed path is intact: the cleartext card authored AFTER
    // the asserted instant is still quarantined, exactly as the true cutover
    // quarantines it.
    expect(after.items.has(GAP_ID)).toBe(false);
    expect(after.items.has("conf-005")).toBe(false); // the smuggled post-cutover cleartext
  });

  it("never grandfathers MORE than the served grants already do", async () => {
    // An assertion LATER than the board's own earliest served grant does not
    // apply: the effective cutover is min(asserted, derived). Without the min the
    // gap card — post-cutover cleartext on a HEALTHY board — would fold.
    //
    // GUARD: the `at < cur` comparison in noteConfidentialSince (keyring.ts).
    // Assign unconditionally and this goes RED on exactly that card. Verified.
    const late = await fold(healthy(boardDefinition(OWNER_SEC, GAP_PLAINTEXT_AT + 1)), STRANGER_SEC);
    expect(late.gate).toEqual({ cutover: CUTOVER, ok: true });
    expect(late.items.has(GAP_ID)).toBe(false);
  });

  it("a keyholder sees the same cutover as a stranger — reading power does not decide it", async () => {
    // The two readers differ in what they can DECRYPT and must not differ in what
    // they believe about WHEN. Same snapshot, owner's own key.
    const stranger = await fold(exploit(ASSERTED), STRANGER_SEC);
    const owner = await fold(exploit(ASSERTED), OWNER_SEC);
    expect(owner.gate).toEqual(stranger.gate);
    expect(owner.conf).toEqual(stranger.conf);
    expect(owner.items.has(GRANDFATHERED_ID)).toBe(true);
  });
});

describe("an assertion with NO GRANTS establishes the instant, not read access", () => {
  // The ready-475 REWORK's second question, answered here for the browser and in
  // pkg/sync/keydist_confidentialsince_test.go for Go — and pinned as a SHARED
  // vector too (keyring_confidential_since_with_no_grants_...), because "a sealed
  // card plus no derived cutover" is exactly the shape fold.vectors.test.ts's
  // divergence check exists about, and the assertion is what takes it out of that
  // zone: with one, both readers land on the same instant, the same gate and the
  // same items.
  //
  // THE RULING: `state: "confidential"` from an assertion alone is a statement
  // about WHEN, never about what this reader may read. The gate comes ON at the
  // asserted instant; the keyring stays empty; every sealed card keeps rendering
  // the placeholder.
  //
  // GUARD: `noteConfidentialSince`'s `this.noteCutover(coord, at)` (keyring.ts) —
  // the assertion feeding the CUTOVER and not only the `assertedSince` map. Drop
  // that one line and this suite goes RED on "establishes the instant with zero
  // grants served": confidentialityOf still short-circuits to "confidential", but
  // there is no derived cutover behind it, so encryptedBoardsOf reports
  // `{cutover: 0, ok: false}` — the gate INERT — and the post-cutover cleartext
  // folds. Verified red with that one change, green with it restored.
  //
  // Note which guard this is NOT. Removing confidentialityOf's short-circuit
  // instead leaves this suite GREEN, because with no grants there is no
  // grantEpochFloor and no sealed card older than the asserted instant, so no
  // witness has anything to say and the assertion-fed cutover carries the case on
  // its own. The short-circuit's own guard test is PROPERTY 3 below, on the
  // snapshot where a witness DOES fire.

  /** The exploit snapshot with EVERY grant removed: a reader who was never
   * granted anything, or whose relay answer omitted the lot. */
  const noGrants = (def: NostrEvent) => [def, ...cards, cardGapPlaintext];

  it("CONTROL: with no grants and no assertion the browser cannot establish anything", async () => {
    const r = await fold(noGrants(boardEvent), STRANGER_SEC);
    expect(r.conf).toEqual({ state: "unknown", why: "no-grant" });
    expect(r.gate).toEqual({ cutover: 0, ok: true });
    // Fail-closed with no instant grandfathers nothing at all.
    expect(r.items.has(GRANDFATHERED_ID)).toBe(false);
    expect(r.items.has(GAP_ID)).toBe(false);
  });

  it("the owner's assertion establishes the instant with zero grants served", async () => {
    const r = await fold(noGrants(ASSERTED), STRANGER_SEC);
    expect(r.conf).toEqual({ state: "confidential", why: null });
    expect(r.gate).toEqual({ cutover: CUTOVER, ok: true });
    // The board's genuinely pre-cutover history is shown...
    expect(r.items.has(GRANDFATHERED_ID)).toBe(true);
    // ...and the fail-closed path is intact at the instant the owner named.
    expect(r.items.has(GAP_ID)).toBe(false);
    expect(r.items.has("conf-005")).toBe(false); // the smuggled post-cutover cleartext
  });

  it("and confers NOTHING to read the board with", async () => {
    // The half that would make the ruling wrong if it failed: establishing WHEN a
    // board went confidential is not being able to read it, and only the first is
    // the owner's to state. No key arrives, so every sealed card stays a
    // placeholder and no free text reaches the page.
    const r = await fold(noGrants(ASSERTED), STRANGER_SEC);
    expect(r.keyring.cek(BOARD_COORD, 1)).toBeNull();
    expect(r.keyring.ltk(BOARD_COORD)).toBeNull();
    expect(r.keyring.currentEpoch(BOARD_COORD)).toBeNull();
    const sealedIDs = cards
      .filter((c) => tagValue(c, "enc") === "1" && c.id !== cardForgedSignature.id)
      .map((c) => tagValue(c, "d"));
    expect(sealedIDs.length).toBeGreaterThan(0);
    for (const id of sealedIDs) {
      expect(r.items.get(id)?.title).toBe("[encrypted]");
    }
  });
});

describe("PROPERTY 1 — a board with NO assertion behaves exactly as today", () => {
  // GUARD: boardConfidentialSince's positive-integer check (keyring.ts) — "an
  // absent or malformed tag is NOT an assertion". Return 0 for a board carrying
  // no tag and the healthy arm goes RED: the effective cutover becomes
  // min(0, derived) = 0 and every plaintext card on the board vanishes.
  // Verified red with that one change, green with it restored.
  it("a healthy board still derives its real cutover and grandfathers its real history", async () => {
    const r = await fold(healthy(boardEvent), STRANGER_SEC);
    expect(r.conf).toEqual({ state: "confidential", why: null });
    expect(r.gate).toEqual({ cutover: CUTOVER, ok: true });
    expect(r.items.has(GRANDFATHERED_ID)).toBe(true);
    expect(r.items.has(GAP_ID)).toBe(false);
  });

  it("a contradicted board still fails closed, with the same reason as before", async () => {
    const r = await fold(exploit(boardEvent), STRANGER_SEC);
    expect(r.conf).toEqual({ state: "unknown", why: "grants-withheld" });
    expect(r.gate).toEqual({ cutover: 0, ok: true });
    expect(r.items.has(GRANDFATHERED_ID)).toBe(false);
    expect(r.items.has(GAP_ID)).toBe(false);
  });

  it("a malformed value is not an assertion either", async () => {
    for (const bad of ["", "0", "-1", "1750000100.5", "abc", " 1750000100"]) {
      const def = signNostrEvent(
        {
          created_at: boardEvent.created_at + 1,
          kind: 30301,
          tags: [
            ["d", BOARD_D],
            ["title", BOARD_TITLE],
            ["confidential_since", bad],
          ],
          content: "",
        },
        OWNER_SEC,
      );
      expect(assertedConfidentialSince([def], BOARD_COORD)).toBeNull();
      const r = await fold(exploit(def), STRANGER_SEC);
      expect(r.conf).toEqual({ state: "unknown", why: "grants-withheld" });
    }
  });
});

describe("PROPERTY 2 — an assertion signed by anyone but the board owner is IGNORED", () => {
  // GUARD: the coordinate comparison in assertedConfidentialSince (which IS the
  // author check — a kind-30301's coordinate embeds its author) and the
  // verifyEvent call beside it. Weaken the first to a bare `d`-tag match and the
  // stranger's board is honoured; drop the second and the tampered one is. Each
  // removed separately, each turning this suite RED, each restored.
  it("FIXTURE INVARIANT: the tampered definition carries the tag and does not verify", () => {
    expect(tagValue(TAMPERED, "confidential_since")).toBe(String(CUTOVER));
    expect(TAMPERED.pubkey).toBe(OWNER_PUB);
    // Its id still covers the ORIGINAL tags, so the signature no longer matches.
    expect(assertedConfidentialSince([TAMPERED], BOARD_COORD)).toBeNull();
  });

  it.each([
    ["a stranger's board definition, correctly signed", FOREIGN],
    ["the owner's definition with the tag added after signing", TAMPERED],
  ])("%s asserts nothing", async (_name, def) => {
    // Served ALONGSIDE the real (unasserted) definition, which is how a relay
    // would actually deliver it.
    const r = await fold(exploit(boardEvent, [def]), STRANGER_SEC);
    expect(r.conf).toEqual({ state: "unknown", why: "grants-withheld" });
    expect(r.gate).toEqual({ cutover: 0, ok: true });
    expect(r.items.has(GRANDFATHERED_ID)).toBe(false);
    expect(r.items.has(GAP_ID)).toBe(false);
  });

  it("and cannot pull a HEALTHY board's cutover earlier either", async () => {
    // The other direction: a foreign assertion at the unix epoch would, if
    // believed, quarantine the whole board. Costing visibility is the safe
    // direction, but a stranger must not be able to do it, so it is pinned.
    const r = await fold(healthy(boardEvent).concat(boardDefinition(STRANGER_SEC, 1)), STRANGER_SEC);
    expect(r.gate).toEqual({ cutover: CUTOVER, ok: true });
    expect(r.items.has(GRANDFATHERED_ID)).toBe(true);
  });
});

describe("PROPERTY 3 — a relay OMITTING the assertion yields today's behaviour, not a wider one", () => {
  // GUARD: the `confidentialSince(coord) !== null` condition in
  // confidentialityOf — i.e. the fact that the assertion's short-circuit is
  // conditional at all. Suppress the witnesses whether or not an assertion was
  // found and the omitted arm goes RED: the manufactured cutover is believed and
  // the gap cleartext folds. Verified red with that one change, green with it
  // restored.
  it("dropping the definition event entirely lands on the withheld verdict", async () => {
    const served = await fold(exploit(ASSERTED), STRANGER_SEC);
    expect(served.items.has(GRANDFATHERED_ID)).toBe(true);

    // The relay serves everything EXCEPT the board's own definition.
    const omitted = await fold(exploit(ASSERTED).slice(1), STRANGER_SEC);
    expect(omitted.conf).toEqual({ state: "unknown", why: "grants-withheld" });
    expect(omitted.gate).toEqual({ cutover: 0, ok: true });
    expect(omitted.items.has(GAP_ID)).toBe(false);
    // Omission withholds MORE, never less. That is the whole property.
    expect(omitted.items.has(GRANDFATHERED_ID)).toBe(false);
  });

  it("serving a STALE definition, from before the assertion, does the same", async () => {
    // A relay may hold an older revision of an addressable event and serve that.
    // The older one has no tag, so it asserts nothing and the reader is back on
    // today's path — it cannot be used to replace a newer assertion with silence
    // and get anything wider.
    const r = await fold(exploit(boardEvent), STRANGER_SEC);
    expect(r.conf).toEqual({ state: "unknown", why: "grants-withheld" });
    expect(r.items.has(GRANDFATHERED_ID)).toBe(false);
  });
});

// ---------------------------------------------------------------------------
// THE SAME THING THROUGH THE WHOLE PAGE. Everything above stops at the fold; a
// reader's actual question is what the browser puts on screen, and main.ts wires
// the keyring, the confidentiality decision and the gate together itself.
// ---------------------------------------------------------------------------

/** SEALED_ADMITTED is main.grantsomission.test.ts's measure: the cards a
 * fail-CLOSED gate still admits — every well-formed sealed envelope whose
 * signature verifies. Derived from the shared fixture, never a magic number, so
 * a future fixture addition cannot turn this into a false failure. */
const SEALED_ADMITTED = cards.filter((c) => tagValue(c, "enc") === "1" && c.id !== cardForgedSignature.id);

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

function deps(snapshot: NostrEvent[], secretHex: string): BoardDeps {
  return {
    loadRelays: async () => ["wss://relay.test"],
    fetchEvents: async () => snapshot,
    keyUnwrapper: () => nip07KeyUnwrapper(fakeNip44Signer(secretHex)),
  };
}

const boardFragment: ParsedFragment = { kind: "board", board: BOARD_COORD, relays: ["wss://relay.test"] };
const pageText = () => root.textContent ?? "";
const notice = () => root.querySelector(".confidential-notice")?.textContent ?? "";
const everythingCount = () => root.querySelector('.left-tree .node[data-scope=""] .ct')?.textContent;

describe("through the whole page", () => {
  it("the owner's assertion turns the short board back into the whole board", async () => {
    // WITHOUT it — main.grantsomission.test.ts's measured state, restated here so
    // the pair below is a difference of ONE event.
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(exploit(boardEvent), STRANGER_SEC));
    expect(everythingCount()).toBe(String(SEALED_ADMITTED.length));
    expect(notice()).toContain("CONFIDENTIALITY STATE COULD NOT BE ESTABLISHED");

    // WITH it — the grandfathered card is back, and it is the ONLY one that is.
    document.body.replaceChildren(root);
    root.replaceChildren();
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(exploit(ASSERTED), STRANGER_SEC));
    expect(everythingCount()).toBe(String(SEALED_ADMITTED.length + 1));
    expect(notice()).not.toContain("CONFIDENTIALITY STATE COULD NOT BE ESTABLISHED");
    // And no cleartext the true cutover withholds reaches the DOM.
    expect(pageText()).not.toContain(GAP_PLAINTEXT_TITLE);
    expect(pageText()).not.toContain(GAP_PLAINTEXT_BODY);
    expect(pageText()).not.toContain("SMUGGLED CLEARTEXT TITLE");
  });
});
