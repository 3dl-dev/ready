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
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { afterLogin, loadBoardItems, main, type BoardDeps, type Identity } from "./main";
import { authTransition, canSign } from "./lib/auth";
import { fetchEventsFromRelays, type NostrFilter } from "./lib/relay";
import { boardCoord, type DiscoveredBoard } from "./lib/boarddiscovery";
import * as boarddiscoveryModule from "./lib/boarddiscovery";
import { encodeNpub } from "./lib/npub";
import { fakeNip44Signer } from "./lib/fakesigner";
import { neverUnwraps, nip07KeyUnwrapper } from "./lib/keyunwrap";
import {
  makeNip01Relay,
  type Nip01RelayConfig,
  type Nip01RelayHandle,
} from "./lib/nip01relay.fixtures";
import type { NostrEvent } from "./lib/nostrevent";
import type { ParsedFragment } from "./lib/fragment";
// ready-1af's write-gate block: the real write path (buildWrite -> signWith ->
// publishEvents) and the real signer, so a refusal can be attributed to one
// specific control rather than to "something threw".
import { signNostrEvent, xOnlyPubkey } from "./lib/schnorrsign";
import { buildFullCreate } from "./board/writeevents";
import { NotAuthorizedError } from "./board/nostrwriter";
import { RelayRejectedError } from "./lib/publish";
import {
  BOARD_COORD as CONF_COORD,
  BOARD_D as CONF_BOARD_D,
  OWNER_PUB as CONF_OWNER,
  OWNER_SEC as CONF_OWNER_SEC,
  CEK_EPOCH1,
  CEK_EPOCH2,
  LTK as CONF_LTK,
  CUTOVER as CONF_CUTOVER,
  boardEvent as confBoardEvent,
  cards as confCards,
  // ready-191 rework 4: the adversarial post-cutover cleartext card, named so
  // the writer's quarantine can be asserted against the very event it drops.
  cardSmuggledCleartext,
  expectedPlaintext as confExpected,
  grants as confGrants,
} from "./lib/confidential.fixtures";
// ready-191 rework: reading the browser's own sealed write back through the real
// fold, as an independent key-holder would. Same seam main.ts projects through.
import { foldItemSource } from "./lib/itemsource";
import { PLACEHOLDER, labelToken, type BoardDecryptor, type EncryptedBoardSet } from "./lib/envelope";
import { hexToBytes } from "./lib/sha256";
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
  ATTACKER_OWNER,
  attackerAlpha,
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

/**
 * The same two identity shapes as IDENTITIES, but pubkey === STRANGER instead
 * of OWNER — a logged-in identity that owns none of the boards a hostile
 * relay serves. ready-4c98: the board-link follow-target cases were pinned
 * with only the signing (NIP-07 extension) shape, mirroring exactly the blind
 * spot this file's own "TWO AXES, NOT ONE" header (above) already names for
 * the rest of the suite — a canSign-gated rewrite of this discovery path
 * shipped green once before every other case here was parametrized over both
 * shapes. Both entries here are distinct from OWNER, so the read-only case
 * still exercises the three-different-keys collapse the item exists to
 * catch, not a fourth spelling of the signing one.
 */
const STRANGER_IDENTITIES: { name: string; signing: boolean; identity: Identity }[] = [
  {
    name: "read-only npub",
    signing: false,
    identity: {
      pubkey: STRANGER,
      auth: authTransition({ type: "login", method: "readOnly" }),
    },
  },
  {
    name: "NIP-07 extension (signing)",
    signing: true,
    identity: {
      pubkey: STRANGER,
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
    // ready-48f CHANGED WHAT THIS BRANCH DOES, and these cases are the record of
    // both halves.
    //
    // It used to render the awaiting-authorization panel and RETURN, contacting
    // no relay at all — asserted here as "contacts no relay at all". The reason
    // given was "a claim link is an invitation, not an authorization". The
    // AUTHORIZATION half of that is unchanged and is still asserted below: the
    // link confers no trust, opens only the ONE board it names, and renders none
    // of the hostile snapshot's forgeries. What was wrong was the consequence —
    // the invitee was shown a blank page, so "a stranger with a link reaches a
    // POPULATED board" was false on the very link `rd board share` mints for a
    // stranger, and a page that never opened a subscription could not pick the
    // owner's later grant up either (measured in scripts/live-stranger-walk.mjs:
    // 0 cards before `rd grant --claim`, 0 cards 45s after it).
    //
    // Nothing fetched here is a new exposure: it is what the relay already
    // serves anyone who asks for that coordinate, and `rd join <token>` — the
    // CLI half of the SAME token, advertised in `rd board share`'s own help —
    // already syncs exactly these events for exactly this person.
    const claimFragment = (board: string): ParsedFragment => ({
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
    });

    /** The awaiting-authorization copy, asserted by equality so neither the
     * "(read-only)" suffix nor its absence can appear on the wrong session.
     * renderAwaitingAuthorization reads identity.auth.readOnly directly, so this
     * is the second place the signing/read-only distinction shows up in the
     * DOM. */
    function expectAwaitingPanel(board: string): void {
      expect(root.querySelector("section.awaiting-authorization > h2")?.textContent).toBe(
        "Awaiting authorization",
      );
      expect(root.querySelector("section.awaiting-authorization > p")?.textContent).toBe(
        `You are logged in as ${encodeNpub(identity.pubkey)}${signing ? "" : " (read-only)"}. ` +
          `Ask the owner of board ${board} to grant this key access.`,
      );
    }

    it("names the invited board, and opens THAT board and nothing else the relay is serving", async () => {
      // A coordinate that IS in the fixtures, so "it opened the invited board"
      // is observable. The hostile snapshot also carries OWNER's two OTHER
      // genuine boards and four forgeries; none of them is what this link
      // invites, so none of them may render.
      const invited = boardCoord(OWNER, "alpha");
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

      await afterLogin(root, identity, claimFragment(invited), deps);

      // THE NEW BEHAVIOUR: the invited board is open.
      expect(renderedBoards(root)).toEqual([
        OWNERS_GENUINE_BOARDS.find((b) => b.coord === invited)!,
      ]);
      // …reached over the LINK's relays, which is the only relay set a claim
      // token carries. Config relays are not consulted.
      expect([...new Set(FakeRelayWebSocket.urls)]).toEqual([LINK_RELAY]);
      expect(FakeRelayWebSocket.urls).not.toContain(CONFIG_RELAY);
      // …and scoped to the invited coordinate, not to "everything you have".
      // Every filter is either the two authority REQs for THIS board or the
      // item REQ for it.
      expect(capture.filters.length).toBeGreaterThan(0);
      for (const f of capture.filters) {
        const scoped =
          (f["#d"] as string[] | undefined)?.includes("alpha") === true ||
          (f["#a"] as string[] | undefined)?.includes(invited) === true;
        expect(scoped, `unscoped filter ${JSON.stringify(f)}`).toBe(true);
      }

      // THE UNCHANGED HALF: a claim link is not an authorization. OWNER's OTHER
      // genuine boards stay out — the link names one board and admits one board
      // — and no forgery reaches the DOM.
      const otherGenuine = OWNERS_GENUINE_BOARDS.filter((b) => b.coord !== invited);
      expect(otherGenuine).toHaveLength(2);
      for (const b of otherGenuine) {
        expect(root.textContent ?? "").not.toContain(b.coord);
        expect(root.innerHTML).not.toContain(b.coord);
      }
      expectNoForgedContent(root);

      // THE PANEL DOES NOT LIE TO A KEY THAT IS ALREADY GRANTED. Both identities
      // in this describe ARE the board's owner, and deriveLevels seats a board's
      // author implicitly, so this session is granted the moment the board is
      // discovered — and the panel is gone. It is driven by the derived grant
      // levels, not by "the fragment was a claim link", which is the difference
      // between telling this user something true and telling them to go ask
      // themselves for access.
      expect(root.querySelector("section.awaiting-authorization")).toBeNull();
    });

    it("an UNGRANTED key sees the invited board AND the panel at the same time", async () => {
      // The stranger case, which is what a claim link is actually for: a key
      // that owns nothing here and has been granted nothing yet. It is the only
      // way to observe the two together, because an owner is granted implicitly
      // (the case above) and would never see the panel over a board.
      const invited = boardCoord(OWNER, "alpha");
      const stranger: Identity = {
        pubkey: STRANGER,
        auth: authTransition({ type: "login", method: signing ? "extension" : "readOnly" }),
      };
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

      await afterLogin(root, stranger, claimFragment(invited), deps);

      // The board the link names is open — discovery follows the COORDINATE's
      // owner, not the viewer, so an ungranted stranger reaches a populated
      // board rather than a blank page.
      expect(renderedBoards(root)).toEqual([
        OWNERS_GENUINE_BOARDS.find((b) => b.coord === invited)!,
      ]);
      // …and the page says, in the same document, that this key is not yet
      // authorized on it.
      expect(root.querySelector("section.awaiting-authorization > h2")?.textContent).toBe(
        "Awaiting authorization",
      );
      expect(root.querySelector("section.awaiting-authorization > p")?.textContent).toBe(
        `You are logged in as ${encodeNpub(STRANGER)}${signing ? "" : " (read-only)"}. ` +
          `Ask the owner of board ${invited} to grant this key access.`,
      );
      expectNoForgedContent(root);
    });

    it("an invitation to a board the relay does not serve renders the panel and no board at all", async () => {
      // A coordinate DELIBERATELY absent from the fixtures, so "none of OWNER's
      // genuine boards were rendered" is assertable in full.
      const board = boardCoord(OWNER, "invited");
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

      await afterLogin(root, identity, claimFragment(board), deps);

      expect(renderedBoards(root)).toEqual([]);
      expectNoneOfOwnersBoardsRendered(root);
      expectNoForgedContent(root);
      expectAwaitingPanel(board);
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

// ready-4c98 REWORK: parametrized over STRANGER_IDENTITIES (read-only npub AND
// NIP-07 extension), not just the signing shape. The file's own "TWO AXES,
// NOT ONE" header records that a canSign-gated rewrite of this exact
// discovery path already shipped green once, which is why every other case
// in this file runs over both identity shapes — a stranger identity is no
// exception, and pinning only the signing half would repeat precisely that
// blind spot for the follow-target property this describe block exists to
// cover.
describe.each(STRANGER_IDENTITIES)(
  "afterLogin — the follow target is the logged-in key, not the relay's choice ($name)",
  ({ signing, identity }) => {
    it("SECURITY: an identity that authored none of the served boards sees none of them, however valid their signatures", async () => {
      // STRANGER logs in and owns nothing. The hostile relay answers with
      // OWNER's three GENUINE, correctly-signed boards plus OTHER's genuine
      // delta. Signature verification alone does not reject any of those
      // four — only "author must equal the logged-in key" does. Every other
      // case in the rest of this file uses identity.pubkey === OWNER, where
      // that distinction is invisible: main.ts could take its follow target
      // from the served events (e.g. events[0].pubkey) and still render the
      // right rows.
      const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);
      expect(canSign(identity.auth)).toBe(signing);

      await afterLogin(root, identity, { kind: "none" }, deps);

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
        expectedIdentityLine(STRANGER, signing),
      );
    });

    // ready-4c98: the single-board-link path (fragment.kind === "board") had NO
    // case where parsedCoord.owner, identity.pubkey and events[0].pubkey were
    // three DIFFERENT keys — every existing board-link fixture collapses all
    // three to OWNER, so `discoverOwnerBoards(events, [events[0]?.pubkey ??
    // parsedCoord.owner], parsedCoord.boardD)` (follow whatever the relay
    // served first, or the logged-in key, instead of the link's own
    // coordinate) would still pass 100% of them.
    it("SECURITY: a single-board link follows the LINK'S coordinate owner, not the relay's first event nor the logged-in identity", async () => {
      // THREE DISTINCT keys: the link names OWNER; the logged-in identity is
      // STRANGER, who owns nothing; the hostile relay's FIRST served event
      // (events[0]) is attackerAlpha, authored by a third key, ATTACKER_OWNER —
      // genuinely Go-signed, at the exact "alpha" coordinate
      // the link asks OWNER for. Verification alone cannot reject it; only
      // "author must equal the link's named owner" can.
      //
      // injectedDeps (the filter-ignoring FakeRelayWebSocket), not
      // injectedHonouringDeps, is the right seam here: nothing below asserts
      // on the emitted REQ filter, and the behavioural assertions
      // (renderedBoards, discoverOwnerBoards' owners argument) hold
      // regardless of what was requested, because discoverOwnerBoards filters
      // the FULL received snapshot by owner+coordinate client-side. A
      // filter-ignoring stub cannot make this test pass vacuously — it can
      // only make the anti-vacuity checks below (capture.snapshot really
      // contained attackerAlpha) trivially true, which is exactly what they
      // are for.
      const deps = injectedDeps(
        [attackerAlpha, alpha, alphaDup, beta, gamma, forgedSig, impersonator, delta],
        capture,
      );
      const discoverSpy = vi.spyOn(boarddiscoveryModule, "discoverOwnerBoards");

      try {
        await afterLogin(
          root,
          identity,
          { kind: "board", board: boardCoord(OWNER, "alpha"), relays: [LINK_RELAY] },
          deps,
        );

        // ANTI-VACUITY: the hostile relay really did serve attackerAlpha FIRST —
        // if the mutation reads events[0]?.pubkey, this is the pubkey it gets.
        expect(capture.snapshot[0]?.id).toBe(attackerAlpha.id);
        expect(capture.snapshot.map((e) => e.id)).toContain(attackerAlpha.id);

        // DIRECTLY on the resolved follow target, not only the REQ filter (the
        // single-board REQ carries no `authors` at all post-ready-5c5, so a
        // filter-only assertion could never have distinguished this anyway).
        // discoverOwnerBoards' owners argument must be exactly [OWNER] — never
        // [attackerAlpha.pubkey] (events[0].pubkey) nor [identity.pubkey]
        // (STRANGER, for either identity shape parametrized here).
        expect(discoverSpy).toHaveBeenCalled();
        const owners = discoverSpy.mock.calls[0]?.[1];
        expect(owners).toEqual([OWNER]);
        expect(owners).not.toEqual([ATTACKER_OWNER]);
        expect(owners).not.toEqual([STRANGER]);

        // BEHAVIOURALLY too: OWNER's genuine "alpha" coordinate renders (latest-
        // wins: alphaDup's title), attacker's namesake board does not, however
        // validly it is signed, and it is not the logged-in identity's problem
        // either (stranger owns nothing and sees no boards of their own).
        expect(renderedBoards(root)).toEqual([
          { title: "Alpha Board Dup", coord: boardCoord(OWNER, "alpha") },
        ]);
        expect(root.textContent).not.toContain("Attacker's Alpha Board");
        expectNoForgedContent(root);
      } finally {
        discoverSpy.mockRestore();
      }
    });
  },
);

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

// ---------------------------------------------------------------------------
// ready-1af — THE WRITE GATE, WITNESSED RATHER THAN ASSERTED.
//
// main.ts's comment at the `pk=`/readOnly identity mint site used to claim that
// `method: "readOnly"` "is what every write control ... gates on" while NOTHING
// read any such flag: WorkspaceOptions (render.ts) carried no `readOnly` field
// and write.ts's BoardWriter interface has never had one. That is the exact
// shape ready-75a shipped under — a doc comment promising a control its callers
// do not provide — and it is worse than no comment, because the next person to
// build a write control believes the gate is already there.
//
// The gate that DOES exist lives two steps from that mint site, in
// loadBoardItems' `signer: canSign(identity.auth) ? nip07Signer() : undefined`,
// and in NostrBoardWriter.whyReadOnly()'s no-signer branch. This block is the
// witness for that mechanism through the real loadBoardItems, and for the ORDER
// whyReadOnly() evaluates its three refusal reasons in — which the corrected
// comment now states and which nothing else in the suite pinned.
//
// WHY THE FIXTURE IS BUILT THE WAY IT IS. Round 1 of this item shipped a block
// that passed with the entire write gate DELETED: it wrote to the item id
// "some-item" against an EMPTY snapshot, so the rejection came from
// writeevents.ts's requireItem refusing an unknown id long before authorization
// was ever reached, and the installed signer was never called because buildWrite
// threw first. Every ingredient below closes a specific one of those holes:
//
//   - THE TARGET ITEM REALLY EXISTS. GATE_SNAPSHOT is a real signed create
//     (buildFullCreate + schnorrsign.ts, the same construction nostrwriter.
//     test.ts's seedItem uses), so buildWrite CANNOT refuse GATE_ITEM. The
//     `writer.items().has(GATE_ITEM)` assertion pins that, so this test cannot
//     silently degrade back into an unknown-id test.
//   - THE REFUSAL IS TYPED, NOT JUST "SOMETHING THREW". NotAuthorizedError plus
//     the message text, so a WriteRefusedError / SignerMissingError / relay
//     error cannot stand in for the gate.
//   - THE SIGNER IS REAL AND CAPABLE. window.nostr.signEvent produces genuine
//     BIP-340 signatures for GATE_OWNER, so a refusal cannot be "there was no
//     extension anyway", and `not.toHaveBeenCalled()` proves the refusal landed
//     before anything was signed.
//   - THE CONTRAST CASE PROVES THE FIXTURE CAN WRITE. Same board, same
//     snapshot, same item, same op, same installed extension — only the login
//     method differs — and there the write runs all the way through signing.
//   - `relays: []` KEEPS IT OFF THE NETWORK. loadBoardItems has no
//     publishOptions seam, so the contrast case would otherwise open a real
//     socket. With no relays, publishEvents refuses up front
//     (RelayRejectedError, "no relays are configured") AFTER signing, which is
//     exactly the boundary this block needs to observe.
//
// GATE_OWNER's write authority is NOT a grant event: `authorityEvents` is empty
// here, and rolegrant.ts's deriveLevels seats the board's own author at
// LEVEL_MAINTAINER unconditionally (`levels.set(boardAuthor, LEVEL_MAINTAINER)`).
// That implicit authority is the point — the grant-level branch of whyReadOnly()
// is satisfied, the board is public so the confidentiality branch is too, and
// the only branch left that can explain a refusal is the signer one.
// ---------------------------------------------------------------------------

/** A test-only secp256k1 secret (BIP-340's own test-vector key, also used by
 * nostrwriter.test.ts). It exists so this file can produce a snapshot whose card
 * events genuinely verify — the fold re-checks every signature, so a
 * hand-written card would simply vanish and the target item would not exist
 * after all, which is precisely the hole this block closes. */
const GATE_SECRET = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
const GATE_OWNER = xOnlyPubkey(GATE_SECRET);
const GATE_D = "writegate";
const GATE_ITEM = "gate-1";

const GATE_BOARD: DiscoveredBoard = {
  coord: boardCoord(GATE_OWNER, GATE_D),
  ownerPubkey: GATE_OWNER,
  boardD: GATE_D,
  title: "Write Gate Board",
};

/** The board's snapshot: a real, signed create of GATE_ITEM (board event, card,
 * issue root, status), so the writes below target an item that EXISTS. */
const GATE_SNAPSHOT: NostrEvent[] = buildFullCreate(
  {
    signer: GATE_OWNER,
    boardAuthor: GATE_OWNER,
    boardD: GATE_D,
    boardTitle: GATE_BOARD.title,
    items: new Map(),
    issueEventIds: new Map(),
    createdAt: 1_780_000_000,
  },
  {
    id: GATE_ITEM,
    msg_id: "",
    title: "Gate Seed",
    context: "",
    type: "task",
    priority: "p2",
    status: "inbox",
    for: GATE_OWNER,
    created_at: 0n,
    updated_at: 0n,
  },
).map((b) =>
  signNostrEvent({ created_at: b.created_at, kind: b.kind, tags: b.tags, content: b.content }, GATE_SECRET),
);

/** The confidential board's snapshot — REAL Go-signed sealed cards and owner CEK
 * grants (confidential.fixtures.ts, generated by the production Go writer). Used
 * so `confidential: true` is established the way production establishes it,
 * rather than being set by hand. */
const CONF_SNAPSHOT: NostrEvent[] = [confBoardEvent, ...confGrants, ...confCards];

describe("ready-1af: the control that actually refuses a browser write", () => {
  let signEvent: ReturnType<typeof vi.fn>;
  let origNostr: Window["nostr"];

  beforeEach(() => {
    origNostr = window.nostr;
    // A REAL signer: genuine BIP-340 signatures as GATE_OWNER, so publish.ts's
    // signWith/assertSignedAsBuilt accepts what comes back and the contrast case
    // gets all the way past signing.
    signEvent = vi.fn(async (e: { created_at: number; kind: number; tags: string[][]; content: string }) =>
      signNostrEvent({ created_at: e.created_at, kind: e.kind, tags: e.tags, content: e.content }, GATE_SECRET),
    );
    window.nostr = { getPublicKey: async () => GATE_OWNER, signEvent } as unknown as Window["nostr"];
  });

  afterEach(() => {
    window.nostr = origNostr;
  });

  /** writerFor runs the REAL loadBoardItems — the function that owns the
   * `canSign(identity.auth) ? nip07Signer() : undefined` decision — and returns
   * the writer it built for `board`. Only the relay transport is stubbed. */
  async function writerFor(
    identity: Identity,
    board: DiscoveredBoard,
    snapshot: NostrEvent[],
    authority: NostrEvent[],
  ) {
    const deps: BoardDeps = {
      loadRelays: async () => [],
      fetchEvents: async () => snapshot,
      keyUnwrapper: () => neverUnwraps,
    };
    const { writers } = await loadBoardItems(
      [board],
      [], // no write relays: publishEvents refuses up front instead of dialling out
      authority,
      identity,
      deps,
      () => {},
    );
    const writer = writers.get(board.coord);
    expect(writer, "loadBoardItems built no writer for this board").toBeDefined();
    return writer!;
  }

  const readOnly = (pubkey: string): Identity => ({
    pubkey,
    auth: authTransition({ type: "login", method: "readOnly" }),
  });
  const extension = (pubkey: string): Identity => ({
    pubkey,
    auth: authTransition({ type: "login", method: "extension" }),
  });

  it("a read-only identity is refused BEFORE anything is signed — on a board it owns, for an item that exists, with a working extension installed", async () => {
    const identity = readOnly(GATE_OWNER);
    expect(canSign(identity.auth)).toBe(false);

    const writer = await writerFor(identity, GATE_BOARD, GATE_SNAPSHOT, []);

    // ANTI-TAUTOLOGY: the item really is in the writer's projection, so
    // writeevents.ts's requireItem cannot be what refuses below.
    expect(
      writer.items().has(GATE_ITEM),
      "GATE_ITEM is not in the snapshot — a refusal below would be requireItem's, not the gate's",
    ).toBe(true);

    expect(writer.whyReadOnly()).toMatch(/never accepts a secret key/i);

    await expect(writer.setPriority(GATE_ITEM, "p0")).rejects.toBeInstanceOf(NotAuthorizedError);
    await expect(writer.setPriority(GATE_ITEM, "p0")).rejects.toThrow(/never accepts a secret key/i);
    // Nothing was signed: the refusal is applyNow's whyReadOnly() re-check,
    // which runs before buildWrite and before the signing loop.
    expect(signEvent).not.toHaveBeenCalled();
  });

  it("CONTRAST: the same board, item, op and installed extension DOES reach the signer once the login method can sign", async () => {
    const identity = extension(GATE_OWNER);
    expect(canSign(identity.auth)).toBe(true);

    const writer = await writerFor(identity, GATE_BOARD, GATE_SNAPSHOT, []);
    expect(writer.whyReadOnly()).toBeUndefined();

    // The only thing left to stop this write is the (deliberately empty) relay
    // list, and it stops it AFTER the event has been built and signed.
    await expect(writer.setPriority(GATE_ITEM, "p0")).rejects.toBeInstanceOf(RelayRejectedError);
    expect(signEvent).toHaveBeenCalled();
  });

  // ── the ORDER whyReadOnly() gives its three reasons in ────────────────────
  //
  // nostrwriter.ts checks CONFIDENTIALITY first, SIGNER PRESENCE second and
  // GRANT LEVEL third. Round 1's corrected comment asserted signer presence
  // FIRST — this item's own failure mode recurring inside its fix — and nothing
  // in the suite contradicted it, because every existing case varies one input
  // at a time and any order then produces the same message. The two cases below
  // each make TWO branches true at once, so only the real order satisfies them.
  it("ORDER 1/2: confidentiality outranks signer presence — a read-only identity on a confidential board is told about the SEAL", async () => {
    const board: DiscoveredBoard = {
      coord: CONF_COORD,
      ownerPubkey: CONF_OWNER,
      boardD: CONF_BOARD_D,
      title: "Confidential Board",
    };
    // Read-only AND confidential: both branches are true. The board owner's own
    // key, so the grant branch is not.
    const writer = await writerFor(readOnly(CONF_OWNER), board, CONF_SNAPSHOT, confGrants);

    expect(writer.whyReadOnly()).toMatch(/seals its free text/i);
    expect(writer.whyReadOnly()).not.toMatch(/never accepts a secret key/i);
  });

  it("ORDER 2/2: signer presence outranks grant level — an ungranted read-only key is told about the SIGNER, and the same key WITH a signer is told about the grant", async () => {
    // A key that is neither GATE_BOARD's author nor a grantee, and no grant
    // event exists at all, so deriveLevels seats only GATE_OWNER.
    const readOnlyWriter = await writerFor(readOnly(STRANGER), GATE_BOARD, GATE_SNAPSHOT, []);
    expect(readOnlyWriter.whyReadOnly()).toMatch(/never accepts a secret key/i);
    expect(readOnlyWriter.whyReadOnly()).not.toMatch(/no write grant/i);

    // ANTI-TAUTOLOGY for the line above: the grant branch really is reachable
    // for this key — it is only being outranked.
    const signingWriter = await writerFor(extension(STRANGER), GATE_BOARD, GATE_SNAPSHOT, []);
    expect(signingWriter.whyReadOnly()).toMatch(/no write grant/i);
  });

  // ── ready-191: the confidential refusal is about the KEY, not the board ────
  //
  // Every case above holds `keyUnwrapper: () => neverUnwraps`, so no CEK ever
  // reaches the page and the confidential board is correctly read-only. That is
  // half the contract. The other half — the half that made every board created
  // by a plain `rd init` read-only in the browser — is that a session which DOES
  // hold the board's key must be able to write, sealed. This case runs the REAL
  // loadBoardItems with a working unwrapper and asserts it threaded the key all
  // the way into the writer.
  it("ready-191: the SAME confidential board becomes writable once the session holds its CEK", async () => {
    const board: DiscoveredBoard = {
      coord: CONF_COORD,
      ownerPubkey: CONF_OWNER,
      boardD: CONF_BOARD_D,
      title: "Confidential Board",
    };
    const identity = extension(CONF_OWNER);
    // The extension signs as the confidential board's owner — publish.ts checks
    // the returned event was signed as built, so the identity and the signer must
    // be the same key.
    const confSign = vi.fn(async (e: { created_at: number; kind: number; tags: string[][]; content: string }) =>
      signNostrEvent({ created_at: e.created_at, kind: e.kind, tags: e.tags, content: e.content }, CONF_OWNER_SEC),
    );
    window.nostr = { getPublicKey: async () => CONF_OWNER, signEvent: confSign } as unknown as Window["nostr"];

    const deps: BoardDeps = {
      loadRelays: async () => [],
      fetchEvents: async () => CONF_SNAPSHOT,
      // The real grant → NIP-44 unwrap → CEK path, with a spec-validated NIP-44
      // v2 implementation standing in for the extension (nip44ref.test.ts).
      keyUnwrapper: () => nip07KeyUnwrapper(fakeNip44Signer(CONF_OWNER_SEC)),
    };
    // `items` is the PAGE's projection — what the user is looking at. Captured
    // so the writer's own projection can be compared against it below.
    const { items: pageItems, writers } = await loadBoardItems(
      [board],
      [],
      confGrants,
      identity,
      deps,
      () => {},
    );
    const writer = writers.get(board.coord)!;

    // NOT read-only — and specifically not for the confidentiality reason, which
    // is the branch this item narrowed.
    expect(writer.whyReadOnly()).toBeUndefined();

    // ANTI-TAUTOLOGY: the writer really did decrypt, so the write below rebuilds
    // the card from REAL content rather than being refused as redacted.
    const conf1 = confExpected.find((e) => e.id === "conf-001")!;
    expect(writer.items().get("conf-001")!.title).toBe(conf1.title);
    expect(writer.items().get("conf-001")!.redacted).toBeFalsy();

    // The only thing left to stop the write is the empty relay list, which stops
    // it AFTER the events are built and signed.
    await expect(writer.setPriority("conf-001", "p0")).rejects.toBeInstanceOf(RelayRejectedError);
    expect(confSign).toHaveBeenCalled();

    // What it signed was SEALED: no clear title tag, the enc markers present, and
    // the item's real title nowhere in the serialized event.
    const card = (await confSign.mock.results[0].value) as NostrEvent;
    expect(card.kind).toBe(30302);
    expect(card.tags.some((t) => t[0] === "title")).toBe(false);
    expect(card.tags).toContainEqual(["enc", "1"]);
    // …and sealed under the epoch main.ts SELECTED, not merely under some epoch.
    // This session holds 1 and 2; the board's current epoch is 2. See the case
    // below for why the marker's presence alone is not enough.
    expect(card.tags).toContainEqual(["cek_epoch", "2"]);
    expect(JSON.stringify(card)).not.toContain(conf1.title);

    // ── ready-191 rework: WHICH LTK the labels were tokenized under ──────────
    //
    // The sibling of the epoch line, and the same blind spot. main.ts's
    // `ltk: keyring.ltk(b.coord)` was witnessed by nothing: replacing it with
    // new Uint8Array(32).fill(7) left the whole suite green, and `undefined` —
    // which drops the `l` tags entirely — was green too. The Go conformance test
    // asserts the token FORMAT but INJECTS the LTK, so it never sees main.ts's
    // SELECTION.
    //
    // WHAT A WRONG LTK COSTS: the tokens are opaque, so a card tokenized under
    // the wrong key looks exactly as correct as a right one on the wire and in
    // the DOM. What breaks is the relay-side `#l` equality filter these tokens
    // exist for — the browser's "crypto" and rd's "crypto" stop being the same
    // string, so a label query silently returns a board missing every card the
    // browser wrote, with nothing anywhere reporting a fault.
    //
    // Asserted against the FIXTURE's LTK (the one the Go writer used to seal the
    // cards this page just read), not against anything the page produced — so
    // this is a cross-implementation agreement, not self-consistency.
    expect(conf1.labels).toEqual(["crypto", "board"]);
    expect(card.tags).toContainEqual(["l", labelToken(hexToBytes(CONF_LTK), "crypto")]);
    expect(card.tags).toContainEqual(["l", labelToken(hexToBytes(CONF_LTK), "board")]);
    // ANTI-TAUTOLOGY: the tokens are not the label text — the clear labels never
    // went on the wire, which is the OTHER half of what tokenizing is for.
    expect(card.tags).not.toContainEqual(["l", "crypto"]);
    expect(card.tags).not.toContainEqual(["l", "board"]);

    // ── ready-191 rework 4: WHICH confidentiality gate the WRITER projects through ──
    //
    // The third and worst member of the same family as the epoch and LTK lines,
    // found by the witness audit those two produced. main.ts hands the writer the
    // SAME `encryptedBoards` gate the page's read just used. That argument was
    // witnessed by nothing: replacing it with a fail-open
    //   { cutover: () => ({ cutover: 0, ok: false }) }
    // — the exact shape confidentiality.ts's own encryptedBoardsOf doc calls "the
    // bug this item fixes" — left the whole 768-case suite green, measured.
    //
    // WHAT THE FAIL-OPEN COSTS, on this shipped fixture: conf-005 is an
    // attacker-authored POST-cutover CLEARTEXT card. The page withholds it
    // (main.confidential.test.ts). A fail-open writer PROJECTS it, title and all,
    // and setPriority then seals the attacker's plaintext into an owner-SIGNED
    // card tagged ["enc","1"],["cek_epoch","2"]. Quarantined content laundered
    // into an authentic sealed card under the owner's own key — this item's done
    // condition ("an independent rd decrypts to exactly the intended state")
    // failing in the worst available direction, since the laundered card is
    // indistinguishable from a real one to every downstream reader.
    //
    // PREMISE: the adversarial event really is on the board this writer holds, so
    // its absence below is a QUARANTINE and not an empty fixture.
    expect(CONF_SNAPSHOT).toContain(cardSmuggledCleartext);
    expect(cardSmuggledCleartext.tags).toContainEqual(["title", "SMUGGLED CLEARTEXT TITLE"]);

    // THE CLAIM: the writer's own projection withholds it, exactly as the page's
    // does — one board, one verdict.
    expect(writer.items().has("conf-005")).toBe(false);
    // …and under no other id either: the quarantine is asserted on the CONTENT
    // the attacker smuggled, not only on the "d" they filed it under.
    for (const projected of writer.items().values()) {
      expect(projected.title).not.toContain("SMUGGLED CLEARTEXT");
      expect(projected.context ?? "").not.toContain("SMUGGLED CLEARTEXT");
    }
    expect([...writer.items().keys()].sort()).toEqual(pageItems.map((i) => i.id).sort());

    // ANTI-TAUTOLOGY 1: the gate is a CUTOVER, not "the writer drops every
    // plaintext card". conf-006 carries a clear title too and is grandfathered
    // in, by this projection, because it predates the cutover.
    expect(writer.items().get("conf-006")!.title).toBe("Legacy plaintext card");

    // ANTI-TAUTOLOGY 2: "not in the projection" is not "unwritable in general" —
    // the same writer wrote conf-001 twenty lines up.
    //
    // AND THE CONSEQUENCE ITSELF: the laundering write is refused BEFORE anything
    // is built or signed. Under the fail-open mutation this call instead reaches
    // the relay, and the signer is handed a sealed card whose plaintext is the
    // attacker's title — so `confSign` gaining a call is the leak, and its call
    // count is asserted unchanged rather than merely "an error was thrown".
    const signsBefore = confSign.mock.calls.length;
    await expect(writer.setPriority("conf-005", "p0")).rejects.toMatchObject({
      name: "WriteRefusedError",
      code: "unknown_item",
    });
    expect(confSign.mock.calls.length).toBe(signsBefore);
  });

  // ── ready-191 rework: WHICH epoch the seal used ───────────────────────────
  //
  // The case above asserts the enc marker is PRESENT. That is not enough, and it
  // was measured: mutating main.ts's selection to
  //   const epoch = keyring.currentEpoch(b.coord) === null ? null : 1
  // published ["cek_epoch","1"] on a board whose current epoch is 2, and the
  // ENTIRE vitest suite plus the Go conformance test stayed green. Every other
  // confidential case fixes the epoch at 1 with a keyring holding only epoch 1,
  // so `currentEpoch()` was the one line in the write path nothing exercised.
  //
  // WHAT A STALE-EPOCH SEAL COSTS, and why no read-side assertion can catch it:
  // the card is sealed under a key the WRITER still holds, so it renders
  // perfectly for the writer and for anyone else who predates the rotation.
  // The people it is broken for are the ones who joined after — including rd on
  // another machine, holding only the current epoch — and they see "[encrypted]"
  // forever, with nothing anywhere reporting a fault. That is this item's own
  // done condition ("an independent rd decrypts to exactly the intended state")
  // failing silently, so it is asserted the way the independent reader would
  // find out: by FOLDING the published event with a decryptor that holds only
  // the post-rotation key.
  it("ready-191: the browser seals under the CURRENT epoch — a reader holding ONLY the post-rotation key opens what it wrote", async () => {
    const board: DiscoveredBoard = {
      coord: CONF_COORD,
      ownerPubkey: CONF_OWNER,
      boardD: CONF_BOARD_D,
      title: "Confidential Board",
    };
    const identity = extension(CONF_OWNER);
    const confSign = vi.fn(async (e: { created_at: number; kind: number; tags: string[][]; content: string }) =>
      signNostrEvent({ created_at: e.created_at, kind: e.kind, tags: e.tags, content: e.content }, CONF_OWNER_SEC),
    );
    window.nostr = { getPublicKey: async () => CONF_OWNER, signEvent: confSign } as unknown as Window["nostr"];

    const deps: BoardDeps = {
      loadRelays: async () => [],
      fetchEvents: async () => CONF_SNAPSHOT,
      keyUnwrapper: () => nip07KeyUnwrapper(fakeNip44Signer(CONF_OWNER_SEC)),
    };
    const { writers } = await loadBoardItems([board], [], confGrants, identity, deps, () => {});
    const writer = writers.get(board.coord)!;

    // ANTI-TAUTOLOGY, and the whole reason this fixture can pin a SELECTION:
    // this session holds BOTH epochs. conf-001 was sealed under epoch 1 and
    // conf-003 under epoch 2, and both open here — so epoch 1 is genuinely
    // available to seal under, and picking 2 is a choice rather than the only
    // thing that could have happened.
    const conf1 = confExpected.find((e) => e.id === "conf-001")!;
    const conf3 = confExpected.find((e) => e.id === "conf-003")!;
    expect(conf1.epoch).toBe(1);
    expect(conf3.epoch).toBe(2);
    expect(writer.items().get("conf-001")!.title).toBe(conf1.title);
    expect(writer.items().get("conf-003")!.title).toBe(conf3.title);

    await expect(writer.setPriority("conf-001", "p0")).rejects.toBeInstanceOf(RelayRejectedError);
    const card = (await confSign.mock.results[0].value) as NostrEvent;
    expect(card.kind).toBe(30302);
    expect(card.tags).toContainEqual(["cek_epoch", "2"]);

    // THE INDEPENDENT READER: someone granted at the rotation, holding the
    // post-rotation key and nothing else — rd on another machine, or any member
    // minted after the revoke. Real key bytes and the real fold, so "opens" is an
    // AEAD outcome and not a flag the test set.
    const postRotationOnly: BoardDecryptor = {
      cek: (coord, epoch) => (coord === CONF_COORD && epoch === 2 ? hexToBytes(CEK_EPOCH2) : null),
    };
    const preRotationOnly: BoardDecryptor = {
      cek: (coord, epoch) => (coord === CONF_COORD && epoch === 1 ? hexToBytes(CEK_EPOCH1) : null),
    };
    const confidentialBoard: EncryptedBoardSet = {
      cutover: (coord) =>
        coord === CONF_COORD ? { cutover: CONF_CUTOVER, ok: true } : { cutover: 0, ok: false },
    };
    const readBack = (dec: BoardDecryptor) =>
      foldItemSource(
        {
          trusted: null,
          maintainers: null,
          pinnedBoard: CONF_COORD,
          decryptor: dec,
          encryptedBoards: confidentialBoard,
        },
        CONF_COORD,
      )
        // The board event and the ONE event the browser just published — nothing
        // else, so what is read back can only have come out of the write path.
        .loadItems([confBoardEvent, card])
        .find((i) => i.id === "conf-001")!;

    const opened = readBack(postRotationOnly);
    expect(opened.redacted).toBeFalsy();
    expect(opened.title).toBe(conf1.title);
    expect(opened.context).toBe(conf1.context);
    // …decrypted to EXACTLY the intended state, which is the mutation the write
    // was for.
    expect(opened.priority).toBe("p0");

    // ANTI-TAUTOLOGY for the line above: the pre-rotation key does NOT open the
    // same bytes, so `opened` is a real decrypt and not "any key works". This is
    // also the assertion a stale-epoch seal inverts — under the epoch-1 mutation
    // this reader is the one that opens it and the post-rotation member above is
    // the one that gets [encrypted].
    const stale = readBack(preRotationOnly);
    expect(stale.redacted).toBe(true);
    expect(stale.title).toBe(PLACEHOLDER);
  });
});
