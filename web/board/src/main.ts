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
import type { PortfolioKeys } from "./lib/portfoliokeys";
import { loadOwnBoardsRelays } from "./lib/relayconfig";
import {
  fetchEventsFromRelays,
  type FetchEventsOptions,
  type NostrFilter,
  type RelayStatusEvent,
} from "./lib/relay";
import type { NostrEvent } from "./lib/nostrevent";
import { discoverOwnerBoards, parseBoardCoord, type DiscoveredBoard } from "./lib/boarddiscovery";
import { applyFragmentKeys, deriveBoardKeyring, KIND_ROLE_GRANT, type BoardKeyring } from "./lib/keyring";
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
      // ready-5c5: query by the board's own "a" coordinate, NOT authors. A
      // nostr relay's `authors` index is allowed to under-return (measured on
      // wss://relay.3dl.network: a paged kind+authors REQ served 42 of an
      // owner's 56 boards while a paged kind-only REQ, same relay, same run,
      // served all 56). The coordinate is a tag on the event itself, not a
      // secondary index a relay can maintain out of step with the event
      // store, so this cannot silently drop a board the author index does.
      // discoverOwnerBoards still schnorr-verifies every event and re-checks
      // the owner client-side, so no security property is lost by dropping
      // `authors` from the wire filter.
      authorityEvents = await deps.fetchEvents(
        relays,
        { kinds: AUTHORITY_KINDS, ["#a"]: [fragment.board] },
        { onStatus },
      );
      boards = discoverOwnerBoards(authorityEvents, [parsedCoord.owner], parsedCoord.boardD);
    } else if (fragment.kind === "portfolio") {
      // ready-4d9. The WHOLE portfolio: no boardD filter, so discovery returns
      // every board these owners published. The owner set is the viewer plus
      // every owner named in the link's own key material — the viewer alone
      // would miss a confidential board owned by someone else that this key was
      // granted read access to, and the link is carrying that board's key.
      //
      // ready-5c5: no boardD is known ahead of time here, so an "#a" filter
      // isn't available (unlike the single-board case above) — the query is
      // kind-scoped only, NOT authors-scoped, because `authors` is exactly
      // the filter measured to under-return on wss://relay.3dl.network.
      // Owners are still enforced, just client-side: discoverOwnerBoards
      // schnorr-verifies every kind-30301 and drops any whose author is not
      // in this set, so a hostile or merely noisy relay cannot add a board
      // here, and the keyring is still derived per board from owner-signed
      // grants below.
      relays = fragment.relays.length > 0 ? fragment.relays : await deps.loadRelays();
      const owners = [...new Set([identity.pubkey, ...keyOwners(fragment.keys)])];
      authorityEvents = await deps.fetchEvents(relays, { kinds: AUTHORITY_KINDS }, { onStatus });
      boards = discoverOwnerBoards(authorityEvents, owners);
    } else {
      // fragment.kind === "none": own-boards discovery (ready-dbf done condition 3).
      // ready-5c5: kind-scoped only, see the portfolio branch's comment above —
      // this is the exact query the veracity finding measured under-returning
      // 14 of 56 boards via `authors`.
      relays = await deps.loadRelays();
      authorityEvents = await deps.fetchEvents(relays, { kinds: AUTHORITY_KINDS }, { onStatus });
      boards = discoverOwnerBoards(authorityEvents, [identity.pubkey]);
    }

    const linkKeys = fragmentKeyMap(fragment);
    const { items, confidential } = await loadBoardItems(
      boards,
      relays,
      authorityEvents,
      identity,
      deps,
      onStatus,
      linkKeys,
    );
    connecting.remove();

    const notice = [confidential ? confidentialNotice(items, boards.length) : "", unservedBoardsNotice(linkKeys, boards)]
      .filter((s) => s !== "")
      .join(" ");

    mountBoardWorkspace(root, items, {
      viewerId: identity.pubkey,
      boards: boards.map((b) => ({ coord: b.coord, title: b.title || "(confidential board)" })),
      identityLine: `Logged in as ${safeEncodeNpub(identity.pubkey)}${canSign(identity.auth) ? "" : " (read-only)"}`,
      emptyBoardsNote: "No boards found.",
      notice: notice !== "" ? notice : undefined,
    });
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
  // "(read-only)" marker on the identity line and what every write control (the
  // scaffolded board/write.ts drop path, and whatever lands on it) gates on.
  // Flipping it to "extension" is a one-word edit that silently converts a
  // bearer READ link into a session the page treats as signing-capable, so it is
  // witnessed directly: main.fragmentkey.test.ts, "the pk= identity CANNOT SIGN".
  //
  // Decryption comes from the fragment's own keys, threaded through afterLogin.
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
