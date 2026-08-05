// ready.3dl.dev/board: log in with your own key and see every board you can
// read, rendered as the approved board workspace (docs/design/board-prototype.html).
//
// SHAPE OF THIS FILE: it is the composition root and nothing else. Relay ->
// verify -> derive keys -> fold -> mount. Every step below lives in a module
// with its own suite; what is tested HERE (main.test.ts, main.confidential.test.ts)
// is the composition, because that is the layer where a one-line edit can
// disable a property every underlying suite still proves.
//
// TESTABILITY (ready-9b4): this module used to be un-testable — it held the
// #app element in a module-level const and ran main() as an import side
// effect, so nothing could drive the relay -> verify -> DOM path. Two
// changes fix that without restructuring the app: the render helpers take
// their container as a parameter, and afterLogin takes a BoardDeps seam
// whose default is the real relay config + real multi-relay fetcher. See
// main.test.ts, which injects a fake WebSocket serving Go-signed genuine
// events mixed with forged ones and asserts the forged ones never reach the
// DOM (ready-dbf done condition 4).
//
// ready-56b — WHY THERE IS NO BOARD-COORDINATE LIST HERE ANY MORE. This file
// used to open with renderBoards(): a <ul> of "30301:<64 hex>:<d>" strings,
// one per verified board, mounted ABOVE the workspace. It was debug output
// from the discovery milestone that survived into production and was the
// first thing on the deployed page. It exists in no design. The verified board
// set is still in the DOM, and still exactly as assertable — it is the
// left tree's project nodes, which carry data-board-coord — but a coordinate
// is provenance, not chrome, so it is an attribute and not 64 characters of
// rendered text.
//
// BUILD_STAMP is injected at build time via VITE_BUILD_STAMP (see
// .github/workflows/pages.yml) — kept from the ready-2f1 placeholder so the
// orchestrator's post-merge "is the deployed page the one just built" check
// still works.
const BUILD_STAMP: string = import.meta.env.VITE_BUILD_STAMP ?? "dev-local";

import { authTransition, canSign, type AuthTransition } from "./lib/auth";
import { awaitNip07Extension, hasNip07Extension, loginWithExtension, nip44Provider } from "./lib/nip07";
import { NostrBoardWriter, NotAuthorizedError } from "./board/nostrwriter";
import type { BoardWriter } from "./board/write";
import type { Nip07Signer } from "./lib/publish";
import { deriveLevels } from "./lib/rolegrant";
import { decodeNpub, encodeNpub } from "./lib/npub";
import { parseAndStripFragment, type ParsedFragment } from "./lib/fragment";
import type { PortfolioKeys } from "./lib/portfoliokeys";
import { loadOwnBoardsRelays } from "./lib/relayconfig";
import {
  fetchEventsFromRelays,
  subscribeToRelays,
  type FetchEventsOptions,
  type LiveSubscription,
  type LiveSubscriptionOptions,
  type NostrFilter,
  type RelayStatusEvent,
} from "./lib/relay";
import type { NostrEvent } from "./lib/nostrevent";
import { dedupeExact, eventIdentity } from "./lib/nostrevent";
import { discoverOwnerBoards, parseBoardCoord, KIND_BOARD, type DiscoveredBoard } from "./lib/boarddiscovery";
import { applyFragmentKeys, deriveBoardKeyring, KIND_ROLE_GRANT, type BoardKeyring } from "./lib/keyring";
import { nip07KeyUnwrapper, neverUnwraps, type KeyUnwrapper } from "./lib/keyunwrap";
// The §11.13/§11.13a confidentiality decision layer. It lived in this file until
// ready-882 moved it to a module so the conformance runner could drive the REAL
// adapter instead of a substitute; see lib/confidentiality.ts's header.
import {
  confidentialityOf,
  encryptedBoardsOf,
  type Confidentiality,
  type WhyUnestablished,
} from "./lib/confidentiality";
import { PLACEHOLDER } from "./lib/envelope";
import { verifiedEvents } from "./lib/nostrevent";
import { foldItemSource, type ItemSource } from "./lib/itemsource";
import { mountBoardWorkspace, type BoardRef, type BoardWorkspace } from "./board/render";
import type { Item } from "./board/types";
import type { BoardStatus } from "./lib/boardstate";
import {
  admissibleBoards,
  gateFingerprint,
  openBoardCache,
  pruneView,
  scopeKey,
  DEFAULT_LIMITS,
  type BoardCache,
  type CacheStorage,
  type CachedBoard,
  type CachedView,
} from "./lib/boardcache";
import { browserCacheStorage } from "./lib/localcachestorage";
import "./board/board.css";

export type { BoardLoadState, BoardStatus } from "./lib/boardstate";

export interface Identity {
  pubkey: string;
  auth: AuthTransition;
}

/**
 * BoardDeps is the injection seam for everything afterLogin does that reaches
 * outside the page: resolving the relay set, querying relays, and reaching the
 * signer. It exists so a test can serve a scripted relay snapshot; it
 * deliberately does NOT abstract over discoverOwnerBoards or deriveBoardKeyring,
 * because the signature verification inside them is the property under test and
 * must stay un-stubbable from here.
 */
export interface BoardDeps {
  loadRelays: () => Promise<string[]>;
  fetchEvents: (relays: string[], filter: NostrFilter, opts: FetchEventsOptions) => Promise<NostrEvent[]>;
  /**
   * keyUnwrapper resolves the signer that can unwrap a confidential board's CEK
   * for `identity`. It is a function of the identity, not a constant, because of
   * a specific hazard: a visitor may have an extension installed AND be viewing
   * a board through a read-only npub that is NOT the extension's key. Handing
   * the extension's keys to that view would decrypt a board for a pubkey that
   * did not authenticate. So the production implementation returns the real
   * unwrapper only for an identity that can sign — i.e. one whose pubkey came
   * out of getPublicKey() — and the no-keys unwrapper otherwise.
   */
  keyUnwrapper: (identity: Identity) => KeyUnwrapper;
  /**
   * subscribeEvents opens the LIVE subscription that keeps an already-rendered
   * board current (ready-4359). Production supplies the real relay client; a
   * test that does not want a live socket omits it and gets a board that folds
   * once, exactly as before.
   *
   * IT IS OPTIONAL SO THAT OMITTING IT MEANS "NO LIVE UPDATES", NEVER "OPEN A
   * REAL SOCKET". Every board test in this suite builds its own BoardDeps
   * literal; a required field with a real-network default would have had them
   * all dialling wss:// URLs out of jsdom the moment this landed. The production
   * wiring is pinned separately, by main.test.ts asserting defaultDeps carries
   * it — that is the regression this shape trades for.
   */
  subscribeEvents?: (
    relays: readonly string[],
    filter: NostrFilter,
    opts: LiveSubscriptionOptions,
  ) => LiveSubscription;
  /**
   * cacheStorage resolves the store the warm paint reads and the settled load
   * writes (ready-fe4). Production supplies window.localStorage through
   * lib/localcachestorage.ts — the one file in the bundle that names a
   * persistence API.
   *
   * IT IS OPTIONAL SO THAT OMITTING IT MEANS "NO CACHE", NEVER "REACH FOR THE
   * REAL ONE", for the same reason subscribeEvents is optional: every board test
   * in this suite builds its own BoardDeps literal, and a required field with a
   * real-storage default would have them all sharing jsdom's localStorage across
   * cases and painting one test's board into another's. A cache-less page is
   * exactly the page ready-27b shipped, so the fallback is a working page and
   * not a degraded one. The production wiring is pinned separately, by
   * main.cache.test.ts asserting defaultDeps carries it.
   */
  cacheStorage?: () => CacheStorage | undefined;
}

/** Production wiring: same-origin relays.json + the real WebSocket client +
 * the browser extension's NIP-44 namespace. */
export const defaultDeps: BoardDeps = {
  loadRelays: () => loadOwnBoardsRelays(),
  fetchEvents: (relays, filter, opts) => fetchEventsFromRelays(relays, filter, opts),
  keyUnwrapper: (identity) => (canSign(identity.auth) ? nip07KeyUnwrapper(nip44Provider()) : neverUnwraps),
  subscribeEvents: (relays, filter, opts) => subscribeToRelays(relays, filter, opts),
  cacheStorage: () => browserCacheStorage(),
};

/**
 * AUTHORITY_KINDS is the one REQ that decides what this viewer may see: the
 * kind-30301 board definitions AND the kind-39301 role grants, from the owner.
 * They travel together because a confidential board's read key rides INSIDE an
 * owner-signed grant — one query carries both "which boards" and "can I read
 * them". Splitting it would mean deriving a keyring from a second, later
 * snapshot that need not agree with the first.
 */
const AUTHORITY_KINDS = [KIND_BOARD, KIND_ROLE_GRANT];

/**
 * BOARD_KINDS is one board's item stream: cards, the four status kinds, and the
 * role grants a granted contributor's read-trust derives from. It mirrors
 * pkg/sync/nostrinbound.go's BoardSyncFilter exactly.
 *
 * It is a named constant because TWO queries must use the SAME set — the
 * one-shot backfill at load and the live subscription that keeps it current. A
 * live filter narrower than the backfill's would leave a kind that renders at
 * load and then never updates, which is worse than not being live at all: the
 * board would look current and be selectively stale.
 */
const BOARD_KINDS = [30302, 1630, 1631, 1632, 1633, 39301];

/** dedupeSnapshot merges event lists from separate REQs into one snapshot. The
 * single-board path (ready-5c5) needs two REQs — the two authority kinds hang
 * off different tags — and a relay may legitimately serve the same event to
 * both, so the snapshot handed to discoverOwnerBoards / deriveBoardKeyring is
 * de-duplicated first: a grant replayed twice would otherwise be replayed twice
 * by deriveLevels.
 *
 * ready-dd5: it dedups on the FULL event content, never on the self-declared
 * id, because nothing here has verified a signature yet. Keying on the id let a
 * forgery reusing a genuine id evict the genuine event before any consumer
 * (discoverOwnerBoards, deriveBoardKeyring, projectItems — all of which DO
 * verify) ever saw it. */
function dedupeSnapshot(events: NostrEvent[]): NostrEvent[] {
  return dedupeExact(events);
}

function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Partial<HTMLElementTagNameMap[K]> = {},
  children: (Node | string)[] = [],
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  Object.assign(node, props);
  for (const c of children) node.append(c);
  return node;
}

function renderLogin(
  root: HTMLElement,
  fragment: ParsedFragment,
  onIdentity: (id: Identity) => void,
): void {
  root.replaceChildren();
  root.className = "login-page";

  const heading = el("h1", { textContent: "ready — board" });
  const error = el("p", { className: "error" });

  const extBtn = el("button", { textContent: "Log in with browser extension (NIP-07)" });
  extBtn.addEventListener("click", () => {
    error.textContent = "";
    loginWithExtension()
      .then((pubkey) => onIdentity({ pubkey, auth: authTransition({ type: "login", method: "extension" }) }))
      .catch((err: unknown) => {
        error.textContent = err instanceof Error ? err.message : String(err);
      });
  });
  if (!hasNip07Extension()) {
    extBtn.disabled = true;
    extBtn.title = "No NIP-07 extension detected (window.nostr is not present)";
    // ready-48f: a real extension injects window.nostr ASYNCHRONOUSLY and loses
    // this race routinely (see awaitNip07Extension's doc for the measurement),
    // so the disabled state above is a FIRST GUESS, not a verdict. Without this
    // re-check the only NIP-07 control on the page stays dead for the life of a
    // document on which the extension was installed and working.
    void awaitNip07Extension().then((arrived) => {
      if (!arrived || !extBtn.isConnected) return;
      extBtn.disabled = false;
      extBtn.title = "";
    });
  }

  const npubInput = el("input", { type: "text", placeholder: "npub1..." });
  const npubBtn = el("button", { textContent: "View read-only" });
  npubBtn.addEventListener("click", () => {
    error.textContent = "";
    try {
      const pubkey = decodeNpub(npubInput.value.trim());
      onIdentity({ pubkey, auth: authTransition({ type: "login", method: "readOnly" }) });
    } catch (err) {
      error.textContent = err instanceof Error ? err.message : String(err);
    }
  });

  const claimNote =
    fragment.kind === "claim"
      ? el("p", {
          textContent: `This link is an invitation to board ${fragment.payload.board}. Log in above; the board owner will need to grant your key access.`,
        })
      : null;

  const stamp = el("p", { id: "build-stamp" }, [el("code", { textContent: `build:${BUILD_STAMP}` })]);

  root.append(
    heading,
    ...(claimNote ? [claimNote] : []),
    el("section", { className: "login" }, [extBtn, el("div", {}, [npubInput, npubBtn])]),
    error,
    stamp,
  );
}

function renderAwaitingAuthorization(
  root: HTMLElement,
  identity: Identity,
  board: string,
  npub: string,
): HTMLElement {
  const panel = el("section", { className: "awaiting-authorization" }, [
    el("h2", { textContent: "Awaiting authorization" }),
    el("p", {
      textContent: `You are logged in as ${npub}${identity.auth.readOnly ? " (read-only)" : ""}. Ask the owner of board ${board} to grant this key access.`,
    }),
  ]);
  root.append(panel);
  return panel;
}

function renderConnecting(root: HTMLElement): HTMLElement {
  const status = el("p", { className: "connecting", textContent: "Connecting to relays…" });
  root.append(status);
  return status;
}

/**
 * loadBoardItems fetches each board's cards and status events, derives this
 * reader's key material for that board from the owner-signed grants already in
 * hand, and folds the two together into the UI's Item[].
 *
 * ready-56b — WHY THE DECRYPTOR IS THREADED HERE. This function used to build
 * ProjectOptions with `decryptor: null, encryptedBoards: null`, which is an
 * instruction to the fold NOT to decrypt. The fold was conformant, the keyring
 * was correct, and every title on the deployed page still read "[encrypted]",
 * because the projection was being asked for that. The keyring is derived from
 * `authorityEvents` — the same snapshot the board itself came from — so a card
 * can never introduce its own key, and it is derived BEFORE projecting so the
 * fold gate knows the board is confidential even for a reader who holds nothing.
 *
 * Kinds mirror what the Go fold consumes (spec §2): 30302 cards, 1630/1631/1632/
 * 1633 status, and 39301 role grants (a granted contributor's read-trust derives
 * from them).
 *
 * READ-TRUST IS ENFORCED HERE (ready-605), and the argument that used to sit in
 * this comment for `trusted: null` — "the fold re-verifies every event's
 * signature itself, so the gate would be a weaker second check" — is a category
 * error that shipped. A SIGNATURE PROVES AUTHORSHIP, NOT AUTHORITY: any
 * generated keypair produces events that verify. With the gate off, an ungranted
 * key publishing a later kind-30302 for an EXISTING item id on this board's
 * coordinate won latest-wins in every browser viewer's projection — and because
 * a card write REBUILDS THE WHOLE CARD from that projection (writeevents.ts
 * buildCardEvent), its title, context and labels were then re-signed under the
 * VIEWER's key the moment the viewer touched that card. Nothing else caught it:
 * relay write policy is not rd's (measured 2026-07-30, the LAN relays accept a
 * never-granted key — ready-345), so this gate is the only one.
 *
 * THE SET IS GRANT-DERIVED, exactly as rd's CLI builds it (cmd/rd/nostr.go's
 * nostrTrustSet -> pkg/sync/rolegrant.go's DeriveReadTrust): the keys of
 * deriveLevels over the OWNER-SIGNED 39301 grants for this board — the board
 * author (the bootstrap root, always present, which is what makes it
 * non-circular) unioned with every cap-valid grantee, revoked keys included so
 * their PAST events survive (the fold's §3.5 until gate drops their future
 * ones). It is the SAME DerivedGrants object the writer's grantLevels comes
 * from, so the page's read-trust and the writer's write-authority cannot drift
 * apart — the ready-191 shape of defect, where the two projections disagreed.
 *
 * IT IS DELIBERATELY NOT UNIONED WITH `identity.pubkey`, though rd's CLI unions
 * self. rd trusts self because self authored the events in its own local log;
 * this page has no such log, and its identity can come from a LINK — main()
 * mints a read-only identity from the fragment's `pk=`, and the same link
 * chooses the relays. Trusting self here would let a crafted link name the
 * attacker's own key as the viewer and re-admit exactly the card this gate
 * exists to drop. Anything with a legitimate reason to write already holds a
 * grant, so it is already in the set.
 *
 * Failures are non-fatal — a board that will not load must not take the whole
 * page down, which is the ready-62d1 lesson applied one layer up.
 *
 * PER-BOARD KEY SCOPE (ready-df0, made load-bearing by ready-4d9). `fragmentKeys`
 * is keyed by board COORDINATE and looked up with the coordinate of the board
 * being loaded, so a key minted for board A is structurally incapable of being
 * offered to board B. That check used to be `fragmentKeys.coord === b.coord`
 * against a single-board link, and removing it passed the entire suite because a
 * `#board=` link is boardD-filtered by discoverOwnerBoards down to exactly ONE
 * coordinate — there was no second board for a key to leak to. The
 * whole-portfolio link makes the board list genuinely multi-board, so the scope
 * is now enforced in production on every load. It stays witnessed directly:
 * main.fragmentkey.test.ts, "PER-BOARD KEY SCOPE" (single-board keys in a
 * multi-board list) and main.portfolio.test.ts (a portfolio map that covers some
 * boards and not others).
 *
 * CONFIDENTIALITY IS THREE-VALUED HERE (ready-daf). `confidential` is true when
 * any board in the view is confidential — established OR unestablished, because
 * in both cases the board IS confidential and the reader must be told. The
 * separate `unestablished` list names the boards whose cutover could not be
 * established AND why, which is a different statement and gets its own sentences
 * in the UI: see confidentialityOf and unestablishedConfidentialityNotice.
 *
 * ONE BOARD AT A TIME, SEVERAL BOARDS AT ONCE (ready-fe4). The loop used to be
 * strictly serial: fetch board 1, fold board 1, fetch board 2… Measured against
 * wss://relay.3dl.network on 2026-07-30 over 39 boards, that is 73.1s before the
 * reader sees ANYTHING, because nothing is on screen until the last board lands.
 * Two changes, neither of which touches a gate:
 *
 *   - FETCHES OVERLAP, bounded by BOARD_FETCH_CONCURRENCY. The relay half of the
 *     load (measured: 8.3s of 69.1s over 12 boards) now runs while the CPU half
 *     folds, instead of after it. It is bounded and not unbounded because 39
 *     simultaneous sockets to one relay is a different kind of rude.
 *   - EACH BOARD IS REPORTED THE MOMENT IT IS DONE, through `onBoard`. The fold
 *     is single-threaded and CPU-bound — BIP-340 verification is 3.49ms per
 *     event through secp256k1.ts's BigInt arithmetic — so the total will always
 *     be tens of seconds on a portfolio this size. What the reader gets back is
 *     the first board in about a second and the rest streaming in.
 *
 * THE FOLD STILL RUNS BOARD-BY-BOARD, IN ORDER. Only the fetches overlap. That
 * is deliberate: every board's projection depends on its own keyring and its own
 * confidentiality verdict, and interleaving those derivations would make the
 * order in which relays happened to answer part of what the page decides.
 *
 * EXPORTED FOR TESTS ONLY.
 */
export interface LoadBoardOptions {
  /** Called with one board's completed contribution the moment it is folded, so
   * the page can paint it without waiting for the others. */
  onBoard?: (result: {
    coord: string;
    items: Item[];
    status: BoardStatus;
    /** The verdict the fold ran under, carried because the CACHE has to record
     * it and the BoardStatus cannot stand in for it: a confidential board this
     * session holds a key for reports status "open", and an entry that recorded
     * that as "public" would be paintable into a session holding no key at all
     * (boardcache.ts's admissibleBoards). */
    confidentiality: Confidentiality;
    /** The newest created_at this board's snapshot carried — the high-water mark
     * recorded with the cache entry, and the same number the live subscription
     * reconnects on (LiveBoard.newest). */
    newest: number;
    /**
     * ready-c7b: THIS board's writer, built from the SAME fold, handed over the
     * moment this board is done rather than withheld until every board in the
     * portfolio finishes (that wait is what settle() is for, and it used to be
     * the ONLY way a writer reached the page). Waiting for the whole load left a
     * window — the width of every OTHER board's fetch-and-fold — where a
     * freshly-painted board's gate rail had real items but no writer to resolve
     * them with, and boardScopedWriter's fallback ("Read-only: no board finished
     * loading.") was truthfully describing an empty writers Map, not a stale
     * snapshot. Undefined when the board failed to load — there is no writer to
     * hand over, and reconcileOne must not let a stale one linger for a board
     * whose current fold produced nothing.
     */
    writer: NostrBoardWriter | undefined;
  }) => void;
  /** Overrides BOARD_FETCH_CONCURRENCY. Nothing in production sets it. */
  concurrency?: number;
}

/**
 * How many board fetches may be in flight at once. Six is chosen against the
 * measured shape of the load, not tuned: the relay half is ~0.7s per board and
 * the fold half ~3s per board, so six in flight keeps the next board's events
 * always waiting when the fold finishes the current one, with no more sockets
 * open to one relay than a browser opens to one origin anyway.
 */
const BOARD_FETCH_CONCURRENCY = 6;

export async function loadBoardItems(
  boards: DiscoveredBoard[],
  relays: string[],
  authorityEvents: NostrEvent[],
  identity: Identity,
  deps: BoardDeps,
  onStatus: (e: RelayStatusEvent) => void,
  fragmentKeys?: PortfolioKeys,
  options: LoadBoardOptions = {},
): Promise<{
  items: Item[];
  confidential: boolean;
  unestablished: UnestablishedBoard[];
  writers: Map<string, NostrBoardWriter>;
  live: LiveBoard[];
  status: BoardStatus[];
}> {
  const out: Item[] = [];
  const writers = new Map<string, NostrBoardWriter>();
  const live: LiveBoard[] = [];
  let confidential = false;
  const unestablished: UnestablishedBoard[] = [];
  const status: BoardStatus[] = [];
  const unwrap = deps.keyUnwrapper(identity);
  const signing = canSign(identity.auth);

  // ready-fe4: the fetches, started ahead of the fold, at most
  // BOARD_FETCH_CONCURRENCY in flight. A rejected fetch is held here and
  // re-thrown inside the per-board try below, so a board whose relay answer
  // fails still gets its "failed" status rather than an unhandled rejection.
  const inflight = new Map<number, Promise<NostrEvent[]>>();
  const startFetch = (index: number): void => {
    const b = boards[index];
    if (!b || inflight.has(index)) return;
    inflight.set(
      index,
      deps
        // Kinds mirror pkg/sync/nostrinbound.go's BoardSyncFilter exactly.
        .fetchEvents(relays, { kinds: BOARD_KINDS, "#a": [b.coord] }, { onStatus })
        .catch((err: unknown) => Promise.reject(err instanceof Error ? err : new Error(String(err)))),
    );
  };
  const lookahead = Math.max(1, options.concurrency ?? BOARD_FETCH_CONCURRENCY);
  for (let i = 0; i < Math.min(lookahead, boards.length); i++) startFetch(i);

  for (let index = 0; index < boards.length; index++) {
    const b = boards[index];
    startFetch(index);
    startFetch(index + lookahead);
    try {
      const pending = inflight.get(index);
      const events = pending === undefined ? [] : await pending;
      inflight.delete(index);
      /**
       * deriveRead is the WHOLE read-side derivation for this board, in ONE
       * place: keyring, confidentiality decision, quarantine set, read-trust set
       * and the ItemSource built from them.
       *
       * ready-48f made it a function rather than a straight line so the live
       * subscription can redo it when an OWNER-SIGNED grant arrives, instead of
       * the page holding its load-time authority snapshot for the life of the
       * tab. It is deliberately the same code both times: a second, live-only
       * derivation is exactly how the page and rd would come to disagree.
       */
      const deriveRead = async (authority: NostrEvent[], snapshot: NostrEvent[]) => {
        const keyring = await deriveBoardKeyring(authority, identity.pubkey, b.ownerPubkey, b.boardD, unwrap);
        // ready-df0/ready-4d9: a key-bearing link supplies the CEK the page could
        // never unwrap for itself (no secret key here, so no ECDH). Looked up by
        // THIS board's coordinate, so a key the link carries for another board is
        // never applied here — with a portfolio link the map genuinely holds other
        // boards' keys, which is what makes this lookup a control rather than a
        // formality. Applied AFTER derivation so it adds keys without displacing
        // anything the signed grants established, cutover above all. See
        // applyFragmentKeys.
        const lk = fragmentKeys?.get(b.coord);
        if (lk) applyFragmentKeys(keyring, b.coord, lk);
        // ready-daf: three-valued, and computed from the board's OWN snapshot
        // plus the link — not from the presence of relay-supplied grants alone,
        // whose absence used to read as "public".
        const { state, why } = confidentialityOf(keyring, b.coord, snapshot, lk !== undefined);
        const encryptedBoards = encryptedBoardsOf(keyring, state);
        // ready-605: the read-trust set for THIS board, derived from the
        // OWNER-SIGNED authority snapshot and used by BOTH the page's projection
        // below and the writer's own (via grantLevels). See this function's
        // header for why it is grant-derived and why self is not in it. Because
        // this is the only thing that decides trust, re-deriving it from a LATER
        // snapshot cannot admit anything the owner did not sign — see
        // LiveBoard.refreshAuthority.
        const grants = deriveLevels(verifiedEvents(authority), b.ownerPubkey, b.boardD);
        const src = foldItemSource(
          {
            trusted: new Set(grants.levels.keys()),
            maintainers: null,
            pinnedBoard: b.coord,
            decryptor: keyring,
            encryptedBoards,
          },
          b.coord,
        );
        return { keyring, state, why, encryptedBoards, grants, src, granted: grants.levels.has(identity.pubkey) };
      };

      const read = await deriveRead(authorityEvents, events);
      const { keyring, state, encryptedBoards, grants, src } = read;
      if (state !== "public") confidential = true;
      if (state === "unknown") unestablished.push({ name: b.title || b.boardD, why: read.why ?? "no-grant" });
      const boardItems = src.loadItems(events);
      out.push(...boardItems);

      // ready-191: the WRITE-side envelope. A confidential board is writable from
      // this page exactly while the session holds its CEK — the same key the read
      // above just used, from the same keyring, so a board whose cards render is
      // a board whose cards can be republished, and one that renders
      // "[encrypted]" stays read-only and says so.
      //
      // THE EPOCH IS currentEpoch(), THE HIGHEST HELD — not the newest grant seen
      // and not the epoch of the card being edited. See BoardKeyring.currentEpoch's
      // doc: a member who missed a rotation seals under its stale highest epoch,
      // which the owner (who minted the rotation and self-wrapped it) can still
      // read; sealing under any other held epoch publishes a card part of the
      // board cannot read, and nothing on the READ path would ever report it.
      const epoch = keyring.currentEpoch(b.coord);
      const cek = epoch === null ? null : keyring.cek(b.coord, epoch);
      const enc =
        state !== "public" && epoch !== null && cek !== null
          ? { cek, epoch, ltk: keyring.ltk(b.coord) }
          : null;

      // ready-b2b: the WRITER for this board, built from the same snapshot the
      // read just folded. It is per-board because authority is per-board — the
      // grant levels, the owner pubkey and whether the board is confidential all
      // differ across the portfolio, and a writer that averaged them would be
      // wrong for every board but one.
      const writer = new NostrBoardWriter({
        signerPubkey: identity.pubkey,
        signer: canSign(identity.auth) ? nip07Signer() : undefined,
        board: { ownerPubkey: b.ownerPubkey, boardD: b.boardD, title: b.title },
        relays,
        snapshot: events,
        // The SAME derivation the read-trust set above came from — one signed
        // source feeds both, so what this writer may publish and what the page
        // is willing to project can never disagree (ready-605).
        grantLevels: grants.levels,
        confidential: state !== "public",
        enc,
        // The writer projects its own view to build the next write from; it
        // must decrypt and quarantine exactly as the read above did, or it
        // would refuse every write on a confidential board (refuseRedacted)
        // and show the user a different board than the page does.
        decryptor: keyring,
        encryptedBoards,
      });
      writers.set(b.coord, writer);

      // ready-4359: everything the live subscription needs to re-fold THIS board
      // when the relay pushes something new — the events it has, the fold that
      // produced these items (same options, same keyring: a re-fold under
      // different options would be a second, divergent projection), and the
      // writer whose snapshot must stay in step with it.
      const newest = events.reduce(
        (max, e) => (typeof e.created_at === "number" && e.created_at > max ? e.created_at : max),
        0,
      );
      const lb: LiveBoard = {
        coord: b.coord,
        events: [...events],
        seen: new Set(events.map(eventIdentity)),
        newest,
        items: boardItems,
        src,
        writer,
        authority: [...authorityEvents],
        granted: read.granted,
        refreshAuthority: async () => {},
      };
      // ready-48f: RE-DERIVE THE READ SIDE when the relay pushes an owner-signed
      // grant. See LiveBoard.refreshAuthority for what this may and may not
      // change, and why it cannot admit an event the owner did not sign.
      lb.refreshAuthority = async () => {
        const next = await deriveRead(lb.authority, lb.events);
        lb.src = next.src;
        lb.granted = next.granted;
      };
      live.push(lb);

      // ready-27b: what this reader can honestly say about THIS board, decided
      // here because this is the only place the board's own keyring, its
      // confidentiality verdict and this session's signing capability are all in
      // scope at once. See boardStatusOf.
      const boardStatus = boardStatusOf(b, state, keyring, signing);
      status.push(boardStatus);
      // ready-fe4: this board is DONE — paint it now rather than after the rest.
      // The callback is invoked with the board's OWN admitted items, never with
      // the accumulator, so a consumer cannot mistake a partial view for a
      // complete one.
      options.onBoard?.({
        coord: b.coord,
        items: boardItems,
        status: boardStatus,
        confidentiality: state,
        newest,
        writer,
      });
    } catch (err) {
      inflight.delete(index);
      // ready-27b: a board that will not load is REPORTED, not dropped. The
      // others still render — that stance is unchanged, and it is the ready-62d1
      // lesson one layer up — but the board keeps its node in the left tree and
      // that node now says the load failed. Before this, the node was still
      // there (it comes from discovery, which succeeded) showing a count of 0,
      // which is the page asserting "this board has no work" about a board it
      // never managed to read: the one outcome the portfolio view must never
      // produce, because it is indistinguishable from the truth.
      const failed: BoardStatus = {
        coord: b.coord,
        name: b.title || b.boardD,
        state: "failed",
        detail:
          `This board did not load: ${err instanceof Error ? err.message : String(err)}. ` +
          `Its cards are NOT shown and its count here is not a count of its work — reload, or check the relays in this link.`,
      };
      status.push(failed);
      // ready-fe4: reported through the SAME channel a successful board is, with
      // zero items. That is what evicts this board's CACHED items from the paint
      // — a board that has just failed to load must not keep showing the cards a
      // previous session read off it under a node that now says the load failed.
      // A board that did not load has no verdict; "unknown" is the fail-closed
      // stand-in, and `save` refuses to cache a failed board at all.
      options.onBoard?.({
        coord: b.coord,
        items: [],
        status: failed,
        confidentiality: "unknown",
        newest: 0,
        writer: undefined,
      });
    }
  }
  return { items: out, confidential, unestablished, writers, live, status };
}

/**
 * boardStatusOf decides ONE board's load state from its own keyring and
 * confidentiality verdict.
 *
 * WHY "HOLDS A KEY" IS THE TEST FOR "open", rather than "no placeholders
 * rendered": a board can hold a key and still have individual cards sealed under
 * an epoch this reader missed, and a board can render zero placeholders simply by
 * being empty. currentEpoch() !== null is the property that actually decides
 * whether this session can open this board's content at all, and it is the same
 * property the WRITE path seals under (see the enc branch above), so the state
 * shown to the reader and the state the writer acts on cannot drift apart.
 *
 * THE READ-ONLY WORDING IS DIFFERENT ON PURPOSE. A link session holds no signing
 * key, so it can never unwrap a grant — telling such a reader to ask for a grant
 * would send them to fix the wrong thing. What they need is a link minted with
 * this board's key in it.
 *
 * "withholding" OUTRANKS EVERY KEY QUESTION, and that order is the point of the
 * state existing. Holding a key is not the same as seeing the board: on an
 * unestablished cutover the fold drops every card that is not a sealed envelope
 * whether or not this reader can decrypt, so a keyholder gets a SHORT board and
 * the old "do I hold a key?" test called that board open. The key situation is
 * still said, in the same sentence, because it is still true and still
 * actionable — it just cannot be the headline when the count itself is short.
 */
function boardStatusOf(
  board: DiscoveredBoard,
  state: Confidentiality,
  keyring: BoardKeyring,
  signing: boolean,
): BoardStatus {
  const name = board.title || board.boardD;
  const base = { coord: board.coord, name };
  const held = keyring.currentEpoch(board.coord) !== null;
  if (state === "unknown") {
    return {
      ...base,
      state: "withholding",
      detail:
        `THE COUNT SHOWN FOR THIS BOARD IS SHORT. It is confidential, and the owner-signed grants that reached this page do not establish WHEN it became confidential, ` +
        `so every card on it that is not a sealed envelope is WITHHELD — a card written before the board went confidential cannot be told apart from cleartext written after. ` +
        `\`rd list\` in this project will show more items than this board does. The paragraph above says which evidence is missing.` +
        (held ? "" : ` This session also holds no read key for this board, so the cards that do render show ${PLACEHOLDER}.`),
    };
  }
  if (state === "public" || held) {
    return { ...base, state: "open", detail: "" };
  }
  if (signing && keyring.granteeGrants(board.coord) > 0) {
    return {
      ...base,
      state: "unreadable-grant",
      detail:
        `This board is confidential and its owner DID grant this key access — but this browser could not open the key in that grant, ` +
        `so every card below is shown as ${PLACEHOLDER}. A grant issued before mid-2026 wraps its key in a form no browser extension can return intact; ` +
        `the owner re-issues it with \`rd confidential rewrap\`. A declined extension prompt looks identical from here, so try reloading first.`,
    };
  }
  return {
    ...base,
    state: "sealed",
    detail: signing
      ? `This board is confidential and no owner-signed grant naming this key reached this page, so every card below is shown as ${PLACEHOLDER}. Ask this board's owner to grant this key access.`
      : `This board is confidential and this link carries no read key for it, so every card below is shown as ${PLACEHOLDER}. This session holds no signing key, so it cannot unwrap one either — ask for a link minted with this board's key.`,
  };
}

/**
 * LiveBoard is one board's re-foldable state: the events already folded, the
 * fold that produced the current items, and the writer built from the same
 * snapshot.
 *
 * READ AUTHORITY IS LIVE; WRITE AUTHORITY IS NOT (ready-48f, narrowing
 * ready-4359's "authority is not live").
 *
 * The original rule froze `src` — keyring, confidentiality gate, read-trust set
 * — at load, on the reasoning that "re-deriving any of that from the live stream
 * would let a pushed event change what this session can read or is willing to
 * write". Measured against the walk that rule exists to make possible
 * (scripts/live-stranger-walk.mjs), it also froze the thing the product is FOR:
 * the owner ran `rd grant --claim`, the kind-39301 landed on the open page — it
 * is in BOARD_KINDS, so the subscription had it all along — and the titles
 * stayed "[encrypted]" until a reload, because the keyring that could open them
 * was never re-derived.
 *
 * WHY RE-DERIVING IS SAFE, AND WHAT ACTUALLY GUARDS IT. The guard was never the
 * frozen snapshot; it is the derivation. deriveBoardKeyring and deriveLevels
 * schnorr-verify every event and drop any grant not signed by THIS BOARD'S
 * OWNER (rolegrant.ts's `g.boardOwner !== boardAuthor` and signerMayGrant), so a
 * hostile or merely noisy relay cannot introduce authority by pushing events —
 * exactly as it cannot at load, where the same events arrive over the same
 * socket. What a later snapshot CAN do is what it should: admit a key the owner
 * has just granted, and drop one the owner has just revoked. That is what rd
 * projects from the same events, which is the property this whole page is built
 * to preserve.
 *
 * WHAT IS STILL FROZEN: `writer`. Write authority, the write-side CEK and the
 * confidentiality decision the writer was constructed with are NOT re-derived,
 * so no pushed event can turn a session that could not write into one that can.
 * A key granted while the page is open reads live and must reload to write.
 * That is the conservative half of the original rule, kept deliberately.
 */
export interface LiveBoard {
  coord: string;
  /** Every event this board has folded, live ones appended. It only grows: a
   * superseded card is still evidence the fold needs (latest-wins is decided
   * over the whole set), so nothing here can be pruned without making the page's
   * projection depend on when it was opened. A session that sits open through
   * thousands of writes therefore grows; a reload compacts it. */
  events: NostrEvent[];
  /** eventIdentity of everything in `events`, so a re-served event is not
   * appended twice. Content-keyed, never id-keyed — lib/relay.ts's reason. */
  seen: Set<string>;
  /** Newest created_at folded so far: the live REQ's `since` cursor. */
  newest: number;
  /** This board's CURRENT projection — the one the load produced, replaced each
   * time this board is re-folded. Carried so the first live event does not have
   * to re-fold every OTHER board just to reassemble the view. */
  items: Item[];
  src: ItemSource;
  writer: NostrBoardWriter;
  /** The owner-signed authority snapshot `src` was derived from — the kind-30301
   * board definitions and kind-39301 grants the load fetched, plus every grant
   * the live subscription has appended since. */
  authority: NostrEvent[];
  /** Whether THIS viewer's key is in the derived grant levels for this board.
   * The page's "awaiting authorization" panel is a statement about exactly this,
   * so it is read from the derivation rather than inferred from whether a title
   * happened to decrypt. */
  granted: boolean;
  /** Re-runs the READ-side derivation over `authority` (see this interface's
   * doc). Called by startLiveUpdates before a re-fold, and only when a kind-39301
   * has actually arrived — every other pushed event leaves authority untouched
   * and skips the NIP-44 round trip to the signer that this costs. */
  refreshAuthority: () => Promise<void>;
}

/** Milliseconds of quiet before a burst of pushed events is folded. One rd
 * command publishes several events (a card plus its status event, sometimes a
 * grant), and they arrive within milliseconds of each other; folding on each one
 * would re-verify every signature on the board N times and flash N intermediate
 * states through the DOM. Short enough that a human reads it as immediate. */
const LIVE_COALESCE_MS = 150;

/**
 * startLiveUpdates is the reverse direction of the board's write path
 * (ready-4359 done condition 4): a change made anywhere else — the rd CLI on
 * another machine, a second browser, a teammate — reaches the OPEN page with no
 * reload.
 *
 * It re-folds rather than patching. The pushed event is appended to the board's
 * event list and the WHOLE board is projected again through the SAME
 * ItemSource — so what the page shows after a live update is, by construction,
 * what `rd` would project from the same events. A patch path would be a second
 * implementation of the fold, and the failure mode this epic exists to prevent
 * is precisely the client and rd disagreeing.
 *
 * The writer absorbs the same events (NostrBoardWriter.absorb, which explains
 * why at length): the snapshot every subsequent write is BUILT from must not
 * stay at the state the page has stopped showing.
 *
 * EXPORTED FOR TESTS — main.test.ts drives it with a scripted relay and asserts
 * the DOM changes with no second afterLogin call.
 */
export function startLiveUpdates(args: {
  boards: LiveBoard[];
  relays: readonly string[];
  subscribe: NonNullable<BoardDeps["subscribeEvents"]>;
  onItems: (items: Item[]) => void;
  coalesceMs?: number;
}): LiveSubscription {
  const { boards, relays, subscribe, onItems } = args;
  const coalesceMs = args.coalesceMs ?? LIVE_COALESCE_MS;
  const subs: LiveSubscription[] = [];
  let pending: ReturnType<typeof setTimeout> | undefined;
  let closed = false;

  /**
   * The last projection of each board, so an event on ONE board does not re-fold
   * the other 23. A `--portfolio` link is genuinely multi-board (ready-4d9) and
   * every fold re-verifies every signature on the board it folds, so folding all
   * of them per pushed event would make one busy board slow the whole view down.
   * Only the boards that actually received something are re-folded; the rest are
   * reused verbatim, which is the same array the previous fold produced.
   */
  const dirty = new Set<string>();

  /**
   * Boards that received an owner-signed-candidate kind-39301 since the last
   * fold, and therefore need their READ authority re-derived before it (see
   * LiveBoard's doc for why that is safe and what it deliberately does not
   * touch). Separate from `dirty` because re-derivation costs a NIP-44 round
   * trip to the extension per grant, and the overwhelmingly common live event is
   * a card, not a grant.
   */
  const authorityDirty = new Set<string>();

  const refold = async (): Promise<void> => {
    pending = undefined;
    if (closed) return;
    for (const b of boards) {
      if (!authorityDirty.has(b.coord)) continue;
      try {
        await b.refreshAuthority();
      } catch {
        // Keep the authority this board already had. A grant that cannot be
        // derived (a signer that refused, a malformed wrap) must not take away
        // the access the page already has.
      }
    }
    authorityDirty.clear();
    if (closed) return;
    const items: Item[] = [];
    for (const b of boards) {
      if (dirty.has(b.coord)) {
        try {
          b.items = b.src.loadItems(b.events);
        } catch {
          // Keep the last good projection for THIS board and carry on with the
          // others — the same stance loadBoardItems takes at load ("skip this
          // board; the others still render"). One board that cannot fold must
          // not stop every other board on the page from updating, and it must
          // not leave the view empty either.
        }
      }
      items.push(...b.items);
    }
    dirty.clear();
    onItems(items);
  };

  for (const b of boards) {
    subs.push(
      subscribe(
        relays,
        // `#a`, never `authors` — see lib/relay.ts's NO `authors` FILTER note.
        // `since` is the newest instant already folded, so a reconnect asks for
        // the gap rather than the board.
        { kinds: BOARD_KINDS, "#a": [b.coord], since: b.newest > 0 ? b.newest : undefined },
        {
          onEvent: (e) => {
            if (closed) return;
            const key = eventIdentity(e);
            // The subscription de-duplicates within itself; this catches the
            // overlap with the events the initial LOAD already folded, which it
            // has never seen.
            if (b.seen.has(key)) return;
            b.seen.add(key);
            b.events.push(e);
            if (typeof e.created_at === "number" && e.created_at > b.newest) b.newest = e.created_at;
            b.writer.absorb([e]);
            dirty.add(b.coord);
            // ready-48f: a role grant is AUTHORITY as well as an event to fold.
            // It is appended to the authority snapshot and the board is marked
            // for re-derivation; nothing here decides whether it counts —
            // deriveLevels/deriveBoardKeyring verify the signature and the board
            // owner, and drop it if it is neither.
            if (e.kind === KIND_ROLE_GRANT) {
              b.authority.push(e);
              authorityDirty.add(b.coord);
            }
            if (pending === undefined) pending = setTimeout(() => void refold(), coalesceMs);
          },
        },
      ),
    );
  }

  return {
    close(): void {
      closed = true;
      if (pending !== undefined) clearTimeout(pending);
      pending = undefined;
      for (const s of subs) s.close();
    },
  };
}

/** nip07Signer returns the extension's SIGNING surface, or undefined when it
 * cannot sign. Kept beside the read-side nip44Provider for the same reason that
 * one exists: the page reaches the extension through exactly two narrow
 * functions, and neither of them can ever be a key. */
function nip07Signer(win: Window = window): Nip07Signer | undefined {
  const ns = win.nostr as (Window["nostr"] & Partial<Nip07Signer>) | undefined;
  return ns && typeof ns.signEvent === "function" ? (ns as unknown as Nip07Signer) : undefined;
}

/**
 * boardScopedWriter routes each write to the writer for the board the ITEM
 * lives on. The board view is explicitly cross-board, so "the writer" is not one
 * object: authority, confidentiality and the owner pubkey are per-board
 * properties, and an item's board coordinate is the only thing that says which
 * set applies. An item whose board produced no writer (it failed to load) is
 * refused rather than written to a neighbouring board.
 *
 * EXPORTED FOR TESTS.
 */
export function boardScopedWriter(
  writers: Map<string, NostrBoardWriter>,
  itemBoard: Map<string, string>,
): BoardWriter {
  const pick = (itemId: string): NostrBoardWriter => {
    const coord = itemBoard.get(itemId);
    const w = coord ? writers.get(coord) : undefined;
    if (!w) {
      throw new NotAuthorizedError(
        `no writable board is loaded for item ${itemId} — reload the board, or check that the owner ` +
          `granted this key access`,
      );
    }
    return w;
  };
  const on = <T>(itemId: string, f: (w: NostrBoardWriter) => Promise<T>): Promise<T> => {
    try {
      return f(pick(itemId));
    } catch (err) {
      return Promise.reject(err);
    }
  };
  return {
    moveStatus: (id, to) => on(id, (w) => w.moveStatus(id, to)),
    resolveGate: (id, approve, reason) => on(id, (w) => w.resolveGate(id, approve, reason)),
    claim: (id, reason) => on(id, (w) => w.claim(id, reason)),
    close: (id, resolution, reason) => on(id, (w) => w.close(id, resolution, reason)),
    setTitle: (id, title) => on(id, (w) => w.setTitle(id, title)),
    setPriority: (id, priority) => on(id, (w) => w.setPriority(id, priority)),
    setLabel: (id, label, present) => on(id, (w) => w.setLabel(id, label, present)),
    whyReadOnly: () => {
      // The board-level answer, for the detail pane's actions block: read-only
      // only when EVERY loaded board is. With a mixed portfolio the actions stay
      // available and the per-board writer refuses the ones it must.
      const reasons = [...writers.values()].map((w) => w.whyReadOnly());
      if (reasons.length === 0) return "Read-only: no board finished loading.";
      const blocking = reasons.filter((r) => r !== undefined);
      return blocking.length === reasons.length ? blocking[0] : undefined;
    },
  };
}

/**
 * keyOwners lists the distinct owner pubkeys named by a portfolio link's key
 * material. Derived from the board COORDINATES the keys are filed under, so it
 * cannot disagree with the scope those keys are applied at.
 */
function keyOwners(keys: PortfolioKeys | undefined): string[] {
  if (!keys) return [];
  const out: string[] = [];
  for (const coord of keys.keys()) {
    const parsed = parseBoardCoord(coord);
    if (parsed) out.push(parsed.owner);
  }
  return out;
}

/**
 * fragmentKeyMap normalizes BOTH key-bearing link shapes to the one thing
 * loadBoardItems consumes: coordinate -> key material.
 *
 * A single-board link's keys belong to the ONE coordinate the same fragment
 * names, and that binding is made here, once, at the only place both halves are
 * in scope. Downstream there is no "the board this link was about" — only a
 * coordinate lookup — which is why board A's key cannot reach board B.
 */
function fragmentKeyMap(fragment: ParsedFragment): PortfolioKeys | undefined {
  if (fragment.kind === "board" && fragment.keys) return new Map([[fragment.board, fragment.keys]]);
  if (fragment.kind === "portfolio") return fragment.keys;
  return undefined;
}

/**
 * confidentialNotice states BOTH halves — what was read and what was not —
 * because either half alone misleads. "N decrypted" with no mention of the rest
 * hides that the view is partly invisible; "N sealed" with no mention of the
 * rest reads like a failure even when most of it rendered.
 *
 * `boardCount` exists because ready-4d9 made the multi-board case real: saying
 * "this board is confidential" over a 24-board portfolio misstates both what is
 * sealed and whose owner to ask about it.
 */
function confidentialNotice(items: Item[], boardCount: number): string {
  const sealed = items.filter((i) => i.title === PLACEHOLDER);
  const opened = items.length - sealed.length;
  const subject = boardCount > 1 ? `${boardCount} boards in this view are confidential` : "This board is confidential";
  const owner = boardCount > 1 ? "Ask those boards' owners" : "Ask the board owner";
  const parts = [
    opened > 0
      ? `${subject}; ${opened} of ${items.length} titles were decrypted in your browser.`
      : `${subject}.`,
  ];
  if (sealed.length > 0) {
    parts.push(
      `${sealed.length} of ${items.length} items are sealed to a key you do not hold — they show ${PLACEHOLDER}. ${owner} to grant this key access.`,
    );
  }
  return parts.join(" ");
}

/** UnestablishedBoard names one board whose cutover could not be established,
 * with the reason, for the notice below. */
interface UnestablishedBoard {
  name: string;
  why: WhyUnestablished;
}

/**
 * unestablishedConfidentialityNotice is the UI half of ready-daf: it says, in
 * the reader's own words, that the page could NOT establish a board's
 * confidentiality state — instead of presenting that board as public, which is
 * what the page did before.
 *
 * The wording is constrained the same way unservedBoardsNotice's is, and for the
 * same reason: it must claim exactly what this page can support. It CAN say "the
 * grants that reached this page do not establish the cutover". It CANNOT say "the
 * owner published no grant" (a relay may have omitted it) and it must not imply
 * the view is complete. Both halves are said out loud, because either alone
 * misleads: silence reads as "public", and "confidential" alone reads as though
 * the quarantine is precise when in fact it is withholding cards it cannot
 * classify.
 *
 * ROUND 2 ADDS A SECOND, STRONGER SENTENCE for the boards where omission is
 * PROVEN rather than merely unruleoutable. The two are genuinely different facts
 * and a reader acts on them differently: "no grant arrived" is consistent with an
 * indexing gap and says nothing about the relay's honesty, whereas "a
 * signature-verified card on this board contradicts the grants that arrived" says
 * the answer is internally inconsistent and the relay is not serving what it
 * holds. Collapsing them into one hedged sentence would have understated the
 * second and overstated the first.
 *
 * ready-f6b ADDS A THIRD SENTENCE, AND KEEPS IT SEPARATE FROM THE SECOND for the
 * same reason the second exists. Witness C proves the omission from the served
 * GRANTS — their lowest key epoch is above 1, and every confidential board starts
 * at epoch 1 — with no card involved at all. Folding those boards into the
 * card-carried sentence would tell the reader that "a signature-verified sealed
 * card on the board is older than the earliest grant the relays served", which on
 * this shape is simply not true of the evidence: the whole point of the case is
 * that no card contradicts anything. The label must not assert more than the page
 * can prove, so it says what it actually has — the grant set starts too high.
 *
 * Returns "" when every confidential board's cutover was established, so the
 * ordinary case adds no paragraph.
 */
function unestablishedConfidentialityNotice(boards: UnestablishedBoard[]): string {
  if (boards.length === 0) return "";
  const list = [...boards.map((b) => b.name)].sort().join(", ");
  const subject =
    boards.length > 1
      ? `${boards.length} boards in this view carry confidential content`
      : "This board carries confidential content";
  const parts = [
    `CONFIDENTIALITY STATE COULD NOT BE ESTABLISHED: ${subject} (${list}), but the owner-signed grants served by the relays this page reached do not establish WHEN it became confidential. ` +
      `A missing event is not evidence of anything: reads here are unrestricted by design, so any relay can simply omit one. This page therefore treats the board as confidential with an UNKNOWN cutover rather than as public. ` +
      `Every card on it that is not a sealed envelope is WITHHELD from this view, because a card published before the board went confidential cannot be told apart from cleartext published after. Do not read this view as complete, and do not read it as a public board.`,
  ];
  const withheld = boards
    .filter((b) => b.why === "grants-withheld")
    .map((b) => b.name)
    .sort();
  if (withheld.length > 0) {
    parts.push(
      `ON ${withheld.join(", ")} THE OMISSION IS PROVEN, not merely possible: a signature-verified sealed card on the board is older than the earliest grant the relays served, or names a key epoch BELOW the lowest epoch any served grant covers. ` +
        `Either one is only possible if grants OLDER than the ones served exist, so the instant those grants imply is too late and cannot be used. ` +
        `No relay can forge this signal — sealing a card needs a board key and signing it needs its author's key — and it cannot hide the signal without also withholding the cards that carry it.`,
    );
  }
  const firstEpoch = boards
    .filter((b) => b.why === "first-epoch-missing")
    .map((b) => b.name)
    .sort();
  if (firstEpoch.length > 0) {
    parts.push(
      `ON ${firstEpoch.join(", ")} THE OMISSION IS PROVEN BY THE GRANTS THEMSELVES: every owner-signed grant that reached this page names a key epoch ABOVE 1, and a board's first key is always key epoch 1. ` +
        `The grant that minted key epoch 1 is therefore missing from this answer, and it is older than every grant that arrived — so the instant those grants imply is too late and cannot be used. ` +
        `No card is involved in this one: it is the served grants that do not add up, so no card the board keeps or drops can change it.`,
    );
  }
  return parts.join(" ");
}

/**
 * unservedBoardsNotice names the boards a link carries KEYS for that no relay in
 * this link served a DEFINITION for. They render as nothing at all: discovery
 * only mints a coordinate from a verified kind-30301, so a board with no
 * published definition is not "empty", it is absent — and absent is
 * indistinguishable from "this board has no work" unless the page says so.
 *
 * This is not hypothetical. The first live run of `rd board --portfolio
 * --with-key` on the real portfolio emitted keys for 15 boards while
 * wss://relay.3dl.network served kind-30301 definitions for 10 of them, so five
 * boards silently did not appear. The keys are proof the reader is entitled to
 * those boards, but their absence here is NOT proof of anything about the
 * relay's contents — ready-5c5 found the earlier wording ("the boards are not
 * on the relay") to be actively FALSE: the relay's author-index REQ under-
 * returns deterministically (measured: 42 of 56 boards for a paged
 * kind+authors query vs. 56 of 56 for a paged kind-only query, same relay,
 * same run), so "this discovery pass did not surface a definition" and "no
 * definition is published" are two different claims and this function can
 * only ever establish the first one. Wording below must not assert the
 * second.
 *
 * Returns "" when nothing is missing, so the notice does not grow a paragraph
 * for the ordinary case.
 */
function unservedBoardsNotice(keys: PortfolioKeys | undefined, boards: DiscoveredBoard[]): string {
  if (!keys) return "";
  const found = new Set(boards.map((b) => b.coord));
  const missing = [...keys.keys()].filter((coord) => !found.has(coord));
  if (missing.length === 0) return "";
  const names = missing
    .map((coord) => parseBoardCoord(coord)?.boardD ?? coord)
    .sort()
    .join(", ");
  return (
    `This link also carries read keys for ${missing.length} board(s) that this discovery pass did not find a definition for, so they are NOT shown at all: ${names}. ` +
    `The keys are here, so this is not a permission problem — but it does not establish that these boards are unpublished. It may be a relay reachability or indexing gap; check the relay directly before assuming a republish is needed.`
  );
}

/**
 * droppedRelaysNotice names relay= entries fragment.ts rejected before this
 * page ever tried them (ready-280). Those entries never reach fetchEvents at
 * all — they are not a connection that failed, they are a scheme this
 * page's origin cannot open (ws:// from https, or any non-wss:// scheme),
 * so silence here would look like the relay just never answered rather than
 * "this link is carrying a dead entry, minted before ready-634's mint-side
 * filter, or crafted." Reported instead of silently dropped, per the same
 * rule unservedBoardsNotice above already follows for a different gap.
 *
 * Returns "" when nothing was dropped, so the common case (every relay= entry
 * already wss://) adds no paragraph.
 */
function droppedRelaysNotice(fragment: ParsedFragment): string {
  if (fragment.kind !== "board" && fragment.kind !== "portfolio") return "";
  const dropped = fragment.droppedRelays;
  if (!dropped || dropped.length === 0) return "";
  return (
    `This link's relays= parameter named ${dropped.length} relay(s) this page could not open and never tried: ${dropped.join(", ")}. ` +
    `A browser on a secure page cannot open a ws:// (non-TLS) socket — that is not a permission or reachability problem, it is a rule the browser enforces with no override. ` +
    `The board opens using its other relay(s), if any, or its own configured default when the whole list was unusable.`
  );
}

function safeEncodeNpub(pubkeyHex: string): string {
  try {
    return encodeNpub(pubkeyHex);
  } catch {
    return pubkeyHex;
  }
}

/** What a tree node says while its board is painted from the previous visit
 * rather than from this session's own read. Both sentences refuse to let the
 * count be read as a claim about the project's work — the ready-27b rule, one
 * state further out. */
/** Milliseconds of quiet before the workspace is rebuilt for boards that have
 * landed. Same value and same reason as LIVE_COALESCE_MS: short enough that a
 * human reads it as immediate, long enough that a burst of boards costs one
 * render instead of one each. */
const REPAINT_COALESCE_MS = 150;

const STALE_DETAIL_CACHED =
  "THE CARDS SHOWN FOR THIS BOARD ARE FROM THIS BROWSER'S LAST VISIT, not from this session's own read of the relay. " +
  "They are being re-read now; the count, and every state above, may change when the answer lands. Nothing here is a claim about the board as it is right now.";
const STALE_DETAIL_UNREAD =
  "This board has not been read yet in this session, so its cards are NOT shown and the count beside it is not a count of its work. " +
  "It is being read now.";

/**
 * BoardView owns everything on screen between the first paint and the settled
 * load: the workspace, which board's items are showing, and the maps the writer
 * routes through (ready-fe4).
 *
 * WHY IT IS AN OBJECT AND NOT MORE LOCALS IN afterLogin. The page now paints
 * three times over one load — from cache, then per board as each lands, then
 * settled — and every one of those paints has to agree about which items belong
 * to which board, which boards have a writer, and what each tree node is
 * entitled to claim. Holding that in one place is what makes "a cached board is
 * REPLACED, never merged" a single line (reconcileOne) instead of a rule spread
 * across three call sites.
 *
 * THE ONE INVARIANT WORTH STATING: `painted` is keyed by board coordinate and
 * every write to it is a whole-board REPLACE. Nothing here merges a fresh fold
 * into a cached one, because a merge is how a card the current fold WITHHELD
 * (ready-475 is chasing 167 such cards on the live `ready` board) would survive
 * from a stale entry. When a board's own load finishes, that board shows exactly
 * what the fold admitted and nothing else.
 */
interface BoardView {
  /** Read the cache and mount what this session may paint. No awaits. */
  paintFromCache(): void;
  /** Drop everything the freshly discovered board set does not cover. */
  reconcileBoards(boards: DiscoveredBoard[]): void;
  /** Replace ONE board's items and tree node with its completed load. */
  reconcileOne(r: {
    coord: string;
    items: Item[];
    status: BoardStatus;
    confidentiality: Confidentiality;
    newest: number;
    /** ready-c7b: this board's writer, attached the moment this board's own
     * fold is done — see LoadBoardOptions.onBoard. Undefined on a failed load,
     * which must evict any writer a previous fold left behind for this coord. */
    writer: NostrBoardWriter | undefined;
  }): void;
  /** Replace every item (the live-subscription path) and keep routing correct. */
  replaceAll(items: Item[]): void;
  /** The load is complete: attach the real writers, the notice, the states. */
  settle(args: { writers: Map<string, NostrBoardWriter>; notice: string; status: BoardStatus[] }): BoardWorkspace;
  /** Write what is now on screen back for the next visit. */
  save(status: BoardStatus[], notice: string): void;
}

function boardView(
  root: HTMLElement,
  identity: Identity,
  fragment: ParsedFragment,
  deps: BoardDeps,
  linkKeys: PortfolioKeys | undefined,
): BoardView {
  const signing = canSign(identity.auth);
  // THE SCOPE NAMES THE BOARD WHENEVER THE LINK DOES — both link shapes that
  // name one, `#board=` and ready-48f's claim link. A claim link's cache filed
  // under a bare "claim" would be shared by every claim link this viewer opens,
  // and two claim links for two boards carry no key material to tell apart, so
  // their fingerprints would match and board A's cards would paint onto board
  // B's page for the length of one discovery round-trip.
  const scope = scopeKey(
    fragment.kind,
    fragment.kind === "board" ? fragment.board : fragment.kind === "claim" ? fragment.payload.board : "",
  );
  const storage = deps.cacheStorage?.();
  const cache: BoardCache | undefined = storage ? openBoardCache(storage, identity.pubkey, scope) : undefined;

  /** coord -> the items currently on screen for that board. Replace-only. */
  const painted = new Map<string, Item[]>();
  /** coord -> the tree node currently on screen for that board. */
  const refs = new Map<string, BoardRef>();
  /** coord -> newest created_at folded, for the next visit's cache entry. */
  const high = new Map<string, number>();
  /** coord -> the confidentiality verdict this session's fold used. */
  const states = new Map<string, Confidentiality>();
  // Both maps are handed to boardScopedWriter ONCE and MUTATED afterwards, so
  // the writer the workspace holds stays correct as boards arrive. Empty at the
  // cached paint is exactly right: no board has finished loading, so every write
  // is refused ("Read-only: no board finished loading.") and a cached card
  // cannot be dragged into a publish against authority this session has not
  // re-established.
  //
  // ready-c7b: the mutation happens TWICE, and the first one is the fix. Before
  // this, `writers` was only ever populated in settle() — once per LOAD, after
  // EVERY board in the portfolio finished. reconcileOne() painted each board's
  // real items as soon as that board's own fold landed, so a freshly-opened
  // single board could be fully on screen, gate rail included, while `writers`
  // was still the empty Map from mount() — a script or a fast user acting on
  // that rail saw "Read-only: no board finished loading.", truthfully, because
  // the board's own writer sat unused in loadBoardItems's local map for the
  // width of every OTHER board's fetch-and-fold. reconcileOne() now attaches
  // (or evicts) THIS board's writer the moment its own fold is done; settle()'s
  // bulk assignment still runs afterwards and is now merely idempotent for any
  // board reconcileOne already delivered.
  const writers = new Map<string, NostrBoardWriter>();
  const itemBoard = new Map<string, string>();

  let workspace: BoardWorkspace | undefined;
  const allItems = (): Item[] => {
    const out: Item[] = [];
    for (const list of painted.values()) out.push(...list);
    return out;
  };
  const reindex = (items: Item[]): void => {
    for (const i of items) if (i.boardCoord) itemBoard.set(i.id, i.boardCoord);
  };

  const mount = (notice: string | undefined): void => {
    const items = allItems();
    reindex(items);
    workspace = mountBoardWorkspace(root, items, {
      writer: boardScopedWriter(writers, itemBoard),
      viewerId: identity.pubkey,
      boards: [...refs.values()],
      identityLine: `Logged in as ${safeEncodeNpub(identity.pubkey)}${signing ? "" : " (read-only)"}`,
      emptyBoardsNote: "No boards found.",
      notice,
    });
  };

  const repaintNow = (): void => {
    pendingRepaint = undefined;
    if (workspace === undefined) return;
    const items = allItems();
    reindex(items);
    workspace.setBoards([...refs.values()]);
    workspace.setItems(items);
  };

  /**
   * COALESCED, for the same reason startLiveUpdates coalesces its re-folds and
   * with the same constant. render() rebuilds the whole workspace, and a
   * 79-board portfolio settles 79 times over a load; repainting synchronously on
   * each one would rebuild a DOM of thousands of cards 79 times and put the
   * rendering cost back where the fetch concurrency just took it out of. The
   * delay is invisible to a person and the FIRST paint is never delayed by it —
   * mount() is synchronous, and settle() flushes.
   */
  let pendingRepaint: ReturnType<typeof setTimeout> | undefined;
  const repaint = (): void => {
    if (workspace === undefined) return;
    if (pendingRepaint === undefined) pendingRepaint = setTimeout(repaintNow, REPAINT_COALESCE_MS);
  };
  const flushRepaint = (): void => {
    if (pendingRepaint !== undefined) clearTimeout(pendingRepaint);
    repaintNow();
  };

  return {
    paintFromCache(): void {
      const stored = cache?.read();
      if (!stored) return;
      // THE GATE. Only the boards whose admitting decision this session would
      // put the same inputs to; see boardcache.ts's gateFingerprint.
      const admissible = admissibleBoards(stored, {
        viewer: identity.pubkey,
        signing,
        keysFor: (coord) => linkKeys?.get(coord),
      });
      if (admissible.length === 0) return;
      // The notice is a statement about the WHOLE view — "24 boards in this
      // view are confidential", with names. Painting it beside a subset would
      // have it naming boards that are not on screen, so it travels only when
      // the subset is the whole set. The per-board detail below always travels.
      const wholeView = admissible.length === stored.boards.length;
      for (const b of admissible) {
        painted.set(b.coord, b.items);
        high.set(b.coord, b.high);
        states.set(b.coord, b.state);
        refs.set(b.coord, {
          coord: b.coord,
          title: b.title || "(confidential board)",
          state: "stale",
          detail: STALE_DETAIL_CACHED,
        });
      }
      mount(wholeView ? stored.notice : undefined);
    },

    reconcileBoards(boards: DiscoveredBoard[]): void {
      const discovered = new Set(boards.map((b) => b.coord));
      for (const coord of [...painted.keys()]) {
        if (!discovered.has(coord)) {
          painted.delete(coord);
          refs.delete(coord);
          high.delete(coord);
          states.delete(coord);
        }
      }
      for (const b of boards) {
        if (refs.has(b.coord)) {
          refs.set(b.coord, { ...refs.get(b.coord)!, title: b.title || refs.get(b.coord)!.title });
          continue;
        }
        refs.set(b.coord, {
          coord: b.coord,
          title: b.title || "(confidential board)",
          state: "stale",
          detail: STALE_DETAIL_UNREAD,
        });
      }
      if (workspace === undefined) {
        // Nothing was admissible from cache, so this is the first thing on
        // screen. Mount now: the tree of boards, every node saying it has not
        // been read, is a truer "connecting…" than the sentence it replaces.
        if (refs.size > 0) mount(undefined);
        return;
      }
      // FLUSH: this call REMOVES boards the current discovery did not find, and
      // a board that is gone must leave the screen at once rather than linger
      // for a coalesce interval.
      flushRepaint();
    },

    reconcileOne(r): void {
      // REPLACE. Not merge — see the invariant in this interface's doc.
      painted.set(r.coord, r.items);
      states.set(r.coord, r.confidentiality);
      high.set(r.coord, r.newest);
      // ready-c7b: attach (or evict) THIS board's writer now, at the moment its
      // own fold lands, instead of waiting for settle() to hand over the whole
      // portfolio's writers at once. `writers` is the same Map boardScopedWriter
      // closed over at mount() — mutating it here is what a script or user
      // acting on THIS board's freshly-painted gate rail sees immediately,
      // without waiting on every other board in the load.
      if (r.writer) writers.set(r.coord, r.writer);
      else writers.delete(r.coord);
      refs.set(r.coord, {
        coord: r.coord,
        title: refs.get(r.coord)?.title ?? r.status.name,
        state: r.status.state,
        detail: r.status.detail,
      });
      if (workspace === undefined) mount(undefined);
      else repaint();
    },

    replaceAll(items: Item[]): void {
      painted.clear();
      for (const i of items) {
        const coord = i.boardCoord ?? "";
        const list = painted.get(coord);
        if (list) list.push(i);
        else painted.set(coord, [i]);
      }
      reindex(items);
    },

    settle({ writers: fresh, notice, status }): BoardWorkspace {
      for (const [coord, w] of fresh) writers.set(coord, w);
      const statusOf = new Map(status.map((s) => [s.coord, s]));
      for (const [coord, ref] of refs) {
        const s = statusOf.get(coord);
        refs.set(coord, {
          ...ref,
          state: s?.state ?? "failed",
          detail:
            s?.detail ??
            "This board did not load and reported no reason, so nothing here describes its contents.",
        });
      }
      if (workspace === undefined) mount(notice !== "" ? notice : undefined);
      else {
        // FLUSH, not schedule: the load is over, so the very next thing the
        // caller (and any test) reads off the DOM must be the settled board and
        // not whatever the last coalesced tick left there.
        flushRepaint();
        workspace.setNotice(notice !== "" ? notice : undefined);
      }
      return workspace!;
    },

    save(status: BoardStatus[], notice: string): void {
      if (!cache) return;
      const statusOf = new Map(status.map((s) => [s.coord, s]));
      const now = Date.now();
      const boards: CachedBoard[] = [];
      for (const [coord, items] of painted) {
        const s = statusOf.get(coord);
        // A board this session did NOT successfully read contributes nothing:
        // caching a "failed" node's (empty) items would be caching an absence,
        // and caching a board whose status never arrived would be caching an
        // answer nobody gave.
        const verdict = states.get(coord);
        if (!s || s.state === "failed" || verdict === undefined) continue;
        // A board whose cutover this session could NOT establish is never
        // cached, and an existing entry for it is therefore dropped by this
        // write. "unknown" is precisely the verdict that says the grants which
        // reached this page do not add up (ready-daf/ready-f6b), so its admitted
        // items are the ones whose admission is least worth repeating: a
        // grandfathered cleartext card admitted under a cutover a LATER session
        // proves was manufactured must not keep painting from local bytes. The
        // reader loses the warm paint on exactly the boards the page already
        // tells them it cannot vouch for.
        if (verdict === "unknown") continue;
        boards.push({
          coord,
          title: refs.get(coord)?.title ?? s.name,
          // THE FOLD'S OWN VERDICT, carried through onBoard — never inferred
          // from the load state. A confidential board this session holds a key
          // for loads "open", and recording that as "public" would make its
          // decrypted titles paintable into a session with no key path at all.
          state: verdict,
          boardState: s.state,
          detail: s.detail,
          gate: gateFingerprint({ viewer: identity.pubkey, signing, linkKeys: linkKeys?.get(coord) }),
          high: high.get(coord) ?? 0,
          items,
        });
      }
      const view: CachedView = {
        v: 1,
        viewer: identity.pubkey,
        scope,
        savedAt: now,
        notice: notice !== "" ? notice : undefined,
        boards,
      };
      cache.write(pruneView(view, { ...DEFAULT_LIMITS, now }));
    },
  };
}

/**
 * The live subscription belonging to the board currently on screen, closed
 * before another one is mounted. afterLogin can run more than once in a session
 * (log in, hit an error, log in again); two live subscriptions on the same
 * relays would both re-fold into a workspace only one of them still owns, and
 * the sockets of the abandoned one would stay open for the life of the page.
 */
let activeLive: LiveSubscription | undefined;

export async function afterLogin(
  root: HTMLElement,
  identity: Identity,
  fragment: ParsedFragment,
  deps: BoardDeps = defaultDeps,
): Promise<void> {
  root.replaceChildren();
  root.className = "board-page";
  activeLive?.close();
  activeLive = undefined;

  // ready-48f — A CLAIM LINK NOW OPENS THE BOARD IT INVITES YOU TO.
  //
  // WHAT IT USED TO DO, AND WHY THAT WAS NOT ENOUGH. This branch rendered the
  // awaiting-authorization panel and RETURNED, contacting no relay at all, on the
  // reading that "a claim link is an invitation, not an authorization". The
  // authorization half of that is right and is UNCHANGED — nothing below grants
  // this key anything, and the read-trust gate in loadBoardItems still admits
  // only owner-signed grants. What was wrong is the conclusion drawn from it:
  // the invitee was shown a BLANK page, so the objective this page exists for —
  // a stranger with a link reaches a POPULATED board — was false on the very
  // link `rd board share` mints for a stranger. Measured end to end
  // (scripts/live-stranger-walk.mjs): 0 cards, then still 0 cards after the
  // owner ran `rd grant --claim`, because a page that never opened a
  // subscription cannot pick one up either.
  //
  // THE EXPOSURE THIS ADDS IS NONE. Everything fetched below is what the RELAY
  // already serves to anyone who asks for that coordinate, and the claim token
  // was minted BY THE OWNER and handed to this recipient with the coordinate
  // inside it. `rd join <token>` — the CLI half of the very same token,
  // advertised in `rd board share`'s own help — already syncs exactly these
  // events for exactly this person. A confidential board's free text stays
  // sealed: with no grant there is no CEK, so every title renders the
  // "[encrypted]" placeholder until the owner's kind-39301 arrives.
  //
  // The panel stays on screen while this key holds no grant, and is removed the
  // moment one arrives (below), so the page never says "awaiting authorization"
  // over a board it has just decrypted.
  const claim = fragment.kind === "claim" ? fragment.payload : undefined;
  let awaitingPanel: HTMLElement | undefined;
  if (claim) {
    awaitingPanel = renderAwaitingAuthorization(root, identity, claim.board, safeEncodeNpub(identity.pubkey));
  }
  // The workspace's render() calls replaceChildren() on its container, so on the
  // claim path it gets a mount of its own and the panel above survives. Every
  // other path keeps mounting on `root` itself, byte for byte as before.
  const mount = claim ? el("div", { className: "board-mount" }) : root;
  if (claim) root.append(mount);

  const connecting = renderConnecting(root);
  const onStatus = (e: RelayStatusEvent) => {
    connecting.textContent = `Connecting to ${e.relay}… (${e.status}${e.attempt > 0 ? `, attempt ${e.attempt + 1}` : ""})`;
  };

  // ready-fe4: EVERYTHING FROM HERE TO paintFromCache() RUNS WITH NO `await`.
  // That is the whole point of the warm paint — the first relay round-trip on
  // this page is a WebSocket to a scale-to-zero relay that can take ~12s to
  // answer, so anything that waits on it has already lost. localStorage is
  // synchronous; the cached board is on screen before the socket is dialled.
  const linkKeys = fragmentKeyMap(fragment);
  const view = boardView(mount, identity, fragment, deps, linkKeys);
  view.paintFromCache();

  try {
    let relays: string[];
    let boards: DiscoveredBoard[];
    let authorityEvents: NostrEvent[];

    // A claim link names exactly one board and its relays, so it takes the
    // single-board path below — the SAME queries, the SAME discovery and the
    // SAME read-trust gate a `#board=` link takes. It differs in nothing except
    // that the panel above says the owner has not granted this key yet.
    const linkedBoard = claim ? claim.board : fragment.kind === "board" ? fragment.board : "";
    const linkedRelays = claim ? claim.relays : fragment.kind === "board" ? fragment.relays : [];

    if (linkedBoard !== "") {
      const parsedCoord = parseBoardCoord(linkedBoard);
      if (!parsedCoord) throw new Error(`main: malformed board coordinate ${JSON.stringify(linkedBoard)}`);
      relays = linkedRelays.length > 0 ? linkedRelays : await deps.loadRelays();
      // ready-5c5: TWO REQs, because the two authority kinds are addressed by
      // DIFFERENT tags and no single NIP-01 filter can name both. Within one
      // filter every condition ANDs, so a `#a`-scoped AUTHORITY_KINDS REQ asks
      // for "events of kind 30301 or 39301 that carry a=<coord>" — and a
      // kind-30301 board definition carries NO "a" tag at all (its tags are d,
      // title, optional archived, optional p — pkg/sync/nostrwire.go's
      // BuildBoardEvent). Measured against wss://relay.3dl.network 2026-07-29:
      // that filter returns the board's grants and ZERO board definitions, so
      // the page rendered "No boards". Only a relay that IGNORES tag filters
      // hides this, which is exactly what the old fixtures did.
      //
      // Neither REQ carries `authors`: a relay's author index is free to
      // under-return (measured on the same relay, same day: a paged
      // kind+authors REQ served 42 of an owner's 56 boards while a paged
      // kind-only REQ served all 56). Ownership is enforced client-side —
      // discoverOwnerBoards schnorr-verifies every candidate and drops any
      // author outside `[parsedCoord.owner]`, and deriveLevels/
      // deriveBoardKeyring do the same for grants (rolegrant.ts's
      // `g.boardOwner !== boardAuthor` check and signerMayGrant) — so dropping
      // `authors` from the wire filter loses no security property.
      const [boardDefs, boardGrants] = await Promise.all([
        // "d" is the addressable identifier ON the 30301 event itself.
        deps.fetchEvents(relays, { kinds: [KIND_BOARD], ["#d"]: [parsedCoord.boardD] }, { onStatus }),
        // "a" is the board coordinate ON each 39301 grant.
        deps.fetchEvents(relays, { kinds: [KIND_ROLE_GRANT], ["#a"]: [linkedBoard] }, { onStatus }),
      ]);
      authorityEvents = dedupeSnapshot([...boardDefs, ...boardGrants]);
      boards = discoverOwnerBoards(authorityEvents, [parsedCoord.owner], parsedCoord.boardD);
    } else if (fragment.kind === "portfolio") {
      // ready-4d9. The WHOLE portfolio: no boardD filter, so discovery returns
      // every board these owners published. The owner set is the viewer plus
      // every owner named in the link's own key material — the viewer alone
      // would miss a confidential board owned by someone else that this key was
      // granted read access to, and the link is carrying that board's key.
      //
      // ready-5c5: no boardD is known ahead of time here, so neither a "#d"
      // nor a "#a" filter is available (unlike the single-board case above) —
      // the query is kind-scoped only, NOT authors-scoped, because `authors`
      // is exactly the filter measured to under-return on
      // wss://relay.3dl.network. Owners are still enforced, just client-side:
      // discoverOwnerBoards schnorr-verifies every kind-30301 and drops any
      // whose author is not in this set, so a hostile or merely noisy relay
      // cannot add a board here, and the keyring is still derived per board
      // from owner-signed grants below.
      //
      // Kind-only is BROAD — it asks for every author's 30301/39301, not one
      // author's — and a relay caps what one REQ returns regardless of what
      // the client asked for (measured: 500 on wss://relay.3dl.network). That
      // is why relay.ts walks `until` backwards until a page adds nothing new;
      // without it this widened filter would trade an under-returning author
      // index for a silently truncated first page.
      relays = fragment.relays.length > 0 ? fragment.relays : await deps.loadRelays();
      const owners = [...new Set([identity.pubkey, ...keyOwners(fragment.keys)])];
      authorityEvents = await deps.fetchEvents(relays, { kinds: AUTHORITY_KINDS }, { onStatus });
      boards = discoverOwnerBoards(authorityEvents, owners);
    } else {
      // fragment.kind === "none": own-boards discovery (ready-dbf done condition 3).
      // ready-5c5: kind-scoped only AND paged, see the portfolio branch's
      // comment above — this is the exact query the veracity finding measured
      // under-returning 14 of 56 boards via `authors`.
      relays = await deps.loadRelays();
      authorityEvents = await deps.fetchEvents(relays, { kinds: AUTHORITY_KINDS }, { onStatus });
      boards = discoverOwnerBoards(authorityEvents, [identity.pubkey]);
    }

    // ready-fe4: the discovered board set REPLACES whatever the cache painted.
    // A board that is no longer discoverable — unpublished, archived, or simply
    // outside this link's scope — must lose its node and its cards here, before
    // a single fresh event has been folded, because from this moment the page
    // has a current answer to "which boards" and the cached one is superseded.
    view.reconcileBoards(boards);

    const { items, confidential, unestablished, writers, live, status } = await loadBoardItems(
      boards,
      relays,
      authorityEvents,
      identity,
      deps,
      onStatus,
      linkKeys,
      // ready-fe4: each board lands as it finishes. The cached items for that
      // board are REPLACED, never merged — see BoardView.reconcileOne.
      { onBoard: (r) => view.reconcileOne(r) },
    );
    connecting.remove();

    const notice = [
      confidential ? confidentialNotice(items, boards.length) : "",
      unestablishedConfidentialityNotice(unestablished),
      unservedBoardsNotice(linkKeys, boards),
      droppedRelaysNotice(fragment),
    ]
      .filter((s) => s !== "")
      .join(" ");

    // ready-fe4: the load is complete, so what is on screen is now exactly what
    // this session's own fold produced — nothing cached survives — and THAT is
    // what gets written back for the next visit.
    const workspace = view.settle({ writers, notice, status });
    view.save(status, notice);

    // ready-48f: the panel is a statement about THIS key's standing on the
    // board, so it is driven by the derived grant levels — the same
    // owner-signed derivation the read-trust gate uses — and not by "did we
    // manage to decrypt something". A key that is already granted when the link
    // is opened never sees it at all.
    const dropAwaitingPanel = (): void => {
      if (awaitingPanel && live.some((b) => b.granted)) {
        awaitingPanel.remove();
        awaitingPanel = undefined;
      }
    };
    dropAwaitingPanel();

    // ready-4359 done condition 4. Without this the page folds ONCE and a change
    // made anywhere else is invisible until the human reloads. Any previous
    // subscription was already closed at the top of this function.
    activeLive = deps.subscribeEvents
      ? startLiveUpdates({
          boards: live,
          relays,
          subscribe: deps.subscribeEvents,
          onItems: (next) => {
            // ready-fe4: the BoardView owns the item -> board index the writer
            // routes through, so the live path hands it the new projection
            // rather than mutating a map of its own. Same reason ready-4359
            // mutated rather than rebuilt: an item the rd CLI creates after this
            // page loaded must be writable, not merely visible.
            view.replaceAll(next);
            workspace.setItems(next);
            dropAwaitingPanel();
          },
        })
      : undefined;
  } catch (err) {
    connecting.textContent = err instanceof Error ? err.message : String(err);
  }
}

/**
 * main is the entry point. `deps` exists for the same reason afterLogin takes
 * one — but for a property afterLogin cannot carry: the IDENTITY main() mints
 * from a `pk=` fragment is built HERE, and whether that identity can sign is a
 * security control (see the comment at the mint site below). A test that
 * constructs its own read-only identity and hands it to afterLogin proves
 * nothing about this file; it has to observe the identity main() actually made.
 * The seam is what lets it, by handing back every identity the composition
 * routes through keyUnwrapper. Production passes nothing and gets defaultDeps.
 */
export function main(deps: BoardDeps = defaultDeps): void {
  const root = document.getElementById("app");
  if (!root) return;

  // ready-62d1: a malformed #rd1_ fragment must not take the page down. It used
  // to: parseAndStripFragment threw here with no catch, so the whole module died
  // and NOTHING rendered -- no heading, no NIP-07 button, no npub form, no error.
  // The user could not even log in to recover, on the first-touch surface for a
  // shared invite. Degrade to a normal login page with a visible notice. The
  // fragment is stripped either way; see parseAndStripFragment's `finally`.
  let fragment: ParsedFragment;
  let fragmentError = false;
  try {
    fragment = parseAndStripFragment();
  } catch {
    fragment = { kind: "none" };
    fragmentError = true;
  }

  // ready-df0: an `rd board --with-key` link names, in `pk=`, the PUBLIC pubkey
  // it was minted for, so there is nothing left for the visitor to supply — no
  // extension to install, no npub to paste. Skip the login form and open the
  // board directly.
  //
  // `method: "readOnly"` IS A SECURITY CONTROL, NOT A LABEL. The owner approved
  // putting a CEK in a URL specifically because a CEK cannot sign, and this line
  // is what makes that true of the resulting session: canSign() false means
  // defaultDeps.keyUnwrapper returns neverUnwraps, so an unrelated extension
  // that happens to be installed is never asked to nip44.decrypt grants for a
  // pubkey that did not authenticate — nobody proved they hold `pk=`'s secret;
  // they only proved they were sent a link naming it. It is also what keeps the
  // "(read-only)" marker on the identity line.
  //
  // ready-1af: THE WRITE GATE IS NOT HERE, and NO WRITE CONTROL READS A
  // `readOnly` FLAG. The AuthState field of that name is real (auth.ts) but has
  // exactly two readers in the whole page: canSign() itself (`loggedIn &&
  // !readOnly`), and renderAwaitingAuthorization's sentence on the claim-link
  // path. Nothing else — WorkspaceOptions (render.ts) has no such field,
  // BoardWriter (write.ts) has no such method, and the read-only note the board
  // UI shows is obtained by ASKING THE WRITER (`this.writer.whyReadOnly?.()`,
  // render.ts). An earlier revision of this comment claimed the flag was "what
  // every write control gates on"; no such gate existed anywhere.
  //
  // What actually gates a write is two steps away, in loadBoardItems below:
  // `signer: canSign(identity.auth) ? nip07Signer() : undefined`. canSign()
  // being false here is what makes that `undefined`, so the NostrBoardWriter
  // built for every board this identity sees is constructed with NO signer,
  // REGARDLESS of whether a NIP-07 extension is installed.
  // NostrBoardWriter.whyReadOnly() (nostrwriter.ts) then refuses every write,
  // and applyNow() re-checks whyReadOnly() before building or signing a single
  // event — belt AND suspenders, neither one this comment.
  //
  // WHICH REASON THE USER IS TOLD, IN THE WRITER'S ACTUAL ORDER: whyReadOnly()
  // tests CONFIDENTIALITY first, then SIGNER PRESENCE, then GRANT LEVEL. So a
  // read-only session on a confidential board is told about the seal, not about
  // the missing signer — the write is refused either way, but do not read the
  // signer branch as the first thing consulted (the round-1 fix of this very
  // item asserted exactly that, wrongly). The order is pinned, not assumed, by
  // main.test.ts's "ready-1af" ORDER 1/2 and ORDER 2/2 cases, each of which
  // makes two branches true at once so only the real order satisfies it.
  //
  // Pinned end to end by main.test.ts's "ready-1af: the control that actually
  // refuses a browser write" block: one board, one item that genuinely exists
  // in the snapshot, a real BIP-340-capable window.nostr installed for both
  // cases, and only the auth method differing — the read-only case rejects with
  // NotAuthorizedError and never reaches the signer (that is what witnesses
  // applyNow's re-check), while the extension case signs. The writer's write
  // authority there is NOT a grant event — no grant event is passed at all;
  // rolegrant.ts's deriveLevels seats a board's own author at LEVEL_MAINTAINER
  // implicitly, which is what leaves the signer branch as the only possible
  // explanation for the refusal. The three refusal branches are also pinned one
  // by one, at the unit layer, by nostrwriter.test.ts's "who may write
  // (client-side, and BEFORE anything is signed)".
  //
  // Flipping it to "extension" is a one-word edit that silently converts a
  // bearer READ link into a session the page treats as signing-capable, so it is
  // witnessed directly: main.fragmentkey.test.ts, "the pk= identity CANNOT SIGN",
  // and (for the write side) the ready-1af block named above.
  //
  // Decryption comes from the fragment's own keys, threaded through afterLogin.
  //
  // ready-de7: THIS BRANCH IS NOW THE ONLY WAY A CEK ENTERS THE PAGE. It used to
  // be possible to arrive at the login form below still holding link keys — a
  // `#board=<coord>&cek=...` fragment with no `pk=` parsed fine, `linkViewer` was
  // undefined, and a visitor who then logged in with an extension reached
  // loadBoardItems with a SIGNING identity and the link's CEKs, which is exactly
  // the premise applyFragmentKeys' grant-check bypass assumes away. fragment.ts
  // now refuses that shape, so `fragment.keys.ceks` non-empty implies `pk=`
  // implies this branch implies read-only. Witnessed by main.fragmentkey.test.ts,
  // "A CEK CANNOT REACH A SIGNING SESSION".
  //
  // ready-4d9: a `--portfolio` link is the same act at portfolio scope — it also
  // names its viewer in pk=, and it also opens read-only for exactly the reasons
  // above. Its keys buy MORE reading (every board, not one) and still zero
  // signing, so the identity it mints is the same read-only shape.
  const linkViewer = fragment.kind === "portfolio" || fragment.kind === "board" ? fragment.viewer : undefined;
  if (linkViewer) {
    const identity: Identity = {
      pubkey: linkViewer,
      auth: authTransition({ type: "login", method: "readOnly" }),
    };
    void afterLogin(root, identity, fragment, deps);
    return;
  }

  renderLogin(root, fragment, (identity) => {
    void afterLogin(root, identity, fragment, deps);
  });

  // Appended AFTER renderLogin, which calls root.replaceChildren() and would
  // otherwise wipe this notice.
  if (fragmentError) {
    root.append(
      el("p", {
        className: "fragment-error",
        textContent:
          "That board link is not valid — it may have been truncated in transit. " +
          "Ask whoever shared it for a fresh link, or log in above to see your own boards.",
      }),
    );
  }
}

main();
