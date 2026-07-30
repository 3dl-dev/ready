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

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { afterLogin, defaultDeps, loadBoardItems, main, type BoardDeps, type Identity } from "./main";
import { authTransition, canSign } from "./lib/auth";
import { fakeNip44Signer } from "./lib/fakesigner";
import { neverUnwraps } from "./lib/keyunwrap";
import { PLACEHOLDER } from "./lib/envelope";
import { bytesToHex, hexToBytes } from "./lib/sha256";
import { tagValue } from "./lib/nostrevent";
import type { NostrEvent } from "./lib/nostrevent";
import type { DiscoveredBoard } from "./lib/boarddiscovery";
import type { FragmentKeys, ParsedFragment } from "./lib/fragment";
import {
  BOARD_COORD,
  BOARD_D,
  CEK_EPOCH1,
  CEK_EPOCH2,
  LTK,
  OWNER_PUB,
  OWNER_SEC,
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

/** What cmd/rd/board.go's --with-key fragment carries for this board: BOTH held
 * epochs, because the board rotated at conf-003.
 *
 * `ltk` is here on the PARSE side only. The CLI stopped emitting ltk= (it had no
 * consumer in the page — least privilege), but a link minted by an older build
 * still carries one, and such a link must keep working; see
 * `bothEpochKeysWithLegacyLTK` and cmd/rd/board.go's boardKeyFragment. */
function bothEpochKeys(): FragmentKeys {
  return {
    ceks: [
      { epoch: 1, key: hexToBytes(CEK_EPOCH1) },
      { epoch: 2, key: hexToBytes(CEK_EPOCH2) },
    ],
  };
}

/** The shape a PRE-ready-df0-cleanup link carries: the same CEKs plus the
 * label-token key the CLI used to embed. Parsed, tolerated, ignored. */
function bothEpochKeysWithLegacyLTK(): FragmentKeys {
  return { ...bothEpochKeys(), ltk: hexToBytes(LTK) };
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

// ---------------------------------------------------------------------------
// ready-de7 — A CEK CANNOT REACH A SIGNING SESSION.
//
// keyring.ts's applyFragmentKeys BYPASSES the four checks deriveBoardKeyring
// applies to a relay-served grant. That is correct for reads, and its stated
// justification has two halves: the key came from the reader's own `rd` (which
// ran those checks locally), AND the session holding it cannot sign, so the key
// buys reading and nothing else. ready-1af made the second half real for a link
// that names a viewer — `pk=` mints `method: "readOnly"`, which is what leaves
// NostrBoardWriter without a signer.
//
// THE PATH THAT ESCAPED IT was a link with cek= and NO pk=. Nothing rejected
// that shape, main.ts had no viewer to open as, so it fell through to the LOGIN
// FORM — and a visitor who logged in there with a NIP-07 extension held a
// SIGNING session that was still handed the link's CEKs. Probed before the fix
// on exactly this fixture: an `extension` identity opened four sealed titles
// from link keys alone. No write escalation followed (a board carrying link keys
// is never "public" — confidentiality.ts — and NostrBoardWriter refuses every
// write to a confidential board), but the premise the bypass rests on was false,
// and the only thing standing in for it was an unrelated control that ready-034c
// is actively changing.
//
// fragment.ts now refuses cek= without pk=, so the chain is structural: CEKs
// imply a viewer, a viewer implies read-only, read-only implies no signer. This
// block is the composition-level witness for the first link of that chain — the
// unit-level one is lib/fragment.test.ts's "cek= without pk= is refused".
//
// WHY IT DRIVES main() AND A REAL EXTENSION. The premise is about what the PAGE
// does, not about what a parser returns, and the hazardous session is one that
// can sign. So: a genuinely working NIP-07 extension is installed the whole
// time, `keyUnwrapper` is the PRODUCTION predicate (defaultDeps), the login
// button is really clicked, and the resulting identity line is asserted NOT to
// say "(read-only)" — i.e. the assertions below run against the very session the
// bypass must never be handed keys for, not against a read-only one that could
// never have used them anyway.
// ---------------------------------------------------------------------------
describe("ready-de7: A CEK CANNOT REACH A SIGNING SESSION", () => {
  let origHistory: History;
  let replaceState: ReturnType<typeof vi.fn>;
  let origNostr: Window["nostr"];

  /** Deps that serve the whole fixture for any REQ and use the PRODUCTION
   * keyUnwrapper, so the extension below is genuinely reachable by any session
   * that can sign. */
  const prodUnwrapDeps: BoardDeps = {
    loadRelays: async () => ["wss://relay.test"],
    fetchEvents: async () => SNAPSHOT,
    keyUnwrapper: defaultDeps.keyUnwrapper,
  };

  beforeEach(() => {
    origHistory = window.history;
    replaceState = vi.fn();
    Object.defineProperty(window, "history", { value: { replaceState }, configurable: true, writable: true });
    origNostr = window.nostr;
    // A REAL extension holding the STRANGER's key: it answers getPublicKey and
    // it really does NIP-44-decrypt. The stranger holds no grant on this board,
    // so any plaintext that appears below can only have come from the LINK.
    const signer = fakeNip44Signer(STRANGER_SEC);
    window.nostr = {
      getPublicKey: async () => STRANGER_PUB,
      nip44: { decrypt: (pk: string, ct: string) => signer.decrypt(pk, ct) },
    } as unknown as Window["nostr"];
  });

  afterEach(() => {
    Object.defineProperty(window, "history", { value: origHistory, configurable: true, writable: true });
    window.nostr = origNostr;
    window.location.hash = "";
  });

  const extensionButton = () =>
    [...root.querySelectorAll("button")].find((b) => (b.textContent ?? "").includes("extension"));

  it("a cek= link with no pk= is reported as damaged, and an extension login gets NO key material", async () => {
    window.location.hash =
      `#board=${encodeURIComponent(BOARD_COORD)}&cek=1:${CEK_EPOCH1},2:${CEK_EPOCH2}`;
    main(prodUnwrapDeps);

    // The link is refused as incomplete, and the reader is left able to recover.
    await vi.waitFor(() => expect(root.querySelector(".fragment-error")).not.toBeNull());
    const btn = extensionButton();
    expect(btn, "no extension button to log in with").toBeDefined();
    // NOT vacuous: the extension is present and the button is live, so the
    // signing login below is a real one.
    expect(btn!.disabled).toBe(false);
    // And the key material is out of the address bar regardless.
    expect(replaceState).toHaveBeenCalledWith(null, "", window.location.pathname + window.location.search);

    btn!.click();
    await vi.waitFor(() => expect(root.querySelector(".identity")).not.toBeNull());

    // THIS SESSION CAN SIGN — that is the whole point of the case.
    expect(root.querySelector(".identity")!.textContent).not.toContain("(read-only)");
    // And it holds nothing: no board, no cards, no plaintext, no ciphertext.
    expect(pageText()).toContain("No boards found.");
    expect(root.querySelector(".card[data-id]")).toBeNull();
    assertNothingLeaked([]);
  });

  it("ANTI-TAUTOLOGY: the SAME keys, deps and extension DO open the board once the link names its viewer — read-only", async () => {
    // Without this, the silence above could mean the fixture keys were wrong, or
    // that the fake relay served nothing. Only `pk=` differs between the two
    // cases, and here every sealed title renders — from the link alone, since a
    // read-only identity makes defaultDeps.keyUnwrapper inert.
    window.location.hash =
      `#board=${encodeURIComponent(BOARD_COORD)}&pk=${OWNER_PUB}&cek=1:${CEK_EPOCH1},2:${CEK_EPOCH2}`;
    main(prodUnwrapDeps);

    await vi.waitFor(() => expect(root.querySelector(".card[data-id]")).not.toBeNull());
    expect(root.querySelector(".fragment-error")).toBeNull();
    expect(extensionButton(), "a pk= link must not show a login form").toBeUndefined();
    expect(root.querySelector(".identity")!.textContent).toContain("(read-only)");
    for (const want of expectedPlaintext) {
      expect(pageText(), `missing ${want.id}`).toContain(want.title);
    }
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

// ---------------------------------------------------------------------------
// THE pk= IDENTITY CANNOT SIGN.
//
// main.ts mints an Identity out of the fragment's `pk=` and marks it
// readOnly, and its own comment names that marking as the control that keeps
// keyUnwrapper() inert. Until this block existed, NOTHING witnessed it: flipping
// that one word to `method: "extension"` passed the entire suite. With it
// flipped, canSign() becomes true, so defaultDeps hands the board an unwrapper
// wired to whatever NIP-07 extension happens to be installed — and asks it to
// nip44.decrypt grants for a pubkey NOBODY AUTHENTICATED AS. A `pk=` value is
// not a proof of possession; it is a string somebody put in a link. The whole
// reason a CEK in a URL was acceptable is that a CEK cannot sign, and this is
// where that stops being true if the marking goes.
//
// So the assertions below run against the PRODUCTION predicate
// (defaultDeps.keyUnwrapper) and a REAL, working extension — one that genuinely
// opens the owner's grants, proven in the last case. "Returns null" therefore
// means "the control refused", not "there was nothing to decrypt anyway".
// ---------------------------------------------------------------------------
describe("the pk= identity CANNOT SIGN, so an installed extension is never reached", () => {
  /** A REAL extension, holding the OWNER's key: it can open every grant in the
   * fixture. Present in the page the whole time — that is the hazard. */
  let decrypt: ReturnType<typeof vi.fn>;
  let origHistory: History;

  /** The owner's own epoch-1 grant, taken straight out of the Go-signed fixture:
   * a wrap this extension really does open. */
  const ownerGrantEpoch1 = grants.find(
    (e) => e.pubkey === OWNER_PUB && tagValue(e, "p") === OWNER_PUB && tagValue(e, "cek_epoch") === "1",
  )!;
  const WRAPPED_CEK1 = tagValue(ownerGrantEpoch1, "cek");

  beforeEach(() => {
    expect(WRAPPED_CEK1).not.toBe("");
    const signer = fakeNip44Signer(OWNER_SEC);
    decrypt = vi.fn((pk: string, ct: string) => signer.decrypt(pk, ct));
    window.nostr = { getPublicKey: async () => OWNER_PUB, nip44: { decrypt } };

    origHistory = window.history;
    Object.defineProperty(window, "history", {
      value: { replaceState: vi.fn() },
      configurable: true,
      writable: true,
    });
  });

  afterEach(() => {
    delete window.nostr;
    Object.defineProperty(window, "history", { value: origHistory, configurable: true, writable: true });
    window.location.hash = "";
  });

  /** Drives the REAL main() over a REAL key-bearing fragment and hands back the
   * identity main() minted. The identity is captured through the keyUnwrapper
   * seam, which is where the composition asks "may this session decrypt?" — so
   * what is asserted below is the exact value production hands that predicate,
   * not a look-alike a test built. */
  async function openKeyLink(): Promise<Identity> {
    let seen: Identity | undefined;
    const deps: BoardDeps = {
      loadRelays: async () => ["wss://relay.test"],
      fetchEvents: async () => SNAPSHOT,
      // The PRODUCTION predicate, unmodified, with the real window.nostr above
      // in scope. Only the relay transport is faked.
      keyUnwrapper: (identity) => {
        seen = identity;
        return defaultDeps.keyUnwrapper(identity);
      },
    };
    window.location.hash =
      `#board=${encodeURIComponent(BOARD_COORD)}&pk=${OWNER_PUB}` +
      `&cek=1:${CEK_EPOCH1},2:${CEK_EPOCH2}`;
    main(deps);
    await vi.waitFor(() => expect(root.querySelector(".card[data-id]")).not.toBeNull());
    expect(seen, "main() never routed an identity through keyUnwrapper").toBeDefined();
    return seen!;
  }

  it("mints a read-only identity: canSign() is false", async () => {
    const identity = await openKeyLink();
    expect(identity.pubkey).toBe(OWNER_PUB);
    expect(identity.auth.readOnly).toBe(true);
    expect(canSign(identity.auth)).toBe(false);
  });

  it("never asks window.nostr to decrypt anything, across a whole page load", async () => {
    await openKeyLink();
    // Not vacuous: the page DID render the sealed titles — it just did it from
    // the fragment's own keys, with the extension sitting there untouched.
    expect(pageText()).toContain(expectedPlaintext[0].title);
    expect(decrypt, "an extension was asked to decrypt for a pubkey that never authenticated").not.toHaveBeenCalled();
  });

  it("the PRODUCTION keyUnwrapper for that identity is inert — null, without calling the extension", async () => {
    const identity = await openKeyLink();
    const unwrap = defaultDeps.keyUnwrapper(identity);

    expect(await unwrap(OWNER_PUB, WRAPPED_CEK1)).toBeNull();
    expect(decrypt).not.toHaveBeenCalled();
  });

  it("ANTI-TAUTOLOGY: that same extension, same wrap, DOES open for a signing identity", async () => {
    // If this case failed, "null" above would prove nothing — it could just mean
    // the fake signer was broken. It opens, so the null is the control refusing.
    const signing: Identity = {
      pubkey: OWNER_PUB,
      auth: authTransition({ type: "login", method: "extension" }),
    };
    const opened = await defaultDeps.keyUnwrapper(signing)(OWNER_PUB, WRAPPED_CEK1);
    expect(opened).not.toBeNull();
    expect(bytesToHex(opened!)).toBe(CEK_EPOCH1);
    expect(decrypt).toHaveBeenCalled();
  });

  it("says READ-ONLY on the identity line the reader actually sees", async () => {
    await openKeyLink();
    const line = root.querySelector(".identity")?.textContent ?? "";
    expect(line).toContain("Logged in as npub1");
    expect(line).toContain("(read-only)");
  });
});

// ---------------------------------------------------------------------------
// PER-BOARD KEY SCOPE.
//
// loadBoardItems applies a link's keys only to the ONE coordinate the SAME
// fragment names (`fragmentKeys.coord === b.coord`). Removing that call-site
// check used to pass the whole suite: keyring.test.ts's scoping case proves only
// that applyFragmentKeys honours the coordinate ARGUMENT it is handed, and the
// "a link naming a board that does not exist" case above explicitly disclaims
// being the scoping proof. Nothing asserted that the CALLER passes the right
// argument, or passes it only for the right board.
//
// It is unreachable in production TODAY only because a `#board=` link is
// boardD-filtered by discoverOwnerBoards down to one coordinate. The
// whole-portfolio link (ready-4d9) makes the board list genuinely multi-board —
// mountBoardWorkspace already takes a multi-board list — so this check becomes
// load-bearing within the next item. Witness it now, not after.
// ---------------------------------------------------------------------------
describe("PER-BOARD KEY SCOPE — a key for one board is never offered to another", () => {
  /** A second board in the same reader's portfolio, owned by the same key. It is
   * the board the link does NOT name. */
  const OTHER_COORD = `30301:${OWNER_PUB}:otherboard`;

  const confidentialBoard: DiscoveredBoard = {
    coord: BOARD_COORD,
    ownerPubkey: OWNER_PUB,
    boardD: BOARD_D,
    title: "Confidential Board",
  };
  const otherBoard: DiscoveredBoard = {
    coord: OTHER_COORD,
    ownerPubkey: OWNER_PUB,
    boardD: "otherboard",
    title: "Another Board",
  };

  /** No signer at all — a plain browser opening a link. So the ONLY key material
   * that can reach any keyring here is the fragment's, which is precisely what
   * makes the scoping question the only question. */
  async function load(boards: DiscoveredBoard[], fragmentCoord: string) {
    return loadBoardItems(
      boards,
      ["wss://relay.test"],
      SNAPSHOT,
      linkIdentity(OWNER_PUB),
      noExtensionDeps,
      () => {},
      // ready-4d9 turned this argument into a coordinate -> keys MAP (the shape
      // a portfolio link produces). A single-board link is now the one-entry
      // case of it, which is exactly what main.ts's fragmentKeyMap builds — so
      // this helper still models a `#board=` link faithfully, and the scoping
      // question it asks is unchanged.
      new Map([[fragmentCoord, bothEpochKeys()]]),
    );
  }

  const titlesOf = (items: { id: string; title: string }[]) => new Map(items.map((i) => [i.id, i.title]));

  it("a key scoped to board B leaves board A's items SEALED, even though both are in the same list", async () => {
    // TWO boards in the list. The fragment names the OTHER one. Nothing about
    // the confidential board's cards changed — the same hostile relay serves the
    // same snapshot — so the only thing standing between this reader and those
    // titles is the coordinate check.
    const { items } = await load([confidentialBoard, otherBoard], OTHER_COORD);

    const byId = titlesOf(items);
    expect(byId.size).toBeGreaterThan(0);
    for (const want of expectedPlaintext) {
      expect(byId.get(want.id), `item ${want.id} opened with a key scoped to another board`).toBe(PLACEHOLDER);
    }
    // And the strongest form of the same statement: no plaintext anywhere.
    const blob = JSON.stringify(items);
    for (const want of expectedPlaintext) {
      expect(blob).not.toContain(want.title);
      expect(blob).not.toContain(want.context);
    }
  });

  it("ANTI-TAUTOLOGY: the identical two-board list, the identical keys, scoped to board A instead — and A opens", async () => {
    // Without this case the one above would pass on a build where fragment keys
    // never worked at all.
    const { items } = await load([confidentialBoard, otherBoard], BOARD_COORD);

    const byId = titlesOf(items);
    for (const want of expectedPlaintext) {
      expect(byId.get(want.id)).toBe(want.title);
    }
  });

  it("the scope holds whichever order the boards come back in", async () => {
    // discoverOwnerBoards sorts by coordinate, and a future portfolio link has
    // no reason to preserve any particular order. The check must not depend on
    // the named board being visited first (or last).
    const { items } = await load([otherBoard, confidentialBoard], OTHER_COORD);
    const byId = titlesOf(items);
    for (const want of expectedPlaintext) {
      expect(byId.get(want.id)).toBe(PLACEHOLDER);
    }
  });
});

// ---------------------------------------------------------------------------
// The LTK is no longer emitted (least privilege: keyring.ltk() and labelToken()
// have no production consumer). A link minted by an older build still carries
// one, so the PARSE side stays tolerant — that is what these two cases pin.
// ---------------------------------------------------------------------------
describe("a legacy link that still carries ltk= keeps working", () => {
  it("renders exactly the same titles as the same link without ltk=", async () => {
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(bothEpochKeysWithLegacyLTK()), noExtensionDeps);
    const withLTK = renderedItems();

    root.replaceChildren();
    await afterLogin(root, linkIdentity(OWNER_PUB), keyFragment(bothEpochKeys()), noExtensionDeps);

    expect(withLTK).toEqual(renderedItems());
    for (const want of expectedPlaintext) {
      expect(withLTK.find((i) => i.id === want.id)!.title).toBe(want.title);
    }
  });

  it("main() parses an ltk=-bearing fragment without throwing and still opens the board", () => {
    document.body.innerHTML = '<div id="app"></div>';
    const origHistory = window.history;
    Object.defineProperty(window, "history", {
      value: { replaceState: vi.fn() },
      configurable: true,
      writable: true,
    });
    window.location.hash =
      `#board=${encodeURIComponent(BOARD_COORD)}&pk=${OWNER_PUB}&cek=1:${CEK_EPOCH1}&ltk=${LTK}`;
    try {
      expect(() => main(noExtensionDeps)).not.toThrow();
      // Not the ready-62d1 degraded login page: an old link is VALID, not damaged.
      expect(document.getElementById("app")!.className).toBe("board-page");
      expect(document.querySelector(".fragment-error")).toBeNull();
    } finally {
      Object.defineProperty(window, "history", { value: origHistory, configurable: true, writable: true });
      window.location.hash = "";
    }
  });
});
