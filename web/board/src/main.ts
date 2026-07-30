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
import { hasNip07Extension, loginWithExtension, nip44Provider } from "./lib/nip07";
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
import { applyFragmentKeys, deriveBoardKeyring, KIND_ROLE_GRANT } from "./lib/keyring";
import { nip07KeyUnwrapper, neverUnwraps, type KeyUnwrapper } from "./lib/keyunwrap";
// The §11.13/§11.13a confidentiality decision layer. It lived in this file until
// ready-882 moved it to a module so the conformance runner could drive the REAL
// adapter instead of a substitute; see lib/confidentiality.ts's header.
import { confidentialityOf, encryptedBoardsOf, type WhyUnestablished } from "./lib/confidentiality";
import { PLACEHOLDER } from "./lib/envelope";
import { verifiedEvents } from "./lib/nostrevent";
import { foldItemSource, type ItemSource } from "./lib/itemsource";
import { mountBoardWorkspace } from "./board/render";
import type { Item } from "./board/types";
import "./board/board.css";

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
}

/** Production wiring: same-origin relays.json + the real WebSocket client +
 * the browser extension's NIP-44 namespace. */
export const defaultDeps: BoardDeps = {
  loadRelays: () => loadOwnBoardsRelays(),
  fetchEvents: (relays, filter, opts) => fetchEventsFromRelays(relays, filter, opts),
  keyUnwrapper: (identity) => (canSign(identity.auth) ? nip07KeyUnwrapper(nip44Provider()) : neverUnwraps),
  subscribeEvents: (relays, filter, opts) => subscribeToRelays(relays, filter, opts),
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
): void {
  const panel = el("section", { className: "awaiting-authorization" }, [
    el("h2", { textContent: "Awaiting authorization" }),
    el("p", {
      textContent: `You are logged in as ${npub}${identity.auth.readOnly ? " (read-only)" : ""}. Ask the owner of board ${board} to grant this key access.`,
    }),
  ]);
  root.append(panel);
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
 * `trusted: null` disables the read-trust gate (spec §3.4) deliberately: the
 * fold re-verifies every event's signature itself (fold.ts, proven by the
 * forged_events_dropped vector), so the gate would be a weaker second check.
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
 * EXPORTED FOR TESTS ONLY.
 */
export async function loadBoardItems(
  boards: DiscoveredBoard[],
  relays: string[],
  authorityEvents: NostrEvent[],
  identity: Identity,
  deps: BoardDeps,
  onStatus: (e: RelayStatusEvent) => void,
  fragmentKeys?: PortfolioKeys,
): Promise<{
  items: Item[];
  confidential: boolean;
  unestablished: UnestablishedBoard[];
  writers: Map<string, NostrBoardWriter>;
  live: LiveBoard[];
}> {
  const out: Item[] = [];
  const writers = new Map<string, NostrBoardWriter>();
  const live: LiveBoard[] = [];
  let confidential = false;
  const unestablished: UnestablishedBoard[] = [];
  const unwrap = deps.keyUnwrapper(identity);

  for (const b of boards) {
    try {
      const events = await deps.fetchEvents(
        relays,
        // Kinds mirror pkg/sync/nostrinbound.go's BoardSyncFilter exactly.
        { kinds: BOARD_KINDS, "#a": [b.coord] },
        { onStatus },
      );
      const keyring = await deriveBoardKeyring(
        authorityEvents,
        identity.pubkey,
        b.ownerPubkey,
        b.boardD,
        unwrap,
      );
      // ready-df0/ready-4d9: a key-bearing link supplies the CEK the page could
      // never unwrap for itself (no secret key here, so no ECDH). Looked up by
      // THIS board's coordinate, so a key the link carries for another board is
      // never applied here — with a portfolio link the map genuinely holds other
      // boards' keys, which is what makes this lookup a control rather than a
      // formality. Applied AFTER derivation so it adds keys without displacing
      // anything the signed grants established, cutover above all. See
      // applyFragmentKeys.
      const linkKeys = fragmentKeys?.get(b.coord);
      if (linkKeys) applyFragmentKeys(keyring, b.coord, linkKeys);
      // ready-daf: three-valued, and computed from the board's OWN snapshot
      // plus the link — not from the presence of relay-supplied grants alone,
      // whose absence used to read as "public".
      const { state, why } = confidentialityOf(keyring, b.coord, events, linkKeys !== undefined);
      if (state !== "public") confidential = true;
      if (state === "unknown") unestablished.push({ name: b.title || b.boardD, why: why ?? "no-grant" });
      const encryptedBoards = encryptedBoardsOf(keyring, state);
      const src = foldItemSource(
        {
          trusted: null,
          maintainers: null,
          pinnedBoard: b.coord,
          decryptor: keyring,
          encryptedBoards,
        },
        b.coord,
      );
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
        grantLevels: deriveLevels(verifiedEvents(authorityEvents), b.ownerPubkey, b.boardD).levels,
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
      live.push({
        coord: b.coord,
        events: [...events],
        seen: new Set(events.map(eventIdentity)),
        newest: events.reduce((max, e) => (typeof e.created_at === "number" && e.created_at > max ? e.created_at : max), 0),
        items: boardItems,
        src,
        writer,
      });
    } catch {
      // Skip this board; the others still render.
    }
  }
  return { items: out, confidential, unestablished, writers, live };
}

/**
 * LiveBoard is one board's re-foldable state: the events already folded, the
 * fold that produced the current items, and the writer built from the same
 * snapshot.
 *
 * `src` is CARRIED, not rebuilt. The projection depends on the keyring, the
 * confidentiality gate and the pinned coordinate that were derived from the
 * owner-signed authority snapshot at load; re-deriving any of that from the live
 * stream would let a pushed event change what this session can read or is
 * willing to write. Live events are ITEM state. Authority is not live.
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

  const refold = (): void => {
    pending = undefined;
    if (closed) return;
    const items: Item[] = [];
    for (const b of boards) {
      if (dirty.has(b.coord)) b.items = b.src.loadItems(b.events);
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
            if (pending === undefined) pending = setTimeout(refold, coalesceMs);
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

  if (fragment.kind === "claim") {
    renderAwaitingAuthorization(root, identity, fragment.payload.board, safeEncodeNpub(identity.pubkey));
    return;
  }

  const connecting = renderConnecting(root);
  const onStatus = (e: RelayStatusEvent) => {
    connecting.textContent = `Connecting to ${e.relay}… (${e.status}${e.attempt > 0 ? `, attempt ${e.attempt + 1}` : ""})`;
  };

  try {
    let relays: string[];
    let boards: DiscoveredBoard[];
    let authorityEvents: NostrEvent[];

    if (fragment.kind === "board") {
      const parsedCoord = parseBoardCoord(fragment.board);
      if (!parsedCoord) throw new Error(`main: malformed board coordinate ${JSON.stringify(fragment.board)}`);
      relays = fragment.relays.length > 0 ? fragment.relays : await deps.loadRelays();
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
        deps.fetchEvents(relays, { kinds: [KIND_ROLE_GRANT], ["#a"]: [fragment.board] }, { onStatus }),
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

    const linkKeys = fragmentKeyMap(fragment);
    const { items, confidential, unestablished, writers, live } = await loadBoardItems(
      boards,
      relays,
      authorityEvents,
      identity,
      deps,
      onStatus,
      linkKeys,
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

    // ready-4359: MUTATED, not rebuilt, when a live update arrives. It answers
    // "which board does this item live on" for every write, so an item the rd
    // CLI creates after this page loaded would otherwise render on the board and
    // be unwritable ("no writable board is loaded for item …") — the page
    // showing work it silently cannot act on.
    const itemBoard = new Map(items.filter((i) => i.boardCoord).map((i) => [i.id, i.boardCoord!]));

    const workspace = mountBoardWorkspace(root, items, {
      writer: boardScopedWriter(writers, itemBoard),
      viewerId: identity.pubkey,
      boards: boards.map((b) => ({ coord: b.coord, title: b.title || "(confidential board)" })),
      identityLine: `Logged in as ${safeEncodeNpub(identity.pubkey)}${canSign(identity.auth) ? "" : " (read-only)"}`,
      emptyBoardsNote: "No boards found.",
      notice: notice !== "" ? notice : undefined,
    });

    // ready-4359 done condition 4. Without this the page folds ONCE and a change
    // made anywhere else is invisible until the human reloads. Any previous
    // subscription was already closed at the top of this function.
    activeLive = deps.subscribeEvents
      ? startLiveUpdates({
          boards: live,
          relays,
          subscribe: deps.subscribeEvents,
          onItems: (next) => {
            for (const i of next) if (i.boardCoord) itemBoard.set(i.id, i.boardCoord);
            workspace.setItems(next);
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
