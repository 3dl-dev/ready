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
import { decodeNpub, encodeNpub } from "./lib/npub";
import { parseAndStripFragment, type ParsedFragment } from "./lib/fragment";
import { loadOwnBoardsRelays } from "./lib/relayconfig";
import {
  fetchEventsFromRelays,
  type FetchEventsOptions,
  type NostrFilter,
  type RelayStatusEvent,
} from "./lib/relay";
import type { NostrEvent } from "./lib/nostrevent";
import { discoverOwnerBoards, parseBoardCoord, type DiscoveredBoard } from "./lib/boarddiscovery";
import { deriveBoardKeyring, KIND_ROLE_GRANT, type BoardKeyring } from "./lib/keyring";
import { nip07KeyUnwrapper, neverUnwraps, type KeyUnwrapper } from "./lib/keyunwrap";
import type { EncryptedBoardSet } from "./lib/envelope";
import { PLACEHOLDER } from "./lib/envelope";
import { foldItemSource } from "./lib/itemsource";
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
}

/** Production wiring: same-origin relays.json + the real WebSocket client +
 * the browser extension's NIP-44 namespace. */
export const defaultDeps: BoardDeps = {
  loadRelays: () => loadOwnBoardsRelays(),
  fetchEvents: (relays, filter, opts) => fetchEventsFromRelays(relays, filter, opts),
  keyUnwrapper: (identity) => (canSign(identity.auth) ? nip07KeyUnwrapper(nip44Provider()) : neverUnwraps),
};

/**
 * AUTHORITY_KINDS is the one REQ that decides what this viewer may see: the
 * kind-30301 board definitions AND the kind-39301 role grants, from the owner.
 * They travel together because a confidential board's read key rides INSIDE an
 * owner-signed grant — one query carries both "which boards" and "can I read
 * them". Splitting it would mean deriving a keyring from a second, later
 * snapshot that need not agree with the first.
 */
const AUTHORITY_KINDS = [30301, KIND_ROLE_GRANT];

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
 * encryptedBoardsOf adapts a BoardKeyring to the fold's EncryptedBoardSet.
 *
 * This is the gate that makes a confidential board fail closed for a reader who
 * holds nothing: BoardKeyring.cutover() is populated from EVERY owner CEK grant,
 * not only the ones addressed to this reader, so a stranger still learns the
 * board went confidential and post-cutover cleartext is quarantined instead of
 * rendered. Returning `ok:false` here — the shape a naive "I have no keys, so
 * nothing is encrypted" adapter would produce — would render exactly the
 * smuggled-cleartext card the fold gate exists to drop.
 */
function encryptedBoardsOf(keyring: BoardKeyring): EncryptedBoardSet {
  return {
    cutover(coord: string): { cutover: number; ok: boolean } {
      const at = keyring.cutover(coord);
      return at === null ? { cutover: 0, ok: false } : { cutover: at, ok: true };
    },
  };
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
 */
async function loadBoardItems(
  boards: DiscoveredBoard[],
  relays: string[],
  authorityEvents: NostrEvent[],
  identity: Identity,
  deps: BoardDeps,
  onStatus: (e: RelayStatusEvent) => void,
): Promise<{ items: Item[]; confidential: boolean }> {
  const out: Item[] = [];
  let confidential = false;
  const unwrap = deps.keyUnwrapper(identity);

  for (const b of boards) {
    try {
      const events = await deps.fetchEvents(
        relays,
        // Kinds mirror pkg/sync/nostrinbound.go's BoardSyncFilter exactly.
        { kinds: [30302, 1630, 1631, 1632, 1633, 39301], "#a": [b.coord] },
        { onStatus },
      );
      const keyring = await deriveBoardKeyring(
        authorityEvents,
        identity.pubkey,
        b.ownerPubkey,
        b.boardD,
        unwrap,
      );
      if (keyring.cutover(b.coord) !== null) confidential = true;
      const src = foldItemSource(
        {
          trusted: null,
          maintainers: null,
          pinnedBoard: b.coord,
          decryptor: keyring,
          encryptedBoards: encryptedBoardsOf(keyring),
        },
        b.coord,
      );
      out.push(...src.loadItems(events));
    } catch {
      // Skip this board; the others still render.
    }
  }
  return { items: out, confidential };
}

/**
 * confidentialNotice states BOTH halves — what was read and what was not —
 * because either half alone misleads. "N decrypted" with no mention of the rest
 * hides that the board is partly invisible; "N sealed" with no mention of the
 * rest reads like a failure even when most of the board rendered.
 */
function confidentialNotice(items: Item[]): string {
  const sealed = items.filter((i) => i.title === PLACEHOLDER);
  const opened = items.length - sealed.length;
  const parts = [
    opened > 0
      ? `This board is confidential; ${opened} of ${items.length} titles were decrypted in your browser.`
      : "This board is confidential.",
  ];
  if (sealed.length > 0) {
    parts.push(
      `${sealed.length} of ${items.length} items are sealed to a key you do not hold — they show ${PLACEHOLDER}. Ask the board owner to grant this key access.`,
    );
  }
  return parts.join(" ");
}

function safeEncodeNpub(pubkeyHex: string): string {
  try {
    return encodeNpub(pubkeyHex);
  } catch {
    return pubkeyHex;
  }
}

export async function afterLogin(
  root: HTMLElement,
  identity: Identity,
  fragment: ParsedFragment,
  deps: BoardDeps = defaultDeps,
): Promise<void> {
  root.replaceChildren();
  root.className = "board-page";

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
      authorityEvents = await deps.fetchEvents(
        relays,
        { kinds: AUTHORITY_KINDS, authors: [parsedCoord.owner] },
        { onStatus },
      );
      boards = discoverOwnerBoards(authorityEvents, [parsedCoord.owner], parsedCoord.boardD);
    } else {
      // fragment.kind === "none": own-boards discovery (ready-dbf done condition 3).
      relays = await deps.loadRelays();
      authorityEvents = await deps.fetchEvents(
        relays,
        { kinds: AUTHORITY_KINDS, authors: [identity.pubkey] },
        { onStatus },
      );
      boards = discoverOwnerBoards(authorityEvents, [identity.pubkey]);
    }

    const { items, confidential } = await loadBoardItems(
      boards,
      relays,
      authorityEvents,
      identity,
      deps,
      onStatus,
    );
    connecting.remove();

    mountBoardWorkspace(root, items, {
      viewerId: identity.pubkey,
      boards: boards.map((b) => ({ coord: b.coord, title: b.title || "(confidential board)" })),
      identityLine: `Logged in as ${safeEncodeNpub(identity.pubkey)}${canSign(identity.auth) ? "" : " (read-only)"}`,
      emptyBoardsNote: "No boards found.",
      notice: confidential ? confidentialNotice(items) : undefined,
    });
  } catch (err) {
    connecting.textContent = err instanceof Error ? err.message : String(err);
  }
}

export function main(): void {
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

  renderLogin(root, fragment, (identity) => {
    void afterLogin(root, identity, fragment);
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
