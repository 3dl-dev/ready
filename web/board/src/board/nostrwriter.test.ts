// nostrwriter.test.ts — the write path's REFUSALS and its failure handling.
//
// The happy path is proved elsewhere and more strongly: writeevents.vectors.
// test.ts pins the exact events against rd's own vector file, and
// scripts/live-write-roundtrip.mjs performs all seven operations in a real
// Chromium against the live relay and reads them back with an independent rd.
// What CI cannot run (a live relay, a real browser) is what these tests cover
// deterministically: who is refused, when the refusal happens (BEFORE anything
// is signed), and what the writer does when the relay says no.
import { describe, expect, it, vi } from "vitest";
import type { BoardDecryptor, SealEnvelope } from "../lib/envelope";
import { signNostrEvent, xOnlyPubkey } from "../lib/schnorrsign";
import { hexToBytes } from "../lib/sha256";
import type { NostrEvent } from "../lib/nostrevent";
import { RelayRejectedError, SignerMismatchError, type Nip07Signer } from "../lib/publish";
import { LEVEL_CONTRIBUTOR, LEVEL_MAINTAINER } from "../lib/rolegrant";
import type { Item } from "../lib/state";
import { NostrBoardWriter, NotAuthorizedError } from "./nostrwriter";
import { buildFullCreate, WriteRefusedError, type WriteEnv } from "./writeevents";

const SECRET = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
const OWNER = xOnlyPubkey(SECRET);
const OTHER = "abc123deadbeefabc123deadbeefabc123deadbeefabc123deadbeefabc12300";
const BOARD_D = "proj";
const RELAY = "wss://relay.example/";

function realSigner(secret = SECRET): Nip07Signer {
  return {
    async signEvent(e) {
      return signNostrEvent({ created_at: e.created_at, kind: e.kind, tags: e.tags, content: e.content }, secret);
    },
  };
}

/** A relay socket that answers every EVENT with the given OK verdict. No real
 * network, but the FRAME SHAPE is NIP-01's: the writer must read acceptance out
 * of ["OK", id, accepted, message], never out of "the socket didn't error". */
function fakeRelay(accepted: boolean, message = ""): (url: string) => WebSocket {
  return () => {
    const sock: Record<string, unknown> = {
      send(raw: string) {
        const [, ev] = JSON.parse(raw) as [string, NostrEvent];
        setTimeout(() => {
          (sock.onmessage as (m: { data: string }) => void)?.({
            data: JSON.stringify(["OK", ev.id, accepted, message]),
          });
        }, 0);
      },
      close() {},
    };
    setTimeout(() => (sock.onopen as (() => void) | undefined)?.(), 0);
    return sock as unknown as WebSocket;
  };
}

function seedItem(overrides: Partial<Item> = {}): { snapshot: NostrEvent[]; item: Item } {
  const item: Item = {
    id: "seed-1",
    msg_id: "",
    title: "Seed",
    context: "",
    type: "task",
    priority: "p2",
    status: "inbox",
    for: OWNER,
    created_at: 0n,
    updated_at: 0n,
    ...overrides,
  };
  const env: WriteEnv = {
    signer: OWNER,
    boardAuthor: OWNER,
    boardD: BOARD_D,
    boardTitle: BOARD_D,
    items: new Map(),
    issueEventIds: new Map(),
    createdAt: 1_780_000_000,
  };
  const snapshot = buildFullCreate(env, item).map((b) =>
    signNostrEvent({ created_at: b.created_at, kind: b.kind, tags: b.tags, content: b.content }, SECRET),
  );
  return { snapshot, item };
}

function makeWriter(over: Partial<ConstructorParameters<typeof NostrBoardWriter>[0]> = {}) {
  const { snapshot } = seedItem();
  return new NostrBoardWriter({
    signerPubkey: OWNER,
    signer: realSigner(),
    board: { ownerPubkey: OWNER, boardD: BOARD_D, title: BOARD_D },
    relays: [RELAY],
    snapshot,
    grantLevels: new Map([[OWNER, LEVEL_MAINTAINER]]),
    publishOptions: { socketFactory: fakeRelay(true), timeoutMs: 2000 },
    ...over,
  });
}

describe("who may write (client-side, and BEFORE anything is signed)", () => {
  it("a key with no grant on the board is refused, and the signer is never called", async () => {
    const signEvent = vi.fn();
    const w = makeWriter({ grantLevels: new Map([[OTHER, LEVEL_CONTRIBUTOR]]), signer: { signEvent } });
    expect(w.whyReadOnly()).toMatch(/no write grant/i);
    await expect(w.claim("seed-1")).rejects.toBeInstanceOf(NotAuthorizedError);
    expect(signEvent).not.toHaveBeenCalled();
  });

  it("no NIP-07 signer means read-only — and never a prompt for a key", async () => {
    const w = makeWriter({ signer: undefined });
    expect(w.whyReadOnly()).toMatch(/never accepts a secret key/i);
    await expect(w.moveStatus("seed-1", "active")).rejects.toBeInstanceOf(NotAuthorizedError);
  });

  it("a CONFIDENTIAL board with NO key held is refused rather than downgraded to plaintext", async () => {
    const signEvent = vi.fn();
    const w = makeWriter({ confidential: true, signer: { signEvent } });
    expect(w.whyReadOnly()).toMatch(/seals its free text/i);
    await expect(w.setTitle("seed-1", "leak me")).rejects.toBeInstanceOf(NotAuthorizedError);
    expect(signEvent).not.toHaveBeenCalled();
  });

  it("a granted key with a signer on a public board may write", async () => {
    const w = makeWriter();
    expect(w.whyReadOnly()).toBeUndefined();
    await w.claim("seed-1");
    expect(w.items().get("seed-1")!.status).toBe("active");
    expect(w.items().get("seed-1")!.by).toBe(OWNER);
  });
});

describe("the relay's answer is the outcome", () => {
  it("a rejected publish throws with the relay's own words and absorbs nothing", async () => {
    const w = makeWriter({
      publishOptions: {
        socketFactory: fakeRelay(false, "restricted: pubkey is not admitted to this relay's tenant write-allowlist"),
        timeoutMs: 2000,
      },
    });
    await expect(w.claim("seed-1")).rejects.toBeInstanceOf(RelayRejectedError);
    await expect(w.claim("seed-1")).rejects.toThrow(/not admitted/);
    // The optimistic UI's revert depends on this: a refused write must leave the
    // writer's own view exactly where it was.
    expect(w.items().get("seed-1")!.status).toBe("inbox");
  });

  it("a socket that never answers is a failure, not a success", async () => {
    const w = makeWriter({
      publishOptions: {
        socketFactory: () => ({ send() {}, close() {} }) as unknown as WebSocket,
        timeoutMs: 50,
      },
    });
    await expect(w.moveStatus("seed-1", "active")).rejects.toBeInstanceOf(RelayRejectedError);
    expect(w.items().get("seed-1")!.status).toBe("inbox");
  });
});

describe("the signer is not trusted to return what it was given", () => {
  it("an extension that rewrites created_at fails the write instead of publishing a broken anchor", async () => {
    const w = makeWriter({
      signer: {
        async signEvent(e) {
          return signNostrEvent(
            { created_at: e.created_at + 5, kind: e.kind, tags: e.tags, content: e.content },
            SECRET,
          );
        },
      },
    });
    await expect(w.claim("seed-1")).rejects.toBeInstanceOf(SignerMismatchError);
    expect(w.items().get("seed-1")!.status).toBe("inbox");
  });

  it("an extension signing as a DIFFERENT key is refused", async () => {
    const other = "0b432b2677937381aef05bb02a66ecd012773062cf3fa2549e44f58ed2401710";
    const w = makeWriter({ signer: realSigner(other) });
    await expect(w.claim("seed-1")).rejects.toBeInstanceOf(SignerMismatchError);
  });

  // ready-e51 (veracity audit). publish.ts's header claims assertSignedAsBuilt
  // refuses "a signer that quietly rewrote ANY field", and the id equality check
  // is the only line that can see a rewritten TAG or CONTENT — the pubkey and
  // created_at comparisons cannot. That line was witnessed by nothing: disabling
  // it (`if (false && (recomputed !== signed.id || signed.id !== built.id))`)
  // left 832/832 vitest green, because the two cases above trip the created_at
  // and pubkey guards respectively and never reach it.
  //
  // The realistic shape is not an attacker but a helpful extension: several
  // providers normalise or re-order what they are handed. On a CONFIDENTIAL
  // board that would silently strip ["enc","1"] and publish an owner-signed card
  // the rest of the board reads as cleartext; on any board it publishes state
  // the user did not ask for while the page reports success.
  it("an extension that rewrites a TAG is refused, though pubkey and created_at are untouched", async () => {
    const w = makeWriter({
      signer: {
        async signEvent(e) {
          // Same key, same created_at, same kind — only the payload differs, and
          // the returned event is a VALID signature over the rewritten tags, so
          // nothing but the id comparison against what the caller built can
          // notice. Rewriting the status tag is the concrete harm: the card the
          // relay stores says something the human never chose.
          const tags = e.tags.map((t) => (t[0] === "s" ? ["s", "cancelled"] : t));
          return signNostrEvent({ created_at: e.created_at, kind: e.kind, tags, content: e.content }, SECRET);
        },
      },
    });
    await expect(w.claim("seed-1")).rejects.toBeInstanceOf(SignerMismatchError);
    // Nothing was absorbed: the writer's view is exactly where it started, so
    // the refusal happened before the event could enter the local snapshot.
    expect(w.items().get("seed-1")!.status).toBe("inbox");
  });

  it("an extension that rewrites CONTENT is refused the same way", async () => {
    const w = makeWriter({
      signer: {
        async signEvent(e) {
          return signNostrEvent(
            { created_at: e.created_at, kind: e.kind, tags: e.tags, content: e.content + " (edited by the extension)" },
            SECRET,
          );
        },
      },
    });
    await expect(w.close("seed-1", "done", "the reason the human typed")).rejects.toBeInstanceOf(SignerMismatchError);
    expect(w.items().get("seed-1")!.status).toBe("inbox");
  });

  // ANTI-TAUTOLOGY for both cases above: the SAME writer with a signer that
  // returns exactly what it was given publishes without complaint, so the two
  // refusals are attributable to the rewrite and not to the write path being
  // refused for some unrelated reason.
  it("…and an honest signer returning exactly what it was handed is accepted", async () => {
    const w = makeWriter();
    await w.close("seed-1", "done", "the reason the human typed");
    expect(w.items().get("seed-1")!.status).toBe("done");
  });
});

describe("rd's own refusals hold in the browser too", () => {
  it("claiming a terminal item is refused client-side with rd's wording", async () => {
    const { snapshot } = seedItem({ id: "closed-1", status: "done" });
    const w = makeWriter({ snapshot });
    await expect(w.claim("closed-1")).rejects.toBeInstanceOf(WriteRefusedError);
    await expect(w.claim("closed-1")).rejects.toThrow(/already done/);
  });

  it("blocked can never be set directly — it is derived", async () => {
    const w = makeWriter();
    await expect(w.moveStatus("seed-1", "blocked")).rejects.toThrow(/derived from dependencies/);
  });
});

describe("§17.2 — two rapid writes to the same item are serialized", () => {
  it("the second write is stamped strictly later, so the later intent wins", async () => {
    const w = makeWriter();
    // Fired without awaiting the first: both would otherwise read the same
    // "newest event in this chain" and be stamped identically, at which point
    // the read side breaks the tie by event id — a coin flip.
    const a = w.claim("seed-1", "first");
    const b = w.close("seed-1", "done", "second");
    await Promise.all([a, b]);
    const stamps = w
      .items()
      .get("seed-1")!
      .history!.map((h) => h.timestamp);
    expect(new Set(stamps).size).toBe(stamps.length);
    expect(w.items().get("seed-1")!.status).toBe("done");
  });
});

describe("a CONFIDENTIAL board the session HOLDS the key for (ready-191)", () => {
  // The board is confidential and this session holds its CEK — from a grant
  // addressed to this key, or from a key-bearing link. The writer must then SEAL
  // rather than refuse; refusing here is what made every board created by a
  // plain `rd init` read-only in the browser.
  const CEK = hexToBytes("11".repeat(32));
  const LTK = hexToBytes("22".repeat(32));
  const enc: SealEnvelope = { cek: CEK, epoch: 1, ltk: LTK };
  const coord = `30301:${OWNER}:${BOARD_D}`;
  const decryptor: BoardDecryptor = {
    cek: (c, epoch) => (c === coord && epoch === 1 ? CEK : null),
  };

  /** A confidential seed built with the SAME buildFullCreate the writer itself
   * uses, run with the enc envelope — so the snapshot is genuinely sealed rather
   * than a plaintext card wearing an enc tag. */
  function confidentialWriter(over: Partial<ConstructorParameters<typeof NostrBoardWriter>[0]> = {}) {
    const item: Item = {
      id: "seed-1",
      msg_id: "",
      title: "Sealed seed",
      context: "sealed body",
      type: "task",
      priority: "p2",
      status: "inbox",
      for: OWNER,
      created_at: 0n,
      updated_at: 0n,
    };
    const snapshot = buildFullCreate(
      {
        signer: OWNER,
        boardAuthor: OWNER,
        boardD: BOARD_D,
        boardTitle: BOARD_D,
        items: new Map(),
        issueEventIds: new Map(),
        createdAt: 1_780_000_000,
        confidential: true,
        enc,
      },
      item,
    ).map((b) => signNostrEvent({ created_at: b.created_at, kind: b.kind, tags: b.tags, content: b.content }, SECRET));
    return new NostrBoardWriter({
      signerPubkey: OWNER,
      signer: realSigner(),
      board: { ownerPubkey: OWNER, boardD: BOARD_D, title: BOARD_D },
      relays: [RELAY],
      snapshot,
      grantLevels: new Map([[OWNER, LEVEL_MAINTAINER]]),
      publishOptions: { socketFactory: fakeRelay(true), timeoutMs: 2000 },
      confidential: true,
      enc,
      decryptor,
      ...over,
    });
  }

  it("is NOT read-only, and a status move round-trips through the writer's own view", async () => {
    const w = confidentialWriter();
    expect(w.whyReadOnly()).toBeUndefined();
    await w.claim("seed-1", "mine now");
    const item = w.items().get("seed-1")!;
    expect(item.status).toBe("active");
    expect(item.title).toBe("Sealed seed");
    expect(item.context).toBe("sealed body");
    expect(item.redacted).toBeFalsy();
  });

  it("WITHOUT the decryptor the writer refuses — it must not republish placeholders", async () => {
    // The trap this guards: the writer projects its own view to build the next
    // write from, so a projection told not to decrypt marks every sealed card
    // Redacted, and rebuilding a card from "[encrypted]" would re-seal the
    // placeholder AS the item's content, irreversibly. Refusing is correct; the
    // fix is to hand the writer its keys, which the case above does.
    const w = confidentialWriter({ decryptor: null });
    expect(w.items().get("seed-1")!.redacted).toBe(true);
    await expect(w.claim("seed-1")).rejects.toBeInstanceOf(WriteRefusedError);
  });

  it("holding the read key does NOT bypass the write-grant check", async () => {
    // Read authority and write authority are different questions. A key-bearing
    // link makes a board readable; it says nothing about whether this pubkey may
    // write, and the relay will refuse it too.
    const signEvent = vi.fn();
    const w = confidentialWriter({ grantLevels: new Map([[OTHER, LEVEL_CONTRIBUTOR]]), signer: { signEvent } });
    expect(w.whyReadOnly()).toMatch(/no write grant/i);
    await expect(w.claim("seed-1")).rejects.toBeInstanceOf(NotAuthorizedError);
    expect(signEvent).not.toHaveBeenCalled();
  });
});
