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
import { hasNip07Extension, loginWithExtension } from "./lib/nip07";
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
}

/** Production wiring: same-origin relays.json + the real WebSocket client. */
export const defaultDeps: BoardDeps = {
  loadRelays: () => loadOwnBoardsRelays(),
  fetchEvents: (relays, filter, opts) => fetchEventsFromRelays(relays, filter, opts),
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
      const events = await deps.fetchEvents(
        relays,
        { kinds: [30301], authors: [parsedCoord.owner] },
        { onStatus },
      );
      connecting.remove();
      renderBoards(root, discoverOwnerBoards(events, [parsedCoord.owner], parsedCoord.boardD));
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

function main(): void {
  const root = document.getElementById("app");
  if (!root) return;
  const fragment = parseAndStripFragment();
  renderLogin(root, fragment, (identity) => {
    void afterLogin(root, identity, fragment);
  });
}

main();
