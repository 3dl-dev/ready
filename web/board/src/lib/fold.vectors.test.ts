// THE VECTOR SUITE IS THE CONTRACT (ready-35b). This test consumes
// testdata/fold.vectors.json — the SAME committed file
// internal/foldvectors/vectors_test.go runs against rd's own Go fold — BYTE
// FOR BYTE, from its real repo path. It is not copied, not re-encoded, not a
// parallel fixture set: an edit to that file changes what this test asserts
// with zero changes here.
//
// FormatVersion is 2: expect.items[].created_at/updated_at are DECIMAL
// STRINGS (arbitrary int64 unix nanoseconds — JS Number cannot hold that
// range exactly, spec §4.8), parsed as BigInt via state.ts's Item type; never
// Number() and never a generic JSON number parser.

import { describe, expect, it } from "vitest";
// Static JSON import (tsconfig.json: resolveJsonModule) of the COMMITTED
// repo-root vector file — not a copy, not a fetch, not fs.readFileSync: Vite/
// Vitest resolves and parses it like any other module, so there is exactly
// one on-disk copy of this file in the whole repo.
import vectorFileJSON from "../../../../testdata/fold.vectors.json";
import type { NostrEvent } from "./nostrevent";
import { projectItems } from "./fold";
import type { ProjectOptions } from "./fold";
import type { BoardDecryptor, EncryptedBoardSet } from "./envelope";
import { encodeItem } from "./state";
import type { Item } from "./state";
import { named, labelFilter, apply, allNames } from "./views";
import { hexToBytes } from "./sha256";

interface VectorOptions {
  trusted: string[] | null;
  maintainers: string[] | null;
  pinned_board: string;
  decryptor: { keys: { board_coord: string; epoch: number; cek_hex: string }[] } | null;
  encrypted_boards: { boards: { board_coord: string; cutover: number }[] } | null;
}

interface Vector {
  name: string;
  spec_clauses: string[];
  note: string;
  options: VectorOptions;
  identity: string;
  events: (NostrEvent | null)[];
  expect: {
    items: unknown[];
    views: Record<string, string[]>;
    label_views?: Record<string, string[]>;
  };
}

interface VectorFile {
  version: number;
  spec: string;
  note: string;
  timestamp_encoding: string;
  keys: { name: string; secret: string; pubkey: string }[];
  vectors: Vector[];
}

function buildDecryptor(spec: VectorOptions["decryptor"]): BoardDecryptor | null {
  if (!spec) return null;
  const map = new Map<string, Uint8Array>();
  for (const k of spec.keys) map.set(`${k.board_coord}|${k.epoch}`, hexToBytes(k.cek_hex));
  return {
    cek(boardCoord, epoch) {
      return map.get(`${boardCoord}|${epoch}`) ?? null;
    },
  };
}

function buildEncryptedBoards(spec: VectorOptions["encrypted_boards"]): EncryptedBoardSet | null {
  if (!spec) return null;
  const map = new Map<string, number>();
  for (const b of spec.boards) map.set(b.board_coord, b.cutover);
  return {
    cutover(boardCoord) {
      const c = map.get(boardCoord);
      return c === undefined ? { cutover: 0, ok: false } : { cutover: c, ok: true };
    },
  };
}

function toProjectOptions(o: VectorOptions): ProjectOptions {
  return {
    trusted: o.trusted === null ? null : new Set(o.trusted),
    maintainers: o.maintainers && o.maintainers.length > 0 ? new Set(o.maintainers) : null,
    pinnedBoard: o.pinned_board,
    decryptor: buildDecryptor(o.decryptor),
    encryptedBoards: buildEncryptedBoards(o.encrypted_boards),
  };
}

function sortedIds(items: Item[]): string[] {
  return items.map((i) => i.id).sort();
}

const file = vectorFileJSON as unknown as VectorFile;

describe("fold.vectors.json conformance", () => {
  it(`loads at least 30 vectors (format version ${file.version})`, () => {
    expect(file.version).toBe(2);
    expect(file.vectors.length).toBeGreaterThanOrEqual(30);
  });

  // Reported explicitly per the item's done condition: the vector count must
  // equal the Go suite's count (both read the same file, so this is really a
  // sanity check that nothing truncated the read).
  console.log(
    `fold conformance: ${file.vectors.length} vectors from testdata/fold.vectors.json (spec ${file.spec})`,
  );

  it.each(file.vectors)("$name", (v: Vector) => {
    const opts = toProjectOptions(v.options);
    const projected = projectItems(v.events, opts);

    const orderedIds = Array.from(projected.keys()).sort();
    const gotItems = orderedIds.map((id) => encodeItem(projected.get(id)!));
    const wantItems = v.expect.items as Record<string, unknown>[];

    expect(gotItems.length).toBe(wantItems.length);
    // Both sides are sorted by id (the vector file documents its expect.items
    // as sorted by id; ours is sorted the same way above), so a positional
    // compare is a same-item compare.
    const gotSortedById = [...gotItems].sort((a, b) => String(a.id).localeCompare(String(b.id)));
    const wantSortedById = [...wantItems].sort((a, b) => String(a.id).localeCompare(String(b.id)));
    expect(gotSortedById).toEqual(wantSortedById);

    const orderedItems = orderedIds.map((id) => projected.get(id)!);
    for (const viewName of allNames()) {
      const filter = named(viewName, v.identity);
      expect(filter).not.toBeNull();
      const got = sortedIds(apply(orderedItems, filter!));
      const want = [...(v.expect.views[viewName] ?? [])].sort();
      expect(got).toEqual(want);
    }
    // Every view the fold produced must be one the vector actually asserts on
    // — an unasserted view is a silent coverage hole (mirrors the Go
    // suite's "view %q is not asserted by this vector" check).
    for (const viewName of allNames()) {
      expect(v.expect.views).toHaveProperty(viewName);
    }

    if (v.expect.label_views) {
      for (const [atom, want] of Object.entries(v.expect.label_views)) {
        const got = sortedIds(apply(orderedItems, labelFilter(atom)));
        expect(got).toEqual([...want].sort());
      }
    }
  });
});

// TestNegativeVectorsPresent's TS counterpart (done-condition 1's "including
// all negative vectors"): the fail-closed cases must exist and must actually
// differ from what the hostile input was trying to write, not just be named.
describe("fold.vectors.json negative-vector sanity", () => {
  const negativeNames = [
    "malformed_events_dropped",
    "forged_events_dropped",
    "untrusted_author_dropped",
    "dep_unresolvable_and_cross_board_dropped_silently",
    "board_pin_rejects_foreign_board_card",
    "confidential_wrong_cek_placeholder",
    "confidential_no_decryptor_placeholder",
    "fold_gate_quarantines_plaintext_and_malformed",
    // ready-ce8's grant-authority vectors. Present here as well as in the Go
    // suite because THIS implementation has its own copy of the escalation cap,
    // the role->level table, the revocation boundary and the §6.2 maintainer
    // fold (rolegrant.ts, fold.ts) — the Go list alone would not notice if the
    // TS test stopped seeing these cases.
    "grant_cap_only_owner_grants_maintainer",
    "grant_cap_contributor_may_not_delegate",
    "grant_cap_owner_is_irrevocable",
    "grant_cap_peer_maintainer_protected",
    "revoke_boundary_excludes_the_revoke_instant",
    "grant_level_two_confers_status_authority",
  ];
  it.each(negativeNames)("%s vector is present in the committed file", (name) => {
    expect(file.vectors.some((v) => v.name === name)).toBe(true);
  });

  // The TS counterpart of TestGrantAuthorityVectorsRunWithTheGatesEnabled: a
  // grant-authority vector folded with trusted:null or an empty pinned_board
  // asserts its expectation with the gate it exists to pin switched OFF (§3.4
  // disabled; §12/§3.5/§6.2 never derived) — green, and proving nothing. That is
  // the general form of the four holes ready-ce8 closes, so it is a contract.
  const grantAuthorityNames = [
    "grant_cap_only_owner_grants_maintainer",
    "grant_cap_contributor_may_not_delegate",
    "grant_cap_owner_is_irrevocable",
    "grant_cap_peer_maintainer_protected",
    "revoke_boundary_excludes_the_revoke_instant",
    "grant_level_two_confers_status_authority",
  ];
  it.each(grantAuthorityNames)("%s folds with the grant-authority gates ENABLED", (name) => {
    const v = file.vectors.find((x) => x.name === name);
    expect(v).toBeTruthy();
    expect(v!.options.trusted).not.toBeNull();
    expect(v!.options.pinned_board).not.toBe("");
  });
});
