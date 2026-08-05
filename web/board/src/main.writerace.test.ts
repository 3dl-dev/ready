// @vitest-environment jsdom
//
// ready-c7b — THE GATE RAIL MUST BE WRITABLE THE MOMENT ITS OWN BOARD LANDS,
// NOT WHEN THE WHOLE PORTFOLIO SETTLES.
//
// ready-fe4 (b1b7df5, PR #206) split main.ts's mount into an immediate
// cache-paint plus per-board reconcileOne(), with settle() the only place that
// used to hand the real NostrBoardWriter objects to the page. That opened a
// window: a board's ITEMS (including a pending gate) paint the instant that
// board's own fold is done, but its WRITER did not reach boardScopedWriter's
// map until every OTHER board in the load also finished. A fast script or user
// acting on the first board's gate rail during that window saw
// boardScopedWriter's own fallback — "Read-only: no board finished loading." —
// which is not any of NostrBoardWriter.whyReadOnly()'s three clauses, and
// render.ts's buildGateResolve renders a `.gate-read-only` note instead of the
// approve/reject controls. Traced and root-caused in ready-c7b; this file is
// the regression pin ready-c7b calls for.
//
// WHY TWO BOARDS. A single freshly-opened board's reconcileOne is followed,
// with nothing left to await, by loadBoardItems returning and settle()
// flushing — so a controlled (non-network) test cannot observe a moment
// between "this board's writer exists" and "the DOM shows it" for exactly one
// board; settle()'s own flush always lands first. Two boards make the window
// OBSERVABLE without timing tricks: RACE_BOARD's items fetch resolves
// immediately and SLOW_BOARD's is held open by a promise this test controls,
// so loadBoardItems — and therefore afterLogin, and therefore settle() —
// cannot proceed until the test says so. Everything asserted below happens
// strictly BETWEEN RACE_BOARD's own paint and settle().
//
// THE FIX THIS PINS: reconcileOne() now receives (and immediately attaches)
// the writer loadBoardItems already built for that one board, instead of
// waiting for settle()'s bulk hand-off. See main.ts's LoadBoardOptions.onBoard
// and BoardView.reconcileOne.
//
// A SEPARATE, NARROWER GAP THIS FILE DOES NOT PIN, reported instead of
// shipped as an untested "fix": a live run against wss://relay.3dl.network,
// reproducing ready-fd2's STEP 2/5 (open a board this SAME identity already
// opened earlier in the SAME session — so ready-fe4's cache paints it before
// any relay round-trip starts), still showed the exact "Read-only: no board
// finished loading." text for roughly as long as that round-trip took, even
// with this fix applied. Diagnostic instrumentation (reverted) confirmed the
// writer and the DOM update land TOGETHER, atomically, the instant this
// board's own fetch resolves — so nothing in main.ts is delaying it further;
// the remaining width is the real network latency between "cache paint" and
// "this board's own verified answer", which no internal repaint timing can
// close without either not painting the cache (the property ready-fe4 shipped
// and this item requires kept) or authorizing a write before verification (a
// security regression). scripts/live-write-roundtrip.mjs's and
// scripts/live-roundtrip-both-ways.mjs's openBoard() were corrected instead:
// ".card" stopped being proof of a REAL (non-cached) load the moment caching
// shipped, and both now wait for the left-tree node's own `data-board-state`
// to leave "stale" — an existing, purpose-built signal (render.ts: "it is how
// a live run can check... that what the page says about a board is what the
// load found"), not a new sleep or retry.
import { describe, expect, it, vi } from "vitest";
import { afterLogin, type BoardDeps, type Identity } from "./main";
import { authTransition } from "./lib/auth";
import { neverUnwraps } from "./lib/keyunwrap";
import { boardCoord, KIND_BOARD, type DiscoveredBoard } from "./lib/boarddiscovery";
import { KIND_ROLE_GRANT } from "./lib/keyring";
import { signNostrEvent, xOnlyPubkey } from "./lib/schnorrsign";
import { buildFullCreate } from "./board/writeevents";
import type { NostrEvent } from "./lib/nostrevent";
import type { NostrFilter } from "./lib/relay";

// A test-only secp256k1 secret (BIP-340's own test vector, also used by
// main.test.ts's ready-1af block and nostrwriter.test.ts) — the board author
// signs everything below, so `deriveLevels` seats it at LEVEL_MAINTAINER
// unconditionally and the only thing standing between this identity and a
// write is whichever bug is under test.
const OWNER_SECRET = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
const OWNER = xOnlyPubkey(OWNER_SECRET);

const RACE_D = "race-fast";
const SLOW_D = "race-slow";
const GATE_ITEM = "gate-1";

const RACE_BOARD: DiscoveredBoard = {
  coord: boardCoord(OWNER, RACE_D),
  ownerPubkey: OWNER,
  boardD: RACE_D,
  title: "Race Board",
};
const SLOW_BOARD: DiscoveredBoard = {
  coord: boardCoord(OWNER, SLOW_D),
  ownerPubkey: OWNER,
  boardD: SLOW_D,
  title: "Slow Board",
};

const sign = (b: { created_at: number; kind: number; tags: string[][]; content: string }): NostrEvent =>
  signNostrEvent(b, OWNER_SECRET);

/** RACE_BOARD's full snapshot: a real signed create of a GATED item — status
 * waiting, waiting_type gate, a live gate_msg_id — so buildGateRail's
 * gatesFilter() admits it and buildGateResolve has something to render a
 * control (or a read-only note) for. */
const RACE_SNAPSHOT: NostrEvent[] = buildFullCreate(
  {
    signer: OWNER,
    boardAuthor: OWNER,
    boardD: RACE_D,
    boardTitle: RACE_BOARD.title,
    items: new Map(),
    issueEventIds: new Map(),
    createdAt: 1_780_000_000,
  },
  {
    id: GATE_ITEM,
    msg_id: "",
    title: "Needs a ruling",
    context: "",
    type: "task",
    priority: "p1",
    status: "waiting",
    for: OWNER,
    gate: "design",
    waiting_type: "gate",
    waiting_on: "budget approval",
    gate_msg_id: "race-msg-1",
    created_at: 0n,
    updated_at: 0n,
  },
).map(sign);

/** SLOW_BOARD needs only a board definition for discovery — no items are ever
 * folded into it during this test, since its items fetch never resolves. */
const SLOW_BOARD_DEF: NostrEvent[] = buildFullCreate(
  {
    signer: OWNER,
    boardAuthor: OWNER,
    boardD: SLOW_D,
    boardTitle: SLOW_BOARD.title,
    items: new Map(),
    issueEventIds: new Map(),
    createdAt: 1_780_000_000,
  },
  {
    id: "slow-1",
    msg_id: "",
    title: "Unrelated",
    context: "",
    type: "task",
    priority: "p3",
    status: "inbox",
    for: OWNER,
    created_at: 0n,
    updated_at: 0n,
  },
)
  .filter((e) => e.kind === KIND_BOARD)
  .map(sign);

const AUTHORITY_SNAPSHOT: NostrEvent[] = [
  ...RACE_SNAPSHOT.filter((e) => e.kind === KIND_BOARD || e.kind === KIND_ROLE_GRANT),
  ...SLOW_BOARD_DEF,
];

function identity(): Identity {
  // EXTENSION, not read-only: whyReadOnly()'s signer clause fires for a
  // read-only login regardless of this bug, which would make the assertion
  // below true for the wrong reason. A real (faked) window.nostr signer, on a
  // board this identity is the AUTHOR of (deriveLevels seats it at
  // LEVEL_MAINTAINER unconditionally), leaves exactly one thing standing
  // between the gate item and its approve control: whether RACE_BOARD's own
  // writer has reached boardScopedWriter's map yet.
  return { pubkey: OWNER, auth: authTransition({ type: "login", method: "extension" }) };
}

let root: HTMLElement;

describe("ready-c7b — a board's gate rail is writable the moment ITS OWN load lands", () => {
  it("RACE_BOARD's gate is approvable while SLOW_BOARD is still in flight — before settle()", async () => {
    document.body.replaceChildren();
    root = document.createElement("div");
    root.id = "app";
    document.body.append(root);

    // A real (faked) NIP-07 signer for OWNER — genuine BIP-340 signatures, so
    // a refusal below could never be "there was no extension anyway". Same
    // shape as main.test.ts's ready-1af block.
    const origNostr = window.nostr;
    const signEvent = vi.fn(
      async (e: { created_at: number; kind: number; tags: string[][]; content: string }) =>
        signNostrEvent({ created_at: e.created_at, kind: e.kind, tags: e.tags, content: e.content }, OWNER_SECRET),
    );
    window.nostr = { getPublicKey: async () => OWNER, signEvent } as unknown as Window["nostr"];

    try {
      let releaseSlow!: (events: NostrEvent[]) => void;
      const slow = new Promise<NostrEvent[]>((resolve) => {
        releaseSlow = resolve;
      });

      const deps: BoardDeps = {
        loadRelays: async () => ["wss://relay.test"],
        fetchEvents: async (_relays: string[], filter: NostrFilter) => {
          const aTag = filter["#a"];
          if (!aTag) return AUTHORITY_SNAPSHOT; // the one AUTHORITY_KINDS query
          if (aTag[0] === RACE_BOARD.coord) return RACE_SNAPSHOT;
          if (aTag[0] === SLOW_BOARD.coord) return slow; // held open by this test
          throw new Error(`unexpected filter #a=${JSON.stringify(aTag)}`);
        },
        keyUnwrapper: () => neverUnwraps,
        // No cache, no live subscription: irrelevant to this race and each one
        // is its own harness (main.cache.test.ts, main.livesub.test.ts-shaped
        // files) — omitting both keeps this file about exactly one thing.
      };

      const fragment = {
        kind: "portfolio" as const,
        relays: ["wss://relay.test"],
        viewer: OWNER,
        keys: undefined,
      };

      const pending = afterLogin(root, identity(), fragment, deps);

      // Poll, not a fixed await: this is what makes the assertion below
      // independent of exactly how many microtask hops loadBoardItems needs
      // to fold RACE_BOARD. SLOW_BOARD's fetch is still unresolved throughout —
      // nothing here can accidentally race past settle().
      await vi.waitFor(() => {
        expect(root.querySelector(`.gate-item[data-id="${GATE_ITEM}"]`), "RACE_BOARD's gate never painted").not
          .toBeNull();
      });

      // THE ASSERTION. RACE_BOARD's own fold is done and its gate is on screen;
      // SLOW_BOARD has not resolved and settle() cannot have run yet. Before the
      // fix, boardScopedWriter's `writers` Map only gains an entry at settle(),
      // so whyReadOnly() returns "Read-only: no board finished loading." and
      // buildGateResolve renders `.gate-read-only` instead of the controls.
      const control = root.querySelector(`.gate-item[data-id="${GATE_ITEM}"] .gate-resolve`);
      expect(control, "the gate item painted with no resolve control at all").not.toBeNull();
      expect(
        control!.querySelector(".gate-read-only"),
        "RACE_BOARD's gate is READ-ONLY while only SLOW_BOARD is still loading — " +
          (control!.querySelector(".gate-read-only")?.textContent ?? ""),
      ).toBeNull();
      expect(
        control!.querySelector(".gate-reason-input"),
        "the gate has no reason input — it cannot be approved from the browser",
      ).not.toBeNull();
      expect(control!.querySelector(".gate-approve")).not.toBeNull();

      // Cleanup: let SLOW_BOARD land and the load settle, so nothing here
      // leaves a dangling unresolved promise or an unhandled rejection behind.
      releaseSlow([]);
      await pending;
      expect(root.querySelector(`.gate-item[data-id="${GATE_ITEM}"] .gate-reason-input`)).not.toBeNull();
    } finally {
      window.nostr = origNostr;
    }
  });
});
