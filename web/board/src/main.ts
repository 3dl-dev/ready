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
import { dedupeExact } from "./lib/nostrevent";
import { discoverOwnerBoards, parseBoardCoord, KIND_BOARD, type DiscoveredBoard } from "./lib/boarddiscovery";
import { applyFragmentKeys, deriveBoardKeyring, KIND_ROLE_GRANT, type BoardKeyring } from "./lib/keyring";
import { nip07KeyUnwrapper, neverUnwraps, type KeyUnwrapper } from "./lib/keyunwrap";
import type { EncryptedBoardSet } from "./lib/envelope";
import { PLACEHOLDER, boardCoordOf, cekEpochOf, isConfidential } from "./lib/envelope";
import { verifyEvent } from "./lib/nostrevent";
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
const AUTHORITY_KINDS = [KIND_BOARD, KIND_ROLE_GRANT];

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
 * Confidentiality is what this reader can HONESTLY say about one board, and it
 * is deliberately three-valued (ready-daf).
 *
 *  - "confidential": ESTABLISHED. An owner-signed CEK-bearing grant was served,
 *    so both the fact and the cutover instant are known.
 *  - "unknown": the board carries confidential content, but NO owner grant
 *    established WHEN it went confidential. The fact is known; the cutover is
 *    not.
 *  - "public": nothing this page saw says the board is confidential.
 *
 * WHY THE MIDDLE VALUE EXISTS. Before ready-daf there were only two states, and
 * they were derived from owner grants alone — so the ABSENCE of grants meant
 * "public". On this transport that inverts the whole posture: the relay set is
 * public and untrusted, reads are unrestricted BY DESIGN, and nothing obliges a
 * relay to serve every event it holds. A relay that simply OMITS the kind-39301
 * grants therefore downgraded a confidential board to apparently-public,
 * silently, in the reader's browser: the fold's quarantine gate went inert
 * (shouldQuarantine returns false for `ok:false`) and the page said nothing that
 * distinguished the board from one that was never confidential at all. That is
 * the same relay-omission-as-attack shape as ready-dd5, and fail-closed is the
 * stated posture everywhere else here.
 *
 * ROUND 2 — WHY A SERVED GRANT IS NOT ENOUGH. Round 1 read grant omission as
 * all-or-nothing and keyed "unknown" off `cutover() === null`. But the cutover is
 * a MINIMUM over the owner CEK grants that were SERVED (keyring.ts's noteCutover),
 * so omission does not have to be total to work: drop only the OLDEST grants and
 * the minimum moves LATER, cutover() returns a real instant, and this function
 * said "confidential — established" about a cutover that is simply wrong. Every
 * plaintext card authored between the true cutover and the manufactured one then
 * satisfies shouldQuarantine's grandfather clause and renders in clear. Proven,
 * not hypothetical: main.grantsomission.test.ts drives it with an owner-signed
 * plaintext card that the unfixed code puts in the DOM. So a non-null cutover is
 * now believed only when nothing on the board CONTRADICTS it.
 *
 * WHAT THIS CAN AND CANNOT ESTABLISH, stated because it is easy to over-trust.
 * A browser cannot verify that a relay answered COMPLETELY — there is no proof
 * of non-omission in NIP-01 — so "public" never means "proven public", it means
 * "no evidence of confidentiality reached this page". The residual after round 2
 * is narrower than round 1's, and precise: BOTH witnesses below are testimony
 * carried BY the sealed cards, so a relay defeats them only by withholding every
 * sealed card that carries either signal — every one older than the cutover it
 * wants to manufacture, AND every one naming an epoch below the grants it keeps —
 * on top of the grants themselves, and with no key-bearing link in the reader's
 * hands. That snapshot is a board with no old cards and no old epochs, i.e. one
 * whose visible history begins at the manufactured cutover. It cannot be detected
 * from inside a single relay answer, and saying so is the honest limit.
 */
export type Confidentiality = "public" | "confidential" | "unknown";

/**
 * WhyUnestablished distinguishes the two ways the cutover fails to be known,
 * because they are different facts about the relay answer and a reader can act on
 * them differently.
 *
 *  - "no-grant": no owner CEK grant reached this page at all. Consistent with a
 *    lossy relay, an indexing gap, or omission — this page cannot tell which, and
 *    must not claim to (the ready-5c5 wording rule).
 *  - "grants-withheld": a grant DID arrive, and a signature-verified sealed card
 *    on the same board contradicts it. Omission is then PROVEN, not merely
 *    possible: the answer is internally inconsistent.
 */
type WhyUnestablished = "no-grant" | "grants-withheld";

/** BoardConfidentiality is confidentialityOf's answer: the state, plus — only on
 * "unknown" — which of the two reasons applies. */
interface BoardConfidentiality {
  state: Confidentiality;
  why: WhyUnestablished | null;
}

/**
 * SealedEvidence is everything this board's OWN snapshot testifies about its
 * confidentiality, gathered in one pass over the verified sealed cards.
 *
 * Round 1 collected only `present` — the boolean enc marker — and threw the
 * cek_epoch away. That discarded the second witness (see confidentialityOf), so
 * the epoch is kept now.
 */
interface SealedEvidence {
  /** At least one verified enc-marked event for this board was served. */
  present: boolean;
  /** The oldest such event's created_at, or null when there are none. */
  earliestAt: number | null;
  /** The lowest parseable cek_epoch among them, or null when none carries one. */
  lowestEpoch: number | null;
}

/**
 * sealedEvidenceOf summarizes the SEALED events in this board's snapshot — the
 * owner-independent, in-band witness that the board is confidential.
 *
 * Sealed cards are the signal that survives grant omission: a sealed body can
 * only be produced by a holder of the board CEK and is signed by its author, so a
 * relay can remove one but can neither fabricate one nor alter what it says.
 * Sealed cards are also the reason a reader is looking at this board at all, so
 * withholding every one of them is a far more visible act than dropping a handful
 * of grants.
 *
 * The signature check runs LAST because it is the expensive one and the tag
 * checks eliminate every event on a plaintext board first.
 */
function sealedEvidenceOf(events: NostrEvent[], coord: string): SealedEvidence {
  const ev: SealedEvidence = { present: false, earliestAt: null, lowestEpoch: null };
  for (const e of events) {
    if (!e || !isConfidential(e)) continue;
    if (boardCoordOf(e) !== coord) continue;
    // A relay is untrusted: an unverifiable event witnesses nothing, in either
    // direction. This is the same rule deriveBoardKeyring's CHECK 1 applies.
    if (!verifyEvent(e)) continue;
    ev.present = true;
    if (ev.earliestAt === null || e.created_at < ev.earliestAt) ev.earliestAt = e.created_at;
    const epoch = cekEpochOf(e);
    if (epoch !== null && (ev.lowestEpoch === null || epoch < ev.lowestEpoch)) ev.lowestEpoch = epoch;
  }
  return ev;
}

/**
 * grantsWithheld decides whether a NON-NULL cutover is contradicted by the
 * board's own sealed cards — i.e. whether grants older than the ones served are
 * provably missing (ready-daf round 2).
 *
 * Omission can only ever REMOVE grants, and the cutover is a minimum, so the
 * derived instant is always >= the truth. The fail-open case is exactly "strictly
 * greater", and each witness below is a signature-verified reason to conclude it.
 *
 * WITNESS A — TIME. A verified sealed card older than the derived cutover proves
 * the board was already confidential before that instant: something sealed it, so
 * a CEK already existed, so an owner CEK grant older than every one served must
 * exist. No assumption about epoch numbering is needed.
 *
 * WITNESS B — EPOCH. A verified sealed card naming a cek_epoch below the lowest
 * epoch any served owner grant covers proves the grant that minted that epoch was
 * not served — a card cannot seal under an epoch whose CEK does not exist. Epochs
 * increase by one per rotation (keydist.go, and the fixture's epoch-1-then-2
 * rotation), so a LOWER epoch is an OLDER grant, and an older grant moves the
 * minimum earlier. This is the witness that catches the case witness A cannot: a
 * stale writer's card sealed under the old epoch but published AFTER the
 * manufactured cutover, which is newer than it and so raises no alarm by time.
 *
 * WHY B IS DELIBERATELY NARROWER THAN "ANY UNCOVERED EPOCH". A sealed card at an
 * epoch ABOVE everything the served grants cover also proves a grant is missing —
 * but a missing LATER grant cannot move a minimum, so the cutover is still right
 * and quarantining the board would cost visibility for no security gain. The
 * fixture's conf-004 (epoch 9, no epoch-9 grant anywhere) is exactly that shape
 * and is pinned as an anti-tautology case.
 *
 * NEITHER WITNESS CAN BE FORGED, and that is what makes them usable without an
 * authorship check on the card. A relay cannot mint a sealed body (no CEK) or
 * sign one (no author key), and cannot rewrite created_at or cek_epoch — both are
 * inside the signed id. It can only suppress the card, and suppressing the cards
 * is the residual stated in the Confidentiality doc above. Being wrong in the
 * other direction costs visibility only: the board goes to "unknown", which
 * withholds MORE and says so out loud.
 */
function grantsWithheld(cutover: number, keyring: BoardKeyring, coord: string, ev: SealedEvidence): boolean {
  if (ev.earliestAt !== null && ev.earliestAt < cutover) return true; // WITNESS A
  const floor = keyring.grantEpochFloor(coord);
  if (floor !== null && ev.lowestEpoch !== null && ev.lowestEpoch < floor) return true; // WITNESS B
  return false;
}

/**
 * confidentialityOf decides the three-valued state for ONE board, and why.
 *
 * `hasLinkKeys` — this link carries a CEK filed under this coordinate — counts
 * as evidence, and is INDEPENDENT of the relay: it came out of the reader's own
 * `rd`, which had already run the four grant checks against its local signed log
 * before printing the key (see keyring.ts's applyFragmentKeys). A link that
 * carries a board's read key is a statement that the board HAS a read key.
 *
 * Evidence only ever moves a board TOWARDS "unknown" — a strictly TIGHTENING
 * direction, since "unknown" quarantines a superset of what "confidential" does
 * and grandfathers nothing (see encryptedBoardsOf). So a hostile relay, or a
 * crafted link, can use this to HIDE a public board's cards behind a notice that
 * says so out loud; it can never use it to reveal something. That asymmetry is the
 * reason evidence needs no authorship check: being wrong here costs visibility,
 * not confidentiality, and the page says plainly that it could not establish the
 * state.
 */
function confidentialityOf(
  keyring: BoardKeyring,
  coord: string,
  events: NostrEvent[],
  hasLinkKeys: boolean,
): BoardConfidentiality {
  const ev = sealedEvidenceOf(events, coord);
  const cutover = keyring.cutover(coord);
  if (cutover !== null) {
    return grantsWithheld(cutover, keyring, coord, ev)
      ? { state: "unknown", why: "grants-withheld" }
      : { state: "confidential", why: null };
  }
  if (hasLinkKeys || ev.present) return { state: "unknown", why: "no-grant" };
  return { state: "public", why: null };
}

/**
 * encryptedBoardsOf adapts a BoardKeyring to the fold's EncryptedBoardSet, for
 * the ONE board being loaded and in the light of its Confidentiality.
 *
 * This is the gate that makes a confidential board fail closed for a reader who
 * holds nothing: BoardKeyring.cutover() is populated from EVERY owner CEK grant,
 * not only the ones addressed to this reader, so a stranger still learns the
 * board went confidential and post-cutover cleartext is quarantined instead of
 * rendered. Returning `ok:false` for such a board — the shape a naive "I have no
 * keys, so nothing is encrypted" adapter would produce — would render exactly
 * the smuggled-cleartext card the fold gate exists to drop.
 *
 * ON "unknown" IT REPORTS cutover 0, ok TRUE (ready-daf). `ok:true` keeps the
 * gate ON; cutover 0 is the fail-closed reading of an unestablished cutover —
 * "as far as this reader can prove, this board has been confidential for all of
 * time" — so shouldQuarantine grandfathers NOTHING and every card that is not a
 * well-formed sealed envelope is withheld. The alternatives are both wrong:
 * `ok:false` is the bug this item fixes, and a LATE cutover (e.g.
 * MAX_SAFE_INTEGER) would grandfather every plaintext card on the board, which
 * is the same fail-open wearing a different number.
 *
 * ROUND 2 — THE STATE, NOT THE KEYRING, DECIDES. This used to consult
 * keyring.cutover() FIRST and fall back to the state, which meant a cutover that
 * confidentialityOf had already judged untrustworthy was handed to the fold
 * anyway: state "unknown" was unreachable whenever a grant had been served, so
 * partial omission drove the gate with the manufactured instant. The order is now
 * inverted — "unknown" wins over any derived cutover, because "unknown" is
 * precisely the verdict that the derived cutover must not be used.
 */
function encryptedBoardsOf(keyring: BoardKeyring, state: Confidentiality): EncryptedBoardSet {
  return {
    cutover(coord: string): { cutover: number; ok: boolean } {
      if (state === "unknown") return { cutover: 0, ok: true };
      const at = keyring.cutover(coord);
      if (at !== null) return { cutover: at, ok: true };
      return { cutover: 0, ok: false };
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
): Promise<{ items: Item[]; confidential: boolean; unestablished: UnestablishedBoard[] }> {
  const out: Item[] = [];
  let confidential = false;
  const unestablished: UnestablishedBoard[] = [];
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
      // ready-daf: three-valued, and computed from the board's OWN snapshot
      // plus the link — not from the presence of relay-supplied grants alone,
      // whose absence used to read as "public".
      const { state, why } = confidentialityOf(keyring, b.coord, events, linkKeys !== undefined);
      if (state !== "public") confidential = true;
      if (state === "unknown") unestablished.push({ name: b.title || b.boardD, why: why ?? "no-grant" });
      const src = foldItemSource(
        {
          trusted: null,
          maintainers: null,
          pinnedBoard: b.coord,
          decryptor: keyring,
          encryptedBoards: encryptedBoardsOf(keyring, state),
        },
        b.coord,
      );
      out.push(...src.loadItems(events));
    } catch {
      // Skip this board; the others still render.
    }
  }
  return { items: out, confidential, unestablished };
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
    const { items, confidential, unestablished } = await loadBoardItems(
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
