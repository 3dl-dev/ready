// ready-75a regression: a FORGED kind-39301 must not move a read-trust level or
// a revocation bound. The defect this pins was a port divergence — Go's
// pkg/sync/rolegrant.go deriveGrants calls e.Verify() inside the grant loop
// ("a forged/tampered grant cannot influence levels"); the TS port dropped it
// and fold.ts handed deriveLevels the RAW relay array, verifying only later in
// a different loop whose result was never reused. An untrusted relay could
// therefore undo a board owner's revocation with an event carrying no valid
// signature.
//
// The forgeries are appended to a REAL COMMITTED VECTOR
// (testdata/fold.vectors.json :: grant_admits_and_revoke_is_prospective, the
// same file internal/foldvectors/vectors_test.go runs Go's fold against), not
// to a hand-built log, so the genuine grant -> revoke sequence they are trying
// to subvert is the one both folds already agree on.
//
// Both forgery shapes from the two independent reproductions are covered:
//   A. an UNSIGNED grant: id = 64 zeros, sig = 128 zeros, pubkey spoofed to the
//      board owner (an attacker-pubkey grant yields no level at all, so the
//      attack requires spoofing the owner — but no valid signature).
//   B. the STRONGER one: a genuine event's real id and real BIP-340 signature
//      copied VERBATIM onto tampered tags (role: revoked -> maintainer).
//
// ANTI-TAUTOLOGY: forgery B's exact tampered tags, when GENUINELY SIGNED by the
// owner's key from the vector, DO take the agent to maintainer and DO lift the
// revocation bound (see "the attack works when the signature is real"). So
// these tests are not passing because the tags are inert, the coordinate is
// wrong, or the escalation cap bites — the signature check is the only thing
// standing between the forgery and a privilege escalation.

import { describe, expect, it } from "vitest";
import { schnorr } from "@noble/curves/secp256k1.js";
import vectorFileJSON from "../../../../testdata/fold.vectors.json";
import { computeEventId, verifyEvent, tagValue, dedupeExact } from "./nostrevent";
import type { NostrEvent, VerifiedEvent } from "./nostrevent";
import {
  deriveLevels,
  KIND_ROLE_GRANT,
  LEVEL_REVOKED,
  LEVEL_MAINTAINER,
  AUTHORITATIVE_FOREVER,
} from "./rolegrant";
import { projectItems } from "./fold";
import { fetchEventsFromRelays } from "./relay";
import type { ProjectOptions } from "./fold";
import { bytesToHex, hexToBytes } from "./sha256";

const VECTOR_NAME = "grant_admits_and_revoke_is_prospective";

interface VectorFile {
  keys: { name: string; secret: string; pubkey: string }[];
  vectors: { name: string; options: { pinned_board: string }; events: (NostrEvent | null)[] }[];
}

const vectorFile = vectorFileJSON as unknown as VectorFile;
const vector = vectorFile.vectors.find((v) => v.name === VECTOR_NAME);
if (!vector) throw new Error(`vector ${VECTOR_NAME} missing from testdata/fold.vectors.json`);

function keyByName(name: string): { secret: string; pubkey: string } {
  const k = vectorFile.keys.find((x) => x.name === name);
  if (!k) throw new Error(`vector key ${name} missing`);
  return k;
}

const OWNER = keyByName("owner");
const AGENT = keyByName("agent");
const PINNED = vector.options.pinned_board;
const { owner: BOARD_OWNER, boardD: BOARD_D } = (() => {
  const parts = PINNED.split(":");
  return { owner: parts[1], boardD: parts[2] };
})();

/** The genuine owner-signed revoke of AGENT that the forgeries try to undo. */
const GENUINE_REVOKE = (() => {
  const e = vector.events.find(
    (x): x is NostrEvent => !!x && x.kind === KIND_ROLE_GRANT && tagValue(x, "role") === "revoked",
  );
  if (!e) throw new Error(`vector ${VECTOR_NAME} no longer contains a role=revoked grant`);
  return e;
})();

const REVOKE_AT = GENUINE_REVOKE.created_at;

/** Forgery A — wholly unsigned, owner pubkey spoofed, newest so latest-wins
 * would hand it the decision. */
const FORGED_UNSIGNED: NostrEvent = {
  id: "0".repeat(64),
  pubkey: BOARD_OWNER,
  created_at: REVOKE_AT + 300,
  kind: KIND_ROLE_GRANT,
  tags: [
    ["d", `${BOARD_D}:${AGENT.pubkey}`],
    ["p", AGENT.pubkey],
    ["a", PINNED],
    ["role", "contributor"],
  ],
  content: "",
  sig: "0".repeat(128),
};

/** tamperToMaintainer rewrites the role tag, leaving every other field — id,
 * sig, pubkey, created_at, content — byte-identical to the genuine revoke. */
function tamperToMaintainer(e: NostrEvent): NostrEvent {
  return {
    ...e,
    tags: e.tags.map((t) => (t[0] === "role" ? ["role", "maintainer"] : t.slice())),
  };
}

/** Forgery B — real id + real signature lifted verbatim onto tampered tags. */
const FORGED_REAL_SIG = tamperToMaintainer(GENUINE_REVOKE);

/** The SAME tampered tags, but honestly signed by the owner's vector key. This
 * is the potency control: it must succeed. */
const HONESTLY_SIGNED_ESCALATION: NostrEvent = (() => {
  const unsigned = {
    pubkey: BOARD_OWNER,
    created_at: FORGED_REAL_SIG.created_at,
    kind: FORGED_REAL_SIG.kind,
    tags: FORGED_REAL_SIG.tags,
    content: FORGED_REAL_SIG.content,
  };
  const id = computeEventId(unsigned);
  const sig = bytesToHex(schnorr.sign(hexToBytes(id), hexToBytes(OWNER.secret)));
  return { ...unsigned, id, sig };
})();

const GENUINE = vector.events;
const WITH_FORGERIES = [...GENUINE, FORGED_UNSIGNED, FORGED_REAL_SIG];

/** PRODUCTION options: trusted === null (what main.ts's loadBoardItems sets —
 * the read-trust gate is deliberately disabled in the browser), maintainers
 * null, board pinned. The unconditional point-in-time revocation gate is what
 * the forgeries poison, so the disabled gate is NOT what saves this. */
function productionOpts(): ProjectOptions {
  return {
    trusted: null,
    maintainers: null,
    pinnedBoard: PINNED,
    decryptor: null,
    encryptedBoards: null,
  };
}

/** Casting around the VerifiedEvent brand on purpose: this exercises
 * deriveLevels' OWN runtime schnorr check (defence 2), i.e. what protects the
 * derivation if a future caller casts the way this line does. */
function deriveUnchecked(events: (NostrEvent | null)[]) {
  return deriveLevels(events as unknown as VerifiedEvent[], BOARD_OWNER, BOARD_D);
}

describe("ready-75a — a forged kind-39301 cannot move levels or revocation bounds", () => {
  it("control: both forgeries fail verifyEvent, and the potency control passes it", () => {
    expect(verifyEvent(FORGED_UNSIGNED)).toBe(false);
    expect(verifyEvent(FORGED_REAL_SIG)).toBe(false);
    // Forgery B really does carry the genuine event's id and signature.
    expect(FORGED_REAL_SIG.id).toBe(GENUINE_REVOKE.id);
    expect(FORGED_REAL_SIG.sig).toBe(GENUINE_REVOKE.sig);
    expect(tagValue(FORGED_REAL_SIG, "role")).toBe("maintainer");
    // ...and the honest version of the same tags is a valid event.
    expect(verifyEvent(HONESTLY_SIGNED_ESCALATION)).toBe(true);
    expect(verifyEvent(GENUINE_REVOKE)).toBe(true);
  });

  it("baseline: the committed vector revokes the agent prospectively", () => {
    const { levels, until } = deriveUnchecked(GENUINE);
    expect(levels.get(AGENT.pubkey)).toBe(LEVEL_REVOKED);
    expect(until.get(AGENT.pubkey)).toBe(REVOKE_AT);
    expect(levels.get(BOARD_OWNER)).toBe(LEVEL_MAINTAINER);
    expect(until.get(BOARD_OWNER)).toBe(AUTHORITATIVE_FOREVER);
  });

  it("levels and revocation bounds are UNCHANGED with both forgeries appended", () => {
    const base = deriveUnchecked(GENUINE);
    const attacked = deriveUnchecked(WITH_FORGERIES);
    expect([...attacked.levels.entries()].sort()).toEqual([...base.levels.entries()].sort());
    expect([...attacked.until.entries()].sort()).toEqual([...base.until.entries()].sort());
    // Spelled out, because these two are the escalation the forgeries buy:
    expect(attacked.levels.get(AGENT.pubkey)).toBe(LEVEL_REVOKED);
    expect(attacked.until.get(AGENT.pubkey)).toBe(REVOKE_AT);
  });

  it("each forgery alone is inert (neither shape works on its own)", () => {
    for (const forged of [FORGED_UNSIGNED, FORGED_REAL_SIG]) {
      const { levels, until } = deriveUnchecked([...GENUINE, forged]);
      expect(levels.get(AGENT.pubkey)).toBe(LEVEL_REVOKED);
      expect(until.get(AGENT.pubkey)).toBe(REVOKE_AT);
    }
  });

  it("ANTI-TAUTOLOGY: the very same tags, honestly signed, DO escalate", () => {
    const { levels, until } = deriveUnchecked([...GENUINE, HONESTLY_SIGNED_ESCALATION]);
    expect(levels.get(AGENT.pubkey)).toBe(LEVEL_MAINTAINER);
    expect(until.get(AGENT.pubkey)).toBe(AUTHORITATIVE_FOREVER);
  });

  it("deriveLevels REJECTS the raw relay array at compile time (defence 1)", () => {
    const raw: NostrEvent[] = WITH_FORGERIES.filter((e): e is NostrEvent => e !== null);
    // If this stops being a type error, the structural guard is gone: `tsc -b
    // --noEmit` fails on an UNUSED @ts-expect-error, so this line is the
    // compile-time regression test for the VerifiedEvent brand.
    // @ts-expect-error — raw NostrEvent[] is not VerifiedEvent[]
    const forced = deriveLevels(raw, BOARD_OWNER, BOARD_D);
    // Executed anyway: even a caller that casts around the brand gets safe
    // levels, because defence 2 is live inside the replay loop.
    expect(forced.levels.get(AGENT.pubkey)).toBe(LEVEL_REVOKED);
    expect(forced.until.get(AGENT.pubkey)).toBe(REVOKE_AT);
  });
});

describe("ready-75a — the rendered fold is unchanged by the forgeries", () => {
  const ITEM_ID = "ready-v27";

  it("baseline: the post-revoke edit is not honored", () => {
    const items = projectItems(GENUINE, productionOpts());
    const item = items.get(ITEM_ID);
    expect(item).toBeDefined();
    expect(item?.title).toBe("authored before the revoke");
    expect(item?.status).toBe("active");
  });

  it("with both forgeries appended, the item is byte-identical to baseline", () => {
    const base = projectItems(GENUINE, productionOpts());
    const attacked = projectItems(WITH_FORGERIES, productionOpts());
    expect(JSON.stringify([...attacked.keys()])).toBe(JSON.stringify([...base.keys()]));
    for (const [id, item] of base) {
      expect(jsonable(attacked.get(id))).toEqual(jsonable(item));
    }
    const item = attacked.get(ITEM_ID);
    expect(item?.title).toBe("authored before the revoke");
    expect(item?.status).toBe("active");
  });

  it("ANTI-TAUTOLOGY: honestly signed, the post-revoke edit IS honored", () => {
    const items = projectItems([...GENUINE, HONESTLY_SIGNED_ESCALATION], productionOpts());
    const item = items.get(ITEM_ID);
    expect(item?.title).toBe("post-revoke edit");
    expect(item?.status).toBe("done");
  });
});

// ready-dd5 — the same verify-too-late class, one layer earlier. The transport
// (relay.ts fetchEventsFromRelays) and the two-REQ snapshot merge (main.ts)
// used to dedup on the SELF-DECLARED event id before any signature was
// checked, last write winning. Forgery B is the worst case for that: its id AND
// its signature are byte-identical to the genuine revoke, so an id-keyed map
// EVICTS the genuine revoke, the fold then rejects the forgery, and the
// revocation is gone from the snapshot entirely — no forged grant needed to
// win, just a real one deleted.
describe("ready-dd5 — pre-verification dedup must not evict a genuine event", () => {
  it("dedupeExact keeps a same-id/same-sig forgery AND the genuine event", () => {
    const merged = dedupeExact([...GENUINE.filter((e): e is NostrEvent => e !== null), FORGED_REAL_SIG]);
    expect(merged).toContain(GENUINE_REVOKE);
    expect(merged).toContain(FORGED_REAL_SIG);
    // ...and it still collapses a byte-identical duplicate (the same event
    // served by two relays), which is what the dedup exists for.
    const withDupe = dedupeExact([GENUINE_REVOKE, { ...GENUINE_REVOKE, tags: GENUINE_REVOKE.tags.map((t) => t.slice()) }]);
    expect(withDupe).toHaveLength(1);
  });

  it("the fold over the deduped snapshot still honors the revocation", () => {
    const merged = dedupeExact([...GENUINE.filter((e): e is NostrEvent => e !== null), FORGED_REAL_SIG]);
    const items = projectItems(merged, productionOpts());
    expect(items.get("ready-v27")?.title).toBe("authored before the revoke");
    expect(items.get("ready-v27")?.status).toBe("active");
    const { levels, until } = deriveUnchecked(merged);
    expect(levels.get(AGENT.pubkey)).toBe(LEVEL_REVOKED);
    expect(until.get(AGENT.pubkey)).toBe(REVOKE_AT);
  });

  // THE RESIDUAL the first pass missed, end to end. The two tests above call
  // dedupeExact directly, so they only ever exercised the CROSS-RELAY merge.
  // fetchFromOneRelay's own paging dedup collapsed on the id too — one layer
  // earlier — so a SINGLE hostile relay deleted the genuine revoke inside its
  // own walk and nothing reached the merge to be preserved. This drives the
  // REAL fetchEventsFromRelays with ONE relay serving the whole committed
  // vector with forgery B interleaved BEFORE the genuine revoke, then folds
  // exactly what the transport returned.
  it("a single hostile relay cannot delete the genuine revoke from the real transport", async () => {
    const genuine = GENUINE.filter((e): e is NostrEvent => e !== null);
    // Tampered same-id/same-sig copy served FIRST, so an id-keyed dedup with
    // either write-wins rule loses one of the two — and the one it loses is the
    // only event carrying the revocation.
    const served: NostrEvent[] = [];
    for (const e of genuine) {
      if (e === GENUINE_REVOKE) served.push(FORGED_REAL_SIG);
      served.push(e);
    }

    const snapshot = await fetchEventsFromRelays(
      ["wss://hostile.example"],
      { kinds: [KIND_ROLE_GRANT] },
      {
        webSocketCtor: ReplayWebSocket.serving(served) as unknown as typeof WebSocket,
        retries: 0,
        timeoutMs: 5000,
      },
    );

    // The genuine revoke SURVIVED transport — this is the assertion that was
    // false before fetchFromOneRelay was fixed (the snapshot held only the
    // tampered copy).
    expect(snapshot).toContainEqual(GENUINE_REVOKE);
    expect(snapshot).toContainEqual(FORGED_REAL_SIG);

    // ...and the fold over that snapshot still honors the revocation.
    const items = projectItems(snapshot, productionOpts());
    expect(items.get("ready-v27")?.title).toBe("authored before the revoke");
    expect(items.get("ready-v27")?.status).toBe("active");
    const { levels, until } = deriveUnchecked(snapshot);
    expect(levels.get(AGENT.pubkey)).toBe(LEVEL_REVOKED);
    expect(until.get(AGENT.pubkey)).toBe(REVOKE_AT);
  });
});

/**
 * ReplayWebSocket is a hermetic relay stub (rule 11 — no real network in a unit
 * suite): it answers the FIRST REQ with a scripted event list and every later
 * page of relay.ts's `until` walk with an empty EOSE, which is what ends the
 * walk. It exists so the test above can exercise the real fetchEventsFromRelays
 * — including fetchFromOneRelay's dedup — rather than the merge alone.
 */
class ReplayWebSocket {
  private static script: NostrEvent[] = [];
  static serving(events: NostrEvent[]): typeof ReplayWebSocket {
    ReplayWebSocket.script = events;
    return ReplayWebSocket;
  }
  onopen: (() => void) | null = null;
  onerror: ((ev?: unknown) => void) | null = null;
  onclose: ((ev?: unknown) => void) | null = null;
  onmessage: ((ev: { data: string }) => void) | null = null;
  private served = false;

  constructor(_url: string) {
    queueMicrotask(() => this.onopen?.());
  }
  send(data: string): void {
    const frame = JSON.parse(data) as [string, string, unknown];
    if (frame[0] !== "REQ") return;
    const sub = frame[1];
    const batch = this.served ? [] : ReplayWebSocket.script;
    this.served = true;
    queueMicrotask(() => {
      for (const e of batch) this.onmessage?.({ data: JSON.stringify(["EVENT", sub, e]) });
      this.onmessage?.({ data: JSON.stringify(["EOSE", sub]) });
    });
  }
  close(): void {
    /* nothing to release */
  }
}

/** jsonable makes Item (which carries BigInt timestamps) comparable. */
function jsonable(v: unknown): unknown {
  return JSON.parse(JSON.stringify(v, (_k, x) => (typeof x === "bigint" ? x.toString() : x)));
}
