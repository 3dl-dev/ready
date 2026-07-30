// The signer is TEST SCAFFOLDING (see schnorrsign.ts's header). These tests
// prove two things: (1) it produces signatures the SHIPPED verifier — the same
// schnorrVerify that gates every event the board reads off a relay — accepts,
// against the official BIP-340 signing vectors; and (2) the shipped entrypoint
// cannot reach it, so no signing primitive lands in the bundle.
import { readFileSync } from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import { schnorrVerify } from "./secp256k1";
import { verifyEvent } from "./nostrevent";
import { bytesToHex, hexToBytes, schnorrSign, signNostrEvent, xOnlyPubkey } from "./schnorrsign";

// bitcoin/bips bip-0340/test-vectors.csv, rows 0-3: the four rows that carry a
// secret key and an aux value, i.e. the SIGNING vectors (the remaining rows are
// verification-only and are already covered by secp256k1.test.ts).
const BIP340_SIGNING_VECTORS = [
  {
    index: 0,
    secret: "0000000000000000000000000000000000000000000000000000000000000003",
    pubkey: "F9308A019258C31049344F85F89D5229B531C845836F99B08601F113BCE036F9",
    aux: "0000000000000000000000000000000000000000000000000000000000000000",
    msg: "0000000000000000000000000000000000000000000000000000000000000000",
    sig:
      "E907831F80848D1069A5371B402410364BDF1C5F8307B0084C55F1CE2DCA821525F66A4A85EA8B71E482A74F382D2CE5EBEEE8FDB2172F477DF4900D310536C0",
  },
  {
    index: 1,
    secret: "B7E151628AED2A6ABF7158809CF4F3C762E7160F38B4DA56A784D9045190CFEF",
    pubkey: "DFF1D77F2A671C5F36183726DB2341BE58FEAE1DA2DECED843240F7B502BA659",
    aux: "0000000000000000000000000000000000000000000000000000000000000001",
    msg: "243F6A8885A308D313198A2E03707344A4093822299F31D0082EFA98EC4E6C89",
    sig:
      "6896BD60EEAE296DB48A229FF71DFE071BDE413E6D43F917DC8DCF8C78DE33418906D11AC976ABCCB20B091292BFF4EA897EFCB639EA871CFA95F6DE339E4B0A",
  },
  {
    index: 2,
    secret: "C90FDAA22168C234C4C6628B80DC1CD129024E088A67CC74020BBEA63B14E5C9",
    pubkey: "DD308AFEC5777E13121FA72B9CC1B7CC0139715309B086C960E18FD969774EB8",
    aux: "C87AA53824B4D7AE2EB035A2B5BBBCCC080E76CDC6D1692C4B0B62D798E6D906",
    msg: "7E2D58D8B3BCDF1ABADEC7829054F90DDA9805AAB56C77333024B9D0A508B75C",
    sig:
      "5831AAEED7B44BB74E5EAB94BA9D4294C49BCF2A60728D8B4C200F50DD313C1BAB745879A5AD954A72C45A91C3A51D3C7ADEA98D82F8481E0E1E03674A6F3FB7",
  },
  {
    index: 3,
    secret: "0B432B2677937381AEF05BB02A66ECD012773062CF3FA2549E44F58ED2401710",
    pubkey: "25D1DFF95105F5253C4022F628A996AD3A0D95FBF21D468A1B33F8C160D8F517",
    aux: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
    msg: "FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF",
    sig:
      "7EB0509757E246F19449885651611CB965ECC1A187DD51B64FDA1EDC9637D5EC97582B9CB13DB3933705B32BA982AF5AF25FD78881EBB32771FC5922EFC66EA3",
  },
];

describe("schnorrSign (BIP-340 default signing)", () => {
  for (const v of BIP340_SIGNING_VECTORS) {
    it(`official vector ${v.index} reproduces the exact signature`, () => {
      expect(xOnlyPubkey(v.secret).toUpperCase()).toBe(v.pubkey);
      const sig = schnorrSign(hexToBytes(v.msg.toLowerCase()), v.secret.toLowerCase(), hexToBytes(v.aux.toLowerCase()));
      expect(bytesToHex(sig).toUpperCase()).toBe(v.sig);
    });

    it(`official vector ${v.index} verifies under the SHIPPED verifier`, () => {
      const sig = schnorrSign(hexToBytes(v.msg.toLowerCase()), v.secret.toLowerCase(), hexToBytes(v.aux.toLowerCase()));
      expect(schnorrVerify(hexToBytes(v.pubkey.toLowerCase()), hexToBytes(v.msg.toLowerCase()), sig)).toBe(true);
    });
  }

  it("a signed nostr event passes verifyEvent (id + signature)", () => {
    const secret = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
    const e = signNostrEvent(
      { created_at: 1780000000, kind: 30302, tags: [["d", "ready-b2b"]], content: "hello" },
      secret,
    );
    expect(verifyEvent(e)).toBe(true);
    expect(e.pubkey).toBe(xOnlyPubkey(secret));
  });

  it("a tampered tag breaks verification", () => {
    const secret = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
    const e = signNostrEvent(
      { created_at: 1780000000, kind: 30302, tags: [["d", "ready-b2b"]], content: "hello" },
      secret,
    );
    expect(verifyEvent({ ...e, tags: [["d", "ready-evil"]] })).toBe(false);
  });
});

describe("the shipped bundle cannot reach a signing primitive", () => {
  // Static reachability: walk main.ts's import graph and assert schnorrsign.ts
  // never appears. The board must never contain code that can sign with a key
  // the page holds — that is the whole NIP-07 security model.
  it("main.ts's import graph excludes schnorrsign.ts", () => {
    const srcDir = path.resolve(import.meta.dirname, "..");
    const seen = new Set<string>();
    const queue = [path.join(srcDir, "main.ts")];
    while (queue.length > 0) {
      const file = queue.pop()!;
      if (seen.has(file)) continue;
      seen.add(file);
      let text: string;
      try {
        text = readFileSync(file, "utf8");
      } catch {
        continue;
      }
      for (const m of text.matchAll(/from\s+"(\.[^"]+)"/g)) {
        const rel = m[1];
        const base = path.resolve(path.dirname(file), rel);
        const cand = base.endsWith(".ts") ? base : `${base}.ts`;
        queue.push(cand);
      }
    }
    const reached = [...seen].filter((f) => f.endsWith("schnorrsign.ts"));
    expect(reached).toEqual([]);
  });
});
