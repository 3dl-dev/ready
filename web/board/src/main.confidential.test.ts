// @vitest-environment jsdom
//
// DOM-level proof of ready-c4b: confidential titles render in the browser, and
// fail closed when they can't.
//
// This is the layer the item's outcome is written at — "what does a person
// looking at ready.3dl.dev/board actually see" — so the assertions are about
// rendered text, not return values. Everything under the DOM is real: real
// Go-signed events, real signature verification, the real fold gate, the real
// grant -> NIP-07 -> CEK derivation, the real ChaCha20-Poly1305 open. The only
// fakes are the relay transport and the browser extension, and the extension
// fake is a spec-validated NIP-44 v2 implementation (nip44ref.test.ts).
//
// THE FAKE RELAY IS HOSTILE. It ignores the REQ filter and serves everything it
// has, including a card with a forged signature whose ciphertext is perfectly
// valid, and a post-cutover plaintext card carrying a readable title. A relay is
// free to do both. Client-side verification and the fold gate, not relay
// courtesy, are what keep them off the screen.

import { beforeEach, describe, expect, it } from "vitest";
import { afterLogin, type BoardDeps, type Identity } from "./main";
import { authTransition } from "./lib/auth";
import { fakeNip44Signer } from "./lib/fakesigner";
import { nip07KeyUnwrapper, neverUnwraps } from "./lib/keyunwrap";
import { PLACEHOLDER } from "./lib/envelope";
import type { NostrEvent } from "./lib/nostrevent";
import type { ParsedFragment } from "./lib/fragment";
import {
  BOARD_COORD,
  EPOCH3_CARD_CONTEXT,
  EPOCH3_CARD_ID,
  EPOCH3_CARD_TITLE,
  MEMBER_PUB,
  MEMBER_SEC,
  OWNER_PUB,
  REVOKED_PUB,
  REVOKED_SEC,
  STRANGER_PUB,
  STRANGER_SEC,
  OWNER_SEC,
  boardEvent,
  cardSealedUnderEpoch3,
  cards,
  expectedPlaintext,
  grants,
  legacyRawWrapGrant,
  legacyRawWrapGrantOwner,
  rewrappedHexGrant,
  rewrappedHexGrantOwner,
} from "./lib/confidential.fixtures";

const SNAPSHOT: NostrEvent[] = [boardEvent, ...grants, ...cards];

let root: HTMLElement;

beforeEach(() => {
  document.body.replaceChildren();
  root = document.createElement("div");
  root.id = "app";
  document.body.append(root);
  localStorage.clear();
  sessionStorage.clear();
});

function signingIdentity(pubkey: string): Identity {
  return { pubkey, auth: authTransition({ type: "login", method: "extension" }) };
}

function readOnlyIdentity(pubkey: string): Identity {
  return { pubkey, auth: authTransition({ type: "login", method: "readOnly" }) };
}

/** deps serves the WHOLE snapshot for every REQ — a hostile relay honours no
 * filter — and wires the signer for `secretHex`, or none at all when null. */
function deps(secretHex: string | null): BoardDeps {
  return {
    loadRelays: async () => ["wss://relay.test"],
    fetchEvents: async () => SNAPSHOT,
    keyUnwrapper: () => (secretHex === null ? neverUnwraps : nip07KeyUnwrapper(fakeNip44Signer(secretHex))),
  };
}

const boardFragment: ParsedFragment = { kind: "board", board: BOARD_COORD, relays: ["wss://relay.test"] };

/**
 * ready-56b: the confidential read path now renders through the board
 * workspace, not through c4b's standalone `<ul class="item-list">`. Cards are
 * the rows; a card whose free text could not be opened carries `.sealed` and
 * shows PLACEHOLDER in its `.card-title`. Every assertion below is unchanged in
 * substance — it is still "what text does a person see" — only the selectors
 * moved with the markup.
 */
function renderedItems(): { id: string; title: string; sealed: boolean }[] {
  return [...root.querySelectorAll(".card[data-id]")].map((card) => ({
    id: card.querySelector(".card-id")?.textContent ?? "",
    title: card.querySelector(".card-title")?.textContent ?? "",
    sealed: card.classList.contains("sealed"),
  }));
}

const pageText = () => root.textContent ?? "";

/** assertNothingLeaked is run in every case. Whatever else a reader sees, the
 * page must never contain raw ciphertext, cleartext smuggled onto the board, or
 * plaintext this reader has no key for. */
function assertNothingLeaked(allowedTitles: string[]): void {
  const text = pageText();
  for (const e of expectedPlaintext) {
    if (allowedTitles.includes(e.title)) continue;
    expect(text, `leaked title: ${e.title}`).not.toContain(e.title);
    expect(text, `leaked context: ${e.context}`).not.toContain(e.context);
  }
  for (const c of cards) {
    if (c.tags.some((t) => t[0] === "enc") && c.content !== "") {
      expect(text).not.toContain(c.content);
    }
  }
  expect(text).not.toContain("SMUGGLED CLEARTEXT");
  expect(text).not.toContain("Forged card");
}

describe("a granted member sees decrypted titles in the page", () => {
  it("renders the plaintext the Go writer sealed, as TEXT in the DOM", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));

    const items = renderedItems();
    const byId = new Map(items.map((i) => [i.id, i]));
    for (const want of expectedPlaintext) {
      expect(byId.get(want.id)?.title, `item ${want.id}`).toBe(want.title);
      expect(byId.get(want.id)?.sealed).toBe(false);
    }
    // The whole point of the item, asserted at the layer a person experiences:
    // the confidential title is on the screen.
    expect(pageText()).toContain(expectedPlaintext[0].title);
  });

  it("tells the reader the board is confidential and that its key holds the grant", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    const notice = root.querySelector(".confidential-notice")?.textContent ?? "";
    expect(notice).toContain("confidential");
    expect(notice).toContain("titles were decrypted in your browser");
  });

  it("shows the placeholder for the one item sealed to an epoch nobody holds", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    const byId = new Map(renderedItems().map((i) => [i.id, i]));
    expect(byId.get("conf-004")!.title).toBe(PLACEHOLDER);
    expect(byId.get("conf-004")!.sealed).toBe(true);
    // ...while its siblings still render. A per-board bail-out would be a much
    // easier implementation and a much worse product.
    expect(byId.get("conf-001")!.title).toBe(expectedPlaintext[0].title);
  });

  // ready-02e: conf-008 and conf-009 are sealed under epoch 1, which MEMBER_SEC
  // holds — the AEAD open genuinely succeeds (real CEK, real tag). But the
  // opened plaintext is not a {title: string, ...} card payload: conf-008
  // opens to a JSON array, conf-009 opens to an object with no title field at
  // all. A decryption that "succeeds" at the AEAD layer but does not yield a
  // usable card must still render the placeholder AND carry .sealed — not a
  // blank title with no visual signal that something was withheld. Asserted
  // through the real render path (afterLogin -> fold -> mountBoardWorkspace),
  // per the adversary's note that a DOM-assignment stand-in is weak evidence.
  it("treats a sealed payload that opens to a non-object as a decryption FAILURE, not a blank title", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    const byId = new Map(renderedItems().map((i) => [i.id, i]));
    const got = byId.get("conf-008")!;
    expect(got.title).toBe(PLACEHOLDER);
    expect(got.title.trim()).not.toBe("");
    expect(got.sealed).toBe(true);
  });

  it("treats a sealed payload with no string title as a decryption FAILURE, not a blank title", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    const byId = new Map(renderedItems().map((i) => [i.id, i]));
    const got = byId.get("conf-009")!;
    expect(got.title).toBe(PLACEHOLDER);
    expect(got.title.trim()).not.toBe("");
    expect(got.sealed).toBe(true);
  });

  it("never lets the forged card or the smuggled cleartext card reach the DOM", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    const ids = renderedItems().map((i) => i.id);
    expect(ids).not.toContain("conf-007"); // forged signature, valid ciphertext
    expect(ids).not.toContain("conf-005"); // post-cutover cleartext
    assertNothingLeaked(expectedPlaintext.map((e) => e.title));
  });

  it("renders titles as TEXT, so a decrypted title cannot become markup", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    // Decryption produces attacker-influenceable strings (anyone with write
    // access to the board authors them). They go through textContent, never
    // innerHTML — so no element ever comes from a title.
    for (const el of root.querySelectorAll(".card-title")) {
      expect(el.children.length).toBe(0);
    }
  });
});

describe("FAIL CLOSED in the page (done condition 2)", () => {
  it("2a — a logged-in key with NO grant sees placeholders, not blanks and not ciphertext", async () => {
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(STRANGER_SEC));

    const items = renderedItems();
    expect(items.length).toBeGreaterThan(0);
    for (const want of expectedPlaintext) {
      const got = items.find((i) => i.id === want.id)!;
      expect(got.title).toBe(PLACEHOLDER);
      expect(got.title.trim()).not.toBe("");
      expect(got.sealed).toBe(true);
    }
    assertNothingLeaked([]);
  });

  it("2a — tells that reader WHY, and what to do about it", async () => {
    await afterLogin(root, signingIdentity(STRANGER_PUB), boardFragment, deps(STRANGER_SEC));
    const notice = root.querySelector(".confidential-notice")?.textContent ?? "";
    expect(notice).toContain("sealed to a key you do not hold");
    expect(notice).toContain(PLACEHOLDER);
    expect(notice).toContain("grant this key access");
  });

  it("2b — a grant for a DIFFERENT epoch than the card renders the placeholder for that card only", async () => {
    // The revoked member holds epoch 1 and not epoch 2. conf-003 was sealed
    // under epoch 2; conf-001 and conf-002 under epoch 1.
    await afterLogin(root, signingIdentity(REVOKED_PUB), boardFragment, deps(REVOKED_SEC));
    const byId = new Map(renderedItems().map((i) => [i.id, i]));
    expect(byId.get("conf-001")!.title).toBe(expectedPlaintext[0].title);
    expect(byId.get("conf-002")!.title).toBe(expectedPlaintext[1].title);
    expect(byId.get("conf-003")!.title).toBe(PLACEHOLDER);
    assertNothingLeaked([expectedPlaintext[0].title, expectedPlaintext[1].title]);
  });

  it("a read-only npub login gets a placeholder wall, not an error page", async () => {
    // Someone opens a share link with no extension at all. They should still
    // see the board's SHAPE — ids, statuses — and be told plainly why the text
    // is not there.
    await afterLogin(root, readOnlyIdentity(MEMBER_PUB), boardFragment, deps(null));
    const items = renderedItems();
    expect(items.length).toBeGreaterThan(0);
    for (const want of expectedPlaintext) {
      expect(items.find((i) => i.id === want.id)!.title).toBe(PLACEHOLDER);
    }
    assertNothingLeaked([]);
  });

  it("a read-only view of SOMEONE ELSE'S npub does not borrow the extension's keys", async () => {
    // The hazard the keyUnwrapper seam exists for: an extension is installed and
    // holds the member key, but the visitor typed a different npub into the
    // read-only form. Decrypting that board would be decrypting for a pubkey
    // that did not authenticate. defaultDeps gates on canSign; this pins the
    // behaviour with the production predicate in play.
    const productionShapedDeps: BoardDeps = {
      loadRelays: async () => ["wss://relay.test"],
      fetchEvents: async () => SNAPSHOT,
      keyUnwrapper: (identity) =>
        identity.auth.readOnly ? neverUnwraps : nip07KeyUnwrapper(fakeNip44Signer(MEMBER_SEC)),
    };
    await afterLogin(root, readOnlyIdentity(MEMBER_PUB), boardFragment, productionShapedDeps);
    for (const want of expectedPlaintext) {
      expect(renderedItems().find((i) => i.id === want.id)!.title).toBe(PLACEHOLDER);
    }
    assertNothingLeaked([]);
  });
});

describe("no secret material is persisted (done condition 6)", () => {
  const SECRETS = [MEMBER_SEC, STRANGER_SEC];

  function dumpStorage(s: Storage): string {
    const out: string[] = [];
    for (let i = 0; i < s.length; i++) {
      const k = s.key(i)!;
      out.push(k, s.getItem(k) ?? "");
    }
    return out.join("\n");
  }

  it("writes NOTHING to localStorage or sessionStorage on a full confidential render", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));

    // Not vacuous: the render really did decrypt.
    expect(pageText()).toContain(expectedPlaintext[0].title);

    // The strongest available statement, and the one this app can honestly
    // make: the page persists nothing at all. There is no cache to scope per
    // pubkey because there is no cache — the keyring is rebuilt from signed
    // relay events on every load. That costs one extension prompt per load and
    // buys "a stolen browser profile contains no board keys", which is the
    // right trade for a confidential board.
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });

  it("no CEK, LTK or secret key appears in any persisted blob", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    const persisted = dumpStorage(localStorage) + "\n" + dumpStorage(sessionStorage);
    const { CEK_EPOCH1, CEK_EPOCH2, LTK } = await import("./lib/confidential.fixtures");
    for (const secret of [CEK_EPOCH1, CEK_EPOCH2, LTK, ...SECRETS]) {
      expect(persisted).not.toContain(secret);
      // ...in either case, since hex casing is not load-bearing anywhere.
      expect(persisted.toLowerCase()).not.toContain(secret.toLowerCase());
    }
  });

  // The STRUCTURAL half of done condition 6 — "no shipped module may even
  // reference a persistence API" — lives in nostorage.test.ts, which runs in
  // the node environment where import.meta.url is a real file: URL.
});

describe("the confidential path does not disturb a plaintext board", () => {
  it("admits a grandfathered pre-cutover plaintext card, and still drops the smuggled one", async () => {
    // conf-006 was authored BEFORE the board went confidential and carries a
    // clear title; the fold gate must grandfather it. conf-005 is post-cutover
    // cleartext — the same shape, on the wrong side of the cutover — and must
    // be quarantined. The two together are the gate, and asserting only the
    // second would pass on a fold that quarantines everything.
    //
    // WHY THIS IS A COUNT AND NOT A TITLE (ready-56b): conf-006's status is
    // "done", and a terminal item has no column in the three-column design
    // (board/views.ts columnize) — so it is admitted to the board's item set
    // without appearing on a card. The tree's "Everything" node counts the
    // whole admitted set, which is exactly the fold-gate outcome under test.
    // The byte-level "its plaintext survives unchanged" proof is in
    // fold.vectors.test.ts and carditems.test.ts.
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));

    const everything = root.querySelector('.left-tree .node[data-scope=""] .ct')?.textContent;
    // conf-001..004 + conf-006 + conf-008 + conf-009 admitted (ready-02e's
    // conf-008/009 are well-formed confidential events — the fold gate admits
    // them; it is decryptCardPayload's SHAPE check, one layer deeper, that
    // fails them to the placeholder); conf-005 (post-cutover cleartext) and
    // conf-007 (forged signature) dropped.
    expect(everything).toBe("7");

    const ids = renderedItems().map((i) => i.id).sort();
    expect(ids).toEqual(["conf-001", "conf-002", "conf-003", "conf-004", "conf-008", "conf-009"]);
    expect(pageText()).not.toContain("SMUGGLED CLEARTEXT");
  });

  it("still names the board the discovery path produced — by its title, never by its coordinate", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    // The board's signed `title` tag is what a person reads.
    expect(pageText()).toContain("Confidential Board");
    // ready-56b: the coordinate is still in the DOM — it is how the page records
    // WHICH verified board this is, and main.test.ts asserts every item fetch is
    // scoped to one — but as an attribute, not as 64 rendered hex characters.
    const node = root.querySelector(`.left-tree .node[data-board-coord="${BOARD_COORD}"]`);
    expect(node).not.toBeNull();
    expect(node?.querySelector(".nm")?.textContent).toBe("Confidential Board");
    expect(pageText()).not.toContain(BOARD_COORD);
    expect(pageText()).not.toContain(OWNER_PUB);
  });
});

// ready-470 — THE ITEM'S DONE CONDITION, at the layer it was written at.
//
// "Verify by logging into the board page with the owner key and seeing real
// titles." A derived CEK is one layer below that: a keyring can hold the right
// bytes while the page still shows nothing. So these cases run the whole page —
// afterLogin -> signature verification -> fold gate -> keyring -> the
// ChaCha20-Poly1305 open -> mountBoardWorkspace — and assert on the TEXT of a
// `.card-title`.
//
// The two snapshots differ in ONE event: the epoch-3 grant is either the
// pre-ready-c4b raw-byte wrap or the hex re-wrap `rd confidential rewrap`
// publishes over it — same slot, same key value, one second later. Everything else
// (board, other grants, the epoch-3 card) is byte-identical, so any difference on
// the screen is caused by the wrap encoding and nothing else.
describe("ready-470: the re-wrap is what puts a real title on the screen", () => {
  function depsWith(snapshot: NostrEvent[], secretHex: string): BoardDeps {
    return {
      loadRelays: async () => ["wss://relay.test"],
      fetchEvents: async () => snapshot,
      keyUnwrapper: () => nip07KeyUnwrapper(fakeNip44Signer(secretHex)),
    };
  }

  const withGrant = (g: NostrEvent): NostrEvent[] => [boardEvent, ...grants, ...cards, g, cardSealedUnderEpoch3];

  async function renderAs(pubkey: string, secretHex: string, epoch3Grant: NostrEvent) {
    await afterLogin(root, signingIdentity(pubkey), boardFragment, depsWith(withGrant(epoch3Grant), secretHex));
    return new Map(renderedItems().map((i) => [i.id, i]));
  }

  // Each reader is exercised twice: once against the legacy grant it cannot use,
  // once against the repair. The OWNER row is the item's literal done condition —
  // the ready board's own self-grant was a raw-payload one, which is why the page
  // showed [encrypted] to the person who owns it. The MEMBER row proves the repair
  // is not an owner-only special case.
  const readers: { who: string; pub: string; sec: string; legacy: NostrEvent; repaired: NostrEvent }[] = [
    { who: "OWNER", pub: OWNER_PUB, sec: OWNER_SEC, legacy: legacyRawWrapGrantOwner, repaired: rewrappedHexGrantOwner },
    { who: "MEMBER", pub: MEMBER_PUB, sec: MEMBER_SEC, legacy: legacyRawWrapGrant, repaired: rewrappedHexGrant },
  ];

  for (const r of readers) {
    it(`${r.who}: the LEGACY raw wrap leaves the placeholder on the screen`, async () => {
      const byId = await renderAs(r.pub, r.sec, r.legacy);
      const got = byId.get(EPOCH3_CARD_ID)!;
      expect(got.title).toBe(PLACEHOLDER);
      expect(got.sealed).toBe(true);
      // Nothing of the sealed card's text reached the page by any other route.
      expect(pageText()).not.toContain(EPOCH3_CARD_TITLE);
      expect(pageText()).not.toContain(EPOCH3_CARD_CONTEXT);
      // NOT a board-wide bail-out: this reader's other cards still render, so the
      // repaired case below cannot pass merely by un-breaking the whole page.
      expect(byId.get("conf-001")!.title).toBe(expectedPlaintext[0].title);
    });

    it(`${r.who}: the RE-WRAPPED grant renders the real title`, async () => {
      const byId = await renderAs(r.pub, r.sec, r.repaired);
      const got = byId.get(EPOCH3_CARD_ID)!;
      expect(got.title).toBe(EPOCH3_CARD_TITLE);
      expect(got.sealed).toBe(false);
      // The plain-language version of the done condition: the words are on the page.
      expect(pageText()).toContain(EPOCH3_CARD_TITLE);
      // ...and the repair cost the reader none of the history it already held. A
      // ROTATION would also have produced a readable title here — at a new epoch
      // whose key opens none of these.
      for (const want of expectedPlaintext) {
        expect(byId.get(want.id)?.title, `item ${want.id}`).toBe(want.title);
      }
    });
  }

  it("the repair is the ONLY difference between the two pages", async () => {
    // Same reader, same everything but the one grant: every other card renders
    // identically, so the epoch-3 title flipping from placeholder to plaintext is
    // attributable to the wrap encoding alone.
    const before = await renderAs(OWNER_PUB, OWNER_SEC, legacyRawWrapGrantOwner);
    document.body.replaceChildren();
    root = document.createElement("div");
    root.id = "app";
    document.body.append(root);
    const after = await renderAs(OWNER_PUB, OWNER_SEC, rewrappedHexGrantOwner);

    expect([...after.keys()].sort()).toEqual([...before.keys()].sort());
    for (const [id, b] of before) {
      if (id === EPOCH3_CARD_ID) continue;
      expect(after.get(id), `item ${id}`).toEqual(b);
    }
  });
});
