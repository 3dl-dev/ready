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
import { beforeEach, describe, expect, it } from "vitest";
import { afterLogin, type BoardDeps, type Identity } from "./main";
import { authTransition } from "./lib/auth";
import { fetchEventsFromRelays } from "./lib/relay";
import { boardCoord } from "./lib/boarddiscovery";
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
 * The snapshot the hostile relay serves. Deliberately ordered forged-first so
 * a passing result cannot depend on the genuine events arriving first, and it
 * mixes all four rejection reasons the product must survive:
 *   forgedSig    — owner's board, signature corrupted after signing
 *   impersonator — signed by OTHER, pubkey field relabeled to OWNER
 *   delta        — validly signed, but by a pubkey that is not the follow target
 *   alphaDup     — genuine duplicate of an existing coordinate (must collapse)
 */
const HOSTILE_SNAPSHOT: NostrEvent[] = [forgedSig, impersonator, alpha, delta, beta, alphaDup, gamma];

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
}

function injectedDeps(served: NostrEvent[], capture: Capture): BoardDeps {
  FakeRelayWebSocket.reset(served);
  return {
    loadRelays: async () => [CONFIG_RELAY],
    fetchEvents: async (relays, filter, opts) => {
      const events = await fetchEventsFromRelays(relays, filter, {
        ...opts,
        webSocketCtor: FakeRelayWebSocket as unknown as typeof WebSocket,
        retries: 0,
        timeoutMs: 2000,
      });
      capture.snapshot = events;
      return events;
    },
  };
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

const readOnlyIdentity: Identity = {
  pubkey: OWNER,
  auth: authTransition({ type: "login", method: "readOnly" }),
};

let root: HTMLElement;
let capture: Capture;

beforeEach(() => {
  document.body.replaceChildren();
  root = document.createElement("div");
  root.id = "app";
  document.body.append(root);
  capture = { snapshot: [] };
});

describe("afterLogin — own-boards discovery (fragment.kind === 'none')", () => {
  it("SECURITY: renders exactly the owner's genuine boards from a snapshot that also carries forged, impersonated and foreign events", async () => {
    const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

    await afterLogin(root, readOnlyIdentity, { kind: "none" }, deps);

    // 1. The hostile relay really was queried, via the config relay set (a
    //    bare visit carries no link relays).
    expect(FakeRelayWebSocket.urls).toEqual([CONFIG_RELAY]);

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
    //    the first occurrence's title rather than alphaDup's.
    expect(renderedBoards(root)).toEqual([
      { title: "Alpha Board", coord: boardCoord(OWNER, "alpha") },
      { title: "Beta Board", coord: boardCoord(OWNER, "beta") },
      { title: "Gamma Board", coord: boardCoord(OWNER, "gamma") },
    ]);

    // 3. Nothing forged leaked anywhere else in the subtree either.
    expectNoForgedContent(root);

    // 4. The page reached its terminal state: the connecting indicator was
    //    removed, so this is a completed render and not a mid-flight snapshot.
    expect(root.querySelector(".connecting")).toBeNull();
  });

  it("SECURITY: renders no boards at all when every event the relay serves is forged, impersonated or foreign", async () => {
    const deps = injectedDeps([forgedSig, impersonator, delta], capture);

    await afterLogin(root, readOnlyIdentity, { kind: "none" }, deps);

    expect(capture.snapshot.map((e) => e.id).sort()).toEqual(
      [forgedSig.id, impersonator.id, delta.id].sort(),
    );
    expect(renderedBoards(root)).toEqual([]);
    expect(root.textContent).toContain("No boards found.");
    expectNoForgedContent(root);
  });
});

describe("afterLogin — single-board link (fragment.kind === 'board')", () => {
  it("renders the genuine board the coordinate names, querying the relays carried in the link rather than the config set", async () => {
    const deps = injectedDeps(HOSTILE_SNAPSHOT, capture);

    await afterLogin(
      root,
      readOnlyIdentity,
      { kind: "board", board: boardCoord(OWNER, "alpha"), relays: [LINK_RELAY] },
      deps,
    );

    // LINK_RELAY and CONFIG_RELAY are different URLs, so this distinguishes
    // the two relay sources — the link's list wins when it is non-empty.
    expect(FakeRelayWebSocket.urls).toEqual([LINK_RELAY]);
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
      readOnlyIdentity,
      { kind: "board", board: boardCoord(OWNER, "beta"), relays: [] },
      deps,
    );

    expect(FakeRelayWebSocket.urls).toEqual([CONFIG_RELAY]);
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
      readOnlyIdentity,
      { kind: "board", board: boardCoord(OWNER, "evil"), relays: [LINK_RELAY] },
      deps,
    );

    expectSnapshotCarriedTheForgedEvents(capture);
    expect(renderedBoards(root)).toEqual([]);
    expect(root.textContent).toContain("No boards found.");
    expectNoForgedContent(root);
  });
});

describe("afterLogin — relay failure surfaces instead of an empty board list", () => {
  it("shows the relay error and renders no board list when no relay can be reached", async () => {
    const deps: BoardDeps = {
      loadRelays: async () => [CONFIG_RELAY],
      fetchEvents: async () => {
        throw new Error("relay: could not reach any relay: socket error");
      },
    };

    await afterLogin(root, readOnlyIdentity, { kind: "none" }, deps);

    expect(renderedBoards(root)).toEqual([]);
    expect(root.textContent).toContain("could not reach any relay");
    // An unreachable relay must NOT be indistinguishable from "you own no
    // boards" — the empty-list copy is reserved for a successful query.
    expect(root.textContent).not.toContain("No boards found.");
  });
});
