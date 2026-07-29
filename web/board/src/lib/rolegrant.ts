// TS port of pkg/sync/rolegrant.go's DeriveLevels + the escalation cap —
// board-fold-spec.md §12. Role grants (kind 39301) feed read-trust, graded
// operator levels, and status-authority (via the fold's boardMaintainers
// union in fold.ts) but produce no item of their own.
//
// GAP CLOSED (ready-ce8; the gap was recorded here by ready-35b). The
// escalation cap below IS now exercised by the committed corpus, in THIS
// implementation and not only in Go's: fold.vectors.test.ts replays
// testdata/fold.vectors.json through projectItems, and ready-ce8's six
// grant_cap_* / revoke_boundary_* / grant_level_two_* vectors were each proven
// to turn THIS file (and fold.ts) red under the mutation they exist to catch.
// Measured against this port, not asserted:
//
//	signerMayGrant -> `return true`                  -> 4 vectors fail
//	roleToLevel: contributor -> LEVEL_MAINTAINER     -> 4 vectors fail
//	fold.ts §3.5 `>=` -> `>`                         -> 1 vector fails
//	fold.ts: delete the §6.2 grant-maintainer fold    -> 1 vector fails
//
// So a divergence between this port and pkg/sync/rolegrant.go in the escalation
// cap, the role->level table, the revocation boundary or the grant-derived
// maintainer fold is now a CI failure rather than a silent difference. Do not
// weaken any of the four without expecting those vectors to go red — that is
// exactly their job.

import type { NostrEvent } from "./nostrevent";
import { tagValue } from "./nostrevent";

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
 * `events` are assumed already schnorr-verified (the same events passed to
 * projectItems — see fold.ts, which verifies once and reuses the result). */
export function deriveLevels(
  events: (NostrEvent | null)[],
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
