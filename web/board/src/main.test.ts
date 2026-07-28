// @vitest-environment jsdom
//
// INTEGRATION test for main.ts (ready-9b4). Everything below the DOM is REAL:
// the real relay.ts client parses the real NIP-01 EVENT/EOSE frames, the real
// boarddiscovery.ts filters them, and the real nostrevent.ts/secp256k1.ts
// recompute each event id and check its BIP-340 signature. The only thing
// faked is the WebSocket itself, injected through relay.ts's existing
// webSocketCtor seam — so this composes relay -> verify -> render with real
// Go-signed data, which is precisely the composition nothing tested before.
//
// WHY THIS FILE EXISTS: main.ts had no test at all, so ready-dbf done
// condition 4 ("a kind-30301 event with a tampered signature, or authored by
// a pubkey that is not the follow target, must not appear in the board list")
// was enforced only inside the library. A one-line edit in main.ts — render
// the raw relay snapshot instead of discoverOwnerBoards' output — disabled the
// whole property with 88/88 vitest still green. The tests below go red on
// exactly that edit.
//
// The fake relay is HOSTILE by construction: it ignores the `authors` filter
// it is sent and replies with every scripted event, which is what an
// untrusted relay is free to do. Client-side verification, not relay
// courtesy, is what has to keep the forged events out of the DOM.
//
// TWO AXES, NOT ONE. An earlier revision of this file drove every case with a
// single read-only-npub identity whose pubkey happened to be the fixture
// boards' author. Two whole classes of rewrite therefore stayed green:
//
//   (a) SIGNING PATH. `canSign(identity.auth) ? <raw snapshot> : <verified>`
//       in afterLogin bypassed verification for exactly the identity the
//       product's PRIMARY login control produces — renderLogin's first button
//       is the NIP-07 extension, and it calls afterLogin with a signing
//       identity. Nothing in the suite ever constructed one. Every relay-
//       driven case below is now run over BOTH shapes (IDENTITIES), and the
//       canSign-fixture guard test pins that they really are opposite sides of
//       canSign rather than two spellings of read-only.
//   (b) FOLLOW TARGET. identity.pubkey was OWNER in every case, i.e. always
//       equal to the author of the genuine fixtures, so "the follow target is
//       the LOGGED-IN key" was never distinguishable from "the follow target
//       is whatever the relay served". The STRANGER case pins it: a key that
//       owns none of the served boards must see none of them, even though
//       three of those boards carry perfectly valid signatures.
//
// Also newly covered here: the fragment.kind === "claim" branch, which returns
// before any relay query and is the other place afterLogin renders off the
// auth state (renderAwaitingAuthorization reads identity.auth.readOnly).
import { beforeEach, describe, expect, it, vi } from "vitest";
import { afterLogin, main, type BoardDeps, type Identity } from "./main";
import { authTransition, canSign } from "./lib/auth";
import { fetchEventsFromRelays, type NostrFilter } from "./lib/relay";
import { boardCoord } from "./lib/boarddiscovery";
import { encodeNpub } from "./lib/npub";
import type { NostrEvent } from "./lib/nostrevent";
import {
  OWNER,
  OTHER,
  alpha,
  beta,
  gamma,
  alphaDup,
  delta,
  forgedSig,
  impersonator,
} from "./lib/boardevents.fixtures";

// Two DISTINCT relay URLs, so an assertion on which one the fake WebSocket
// was constructed with actually distinguishes the two relay-set sources
// main.ts has: relays carried in a #board=…&relays=… link, versus relays
// loaded from the same-origin relays.json config.
const LINK_RELAY = "wss://link.relay.test";
const CONFIG_RELAY = "wss://config.relay.test";

/**
 * A 32-byte hex pubkey that is NEITHER OWNER nor OTHER — a logged-in user who
 * owns none of the boards the hostile relay serves. Unlike the board-event
 * fixtures this needs no signature: it is only ever an identity (a REQ
 * `authors` entry and a discovery follow target), never a signed event.
 */
const STRANGER = "3a7d1c05e2b94f6810d43fbc27ae59016cb8f2d4739e0a5c6182bd4e90f37c11";

/**
 * The snapshot the hostile relay serves. Deliberately ordered forged-first so
 * a passing result cannot depend on the genuine events arriving first, and it
 * mixes all four rejection reasons the product must survive:
 *   forgedSig    — owner's board, signature corrupted after signing
 *   impersonator — signed by OTHER, pubkey field relabeled to OWNER
 *   delta        — validly signed, but by a pubkey that is not the follow target
 *   alphaDup     — genuine duplicate of an existing coordinate (must collapse)
 */
const HOSTILE_SNAPSHOT: NostrEvent[] = [forgedSig, impersonator, alpha, delta, beta, alphaDup, gamma];

/** The three boards OWNER genuinely published, in the order main.ts must
 * render them (discoverOwnerBoards sorts by coordinate). */
const OWNERS_GENUINE_BOARDS = [
  { title: "Alpha Board", coord: boardCoord(OWNER, "alpha") },
  { title: "Beta Board", coord: boardCoord(OWNER, "beta") },
  { title: "Gamma Board", coord: boardCoord(OWNER, "gamma") },
];

/** Board coordinates and titles that must NEVER be rendered from the snapshot
 * above. Each string is unambiguous — a full "30301:<pubkey>:<d>" coordinate
 * or a full title — so these assertions cannot be satisfied by accident, and
 * cannot false-positive against the hex/bech32 alphabets used elsewhere in
 * the page. */
const MUST_NOT_RENDER = [
  boardCoord(OWNER, "evil"),
  "Evil Board",
  boardCoord(OWNER, "impersonated"),
  "Impersonated Board",
  boardCoord(OTHER, "delta"),
  "Delta Board (foreign owner)",
];

/**
 * The two identity shapes renderLogin can hand afterLogin. `signing` is the
 * expected canSign() verdict, asserted (not assumed) by the guard test below.
 *
 * The NIP-07 shape is the product's primary login path — the extension button
 * is renderLogin's first control — and until it appeared here, every
 * signing-only branch of afterLogin was invisible to the suite.
 */
const IDENTITIES: { name: string; signing: boolean; identity: Identity }[] = [
  {
    name: "read-only npub",
    signing: false,
    identity: {
      pubkey: OWNER,
      auth: authTransition({ type: "login", method: "readOnly" }),
    },
  },
  {
    name: "NIP-07 extension (signing)",
    signing: true,
    identity: {
      pubkey: OWNER,
      auth: authTransition({ type: "login", method: "extension" }),
    },
  },
];

/** A WebSocket stand-in that replays a scripted event list then EOSEs. The
 * onopen/onmessage callbacks are deferred to a microtask because relay.ts
 * assigns its handlers immediately AFTER `new WS(url)` returns. */
class FakeRelayWebSocket {
  static served: NostrEvent[] = [];
  static urls: string[] = [];
  static reset(served: NostrEvent[]): void {
    FakeRelayWebSocket.served = served;
    FakeRelayWebSocket.urls = [];
  }

  url: string;
  onopen: (() => void) | null = null;
  onerror: ((ev?: unknown) => void) | null = null;
  onclose: ((ev?: unknown) => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;

  constructor(url: string) {
    this.url = url;
    FakeRelayWebSocket.urls.push(url);
    queueMicrotask(() => this.onopen?.());
  }

  send(data: string): void {
    const [, subId] = JSON.parse(data) as [string, string, unknown];
    queueMicrotask(() => {
      // Note: the scripted events are replayed WITHOUT applying the REQ
      // filter — a hostile relay is under no obligation to honour it.
      for (const e of FakeRelayWebSocket.served) {
        this.onmessage?.({ data: JSON.stringify(["EVENT", subId, e]) });
      }
      this.onmessage?.({ data: JSON.stringify(["EOSE", subId]) });
    });
  }

  close(): void {
    /* nothing to tear down */
  }
}

interface Capture {
  /** The unverified event snapshot main.ts received from the relay layer.
   * Asserting on this is what stops these tests from passing vacuously: it
   * proves the forged events really did reach main.ts and were dropped
   * downstream, rather than never having been served at all. */
  snapshot: NostrEvent[];
  /** Every filter main.ts asked the relay layer for, in call order. Pins which
   * key drives the REQ — without it, main.ts could query for `authors: []`
   * (i.e. "send me everything") and no assertion would notice, because the
   * fake relay ignores the filter anyway. */
  filters: NostrFilter[];
}

function injectedDeps(served: NostrEvent[], capture: Capture): BoardDeps {
  FakeRelayWebSocket.reset(served);
  return {
    loadRelays: async () => [CONFIG_RELAY],
    fetchEvents: async (relays, filter, opts) => {
      capture.filters.push(filter);
      const events = await fetchEventsFromRelays(relays, filter, {
        ...opts,
        webSocketCtor: FakeRelayWebSocket as unknown as typeof WebSocket,
        retries: 0,
        timeoutMs: 2000,
      });
      // ready-bad: main.ts now makes a SECOND round of fetches (the per-board
      // item query). Record only the FIRST snapshot -- the board-discovery one
      // -- so every anti-vacuity assertion below keeps asserting about the
      // unverified board snapshot it was written against, rather than silently
      // re-pointing at the item fetch.
      if (capture.snapshot.length === 0) capture.snapshot = events;
      return events;
    },
  };
}

/**
 * expectItemFetchesScopedToRenderedBoards asserts that every query AFTER the
 * board-discovery one is a per-board item fetch (ready-bad) whose "#a" scope
 * names a board that actually survived signature verification and reached the
 * DOM. This is deliberately stronger than asserting an exact filter list: a
 * forged or impersonated kind-30301 must not be able to induce an item fetch
 * for a board it invented, which an exact-match assertion would not notice if
 * the fetch list happened to be rewritten.
 */
function expectItemFetchesScopedToRenderedBoards(capture: Capture, root: HTMLElement): void {
  const rendered = new Set(renderedBoards(root).map((b) => b.coord));
  for (const f of capture.filters.slice(1)) {
    const scope = f["#a"];
    expect(scope, `item fetch ${JSON.stringify(f)} is not #a-scoped`).toBeDefined();
    for (const coord of scope ?? []) {
      expect(rendered, `item fetch scoped to unrendered board ${coord}`).toContain(coord);
    }
  }
}

/** Every board the page actually rendered, as {title, coord} in DOM order. */
function renderedBoards(root: HTMLElement): { title: string; coord: string }[] {
  return Array.from(root.querySelectorAll("ul.board-list > li")).map((li) => ({
    title: li.querySelector(".board-title")?.textContent ?? "",
    coord: li.querySelector(".board-coord")?.textContent ?? "",
  }));
}

/** Asserts no forged/foreign identifier appears ANYWHERE under root — not
 * just inside the .board-list projection renderedBoards() reads. Checked
 * against both the rendered text and the serialized markup so a forged value
 * smuggled into an attribute (title=, class=, …) is caught too. */
function expectNoForgedContent(root: HTMLElement): void {
  const text = root.textContent ?? "";
  const html = root.innerHTML;
  for (const forbidden of MUST_NOT_RENDER) {
    expect(text).not.toContain(forbidden);
    expect(html).not.toContain(forbidden);
  }
}

/** Same, for OWNER's GENUINE boards: used by the cases where the logged-in key
 * is not OWNER, so even a perfectly-signed board of someone else's must stay
 * out of the page. */
function expectNoneOfOwnersBoardsRendered(root: HTMLElement): void {
  const text = root.textContent ?? "";
  const html = root.innerHTML;
  for (const b of OWNERS_GENUINE_BOARDS) {
    for (const forbidden of [b.title, b.coord]) {
      expect(text).not.toContain(forbidden);
      expect(html).not.toContain(forbidden);
    }
  }
}

/**
 * ANTI-VACUITY GUARD. Every "forged content must be absent from the DOM"
 * assertion in this file is a negative one, and a negative assertion is
 * satisfied by an empty pipeline. This pins the other side: the UNVERIFIED
 * snapshot main.ts actually received really did carry all three events that
 * must be rejected. Each fixture is named individually rather than compared
 * against HOSTILE_SNAPSHOT — comparing the capture to the constant that
 * produced it is that constant checked against itself, and would stay green
 * if the forged events were quietly dropped from the served list.
 */
function expectSnapshotCarriedTheForgedEvents(capture: Capture): void {
  const ids = capture.snapshot.map((e) => e.id);
  expect(ids).toContain(forgedSig.id); // tampered signature
  expect(ids).toContain(impersonator.id); // pubkey field relabeled to OWNER
  expect(ids).toContain(delta.id); // validly signed by a non-follow-target
}

/** The exact identity line afterLogin must render for a given identity. The
 * " (read-only)" suffix is the ONE canSign-driven render afterLogin has today
 * outside the discovery path, so asserting it by equality (not `toContain`)
 * makes the signing/read-only distinction observable in every case below. */
function expectedIdentityLine(pubkey: string, signing: boolean): string {
  return `Logged in as ${encodeNpub(pubkey)}${signing ? "" : " (read-only)"}`;
}

let root: HTMLElement;
let capture: Capture;

beforeEach(() => {
  document.body.replaceChildren();
  root = document.createElement("div");
  root.id = "app";
  document.body.append(root);
  capture = { snapshot: [], filters: [] };
});

describe("identity fixtures", () => {
  it("GUARD: the two identity shapes land on OPPOSITE sides of canSign", () => {
    // Without this, the parametrization below could silently degrade into the
    // same read-only identity run twice — which is exactly the blind spot the
    // signing-path variants exist to remove — and every canSign-gated
    // mutation would go back to shipping green.
    expect(IDENTITIES.map((i) => [i.name, i.signing, canSign(i.identity.auth)])).toEqual([
      ["read-only npub", false, false],
      ["NIP-07 extension (signing)", true, true],
    ]);
    expect(IDENTITIES.every((i) => i.identity.auth.loggedIn)).toBe(true);
  });
});

describe.each(IDENTITIES)("afterLogin as $name", ({ signing, identity }) => {
  describe("own-boards discovery (fragment.kind === 'none')", () => {
    it("SECURITY: renders exactly the owner's genuine boards from a snapshot that also carries forged, impersonated and foreign events", async () => {
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

      await afterLogin(root, identity, { kind: "none" }, deps);

      // 1. The hostile relay really was queried, via the config relay set (a
      //    bare visit carries no link relays), asking for the LOGGED-IN key's
      //    kind-30301 events.
      expect([...new Set(FakeRelayWebSocket.urls)]).toEqual([CONFIG_RELAY]);
      expect(capture.filters[0]).toEqual({ kinds: [30301], authors: [identity.pubkey] });
      // ready-bad: every later query is a per-board item fetch, and each must be
      // #a-scoped to a board that SURVIVED verification. Stronger than the old
      // exact-match: it also forbids an item fetch leaking to a board the
      // forged/impersonated events tried to introduce.
      expectItemFetchesScopedToRenderedBoards(capture, root);

      // 1b. ANTI-VACUITY. The unverified snapshot main.ts received contained all
      //     seven events, INCLUDING the three that must be rejected — each named
      //     individually rather than compared against HOSTILE_SNAPSHOT, which
      //     would only be that constant checked against itself and would stay
      //     green if the forged events were quietly dropped from the harness.
      //     Without this, every "forged content is absent" assertion below could
      //     be satisfied by a pipeline that was never fed anything forged.
      expect(capture.snapshot.map((e) => e.id).sort()).toEqual(
        [alpha.id, beta.id, gamma.id, alphaDup.id, delta.id, forgedSig.id, impersonator.id].sort(),
      );

      // 2. The rendered list is EXACTLY the three genuine boards of OWNER —
      //    whole-list equality in both directions, so neither an extra forged
      //    row nor a missing genuine row can slip through, and "alpha" carries
      //    the first occurrence's title rather than alphaDup's. This holds for
      //    a signing identity too: verification is NOT gated on canSign.
      expect(renderedBoards(root)).toEqual(OWNERS_GENUINE_BOARDS);

      // 3. Nothing forged leaked anywhere else in the subtree either.
      expectNoForgedContent(root);

      // 4. The identity bar reflects this identity's signing capability
      //    exactly — the assertion that makes the two parametrized runs
      //    genuinely different renders rather than two identical ones.
      expect(root.querySelector("p.identity")?.textContent).toBe(
        expectedIdentityLine(identity.pubkey, signing),
      );

      // 5. The page reached its terminal state: the connecting indicator was
      //    removed, so this is a completed render and not a mid-flight snapshot.
      expect(root.querySelector(".connecting")).toBeNull();
    });

    it("SECURITY: renders no boards at all when every event the relay serves is forged, impersonated or foreign", async () => {
      const deps = injectedDeps([forgedSig, impersonator, delta], capture);

      await afterLogin(root, identity, { kind: "none" }, deps);

      expect(capture.snapshot.map((e) => e.id).sort()).toEqual(
        [forgedSig.id, impersonator.id, delta.id].sort(),
      );
      expect(renderedBoards(root)).toEqual([]);
      expect(root.textContent).toContain("No boards found.");
      expectNoForgedContent(root);
      expect(root.querySelector("p.identity")?.textContent).toBe(
        expectedIdentityLine(identity.pubkey, signing),
      );
    });
  });

  describe("single-board link (fragment.kind === 'board')", () => {
    it("renders the genuine board the coordinate names, querying the relays carried in the link rather than the config set", async () => {
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

      await afterLogin(
        root,
        identity,
        { kind: "board", board: boardCoord(OWNER, "alpha"), relays: [LINK_RELAY] },
        deps,
      );

      // LINK_RELAY and CONFIG_RELAY are different URLs, so this distinguishes
      // the two relay sources — the link's list wins when it is non-empty.
      expect([...new Set(FakeRelayWebSocket.urls)]).toEqual([LINK_RELAY]);
      // The link's coordinate, not the viewer's key, names the author here.
      expect(capture.filters[0]).toEqual({ kinds: [30301], authors: [OWNER] });
      expectItemFetchesScopedToRenderedBoards(capture, root);
      expectSnapshotCarriedTheForgedEvents(capture);
      expect(renderedBoards(root)).toEqual([
        { title: "Alpha Board", coord: boardCoord(OWNER, "alpha") },
      ]);
      expectNoForgedContent(root);
    });

    it("falls back to the config relay set when the link carries no relays", async () => {
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

      await afterLogin(
        root,
        identity,
        { kind: "board", board: boardCoord(OWNER, "beta"), relays: [] },
        deps,
      );

      expect([...new Set(FakeRelayWebSocket.urls)]).toEqual([CONFIG_RELAY]);
      expectSnapshotCarriedTheForgedEvents(capture);
      expect(renderedBoards(root)).toEqual([
        { title: "Beta Board", coord: boardCoord(OWNER, "beta") },
      ]);
      expectNoForgedContent(root);
    });

    it("SECURITY: renders nothing when the event matching the requested coordinate has a forged signature", async () => {
      // The link asks for OWNER's "evil" board. The relay HAS an event for
      // exactly that coordinate — forgedSig — so the d-filter does not save us
      // here; only the signature check does.
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

      await afterLogin(
        root,
        identity,
        { kind: "board", board: boardCoord(OWNER, "evil"), relays: [LINK_RELAY] },
        deps,
      );

      expectSnapshotCarriedTheForgedEvents(capture);
      expect(renderedBoards(root)).toEqual([]);
      expect(root.textContent).toContain("No boards found.");
      expectNoForgedContent(root);
    });
  });

  describe("claim link (fragment.kind === 'claim')", () => {
    it("shows the awaiting-authorization notice and contacts no relay at all", async () => {
      // A coordinate DELIBERATELY absent from the fixtures: the awaiting-
      // authorization copy legitimately names the invited board, so reusing a
      // fixture coordinate here would make "none of OWNER's genuine boards
      // were rendered" unassertable.
      const board = boardCoord(OWNER, "invited");
      // The deps are wired to a live hostile relay on purpose: if the claim
      // branch ever stopped returning early, this snapshot is what would get
      // rendered, and the assertions below would catch it.
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

      await afterLogin(
        root,
        identity,
        {
          kind: "claim",
          payload: {
            v: 3,
            board,
            relays: [LINK_RELAY],
            claim: "claim-nonce",
            iat: 1700000000,
            exp: 1700003600,
            iss: OWNER,
          },
        },
        deps,
      );

      // A claim link is an invitation, not an authorization: nothing may be
      // fetched or rendered until the owner grants the key.
      expect([...new Set(FakeRelayWebSocket.urls)]).toEqual([]);
      expect(capture.filters).toEqual([]);
      expect(renderedBoards(root)).toEqual([]);
      expectNoneOfOwnersBoardsRendered(root);
      expectNoForgedContent(root);

      // renderAwaitingAuthorization reads identity.auth.readOnly directly, so
      // its copy is the second place the signing/read-only distinction shows
      // up in the DOM. Equality, so neither suffix can appear on the wrong one.
      expect(root.querySelector("section.awaiting-authorization > h2")?.textContent).toBe(
        "Awaiting authorization",
      );
      expect(root.querySelector("section.awaiting-authorization > p")?.textContent).toBe(
        `You are logged in as ${encodeNpub(identity.pubkey)}${signing ? "" : " (read-only)"}. ` +
          `Ask the owner of board ${board} to grant this key access.`,
      );
    });
  });

  describe("relay failure surfaces instead of an empty board list", () => {
    it("shows the relay error and renders no board list when no relay can be reached", async () => {
      const deps: BoardDeps = {
        loadRelays: async () => [CONFIG_RELAY],
        fetchEvents: async () => {
          throw new Error("relay: could not reach any relay: socket error");
        },
      };

      await afterLogin(root, identity, { kind: "none" }, deps);

      expect(renderedBoards(root)).toEqual([]);
      expect(root.textContent).toContain("could not reach any relay");
      // An unreachable relay must NOT be indistinguishable from "you own no
      // boards" — the empty-list copy is reserved for a successful query.
      expect(root.textContent).not.toContain("No boards found.");
    });
  });
});

describe("afterLogin — the follow target is the logged-in key, not the relay's choice", () => {
  it("SECURITY: a signing identity that authored none of the served boards sees none of them, however valid their signatures", async () => {
    // STRANGER logs in with a NIP-07 extension and owns nothing. The hostile
    // relay answers with OWNER's three GENUINE, correctly-signed boards plus
    // OTHER's genuine delta. Signature verification alone does not reject any
    // of those four — only "author must equal the logged-in key" does. Every
    // other case in this file uses identity.pubkey === OWNER, where that
    // distinction is invisible: main.ts could take its follow target from the
    // served events (e.g. events[0].pubkey) and still render the right rows.
    const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);
    const stranger: Identity = {
      pubkey: STRANGER,
      auth: authTransition({ type: "login", method: "extension" }),
    };
    expect(canSign(stranger.auth)).toBe(true);

    await afterLogin(root, stranger, { kind: "none" }, deps);

    // The REQ carried STRANGER's key — main.ts asked for its own boards.
    expect(capture.filters[0]).toEqual({ kinds: [30301], authors: [STRANGER] });
    expectItemFetchesScopedToRenderedBoards(capture, root);
    // ANTI-VACUITY: the relay ignored that filter and served everything, so
    // the genuine boards really were in front of main.ts when it rendered.
    expect(capture.snapshot.map((e) => e.id).sort()).toEqual(
      [alpha.id, beta.id, gamma.id, alphaDup.id, delta.id, forgedSig.id, impersonator.id].sort(),
    );

    expect(renderedBoards(root)).toEqual([]);
    expect(root.textContent).toContain("No boards found.");
    expectNoneOfOwnersBoardsRendered(root);
    expectNoForgedContent(root);
    expect(root.querySelector("p.identity")?.textContent).toBe(
      expectedIdentityLine(STRANGER, true),
    );
  });
});

// ready-62d1: a malformed #rd1_ fragment must not take the page down, and must
// still be stripped from the URL. Both used to fail — parseAndStripFragment
// threw before its strip ran, and main() called it at module top level with no
// catch, so NOTHING rendered on what is the first-touch surface for a shared
// invite: no heading, no NIP-07 button, no npub form, no error text. The user
// could not even log in to recover, and the claim-nonce stayed in the address
// bar and history in exactly the case where a token most wants removing.
describe("ready-62d1: malformed board link", () => {
  it("still renders a usable login page, and strips the bad fragment", () => {
    document.body.innerHTML = '<div id="app"></div>';
    const replaceState = vi.fn();
    const origHistory = window.history;
    Object.defineProperty(window, "history", {
      value: { replaceState },
      configurable: true,
      writable: true,
    });
    window.location.hash = "#rd1_!!!!not-base64!!!!";

    try {
      expect(() => main()).not.toThrow();

      const root = document.getElementById("app")!;
      // A login control the user can actually recover through.
      expect(root.querySelector("button")).not.toBeNull();
      // And an honest explanation of what went wrong.
      expect(root.textContent).toContain("not valid");
      // The bad token does not linger in the URL or in history.
      expect(replaceState).toHaveBeenCalled();
    } finally {
      Object.defineProperty(window, "history", {
        value: origHistory,
        configurable: true,
        writable: true,
      });
    }
  });
});
