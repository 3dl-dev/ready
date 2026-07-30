// The CONFIDENTIALITY DECISION LAYER: what this reader can honestly say about
// whether ONE board is confidential, and what the fold's quarantine gate is
// therefore told (spec §11.13, §11.13a).
//
// WHY IT IS A MODULE AND NOT PART OF main.ts (ready-882). main.ts is the
// composition root and nothing else — "every step below lives in a module with
// its own suite", per its own header. This layer arrived inside main.ts with
// ready-daf and grew two witnesses there, which left it reachable only by driving
// the whole DOM app. That mattered the moment the epoch-model conformance vectors
// needed the REAL adapter: fold.vectors.test.ts had substituted a local
// §11.13-only EncryptedBoardSet on a claimed-equivalent rationale, and the claim
// was false — confidentialityOf has a THIRD path into "unknown" (no derived
// cutover, but a verified sealed card present) that the substitute did not, so a
// vector shipped that the deployed browser could not satisfy. Moving the layer
// here is a pure move: same functions, same behaviour, now importable by the
// conformance runner without booting the app.
//
// §11.13a IS A CLIENT-SIDE HARDENING THE GO FOLD DOES NOT IMPLEMENT
// (pkg/sync/keydist.go derives the cutover per §11.13 and trusts it; tracked as
// ready-9a6). A conformance vector is the SHARED contract, so no vector may carry
// an expectation that depends on which of the two layers a reader runs. That is
// not a claim that this file agrees with plain §11.13 — it is a constraint on the
// VECTORS, enforced by fold.vectors.test.ts's divergence-zone test and by the Go
// suite folding the same committed file with the plain §11.13 derivation.

import type { BoardKeyring } from "./keyring";
import type { EncryptedBoardSet } from "./envelope";
import { boardCoordOf, cekEpochOf, isConfidential } from "./envelope";
import { verifyEvent, type NostrEvent } from "./nostrevent";


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
export type WhyUnestablished = "no-grant" | "grants-withheld";

/** BoardConfidentiality is confidentialityOf's answer: the state, plus — only on
 * "unknown" — which of the two reasons applies. */
export interface BoardConfidentiality {
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
export interface SealedEvidence {
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
export function sealedEvidenceOf(events: NostrEvent[], coord: string): SealedEvidence {
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
export function grantsWithheld(cutover: number, keyring: BoardKeyring, coord: string, ev: SealedEvidence): boolean {
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
export function confidentialityOf(
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
export function encryptedBoardsOf(keyring: BoardKeyring, state: Confidentiality): EncryptedBoardSet {
  return {
    cutover(coord: string): { cutover: number; ok: boolean } {
      if (state === "unknown") return { cutover: 0, ok: true };
      const at = keyring.cutover(coord);
      if (at !== null) return { cutover: at, ok: true };
      return { cutover: 0, ok: false };
    },
  };
}
