// @vitest-environment jsdom
//
// ready-27b — THE PORTFOLIO DEGRADES HONESTLY, PER BOARD.
//
// The portfolio view is a list of boards from different owners under different
// keys, and the failure it must not have is the quiet one: a board the page
// could not read looking exactly like a board with no work. There is no way to
// tell those apart from a card count, and the aggregate notices this page
// already carries cannot tell them apart either — they are portfolio-wide
// totals ("N of M titles were decrypted"), which is the right sentence for the
// view and the wrong one for a project.
//
// Four outcomes are asserted here, each on the board's OWN node and its OWN
// status row, and each with an anti-tautology twin that flips exactly one input:
//
//   open              the board's work is here.
//   sealed            confidential; no key for it reached this session.
//   unreadable-grant  confidential; a grant NAMING THIS KEY arrived and this
//                     browser could not open it — the state 14 of the ~24 live
//                     boards are in (ready-06b8), which every browser renders as
//                     [encrypted] and, before this item, said nothing about.
//   failed            the board's own fetch threw. NOTHING is known about its
//                     contents, including whether it is empty.
//
// WHAT IS REAL HERE. Every event is Go-signed by the real writer (the two
// generated fixtures), every signature is verified by the real verifier, every
// unwrap runs the real NIP-44 v2 implementation against the real Go wraps, and
// every assertion is on rendered DOM. The relay transport is a function, and the
// extension is the spec-validated signer the rest of this suite uses; the
// unreadable-grant case in particular is driven by a GO-PRODUCED legacy
// raw-payload grant (confidential.fixtures' legacyRawWrapGrant), not by a
// hand-made one, because the whole claim is about what real legacy grants do.

import { beforeEach, describe, expect, it } from "vitest";
import { afterLogin, type BoardDeps, type Identity } from "./main";
import { authTransition } from "./lib/auth";
import { fakeNip44Signer } from "./lib/fakesigner";
import { nip07KeyUnwrapper, neverUnwraps } from "./lib/keyunwrap";
import { PLACEHOLDER } from "./lib/envelope";
import { hexToBytes } from "./lib/sha256";
import { tagValue, type NostrEvent } from "./lib/nostrevent";
import type { FragmentKeys, ParsedFragment } from "./lib/fragment";
import type { PortfolioKeys } from "./lib/portfoliokeys";
import {
  ALPHA_CEK_EPOCH1,
  ALPHA_CEK_EPOCH2,
  ALPHA_COORD,
  BETA_CEK,
  BETA_COORD,
  DELTA_CEK,
  DELTA_COORD,
  GAMMA_COORD,
  VIEWER_PUB,
  snapshot,
} from "./lib/portfolio.fixtures";
import {
  BOARD_COORD as CONF_COORD,
  MEMBER_PUB,
  MEMBER_SEC,
  OWNER_PUB,
  boardEvent as confBoardEvent,
  cardSealedUnderEpoch3,
  cards as confCards,
  grants as confGrants,
  legacyRawWrapGrant,
  rewrappedHexGrant,
} from "./lib/confidential.fixtures";

let root: HTMLElement;

beforeEach(() => {
  document.body.replaceChildren();
  root = document.createElement("div");
  root.id = "app";
  document.body.append(root);
  localStorage.clear();
  sessionStorage.clear();
});

const cek = (hex: string, epoch: number) => ({ epoch, key: hexToBytes(hex) });

/** What `rd board --portfolio --with-key` gathers for this viewer: alpha (both
 * epochs), beta, delta. NOT gamma — the viewer holds no key for it. */
function portfolioKeys(): PortfolioKeys {
  return new Map<string, FragmentKeys>([
    [ALPHA_COORD, { ceks: [cek(ALPHA_CEK_EPOCH1, 1), cek(ALPHA_CEK_EPOCH2, 2)] }],
    [BETA_COORD, { ceks: [cek(BETA_CEK, 1)] }],
    [DELTA_COORD, { ceks: [cek(DELTA_CEK, 1)] }],
  ]);
}

const portfolioFragment = (keys?: PortfolioKeys): ParsedFragment => ({
  kind: "portfolio",
  relays: ["wss://relay.test"],
  viewer: VIEWER_PUB,
  keys,
});

const linkIdentity = (): Identity => ({
  pubkey: VIEWER_PUB,
  auth: authTransition({ type: "login", method: "readOnly" }),
});

const signingIdentity = (pubkey: string): Identity => ({
  pubkey,
  auth: authTransition({ type: "login", method: "extension" }),
});

/** A link session: no extension anywhere, so every key it has arrived in the
 * URL. `failCoord` makes THAT ONE board's per-board REQ throw, which is the
 * production shape of "this board did not load" — the fetch in loadBoardItems'
 * try block is the only thing that changes. */
function linkDeps(failCoord?: string): BoardDeps {
  return {
    loadRelays: async () => ["wss://relay.test"],
    fetchEvents: async (_relays, filter) => {
      const scoped = filter["#a"];
      if (failCoord && Array.isArray(scoped) && scoped.includes(failCoord)) {
        throw new Error("relay closed the subscription");
      }
      return snapshot;
    },
    keyUnwrapper: () => neverUnwraps,
  };
}

/** A signing session over a ONE-BOARD confidential snapshot, with the real
 * NIP-44 unwrapper wired to `secretHex` — i.e. a browser with an extension. */
function confDeps(events: NostrEvent[], secretHex: string): BoardDeps {
  return {
    loadRelays: async () => ["wss://relay.test"],
    fetchEvents: async () => events,
    keyUnwrapper: () => nip07KeyUnwrapper(fakeNip44Signer(secretHex)),
  };
}

const confFragment: ParsedFragment = { kind: "board", board: CONF_COORD, relays: ["wss://relay.test"] };

/** The left-tree node for one board, with everything it says about it. */
function boardNode(coord: string): { state: string | null; count: number; text: string } {
  const node = root.querySelector(`.node[data-board-coord="${coord}"]`);
  if (!node) throw new Error(`no tree node for ${coord}`);
  return {
    state: node.getAttribute("data-board-state"),
    count: Number(node.querySelector(".ct")?.textContent ?? "NaN"),
    text: node.textContent ?? "",
  };
}

/** The status row for one board, or null when the page claims nothing about it.
 * Keyed by coordinate exactly as the tree node is, so a row can never be read
 * against the wrong board. */
function statusRow(coord: string): string | null {
  const row = root.querySelector(`.board-status-row[data-board-coord="${coord}"]`);
  return row ? (row.textContent ?? "") : null;
}

const renderedIds = (): string[] =>
  [...root.querySelectorAll(".card[data-id]")].map((c) => c.querySelector(".card-id")?.textContent?.trim() ?? "");

const pageText = () => root.textContent ?? "";

// ---------------------------------------------------------------------------
// A BOARD THAT DID NOT LOAD SAYS SO — it does not vanish, and its zero is not
// presented as a count of its work.
// ---------------------------------------------------------------------------
describe("a board whose own fetch fails is REPORTED, not silently emptied", () => {
  it("keeps its node, marks it, and says why — while the other boards render", async () => {
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), linkDeps(BETA_COORD));

    const beta = boardNode(BETA_COORD);
    expect(beta.state, "the failed board is not marked as failed").toBe("failed");
    expect(beta.count, "a board nothing is known about must not claim items").toBe(0);
    expect(beta.text).toContain("did not load");

    const row = statusRow(BETA_COORD);
    expect(row, "the failed board got no status row at all").not.toBeNull();
    expect(row).toContain("Beta Board");
    expect(row).toContain("did not load");
    // The page must not describe the board's contents — it read none of them.
    expect(row).toContain("not a count of its work");

    // THE OTHER BOARDS ARE UNAFFECTED, which is what makes this a per-board
    // statement rather than a page-wide failure: alpha's work is still here.
    expect(renderedIds()).toContain("alpha-001");
    expect(boardNode(ALPHA_COORD).state).toBe("open");
    expect(statusRow(ALPHA_COORD), "an OPEN board must not collect a status row").toBeNull();
  });

  it("ANTI-TAUTOLOGY: the same board with a working fetch is open, with no row", async () => {
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), linkDeps());

    const beta = boardNode(BETA_COORD);
    expect(beta.state).toBe("open");
    expect(beta.count, "beta's one card did not reach the tree count").toBe(1);
    expect(beta.text).not.toContain("did not load");
    expect(statusRow(BETA_COORD)).toBeNull();
    expect(renderedIds()).toContain("beta-001");
  });
});

// ---------------------------------------------------------------------------
// A BOARD THIS SESSION HOLDS NO KEY FOR SAYS SO, BY NAME.
// ---------------------------------------------------------------------------
describe("a sealed board is named, and the boards that opened are not", () => {
  it("gamma is marked sealed and told what would fix it; alpha/beta/delta are open", async () => {
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), linkDeps());

    expect(boardNode(GAMMA_COORD).state).toBe("sealed");
    const row = statusRow(GAMMA_COORD);
    expect(row).not.toBeNull();
    expect(row).toContain("Gamma Board");
    expect(row).toContain(PLACEHOLDER);
    // A LINK session cannot unwrap anything, so it must not be told to go and
    // collect a grant — the honest fix is a link minted with this board's key.
    expect(row).toContain("this link carries no read key");
    expect(row).not.toContain("rd confidential rewrap");

    for (const open of [ALPHA_COORD, BETA_COORD, DELTA_COORD]) {
      expect(boardNode(open).state, `board ${open}`).toBe("open");
      expect(statusRow(open), `board ${open} collected a status row`).toBeNull();
    }
  });

  it("ANTI-TAUTOLOGY: hand the link gamma's key and gamma stops being called sealed", async () => {
    // The same fixture, the same load, one added key. GAMMA_CEK is byte-identical
    // to ALPHA_CEK_EPOCH1 by construction (the generator's CROSS-BOARD TRAP), so
    // this changes only WHICH COORDINATE the key is filed under.
    const keys: PortfolioKeys = new Map([
      ...portfolioKeys(),
      [GAMMA_COORD, { ceks: [cek(ALPHA_CEK_EPOCH1, 1)] }],
    ]);
    await afterLogin(root, linkIdentity(), portfolioFragment(keys), linkDeps());

    expect(boardNode(GAMMA_COORD).state).toBe("open");
    expect(statusRow(GAMMA_COORD)).toBeNull();
    expect(pageText()).toContain("GAMMA SECRET TITLE");
  });

  it("a board with NO state to report renders no status list at all", async () => {
    // Every board open: the ordinary portfolio must not grow a paragraph.
    const keys: PortfolioKeys = new Map([
      ...portfolioKeys(),
      [GAMMA_COORD, { ceks: [cek(ALPHA_CEK_EPOCH1, 1)] }],
    ]);
    await afterLogin(root, linkIdentity(), portfolioFragment(keys), linkDeps());
    expect(root.querySelector(".board-status")).toBeNull();
  });
});

// ---------------------------------------------------------------------------
// THE STATE THE LIVE PORTFOLIO IS ACTUALLY IN (ready-06b8): a grant that names
// this key, carrying a key this browser cannot open.
// ---------------------------------------------------------------------------
describe("a grant this browser cannot open is reported as such, not as 'ask for a grant'", () => {
  /** The member's ONLY grant on this board is the Go-produced legacy raw-payload
   * one. Every other grant addressed to the member is removed, so no other key
   * can mask the outcome; the owner's own epoch-1 grant stays, because it is what
   * establishes that the board is confidential at all. */
  const withMemberGrant = (memberGrant: NostrEvent): NostrEvent[] => [
    confBoardEvent,
    ...confGrants.filter((g) => tagValue(g, "p") !== MEMBER_PUB && tagValue(g, "p") === OWNER_PUB),
    memberGrant,
    ...confCards,
    cardSealedUnderEpoch3,
  ];

  it("names the board, says the browser could not open the key, and names the repair", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), confFragment, confDeps(withMemberGrant(legacyRawWrapGrant), MEMBER_SEC));

    expect(boardNode(CONF_COORD).state).toBe("unreadable-grant");
    expect(boardNode(CONF_COORD).text).toContain("key unreadable");

    const row = statusRow(CONF_COORD);
    expect(row).not.toBeNull();
    expect(row).toContain("Confidential Board");
    expect(row).toContain("DID grant this key access");
    expect(row).toContain("rd confidential rewrap");
    // It must NOT be the "nobody granted you" sentence — that would send the
    // reader to ask for something they already have.
    expect(row).not.toContain("no owner-signed grant naming this key");

    // And the reason this matters at all: the board renders, and it renders
    // sealed.
    expect(pageText()).toContain(PLACEHOLDER);
  });

  it("ANTI-TAUTOLOGY: the RE-WRAPPED grant, same board, same reader, is open", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), confFragment, confDeps(withMemberGrant(rewrappedHexGrant), MEMBER_SEC));

    expect(boardNode(CONF_COORD).state).toBe("open");
    expect(statusRow(CONF_COORD)).toBeNull();
  });

  it("a reader NO grant names is told the other thing — the two states are distinct", async () => {
    // Same board, same snapshot, but the member's grant is not there at all. The
    // page must say "no grant naming this key reached this page", never the
    // rewrap sentence: this reader has nothing to re-wrap.
    const noMemberGrant = [
      confBoardEvent,
      ...confGrants.filter((g) => tagValue(g, "p") === OWNER_PUB),
      ...confCards,
    ];
    await afterLogin(root, signingIdentity(MEMBER_PUB), confFragment, confDeps(noMemberGrant, MEMBER_SEC));

    expect(boardNode(CONF_COORD).state).toBe("sealed");
    const row = statusRow(CONF_COORD);
    expect(row).toContain("no owner-signed grant naming this key");
    expect(row).toContain("Ask this board's owner");
    expect(row).not.toContain("rd confidential rewrap");
  });
});

// ---------------------------------------------------------------------------
// A BOARD WHOSE CUTOVER CANNOT BE ESTABLISHED IS SHOWING A SHORT COUNT, AND
// SAYS SO — even to a reader who holds its key.
//
// THIS IS THE LIVE DEFECT, not a constructed one. Measured against
// wss://relay.3dl.network on 2026-07-30: the `ready` board served 536 distinct
// kind-30302 cards, 369 of them enc-marked; `rd list --json --all` in that
// project reported 536; the page's node for it read "open" with a count of 369.
// The 167 plaintext cards were withheld by the fold (correctly — the cutover was
// unestablished), and the node said nothing, so the number beside the project
// was 31% short with no indication that it was.
// ---------------------------------------------------------------------------
describe("holding the key is not the same as seeing the board", () => {
  /** The owner's epoch-1 grants removed — the shape ready-f6b's witness C fires
   * on, and the shape the live relay is in for a board that rotated before
   * §16.10 gave each epoch its own addressable slot. Every remaining grant names
   * epoch 2, so the earliest grant is provably not in the answer and the cutover
   * cannot be believed. */
  const noEpoch1 = (): NostrEvent[] => [
    confBoardEvent,
    ...confGrants.filter((g) => tagValue(g, "cek_epoch") !== "1"),
    ...confCards,
  ];

  it("a KEYHOLDER on an unestablished cutover is told the count is short", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), confFragment, confDeps(noEpoch1(), MEMBER_SEC));

    const node = boardNode(CONF_COORD);
    expect(node.state, "a board withholding cards was reported as open").toBe("withholding");
    expect(node.text).toContain("items withheld");

    const row = statusRow(CONF_COORD);
    expect(row).not.toBeNull();
    expect(row).toContain("Confidential Board");
    expect(row).toContain("SHORT");
    expect(row).toContain("rd list");

    // THE KEY IS GENUINELY HELD — this is not the sealed case wearing a new
    // name. The epoch-2 card renders in plaintext on the same load.
    expect(pageText()).toContain("Sealed after the rotation");
  });

  it("ANTI-TAUTOLOGY: put the epoch-1 grants back and the same board is open", async () => {
    await afterLogin(root, signingIdentity(MEMBER_PUB), confFragment, confDeps([confBoardEvent, ...confGrants, ...confCards], MEMBER_SEC));

    expect(boardNode(CONF_COORD).state).toBe("open");
    expect(statusRow(CONF_COORD)).toBeNull();
    expect(pageText()).toContain("Sealed after the rotation");
  });
});

// ---------------------------------------------------------------------------
// SWITCHING BETWEEN BOARDS, AND THE COUNTS THE NODES CARRY.
// ---------------------------------------------------------------------------
describe("every project's work in one browser, and one project at a time", () => {
  it("each board's node counts that board's items and nobody else's", async () => {
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), linkDeps());

    // The fixture's per-board card counts, which is the same question `rd list
    // --json` answers per project: 2 on alpha, 1 on beta, 1 on delta.
    expect(boardNode(ALPHA_COORD).count).toBe(2);
    expect(boardNode(BETA_COORD).count).toBe(1);
    expect(boardNode(DELTA_COORD).count).toBe(1);
    // Gamma's count is whatever survived its own quarantine — asserted as
    // "not zero and not silently absent", because a board with an unestablished
    // cutover legitimately withholds cards and the page says so separately.
    expect(boardNode(GAMMA_COORD).count).toBeGreaterThan(0);
  });

  it("clicking a board scopes the view to that board", async () => {
    await afterLogin(root, linkIdentity(), portfolioFragment(portfolioKeys()), linkDeps());
    expect(renderedIds()).toContain("alpha-001");
    expect(renderedIds()).toContain("beta-001");

    const alphaNode = root.querySelector<HTMLElement>(`.node[data-board-coord="${ALPHA_COORD}"]`)!;
    alphaNode.click();

    const shown = renderedIds();
    expect(shown, "alpha's own cards left the view when alpha was selected").toContain("alpha-001");
    for (const id of shown) expect(id.startsWith("alpha-"), `${id} is not alpha's`).toBe(true);

    // ...and back: the board list is a switch, not a one-way filter.
    root.querySelector<HTMLElement>(`.node[data-board-coord="${ALPHA_COORD}"]`)!.click();
    expect(renderedIds()).toContain("beta-001");
  });
});
