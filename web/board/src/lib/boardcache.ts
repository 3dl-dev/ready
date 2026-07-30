// boardcache.ts — the board paints from what this session already ADMITTED,
// then reconciles (ready-fe4).
//
// THE PROBLEM, MEASURED. ready-27b's portfolio view took 97.2s to first paint
// against wss://relay.3dl.network with 75 boards: the owner opens his board and
// waits a minute and a half at a blank page. Instrumented here (see
// scripts/live-portfolio-timing.mjs, run 2026-07-30 over 12 of those boards):
// 69.1s of load, of which only 8.3s was the relay. The rest is BIP-340
// verification — 3.49ms per event through the hand-rolled curve arithmetic in
// secp256k1.ts, on ~900 events per board. No amount of concurrency fixes a
// single-threaded CPU bound; the only way a person sees their board sooner is
// to paint what this browser already decided it could show.
//
// WHAT THIS FILE BUYS, AND WHAT IT DOES NOT — COLD vs WARM, which is the only
// comparison that says anything about a CACHE. The same commit also made the
// board fetches overlap and paint per board as they fold; measured against
// cdc53b7 that took the portfolio's first paint from 73.1s to seconds. THAT
// NUMBER IS THE CONCURRENCY'S, not this file's: both of its runs opened a
// browser with an EMPTY store. This file's own number is one build, two visits
// a minute apart, on the operator's real 40-board portfolio — scripts/
// live-cache.mjs against wss://relay.3dl.network, 2026-07-30, three runs:
//
//                              cold                  warm
//   time to the first item     4.7s / 5.8s / 7.3s    0.17s / 0.15s / 0.12s
//   relay frames arrived by    2645–3143             0, every time
//   time to all boards settled 62s / 48s / 45s       65s / 72s / 47s
//
// So the cache is worth the FIVE-TO-SEVEN SECONDS a person spends looking at an
// empty workspace, and nothing else. It does not make the load finish sooner:
// the settle is bounded by the relay and by BIP-340 verification, and on a warm
// visit it measured 2s to 25s SLOWER, because a warm page renders a full
// workspace up front and then repaints it as each of 40 boards lands. That is
// the honest trade and it is worth taking for someone who opens this page every
// day — their board is readable in a sixth of a second instead of six seconds,
// and every card on it is replaced by this session's own fold as the boards
// arrive. It is not worth pretending to be more than that.
//
// WHAT IS CACHED, AND WHY IT IS THE FOLD'S OUTPUT AND NOT THE RELAY'S INPUT.
// This file stores ADMITTED ITEMS — the Item[] a completed load produced, after
// the read-trust gate (ready-605), after the confidentiality decision
// (ready-daf/ready-f6b) and after the fold's quarantine. It deliberately does
// NOT store raw events. Two reasons, both load-bearing:
//
//   1. A raw-event cache re-projected at paint time would have to re-run every
//      gate to be safe, and re-running them means re-verifying every signature
//      — which is the 3.49ms/event cost this exists to avoid. A cache that must
//      pay the cost it was built to skip is not a cache.
//   2. A raw-event cache re-projected under WEAKER inputs (no keyring yet, no
//      grants fetched yet, no cutover established) is precisely the fail-open
//      ready-daf closed: a board whose confidentiality this session cannot
//      establish would render its cleartext cards from local bytes.
//
// So the stored value is the ANSWER, and what makes serving it honest is that
// it is served only to a session that would ask the same QUESTION. See
// gateFingerprint.
//
// NOTHING CACHED HERE SURVIVES RECONCILE. The cached items are a paint; the
// completed per-board load REPLACES that board's items wholesale. That is what
// keeps localStorage out of the trust path: a tampered entry can change what a
// person sees for the seconds before their own relay answer lands, and cannot
// change anything after it. It is also why a card the fold WITHHELD (ready-475
// is chasing 167 of the `ready` board's 536) cannot be resurrected from a stale
// entry: it was never admitted, so it was never cached, and the reconcile that
// follows is the same full fold that withheld it.
//
// DIVERGENCE FROM moot's lib/feedcache.ts, RECORDED AS ready-fe4 CONDITION 3
// REQUIRES. moot caches its feed in sessionStorage, deliberately, so that stale
// posts cannot resurrect as current in a new tab — a social feed's value IS its
// recency, and a day-old post presented as now is a lie the reader cannot
// detect. This cache uses localStorage instead, and the difference is a
// property of the DATA, not a preference: work items are signed, dated, and
// expected to survive a tab close; staleness is detectable from updated_at and
// is corrected by a reconcile that always runs; and the whole point is the
// session that comes back TOMORROW, which sessionStorage cannot serve. What
// this file borrows from feedcache.ts is its shape — pure prune / serialize /
// deserialize helpers that unit-test with no browser at all, with the Storage
// object injected at the edge (openBoardCache) rather than reached for.

import type { Item } from "../board/types";
import type { FragmentKeys } from "./fragment";
import type { Confidentiality } from "./confidentiality";
import type { BoardLoadState } from "./boardstate";

/** Bumped whenever a stored entry's meaning changes. A reader that finds any
 * other version discards rather than migrating: the cache is an optimization
 * with an authoritative source one round-trip away, so a migration path would
 * be code that can only ever be wrong about what old bytes meant. */
export const CACHE_VERSION = 1;

const KEY_PREFIX = "rd.board.cache.v1.";

/** Storage is the two methods this module uses, so a test needs no browser and
 * production passes window.localStorage. */
export interface CacheStorage {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
  readonly length: number;
  key(index: number): string | null;
}

/** CachedBoard is one board's admitted projection plus everything the paint
 * needs to render its tree node honestly — including the load state and detail
 * ready-27b put there, because a cached node that says "open" about a board
 * that was withholding is the exact lie that item closed. */
export interface CachedBoard {
  coord: string;
  /** The board's signed title at cache time; "" when it was itself sealed. */
  title: string;
  /** The confidentiality verdict the cached items were admitted under. */
  state: Confidentiality;
  /** ready-27b's per-board load outcome, rendered on the tree node. */
  boardState: BoardLoadState;
  detail: string;
  /** The gate fingerprint of the session that admitted these items. */
  gate: string;
  /**
   * Newest created_at in the snapshot these items were folded from.
   *
   * WHAT IT IS NOT: the `since` cursor of the next load's per-board fetch. The
   * reconcile refetches each board IN FULL and replaces its items wholesale, on
   * purpose — see this file's header. A `since`-scoped initial fetch would make
   * the cached items part of the SETTLED projection rather than only of the
   * paint, and everything this file promises (a withheld card cannot come back,
   * a fabricated one cannot survive, a tampered store cannot outlive the relay
   * answer) rests on them not being. The `since`-derived incremental path this
   * transport does have is the LIVE SUBSCRIPTION's, which reconnects on exactly
   * this number (ready-4359, main.ts's startLiveUpdates) — it is incremental
   * against a snapshot THIS session folded, not against bytes off disk.
   *
   * It is recorded for the staleness readout and because the reconnect cursor
   * and the cache entry must come from the same snapshot to be comparable.
   *
   * THE COST OF THAT CHOICE IS MEASURED, not waved at: live-cache.mjs counts
   * every inbound relay frame per visit and prints both. 2026-07-30, the real
   * portfolio — cold 19,580 frames, warm 19,548. A warm visit pulls the SAME
   * traffic as a cold one. Whoever revisits ready-fe4 condition 2 is looking at
   * that pair of numbers, not at a description of them.
   */
  high: number;
  items: Item[];
}

export interface CachedView {
  v: number;
  viewer: string;
  scope: string;
  savedAt: number;
  notice?: string;
  boards: CachedBoard[];
}

/**
 * GateInputs is EVERY input to this page's read decisions that is knowable
 * BEFORE a single relay round-trip completes. That constraint is what makes the
 * fingerprint usable at paint time, and it is also what bounds what the
 * fingerprint can promise — see gateFingerprint.
 */
export interface GateInputs {
  /** The pubkey this session is reading as. */
  viewer: string;
  /** canSign(identity.auth): whether this session can ask a NIP-07 extension to
   * unwrap an owner-signed grant. A read-only `pk=` link session cannot, ever
   * (main.ts's mint site), so it is a stable property of the session and not a
   * guess about the extension's mood. */
  signing: boolean;
  /** The key material THIS LINK carries for the board, or undefined. */
  linkKeys: FragmentKeys | undefined;
}

/**
 * gateFingerprint is the whole security argument of this file in one string.
 *
 * A cached item was admitted by a decision — read-trust, confidentiality
 * cutover, decryptability — that this session cannot re-run before it has
 * fetched anything. So instead of re-deriving the ANSWER, the fingerprint pins
 * the INPUTS: paint a board's cached items only into a session that would put
 * the same question to the same gates. Different viewer, different signing
 * capability, or different link key material for this board => different
 * fingerprint => no paint, and the board waits for its own relay answer like a
 * cold one.
 *
 * WHAT THE KEY COMPONENT IS, AND WHY IT IS THE SHAPE AND NOT THE KEY. It is the
 * sorted list of CEK EPOCHS the link carries for this board, never the key
 * bytes and never a hash of them: no secret and no function of a secret is
 * written to browser storage (main.portfolio.test.ts's "no key material reaches
 * browser storage" is the standing guard, and a digest of a CEK would be a
 * verifier for a guessed one). The shape distinguishes every case that changes
 * what can be READ — no keys vs. keys, epoch 1 vs. epochs 1+2 — and the one
 * case it cannot distinguish, a link supplying DIFFERENT bytes for the same
 * epoch, cannot open anything the cached bytes could not: a wrong CEK fails its
 * AEAD and the fold hands back the placeholder.
 *
 * WHAT IT DOES NOT PROMISE, stated because it is easy to over-read. The
 * fingerprint cannot know whether the extension will actually unwrap a grant
 * this time — that is an await, and an await is not a paint. A signing session
 * whose extension is now locked or declines the prompt will see cached
 * plaintext titles for the seconds before its own load seals them. That is the
 * SAME reader, the SAME pubkey and the SAME browser that legitimately decrypted
 * those titles, so it discloses nothing new; it is a stale paint, and the
 * reconcile corrects it. Every case where the reader CHANGED — a different
 * npub, a link that no longer carries the board's key, a read-only session
 * where a signing one wrote the cache — is a fingerprint mismatch and paints
 * nothing.
 */
export function gateFingerprint(g: GateInputs): string {
  const epochs = (g.linkKeys?.ceks ?? [])
    .map((c) => c.epoch)
    .filter((e) => Number.isFinite(e))
    .sort((a, b) => a - b)
    .join(",");
  return `v${CACHE_VERSION}|${g.viewer}|${g.signing ? "sign" : "read"}|cek:${epochs}`;
}

/**
 * admissibleBoards is the paint-time gate: the cached boards this session may
 * render before it has fetched anything.
 *
 * TWO CHECKS, and the second is not implied by the first. The FINGERPRINT check
 * is the general rule above. The KEY-PATH check is a belt on the one direction
 * that matters most: a board whose cached items were admitted as anything other
 * than "public" is only painted into a session that has some way to hold that
 * board's key — a link that carries it, or a signer that can unwrap a grant.
 * Fingerprint equality already implies it today, which is the point: if a
 * future edit widens the fingerprint (drops `signing`, say), this check still
 * refuses to paint a confidential board's decrypted titles into a session that
 * structurally cannot decrypt.
 */
export function admissibleBoards(view: CachedView, g: ViewGate): CachedBoard[] {
  return view.boards.filter((b) => {
    const linkKeys = g.keysFor(b.coord);
    if (b.gate !== gateFingerprint({ viewer: g.viewer, signing: g.signing, linkKeys })) return false;
    if (b.state !== "public" && !(g.signing || (linkKeys?.ceks?.length ?? 0) > 0)) return false;
    return true;
  });
}

/**
 * ViewGate is GateInputs at VIEW scope: the link's key material is per board, so
 * the fingerprint is too, and a whole-view check has to look each board's keys
 * up by coordinate. That lookup is the same per-board key scope loadBoardItems
 * enforces (ready-df0/ready-4d9) — a key minted for board A can no more admit
 * board B's cached items than it can decrypt board B's cards.
 */
export interface ViewGate {
  viewer: string;
  signing: boolean;
  keysFor: (coord: string) => FragmentKeys | undefined;
}

/**
 * PruneLimits bounds what one view may occupy. localStorage is ~5MB per origin
 * and the live portfolio folds 6,106 items across 79 boards, so an unpruned
 * write throws QuotaExceededError and the cache silently never exists.
 */
export interface PruneLimits {
  /** Total items across all boards. */
  maxItems: number;
  /** Entries older than this are dropped entirely rather than painted. */
  maxAgeMs: number;
  now: number;
}

export const DEFAULT_LIMITS: Omit<PruneLimits, "now"> = {
  maxItems: 4000,
  // A week. Not a correctness bound — the reconcile is what makes the view
  // correct — but a bound on how wrong the first paint may look before it
  // lands, and on how long a board this reader has lost access to may keep
  // painting from bytes written while they still had it.
  maxAgeMs: 7 * 24 * 60 * 60 * 1000,
};

/**
 * pruneView bounds a view for storage. PURE — no Storage, no Date.now, no DOM,
 * so it unit-tests as a function (the property moot's feedcache.ts has and this
 * file keeps).
 *
 * WHAT IS DROPPED FIRST, and why it is `history`. A cached item's history is by
 * far its largest field and the ONLY consumer is the detail pane of one
 * selected item; dropping it makes the first paint smaller without making any
 * card on it wrong. Items themselves are then dropped newest-updated-first
 * across the whole view, so what survives is what a person is most likely to be
 * looking for. Nothing dropped here is lost: the reconcile refetches the board
 * in full and replaces its items wholesale.
 */
export function pruneView(view: CachedView, limits: PruneLimits): CachedView {
  const fresh = limits.now - view.savedAt <= limits.maxAgeMs ? view : { ...view, boards: [] };
  const ranked = fresh.boards
    .map((b) => ({
      ...b,
      items: b.items.map((i) => {
        const { history: _history, ...rest } = i;
        return rest as Item;
      }),
    }))
    .map((b) => ({ ...b, items: [...b.items].sort((a, z) => z.updatedAt - a.updatedAt) }));
  let budget = limits.maxItems;
  const boards: CachedBoard[] = [];
  // Round-robin across boards so a single 500-item board cannot consume the
  // whole budget and leave every other project blank on the first paint.
  const perBoard = ranked.length > 0 ? Math.max(1, Math.floor(limits.maxItems / ranked.length)) : 0;
  for (const b of ranked) {
    const take = Math.min(perBoard, budget, b.items.length);
    budget -= take;
    boards.push({ ...b, items: b.items.slice(0, take) });
  }
  return { ...fresh, boards };
}

/** serializeView is JSON with the version stamped in. Separate from the storage
 * write so it can be tested, and so a caller can measure the byte cost before
 * committing to it. */
export function serializeView(view: CachedView): string {
  return JSON.stringify({ ...view, v: CACHE_VERSION });
}

/**
 * deserializeView returns the view, or null for anything it cannot vouch for:
 * unparseable JSON, a different version, a missing or mismatched viewer/scope,
 * a board with no gate fingerprint. Null is always "no cache", which is the
 * fail-closed direction — the page simply loads cold.
 *
 * IT VALIDATES SHAPE, NOT TRUTH. localStorage is same-origin script-writable,
 * so nothing here can establish that the stored items are genuine; what bounds
 * the damage is that they are replaced wholesale by the reconcile, and that
 * `gate` has to match a fingerprint this session computes from its OWN inputs.
 */
export function deserializeView(raw: string | null, viewer: string, scope: string): CachedView | null {
  if (raw === null || raw === "") return null;
  let parsed: unknown;
  try {
    parsed = JSON.parse(raw);
  } catch {
    return null;
  }
  if (typeof parsed !== "object" || parsed === null) return null;
  const v = parsed as Partial<CachedView>;
  if (v.v !== CACHE_VERSION) return null;
  if (v.viewer !== viewer || v.scope !== scope) return null;
  if (typeof v.savedAt !== "number" || !Array.isArray(v.boards)) return null;
  const boards: CachedBoard[] = [];
  for (const b of v.boards) {
    if (typeof b !== "object" || b === null) continue;
    const c = b as Partial<CachedBoard>;
    if (typeof c.coord !== "string" || c.coord === "") continue;
    if (typeof c.gate !== "string" || c.gate === "") continue;
    if (!Array.isArray(c.items)) continue;
    boards.push({
      coord: c.coord,
      title: typeof c.title === "string" ? c.title : "",
      state: c.state === "confidential" || c.state === "unknown" ? c.state : "public",
      boardState: typeof c.boardState === "string" ? (c.boardState as BoardLoadState) : "open",
      detail: typeof c.detail === "string" ? c.detail : "",
      gate: c.gate,
      high: typeof c.high === "number" ? c.high : 0,
      items: c.items.filter((i): i is Item => typeof i === "object" && i !== null && typeof (i as Item).id === "string"),
    });
  }
  return {
    v: CACHE_VERSION,
    viewer,
    scope,
    savedAt: v.savedAt,
    notice: typeof v.notice === "string" ? v.notice : undefined,
    boards,
  };
}

/**
 * scopeKey names the QUESTION a view answers, so a portfolio link's cache is
 * never painted onto a single-board page and vice versa. Two sessions that
 * asked different questions of the relay hold different boards, so sharing one
 * entry would paint boards the current link does not cover.
 */
export function scopeKey(kind: string, detail = ""): string {
  return detail === "" ? kind : `${kind}:${detail}`;
}

/** BoardCache is the storage edge. Everything above it is pure. */
export interface BoardCache {
  read(): CachedView | null;
  write(view: CachedView): void;
  clear(): void;
}

function storageKey(viewer: string, scope: string): string {
  return `${KEY_PREFIX}${viewer}.${scope}`;
}

/**
 * openBoardCache binds the pure helpers above to a Storage.
 *
 * KEYED PER LOGGED-IN PUBKEY (ready-fe4 condition 5), and every OTHER pubkey's
 * entries are dropped on open. There is no logout button on this page — a
 * session ends by navigating away or by logging in as someone else — so
 * "discarded on logout" is enforced at the only moment the page can observe it:
 * the next identity to open a cache evicts the previous one's. A shared machine
 * therefore does not keep the last reader's board titles around under a key the
 * next reader's session would never look at but any script on the origin could.
 */
export function openBoardCache(storage: CacheStorage, viewer: string, scope: string): BoardCache {
  const key = storageKey(viewer, scope);
  discardOtherViewers(storage, viewer);
  return {
    read(): CachedView | null {
      try {
        return deserializeView(storage.getItem(key), viewer, scope);
      } catch {
        return null;
      }
    },
    write(view: CachedView): void {
      // Quota is a hard, common failure at this size, and a failed cache write
      // must never be visible to the reader: halve and retry, then give up. A
      // page with no cache is the page ready-27b shipped, which works.
      let candidate = view;
      for (let attempt = 0; attempt < 3; attempt++) {
        try {
          storage.setItem(key, serializeView(candidate));
          return;
        } catch {
          const half = Math.max(1, Math.floor(totalItems(candidate) / 2));
          candidate = pruneView(candidate, { maxItems: half, maxAgeMs: DEFAULT_LIMITS.maxAgeMs, now: view.savedAt });
        }
      }
      try {
        storage.removeItem(key);
      } catch {
        /* nothing further to try */
      }
    },
    clear(): void {
      try {
        storage.removeItem(key);
      } catch {
        /* nothing further to try */
      }
    },
  };
}

function totalItems(view: CachedView): number {
  let n = 0;
  for (const b of view.boards) n += b.items.length;
  return n;
}

/** discardOtherViewers removes every cached view belonging to a DIFFERENT
 * pubkey. See openBoardCache for why this is where "discarded on logout"
 * lives. */
export function discardOtherViewers(storage: CacheStorage, viewer: string): void {
  const doomed: string[] = [];
  try {
    for (let i = 0; i < storage.length; i++) {
      const k = storage.key(i);
      if (k === null || !k.startsWith(KEY_PREFIX)) continue;
      if (!k.startsWith(`${KEY_PREFIX}${viewer}.`)) doomed.push(k);
    }
    for (const k of doomed) storage.removeItem(k);
  } catch {
    /* a Storage that will not enumerate is a Storage with no cache to clear */
  }
}
