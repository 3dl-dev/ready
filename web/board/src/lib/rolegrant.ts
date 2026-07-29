// TS port of pkg/sync/rolegrant.go's DeriveLevels + the escalation cap —
// board-fold-spec.md §12. Role grants (kind 39301) feed read-trust, graded
// operator levels, and status-authority (via the fold's boardMaintainers
// union in fold.ts) but produce no item of their own.
//
// KNOWN GAP (ready-35b): disabling the escalation cap entirely leaves every
// committed vector green (ready-ce8) — the cap logic below is implemented
// per spec but is NOT exercised by the committed vector suite. Treat
// signerMayGrant as spec-faithful-but-vector-unverified until ready-ce8 adds
// coverage.

import type { NostrEvent, VerifiedEvent } from "./nostrevent";
import { tagValue, verifyEvent } from "./nostrevent";

export const KIND_ROLE_GRANT = 39301;

export const ROLE_OWNER = "owner";
export const ROLE_MAINTAINER = "maintainer";
export const ROLE_CONTRIBUTOR = "contributor";
export const ROLE_REVOKED = "revoked";

export const LEVEL_REVOKED = 0;
export const LEVEL_CONTRIBUTOR = 1;
export const LEVEL_MAINTAINER = 2;
export const LEVEL_DEFAULT = LEVEL_CONTRIBUTOR;

// authoritativeForever: a non-revoked key's events are always honored. Using
// JS `Infinity` (rather than a finite sentinel) makes `createdAt >= until`
// correctly false for every representable createdAt, matching Go's
// math.MaxInt64 sentinel's role exactly.
export const AUTHORITATIVE_FOREVER = Infinity;

export const KindBoard = 30301;

/** BoardCoord returns "30301:<pubkey>:<boardD>" (pkg/sync/nostrwire.go:193). */
export function boardCoord(pubkey: string, boardD: string): string {
  return `${KindBoard}:${pubkey}:${boardD}`;
}

/** parseBoardCoord splits "30301:<owner>:<d>", ok=false if malformed. Mirrors
 * pkg/sync/rolegrant.go's parseBoardCoord: SplitN=3 so a boardD containing
 * ':' survives intact. */
export function parseBoardCoord(a: string): { owner: string; boardD: string; ok: boolean } {
  const parts = splitN3(a, ":");
  if (parts.length !== 3) return { owner: "", boardD: "", ok: false };
  if (parts[0] !== String(KindBoard)) return { owner: "", boardD: "", ok: false };
  if (parts[1] === "" || parts[2] === "") return { owner: "", boardD: "", ok: false };
  return { owner: parts[1], boardD: parts[2], ok: true };
}

function splitN3(s: string, sep: string): string[] {
  const out: string[] = [];
  let rest = s;
  for (let i = 0; i < 2; i++) {
    const idx = rest.indexOf(sep);
    if (idx < 0) break;
    out.push(rest.slice(0, idx));
    rest = rest.slice(idx + 1);
  }
  out.push(rest);
  return out;
}

interface RoleGrant {
  signer: string;
  grantee: string;
  role: string;
  boardOwner: string;
  boardD: string;
  from: number;
  createdAt: number;
  id: string;
  claim: string;
}

/** parseRoleGrant mirrors pkg/sync/rolegrant.go's parseRoleGrant. Does NOT
 * verify the signature — the caller must do that first. */
function parseRoleGrant(e: NostrEvent): RoleGrant | null {
  if (!e || e.kind !== KIND_ROLE_GRANT) return null;
  const grantee = tagValue(e, "p");
  const role = tagValue(e, "role");
  if (grantee === "" || role === "") return null;
  if (role !== ROLE_OWNER && role !== ROLE_MAINTAINER && role !== ROLE_CONTRIBUTOR && role !== ROLE_REVOKED) {
    return null;
  }
  const { owner, boardD, ok } = parseBoardCoord(tagValue(e, "a"));
  if (!ok) return null;
  let from = 0;
  const fromTag = tagValue(e, "from");
  if (fromTag !== "") {
    const v = Number(fromTag);
    if (!Number.isInteger(v) || v < 0 || !/^\d+$/.test(fromTag)) return null;
    from = v;
  }
  return {
    signer: e.pubkey,
    grantee,
    role,
    boardOwner: owner,
    boardD,
    from,
    createdAt: e.created_at,
    id: e.id,
    claim: tagValue(e, "claim"),
  };
}

/** roleToLevel mirrors pkg/sync/rolegrant.go's roleToLevel. */
function roleToLevel(role: string): number {
  switch (role) {
    case ROLE_OWNER:
    case ROLE_MAINTAINER:
      return LEVEL_MAINTAINER;
    case ROLE_CONTRIBUTOR:
      return LEVEL_CONTRIBUTOR;
    case ROLE_REVOKED:
      return LEVEL_REVOKED;
    default:
      return LEVEL_DEFAULT;
  }
}

/** newerGrant mirrors newerThan's tie-break: greater created_at wins; on a
 * tie the lexicographically LOWEST event id wins. */
function newerGrant(a: RoleGrant, b: RoleGrant): boolean {
  if (a.createdAt !== b.createdAt) return a.createdAt > b.createdAt;
  return a.id < b.id;
}

/** signerMayGrant mirrors pkg/sync/rolegrant.go's escalation cap (spec §12.5). */
function signerMayGrant(
  levels: Map<string, number>,
  boardAuthor: string,
  signer: string,
  grantee: string,
  role: string,
): boolean {
  const isBoardAuthor = signer !== "" && signer === boardAuthor;
  switch (role) {
    case ROLE_MAINTAINER:
    case ROLE_OWNER:
      return isBoardAuthor;
    case ROLE_CONTRIBUTOR:
    case ROLE_REVOKED: {
      if (isBoardAuthor) return true;
      if ((levels.get(signer) ?? LEVEL_DEFAULT) < LEVEL_MAINTAINER) return false;
      if (grantee === boardAuthor) return false;
      if ((levels.get(grantee) ?? LEVEL_DEFAULT) >= LEVEL_MAINTAINER) return false;
      return true;
    }
    default:
      return false;
  }
}

export interface DerivedGrants {
  levels: Map<string, number>;
  until: Map<string, number>;
}

/** DeriveLevels mirrors pkg/sync/rolegrant.go's DeriveLevels: replays the
 * verified 39301 events bound to 30301:<boardAuthor>:<boardD> and returns the
 * {pubkey -> level} and {pubkey -> authoritative-until} maps (spec §12).
 *
 * SECURITY (ready-75a — this comment describes what the code DOES, it is not
 * an assumption about the caller). Read-trust levels and the prospective
 * revocation bound are derived here, so an unverified grant reaching this
 * replay lets an untrusted relay undo a board owner's revocation with an event
 * carrying no valid signature. Two independent defences, both live:
 *
 *   1. `events` is VerifiedEvent[] — a branded type only nostrevent.ts's
 *      verifiedEvents() can mint, and it mints it by RUNNING verifyEvent.
 *      Passing the raw relay array is a compile error.
 *   2. The replay loop below ALSO calls verifyEvent per grant, exactly as
 *      Go's deriveGrants does ("a forged/tampered grant cannot influence
 *      levels", pkg/sync/rolegrant.go). The brand is erased at runtime; this
 *      is not, so a caller that casts around (1) still cannot poison levels.
 *
 * The original defect was this port dropping Go's in-loop e.Verify() while its
 * doc comment claimed the caller had already verified — the caller (fold.ts)
 * verified in a DIFFERENT loop whose result never reached here. Do not remove
 * either defence. */
export function deriveLevels(
  events: readonly (VerifiedEvent | null)[],
  boardAuthor: string,
  boardD: string,
): DerivedGrants {
  const levels = new Map<string, number>();
  const until = new Map<string, number>();

  if (boardAuthor !== "") {
    levels.set(boardAuthor, LEVEL_MAINTAINER);
    until.set(boardAuthor, AUTHORITATIVE_FOREVER);
  }

  const grants: RoleGrant[] = [];
  for (const e of events) {
    if (!e || e.kind !== KIND_ROLE_GRANT) continue;
    // Only signed, internally-consistent events count (schnorr) — mirrors the
    // read-side gate and pkg/sync/rolegrant.go's deriveGrants: a forged or
    // tampered grant cannot influence levels. Defence (2) above; the
    // VerifiedEvent parameter type is defence (1).
    if (!verifyEvent(e)) continue;
    const g = parseRoleGrant(e);
    if (!g) continue;
    if (boardAuthor === "" || boardD === "" || g.boardOwner !== boardAuthor || g.boardD !== boardD) continue;
    grants.push(g);
  }

  // Replay oldest-first: ascending (created_at, id).
  grants.sort((a, b) => (newerGrant(b, a) ? -1 : newerGrant(a, b) ? 1 : 0));

  const claimedBy = new Map<string, string>();
  for (const g of grants) {
    if (!signerMayGrant(levels, boardAuthor, g.signer, g.grantee, g.role)) continue;
    if (g.claim !== "" && g.signer === boardAuthor) {
      const bound = claimedBy.get(g.claim);
      if (bound !== undefined && bound !== g.grantee) continue;
      claimedBy.set(g.claim, g.grantee);
    }
    levels.set(g.grantee, roleToLevel(g.role));
    if (g.role === ROLE_REVOKED) {
      until.set(g.grantee, g.from > 0 ? g.from : g.createdAt);
    } else {
      until.set(g.grantee, AUTHORITATIVE_FOREVER);
    }
  }

  return { levels, until };
}
