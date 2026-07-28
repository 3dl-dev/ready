// Grant -> CEK derivation (ready-c4b), driven end to end over Go-signed
// kind-39301 grants with real NIP-44 v2 wraps.
//
// The reader here goes through the SAME path production does: the page holds no
// secret, calls the signer's nip44.decrypt, gets a STRING back, and decodes it
// (keyunwrap.ts). The signer is fakesigner.ts over nip44ref.ts, which is pinned
// to the official NIP-44 v2 vectors — so nothing in this file is a
// self-consistency check.
//
// Each of the four admission checks in keyring.ts has its own case below, and
// each case is a real attack rather than a shape: a forged grant, a grant signed
// by a non-owner carrying its own key, a wrap retargeted into someone else's
// grant, and a revoked member reaching for an epoch it was never given.

import { describe, expect, it } from "vitest";
import { applyFragmentKeys, deriveBoardKeyring, parseRoleGrant, KIND_ROLE_GRANT } from "./keyring";
import { nip07KeyUnwrapper, neverUnwraps, decodeWrappedKey } from "./keyunwrap";
import { fakeNip44Signer } from "./fakesigner";
import { bytesToHex, hexToBytes } from "./sha256";
import {
  BOARD_COORD,
  BOARD_D,
  CEK_EPOCH1,
  CEK_EPOCH2,
  CUTOVER,
  LTK,
  MEMBER_PUB,
  MEMBER_SEC,
  OWNER_PUB,
  OWNER_SEC,
  REVOKED_PUB,
  REVOKED_SEC,
  STRANGER_PUB,
  STRANGER_SEC,
  grants,
} from "./confidential.fixtures";

function unwrapperFor(secretHex: string) {
  return nip07KeyUnwrapper(fakeNip44Signer(secretHex));
}

const derive = (readerPub: string, readerSec: string) =>
  deriveBoardKeyring(grants, readerPub, OWNER_PUB, BOARD_D, unwrapperFor(readerSec));

describe("deriveBoardKeyring over Go-signed grants", () => {
  it("gives the OWNER both epochs and the LTK", async () => {
    const kr = await derive(OWNER_PUB, OWNER_SEC);
    expect(kr.epochs(BOARD_COORD)).toEqual([1, 2]);
    expect(bytesToHex(kr.cek(BOARD_COORD, 1)!)).toBe(CEK_EPOCH1);
    expect(bytesToHex(kr.cek(BOARD_COORD, 2)!)).toBe(CEK_EPOCH2);
    expect(bytesToHex(kr.ltk(BOARD_COORD)!)).toBe(LTK);
  });

  it("gives a REMAINING member both epochs — a rotation does not cost it history", async () => {
    const kr = await derive(MEMBER_PUB, MEMBER_SEC);
    expect(kr.epochs(BOARD_COORD)).toEqual([1, 2]);
    expect(bytesToHex(kr.cek(BOARD_COORD, 1)!)).toBe(CEK_EPOCH1);
    expect(bytesToHex(kr.cek(BOARD_COORD, 2)!)).toBe(CEK_EPOCH2);
  });

  it("gives a REVOKED member epoch 1 and NOTHING after it (forward secrecy)", async () => {
    // The revoked key keeps what it already held — historical reads survive,
    // an accepted limit of the design. What it never receives is a wrap for the
    // epoch minted after its revocation, so it cannot follow the board forward.
    const kr = await derive(REVOKED_PUB, REVOKED_SEC);
    expect(kr.epochs(BOARD_COORD)).toEqual([1]);
    expect(kr.cek(BOARD_COORD, 2)).toBeNull();
  });

  it("gives a STRANGER nothing at all", async () => {
    const kr = await derive(STRANGER_PUB, STRANGER_SEC);
    expect(kr.epochs(BOARD_COORD)).toEqual([]);
    expect(kr.cek(BOARD_COORD, 1)).toBeNull();
    expect(kr.ltk(BOARD_COORD)).toBeNull();
  });

  it("still tells a stranger the board is CONFIDENTIAL", async () => {
    // The cutover is tracked from every owner CEK grant, not just the ones
    // addressed to the reader. Without that, a reader holding no key would
    // think the board was plaintext and would happily render post-cutover
    // cleartext a rogue client published.
    const kr = await derive(STRANGER_PUB, STRANGER_SEC);
    expect(kr.cutover(BOARD_COORD)).toBe(CUTOVER);
  });

  it("a read-only identity (no signer) holds no keys but still sees the cutover", async () => {
    const kr = await deriveBoardKeyring(grants, MEMBER_PUB, OWNER_PUB, BOARD_D, neverUnwraps);
    expect(kr.epochs(BOARD_COORD)).toEqual([]);
    expect(kr.cutover(BOARD_COORD)).toBe(CUTOVER);
  });
});

describe("admission checks — each one is load-bearing", () => {
  it("CHECK 1: a FORGED grant mints nothing", async () => {
    const memberGrant = grants.find((g) => g.tags.some((t) => t[0] === "p" && t[1] === MEMBER_PUB))!;
    const forged = { ...memberGrant, sig: "00" + memberGrant.sig.slice(2) };
    const kr = await deriveBoardKeyring([forged], MEMBER_PUB, OWNER_PUB, BOARD_D, unwrapperFor(MEMBER_SEC));
    expect(kr.epochs(BOARD_COORD)).toEqual([]);
    // ...and it does not even set the cutover, so a forged grant cannot flip a
    // plaintext board into confidential mode and blank out every card.
    expect(kr.cutover(BOARD_COORD)).toBeNull();
  });

  it("CHECK 2: a grant signed by a NON-OWNER carrying its own key is ignored", async () => {
    // The stranger self-signed a perfectly valid grant to itself with a CEK it
    // minted. If non-owner grants counted, any pubkey could introduce a board
    // key and author cards the board would then render as authentic.
    const kr = await derive(STRANGER_PUB, STRANGER_SEC);
    expect(kr.epochs(BOARD_COORD)).toEqual([]);
  });

  it("CHECK 3: a grant addressed to SOMEONE ELSE is not even offered to the signer", async () => {
    const asked: string[] = [];
    const signer = fakeNip44Signer(STRANGER_SEC, { onDecrypt: (_, payload) => asked.push(payload) });
    const kr = await deriveBoardKeyring(grants, STRANGER_PUB, OWNER_PUB, BOARD_D, nip07KeyUnwrapper(signer));
    expect(kr.epochs(BOARD_COORD)).toEqual([]);
    // A hostile relay must not be able to make the page fire an extension
    // prompt per grant it serves.
    expect(asked).toEqual([]);
  });

  it("CHECK 4: a wrap RETARGETED into a grant p-tagged to the reader does not open", async () => {
    // Lift the epoch-2 CEK wrap that was sealed to the MEMBER and drop it into
    // the revoked key's own (genuine, owner-signed) grant. The p tag now names
    // the revoked key, so check 3 passes — but the wrap is ECDH-bound to the
    // member, so it will not open. This is the anti-retarget guard, and it is
    // the reason NIP-44's missing AAD is not a hole here.
    const memberE2 = grants.find(
      (g) => g.tags.some((t) => t[0] === "p" && t[1] === MEMBER_PUB) && g.tags.some((t) => t[0] === "cek_epoch" && t[1] === "2"),
    )!;
    const revokedE1 = grants.find(
      (g) => g.tags.some((t) => t[0] === "p" && t[1] === REVOKED_PUB) && g.tags.some((t) => t[0] === "cek"),
    )!;
    const stolenCEK = memberE2.tags.find((t) => t[0] === "cek")![1];
    const retargeted = {
      ...revokedE1,
      tags: revokedE1.tags.map((t) => (t[0] === "cek" ? ["cek", stolenCEK] : t[0] === "cek_epoch" ? ["cek_epoch", "2"] : t)),
    };
    // NOTE: retargeting also breaks the signature (the id no longer derives),
    // so check 1 rejects it first. Re-signing needs the owner's key, which is
    // the point. Prove the ECDH binding independently by handing the unwrapper
    // the stolen payload directly.
    const kr = await deriveBoardKeyring([retargeted], REVOKED_PUB, OWNER_PUB, BOARD_D, unwrapperFor(REVOKED_SEC));
    expect(kr.epochs(BOARD_COORD)).toEqual([]);
    expect(await unwrapperFor(REVOKED_SEC)(OWNER_PUB, stolenCEK)).toBeNull();
    expect(await unwrapperFor(STRANGER_SEC)(OWNER_PUB, stolenCEK)).toBeNull();
  });

  it("a grant for a DIFFERENT board contributes nothing to this one", async () => {
    const memberGrant = grants.find((g) => g.tags.some((t) => t[0] === "p" && t[1] === MEMBER_PUB))!;
    const kr = await deriveBoardKeyring([memberGrant], MEMBER_PUB, OWNER_PUB, "someotherboard", unwrapperFor(MEMBER_SEC));
    expect(kr.epochs(`30301:${OWNER_PUB}:someotherboard`)).toEqual([]);
  });

  it("a signer that REJECTS every prompt yields an empty keyring, not a crash", async () => {
    const signer = fakeNip44Signer(MEMBER_SEC, { alwaysReject: true });
    const kr = await deriveBoardKeyring(grants, MEMBER_PUB, OWNER_PUB, BOARD_D, nip07KeyUnwrapper(signer));
    expect(kr.epochs(BOARD_COORD)).toEqual([]);
  });
});

describe("parseRoleGrant", () => {
  it("rejects a bogus cek_epoch rather than binding a key to epoch 0", async () => {
    const memberGrant = grants.find((g) => g.tags.some((t) => t[0] === "p" && t[1] === MEMBER_PUB))!;
    const bogus = {
      ...memberGrant,
      tags: memberGrant.tags.map((t) => (t[0] === "cek_epoch" ? ["cek_epoch", "not-a-number"] : t)),
    };
    expect(parseRoleGrant(bogus)?.cekEpoch).toBe(0);
    // ...and derivation drops it entirely — including its cutover contribution.
    const kr = await deriveBoardKeyring([bogus], MEMBER_PUB, OWNER_PUB, BOARD_D, unwrapperFor(MEMBER_SEC));
    expect(kr.cutover(BOARD_COORD)).toBeNull();
  });

  it("rejects a wrong kind, an unknown role and a malformed board coordinate", () => {
    const g = grants[0];
    expect(parseRoleGrant({ ...g, kind: 30302 })).toBeNull();
    expect(parseRoleGrant({ ...g, tags: g.tags.map((t) => (t[0] === "role" ? ["role", "admin"] : t)) })).toBeNull();
    expect(parseRoleGrant({ ...g, tags: g.tags.map((t) => (t[0] === "a" ? ["a", "1:2"] : t)) })).toBeNull();
    expect(parseRoleGrant({ ...g, tags: g.tags.filter((t) => t[0] !== "p") })).toBeNull();
  });

  it("reads the grant kind constant the Go side uses", () => {
    expect(KIND_ROLE_GRANT).toBe(39301);
    expect(grants.every((g) => g.kind === KIND_ROLE_GRANT)).toBe(true);
  });
});

describe("decodeWrappedKey — the string boundary NIP-07 forces", () => {
  it("accepts the 64-hex payload pkg/sync/keydist.go's WrapKey seals", () => {
    expect(bytesToHex(decodeWrappedKey(CEK_EPOCH1)!)).toBe(CEK_EPOCH1);
    expect(bytesToHex(decodeWrappedKey(CEK_EPOCH1.toUpperCase())!)).toBe(CEK_EPOCH1);
  });

  it("REFUSES to guess at anything else, including a 32-character string", async () => {
    // A raw 32-byte key that has been through an extension's TextDecoder is
    // unrecoverable. Guessing 32 bytes out of the mangled string would trade a
    // visible placeholder for a silent mis-decrypt, which is strictly worse.
    for (const bad of ["", "deadbeef", "x".repeat(64), "y".repeat(32), CEK_EPOCH1 + "00"]) {
      expect(decodeWrappedKey(bad), JSON.stringify(bad)).toBeNull();
    }
  });

  it("demonstrates the corruption the hex encoding exists to prevent", async () => {
    // Seal RAW bytes (the pre-ready-c4b Go behaviour) and push them through the
    // same string boundary a real extension has. The bytes do not survive.
    const { seal } = await import("./nip44ref");
    const rawWrap = seal(OWNER_SEC, MEMBER_PUB, hexToBytes(CEK_EPOCH1));
    const returned = await fakeNip44Signer(MEMBER_SEC).decrypt(OWNER_PUB, rawWrap);
    const recovered = new TextEncoder().encode(returned);
    expect(recovered.length === 32 && bytesToHex(recovered) === CEK_EPOCH1).toBe(false);
    // And the adapter fails closed on it rather than handing back 32 wrong bytes.
    expect(decodeWrappedKey(returned)).toBeNull();
  });
});

// ready-df0. applyFragmentKeys is the ONLY way key material enters a keyring
// without passing this module's four relay-facing admission checks, so its exact
// blast radius is the thing to pin: which coordinate it may touch, and what it
// is forbidden to touch at all.
describe("applyFragmentKeys — the `rd board --with-key` seam", () => {
  const OTHER_COORD = `30301:${OWNER_PUB}:otherboard`;

  /** A reader holding NOTHING for this board: no signer, so not one of the four
   * checks that could mint a key has anything to mint from. */
  const emptyKeyring = () => deriveBoardKeyring(grants, MEMBER_PUB, OWNER_PUB, BOARD_D, neverUnwraps);

  it("adds every supplied epoch to the NAMED coordinate", async () => {
    const kr = await emptyKeyring();
    expect(kr.cek(BOARD_COORD, 1)).toBeNull();

    applyFragmentKeys(kr, BOARD_COORD, {
      ceks: [
        { epoch: 1, key: hexToBytes(CEK_EPOCH1) },
        { epoch: 2, key: hexToBytes(CEK_EPOCH2) },
      ],
      ltk: hexToBytes(LTK),
    });

    expect(kr.epochs(BOARD_COORD)).toEqual([1, 2]);
    expect(bytesToHex(kr.cek(BOARD_COORD, 1)!)).toBe(CEK_EPOCH1);
    expect(bytesToHex(kr.cek(BOARD_COORD, 2)!)).toBe(CEK_EPOCH2);
    expect(bytesToHex(kr.ltk(BOARD_COORD)!)).toBe(LTK);
  });

  it("SCOPING: touches no other coordinate — a link's key can never spill onto another board", async () => {
    const kr = await emptyKeyring();
    applyFragmentKeys(kr, BOARD_COORD, {
      ceks: [{ epoch: 1, key: hexToBytes(CEK_EPOCH1) }],
      ltk: hexToBytes(LTK),
    });

    expect(kr.cek(OTHER_COORD, 1)).toBeNull();
    expect(kr.ltk(OTHER_COORD)).toBeNull();
    expect(kr.epochs(OTHER_COORD)).toEqual([]);
  });

  it("cannot declare a board confidential, and cannot un-declare one", async () => {
    // cutover is the fold gate's input: it decides whether post-cutover
    // cleartext is quarantined. It must stay a function of owner-signed grants
    // ALONE, or a link could switch that gate off.
    const fresh = await deriveBoardKeyring([], MEMBER_PUB, OWNER_PUB, BOARD_D, neverUnwraps);
    applyFragmentKeys(fresh, BOARD_COORD, { ceks: [{ epoch: 1, key: hexToBytes(CEK_EPOCH1) }] });
    expect(fresh.cutover(BOARD_COORD)).toBeNull();

    const derived = await emptyKeyring();
    expect(derived.cutover(BOARD_COORD)).toBe(CUTOVER);
    applyFragmentKeys(derived, BOARD_COORD, { ceks: [{ epoch: 1, key: hexToBytes(CEK_EPOCH1) }] });
    expect(derived.cutover(BOARD_COORD)).toBe(CUTOVER);
  });

  it("drops a key of the wrong size and a non-positive epoch rather than binding them", async () => {
    const kr = await emptyKeyring();
    applyFragmentKeys(kr, BOARD_COORD, {
      ceks: [
        { epoch: 1, key: new Uint8Array(16) },
        { epoch: 0, key: hexToBytes(CEK_EPOCH1) },
        { epoch: -3, key: hexToBytes(CEK_EPOCH1) },
      ],
      ltk: new Uint8Array(31),
    });
    expect(kr.epochs(BOARD_COORD)).toEqual([]);
    expect(kr.ltk(BOARD_COORD)).toBeNull();
  });
});
