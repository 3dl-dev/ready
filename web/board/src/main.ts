// ready.3dl.dev/board (ready-dbf): log in with your own key and see all your
// boards enumerated.
//
// SCOPE (see the item for the full text): read-only. No signing UI, no
// publish path, no decryption. A confidential board (no "title" tag) renders
// a placeholder title. The board UI itself (columns/swimlanes/filters) is
// ready-445, not this file — this page only gets you from "nothing" to "a
// list of your boards, or an awaiting-authorization notice."
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
import { fetchEventsFromRelays, type RelayStatusEvent } from "./lib/relay";
import { discoverOwnerBoards, parseBoardCoord, type DiscoveredBoard } from "./lib/boarddiscovery";

interface Identity {
  pubkey: string;
  auth: AuthTransition;
}

const root = document.getElementById("app");

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

function renderLogin(fragment: ParsedFragment, onIdentity: (id: Identity) => void): void {
  if (!root) return;
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

function renderAwaitingAuthorization(identity: Identity, board: string, npub: string): void {
  if (!root) return;
  const panel = el("section", { className: "awaiting-authorization" }, [
    el("h2", { textContent: "Awaiting authorization" }),
    el("p", {
      textContent: `You are logged in as ${npub}${identity.auth.readOnly ? " (read-only)" : ""}. Ask the owner of board ${board} to grant this key access.`,
    }),
  ]);
  root.append(panel);
}

function renderConnecting(): HTMLElement {
  const status = el("p", { className: "connecting", textContent: "Connecting to relays…" });
  root?.append(status);
  return status;
}

function renderBoards(boards: DiscoveredBoard[]): void {
  if (!root) return;
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

function renderIdentityBar(identity: Identity): void {
  if (!root) return;
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

async function afterLogin(identity: Identity, fragment: ParsedFragment): Promise<void> {
  if (!root) return;
  root.replaceChildren();
  root.append(el("h1", { textContent: "ready — board" }));
  renderIdentityBar(identity);

  if (fragment.kind === "claim") {
    renderAwaitingAuthorization(identity, fragment.payload.board, safeEncodeNpub(identity.pubkey));
    return;
  }

  const connecting = renderConnecting();
  const onStatus = (e: RelayStatusEvent) => {
    connecting.textContent = `Connecting to ${e.relay}… (${e.status}${e.attempt > 0 ? `, attempt ${e.attempt + 1}` : ""})`;
  };

  try {
    if (fragment.kind === "board") {
      const parsedCoord = parseBoardCoord(fragment.board);
      if (!parsedCoord) throw new Error(`main: malformed board coordinate ${JSON.stringify(fragment.board)}`);
      const relays = fragment.relays.length > 0 ? fragment.relays : await loadOwnBoardsRelays();
      const events = await fetchEventsFromRelays(
        relays,
        { kinds: [30301], authors: [parsedCoord.owner] },
        { onStatus },
      );
      connecting.remove();
      renderBoards(discoverOwnerBoards(events, [parsedCoord.owner], parsedCoord.boardD));
      return;
    }

    // fragment.kind === "none": own-boards discovery (done condition 3).
    const relays = await loadOwnBoardsRelays();
    const events = await fetchEventsFromRelays(relays, { kinds: [30301], authors: [identity.pubkey] }, { onStatus });
    connecting.remove();
    renderBoards(discoverOwnerBoards(events, [identity.pubkey]));
  } catch (err) {
    connecting.textContent = err instanceof Error ? err.message : String(err);
  }
}

function main(): void {
  if (!root) return;
  const fragment = parseAndStripFragment();
  renderLogin(fragment, (identity) => {
    void afterLogin(identity, fragment);
  });
}

main();
