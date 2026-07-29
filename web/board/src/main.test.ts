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
import { neverUnwraps } from "./lib/keyunwrap";
import {
  makeNip01Relay,
  type Nip01RelayConfig,
  type Nip01RelayHandle,
} from "./lib/nip01relay.fixtures";
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
 * render them (discoverOwnerBoards sorts by coordinate). "alpha"'s title is
 * alphaDup's ("Alpha Board Dup"), not alpha's — alphaDup (created_at
 * 1700000004) is a LATER republish of the same coordinate than alpha
 * (1700000001), and discoverOwnerBoards is latest-wins (ready-a9b), not
 * first-in-snapshot-wins: the winning definition is whichever has the
 * greatest created_at, independent of HOSTILE_SNAPSHOT's array order. */
const OWNERS_GENUINE_BOARDS = [
  { title: "Alpha Board Dup", coord: boardCoord(OWNER, "alpha") },
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
    const [type, subId] = JSON.parse(data) as [string, string, unknown];
    // ready-5c5: answer REQ and ONLY REQ. relay.ts's `until` walk now sends a
    // CLOSE after each page's EOSE, and a stub that replays its script for
    // every frame type answers that CLOSE with another EOSE, which starts
    // another page, forever.
    if (type !== "REQ") return;
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
    // ready-c4b: no key material for these cases. The confidential read path
    // has its own suite (main.confidential.test.ts); here an identity that
    // holds no board key is the right default, and it keeps every assertion
    // below about verification rather than decryption.
    keyUnwrapper: () => neverUnwraps,
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
 * Same seam as injectedDeps, but wired to a relay that HONOURS the REQ filter
 * it is sent (nip01relay.fixtures.ts) instead of one that ignores it.
 *
 * ready-5c5: this is the seam that can tell a correct filter from one that
 * matches nothing. injectedDeps' FakeRelayWebSocket structurally cannot — it
 * replays its whole script for any filter, so every "the page rendered board X"
 * assertion driven by it is really an assertion that main.ts sent SOME filter.
 */
function injectedHonouringDeps(
  config: Nip01RelayConfig,
  capture: Capture,
): { deps: BoardDeps; handle: Nip01RelayHandle } {
  const { ctor, handle } = makeNip01Relay(config);
  return {
    handle,
    deps: {
      keyUnwrapper: () => neverUnwraps,
      loadRelays: async () => [CONFIG_RELAY],
      fetchEvents: async (relays, filter, opts) => {
        capture.filters.push(filter);
        const events = await fetchEventsFromRelays(relays, filter, {
          ...opts,
          webSocketCtor: ctor,
          retries: 0,
          timeoutMs: 2000,
        });
        if (capture.snapshot.length === 0) capture.snapshot = events;
        return events;
      },
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
  // ready-5c5: identified by KIND, not by position. The authority round is no
  // longer always one REQ (a single-board link needs two — see the
  // single-board case), so "everything after the first" would now sweep an
  // authority REQ into this check.
  const itemFetches = capture.filters.filter((f) => f.kinds?.includes(30302));
  for (const f of itemFetches) {
    const scope = f["#a"];
    expect(scope, `item fetch ${JSON.stringify(f)} is not #a-scoped`).toBeDefined();
    for (const coord of scope ?? []) {
      expect(rendered, `item fetch scoped to unrendered board ${coord}`).toContain(coord);
    }
  }
}

/**
 * Every board the page actually rendered, as {title, coord} in DOM order.
 *
 * ready-56b: this used to read `ul.board-list > li` — the raw coordinate dump
 * that sat above the board and printed nine lines of "30301:<64 hex>:<d>". That
 * element is deleted; the verified board set now lives where the design puts
 * it, as the left tree's project nodes. The board's NAME is the rendered text
 * and its coordinate is a data attribute, which is strictly more assertable
 * than before: `expectNoCoordinateDump` below pins that the coordinate is NOT
 * printed as text, and this projection still fails if a board that did not
 * survive verification reaches the DOM.
 */
function renderedBoards(root: HTMLElement): { title: string; coord: string }[] {
  return Array.from(root.querySelectorAll(".left-tree .node[data-board-coord]")).map((node) => ({
    title: node.querySelector(".nm")?.textContent ?? "",
    coord: (node as HTMLElement).dataset.boardCoord ?? "",
  }));
}

/** No board coordinate may appear as rendered TEXT anywhere on the page. This
 * is the assertion the deleted debug list would have failed. */
function expectNoCoordinateDump(root: HTMLElement): void {
  const text = root.textContent ?? "";
  for (const b of renderedBoards(root)) {
    expect(text, `coordinate ${b.coord} is printed as text`).not.toContain(b.coord);
  }
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
      // ready-c4b: the authority REQ carries the 39301 role grants alongside
      // the 30301 boards, because a confidential board's read key rides inside
      // an owner-signed grant and both must come from ONE snapshot.
      // ready-5c5: kind-scoped only, no `authors` — a relay's author index is
      // free to under-return (measured on wss://relay.3dl.network), so
      // ownership is enforced client-side by discoverOwnerBoards instead of
      // trusted to the wire filter.
      expect(capture.filters[0]).toEqual({ kinds: [30301, 39301] });
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
      //    alphaDup's title (the LATER of the two definitions, ready-a9b
      //    latest-wins) rather than alpha's. This holds for a signing identity
      //    too: verification is NOT gated on canSign.
      expect(renderedBoards(root)).toEqual(OWNERS_GENUINE_BOARDS);

      // 2b. ready-56b: and their coordinates are provenance, not chrome — the
      //     debug-grade coordinate dump that used to head the page is gone.
      expectNoCoordinateDump(root);

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
      // ready-c4b added the 39301 role grants to the authority REQ (a
      // confidential board's read key rides inside an owner-signed grant);
      // ready-bad added the per-board item REQ, scoped by board coordinate.
      // ready-5c5: the authority snapshot is now TWO REQs, neither carrying
      // `authors` (a relay's author index under-returns — measured on
      // wss://relay.3dl.network). They differ because the two authority kinds
      // are addressed by different tags: the 30301 definition by its own "d",
      // each 39301 grant by the "a" board coordinate it names. A single
      // `#a`-scoped AUTHORITY_KINDS filter — which is what this line asserted
      // before — ANDs those conditions and therefore matches NO board
      // definition at all, which is how the first attempt at this item shipped
      // a page that rendered "No boards" against the live relay.
      expect(capture.filters).toEqual([
        { kinds: [30301], "#d": ["alpha"] },
        { kinds: [39301], "#a": [boardCoord(OWNER, "alpha")] },
        { kinds: [30302, 1630, 1631, 1632, 1633, 39301], "#a": [boardCoord(OWNER, "alpha")] },
      ]);
      expectItemFetchesScopedToRenderedBoards(capture, root);
      expectSnapshotCarriedTheForgedEvents(capture);
      // "Alpha Board Dup", not "Alpha Board" — alphaDup is the LATER of the
      // two "alpha" definitions in HOSTILE_SNAPSHOT (see OWNERS_GENUINE_BOARDS'
      // doc); latest-wins picks it regardless of snapshot order.
      expect(renderedBoards(root)).toEqual([
        { title: "Alpha Board Dup", coord: boardCoord(OWNER, "alpha") },
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

      // The board path opens a SECOND subscription to the same relay set (the
      // board's items, after its 30301/39301 authority events). The property
      // under test is WHICH relays were used, not how many sockets — so
      // compare the distinct set.
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

    it("ready-280: surfaces relays fragment.ts dropped as a visible notice, and never dials them", async () => {
      // fragment.ts is the layer that drops non-wss entries (see
      // fragment.test.ts's "ready-280 relay scheme filtering" fixtures); this
      // proves the OTHER half — that afterLogin, given a fragment carrying
      // droppedRelays, tells the user rather than staying silent, and that the
      // dropped URL is never one FakeRelayWebSocket actually opened.
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);
      const DROPPED = "ws://bad.example:7777";

      await afterLogin(
        root,
        identity,
        { kind: "board", board: boardCoord(OWNER, "alpha"), relays: [LINK_RELAY], droppedRelays: [DROPPED] },
        deps,
      );

      expect([...new Set(FakeRelayWebSocket.urls)]).toEqual([LINK_RELAY]);
      expect(FakeRelayWebSocket.urls).not.toContain(DROPPED);
      const notice = root.querySelector(".confidential-notice")?.textContent ?? "";
      expect(notice).toContain(DROPPED);
      expect(notice).toMatch(/could not open and never tried/);
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
        keyUnwrapper: () => neverUnwraps,
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

    // ready-5c5: the REQ is kind-scoped only (no `authors`) — STRANGER's key
    // is enforced client-side, by discoverOwnerBoards' owner check below, not
    // by the wire filter.
    expect(capture.filters[0]).toEqual({ kinds: [30301, 39301] });
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

/**
 * ready-5c5 — EVERY discovery query, against a relay that HONOURS NIP-01.
 *
 * The first attempt at this item passed 531 tests and rendered "No boards"
 * against wss://relay.3dl.network, for three separate reasons. Each has a case
 * below, and each case is driven by makeNip01Relay — a relay that applies the
 * filter — because the filter-ignoring fakes used everywhere else in this file
 * cannot fail on any of them:
 *
 *   1. WRONG TAG. The single-board query was `{kinds:[30301,39301],
 *      "#a":[coord]}`. Within a NIP-01 filter every condition ANDs, and a
 *      kind-30301 board definition has NO "a" tag (BuildBoardEvent emits d,
 *      title, optional archived, optional p). So it matched grants and zero
 *      definitions.
 *   2. UNPROVEN QUERIES. Only the own-boards query was ever exercised
 *      red-first. There is one case per query here — own-boards, portfolio,
 *      single-board — so no query rides on another's proof.
 *   3. TRUNCATION. Dropping `authors` widened the discovery REQ from one
 *      author's events to every author's, and a relay caps one REQ regardless
 *      of what the client asked for. maxLimit below is that cap.
 *
 * ANTI-VACUITY runs through all of it: each case asserts, against the SAME
 * relay instance, that the pre-fix filter really does come back wrong. A
 * negative fixture nobody proves is discriminating is how the first attempt
 * shipped.
 */
describe("ready-5c5: discovery against a relay that HONOURS the REQ filter", () => {
  const ownerIdentity: Identity = {
    pubkey: OWNER,
    auth: authTransition({ type: "login", method: "extension" }),
  };
  const ALPHA = boardCoord(OWNER, "alpha");
  const ALL_THREE = [
    { title: "Alpha Board", coord: boardCoord(OWNER, "alpha") },
    { title: "Beta Board", coord: boardCoord(OWNER, "beta") },
    { title: "Gamma Board", coord: boardCoord(OWNER, "gamma") },
  ];

  /** Runs one REQ straight at a relay built from the same config, so a case can
   * assert what a DIFFERENT (e.g. the pre-fix) filter would have returned. This
   * is what makes each fixture provably discriminating rather than assumed to
   * be. */
  async function ask(config: Nip01RelayConfig, filter: NostrFilter): Promise<NostrEvent[]> {
    const { ctor } = makeNip01Relay(config);
    return fetchEventsFromRelays([CONFIG_RELAY], filter, {
      webSocketCtor: ctor,
      retries: 0,
      timeoutMs: 2000,
    });
  }

  it("GUARD: this relay really does apply tag filters, and the pre-fix `#a` filter really does miss every board definition", async () => {
    // If this guard fails, every case below is meaningless — that is precisely
    // the state the first attempt shipped in.
    const config: Nip01RelayConfig = { events: [alpha, beta, gamma] };

    // The filter main.ts USED to send for a single-board link.
    const preFix = await ask(config, { kinds: [30301, 39301], "#a": [ALPHA] });
    expect(preFix).toEqual([]);

    // The filter it sends now — the board's own "d".
    const postFix = await ask(config, { kinds: [30301], "#d": ["alpha"] });
    expect(postFix.map((e) => e.id)).toEqual([alpha.id]);

    // And the relay is not simply answering nothing: kind-only sees all three.
    const kindOnly = await ask(config, { kinds: [30301, 39301] });
    expect(kindOnly.map((e) => e.id).sort()).toEqual([alpha.id, beta.id, gamma.id].sort());
  });

  it("QUERY 1/3 own-boards: renders all three boards though the relay's author index holds only two", async () => {
    // The measured production shape: a paged {kinds:[30301], authors:[owner]}
    // REQ served 42 of an owner's 56 boards; a paged {kinds:[30301]} REQ, same
    // relay same run, served all 56. Here the author index holds alpha+beta and
    // "loses" gamma.
    const config: Nip01RelayConfig = { events: [alpha, beta, gamma], authorIndex: [alpha, beta] };

    // ANTI-VACUITY, this exact relay config: the pre-fix filter is genuinely
    // lossy against it, so the render below is not something any filter would
    // have produced.
    const preFix = await ask(config, { kinds: [30301, 39301], authors: [OWNER] });
    expect(preFix.map((e) => e.id).sort()).toEqual([alpha.id, beta.id].sort());

    const { deps, handle } = injectedHonouringDeps(config, capture);
    await afterLogin(root, ownerIdentity, { kind: "none" }, deps);

    expect(capture.filters[0]).toEqual({ kinds: [30301, 39301] });
    expect(handle.requests[0]?.authors).toBeUndefined();
    expect(renderedBoards(root)).toEqual(ALL_THREE);
  });

  it("QUERY 2/3 portfolio: renders all three boards though the relay's author index holds only two", async () => {
    const config: Nip01RelayConfig = { events: [alpha, beta, gamma], authorIndex: [alpha, beta] };

    const preFix = await ask(config, { kinds: [30301, 39301], authors: [OWNER] });
    expect(preFix.map((e) => e.id).sort()).toEqual([alpha.id, beta.id].sort());

    const { deps, handle } = injectedHonouringDeps(config, capture);
    await afterLogin(
      root,
      ownerIdentity,
      { kind: "portfolio", relays: [], viewer: OWNER },
      deps,
    );

    expect(capture.filters[0]).toEqual({ kinds: [30301, 39301] });
    expect(handle.requests[0]?.authors).toBeUndefined();
    expect(renderedBoards(root)).toEqual(ALL_THREE);
  });

  it("QUERY 3/3 single-board: renders the board the link names — the query the `#a` regression zeroed out", async () => {
    // THE REGRESSION. Against this relay the pre-fix `#a`-scoped
    // AUTHORITY_KINDS filter returns nothing at all (asserted in the GUARD
    // above), so the page rendered "No boards" — which is what the live relay
    // did, and what no filter-ignoring fixture could ever show.
    const config: Nip01RelayConfig = { events: [alpha, beta, gamma], authorIndex: [alpha, beta] };

    const { deps, handle } = injectedHonouringDeps(config, capture);
    await afterLogin(root, ownerIdentity, { kind: "board", board: ALPHA, relays: [] }, deps);

    // Two authority REQs, because the two kinds hang off different tags, and
    // NEITHER carries `authors`.
    expect(capture.filters.slice(0, 2)).toEqual([
      { kinds: [30301], "#d": ["alpha"] },
      { kinds: [39301], "#a": [ALPHA] },
    ]);
    for (const req of handle.requests) expect(req.authors).toBeUndefined();

    expect(renderedBoards(root)).toEqual([{ title: "Alpha Board", coord: ALPHA }]);
    expect(root.textContent).not.toContain("No boards found.");
  });

  it("QUERY 3/3 single-board: still refuses a coordinate whose only matching event is forged", async () => {
    // The `#d` filter matches forgedSig ("evil"), so the relay really does
    // serve it — only the schnorr check keeps it off the page. Dropping
    // `authors` from the wire filter must not have moved that check.
    const config: Nip01RelayConfig = { events: [forgedSig, impersonator, delta, alpha] };
    const { deps } = injectedHonouringDeps(config, capture);

    await afterLogin(
      root,
      ownerIdentity,
      { kind: "board", board: boardCoord(OWNER, "evil"), relays: [] },
      deps,
    );

    expect(capture.snapshot.map((e) => e.id)).toEqual([forgedSig.id]);
    expect(renderedBoards(root)).toEqual([]);
    expect(root.textContent).toContain("No boards found.");
    expectNoForgedContent(root);
  });

  it("TRUNCATION: renders all three boards from a relay that caps ONE REQ at two events", async () => {
    // Widening the discovery filter from `authors:[owner]` to kind-only is only
    // safe if the client pages. Measured: wss://relay.3dl.network answers an
    // unbounded {kinds:[30302]} REQ with exactly 500 of the 5648 events it
    // holds, and says nothing about the other 5148. maxLimit: 2 is that cap,
    // shrunk to fixture size.
    const config: Nip01RelayConfig = { events: [alpha, beta, gamma], maxLimit: 2 };

    // ANTI-VACUITY: one unpaged REQ against this relay really does truncate.
    const oneShot = await ask(config, { kinds: [30301, 39301], limit: 2 });
    expect(oneShot).toHaveLength(2);

    const { deps, handle } = injectedHonouringDeps(config, capture);
    await afterLogin(root, ownerIdentity, { kind: "none" }, deps);

    expect(renderedBoards(root)).toEqual(ALL_THREE);
    // It got there by WALKING: more than one REQ, and every REQ after the first
    // carries an `until` cursor strictly older than the one before it.
    const discovery = handle.requests.filter((f) => f.kinds?.includes(30301));
    expect(discovery.length).toBeGreaterThan(1);
    expect(discovery[0]?.until).toBeUndefined();
    for (let i = 1; i < discovery.length; i++) {
      expect(discovery[i]?.until).toBeDefined();
      if (i > 1) expect(discovery[i]!.until!).toBeLessThan(discovery[i - 1]!.until!);
    }
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
