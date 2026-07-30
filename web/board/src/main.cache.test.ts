// @vitest-environment jsdom
//
// ready-fe4 — THE PAGE PAINTS FROM CACHE AND STILL FAILS CLOSED.
//
// WHY THIS FILE EXISTS SEPARATELY FROM lib/boardcache.test.ts. That file proves
// the RULES (fingerprint, admission, prune). This one proves the rules are
// WIRED: it drives the real afterLogin over the Go-signed portfolio fixture —
// real NIP-44 wraps, real ChaCha20-Poly1305 envelopes, real signatures — and
// asks the DOM what a person actually sees, at two distinct instants:
//
//   * THE WARM PAINT. afterLogin's cached paint happens before its first
//     `await`, so calling it WITHOUT awaiting and reading the DOM on the very
//     next line is a precise instrument for "what is on screen before any relay
//     round-trip". Every "paints immediately" claim below is measured there.
//   * THE SETTLED LOAD. After the returned promise resolves, what is on screen
//     must be exactly what this session's own fold produced and nothing else.
//
// THE THREE SECURITY PROPERTIES, each with the neutralization that turns it red
// stated in the test itself:
//
//   1. A cached board is not painted into a session that would put DIFFERENT
//      inputs to the read gates (different viewer, or a link that no longer
//      carries the board's key). admissibleBoards holds this with TWO guards
//      and each has its own case here, because deleting one and seeing green is
//      how a fail-closed check gets deleted for real: the KEY-PATH BELT is what
//      "A LINK THAT NO LONGER CARRIES THE KEYS" turns red, and the FINGERPRINT
//      check is what "ONLY THE FINGERPRINT CATCHES THIS ONE" turns red. Each
//      annotation states which, verified by deleting one guard at a time.
//   2. A card the CURRENT fold quarantines does not survive from a cached entry.
//      Neutralize reconcileOne's replace-don't-merge and the withheld card stays.
//   3. A card no read-trusted key ever signed cannot be introduced BY the cache.
//      Neutralize the same replace and a fabricated card survives the load.
//
// WHAT THIS FILE IS NOT, stated because the first round of ready-fe4 offered it
// as something it is not. Its instrument is TWO FULL LOADS in jsdom, which shows
// that a second load paints early and converges — it does NOT show that an OPEN
// page converges, and ready-fe4 condition 4 asks for exactly that ("mutate an
// item via the rd CLI and confirm the open board converges without a manual
// reload"). That proof is scripts/live-cache.mjs part B: a real Chromium with a
// warm localStorage paints a title the rd CLI has already superseded on
// wss://relay.3dl.network, and the SAME document — a window sentinel a reload
// would erase is checked afterwards — replaces it with the newer one.

import { beforeEach, describe, expect, it } from "vitest";
import { afterLogin, defaultDeps, type BoardDeps, type Identity } from "./main";
import { authTransition } from "./lib/auth";
import { neverUnwraps } from "./lib/keyunwrap";
import { PLACEHOLDER } from "./lib/envelope";
import { hexToBytes } from "./lib/sha256";
import { gateFingerprint, scopeKey, serializeView, type CacheStorage, type CachedView } from "./lib/boardcache";
import type { FragmentKeys, ParsedFragment } from "./lib/fragment";
import type { PortfolioKeys } from "./lib/portfoliokeys";
import type { NostrEvent } from "./lib/nostrevent";
import type { Item } from "./board/types";
import {
  ALPHA_CEK_EPOCH1,
  ALPHA_CEK_EPOCH2,
  ALPHA_COORD,
  BETA_CEK,
  BETA_COORD,
  DELTA_CEK,
  DELTA_COORD,
  GAMMA_CEK,
  GAMMA_COORD,
  VIEWER_PUB,
  snapshot,
} from "./lib/portfolio.fixtures";

const OTHER_VIEWER = "c".repeat(64);

let root: HTMLElement;
let storage: CacheStorage & { data: Map<string, string> };

/** A real Storage, in memory. jsdom has a localStorage, but sharing one across
 * cases is how a cache test lies to itself. */
function memStorage(): CacheStorage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    get length() {
      return data.size;
    },
    key: (i: number) => [...data.keys()][i] ?? null,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  };
}

beforeEach(() => {
  document.body.replaceChildren();
  root = document.createElement("div");
  root.id = "app";
  document.body.append(root);
  storage = memStorage();
  localStorage.clear();
  sessionStorage.clear();
});

const cek = (hex: string, epoch: number) => ({ epoch, key: hexToBytes(hex) });

function portfolioKeys(): PortfolioKeys {
  return new Map<string, FragmentKeys>([
    [ALPHA_COORD, { ceks: [cek(ALPHA_CEK_EPOCH1, 1), cek(ALPHA_CEK_EPOCH2, 2)] }],
    [BETA_COORD, { ceks: [cek(BETA_CEK, 1)] }],
    [DELTA_COORD, { ceks: [cek(DELTA_CEK, 1)] }],
  ]);
}

/** The same link PLUS gamma's key. Used only by the quarantine case below,
 * which needs a board this session can genuinely open — the belt in
 * admissibleBoards refuses to paint a confidential board into a session with no
 * key path at all, so without gamma's key there would be no cached paint for the
 * quarantine to be tested against. */
function portfolioKeysWithGamma(): PortfolioKeys {
  return new Map<string, FragmentKeys>([...portfolioKeys(), [GAMMA_COORD, { ceks: [cek(GAMMA_CEK, 1)] }]]);
}

function fragment(keys?: PortfolioKeys, viewer = VIEWER_PUB): ParsedFragment {
  return { kind: "portfolio", relays: ["wss://relay.test"], viewer, keys };
}

function identity(pubkey = VIEWER_PUB): Identity {
  return { pubkey, auth: authTransition({ type: "login", method: "readOnly" }) };
}

/** Deps serving `events`, with the cache wired to THIS case's storage. No
 * extension, ever: every plaintext title below was opened by a key from the
 * link and nothing else. */
function deps(events: NostrEvent[] = snapshot): BoardDeps {
  return {
    loadRelays: async () => ["wss://relay.test"],
    fetchEvents: async () => events,
    keyUnwrapper: () => neverUnwraps,
    cacheStorage: () => storage,
  };
}

const pageText = () => root.textContent ?? "";
const cardTitles = () =>
  [...root.querySelectorAll(".card[data-id]")].map((c) => c.querySelector(".card-title")?.textContent ?? "");
const cardIds = () => [...root.querySelectorAll(".card[data-id]")].map((c) => c.getAttribute("data-id") ?? "");

/** The fixture's gamma board minus its owner grant: the relay serves the sealed
 * cards but withholds the kind-39301 that establishes WHEN gamma went
 * confidential. confidentialityOf then returns "unknown", and the fold withholds
 * every card on gamma that is not a sealed envelope — including the genuine
 * pre-cutover plaintext one, which is exactly what a cached entry must not be
 * able to put back. */
function snapshotWithoutGammaGrant(): NostrEvent[] {
  return snapshot.filter(
    (e) => !(e.kind === 39301 && e.tags.some((t) => t[0] === "a" && t[1] === GAMMA_COORD)),
  );
}

// ---------------------------------------------------------------------------
// THE OUTCOME: a board this session has seen before is on screen before any
// relay round-trip.
// ---------------------------------------------------------------------------
describe("a board the session has seen before paints immediately", () => {
  it("the SECOND load has real cards on screen before its first await resolves", async () => {
    await afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    const cold = cardIds();
    expect(cold.length, "the cold load rendered nothing — nothing to cache").toBeGreaterThan(0);
    expect(storage.data.size, "nothing was written back").toBe(1);

    // A brand-new page, same browser, same link.
    root.replaceChildren();
    const pending = afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    // NO await has resolved yet. This is the warm paint.
    const warm = cardIds();
    expect(warm.length, "the warm paint put no card on screen").toBeGreaterThan(0);
    expect(cardTitles()).toContain("Alpha epoch one card");
    // …and it says, on every board node, that this is not yet this session's
    // own read (ready-27b's rule, one state further out).
    expect(pageText()).toContain("from cache");

    await pending;
    // Converged to the same set the cold load produced, with no "from cache"
    // claim left standing.
    expect(new Set(cardIds())).toEqual(new Set(cold));
    expect(pageText()).not.toContain("from cache");
  });

  it("the FIRST load paints nothing from cache and still renders", async () => {
    const pending = afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    expect(cardIds(), "a cold page must not invent cards").toEqual([]);
    await pending;
    expect(cardIds().length).toBeGreaterThan(0);
  });

  it("a page with NO cacheStorage behaves exactly as it did before this item", async () => {
    const noCache: BoardDeps = { ...deps(), cacheStorage: undefined };
    await afterLogin(root, identity(), fragment(portfolioKeys()), noCache);
    const first = new Set(cardIds());
    root.replaceChildren();
    const pending = afterLogin(root, identity(), fragment(portfolioKeys()), noCache);
    expect(cardIds()).toEqual([]);
    await pending;
    expect(new Set(cardIds())).toEqual(first);
  });

  it("production wires a real store — otherwise every test here proves nothing about the deployed page", () => {
    expect(typeof defaultDeps.cacheStorage).toBe("function");
  });
});

// ---------------------------------------------------------------------------
// SECURITY 1 — the fingerprint gate.
// ---------------------------------------------------------------------------
describe("the cached paint is refused to a session that would ask a different question", () => {
  it("A LINK THAT NO LONGER CARRIES THE KEYS PAINTS NOTHING, not even for an instant", async () => {
    // Session 1: the key-bearing link. alpha/beta/delta open in plaintext.
    await afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    expect(cardTitles()).toContain("Alpha epoch one card");

    // Session 2: same viewer, same browser, same boards — but the link this
    // time carries no keys at all. This session cannot decrypt anything (no
    // extension, no CEK), so a cached paint of session 1's DECRYPTED titles
    // would be the page showing content the current session has no way to open.
    root.replaceChildren();
    const pending = afterLogin(root, identity(), fragment(undefined), deps());

    // NEUTRALIZATION — THE KEY-PATH BELT, not the fingerprint. Delete the
    // `b.state !== "public" && !(...)` line in boardcache.ts's admissibleBoards
    // and this assertion fails with ['gamma-002','gamma-001'] on screen
    // (measured 2026-07-30, deleting each guard on its own and running the
    // whole suite).
    //
    // DELETING THE FINGERPRINT CHECK INSTEAD LEAVES THIS CASE GREEN, and that
    // is not evidence the fingerprint check is dead — do not read it that way
    // and delete it. Both guards refuse alpha/beta/delta here, so removing
    // either one alone still hides their plaintext; what only the belt refuses
    // is GAMMA, whose entry was written by a session that held no gamma key
    // either, so its stored `cek:` fingerprint MATCHES this keyless session's
    // and the fingerprint admits it. The guard the fingerprint alone holds is
    // the case below, "ONLY THE FINGERPRINT CATCHES THIS ONE".
    expect(cardIds(), "the warm paint admitted a board this session holds no key for").toEqual([]);
    expect(pageText()).not.toContain("Alpha epoch one card");

    await pending;
    // And the settled load agrees: everything sealed.
    expect(pageText()).not.toContain("Alpha epoch one card");
    expect(cardTitles()).toContain(PLACEHOLDER);
  });

  it("A DIFFERENT VIEWER PAINTS NOTHING, and the previous viewer's entry is discarded", async () => {
    await afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    expect(storage.data.size).toBe(1);

    root.replaceChildren();
    const pending = afterLogin(root, identity(OTHER_VIEWER), fragment(portfolioKeys(), OTHER_VIEWER), deps());
    expect(cardIds(), "another pubkey's session painted this pubkey's board").toEqual([]);
    expect(pageText()).not.toContain("Alpha epoch one card");
    // ready-fe4 condition 5: keyed per logged-in pubkey and dropped when
    // another identity opens the page. There is no logout button; this is the
    // only moment the page can observe the change.
    expect([...storage.data.keys()].some((k) => k.includes(VIEWER_PUB))).toBe(false);
    await pending;
  });

  it("ONLY THE FINGERPRINT CATCHES THIS ONE: same viewer, same keys, different signing capability", async () => {
    // Constructed so the BELT passes and only the fingerprint can refuse: the
    // entry was recorded by a SIGNING session (gate says "sign"), and this
    // session holds the link's keys, so `signing || linkKeys` is true either
    // way. What differs is the capability the admitting decision ran under —
    // a signing session unwraps owner grants, a link session does not, so the
    // two sessions do not see the same board and one's answer is not the
    // other's.
    const view: CachedView = {
      v: 1,
      viewer: VIEWER_PUB,
      scope: scopeKey("portfolio"),
      savedAt: Date.now(),
      boards: [
        {
          coord: ALPHA_COORD,
          title: "Alpha Board",
          state: "confidential",
          boardState: "open",
          detail: "",
          gate: gateFingerprint({
            viewer: VIEWER_PUB,
            signing: true, // <- the only difference
            linkKeys: portfolioKeys().get(ALPHA_COORD),
          }),
          high: 0,
          items: [
            {
              id: "from-a-signing-session",
              title: "ONLY A SIGNING SESSION SAW THIS",
              type: "task",
              for: VIEWER_PUB,
              priority: "p1",
              status: "inbox",
              createdAt: 1,
              updatedAt: 2,
              boardCoord: ALPHA_COORD,
            },
          ],
        },
      ],
    };
    storage.setItem(`rd.board.cache.v1.${VIEWER_PUB}.${scopeKey("portfolio")}`, serializeView(view));

    const pending = afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    // NEUTRALIZATION: delete the `b.gate !== gateFingerprint(...)` line in
    // admissibleBoards and this fails — the belt lets it through.
    expect(cardIds(), "a signing session's entry painted into a read-only one").not.toContain(
      "from-a-signing-session",
    );
    expect(pageText()).not.toContain("ONLY A SIGNING SESSION SAW THIS");
    await pending;
    expect(pageText()).not.toContain("ONLY A SIGNING SESSION SAW THIS");
  });

  it("NOT VACUOUS: the same two loads with the SAME inputs DO paint", async () => {
    // Without this, both cases above would pass on a cache that never paints.
    await afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    root.replaceChildren();
    const pending = afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    expect(cardIds().length).toBeGreaterThan(0);
    await pending;
  });
});

// ---------------------------------------------------------------------------
// SECURITY 2 — the quarantine.
// ---------------------------------------------------------------------------
describe("a card the CURRENT fold withholds does not survive from the cache", () => {
  it("gamma's grandfathered plaintext is gone once the grants stop establishing the cutover", async () => {
    // Session 1: gamma's owner grant is served, so the cutover is established
    // and gamma's genuine PRE-cutover plaintext card is grandfathered in. The
    // link carries gamma's key, so this session can open the board and its
    // cached paint is admissible to the next one.
    await afterLogin(root, identity(), fragment(portfolioKeysWithGamma()), deps());
    expect(pageText(), "the fixture never rendered the grandfathered card").toContain("Gamma legacy plaintext");

    // Session 2: the relay withholds gamma's grant. Nothing else changes — same
    // viewer, same link, same keys — so the fingerprint MATCHES and the cache
    // paints. The fold, however, can no longer establish gamma's cutover, so it
    // withholds every card on gamma that is not a sealed envelope.
    root.replaceChildren();
    const pending = afterLogin(
      root,
      identity(),
      fragment(portfolioKeysWithGamma()),
      deps(snapshotWithoutGammaGrant()),
    );
    expect(pageText(), "the warm paint should have shown the cached board").toContain("Gamma legacy plaintext");
    await pending;

    // NEUTRALIZATION: make BoardView.reconcileOne MERGE its items into the
    // cached ones instead of replacing them, and this assertion fails — the
    // withheld card is still on screen after the load.
    expect(pageText(), "a card the current fold quarantined survived from the cache").not.toContain(
      "Gamma legacy plaintext",
    );
    expect(pageText()).toContain("CONFIDENTIALITY STATE COULD NOT BE ESTABLISHED");
    // The SMUGGLED post-cutover cleartext was never admitted in either session
    // and is not admitted now — the quarantine is not selectively lenient about
    // which withheld card the cache may put back.
    expect(pageText()).not.toContain("GAMMA SMUGGLED CLEARTEXT");
    expect(pageText()).not.toContain("GAMMA SMUGGLED BODY");
  });

  it("and gamma is DROPPED from the cache, so a third session never paints it again", async () => {
    await afterLogin(root, identity(), fragment(portfolioKeysWithGamma()), deps());
    root.replaceChildren();
    await afterLogin(root, identity(), fragment(portfolioKeysWithGamma()), deps(snapshotWithoutGammaGrant()));

    const stored = JSON.parse([...storage.data.values()][0]) as CachedView;
    expect(stored.boards.map((b) => b.coord), "an unestablished board was cached").not.toContain(GAMMA_COORD);

    root.replaceChildren();
    const pending = afterLogin(
      root,
      identity(),
      fragment(portfolioKeysWithGamma()),
      deps(snapshotWithoutGammaGrant()),
    );
    expect(pageText()).not.toContain("Gamma legacy plaintext");
    await pending;
  });

  it("BELT: a confidential board is not painted at all into a session with no key path", async () => {
    // The same two sessions, but the link never carries gamma's key. The
    // fingerprint MATCHES (no gamma key either time), so only admissibleBoards'
    // state check stands between the cached grandfathered card and the screen.
    await afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    expect(pageText(), "gamma's grandfathered card never rendered, so nothing was cached").toContain(
      "Gamma legacy plaintext",
    );

    root.replaceChildren();
    const pending = afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    // NEUTRALIZATION: delete the `b.state !== "public" && !(...)` line in
    // admissibleBoards and this fails — gamma paints from cache into a session
    // that holds no way to open it.
    expect(pageText(), "a confidential board painted into a keyless session").not.toContain("Gamma legacy plaintext");
    // NOT VACUOUS: the boards this session CAN open did paint.
    expect(cardTitles()).toContain("Alpha epoch one card");
    await pending;
  });
});

// ---------------------------------------------------------------------------
// SECURITY 3 — the read-trust gate (ready-605).
// ---------------------------------------------------------------------------
describe("the cache cannot introduce a card no read-trusted key signed", () => {
  it("a fabricated card in a fingerprint-VALID entry is gone once the board loads", async () => {
    // The hostile input is a tampered store, which is the only way a card that
    // no key signed can reach this cache at all: nothing but the fold's own
    // output is ever written. The fingerprint is correct, so the paint gate
    // admits it — this test is about what happens NEXT.
    const forged: Item = {
      id: "forged-001",
      title: "FORGED BY NOBODY",
      type: "task",
      for: VIEWER_PUB,
      priority: "p0",
      status: "active",
      createdAt: 1,
      updatedAt: 9_999_999,
      boardCoord: ALPHA_COORD,
    };
    const view: CachedView = {
      v: 1,
      viewer: VIEWER_PUB,
      scope: scopeKey("portfolio"),
      savedAt: Date.now(),
      boards: [
        {
          coord: ALPHA_COORD,
          title: "Alpha",
          state: "confidential",
          boardState: "open",
          detail: "",
          gate: gateFingerprint({
            viewer: VIEWER_PUB,
            signing: false,
            linkKeys: portfolioKeys().get(ALPHA_COORD),
          }),
          high: 0,
          items: [forged],
        },
      ],
    };
    storage.setItem(`rd.board.cache.v1.${VIEWER_PUB}.${scopeKey("portfolio")}`, serializeView(view));

    const pending = afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    // It IS painted — localStorage is same-origin-writable and the paint is a
    // paint. The claim this file makes is not that a tampered store is
    // undetectable, it is that it cannot outlive the reader's own relay answer.
    expect(cardIds()).toContain("forged-001");

    await pending;
    // NEUTRALIZATION: make BoardView.reconcileOne merge instead of replace and
    // this fails — a card signed by nobody is in the settled board.
    expect(cardIds(), "a card the read-trust gate never admitted survived the load").not.toContain("forged-001");
    expect(pageText()).not.toContain("FORGED BY NOBODY");
    // And the load DID happen, so the absence above is a replacement and not a
    // page that rendered nothing.
    expect(cardTitles()).toContain("Alpha epoch one card");
  });

  it("a board that vanishes from discovery takes its cached cards with it", async () => {
    await afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    expect(pageText()).toContain("Beta board card");

    // The relay no longer serves beta's kind-30301 definition, so discovery
    // does not mint its coordinate. A board that is not discovered is ABSENT,
    // and absent must not keep painting.
    const withoutBeta = snapshot.filter(
      (e) => !(e.kind === 30301 && e.tags.some((t) => t[0] === "d" && t[1] === "beta")),
    );
    root.replaceChildren();
    await afterLogin(root, identity(), fragment(portfolioKeys()), deps(withoutBeta));
    expect(pageText(), "a board discovery did not find kept painting from cache").not.toContain("Beta board card");
  });
});

// ---------------------------------------------------------------------------
// ready-c4b condition 6, now actually exercised: the page writes on every load.
// ---------------------------------------------------------------------------
describe("no key material reaches the cache", () => {
  it("a full confidential portfolio load writes no CEK, in any encoding", async () => {
    await afterLogin(root, identity(), fragment(portfolioKeys()), deps());
    const written = [...storage.data.values()].join("\n");
    expect(written.length, "nothing was written, so this test proves nothing").toBeGreaterThan(100);
    for (const secret of [ALPHA_CEK_EPOCH1, ALPHA_CEK_EPOCH2, BETA_CEK, DELTA_CEK]) {
      expect(written, `raw hex CEK in the cache: ${secret}`).not.toContain(secret);
      // The bytes could also travel as a JSON array or as base64.
      const bytes = [...hexToBytes(secret)];
      expect(written, "CEK bytes as a JSON array").not.toContain(JSON.stringify(bytes).slice(1, 40));
      expect(written, "CEK bytes as base64").not.toContain(btoa(String.fromCharCode(...bytes)).slice(0, 20));
    }
  });
});
