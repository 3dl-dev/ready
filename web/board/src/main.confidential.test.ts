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
  MEMBER_PUB,
  MEMBER_SEC,
  OWNER_PUB,
  REVOKED_PUB,
  REVOKED_SEC,
  STRANGER_PUB,
  STRANGER_SEC,
  boardEvent,
  cards,
  expectedPlaintext,
  grants,
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

function renderedItems(): { id: string; title: string; sealed: boolean }[] {
  return [...root.querySelectorAll("li.item")].map((li) => ({
    id: li.querySelector(".item-id")?.textContent ?? "",
    title: li.querySelector(".item-title")?.textContent ?? "",
    sealed: li.classList.contains("sealed"),
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
    for (const el of root.querySelectorAll(".item-title")) {
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
  it("renders a grandfathered pre-cutover plaintext card normally", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    const byId = new Map(renderedItems().map((i) => [i.id, i]));
    expect(byId.get("conf-006")!.title).toBe("Legacy plaintext card");
    expect(byId.get("conf-006")!.sealed).toBe(false);
  });

  it("still renders the board header the discovery path produces", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), boardFragment, deps(MEMBER_SEC));
    expect(pageText()).toContain("Confidential Board");
    expect(pageText()).toContain(BOARD_COORD);
    expect(pageText()).toContain(OWNER_PUB.slice(0, 8));
  });
});
