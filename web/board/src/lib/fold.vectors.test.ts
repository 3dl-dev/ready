// THE VECTOR SUITE IS THE CONTRACT (ready-35b). This test consumes
// testdata/fold.vectors.json — the SAME committed file
// internal/foldvectors/vectors_test.go runs against rd's own Go fold — BYTE
// FOR BYTE, from its real repo path. It is not copied, not re-encoded, not a
// parallel fixture set: an edit to that file changes what this test asserts
// with zero changes here.
//
// FormatVersion is 3.
//
//   - v2: expect.items[].created_at/updated_at are DECIMAL STRINGS (arbitrary
//     int64 unix nanoseconds — JS Number cannot hold that range exactly, spec
//     §4.8), parsed as BigInt via state.ts's Item type; never Number() and never
//     a generic JSON number parser.
//   - v3 (ready-882): `options.keyring` / `expect.keyring`. A vector carrying
//     `options.keyring` does NOT hand the fold its key material — it hands it a
//     reader SECRET and expects this client to DERIVE the CEKs and the cutover
//     from the vector's own owner-signed kind-39301 grants (keyring.ts's
//     deriveBoardKeyring, spec §11.10-§11.14). That is the only shape under which
//     the epoch model is testable at all, and it is the shape this page runs in
//     production: main.ts derives the keyring from relay events before folding.

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
import { deriveBoardKeyring, type BoardKeyring } from "./keyring";
import { nip07KeyUnwrapper } from "./keyunwrap";
import { fakeNip44Signer } from "./fakesigner";
import { xOnlyPubkey } from "./schnorrsign";

interface VectorOptions {
  trusted: string[] | null;
  maintainers: string[] | null;
  pinned_board: string;
  decryptor: { keys: { board_coord: string; epoch: number; cek_hex: string }[] } | null;
  encrypted_boards: { boards: { board_coord: string; cutover: number }[] } | null;
  keyring: { reader_secret: string; board_author: string; board_d: string } | null;
}

interface KeyringFacts {
  board_coord: string;
  confidential: boolean;
  cutover: number;
  current_epoch: number;
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
    keyring?: KeyringFacts;
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

/**
 * deriveVectorKeyring runs THIS client's own grant -> key derivation over the
 * vector's events, for the reader the vector names (spec §11.10-§11.14).
 *
 * The reader is given as a SECRET because unwrapping is ECDH and the ECDH
 * binding is one of the four admission checks — a wrap addressed to someone else
 * must not open. It goes through the production seam (nip07KeyUnwrapper over a
 * NIP-44 v2 signer) rather than a shortcut, so the page's real string boundary is
 * on the path: sealing raw bytes instead of hex would fail these vectors exactly
 * as it fails in a browser.
 */
async function deriveVectorKeyring(
  spec: NonNullable<VectorOptions["keyring"]>,
  events: (NostrEvent | null)[],
): Promise<BoardKeyring> {
  return deriveBoardKeyring(
    events.filter((e): e is NostrEvent => e !== null),
    xOnlyPubkey(spec.reader_secret),
    spec.board_author,
    spec.board_d,
    nip07KeyUnwrapper(fakeNip44Signer(spec.reader_secret)),
  );
}

/**
 * keyringEncryptedBoards adapts a derived keyring to the fold's
 * EncryptedBoardSet using §11.13 ALONE: confidential iff a cutover was derived,
 * at that instant.
 *
 * main.ts deliberately does NOT wire it this way — it puts §11.13a's
 * omission-witness layer (confidentialityOf / encryptedBoardsOf) in front, which
 * can downgrade a board to "unknown" and quarantine strictly MORE. That layer is
 * a client-side hardening the Go fold does not implement (spec §11.13a records
 * this: "The Go reader does not yet apply §11.13a", tracked as ready-9a6), so
 * running it here would make this suite assert something the vector file's
 * expectations were not authored against. The vectors pin the SHARED contract;
 * the extra layer has its own tests (main.grantsomission.test.ts).
 */
function keyringEncryptedBoards(kr: BoardKeyring): EncryptedBoardSet {
  return {
    cutover(boardCoord: string) {
      const at = kr.cutover(boardCoord);
      return at === null ? { cutover: 0, ok: false } : { cutover: at, ok: true };
    },
  };
}

async function toProjectOptions(
  o: VectorOptions,
  events: (NostrEvent | null)[],
): Promise<{ opts: ProjectOptions; keyring: BoardKeyring | null }> {
  if (o.keyring) {
    // The derived form REPLACES both declarative fields; the vector file
    // forbids setting them together, and asserting that here keeps a client
    // from quietly preferring the easy one.
    expect(o.decryptor).toBeNull();
    expect(o.encrypted_boards).toBeNull();
    const kr = await deriveVectorKeyring(o.keyring, events);
    return {
      keyring: kr,
      opts: {
        trusted: o.trusted === null ? null : new Set(o.trusted),
        maintainers: o.maintainers && o.maintainers.length > 0 ? new Set(o.maintainers) : null,
        pinnedBoard: o.pinned_board,
        decryptor: kr,
        encryptedBoards: keyringEncryptedBoards(kr),
      },
    };
  }
  return {
    keyring: null,
    opts: {
      trusted: o.trusted === null ? null : new Set(o.trusted),
      maintainers: o.maintainers && o.maintainers.length > 0 ? new Set(o.maintainers) : null,
      pinnedBoard: o.pinned_board,
      decryptor: buildDecryptor(o.decryptor),
      encryptedBoards: buildEncryptedBoards(o.encrypted_boards),
    },
  };
}

function sortedIds(items: Item[]): string[] {
  return items.map((i) => i.id).sort();
}

const file = vectorFileJSON as unknown as VectorFile;

describe("fold.vectors.json conformance", () => {
  it(`loads at least 30 vectors (format version ${file.version})`, () => {
    expect(file.version).toBe(3);
    expect(file.vectors.length).toBeGreaterThanOrEqual(30);
  });

  // Reported explicitly per the item's done condition: the vector count must
  // equal the Go suite's count (both read the same file, so this is really a
  // sanity check that nothing truncated the read).
  console.log(
    `fold conformance: ${file.vectors.length} vectors from testdata/fold.vectors.json (spec ${file.spec})`,
  );

  it.each(file.vectors)("$name", async (v: Vector) => {
    const { opts, keyring } = await toProjectOptions(v.options, v.events);
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

    // The DERIVED-KEYRING expectation (spec §11.10-§11.14), asserted in both
    // directions: a vector that declares one must match it, and a vector that
    // derives a keyring must assert it — a derivation nobody looks at is not
    // coverage. current_epoch is the one fact with no item-level consequence:
    // it is the epoch a WRITE seals under, so this is where a read-only client
    // gets held to it.
    if (v.expect.keyring) {
      expect(keyring).not.toBeNull();
      const coord = v.expect.keyring.board_coord;
      const cutover = keyring!.cutover(coord);
      expect(cutover !== null).toBe(v.expect.keyring.confidential);
      expect(cutover ?? 0).toBe(v.expect.keyring.cutover);
      expect(keyring!.currentEpoch(coord) ?? 0).toBe(v.expect.keyring.current_epoch);
    } else {
      expect(keyring).toBeNull();
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

  // ready-882's two subsystems. Present here as well as in the Go suite for the
  // same reason the grant-authority list is: THIS implementation has its own copy
  // of the gate projection (fold.ts) and its own grant -> key derivation
  // (keyring.ts), so a vector dropped from the committed file would silently stop
  // exercising the browser's version of §9.1-§9.3 and §11.10-§11.14.
  const gateLifecycleNames = [
    "gate_open_projects_a_resolvable_gate",
    "gate_approve_clears_every_gate_field",
    "gate_approve_under_blocking_clears_the_gate_not_the_block",
    "gate_reject_keeps_the_gate_open",
  ];
  it.each(gateLifecycleNames)("%s gate-transition vector is present", (name) => {
    expect(file.vectors.some((v) => v.name === name)).toBe(true);
  });

  const keyringNames = [
    "keyring_epoch_zero_grant_yields_no_key_and_no_cutover",
    "keyring_retains_every_epoch_across_a_rotation",
    "keyring_cutover_is_the_earliest_owner_grant_whoever_it_names",
  ];
  // The epoch-model counterpart of the "gates ENABLED" check above, and the same
  // failure it guards against: rewrite one of these vectors into the declarative
  // key shape (options.decryptor + options.encrypted_boards) and it stays GREEN
  // while the derivation it exists to pin — which epochs are retained, when the
  // board went confidential, which epoch a write seals under — no longer runs at
  // all. So the derived shape is part of the contract, not the fixture author's
  // choice.
  it.each(keyringNames)("%s DERIVES its key material rather than declaring it", (name) => {
    const v = file.vectors.find((x) => x.name === name);
    expect(v).toBeTruthy();
    expect(v!.options.keyring).not.toBeNull();
    expect(v!.options.decryptor).toBeNull();
    expect(v!.options.encrypted_boards).toBeNull();
    expect(v!.expect.keyring).toBeTruthy();
    // The grants the derivation consumes have to be in the vector's own log.
    expect(v!.events.some((e) => e !== null && e.kind === 39301)).toBe(true);
  });
});
