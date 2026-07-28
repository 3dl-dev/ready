// ready.3dl.dev/board (ready-dbf): log in with your own key and see all your
// boards enumerated.
//
// SCOPE (see the item for the full text): read-only. No signing UI, no
// publish path, no decryption. A confidential board (no "title" tag) renders
// a placeholder title. The board UI itself (columns/swimlanes/filters) is
// ready-445, not this file — this page only gets you from "nothing" to "a
// list of your boards, or an awaiting-authorization notice."
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
import { deriveBoardKeyring, KIND_ROLE_GRANT } from "./lib/keyring";
import { nip07KeyUnwrapper, neverUnwraps, type KeyUnwrapper } from "./lib/keyunwrap";
import { projectCards, KIND_CARD, type BoardItem } from "./lib/carditems";
import { PLACEHOLDER } from "./lib/envelope";

export interface Identity {
  pubkey: string;
  auth: AuthTransition;
}

/**
 * BoardDeps is the injection seam for everything afterLogin does that reaches
 * outside the page: resolving the relay set and querying relays. It exists so
 * a test can serve a scripted relay snapshot; it deliberately does NOT
 * abstract over discoverOwnerBoards, because the signature verification that
 * happens inside discoverOwnerBoards is the property under test and must stay
 * un-stubbable from here.
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

function renderBoards(root: HTMLElement, boards: DiscoveredBoard[]): void {
  if (boards.length === 0) {
    root.append(el("p", { textContent: "No boards found." }));
    return;
  }
  const list = el(
    "ul",
    { className: "board-list" },
    boards.map((b) =>
      el("li", {}, [
        el("span", { className: "board-title", textContent: b.title || "(confidential board)" }),
        el("code", { className: "board-coord", textContent: b.coord }),
      ]),
    ),
  );
  root.append(list);
}

/**
 * renderItems draws one row per item. `title` is set through textContent, never
 * innerHTML, so a decrypted title is text and cannot become markup.
 *
 * An item whose free text could not be opened is rendered with the class
 * "encrypted" and the PLACEHOLDER string. That combination is deliberate and is
 * the UI half of failing closed: the row still exists (its status, priority and
 * type are clear tags and are perfectly readable), but the title says
 * "[encrypted]" rather than going blank. A blank cell would read as "this item
 * has no title", which is false; the placeholder reads as "you are not holding
 * the key for this one", which is true.
 */
function renderItems(root: HTMLElement, items: BoardItem[]): void {
  if (items.length === 0) {
    root.append(el("p", { className: "no-items", textContent: "No items on this board." }));
    return;
  }
  const list = el(
    "ul",
    { className: "item-list" },
    items.map((it) => {
      const title = el("span", {
        className: it.decrypted ? "item-title" : "item-title encrypted",
        textContent: it.title,
      });
      if (!it.decrypted) {
        title.title = `Sealed under CEK epoch ${it.epoch ?? "?"} — this key holds no grant for it.`;
      }
      const meta = el("span", {
        className: "item-meta",
        textContent: [it.status, it.priority, it.type].filter((s) => s !== "").join(" · "),
      });
      return el("li", { className: it.decrypted ? "item" : "item sealed" }, [
        el("code", { className: "item-id", textContent: it.id }),
        title,
        meta,
      ]);
    }),
  );
  root.append(list);
}

/**
 * renderBoardItems is the confidential read path end to end (ready-c4b): fetch
 * the board's cards, derive this reader's key material from the owner-signed
 * grants already in hand, project, render.
 *
 * The keyring is derived from `authorityEvents` — the same snapshot the board
 * itself came from — so a card can never introduce its own key. Deriving it
 * BEFORE projecting also means the fold gate knows the board is confidential
 * even for a reader who holds nothing, which is what keeps post-cutover
 * cleartext quarantined for a stranger instead of rendered.
 *
 * A failure anywhere here degrades to a visible message; it never leaves the
 * page half-rendered and never falls back to showing clear tags in place of
 * sealed text.
 */
async function renderBoardItems(
  root: HTMLElement,
  identity: Identity,
  parsed: { owner: string; boardD: string },
  coord: string,
  relays: string[],
  authorityEvents: NostrEvent[],
  deps: BoardDeps,
  onStatus: (e: RelayStatusEvent) => void,
): Promise<void> {
  const loading = el("p", { className: "loading-items", textContent: "Loading items…" });
  root.append(loading);
  try {
    const cardEvents = await deps.fetchEvents(relays, { kinds: [KIND_CARD], ["#a"]: [coord] }, { onStatus });
    const keyring = await deriveBoardKeyring(
      authorityEvents,
      identity.pubkey,
      parsed.owner,
      parsed.boardD,
      deps.keyUnwrapper(identity),
    );
    const items = projectCards(cardEvents, { coord, keyring });
    loading.remove();

    // The notice states BOTH halves — what was read and what was not — because
    // either half alone misleads. "N decrypted" with no mention of the rest
    // hides that the board is partly invisible; "N sealed" with no mention of
    // the rest reads like a failure even when most of the board rendered.
    if (keyring.cutover(coord) !== null) {
      const sealed = items.filter((i) => !i.decrypted);
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
      root.append(el("p", { className: "confidential-notice", textContent: parts.join(" ") }));
    }
    renderItems(root, items);
  } catch (err) {
    loading.textContent = err instanceof Error ? err.message : String(err);
  }
}

function renderIdentityBar(root: HTMLElement, identity: Identity): void {
  const npub = safeEncodeNpub(identity.pubkey);
  root.append(
    el("p", {
      className: "identity",
      textContent: `Logged in as ${npub}${canSign(identity.auth) ? "" : " (read-only)"}`,
    }),
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
  root.append(el("h1", { textContent: "ready — board" }));
  renderIdentityBar(root, identity);

  if (fragment.kind === "claim") {
    renderAwaitingAuthorization(root, identity, fragment.payload.board, safeEncodeNpub(identity.pubkey));
    return;
  }

  const connecting = renderConnecting(root);
  const onStatus = (e: RelayStatusEvent) => {
    connecting.textContent = `Connecting to ${e.relay}… (${e.status}${e.attempt > 0 ? `, attempt ${e.attempt + 1}` : ""})`;
  };

  try {
    if (fragment.kind === "board") {
      const parsedCoord = parseBoardCoord(fragment.board);
      if (!parsedCoord) throw new Error(`main: malformed board coordinate ${JSON.stringify(fragment.board)}`);
      const relays = fragment.relays.length > 0 ? fragment.relays : await deps.loadRelays();
      // One REQ for the owner's authority events: the 30301 board itself and
      // every 39301 role grant. The grants are where a confidential board's read
      // key rides, so this single query carries both "which board" and "can I
      // read it".
      const events = await deps.fetchEvents(
        relays,
        { kinds: [30301, KIND_ROLE_GRANT], authors: [parsedCoord.owner] },
        { onStatus },
      );
      connecting.remove();
      renderBoards(root, discoverOwnerBoards(events, [parsedCoord.owner], parsedCoord.boardD));
      await renderBoardItems(root, identity, parsedCoord, fragment.board, relays, events, deps, onStatus);
      return;
    }

    // fragment.kind === "none": own-boards discovery (done condition 3).
    const relays = await deps.loadRelays();
    const events = await deps.fetchEvents(relays, { kinds: [30301], authors: [identity.pubkey] }, { onStatus });
    connecting.remove();
    renderBoards(root, discoverOwnerBoards(events, [identity.pubkey]));
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
