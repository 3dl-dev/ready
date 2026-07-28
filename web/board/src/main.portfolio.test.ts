// @vitest-environment jsdom
//
// ready-4d9 — DOM proof that ONE link opens the WHOLE portfolio: several boards,
// several owners, real titles, no browser extension anywhere.
//
// WHY THIS FILE EXISTS ALONGSIDE main.fragmentkey.test.ts. That file proves a
// single-board key link renders titles. Everything this item added is a
// statement about SEVERAL boards at once, and none of it can be witnessed on a
// one-board fixture:
//
//   * boards from MORE THAN ONE OWNER are discovered, because the page queries
//     every owner named in the link's key material and not just the viewer;
//   * a board the link has no key for stays SEALED while its neighbours open;
//   * a key filed under board A is never offered to board B — the check that was
//     unreachable in production until this item, and is now exercised on every
//     load;
//   * a truncated link still renders a usable page and still strips the fragment.
//
// The fixture is Go-signed throughout (web/board/testdata/genportfolio): four
// boards, five owner grants, eight cards, real NIP-44 wraps, real
// ChaCha20-Poly1305 envelopes. Read that generator's header for the scenario —
// in particular for why gamma's cards are sealed under ALPHA's epoch-1 key,
// which is what makes a cross-board key leak VISIBLE here rather than
// indistinguishable from a failed AEAD.

import { beforeEach, describe, expect, it, vi } from "vitest";
import { afterLogin, loadBoardItems, main, type BoardDeps, type Identity } from "./main";
import { authTransition, canSign } from "./lib/auth";
import { neverUnwraps } from "./lib/keyunwrap";
import { PLACEHOLDER } from "./lib/envelope";
import { hexToBytes } from "./lib/sha256";
import type { FragmentKeys, ParsedFragment } from "./lib/fragment";
import type { PortfolioKeys } from "./lib/portfoliokeys";
import type { DiscoveredBoard } from "./lib/boarddiscovery";
import {
  ALPHA_CEK_EPOCH1,
  ALPHA_CEK_EPOCH2,
  ALPHA_COORD,
  BETA_CEK,
  BETA_COORD,
  DELTA_CEK,
  DELTA_COORD,
  GAMMA_COORD,
  OTHER_OWNER_PUB,
  PORTFOLIO_KEYS_BLOB,
  VIEWER_PUB,
  expectedPlaintext,
  forbiddenText,
  snapshot,
} from "./lib/portfolio.fixtures";

let root: HTMLElement;

beforeEach(() => {
  document.body.replaceChildren();
  root = document.createElement("div");
  root.id = "app";
  document.body.append(root);
  localStorage.clear();
  sessionStorage.clear();
});

/** NO SIGNER, EVER — a plain browser with no extension. Every title that renders
 * below was decrypted with a key that arrived in the URL and nothing else. */
const noExtensionDeps: BoardDeps = {
  loadRelays: async () => ["wss://relay.test"],
  fetchEvents: async () => snapshot,
  keyUnwrapper: () => neverUnwraps,
};

const cek = (hex: string, epoch: number) => ({ epoch, key: hexToBytes(hex) });

/** What `rd board --portfolio --with-key` gathers for this viewer: alpha (both
 * epochs — it rotated), beta, and delta (owned by someone else, granted to us).
 * NOT gamma: the viewer holds no key for it, so the CLI could not embed one. */
function portfolioKeys(): PortfolioKeys {
  return new Map<string, FragmentKeys>([
    [ALPHA_COORD, { ceks: [cek(ALPHA_CEK_EPOCH1, 1), cek(ALPHA_CEK_EPOCH2, 2)] }],
    [BETA_COORD, { ceks: [cek(BETA_CEK, 1)] }],
    [DELTA_COORD, { ceks: [cek(DELTA_CEK, 1)] }],
  ]);
}

function portfolioFragment(keys?: PortfolioKeys): ParsedFragment {
  return { kind: "portfolio", relays: ["wss://relay.test"], viewer: VIEWER_PUB, keys };
}

function linkIdentity(pubkey = VIEWER_PUB): Identity {
  return { pubkey, auth: authTransition({ type: "login", method: "readOnly" }) };
}

function renderedItems(): { id: string; title: string; sealed: boolean }[] {
  return [...root.querySelectorAll(".card[data-id]")].map((card) => ({
    id: card.querySelector(".card-id")?.textContent ?? "",
    title: card.querySelector(".card-title")?.textContent ?? "",
    sealed: card.classList.contains("sealed"),
  }));
}

const pageText = () => root.textContent ?? "";

/** loginFormShown asks the DOM, not the class name on #app.
 *
 * renderLogin sets root.className = "login-page" — on the ROOT ITSELF — so
 * `root.querySelector(".login-page")` is null whether or not the form rendered,
 * and asserting on it proves nothing in either direction. The login form's own
 * <section class="login"> is a real child element, so this is the question
 * actually being asked: is the visitor being asked to log in? */
const loginFormShown = () => root.querySelector("section.login") !== null;

/** Whatever else renders, gamma's sealed content and its smuggled post-cutover
 * cleartext must never reach the DOM. */
function assertGammaStaysSealed(): void {
  for (const text of forbiddenText) {
    expect(pageText(), `leaked: ${text}`).not.toContain(text);
  }
}

// ---------------------------------------------------------------------------
// THE ITEM: one link, several boards, plaintext titles.
// ---------------------------------------------------------------------------
describe("one portfolio link renders MULTIPLE boards' titles in plaintext", () => {
  it("opens every board the link carries a key for, across two owners", async () => {
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), noExtensionDeps);

    const byId = new Map(renderedItems().map((i) => [i.id, i]));
    // Not one board's items — items from THREE boards, two of which the viewer
    // owns and one of which it does not.
    const coords = new Set(expectedPlaintext.map((e) => e.coord));
    expect(coords.size).toBe(3);

    for (const want of expectedPlaintext) {
      const got = byId.get(want.id);
      expect(got, `item ${want.id} (${want.coord}) did not render at all`).toBeDefined();
      expect(got!.title, `item ${want.id} on ${want.coord}`).toBe(want.title);
      expect(got!.sealed).toBe(false);
    }
    assertGammaStaysSealed();
  });

  it("the left tree lists ALL FOUR boards, including the one that stayed sealed", async () => {
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), noExtensionDeps);
    const coords = [...root.querySelectorAll("[data-board-coord]")].map((n) => n.getAttribute("data-board-coord"));
    for (const want of [ALPHA_COORD, BETA_COORD, GAMMA_COORD, DELTA_COORD]) {
      expect(coords, `board ${want} missing from the tree`).toContain(want);
    }
  });

  it("a board the link has NO key for fails closed: placeholders, not plaintext", async () => {
    // Done condition 2. gamma is discovered and known to be confidential (its
    // owner grant sets the cutover), but its key was never in the link.
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), noExtensionDeps);

    const gammaSealed = renderedItems().filter((i) => i.id === "gamma-001");
    expect(gammaSealed.length, "gamma's sealed card did not render at all").toBe(1);
    expect(gammaSealed[0].title).toBe(PLACEHOLDER);
    expect(gammaSealed[0].sealed).toBe(true);
    // Its genuine PRE-cutover plaintext card still renders — so "gamma is
    // sealed" is about the key, not about the board being invisible.
    expect(pageText()).toContain("Gamma legacy plaintext");
    assertGammaStaysSealed();
  });

  it("the notice counts BOARDS, not one board, and says how much was decrypted", async () => {
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), noExtensionDeps);
    const notice = root.querySelector(".notice")?.textContent ?? pageText();
    expect(notice).toMatch(/\d+ boards in this view are confidential/);
    expect(notice).toContain("titles were decrypted in your browser");
    expect(notice).toContain(PLACEHOLDER);
  });

  it("names the boards it holds keys for that the relay never published", async () => {
    // Done condition 2's second half, and a defect the first LIVE run found: the
    // real `rd board --portfolio --with-key` emitted keys for 15 boards while the
    // relay served kind-30301 definitions for 10, so five boards silently did not
    // appear — indistinguishable from "those boards have no work". A board with
    // no published definition is ABSENT, not empty, and the page has to say which.
    const withGhost: PortfolioKeys = new Map([
      ...portfolioKeys(),
      [`30301:${VIEWER_PUB}:never-published`, { ceks: [cek(BETA_CEK, 1)] }],
    ]);
    await afterLogin(root, linkIdentity(), portfolioFragment(withGhost), noExtensionDeps);

    const text = pageText();
    expect(text).toContain("never-published");
    expect(text).toContain("publishing gap, not a permission problem");
    // And it does NOT claim the ghost board is sealed or unauthorized — the two
    // situations the notice exists to keep apart.
    expect(text).not.toMatch(/never-published[^.]*key you do not hold/);
  });

  it("says nothing about unserved boards when every key matches a real board", async () => {
    // The notice must not grow a paragraph for the ordinary case.
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), noExtensionDeps);
    expect(pageText()).not.toContain("publishing gap");
  });

  it("ANTI-TAUTOLOGY: the same fixture with NO keys renders no plaintext title at all", async () => {
    // Without this, every assertion above could pass on a build where the fold
    // simply rendered cleartext it should not have.
    await afterLogin(root, linkIdentity(), portfolioFragment(undefined), noExtensionDeps);
    for (const want of expectedPlaintext) {
      expect(pageText(), `title ${want.title} rendered with no key present`).not.toContain(want.title);
    }
    // The boards are still there; only their titles are sealed.
    expect(renderedItems().filter((i) => i.sealed).length).toBeGreaterThan(0);
  });
});

// ---------------------------------------------------------------------------
// PER-BOARD KEY SCOPE, at portfolio scale.
//
// main.fragmentkey.test.ts witnesses this for a single-board link placed in a
// multi-board list. Here the map itself is genuinely multi-board, which is the
// production shape from this item forward. The trap that makes it meaningful:
// gamma's cards are sealed under ALPHA's epoch-1 key (see the fixture
// generator), so a page that offered alpha's key to gamma would DECRYPT GAMMA
// AND RENDER ITS TITLES — a visible leak, not a silent AEAD failure.
// ---------------------------------------------------------------------------
describe("PER-BOARD KEY SCOPE — a portfolio link's keys never cross boards", () => {
  const boards: DiscoveredBoard[] = [
    { coord: ALPHA_COORD, ownerPubkey: VIEWER_PUB, boardD: "alpha", title: "Alpha Board" },
    { coord: BETA_COORD, ownerPubkey: VIEWER_PUB, boardD: "beta", title: "Beta Board" },
    { coord: GAMMA_COORD, ownerPubkey: VIEWER_PUB, boardD: "gamma", title: "Gamma Board" },
    { coord: DELTA_COORD, ownerPubkey: OTHER_OWNER_PUB, boardD: "delta", title: "Delta Board" },
  ];

  async function load(keys: PortfolioKeys | undefined, order = boards) {
    return loadBoardItems(order, ["wss://relay.test"], snapshot, linkIdentity(), noExtensionDeps, () => {}, keys);
  }

  const titles = (items: { id: string; title: string }[]) => new Map(items.map((i) => [i.id, i.title]));

  it("alpha's key opens alpha and leaves gamma sealed, though the SAME key bytes would open gamma", async () => {
    const { items } = await load(portfolioKeys());
    const byId = titles(items);

    // The positive half: alpha really did open with that key.
    expect(byId.get("alpha-001")).toBe("Alpha epoch one card");
    // The scoping half: gamma is sealed under those exact bytes and stayed sealed.
    expect(byId.get("gamma-001")).toBe(PLACEHOLDER);
    expect(JSON.stringify(items)).not.toContain("GAMMA SECRET");
  });

  it("ANTI-TAUTOLOGY: file the very same key under GAMMA's coordinate and gamma opens", async () => {
    // Proves the previous case is the coordinate check refusing, not the key
    // being wrong. Same bytes, different coordinate, different outcome.
    const misfiled: PortfolioKeys = new Map([[GAMMA_COORD, { ceks: [cek(ALPHA_CEK_EPOCH1, 1)] }]]);
    const { items } = await load(misfiled);
    expect(titles(items).get("gamma-001")).toBe("GAMMA SECRET TITLE");
    // And with gamma's key filed under gamma, ALPHA is the one that stays shut.
    expect(titles(items).get("alpha-001")).toBe(PLACEHOLDER);
  });

  it("the scope holds whichever order the boards come back in", async () => {
    const reversed = [...boards].reverse();
    const { items } = await load(portfolioKeys(), reversed);
    expect(titles(items).get("gamma-001")).toBe(PLACEHOLDER);
    expect(titles(items).get("alpha-001")).toBe("Alpha epoch one card");
  });

  it("a key for a board that is not in the list opens nothing", async () => {
    const stray: PortfolioKeys = new Map([
      [`30301:${VIEWER_PUB}:not-a-real-board`, { ceks: [cek(ALPHA_CEK_EPOCH1, 1)] }],
    ]);
    const { items } = await load(stray);
    for (const want of expectedPlaintext) {
      expect(titles(items).get(want.id) ?? PLACEHOLDER).toBe(PLACEHOLDER);
    }
  });

  it("EVERY held epoch travels: alpha's pre-rotation card opens too", async () => {
    // A link that shipped only current epochs would leave alpha-001 sealed while
    // alpha-002 rendered — the rotation bug, at portfolio scale.
    const { items } = await load(portfolioKeys());
    expect(titles(items).get("alpha-001")).toBe("Alpha epoch one card");
    expect(titles(items).get("alpha-002")).toBe("Alpha after the rotation");
  });

  it("only-current-epoch is genuinely different: drop epoch 1 and alpha-001 seals", async () => {
    const currentOnly: PortfolioKeys = new Map([[ALPHA_COORD, { ceks: [cek(ALPHA_CEK_EPOCH2, 2)] }]]);
    const { items } = await load(currentOnly);
    expect(titles(items).get("alpha-001")).toBe(PLACEHOLDER);
    expect(titles(items).get("alpha-002")).toBe("Alpha after the rotation");
  });
});

// ---------------------------------------------------------------------------
// main() over a REAL portfolio fragment: no login form, read-only identity, and
// a damaged link that still leaves a usable page behind.
// ---------------------------------------------------------------------------
describe("main() opening a portfolio link", () => {
  let origHistory: History;
  let replaceState: ReturnType<typeof vi.fn>;

  beforeEach(() => {
    origHistory = window.history;
    replaceState = vi.fn();
    Object.defineProperty(window, "history", { value: { replaceState }, configurable: true, writable: true });
  });

  const restore = () => {
    Object.defineProperty(window, "history", { value: origHistory, configurable: true, writable: true });
    window.location.hash = "";
  };

  /** The exact fragment `rd board --portfolio --with-key` prints — pk= first,
   * keys= LAST (see portfolioURL). PORTFOLIO_KEYS_BLOB is Go-minted by the
   * PRODUCTION encoder over this fixture's real keys, so these cases drive the
   * whole chain main() -> parseFragment -> decodePortfolioKeys against bytes
   * `rd` would actually print. */
  function portfolioHash(blob: string): string {
    return `#pk=${VIEWER_PUB}&relays=${encodeURIComponent("wss://relay.test")}&keys=${blob}`;
  }

  it("mints a READ-ONLY identity, skips the login form, and renders several boards", async () => {
    let seen: Identity | undefined;
    const deps: BoardDeps = {
      ...noExtensionDeps,
      keyUnwrapper: (identity) => {
        seen = identity;
        return neverUnwraps;
      },
    };
    try {
      window.location.hash = portfolioHash(PORTFOLIO_KEYS_BLOB);
      main(deps);
      await vi.waitFor(() => expect(root.querySelector(".card[data-id]")).not.toBeNull());

      expect(seen, "main() never routed an identity through keyUnwrapper").toBeDefined();
      expect(seen!.pubkey).toBe(VIEWER_PUB);
      expect(canSign(seen!.auth), "a portfolio link must never mint a signing session").toBe(false);
      expect(loginFormShown(), "the login form was shown despite pk=").toBe(false);

      // Several boards' plaintext, from a link and nothing else.
      for (const want of expectedPlaintext) {
        expect(pageText(), `missing ${want.id}`).toContain(want.title);
      }
      assertGammaStaysSealed();

      // The fragment — key material and all — is gone from the address bar.
      expect(replaceState).toHaveBeenCalledWith(null, "", window.location.pathname + window.location.search);
    } finally {
      restore();
    }
  });

  it("a TRUNCATED portfolio link renders a usable page and still strips the fragment", async () => {
    // Done condition 3, and the ready-62d1 failure re-aimed: the page must not
    // die, must say the link looks damaged, and must not leave key material in
    // the URL. It must ALSO not open a partial portfolio that looks complete —
    // which is why the decoder throws rather than returning fewer boards.
    try {
      window.location.hash = portfolioHash(PORTFOLIO_KEYS_BLOB).slice(0, -40);
      main(noExtensionDeps);
      await vi.waitFor(() => expect(root.querySelector(".fragment-error")).not.toBeNull());

      // A usable page: the login form is there, so the reader can recover.
      expect(loginFormShown(), "a damaged link left the reader with no way to log in").toBe(true);
      expect(root.querySelector(".fragment-error")!.textContent).toContain("truncated");
      // NOT a partial portfolio pretending to be whole.
      expect(root.querySelector(".card[data-id]")).toBeNull();
      for (const want of expectedPlaintext) {
        expect(pageText()).not.toContain(want.title);
      }
      // And the damaged key material is out of the address bar anyway.
      expect(replaceState).toHaveBeenCalledWith(null, "", window.location.pathname + window.location.search);
    } finally {
      restore();
    }
  });

  it("a keyless portfolio link still opens the board list, with everything sealed", async () => {
    // `rd board --portfolio` without --with-key. Useful, and carrying no secret.
    try {
      window.location.hash = `#pk=${VIEWER_PUB}&relays=${encodeURIComponent("wss://relay.test")}`;
      main(noExtensionDeps);
      await vi.waitFor(() => expect(root.querySelector(".card[data-id]")).not.toBeNull());

      expect(loginFormShown()).toBe(false);
      for (const want of expectedPlaintext) {
        expect(pageText(), `title ${want.title} rendered from a keyless link`).not.toContain(want.title);
      }
      assertGammaStaysSealed();
    } finally {
      restore();
    }
  });

  it("no key material reaches browser storage", async () => {
    try {
      window.location.hash = portfolioHash(PORTFOLIO_KEYS_BLOB);
      main(noExtensionDeps);
      await vi.waitFor(() => expect(root.querySelector(".card[data-id]")).not.toBeNull());

      const stored = JSON.stringify({ ...localStorage, ...sessionStorage });
      for (const secret of [ALPHA_CEK_EPOCH1, ALPHA_CEK_EPOCH2, BETA_CEK, DELTA_CEK, PORTFOLIO_KEYS_BLOB]) {
        expect(stored).not.toContain(secret);
      }
    } finally {
      restore();
    }
  });
});
