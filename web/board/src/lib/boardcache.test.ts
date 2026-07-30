// The PURE half of ready-fe4's cache: fingerprint, admission, prune,
// serialize/deserialize. No browser, no DOM, no Storage — the property the item
// asks this file to keep from moot's feedcache.ts.
//
// The composition half — does the PAGE actually refuse to paint what these
// functions refuse to admit — is main.cache.test.ts, which drives the real
// afterLogin over the Go-signed portfolio fixture. Both are needed: this file
// proves the rule, that one proves the rule is wired to anything.

import { describe, expect, it } from "vitest";
import {
  CACHE_VERSION,
  DEFAULT_LIMITS,
  admissibleBoards,
  deserializeView,
  discardOtherViewers,
  gateFingerprint,
  openBoardCache,
  pruneView,
  scopeKey,
  serializeView,
  type CacheStorage,
  type CachedBoard,
  type CachedView,
} from "./boardcache";
import type { FragmentKeys } from "./fragment";
import type { Item } from "../board/types";

const VIEWER = "a".repeat(64);
const OTHER = "b".repeat(64);
const COORD_A = `30301:${VIEWER}:alpha`;
const COORD_B = `30301:${VIEWER}:beta`;

function item(id: string, updatedAt = 1000): Item {
  return {
    id,
    title: `title ${id}`,
    type: "task",
    for: VIEWER,
    priority: "p2",
    status: "inbox",
    createdAt: 1,
    updatedAt,
    history: [{ timestamp: "2026-07-30T00:00:00Z", fromStatus: "", toStatus: "inbox" }],
  };
}

function board(coord: string, over: Partial<CachedBoard> = {}): CachedBoard {
  return {
    coord,
    title: "Alpha",
    state: "public",
    boardState: "open",
    detail: "",
    gate: gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: undefined }),
    high: 100,
    items: [item(`${coord}-1`)],
    ...over,
  };
}

function view(over: Partial<CachedView> = {}): CachedView {
  return {
    v: CACHE_VERSION,
    viewer: VIEWER,
    scope: scopeKey("portfolio"),
    savedAt: 1_000_000,
    boards: [board(COORD_A)],
    ...over,
  };
}

const keys = (...epochs: number[]): FragmentKeys => ({
  ceks: epochs.map((epoch) => ({ epoch, key: new Uint8Array(32) })),
});

/** An in-memory Storage. Everything the module touches, nothing it does not. */
function memStorage(): CacheStorage & { data: Map<string, string> } {
  const data = new Map<string, string>();
  return {
    data,
    get length() {
      return data.size;
    },
    key: (i: number) => [...data.keys()][i] ?? null,
    getItem: (k: string) => data.get(k) ?? null,
    setItem: (k: string, v: string) => void data.set(k, v),
    removeItem: (k: string) => void data.delete(k),
  };
}

describe("gateFingerprint pins every pre-fetch input to a read decision", () => {
  it("a different viewer is a different fingerprint", () => {
    const a = gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: undefined });
    const b = gateFingerprint({ viewer: OTHER, signing: false, linkKeys: undefined });
    expect(a).not.toBe(b);
  });

  it("a signing session and a read-only one are different fingerprints", () => {
    expect(gateFingerprint({ viewer: VIEWER, signing: true, linkKeys: undefined })).not.toBe(
      gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: undefined }),
    );
  });

  it("holding a key and holding none are different fingerprints", () => {
    expect(gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: keys(1) })).not.toBe(
      gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: undefined }),
    );
  });

  it("holding MORE epochs is a different fingerprint — a rotation changes what opens", () => {
    expect(gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: keys(1, 2) })).not.toBe(
      gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: keys(1) })
    );
  });

  it("epoch ORDER does not change it — the same keys are the same session", () => {
    expect(gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: keys(2, 1) })).toBe(
      gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: keys(1, 2) }),
    );
  });

  it("CARRIES NO KEY MATERIAL — the epochs travel, the bytes never do", () => {
    // Two links, same epoch, different 32 bytes. Same fingerprint, and neither
    // string contains anything derived from the key. See the function's doc for
    // why the shape is sufficient: a wrong CEK opens nothing.
    const one: FragmentKeys = { ceks: [{ epoch: 1, key: new Uint8Array(32).fill(7) }] };
    const two: FragmentKeys = { ceks: [{ epoch: 1, key: new Uint8Array(32).fill(9) }] };
    const fp = gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: one });
    expect(fp).toBe(gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: two }));
    // Neither key's bytes, in either encoding, appear anywhere in the string.
    expect(fp).not.toContain("07".repeat(4));
    expect(fp).not.toContain("09".repeat(4));
    expect(fp).not.toContain(btoa(String.fromCharCode(...new Uint8Array(4).fill(7))));
  });
});

describe("admissibleBoards is the paint-time gate", () => {
  const gate = { viewer: VIEWER, signing: false, keysFor: () => undefined };

  it("admits a board whose recorded inputs match this session's", () => {
    expect(admissibleBoards(view(), gate).map((b) => b.coord)).toEqual([COORD_A]);
  });

  it("refuses a board recorded by a DIFFERENT viewer's session", () => {
    const stale = view({
      boards: [board(COORD_A, { gate: gateFingerprint({ viewer: OTHER, signing: false, linkKeys: undefined }) })],
    });
    expect(admissibleBoards(stale, gate)).toEqual([]);
  });

  it("refuses a board recorded by a session that HELD A LINK KEY when this one does not", () => {
    const withKey = view({
      boards: [
        board(COORD_A, {
          state: "confidential",
          gate: gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: keys(1) }),
        }),
      ],
    });
    expect(admissibleBoards(withKey, gate)).toEqual([]);
    // …and admits it to the session that DOES hold it, or the case above would
    // pass for the wrong reason (nothing is ever admitted).
    expect(
      admissibleBoards(withKey, { viewer: VIEWER, signing: false, keysFor: () => keys(1) }).map((b) => b.coord),
    ).toEqual([COORD_A]);
  });

  it("PER-BOARD KEY SCOPE: a key filed under board B does not admit board A", () => {
    const cached = view({
      boards: [
        board(COORD_A, {
          state: "confidential",
          gate: gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: keys(1) }),
        }),
      ],
    });
    const keysForB = { viewer: VIEWER, signing: false, keysFor: (c: string) => (c === COORD_B ? keys(1) : undefined) };
    expect(admissibleBoards(cached, keysForB)).toEqual([]);
  });

  it("BELT: a non-public board is never painted into a session with no key path at all", () => {
    // Fingerprint deliberately forged to match — this is the check that still
    // holds if a future edit widens the fingerprint.
    const forged = view({
      boards: [
        board(COORD_A, { state: "confidential", gate: gateFingerprint({ viewer: VIEWER, signing: false, linkKeys: undefined }) }),
      ],
    });
    expect(admissibleBoards(forged, gate)).toEqual([]);
    // A public board with the same forged fingerprint IS admitted, so the case
    // above is about confidentiality and not about the fingerprint.
    const publicBoard = view({ boards: [board(COORD_A, { state: "public" })] });
    expect(admissibleBoards(publicBoard, gate).length).toBe(1);
  });
});

describe("pruneView bounds what one view may occupy", () => {
  it("drops history — the largest field with the narrowest consumer", () => {
    const pruned = pruneView(view(), { ...DEFAULT_LIMITS, now: 1_000_000 });
    expect(pruned.boards[0].items[0].history).toBeUndefined();
    expect(pruned.boards[0].items[0].title).toBe(view().boards[0].items[0].title);
  });

  it("caps the total item count, keeping the most recently updated", () => {
    const many = view({
      boards: [board(COORD_A, { items: [item("old", 1), item("new", 999), item("mid", 500)] })],
    });
    const pruned = pruneView(many, { maxItems: 2, maxAgeMs: DEFAULT_LIMITS.maxAgeMs, now: 1_000_000 });
    expect(pruned.boards[0].items.map((i) => i.id)).toEqual(["new", "mid"]);
  });

  it("shares the budget across boards so one busy board does not blank the rest", () => {
    const two = view({
      boards: [
        board(COORD_A, { items: Array.from({ length: 50 }, (_, i) => item(`a${i}`, i)) }),
        board(COORD_B, { items: Array.from({ length: 50 }, (_, i) => item(`b${i}`, i)) }),
      ],
    });
    const pruned = pruneView(two, { maxItems: 10, maxAgeMs: DEFAULT_LIMITS.maxAgeMs, now: 1_000_000 });
    expect(pruned.boards[0].items.length).toBe(5);
    expect(pruned.boards[1].items.length).toBe(5);
  });

  it("an entry older than maxAge keeps NO boards", () => {
    const old = pruneView(view({ savedAt: 0 }), { ...DEFAULT_LIMITS, now: DEFAULT_LIMITS.maxAgeMs + 1 });
    expect(old.boards).toEqual([]);
  });
});

describe("serialize / deserialize refuses anything it cannot vouch for", () => {
  it("round-trips a view", () => {
    const out = deserializeView(serializeView(view()), VIEWER, scopeKey("portfolio"));
    expect(out?.boards[0].items[0].id).toBe(`${COORD_A}-1`);
  });

  it("null for junk, for a wrong version, for a wrong viewer and for a wrong scope", () => {
    expect(deserializeView("{not json", VIEWER, scopeKey("portfolio"))).toBeNull();
    expect(deserializeView(null, VIEWER, scopeKey("portfolio"))).toBeNull();
    expect(deserializeView(JSON.stringify({ ...view(), v: 99 }), VIEWER, scopeKey("portfolio"))).toBeNull();
    expect(deserializeView(serializeView(view()), OTHER, scopeKey("portfolio"))).toBeNull();
    expect(deserializeView(serializeView(view()), VIEWER, scopeKey("board", COORD_A))).toBeNull();
  });

  it("drops a board entry with no gate fingerprint rather than admitting it ungated", () => {
    const raw = JSON.parse(serializeView(view())) as CachedView;
    delete (raw.boards[0] as Partial<CachedBoard>).gate;
    expect(deserializeView(JSON.stringify(raw), VIEWER, scopeKey("portfolio"))?.boards).toEqual([]);
  });

  it("an unrecognised state reads as public — which is the state that CANNOT be painted without a key path", () => {
    const raw = JSON.parse(serializeView(view({ boards: [board(COORD_A, { state: "confidential" })] }))) as CachedView;
    (raw.boards[0] as { state: string }).state = "sort-of-secret";
    const out = deserializeView(JSON.stringify(raw), VIEWER, scopeKey("portfolio"));
    expect(out?.boards[0].state).toBe("public");
  });
});

describe("openBoardCache is keyed per pubkey and evicts every other one", () => {
  it("writes and reads back under this viewer's key", () => {
    const s = memStorage();
    const cache = openBoardCache(s, VIEWER, scopeKey("portfolio"));
    cache.write(view());
    expect(cache.read()?.boards[0].coord).toBe(COORD_A);
    expect([...s.data.keys()][0]).toContain(VIEWER);
  });

  it("a different scope does not read another scope's entry", () => {
    const s = memStorage();
    openBoardCache(s, VIEWER, scopeKey("portfolio")).write(view());
    expect(openBoardCache(s, VIEWER, scopeKey("board", COORD_A)).read()).toBeNull();
  });

  it("opening as another pubkey DISCARDS the previous one's entries", () => {
    const s = memStorage();
    openBoardCache(s, VIEWER, scopeKey("portfolio")).write(view());
    expect(s.data.size).toBe(1);
    openBoardCache(s, OTHER, scopeKey("portfolio"));
    expect(s.data.size).toBe(0);
  });

  it("leaves non-cache keys alone", () => {
    const s = memStorage();
    s.setItem("someone-elses-key", "value");
    discardOtherViewers(s, VIEWER);
    expect(s.getItem("someone-elses-key")).toBe("value");
  });

  it("a Storage that throws on write leaves no entry and no exception", () => {
    const s = memStorage();
    const throwing: CacheStorage = { ...s, setItem: () => { throw new Error("QuotaExceededError"); } };
    expect(() => openBoardCache(throwing, VIEWER, scopeKey("portfolio")).write(view())).not.toThrow();
  });
});
