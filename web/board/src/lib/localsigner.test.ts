// @vitest-environment node
import { describe, it, expect } from "vitest";
import { localSigner } from "./localsigner";
import { xOnlyPubkey } from "./schnorrsign";
import { verifyEvent, computeEventId } from "./nostrevent";

// A fixed, valid secp256k1 secret (1 < k < n) — deterministic across runs.
const SECRET = "0000000000000000000000000000000000000000000000000000000000000042";

describe("localSigner — extension-free signing (ready-f947)", () => {
  it("produces an event verifyEvent accepts, signed as the secret's own pubkey", async () => {
    const signed = await localSigner(SECRET).signEvent({
      created_at: 1_700_000_000,
      kind: 1,
      tags: [["t", "x"]],
      content: "hello from the browser, no extension",
    });
    // verifyEvent is the SAME verifier the board runs on relay events, so a pass
    // here means rd on another machine will accept this signature.
    expect(verifyEvent(signed)).toBe(true);
    expect(signed.pubkey).toBe(xOnlyPubkey(SECRET));
    expect(signed.sig).toMatch(/^[0-9a-f]{128}$/);
  });

  it("signs EXACTLY the fields handed in — never restamps a field (assertSignedAsBuilt)", async () => {
    const unsigned = { created_at: 1_234_567_890, kind: 30302, tags: [["d", "board-d"]], content: "card body" };
    const signed = await localSigner(SECRET).signEvent(unsigned);
    expect(signed.created_at).toBe(unsigned.created_at);
    expect(signed.kind).toBe(unsigned.kind);
    expect(signed.tags).toEqual(unsigned.tags);
    expect(signed.content).toBe(unsigned.content);
    // The id the write path anchors to is the id of the unsigned card + pubkey.
    expect(signed.id).toBe(computeEventId({ pubkey: signed.pubkey, ...unsigned }));
  });
});
