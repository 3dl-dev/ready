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
import { parseBoardCoord, boardCoord } from "./boarddiscovery";
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

  /** cek returns the content key for (coord, epoch), or null when this reader
   * holds none. Null is the fail-closed answer and every caller renders the
   * placeholder for it. */
  cek(coord: string, epoch: number): Uint8Array | null {
    return this.ceks.get(coord)?.get(epoch) ?? null;
  }

  /** ltk returns the board's label-token key, or null. Held so a future path
   * that pushes an `#l` filter to a relay can tokenize the label first (spec
   * §7); labels themselves are filtered client-side on decrypted plaintext. */
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
  noteGrantEpoch(coord: string, epoch: number): void {
    const cur = this.grantEpochFloors.get(coord);
    if (cur === undefined || epoch < cur) this.grantEpochFloors.set(coord, epoch);
  }
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

    // CHECK 4 — the wrap must actually open for this reader's key (ECDH
    // binding). A wrap lifted out of someone else's grant fails here.
    const cek = await unwrap(g.signer, g.wrappedCEK);
    if (cek && cek.length === 32) kr.addCEK(coord, g.cekEpoch, cek);

    if (g.wrappedLTK !== "") {
      const ltk = await unwrap(g.signer, g.wrappedLTK);
      if (ltk && ltk.length === 32) kr.addLTK(coord, ltk);
    }
  }
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
  // keyring's shape should not depend on which build minted the link — but
  // nothing in this app reads BoardKeyring.ltk() today.
  if (keys.ltk && keys.ltk.length === 32) kr.addLTK(coord, keys.ltk);
}
