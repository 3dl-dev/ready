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
import { apply as boardApply, gatesFilter as boardGatesFilter } from "../board/views";
import { toUIItem } from "./itemsource";
import { hexToBytes } from "./sha256";
import { deriveBoardKeyring, type BoardKeyring } from "./keyring";
// The PRODUCTION confidentiality wiring, imported — not reimplemented. See
// productionEncryptedBoards.
import { confidentialityOf, encryptedBoardsOf } from "./confidentiality";
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
/** keyringCoord is the board coordinate a keyring spec names (§4.1 addressable). */
function keyringCoord(spec: NonNullable<VectorOptions["keyring"]>): string {
  return `30301:${spec.board_author}:${spec.board_d}`;
}

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
 * plainEncryptedBoards adapts a derived keyring to the fold's EncryptedBoardSet
 * using §11.13 ALONE: confidential iff a cutover was derived, at that instant.
 * This is what rd's Go reader does — spec §11.13a records that "the Go reader
 * does not yet apply §11.13a" (tracked as ready-9a6).
 *
 * IT IS NOT WHAT THIS SUITE FOLDS WITH. The production wiring is
 * confidentialityOf + encryptedBoardsOf (lib/confidentiality.ts), and this
 * function exists only so the divergence-zone test below can compare the two.
 * Round 1 of ready-882 folded with a local copy of THIS adapter and justified it
 * as equivalent to production; it is not, and the false claim shipped a vector the
 * deployed browser could not satisfy (see the divergence-zone test).
 */
function plainEncryptedBoards(kr: BoardKeyring): EncryptedBoardSet {
  return {
    cutover(boardCoord: string) {
      const at = kr.cutover(boardCoord);
      return at === null ? { cutover: 0, ok: false } : { cutover: at, ok: true };
    },
  };
}

/**
 * productionEncryptedBoards is the REAL browser wiring, imported rather than
 * reimplemented: main.ts calls exactly these two functions on exactly these
 * arguments (`hasLinkKeys` is false because a vector carries no fragment keys).
 *
 * Substituting anything here reopens the hole this rework closes: a vector can
 * then assert fold behaviour the shipped page does not produce, which is the drift
 * the epic's spec -> vectors -> client ordering exists to prevent — introduced by
 * a vector.
 */
function productionEncryptedBoards(
  kr: BoardKeyring,
  coord: string,
  events: (NostrEvent | null)[],
): EncryptedBoardSet {
  const verified = events.filter((e): e is NostrEvent => e !== null);
  const { state } = confidentialityOf(kr, coord, verified, false);
  return encryptedBoardsOf(kr, state);
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
    // The board the §11.13a layer judges is the one the KEYRING SPEC names,
    // exactly as main.ts judges the one board it is loading. (A keyring vector
    // need not also pin that board — §3.4 pinning is an independent gate — but if
    // it does, the two must be the same board.)
    const coord = keyringCoord(o.keyring);
    if (o.pinned_board !== "") expect(o.pinned_board).toBe(coord);
    return {
      keyring: kr,
      opts: {
        trusted: o.trusted === null ? null : new Set(o.trusted),
        maintainers: o.maintainers && o.maintainers.length > 0 ? new Set(o.maintainers) : null,
        pinnedBoard: o.pinned_board,
        decryptor: kr,
        encryptedBoards: productionEncryptedBoards(kr, coord, events),
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
    // ready-e51 (round 3): the SECOND port of the gates predicate — board/views.ts,
    // the one the gate RAIL and the detail pane's ruling banner both read — held
    // to the SAME Go-generated id set as lib/views.ts above.
    //
    // I filed this gap (ready-86b) with the reason "different Item shape, needs
    // an adapter", and then did not try it. The adapter is PRODUCTION CODE:
    // itemsource.toUIItem is what main.ts's fold already maps every projected
    // item through before the UI sees it, so this asserts the rail's membership
    // over exactly the objects the rail is handed at runtime. Before this, the
    // only thing holding that port to the Go authority was a hand-written 9-row
    // TS-to-TS agreement table in board/views.test.ts — and this predicate has
    // already drifted once (ready-e0e), in this exact port.
    expect(sortedIds(boardApply(orderedItems.map((i) => toUIItem(i)), boardGatesFilter()) as unknown as Item[])).toEqual(
      [...(v.expect.views["gates"] ?? [])].sort(),
    );

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
    "keyring_epoch_zero_grant_yields_no_key",
    "keyring_epoch_zero_grant_yields_no_cutover",
    "keyring_retains_every_epoch_across_a_rotation",
    "keyring_cutover_is_the_earliest_owner_grant_whoever_it_names",
    // ready-475's owner-signed cutover assertion (`confidential_since` on the
    // board's own kind-30301). Listed here for the same reason as the four
    // above: THIS implementation has its own copy of the rule (keyring.ts's
    // assertedConfidentialSince + confidentiality.ts), so a vector dropped from
    // the committed file would silently stop exercising it — and the divergence
    // check below is what keeps the assertion OUT of the zone where the two
    // readers legitimately disagree.
    "keyring_confidential_since_establishes_the_cutover",
    "keyring_confidential_since_never_moves_the_cutover_later",
    "keyring_confidential_since_foreign_signer_is_ignored",
    "keyring_confidential_since_absent_is_todays_behaviour",
    // The zero-grant case, and the only one of the five whose PRESENCE in this
    // list is itself the statement: "a sealed card plus no derived cutover" is
    // the shape the divergence check below was written about, and an
    // owner-signed assertion is what pulls it out of the zone. Drop the
    // assertion from that vector and this same check goes red.
    "keyring_confidential_since_with_no_grants_establishes_the_instant_not_read_access",
  ];
  // The epoch-model counterpart of the "gates ENABLED" check above, and the same
  // failure it guards against: rewrite one of these vectors into the declarative
  // key shape (options.decryptor + options.encrypted_boards) and it stays GREEN
  // while the derivation it exists to pin — which epochs are retained, when the
  // board went confidential, which epoch a write seals under — no longer runs at
  // all. So the derived shape is part of the contract, not the fixture author's
  // choice.
  // THE DIVERGENCE ZONE, PINNED (ready-882 rework). Two conformant readers derive
  // the fold's quarantine gate DIFFERENTLY from the same grants:
  //
  //   plain §11.13   — confidential iff a cutover was derived. rd's Go fold
  //                    (pkg/sync/keydist.go), and §11.13a says so outright: "The
  //                    Go reader does not yet apply §11.13a" (ready-9a6).
  //   §11.13a on top — lib/confidentiality.ts. A derived cutover is a LOWER
  //                    BOUND, so the state is three-valued and "unknown"
  //                    quarantines strictly more: gate ON, cutover 0.
  //
  // A vector is the SHARED contract, so its expectations must not depend on which
  // one a reader runs — a vector inside the zone where they disagree is
  // unsatisfiable by one implementation BY CONSTRUCTION. Round 1 shipped exactly
  // that: keyring_epoch_zero_grant_yields_no_key_and_no_cutover asserted a
  // plaintext card folding in clear, which is plain §11.13, while the deployed
  // browser saw a verified sealed card with no derived cutover, went "unknown", and
  // quarantined it. Neither implementation was wrong — the FIXTURE was, and the
  // suite could not see it because it folded with a local copy of the plain adapter
  // instead of production's.
  //
  // The zone is now empty and this is what keeps it empty. It is a real check and
  // not a restatement of the run above: the vectors below fold with production's
  // adapter, so a future vector that lands in the zone fails HERE, naming §11.13a,
  // instead of failing as an unexplained item mismatch — or worse, passing this
  // suite while breaking the Go one.
  // WHAT IS COMPARED IS THE PROJECTION, NOT THE TWO ADAPTERS' ANSWERS. The
  // adapters are ALLOWED to differ — §11.13a exists in order to differ — and on
  // keyring_epoch_zero_grant_yields_no_key they do: the board has a verified sealed
  // card and no derived cutover, so production says "unknown" (gate ON, cutover 0)
  // where plain §11.13 says "plaintext" (gate off). What must not differ is
  // anything the VECTOR ASSERTS, i.e. the projected items — a well-formed sealed
  // envelope is never quarantined either way, so that vector's expectation is
  // satisfiable by both readers while the epoch-0 CEK rejection it pins stays
  // falsifiable. Views are a pure function of the items, so equal items suffice.
  it.each(keyringNames)("%s projects identically under §11.13 and under §11.13a", async (name) => {
    const v = file.vectors.find((x) => x.name === name);
    expect(v).toBeTruthy();
    const spec = v!.options.keyring;
    expect(spec).not.toBeNull();
    const kr = await deriveVectorKeyring(spec!, v!.events);
    const base = {
      trusted: v!.options.trusted === null ? null : new Set(v!.options.trusted),
      maintainers:
        v!.options.maintainers && v!.options.maintainers.length > 0
          ? new Set(v!.options.maintainers)
          : null,
      pinnedBoard: v!.options.pinned_board,
      decryptor: kr,
    };
    const underPlain = projectItems(v!.events, {
      ...base,
      encryptedBoards: plainEncryptedBoards(kr),
    });
    const underHardened = projectItems(v!.events, {
      ...base,
      encryptedBoards: productionEncryptedBoards(kr, keyringCoord(spec!), v!.events),
    });
    const encode = (m: Map<string, Item>) =>
      Array.from(m.keys())
        .sort()
        .map((id) => encodeItem(m.get(id)!));
    expect(
      encode(underHardened),
      `${name} projects differently under §11.13a than under plain §11.13, so its expectation ` +
        `cannot be satisfied by both rd (plain, ready-9a6) and the browser (hardened) — the vector ` +
        `is inside the divergence zone and must be reshaped, not re-expected`,
    ).toEqual(encode(underPlain));
  });

  it.each(keyringNames)("%s DERIVES its key material rather than declaring it", (name) => {
    const v = file.vectors.find((x) => x.name === name);
    expect(v).toBeTruthy();
    expect(v!.options.keyring).not.toBeNull();
    expect(v!.options.decryptor).toBeNull();
    expect(v!.options.encrypted_boards).toBeNull();
    expect(v!.expect.keyring).toBeTruthy();
    // The events the derivation CONSUMES have to be in the vector's own log —
    // the point being that the keyring is built from signed events, never
    // declared. Two kinds qualify and both are checked, rather than 39301 alone:
    // a keyring is derived from owner CEK grants (kind 39301) and, since
    // ready-475, ALSO from an owner-signed `confidential_since` on the board's
    // own kind-30301 definition. The zero-grant assertion vector carries only the
    // second — an assertion standing on its own with no grant anywhere IS its
    // subject — so demanding a 39301 outright would make the one case that pins
    // that unexpressible, which is a fixture limitation and not a contract.
    const derives = v!.events.filter(
      (e) =>
        e !== null &&
        (e.kind === 39301 ||
          (e.kind === 30301 &&
            (e.tags ?? []).some((t) => t[0] === "confidential_since" && t[1] !== ""))),
    );
    expect(derives.length).toBeGreaterThan(0);
  });
});
