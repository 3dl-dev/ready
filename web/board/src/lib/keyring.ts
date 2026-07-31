// Reader-side confidential-board key material (ready-c4b), the browser port of
// pkg/sync/keydist.go's DeriveBoardKeyring + pkg/sync/rolegrant.go's
// parseRoleGrant.
//
// A board's read key rides INSIDE the owner-signed kind-39301 role grant: one
// signed action confers write authority AND the read key. So deriving the
// keyring is an AUTHORIZATION computation, not a lookup, and it runs entirely on
// signed events fetched from an untrusted relay. Four independent checks decide
// whether a wrapped key becomes a usable CEK, and all four are load-bearing:
//
//  1. The grant's Schnorr signature verifies (a relay may serve anything).
//  2. The grant is SIGNED BY THE BOARD OWNER. Only the authz root mints board
//     keys; a self-signed grant carrying a key someone else minted is ignored,
//     or any pubkey could introduce a board key and then author cards under it.
//  3. The grant's SIGNED p tag names this reader. NIP-44 v2 has no AAD, so the
//     wrap itself does not say who it is for; the binding is the owner's
//     signature over the p tag.
//  4. The wrap actually OPENS for this reader. That is the ECDH binding, and it
//     is what makes a captured wrapped-CEK useless when lifted into a grant
//     p-tagged to somebody else — re-signing the grant needs the owner's key,
//     and the wrap will not open for the new grantee.
//
// ALL historical grants are scanned, NOT latest-wins: a member keeps the
// old-epoch CEKs it was already given, so historical reads survive a rotation.
// A revoked member simply never receives a wrap for the NEW epoch — that is the
// whole mechanism of forward secrecy here, and it is why revocation shows up in
// this file as an absence rather than as a rule.

import type { NostrEvent } from "./nostrevent";
import { tagValue, verifyEvent } from "./nostrevent";
import { parseBoardCoord, boardCoord, KIND_BOARD } from "./boarddiscovery";
import type { KeyUnwrapper } from "./keyunwrap";
import type { FragmentKeys } from "./fragment";

/** KIND_ROLE_GRANT = 39301, matching pkg/sync/rolegrant.go's KindRoleGrant. */
export const KIND_ROLE_GRANT = 39301;

const ROLES = new Set(["owner", "maintainer", "contributor", "revoked"]);

/** RoleGrant is the parsed view of a verified kind-39301 event — the fields
 * this client needs, named as in pkg/sync/rolegrant.go's roleGrant. */
export interface RoleGrant {
  signer: string;
  grantee: string;
  role: string;
  boardOwner: string;
  boardD: string;
  createdAt: number;
  wrappedCEK: string;
  cekEpoch: number;
  wrappedLTK: string;
}

/**
 * parseRoleGrant extracts a RoleGrant from a kind-39301 event, or null when the
 * event is not a well-formed grant (wrong kind, missing grantee/role,
 * unrecognized role, or an "a" coordinate that does not name a 30301 board).
 * Like the Go original it does NOT verify the signature — deriveBoardKeyring
 * does that first.
 *
 * An unparseable cek_epoch tag becomes 0, exactly as Go's strconv.Atoi-with-
 * fallback does, and deriveBoardKeyring rejects any grant with epoch < 1 rather
 * than binding a key to a bogus epoch.
 */
export function parseRoleGrant(e: NostrEvent): RoleGrant | null {
  if (!e || e.kind !== KIND_ROLE_GRANT) return null;
  const grantee = tagValue(e, "p");
  const role = tagValue(e, "role");
  if (grantee === "" || role === "" || !ROLES.has(role)) return null;
  const coord = parseBoardCoord(tagValue(e, "a"));
  if (!coord) return null;

  let cekEpoch = 0;
  const rawEpoch = tagValue(e, "cek_epoch");
  if (rawEpoch !== "" && /^-?\d+$/.test(rawEpoch)) {
    const n = Number(rawEpoch);
    if (Number.isSafeInteger(n)) cekEpoch = n;
  }

  return {
    signer: e.pubkey,
    grantee,
    role,
    boardOwner: coord.owner,
    boardD: coord.boardD,
    createdAt: e.created_at,
    wrappedCEK: tagValue(e, "cek"),
    cekEpoch,
    wrappedLTK: tagValue(e, "ltk"),
  };
}

/**
 * BoardKeyring is what a reader can decrypt, and nothing else. It is the browser
 * counterpart of pkg/sync/keydist.go's BoardKeyring and answers the same three
 * questions: which CEK for (board, epoch), which LTK, and when did this board go
 * confidential.
 *
 * IT IS NEVER PERSISTED. There is no serializer here, no toJSON, no storage
 * call anywhere in this module — see storage.test.ts, which asserts no key
 * material reaches localStorage / sessionStorage / IndexedDB. The keyring is
 * rebuilt from signed relay events on every load, which costs one extension
 * prompt and buys "a stolen browser profile contains no board keys".
 */
export class BoardKeyring {
  private readonly ceks = new Map<string, Map<number, Uint8Array>>();
  private readonly ltks = new Map<string, Uint8Array>();
  private readonly cutovers = new Map<string, number>();
  private readonly grantEpochFloors = new Map<string, number>();
  private readonly granteeCEKGrants = new Map<string, number>();
  private readonly assertedSince = new Map<string, number>();

  /** cek returns the content key for (coord, epoch), or null when this reader
   * holds none. Null is the fail-closed answer and every caller renders the
   * placeholder for it. */
  cek(coord: string, epoch: number): Uint8Array | null {
    return this.ceks.get(coord)?.get(epoch) ?? null;
  }

  /** ltk returns the board's label-token key, or null.
   *
   * READ BY THE WRITE PATH since ready-191: main.ts hands it to
   * NostrBoardWriter as `enc.ltk`, and board/writeevents.ts emits each label as
   * labelToken(ltk, label) so a confidential card's `l` tags are relay-filterable
   * without being readable (spec §7). Null is the fail-closed answer and means
   * NO `l` tag is emitted at all — never a plaintext one; rd's own writer takes
   * the identical branch (pkg/sync/nostrwire.go). Labels are still filtered
   * client-side off the decrypted plaintext, so nothing is lost when it is null.
   *
   * Which sessions hold one: a GRANT session does, when the grant carries an
   * `ltk` tag. A LINK-KEY session does NOT — `rd board --with-key` stopped
   * emitting ltk= (fragment.ts's header) — and does not need one, because it
   * cannot write at all. */
  ltk(coord: string): Uint8Array | null {
    return this.ltks.get(coord) ?? null;
  }

  /**
   * cutover returns the board-global created_at of the first CEK-bearing owner
   * grant, or null when the board is not confidential at all.
   *
   * It is tracked from EVERY owner CEK grant, not only the ones addressed to
   * this reader, so a reader who holds no key still knows the board is
   * confidential — which is what lets the fold gate quarantine post-cutover
   * cleartext for a stranger instead of rendering it.
   */
  cutover(coord: string): number | null {
    return this.cutovers.get(coord) ?? null;
  }

  /**
   * grantEpochFloor returns the LOWEST cek_epoch named by any owner CEK grant
   * that was served for this board, or null when none was.
   *
   * It exists because `cutover` above is a MINIMUM and therefore only ever moves
   * LATER when events go missing — so the cutover alone cannot say whether the
   * EARLIEST grant is among the ones that arrived. This can, for the case that
   * matters (ready-daf round 2): epochs increase by one per rotation and a card
   * cannot be sealed under an epoch before that epoch's grant exists, so a sealed
   * card naming an epoch BELOW this floor is proof that an older grant — and thus
   * an earlier cutover — was not served. main.ts's confidentialityOf is the
   * consumer; see the witness comments there.
   *
   * Board-GLOBAL and tracked from the same grants as the cutover: every owner
   * CEK grant regardless of grantee, so it does not vary with who is reading.
   * DISTINCT from `epochs()`, which is only what THIS reader can decrypt.
   */
  grantEpochFloor(coord: string): number | null {
    return this.grantEpochFloors.get(coord) ?? null;
  }

  /**
   * confidentialSince returns the OWNER-SIGNED cutover assertion this board's
   * own kind-30301 definition carries (`confidential_since`, §11.13a,
   * ready-475), or null when no verified definition for this coordinate asserted
   * one. Port of pkg/sync's AssertedConfidentialSince — read that file's header
   * for why an assertion is an extension of §11.13a rather than a weakening of
   * it.
   *
   * It is a DIFFERENT KIND OF FACT from `cutover` above, which is why it is a
   * separate question rather than folded invisibly into that one: `cutover` is
   * DERIVED (a minimum over the grants that arrived, so only ever a lower bound
   * on the truth, which is the entire reason §11.13a's witnesses exist), while
   * this is the owner — the same key that mints every CEK — STATING the instant.
   * confidentialityOf consults it to decide whether the witnesses have anything
   * left to establish; nothing else may use it to widen what is shown.
   *
   * The value has already been folded into `cutover` as a MINIMUM, so an
   * assertion can only move the effective instant EARLIER and never grandfathers
   * a card the served grants alone would have quarantined.
   */
  confidentialSince(coord: string): number | null {
    return this.assertedSince.get(coord) ?? null;
  }

  /**
   * granteeGrants counts the owner-signed CEK-bearing grants that NAME THIS
   * READER on this board — grants that passed checks 1-3 — whether or not their
   * wrap then opened (check 4).
   *
   * IT IS THE DIFFERENCE BETWEEN TWO FACTS THE PAGE OTHERWISE CANNOT TELL APART,
   * and both of them end in the same silent absence of a key (ready-27b):
   *
   *   * "no grant naming this key reached this page" — nobody granted you, or the
   *     relay did not serve it. Nothing this reader can do but ask the owner.
   *   * "a grant naming this key DID reach this page and this browser could not
   *     open it" — count > 0 with no CEK. That is exactly what a pre-ready-c4b
   *     RAW-PAYLOAD wrap looks like from here (keyunwrap.ts's header: a raw
   *     32-byte plaintext cannot survive NIP-07's TextDecoder, so the honest
   *     outcome is the placeholder), and it is the state 14 of the ~24 live
   *     boards are in until `rd confidential rewrap` runs on each. The reader is
   *     entitled to that board and the page renders it as [encrypted] anyway.
   *
   * Counted before the unwrap, so a declined extension prompt lands here too —
   * which is why the notice main.ts builds from this says "this browser could not
   * open it", not "the grant is malformed". The page cannot tell those apart and
   * must not claim to.
   *
   * Counted per COORDINATE, like every other field here, so it can never be
   * reported against a board other than the one the grant named.
   */
  granteeGrants(coord: string): number {
    return this.granteeCEKGrants.get(coord) ?? 0;
  }

  /** epochs lists the CEK epochs this reader holds for a board, ascending.
   * Diagnostics and tests only. */
  epochs(coord: string): number[] {
    return [...(this.ceks.get(coord)?.keys() ?? [])].sort((a, b) => a - b);
  }

  /**
   * currentEpoch returns the HIGHEST CEK epoch this reader holds for the board,
   * or null when it holds none. Port of pkg/sync/keydist.go's CurrentEpoch
   * (board-fold-spec.md §11.14).
   *
   * THIS IS THE EPOCH A WRITE SEALS UNDER, which is why it is a distinct question
   * from `epochs()` and is NOT "the epoch of the newest grant I saw": a member
   * that missed a rotation holds a STALE highest epoch and seals under it (the
   * owner, who minted the rotation and self-wrapped it, always holds the true
   * current one). Sealing under any other held epoch — the lowest, or whichever
   * grant arrived last — publishes a card that part of the board cannot read, and
   * nothing on the READ path would ever report it.
   */
  currentEpoch(coord: string): number | null {
    const held = this.epochs(coord);
    return held.length === 0 ? null : held[held.length - 1];
  }

  /** @internal */
  addCEK(coord: string, epoch: number, cek: Uint8Array): void {
    let m = this.ceks.get(coord);
    if (!m) {
      m = new Map();
      this.ceks.set(coord, m);
    }
    m.set(epoch, cek);
  }

  /** @internal */
  addLTK(coord: string, ltk: Uint8Array): void {
    this.ltks.set(coord, ltk);
  }

  /** @internal */
  noteCutover(coord: string, at: number): void {
    const cur = this.cutovers.get(coord);
    if (cur === undefined || at < cur) this.cutovers.set(coord, at);
  }

  /** @internal */
  noteGranteeGrant(coord: string): void {
    this.granteeCEKGrants.set(coord, (this.granteeCEKGrants.get(coord) ?? 0) + 1);
  }

  /** @internal */
  noteGrantEpoch(coord: string, epoch: number): void {
    const cur = this.grantEpochFloors.get(coord);
    if (cur === undefined || epoch < cur) this.grantEpochFloors.set(coord, epoch);
  }

  /** @internal — records an owner-signed `confidential_since` assertion and
   * folds it into the cutover as a MINIMUM (never later than what the grants
   * already established). Both halves happen here so a caller cannot record one
   * without the other. */
  noteConfidentialSince(coord: string, at: number): void {
    const cur = this.assertedSince.get(coord);
    if (cur === undefined || at < cur) this.assertedSince.set(coord, at);
    this.noteCutover(coord, at);
  }
}

/**
 * boardConfidentialSince reads the `confidential_since` assertion off ONE
 * kind-30301 event, or null when the tag is absent, unparseable, or not a
 * positive instant. Port of pkg/sync.BoardConfidentialSince.
 *
 * A non-positive value is NOT an assertion. Rejecting it here keeps a malformed
 * tag on the ordinary §11.13a path instead of pinning the cutover to 0, which
 * would quarantine the board's whole plaintext history.
 */
export function boardConfidentialSince(e: NostrEvent): number | null {
  const raw = tagValue(e, "confidential_since");
  if (raw === "" || !/^\d+$/.test(raw)) return null;
  const n = Number(raw);
  if (!Number.isSafeInteger(n) || n <= 0) return null;
  return n;
}

/**
 * assertedConfidentialSince returns the owner-signed cutover assertion for coord
 * among events, or null when none carries one. Port of
 * pkg/sync.AssertedConfidentialSince, and the two must agree event-for-event:
 * the conformance vectors fold the same committed board definitions through
 * both.
 *
 * THE AUTHOR CHECK IS THE COORDINATE CHECK. A kind-30301's coordinate is
 * `30301:<its own pubkey>:<its own d tag>`, so an event only matches coord when
 * its author IS coord's owner — a definition signed by anybody else lands on a
 * different coordinate and is invisible here. verifyEvent then rejects a forged
 * or tampered one, because the relay serving these events is untrusted.
 *
 * THE MINIMUM, not latest-wins: a relay may serve an OLDER definition it still
 * holds, and under latest-wins that replay would decide which assertion a reader
 * believes. Under the minimum it cannot — the only direction a replayed
 * definition can move the effective cutover is EARLIER, which quarantines MORE.
 */
export function assertedConfidentialSince(events: NostrEvent[], coord: string): number | null {
  let best: number | null = null;
  for (const e of events) {
    if (!e || e.kind !== KIND_BOARD) continue;
    // The coordinate embeds the author: this IS the owner check.
    if (boardCoord(e.pubkey, tagValue(e, "d")) !== coord) continue;
    const since = boardConfidentialSince(e);
    if (since === null) continue;
    // Last, because it is the expensive one and every event above it is
    // eliminated by a tag comparison first.
    if (!verifyEvent(e)) continue;
    if (best === null || since < best) best = since;
  }
  return best;
}

/**
 * deriveBoardKeyring scans a relay snapshot for owner-signed kind-39301 grants
 * and builds this reader's key material for the board (boardAuthor, boardD).
 * Port of pkg/sync/keydist.go's DeriveBoardKeyring — see the module header for
 * the four checks and why each one is load-bearing.
 *
 * `unwrap` is the signer seam (keyunwrap.ts): in production it prompts the
 * NIP-07 extension, in tests it is a spec-correct NIP-44 v2 implementation. It
 * is called only for grants that already passed checks 1-3, so a hostile relay
 * cannot make the page spam the extension with prompts for grants addressed to
 * other people.
 *
 * Never throws. A malformed grant, a failed unwrap, or a key of the wrong size
 * is skipped, and the reader ends up holding fewer keys — which renders as more
 * placeholders, never as a wrong plaintext.
 */
export async function deriveBoardKeyring(
  events: NostrEvent[],
  readerPubkey: string,
  boardAuthor: string,
  boardD: string,
  unwrap: KeyUnwrapper,
): Promise<BoardKeyring> {
  const coord = boardCoord(boardAuthor, boardD);
  const kr = new BoardKeyring();
  if (boardAuthor === "" || boardD === "") return kr;

  for (const e of events) {
    if (!e || e.kind !== KIND_ROLE_GRANT) continue;
    // CHECK 1 — a relay is untrusted. An unverified grant mints nothing.
    if (!verifyEvent(e)) continue;
    const g = parseRoleGrant(e);
    if (!g) continue;
    if (g.boardOwner !== boardAuthor || g.boardD !== boardD) continue;
    // CHECK 2 — only the OWNER mints CEKs. A CEK "wrapped" by anyone else is
    // ignored: otherwise any pubkey could introduce a board key.
    if (g.signer !== boardAuthor || g.wrappedCEK === "") continue;
    // A valid epoch is >= 1 (cards seal under epoch >= 1). Reject a grant whose
    // cek_epoch tag did not parse rather than binding a key to epoch 0 or
    // setting the board cutover from a malformed grant.
    if (g.cekEpoch < 1) continue;

    // Board-global cutover: earliest owner CEK-bearing grant, tracked
    // regardless of who the grant is addressed to. The epoch floor is tracked
    // from the SAME grants, in the same place, so the two can never disagree
    // about which grants were counted (ready-daf round 2).
    kr.noteCutover(coord, g.createdAt);
    kr.noteGrantEpoch(coord, g.cekEpoch);

    // CHECK 3 — the SIGNED p tag must name this reader.
    if (g.grantee !== readerPubkey) continue;

    // ready-27b: this reader is NAMED by an owner-signed CEK grant on this
    // board. Recorded BEFORE the unwrap, because the whole value of the record
    // is that it survives the unwrap failing — see granteeGrants' doc.
    kr.noteGranteeGrant(coord);

    // CHECK 4 — the wrap must actually open for this reader's key (ECDH
    // binding). A wrap lifted out of someone else's grant fails here.
    const cek = await unwrap(g.signer, g.wrappedCEK);
    if (cek && cek.length === 32) kr.addCEK(coord, g.cekEpoch, cek);

    if (g.wrappedLTK !== "") {
      const ltk = await unwrap(g.signer, g.wrappedLTK);
      if (ltk && ltk.length === 32) kr.addLTK(coord, ltk);
    }
  }

  // §11.13a's OWNER-SIGNED ASSERTION (ready-475): the board's own kind-30301
  // definition may STATE the cutover the grants above can only bound. Folded in
  // as a MINIMUM, so it can only ever move the instant EARLIER — see
  // noteConfidentialSince and confidentialSince. confidentialityOf is what acts
  // on its presence; recording it here keeps the derivation in one place and
  // mirrors pkg/sync's DeriveBoardKeyring exactly.
  const since = assertedConfidentialSince(events, coord);
  if (since !== null) kr.noteConfidentialSince(coord, since);

  return kr;
}

/**
 * applyFragmentKeys adds the key material an `rd board --with-key` link carries
 * (ready-df0) to a keyring that was ALREADY derived from signed relay events.
 *
 * WHY THIS IS NOT A HOLE IN THE FOUR CHECKS ABOVE. Those checks answer "may this
 * relay-served event mint a key for this reader?" — the relay is untrusted, so
 * every wrapped key has to prove owner provenance and ECDH binding before it
 * counts. A fragment key answers a different question. It did not come from a
 * relay; it came out of the reader's own `rd` on the reader's own machine, where
 * DeriveBoardKeyring had already run those same four checks against the local
 * signed log before the CLI would print it. Re-deriving provenance in the
 * browser is impossible anyway (the whole point is that the page holds no secret
 * key and so can do no ECDH) and would prove nothing new.
 *
 * THE OTHER HALF OF THAT ARGUMENT — "and the session holding it cannot sign" —
 * IS NOW ENFORCED, NOT ASSUMED (ready-de7). It used to be neither: a
 * `#board=<coord>&cek=<epoch>:<hex>` link with NO `pk=` parsed happily, main.ts
 * had no viewer to open as so it showed the LOGIN FORM, and a visitor who logged
 * in there with a NIP-07 extension reached this function with a SIGNING
 * identity. The bypass's justification was false on that path. fragment.ts now
 * refuses a cek= that arrives without a pk= (the rule the portfolio shape
 * already had), so the chain holds end to end: CEKs imply pk=, pk= implies
 * main.ts mints `method: "readOnly"` (main.fragmentkey.test.ts, "the pk=
 * identity CANNOT SIGN"), read-only implies NostrBoardWriter is built with no
 * signer and refuses every write (main.test.ts, ready-1af). Witnessed as one
 * path by main.fragmentkey.test.ts's "A CEK CANNOT REACH A SIGNING SESSION".
 * Nothing here inspects the identity — this function is handed keys and applies
 * them, exactly as before; the premise is established upstream, at parse time,
 * before an identity exists at all.
 *
 * WHAT IS STILL ENFORCED, and must stay enforced:
 *  - Keys apply to ONE coordinate — the one named in the same fragment — and
 *    nowhere else. A key can never leak across boards.
 *  - Nothing here touches `cutovers`. Whether a board is confidential, and from
 *    when, still comes ONLY from owner-signed grants (see cutover()), so the
 *    fold gate that quarantines post-cutover cleartext is untouched: a link
 *    cannot declare a board confidential, and cannot declare it public either.
 *  - Nothing here touches `grantEpochFloors` either, and that omission is a
 *    CONTROL (ready-daf round 2). A link carries keys for the epochs its minter
 *    happened to hold; feeding those into the floor would let a link silence the
 *    epoch witness — hand a reader a link with an epoch-1 key and the floor drops
 *    to 1, so an epoch-1 sealed card stops contradicting a served epoch-2-only
 *    grant set, and the manufactured late cutover goes undetected. The floor must
 *    stay a statement about the GRANTS THAT ARRIVED and nothing else.
 *  - Nothing here touches event verification. Every card still has to pass its
 *    own signature check in the fold, and every fetch is still #a-scoped to a
 *    board that survived verification.
 *  - A wrong key is still fail-closed: the AEAD simply does not open and the
 *    title renders as the placeholder, never as ciphertext.
 *
 * The keyring is still never persisted — see the class doc above and
 * nostorage.test.ts.
 */
export function applyFragmentKeys(kr: BoardKeyring, coord: string, keys: FragmentKeys): void {
  for (const { epoch, key } of keys.ceks) {
    if (key.length === 32 && Number.isSafeInteger(epoch) && epoch >= 1) kr.addCEK(coord, epoch, key);
  }
  // keys.ltk arrives only from a link minted before the LTK was dropped from
  // emission (fragment.ts header). Applied for consistency with the CEKs — the
  // keyring's shape should not depend on which build minted the link.
  //
  // ltk() DOES have a production reader now (the write path, ready-191), so this
  // line is no longer inert — but it still cannot become a write escalation: a
  // session that reached applyFragmentKeys came in through `pk=`, which mints a
  // read-only identity with no signer, so every write is refused before an event
  // is built. Pinned by main.fragmentkey.test.ts's "a LEGACY link that DOES carry
  // ltk= is refused identically".
  if (keys.ltk && keys.ltk.length === 32) kr.addLTK(coord, keys.ltk);
}
