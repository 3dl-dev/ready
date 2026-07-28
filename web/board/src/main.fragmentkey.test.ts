// @vitest-environment jsdom
//
// ready-df0 — DOM proof that an `rd board --with-key` link renders REAL TITLES
// with NO browser extension present at all.
//
// The premise this file exists to prove, stated as a fact about the page:
// pasting a read-only npub can NEVER decrypt a confidential board, because the
// CEK is NIP-44-wrapped TO a pubkey and unwrapping it needs that pubkey's
// SECRET. So the owner of the work saw a wall of "[encrypted]" and the only
// sanctioned fix was a NIP-07 extension. Here the extension is not merely
// unused — `keyUnwrapper` is neverUnwraps in EVERY case below, so the page has
// no signer at all — and the titles still render, because the key rode in on
// the URL fragment.
//
// The fixtures are the SAME Go-signed confidential board the ready-c4b suite
// uses (web/board/testdata/genconfidential/main.go): real kind-30301/39301/30302
// events, real NIP-44 wraps, real ChaCha20-Poly1305 envelopes. The fake relay is
// hostile in the same way — it ignores the REQ filter, and serves a card with a
// forged signature and a post-cutover cleartext card. Neither may ever reach the
// DOM, with or without a fragment key.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { afterLogin, main, type BoardDeps, type Identity } from "./main";
import { authTransition } from "./lib/auth";
import { neverUnwraps } from "./lib/keyunwrap";
import { PLACEHOLDER } from "./lib/envelope";
import { hexToBytes } from "./lib/sha256";
import type { NostrEvent } from "./lib/nostrevent";
import type { FragmentKeys, ParsedFragment } from "./lib/fragment";
import {
  BOARD_COORD,
  CEK_EPOCH1,
  CEK_EPOCH2,
  LTK,
  OWNER_PUB,
  STRANGER_PUB,
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

/** NO SIGNER, EVER. This is the whole point: a plain browser, no extension. */
const noExtensionDeps: BoardDeps = {
  loadRelays: async () => ["wss://relay.test"],
  fetchEvents: async () => SNAPSHOT,
  keyUnwrapper: () => neverUnwraps,
};

/** The identity an `rd board --with-key` link opens as: the pubkey from `pk=`,
 * marked read-only because the page holds no signing key. */
function linkIdentity(pubkey: string): Identity {
  return { pubkey, auth: authTransition({ type: "login", method: "readOnly" }) };
}

/** Exactly what cmd/rd/board.go's --with-key fragment carries for this board:
 * BOTH held epochs (the board rotated at conf-003) plus the label-token key. */
function bothEpochKeys(): FragmentKeys {
  return {
    ceks: [
      { epoch: 1, key: hexToBytes(CEK_EPOCH1) },
      { epoch: 2, key: hexToBytes(CEK_EPOCH2) },
    ],
    ltk: hexToBytes(LTK),
  };
}

function keyFragment(keys: FragmentKeys, board = BOARD_COORD): ParsedFragment {
  return { kind: "board", board, relays: ["wss://relay.test"], viewer: OWNER_PUB, keys };
}

function renderedItems(): { id: string; title: string; sealed: boolean }[] {
  return [...root.querySelectorAll(".card[data-id]")].map((card) => ({
    id: card.querySelector(".card-id")?.textContent ?? "",
    title: card.querySelector(".card-title")?.textContent ?? "",
    sealed: card.classList.contains("sealed"),
  }));
}

const pageText = () => root.textContent ?? "";

/** Whatever else a reader sees, the page must never contain raw ciphertext,
 * cleartext smuggled onto the board, or plaintext this reader has no key for. */
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

describe("done condition 1 — a --with-key link decrypts with NO extension", () => {
  it("renders the Go-sealed plaintext titles as TEXT in the DOM", async () => {
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(bothEpochKeys()), noExtensionDeps);

    const byId = new Map(renderedItems().map((i) => [i.id, i]));
    for (const want of expectedPlaintext) {
      expect(byId.get(want.id)?.title, `item ${want.id}`).toBe(want.title);
      expect(byId.get(want.id)?.sealed).toBe(false);
    }
    expect(pageText()).toContain(expectedPlaintext[0].title);
  });

  it("ANTI-TAUTOLOGY: the very same page, same events, same no-extension deps, is a wall of placeholders WITHOUT the fragment key", async () => {
    // This is the "175 rows of [encrypted]" the item was filed about. If this
    // case ever renders plaintext, the test above proves nothing.
    const keyless: ParsedFragment = { kind: "board", board: BOARD_COORD, relays: ["wss://relay.test"] };
    await afterLogin(root, linkIdentity(OWNER_PUB), keyless, noExtensionDeps);

    for (const want of expectedPlaintext) {
      expect(renderedItems().find((i) => i.id === want.id)!.title).toBe(PLACEHOLDER);
    }
    assertNothingLeaked([]);
  });

  it("carries EVERY held epoch, so cards sealed before a rotation open too", async () => {
    // conf-001/002 are epoch 1, conf-003 is epoch 2. A link that shipped only
    // the current epoch would leave the older cards sealed — which is why
    // BoardKeyring.Epochs (pkg/sync/keyepochs.go) exists at all.
    const currentOnly: FragmentKeys = { ceks: [{ epoch: 2, key: hexToBytes(CEK_EPOCH2) }] };
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(currentOnly), noExtensionDeps);

    const byId = new Map(renderedItems().map((i) => [i.id, i]));
    expect(byId.get("conf-001")!.title).toBe(PLACEHOLDER);
    expect(byId.get("conf-003")!.title).toBe(expectedPlaintext[2].title);

    root.replaceChildren();
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(bothEpochKeys()), noExtensionDeps);
    const both = new Map(renderedItems().map((i) => [i.id, i]));
    expect(both.get("conf-001")!.title).toBe(expectedPlaintext[0].title);
    expect(both.get("conf-003")!.title).toBe(expectedPlaintext[2].title);
  });

  it("still tells the reader the board is confidential", async () => {
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(bothEpochKeys()), noExtensionDeps);
    const notice = root.querySelector(".confidential-notice")?.textContent ?? "";
    expect(notice).toContain("confidential");
    expect(notice).toContain("titles were decrypted in your browser");
  });
});

describe("a fragment key does NOT weaken any existing guarantee", () => {
  it("ready-c4b: the forged card and the smuggled post-cutover cleartext still never reach the DOM", async () => {
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(bothEpochKeys()), noExtensionDeps);
    const ids = renderedItems().map((i) => i.id);
    expect(ids).not.toContain("conf-007"); // forged signature, valid ciphertext
    expect(ids).not.toContain("conf-005"); // post-cutover cleartext
    assertNothingLeaked(expectedPlaintext.map((e) => e.title));
  });

  it("ready-c4b: fails CLOSED on a WRONG key — placeholder, never ciphertext, never a partial string", async () => {
    const wrong: FragmentKeys = { ceks: [{ epoch: 1, key: new Uint8Array(32).fill(7) }] };
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(wrong), noExtensionDeps);

    const items = renderedItems();
    expect(items.length).toBeGreaterThan(0);
    for (const want of expectedPlaintext) {
      const got = items.find((i) => i.id === want.id)!;
      expect(got.title).toBe(PLACEHOLDER);
      expect(got.title.trim()).not.toBe("");
    }
    assertNothingLeaked([]);
  });

  it("a link naming a board that does not exist renders nothing, however good its keys are", async () => {
    // NOT the key-scoping proof — that is a keyring-level property and is
    // asserted directly in lib/keyring.test.ts ("applyFragmentKeys"). What this
    // pins is the layer above it: the board a link opens is decided by
    // discoverOwnerBoards over VERIFIED events, so a fragment carrying real keys
    // for a coordinate no signed board event backs renders an empty board rather
    // than borrowing another board's cards to spend the keys on.
    const otherCoord = `30301:${STRANGER_PUB}:someoneelse`;
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(bothEpochKeys(), otherCoord), noExtensionDeps);
    expect(renderedItems()).toEqual([]);
    assertNothingLeaked([]);
  });

  it("a fragment key cannot make a board LOOK confidential, or stop it looking confidential", async () => {
    // cutover comes ONLY from owner-signed grants (keyring.cutover), never from
    // the link — that is what keeps the fold gate quarantining post-cutover
    // cleartext for a reader who holds nothing.
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(bothEpochKeys()), noExtensionDeps);
    expect(renderedItems().map((i) => i.id)).not.toContain("conf-005");
  });

  it("persists NOTHING: no CEK, LTK or key material reaches localStorage or sessionStorage", async () => {
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(bothEpochKeys()), noExtensionDeps);
    expect(pageText()).toContain(expectedPlaintext[0].title); // not vacuous
    expect(localStorage.length).toBe(0);
    expect(sessionStorage.length).toBe(0);
  });
});

// done condition 2 + the "no paste" half of the outcome, at the main() entry
// point: a --with-key link names its own viewer pubkey, so the page must open
// the board directly instead of showing a login form, and the fragment must be
// gone from the URL by the time anything renders.
describe("main() — a --with-key link opens the board with nothing to paste", () => {
  it("skips the login form entirely and strips the key-bearing fragment", () => {
    document.body.innerHTML = '<div id="app"></div>';
    const replaceState = vi.fn();
    const origHistory = window.history;
    Object.defineProperty(window, "history", { value: { replaceState }, configurable: true, writable: true });
    window.location.hash = `#board=${encodeURIComponent(BOARD_COORD)}&pk=${OWNER_PUB}&cek=1:${CEK_EPOCH1}`;

    try {
      expect(() => main()).not.toThrow();

      const app = document.getElementById("app")!;
      // Straight onto the board path — no npub form, no extension button.
      expect(app.className).toBe("board-page");
      expect(app.querySelector('input[type="text"]')).toBeNull();
      expect(app.querySelector("button")).toBeNull();
      // ready-dbf #6 / ready-62d1: the key is removed from the address bar and
      // from history before the reader can copy it out of there.
      expect(replaceState).toHaveBeenCalledWith(null, "", window.location.pathname + window.location.search);
    } finally {
      Object.defineProperty(window, "history", { value: origHistory, configurable: true, writable: true });
      window.location.hash = "";
    }
  });
});
