// @vitest-environment jsdom
//
// writesurface.security.test.ts — the DEMONSTRATIONS behind ready-c6b's
// security review of the browser WRITE surface.
//
// This file proves nothing about how the board is *supposed* to behave; every
// other suite does that. It exists because ready-c6b's done condition 4 refuses
// to accept "reviewed and looks fine" for the two probes that matter most —
// "can a hostile title reach signEvent" and "is the client-side authorization
// check the only gate" — and requires each one demonstrated by execution.
//
// ── HOW THE FINDINGS ARE PINNED, AND WHY NOT WITH `it.fails` ────────────────
//
// Every case below is a plain `it`. The ones carrying an open finding are
// MEASUREMENTS: each asserts, at a line marked `PIN (<item>)`, the exact
// DEFECTIVE value the code produces today, and the comment on that line spells
// out the secure assertion that must replace it once the finding is fixed. So
// the fix's author gets a red test at a named line with the replacement text
// sitting next to it.
//
// THIS FILE USED `it.fails` AND IT WAS WRONG. vitest's `it.fails` passes on ANY
// throw, so it only pins a finding if something independently proves the throw
// happens AT the security assertion. It did not here, and one case was silently
// worthless: the ready-605 signEvent case asserted `tag(signedCard()!, 'title')`
// under `it.fails`, and when the ready-605 fix was applied it stayed GREEN —
// with the read-trust gate on, the hostile card is dropped, resolveGate finds no
// pending gate, nothing reaches the signer, and `signedCard()` is undefined, so
// the `!` threw a TypeError and `it.fails` was satisfied by the wrong throw. The
// security assertion was never reached, before or after the fix. The two cases
// that had an anti-tautology companion survived; the one without a companion was
// the broken one. Measurements do not have that failure mode: an assertion that
// names a concrete value fails at its own line or not at all.
//
// A MEASUREMENT IS NOT AN ENDORSEMENT. Each PIN line is labelled with the item
// that must delete it. Do not "fix" a red PIN by updating it to the new value —
// replace it with the secure assertion in its comment, or the vulnerability
// quietly becomes a requirement, which is the exact failure `it.fails` was
// chosen to avoid.
//
// EACH PIN WAS VERIFIED BY APPLYING THE FIX AND RE-RUNNING (2026-07-30):
// ready-fd9 (publish the status event before the card, in its own publishEvents
// call), ready-951 (a verifyEvent call inside assertSignedAsBuilt), ready-fe5
// (add src/board/ to the guard's scan). Every pin went RED at its marked line,
// and the fixes were reverted. The item text of each names the pin's file, its
// PIN marker and the replacement assertion, so a fixer arriving from the item
// lands on the line rather than hunting for a test that used to exist.
//
// A PIN GOING RED IS THE FIX SUCCEEDING. None of these findings can be closed
// with the suite untouched: the pin lines below MUST be replaced, by the fixer,
// with the secure assertions quoted beside them. "…and the suite is still
// green" is not a satisfiable done condition for any of them.
//
// ready-605 IS FIXED and its two cases below are plain `it` asserting the
// SECURE property — they are no longer pins and must never become defective
// measurements again. They were not simply flipped from `it.fails`, and the
// reason is worth keeping: `it.fails` passes on ANY throw, so the second one
// stayed GREEN under the fix for the wrong reason — the hostile card is
// dropped, resolveGate finds no pending gate, nothing reaches the signer, and
// `tag(signedCard()!, "title")` threw a TypeError on `undefined`. Its
// replacement performs a write that DOES reach the extension and asserts what
// the extension was handed, so "nothing was signed at all" can no longer
// satisfy it. Both were proven by reverting the fix (measured 2026-07-30: both
// go red, see ready-605).
//
// ── THE ASSESSMENT (ready-c6b done condition 1), probe by probe ─────────────
//
// 1. CAN THE PAGE BE INDUCED TO SIGN AN EVENT THE USER DID NOT INTEND? YES, two
//    ways, both demonstrated below.
//    (a) STRUCTURAL, and permanent: a card write REBUILDS THE WHOLE CARD from
//        the projected item (writeevents.ts buildCardEvent). The human approving
//        a gate is approving a RULING; the page re-signs the item's title,
//        context and labels verbatim under their key. Anyone who can put text on
//        the board — including a legitimately granted contributor — chooses what
//        the approver signs. Witnessed by the first case below. Not filed as a
//        defect on its own: it is what "republish the addressable card" means,
//        and the read side cannot tell the two apart.
//    (b) AUTHORIZATION, and a defect: because the browser fold was handed
//        `trusted: null`, "anyone who can put text on the board" was literally
//        anyone with a keypair. -> ready-605 (P0), FIXED: main.ts and
//        nostrwriter.ts now both project under the grant-derived read-trust set,
//        so (a) is bounded to keys the board owner actually granted.
//    A malicious board HOST is out of scope by construction: the host serves the
//    module that calls signEvent, so nothing in the page can defend against it.
//    dist_test.go's no-external-origin scan is what bounds that.
//
// 2. IS THE CLIENT-SIDE AUTHORIZATION CHECK THE ONLY GATE? Against a browser
//    reader, YES — which is why ready-605 had to be fixed rather than parked.
//    - THE RELAY: not a gate rd holds. Measured 2026-07-30 —
//      ws://192.168.2.40:7777 and .41:7777 accept a never-granted key; only
//      wss://relay.3dl.network refuses, under a third-party tenant policy. The
//      product says otherwise in a user-facing sentence -> ready-345.
//    - THE READ-SIDE TRUST GATE: real in rd (cmd/rd/nostr.go:991 ->
//      pkg/sync/nostrproject.go:262, proven by pkg/sync/nostrtrust_test.go's
//      TestProjection_TrustGate_DropsUntrustedTakeover), and — ready-605 — now
//      real in the browser too: main.ts loadBoardItems and nostrwriter.ts
//      items() both project under the key set of deriveLevels over the board's
//      owner-signed 39301 grants, the same membership Go's DeriveReadTrust
//      returns. FOUR cases hold it there, two per production call site: one pair
//      that the ungranted key is DROPPED, and one pair — see "THE DISCRIMINATING
//      CASES" — that the cap-valid grantee is ADMITTED. The second pair is not
//      redundant: without it an EMPTY set passes everything, because fold.ts's
//      §3.4 re-derives grants from the folded snapshot and `opts.trusted`'s
//      CONTENT never gets to decide anything.
//
// 3. DOES KEY MATERIAL REACH STORAGE, A URL, OR A LOG? On the write surface, no.
//    The SealEnvelope {cek, epoch, ltk} lives only in NostrWriterDeps for the
//    tab's lifetime. No shipped module names localStorage / sessionStorage /
//    indexedDB / openDatabase / document.cookie (grepped 2026-07-30), and the
//    only console.* in src/board/ is confwrite.emit.ts's usage line, which is
//    off-page tooling. The URL fragment carries a CEK by DESIGN for
//    `rd board --with-key` (fragment.ts's header), NOT by accident, and that
//    session cannot sign; the LTK — the only WRITE-side key — is deliberately
//    not emitted into any link. The fragment is stripped via history.replaceState
//    in a `finally` (fragment.ts:415-434). PROBED CLEAN, with one guard defect:
//    the structural scan that is supposed to keep it that way does not read
//    src/board/ at all -> ready-fe5.
//
// 4. XSS. PROBED CLEAN, demonstrated rather than grepped: the hostile title,
//    context and label below are rendered through the shipped
//    mountBoardWorkspace into a real DOM and arrive as characters, not nodes.
//    No shipped module uses innerHTML / insertAdjacentHTML / document.write /
//    createContextualFragment / eval / new Function; render.ts builds every node
//    with createElement + textContent, and the only style values it sets come
//    from a fixed five-colour palette (toneForProject), never from item text.
//    The board ships no CSP meta tag (index.html) — noted as hardening, not as
//    a finding, since there is no injection sink to constrain.
//
// 5. OPTIMISTIC UI. Two defects, both demonstrated: a partially-accepted publish
//    is reported as a total refusal while the accepted card has already cleared
//    the gate for every other reader -> ready-fd9; and a badly-signed event
//    passes every check the page makes and is reported as success -> ready-951.
//    Both are made permanent by the same gap: NostrWriterDeps.onApplied has no
//    production caller and render.ts's setItems() is never called outside tests,
//    so nothing ever reconciles the optimistic patch against a re-read.
//
// 6. REPLAY. A card commits to its board (`a`) and its item (`d`) inside the
//    signed event id, so it cannot be retargeted to either. Status events are
//    NOT board-pinned — fold.ts §3.8 and pkg/sync/nostrproject.go:289 both pin
//    CARDS ONLY — so a signed status event could in principle be replayed into
//    another board's projection, bounded by needing a colliding item id AND the
//    author being status-authoritative there. Both implementations agree, so it
//    is a pre-existing fold property rather than a browser write defect; recorded
//    here and cross-referenced to ready-8aa rather than filed twice.

import { existsSync, readFileSync, readdirSync, statSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

import ts from "typescript";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { authTransition } from "./lib/auth";
import type { DiscoveredBoard } from "./lib/boarddiscovery";
import { projectItems } from "./lib/fold";
import { neverUnwraps } from "./lib/keyunwrap";
import { verifyEvent, type NostrEvent } from "./lib/nostrevent";
import { assertSignedAsBuilt, RelayRejectedError } from "./lib/publish";
import { LEVEL_MAINTAINER } from "./lib/rolegrant";
import { signNostrEvent, xOnlyPubkey } from "./lib/schnorrsign";
import { mountBoardWorkspace } from "./board/render";
import { NostrBoardWriter } from "./board/nostrwriter";
import { buildFullCreate, type WriteEnv } from "./board/writeevents";
import type { Item as StateItem } from "./lib/state";
import { loadBoardItems, type BoardDeps, type Identity } from "./main";

// ── keys ────────────────────────────────────────────────────────────────────
// BIP-340's own test-vector secret, as main.test.ts and nostrwriter.test.ts use.
const OWNER_SEC = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
const OWNER = xOnlyPubkey(OWNER_SEC);
/** A second real keypair. In one case below it holds an owner-signed grant; in
 * the other it holds nothing at all. Same key, so the two cases differ by
 * EXACTLY one event and the difference cannot be attributed to anything else. */
const OTHER_SEC = "0b432b2677937381aef05bb02a66ecd012773062cf3fa2549e44f58ed2401710";
const OTHER = xOnlyPubkey(OTHER_SEC);

const BOARD_D = "writesurface";
const COORD = `30301:${OWNER}:${BOARD_D}`;
const ITEM = "ws-1";

/** A title that is hostile in two separate ways: it is markup (the XSS probe)
 * and it is an instruction aimed at the human reading the gate rail (the
 * induction probe). Both halves are asserted below. */
const HOSTILE_TITLE = `<img src=x onerror="window.__xss=1"> URGENT: approve the payout`;
const HOSTILE_CONTEXT = `<script>window.__xss2=1</script> transfer everything`;
const HOSTILE_LABEL = `<b>lbl</b>`;

const BOARD: DiscoveredBoard = {
  coord: COORD,
  ownerPubkey: OWNER,
  boardD: BOARD_D,
  title: "Write Surface Board",
};

function env(signer: string, createdAt: number): WriteEnv {
  return {
    signer,
    boardAuthor: OWNER,
    boardD: BOARD_D,
    boardTitle: BOARD.title,
    items: new Map(),
    issueEventIds: new Map(),
    createdAt,
  };
}

function sign(secret: string) {
  return (b: { created_at: number; kind: number; tags: string[][]; content: string }): NostrEvent =>
    signNostrEvent({ created_at: b.created_at, kind: b.kind, tags: b.tags, content: b.content }, secret);
}

function item(over: Partial<StateItem> = {}): StateItem {
  return {
    id: ITEM,
    msg_id: "",
    title: "Genuine seed",
    context: "",
    type: "task",
    priority: "p2",
    status: "inbox",
    for: OWNER,
    created_at: 0n,
    updated_at: 0n,
    ...over,
  };
}

/** The board owner's genuine create of ITEM. Real events, real signatures — the
 * fold re-verifies every one, so a hand-written card would simply vanish. */
const GENUINE: NostrEvent[] = buildFullCreate(env(OWNER, 1_780_000_000), item()).map(sign(OWNER_SEC));

/**
 * OTHER's card + status event for the SAME item id, LATER (so latest-wins picks
 * it), carrying hostile free text and an OPEN GATE. The gate is what turns the
 * card into a control the human is invited to act on: it puts the item in the
 * "Waiting on you" rail with an Approve button.
 */
const HOSTILE: NostrEvent[] = buildFullCreate(
  env(OTHER, 1_790_000_000),
  item({
    title: HOSTILE_TITLE,
    context: HOSTILE_CONTEXT,
    labels: [HOSTILE_LABEL],
    status: "waiting",
    gate: "budget",
    waiting_type: "gate",
    waiting_on: "the owner",
  }),
).map(sign(OTHER_SEC));

/** An owner-signed kind-39301 admitting OTHER as a contributor on this board.
 * Present in the GRANTED case, absent in the UNGRANTED one. */
const GRANT: NostrEvent = sign(OWNER_SEC)({
  created_at: 1_785_000_000,
  kind: 39301,
  tags: [
    ["d", `${BOARD_D}:${OTHER}`],
    ["p", OTHER],
    ["role", "contributor"],
    ["a", COORD],
  ],
  content: "",
});

/** The SAME contributor key, the SAME grant, but an ordinary BENIGN edit of the
 * same item — later than GENUINE, so it wins latest-wins wherever it is
 * admitted. Used by the discriminating cases below, where the grant lives ONLY
 * in the authority snapshot: a set that fails to carry cap-valid grantees drops
 * this card, and the failure is a legitimate contributor's work vanishing. */
const CONTRIB_TITLE = "contributor edit";
const CONTRIB_CONTEXT = "written by the granted contributor";
const CONTRIB: NostrEvent[] = buildFullCreate(
  env(OTHER, 1_790_000_000),
  item({ title: CONTRIB_TITLE, context: CONTRIB_CONTEXT }),
).map(sign(OTHER_SEC));

const BOARD_EVENT: NostrEvent = GENUINE.find((e) => e.kind === 30301)!;

function identity(method: "extension" | "readOnly", pubkey = OWNER): Identity {
  return { pubkey, auth: authTransition({ type: "login", method }) };
}

/** Runs the REAL loadBoardItems — the production composition — over a scripted
 * relay snapshot. Only the transport is faked. */
async function load(snapshot: NostrEvent[], authority: NostrEvent[]) {
  const deps: BoardDeps = {
    loadRelays: async () => [],
    fetchEvents: async () => snapshot,
    keyUnwrapper: () => neverUnwraps,
  };
  const out = await loadBoardItems([BOARD], [], authority, identity("extension"), deps, () => {});
  return { ...out, writer: out.writers.get(COORD)! };
}

describe("ready-c6b — WRITE SURFACE: what a hostile card can make the page sign", () => {
  let signEvent: ReturnType<typeof vi.fn>;
  let origNostr: Window["nostr"];

  beforeEach(() => {
    origNostr = window.nostr;
    signEvent = vi.fn(async (e: { created_at: number; kind: number; tags: string[][]; content: string }) =>
      sign(OWNER_SEC)(e),
    );
    window.nostr = { getPublicKey: async () => OWNER, signEvent } as unknown as Window["nostr"];
  });

  afterEach(() => {
    window.nostr = origNostr;
    delete (window as unknown as Record<string, unknown>).__xss;
  });

  /** The 30302 card among the events handed to the extension, or undefined. */
  function signedCard(): { created_at: number; kind: number; tags: string[][]; content: string } | undefined {
    for (const call of signEvent.mock.calls) {
      const e = call[0] as { kind: number; tags: string[][]; content: string; created_at: number };
      if (e.kind === 30302) return e;
    }
    return undefined;
  }

  function tag(e: { tags: string[][] }, name: string): string | undefined {
    return e.tags.find((t) => t[0] === name)?.[1];
  }

  // ── PROBE: can the page be induced to sign an event the user did not intend?
  //
  // This case uses a GRANTED contributor, so it is not about the trust gate: it
  // is the permanent shape of the write path. A card write REBUILDS THE WHOLE
  // CARD from the projected item (writeevents.ts buildCardEvent), and the human
  // approving a gate is approving a RULING, not a document. Everything the card
  // carried — title, context, labels — is re-signed under the approver's key.
  it("a granted contributor's title and context are re-signed VERBATIM under the OWNER's key when the owner approves the gate", async () => {
    const { items, writer } = await load([BOARD_EVENT, ...GENUINE, ...HOSTILE, GRANT], [BOARD_EVENT, GRANT]);

    const shown = items.find((i) => i.id === ITEM)!;
    expect(shown.title, "the contributor's card is what the owner is looking at").toBe(HOSTILE_TITLE);
    expect(writer.whyReadOnly(), "the owner must be able to write, or this proves nothing").toBeUndefined();

    // The owner rules on the gate. relays: [] means publishEvents refuses AFTER
    // signing, which is exactly the boundary this probe needs to observe.
    await expect(writer.resolveGate(ITEM, true, "approved")).rejects.toBeInstanceOf(RelayRejectedError);

    const card = signedCard();
    expect(card, "the approval reached the signer").toBeDefined();
    expect(tag(card!, "title")).toBe(HOSTILE_TITLE);
    expect(card!.content).toBe(HOSTILE_CONTEXT);
    expect(card!.tags.some((t) => t[0] === "l" && t[1] === HOSTILE_LABEL)).toBe(true);

    // And it is signed AS THE OWNER: the published card is authentic, so every
    // reader — rd included — attributes the contributor's text to the owner.
    const out = await signEvent.mock.results[0].value;
    expect((out as NostrEvent).pubkey).toBe(OWNER);
  });

  // ── PROBE: is the client-side authorization check the only gate? ──────────
  //
  // Identical snapshot to the case above with the GRANT REMOVED — one event's
  // difference, same keys, so nothing else can explain a different outcome.
  // OTHER now holds no authority on this board whatsoever. The Go fold drops
  // exactly this shape (pkg/sync/nostrtrust_test.go,
  // TestProjection_TrustGate_DropsUntrustedTakeover) and, since ready-605, so
  // does the browser: main.ts folds under the grant-derived read-trust set.
  it("SECURITY (ready-605): an UNGRANTED key's card does not win the browser's projection", async () => {
    const { items } = await load([BOARD_EVENT, ...GENUINE, ...HOSTILE], [BOARD_EVENT]);
    const shown = items.find((i) => i.id === ITEM)!;
    expect(shown.title).toBe("Genuine seed");
    // Every field the hostile card carried is gone, not just the title: it lost
    // latest-wins entirely rather than being partly merged.
    expect(shown.context ?? "").not.toContain("transfer everything");
    expect(shown.labels ?? []).not.toContain(HOSTILE_LABEL);
    // And the gate it planted — the control that invites the human to click —
    // is not in the projection at all.
    expect(shown.gate).toBeUndefined();
    expect(shown.status).toBe("inbox");
  });

  /**
   * THE WRITE HALF, and the reason this case is not simply the one above with
   * `signEvent` bolted on. What a write is BUILT FROM is the WRITER's own
   * projection (nostrwriter.ts items()), which is a second, independent call
   * site — ready-191 shipped a hole of exactly that shape, where the writer's
   * projection and the page's disagreed. So this asserts the writer's map
   * directly, and then asserts what the extension was actually handed.
   *
   * IT DELIBERATELY PERFORMS A WRITE THAT SUCCEEDS IN REACHING THE SIGNER. An
   * earlier revision asserted only `tag(signedCard()!, "title") !== HOSTILE` —
   * which, once the gate drops the hostile card, is satisfied by a TypeError on
   * `undefined` because resolveGate finds no pending gate and NOTHING is ever
   * signed. That version passes with the trust gate on AND with it replaced by
   * any other error. The claim below is a legitimate write the owner is entitled
   * to make, so a card genuinely reaches window.nostr.signEvent, and the
   * assertion is about its CONTENT: the only way to satisfy it is for the fold
   * to have dropped the ungranted card before the card was rebuilt.
   */
  it("SECURITY (ready-605): an UNGRANTED key's content never reaches signEvent — the write the owner DOES make carries the genuine card", async () => {
    const { writer } = await load([BOARD_EVENT, ...GENUINE, ...HOSTILE], [BOARD_EVENT]);

    // 1. The writer's own projection — what buildWrite rebuilds cards from —
    //    dropped it too.
    const projected = writer.items().get(ITEM)!;
    expect(projected.title).toBe("Genuine seed");
    expect(projected.context ?? "").not.toContain("transfer everything");
    expect(projected.gate).toBeUndefined();

    // 2. So the gate the hostile card planted cannot be ruled on: there is
    //    nothing pending, and the refusal happens BEFORE the signer.
    await expect(writer.resolveGate(ITEM, true, "approved")).rejects.toThrow();
    expect(signEvent, "the phantom gate never reached the extension").not.toHaveBeenCalled();

    // 3. A write the owner IS entitled to make. `relays: []` means publishEvents
    //    refuses AFTER signing — the boundary this probe needs to observe.
    await expect(writer.claim(ITEM)).rejects.toBeInstanceOf(RelayRejectedError);

    const card = signedCard();
    expect(card, "a card really did reach the extension — otherwise this proves nothing").toBeDefined();
    expect(tag(card!, "title")).toBe("Genuine seed");
    expect(card!.content).not.toContain("transfer everything");
    expect(card!.tags.some((t) => t[0] === "l" && t[1] === HOSTILE_LABEL)).toBe(false);

    // 4. Nothing the extension was handed, of any kind, carries the hostile text.
    for (const call of signEvent.mock.calls) {
      const e = call[0] as { tags: string[][]; content: string };
      expect(JSON.stringify([e.tags, e.content])).not.toContain("URGENT: approve the payout");
    }
  });

  /**
   * ANTI-TAUTOLOGY for the two above, at the FOLD rather than through the page:
   * the same events and the same fold, the only difference being whether
   * `trusted` is a set or null. It is what identifies the trust gate — and
   * nothing else in the composition — as the thing doing the dropping, and the
   * `trusted: null` half is the exploit as it was live at ready.3dl.dev/board
   * before ready-605. Production now passes the set; the null half stays here as
   * the counterfactual, and it is the reason the two cases above cannot be
   * satisfied by an unrelated refusal somewhere else in loadBoardItems.
   */
  it("ANTI-TAUTOLOGY: the very same events, folded WITH a read-trust set, drop the ungranted card", () => {
    const projected = projectItems([BOARD_EVENT, ...GENUINE, ...HOSTILE], {
      trusted: new Set([OWNER]),
      maintainers: null,
      pinnedBoard: COORD,
      decryptor: null,
      encryptedBoards: null,
    });
    expect(projected.get(ITEM)!.title).toBe("Genuine seed");

    // …and with `trusted: null` — what production passed until ready-605, and
    // what it must never pass again — it does not.
    const ungated = projectItems([BOARD_EVENT, ...GENUINE, ...HOSTILE], {
      trusted: null,
      maintainers: null,
      pinnedBoard: COORD,
      decryptor: null,
      encryptedBoards: null,
    });
    expect(ungated.get(ITEM)!.title).toBe(HOSTILE_TITLE);
  });

  /**
   * ── THE DISCRIMINATING CASES, and why the three above are not enough ───────
   *
   * MEASURED, not argued: replace BOTH production sites with
   * `trusted: new Set<string>()` — an EMPTY set — and every case above stays
   * GREEN. They pin `trusted !== null` and nothing else; the SET'S CONTENT is
   * invisible to them. The cause is fold.ts's §3.4:
   *
   *     if (!trusts(opts, e.pubkey) && !grantTrusts(levels, e.pubkey)) continue;
   *
   * `levels` is the fold's OWN deriveLevels over the events IT IS FOLDING, and
   * every fixture above puts the 39301 grant INSIDE the folded snapshot. So the
   * fold re-derives the contributor's authority for itself and `opts.trusted`
   * never decides anything — leaving ready-605's second requirement ("whatever
   * set is chosen must at minimum admit the board author and every cap-valid
   * grantee") with no falsifiable test, and main.ts's headline claim that one
   * derivation feeds both the fold and the writer equally unpinned.
   *
   * THESE TWO CASES PUT THE OWNER-SIGNED GRANT IN `authorityEvents` AND NOT IN
   * THE FETCHED SNAPSHOT. That is not a contrivance: it is the portfolio /
   * own-boards shape in main(), where authority comes from a kind-only paged REQ
   * ({ kinds: AUTHORITY_KINDS }) and each board's cards from a per-coordinate one
   * ({ kinds: BOARD_KINDS, "#a": [coord] }) — genuinely different event sets, and
   * a grant paged out of the second one is an ordinary outcome. In that shape the
   * ONLY thing that can admit the contributor is the set main.ts derived from the
   * authority snapshot, so an empty set (or one that dropped grantees, or one
   * derived from the wrong snapshot) is now RED. Measured on this branch: shipped
   * set -> "contributor edit"; empty set -> "Genuine seed".
   *
   * One case per CALL SITE, so a regression is attributable: reverting main.ts
   * alone reds the first, reverting nostrwriter.ts alone reds the second.
   */
  it("SECURITY (ready-605): the read-trust set ADMITS a cap-valid grantee whose grant is in the AUTHORITY snapshot only — main.ts's site", async () => {
    // ANTI-TAUTOLOGY, at the fold: the snapshot alone does NOT carry the
    // contributor's authority. Folded with an empty set — the fold's own
    // deriveLevels being all that is left — the edit is dropped. So anything
    // that admits it below came from `authorityEvents` via opts.trusted, which
    // is the exact thing under test.
    const snapshot = [BOARD_EVENT, ...GENUINE, ...CONTRIB];
    const withoutAuthority = projectItems(snapshot, {
      trusted: new Set<string>(),
      maintainers: null,
      pinnedBoard: COORD,
      decryptor: null,
      encryptedBoards: null,
    });
    expect(withoutAuthority.get(ITEM)!.title, "the grant is genuinely absent from the folded events").toBe(
      "Genuine seed",
    );

    const { items } = await load(snapshot, [BOARD_EVENT, GRANT]);
    const shown = items.find((i) => i.id === ITEM)!;
    expect(shown.title, "the granted contributor's edit must survive the gate").toBe(CONTRIB_TITLE);
    expect(shown.context ?? "").toBe(CONTRIB_CONTEXT);
  });

  it("SECURITY (ready-605): the WRITER projects under that same set, so the contributor's edit is what a write is rebuilt from — nostrwriter.ts's site", async () => {
    const { writer } = await load([BOARD_EVENT, ...GENUINE, ...CONTRIB], [BOARD_EVENT, GRANT]);

    // The writer's snapshot is the SAME grant-less event set; its only route to
    // the contributor's authority is grantLevels, which main.ts filled from the
    // authority snapshot. A writer projecting under an empty set shows the stale
    // card here — and would then rebuild every write from it.
    const projected = writer.items().get(ITEM)!;
    expect(projected.title, "the writer must see the same board the page does").toBe(CONTRIB_TITLE);
    expect(projected.context ?? "").toBe(CONTRIB_CONTEXT);

    // And it is what actually reaches the signer: buildCardEvent rebuilds the
    // whole card from this projection, so a dropped-grantee set would silently
    // republish the contributor's work as the pre-edit version under the owner's
    // key. `relays: []` refuses AFTER signing, which is the boundary to observe.
    await expect(writer.claim(ITEM)).rejects.toBeInstanceOf(RelayRejectedError);
    const card = signedCard();
    expect(card, "a card really did reach the extension — otherwise this proves nothing").toBeDefined();
    expect(tag(card!, "title")).toBe(CONTRIB_TITLE);
    expect(card!.content).toBe(CONTRIB_CONTEXT);
  });

  // ── PROBE: XSS ────────────────────────────────────────────────────────────
  //
  // The hostile title/context/label are RENDERED here, into a real DOM, through
  // the shipped mountBoardWorkspace. The claim being tested is not "no innerHTML
  // appears in the source" (that is a grep) but "the markup does not become
  // markup".
  it("a hostile title, context and label reach the DOM as TEXT — no element, no script, no attribute", async () => {
    const { items, writer } = await load([BOARD_EVENT, ...GENUINE, ...HOSTILE, GRANT], [BOARD_EVENT, GRANT]);
    const root = document.createElement("div");
    document.body.append(root);
    const ws = mountBoardWorkspace(root, items, { writer, viewerId: OWNER, boards: [{ coord: COORD, title: BOARD.title }] });
    ws.selectItem(ITEM);

    // The markup is present as characters…
    expect(root.textContent).toContain(HOSTILE_TITLE);
    // …and as no nodes at all.
    expect(root.querySelector("img")).toBeNull();
    expect(root.querySelector("script")).toBeNull();
    // The header tally legitimately contains <b> elements, so the assertion is
    // about THIS text, not about the tag: no element anywhere in the tree was
    // created FROM the label.
    expect([...root.querySelectorAll("b")].map((n) => n.textContent)).not.toContain("lbl");
    expect(root.innerHTML, "the label survives as escaped characters").toContain("&lt;b&gt;lbl&lt;/b&gt;");
    expect((window as unknown as Record<string, unknown>).__xss).toBeUndefined();

    ws.destroy();
    root.remove();
  });
});

// ── PROBE: optimistic UI vs. what the relay actually accepted ───────────────
//
// Resolving a gate is TWO events sent on ONE socket (the refreshed card, then
// the kind-163x status event). publishEvents (lib/publish.ts) requires EVERY
// event to be accepted somewhere and throws otherwise; render.ts's optimistic()
// then reverts the card and shows the relay's words. But the card was already
// sent and already accepted — and the card is where the GATE FIELDS live
// (`gate`, `waiting_type`, `waiting_on` are card tags, read back by
// fold.ts's itemFromCard). So the approver is told the ruling was refused while
// every other reader's copy of the item has lost its pending gate.
//
// This is not a hypothetical relay: a socket that drops after the first OK
// produces exactly this (publishToRelay's `finish` marks the un-answered events
// rejected and keeps the answered ones).
describe("ready-c6b — WRITE SURFACE: a partially-accepted publish", () => {
  const RELAY = "wss://relay.example/";

  /** The owner's genuine create of ITEM with a PENDING GATE — the thing a human
   * is asked to rule on. */
  const GATED: NostrEvent[] = buildFullCreate(
    env(OWNER, 1_780_000_000),
    item({ status: "waiting", gate: "budget", waiting_type: "gate", waiting_on: "the owner" }),
  ).map(sign(OWNER_SEC));

  /** A relay that ACKs everything except kind-1630..1633, whose OK never comes —
   * the shape of a connection dropped mid-batch, or a per-kind write policy. */
  function partialRelay(seen: NostrEvent[]): (url: string) => WebSocket {
    return () => {
      const sock: Record<string, unknown> = {
        send(raw: string) {
          const [, ev] = JSON.parse(raw) as [string, NostrEvent];
          if (ev.kind >= 1630 && ev.kind <= 1633) return; // no answer for this one
          seen.push(ev);
          setTimeout(() => {
            (sock.onmessage as (m: { data: string }) => void)?.({
              data: JSON.stringify(["OK", ev.id, true, ""]),
            });
          }, 0);
        },
        close() {},
      };
      setTimeout(() => (sock.onopen as (() => void) | undefined)?.(), 0);
      return sock as unknown as WebSocket;
    };
  }

  function writerWith(socketFactory: (url: string) => WebSocket): { w: NostrBoardWriter } {
    return {
      w: new NostrBoardWriter({
        signerPubkey: OWNER,
        signer: { async signEvent(e) { return sign(OWNER_SEC)(e); } },
        board: { ownerPubkey: OWNER, boardD: BOARD_D, title: BOARD.title },
        relays: [RELAY],
        snapshot: GATED,
        grantLevels: new Map([[OWNER, LEVEL_MAINTAINER]]),
        publishOptions: { socketFactory, timeoutMs: 200 },
      }),
    };
  }

  /** How an independent reader (rd on another machine) sees the board after the
   * relay kept whatever it accepted. Read-trust ON, i.e. the strict fold. */
  function independentReader(accepted: NostrEvent[]) {
    return projectItems([...GATED, ...accepted], {
      trusted: new Set([OWNER]),
      maintainers: null,
      pinnedBoard: COORD,
      decryptor: null,
      encryptedBoards: null,
    });
  }

  it("MEASURED: the card IS accepted, the status event is not, and the write throws — so the UI reverts and tells the user it was refused", async () => {
    const accepted: NostrEvent[] = [];
    const { w } = writerWith(partialRelay(accepted));
    // ANTI-TAUTOLOGY: the gate really is pending, so this is a real approval.
    expect(w.items().get(ITEM)!.gate).toBe("budget");

    await expect(w.resolveGate(ITEM, true, "approved")).rejects.toBeInstanceOf(RelayRejectedError);

    // The user is told the ruling was refused, and the writer's own view agrees.
    expect(w.items().get(ITEM)!.gate).toBe("budget");
    // PIN (ready-fd9) — but a 30302 with the gate fields CLEARED really was
    // accepted by the relay, and it is the card that carries `gate`,
    // `waiting_type` and `waiting_on` (fold.ts itemFromCard).
    // WHEN FIXED, replace these two lines with:
    //   expect(accepted.find((e) => e.kind === 30302), "a refused ruling must leave no card behind").toBeUndefined();
    const card = accepted.find((e) => e.kind === 30302);
    expect(card, "ready-fd9 fixed? swap this for the secure assertion above").toBeDefined();
    expect(card!.tags.some((t) => t[0] === "gate")).toBe(false);
  });

  it("MEASURED (ready-fd9): the approver is told it was refused while every other reader's gate is already CLEARED", async () => {
    const accepted: NostrEvent[] = [];
    const { w } = writerWith(partialRelay(accepted));
    // ANTI-TAUTOLOGY: the gate really is pending before the ruling.
    expect(w.items().get(ITEM)!.gate).toBe("budget");

    // What the approver is told: refused.
    await expect(w.resolveGate(ITEM, true, "approved")).rejects.toBeInstanceOf(RelayRejectedError);
    expect(w.items().get(ITEM)!.gate, "the writer's own view agrees with what the user was told").toBe("budget");

    const reader = independentReader(accepted);
    // ANTI-VACUITY: the item exists in the independent reader's projection —
    // this measures a CLEARED gate, not an absent item.
    expect(reader.get(ITEM), "the item must project for the reader, or this measures nothing").toBeDefined();

    // PIN (ready-fd9) — the accepted card already cleared the gate for everyone
    // except the person who was told the write did not happen.
    // WHEN FIXED, replace this line with:
    //   expect(reader.get(ITEM)!.gate, "a refused ruling must leave the gate pending for every reader").toBe("budget");
    expect(reader.get(ITEM)!.gate, "ready-fd9 fixed? swap this for the secure assertion above").toBeUndefined();
  });

  // ── PROBE: what the page checks about what the extension handed back ──────
  //
  // publish.ts's assertSignedAsBuilt is explicitly built on "the signer is not
  // trusted to return what it was given": it re-derives the event id, compares
  // the pubkey, and compares created_at. It never checks the ONE field that
  // makes the event authentic — the schnorr signature — even though the page
  // ships a BIP-340 verifier it already runs on every event it READS
  // (lib/nostrevent.ts verifyEvent, lib/secp256k1.ts).
  //
  // The consequence is not academic. Every downstream reader drops an event
  // whose signature does not verify, so the write is a silent no-op; the page
  // reports success and its optimistic patch stands, because nothing ever
  // re-reads (NostrWriterDeps.onApplied has no production caller).
  const okRelay: (url: string) => WebSocket = () => {
    const sock: Record<string, unknown> = {
      send(raw: string) {
        const [, ev] = JSON.parse(raw) as [string, NostrEvent];
        setTimeout(() => {
          (sock.onmessage as (m: { data: string }) => void)?.({ data: JSON.stringify(["OK", ev.id, true, ""]) });
        }, 0);
      },
      close() {},
    };
    setTimeout(() => (sock.onopen as (() => void) | undefined)?.(), 0);
    return sock as unknown as WebSocket;
  };

  /** An extension that returns the event it was asked to sign — correct id,
   * correct pubkey, correct created_at — with a WRONG signature. */
  function badSigSigner() {
    return {
      async signEvent(e: { created_at: number; kind: number; tags: string[][]; content: string }) {
        const good = sign(OWNER_SEC)(e);
        return { ...good, sig: "00" + good.sig.slice(2) };
      },
    };
  }

  it("MEASURED: a badly-signed event passes every check the page makes and is published as a success", async () => {
    const seen: NostrEvent[] = [];
    const w = new NostrBoardWriter({
      signerPubkey: OWNER,
      signer: badSigSigner(),
      board: { ownerPubkey: OWNER, boardD: BOARD_D, title: BOARD.title },
      relays: [RELAY],
      snapshot: GATED,
      grantLevels: new Map([[OWNER, LEVEL_MAINTAINER]]),
      publishOptions: {
        socketFactory: (url) => {
          const s = okRelay(url);
          const orig = s.send.bind(s);
          s.send = (raw: string) => {
            seen.push((JSON.parse(raw) as [string, NostrEvent])[1]);
            orig(raw);
          };
          return s;
        },
        timeoutMs: 200,
      },
    });
    let threw: unknown;
    await w.resolveGate(ITEM, true, "approved").catch((e: unknown) => {
      threw = e;
    });

    // PIN (ready-951) — the page considers this write done.
    // WHEN FIXED, replace this line with:
    //   expect(threw, "a mis-signed write must be refused").toBeInstanceOf(SignerMismatchError);
    expect(threw, "ready-951 fixed? swap this for the secure assertion above").toBeUndefined();

    expect(seen.length, "events really were published").toBeGreaterThan(0);
    // ANTI-TAUTOLOGY: the published events genuinely do not verify — the
    // fixture's corruption is real, not a mislabelled valid signature.
    expect(seen.every((e) => !verifyEvent(e)), "every published event fails BIP-340 verification").toBe(true);
    // …so every reader drops them, and the board never changed.
    expect(independentReader(seen).get(ITEM)!.gate).toBe("budget");
  });

  // The same defect at its own line, one function down from the writer:
  // assertSignedAsBuilt is publish.ts's "the signer is not trusted to return what
  // it was given" check, and the signature is the one field it does not look at.
  it("MEASURED (ready-951): assertSignedAsBuilt accepts an event whose schnorr signature does not verify", () => {
    const built = buildFullCreate(env(OWNER, 1_780_000_000), item()).find((b) => b.kind === 30302)!;
    const good = sign(OWNER_SEC)(built);
    const badlySigned: NostrEvent = { ...good, sig: "00" + good.sig.slice(2) };

    // ANTI-TAUTOLOGY: only the signature differs, and it genuinely does not
    // verify — the id, pubkey and created_at that assertSignedAsBuilt DOES check
    // are all still correct, so nothing else could carry the check.
    expect(badlySigned.id).toBe(built.id);
    expect(badlySigned.pubkey).toBe(built.pubkey);
    expect(badlySigned.created_at).toBe(built.created_at);
    expect(verifyEvent(good), "the uncorrupted event verifies").toBe(true);
    expect(verifyEvent(badlySigned), "the corrupted one does not").toBe(false);

    let threw: unknown;
    try {
      assertSignedAsBuilt(built, badlySigned);
    } catch (e) {
      threw = e;
    }
    // PIN (ready-951) — no throw: the signature is never checked.
    // WHEN FIXED, replace this line with:
    //   expect(threw, "a signature that does not verify must be refused").toBeInstanceOf(SignerMismatchError);
    expect(threw, "ready-951 fixed? swap this for the secure assertion above").toBeUndefined();
  });
});

// ── PROBE (guard defect): does the no-persistence scan cover the write surface?
//
// src/nostorage.test.ts is the STRUCTURAL half of the "no secret material is ever
// written to localStorage/IndexedDB/sessionStorage" claim: it reads every shipped
// module and fails if one so much as names a persistence API. Its own header says
// a module nothing in the suite reaches "could persist a CEK and stay green", and
// that is precisely the hole it has — shippedSources() walks src/ and src/lib/ and
// never src/board/, so all twelve write-surface modules are unscanned, including
// nostrwriter.ts, the one holding the write-side SealEnvelope {cek, epoch, ltk}.
//
// This is the one ready-c6b finding with no executable pin, in a deliverable whose
// premise is demonstration rather than prose. Pinned here rather than fixed there:
// fixing it is a change to another item's guard, and this item is a review.
//
// THE PINS BELOW MEASURE THE GUARD'S OUTPUT, NOT ITS SYNTAX. An earlier revision
// regexed nostorage.test.ts for its two `fileURLToPath(new URL("./lib/", ...))`
// literals and pinned that list. That was wrong in a way worth naming: ready-fe5's
// own done condition offers "walk src/ recursively" as an option, and a recursive
// walk DELETES those literals instead of adding a third — so the pin would have
// stayed green on a correct fix. What follows instead EXECUTES the guard's own
// shippedSources() (lifted out of nostorage.test.ts, TypeScript stripped by the
// tsc the package already depends on, its imports supplied as arguments) and asks
// the only question that matters: is the write surface in the set it returns?
// Extra root, recursive walk, glob — every shape of fix turns these red.
describe("ready-c6b — GUARD: what the no-persistence scan actually reads (ready-fe5)", () => {
  // nostorage.test.ts locates itself with `new URL(..., import.meta.url)`, which
  // this file cannot copy: it runs under jsdom, where import.meta.url is not a
  // file: URL. Walk up from the working directory to the package root instead,
  // and fail loudly rather than silently scanning nothing.
  const srcDir = (() => {
    for (let dir = process.cwd(), i = 0; i < 6; dir = dirname(dir), i++) {
      const cand = join(dir, "src");
      if (existsSync(join(cand, "nostorage.test.ts"))) return cand;
    }
    throw new Error(`cannot locate web/board/src from ${process.cwd()} — this pin would otherwise be vacuous`);
  })();
  const guardPath = join(srcDir, "nostorage.test.ts");
  const guardSrc = readFileSync(guardPath, "utf8");
  const boardDir = join(srcDir, "board");

  type Scanned = { name: string; code: string };

  /**
   * THE GUARD'S OWN shippedSources(), RUN. Everything above the first top-level
   * `describe(` is the guard's scan and the constants it depends on; its imports
   * become parameters, `import.meta.url` becomes the guard's real file URL, and
   * the TypeScript is stripped by the tsc this package already builds with.
   * Nothing is re-implemented here, so this cannot drift into asserting a copy of
   * the scan. If the guard is refactored past what this can lift, it throws with
   * ready-fe5 in the message — loud, never a silent pass.
   */
  function guardScannedSources(): Scanned[] {
    const cut = guardSrc.indexOf("\ndescribe(");
    if (cut < 0) throw new Error("ready-fe5 pin: nostorage.test.ts has no top-level describe() to cut at");
    const prelude = guardSrc
      .slice(0, cut)
      .replace(/^import\s[\s\S]*?;$/gm, "")
      .replace(/^export\s+/gm, "")
      .replace(/import\.meta\.url/g, "__guardUrl");
    if (!prelude.includes("shippedSources")) {
      throw new Error("ready-fe5 pin: shippedSources() is no longer declared above the first describe()");
    }
    const code = ts.transpileModule(prelude, {
      fileName: guardPath,
      compilerOptions: { target: ts.ScriptTarget.ES2022, module: ts.ModuleKind.None },
    }).outputText;
    const run = new Function(
      "readFileSync",
      "readdirSync",
      "existsSync",
      "statSync",
      "fileURLToPath",
      "join",
      "dirname",
      "relative",
      "__guardUrl",
      `${code}\nreturn shippedSources();`,
    ) as (...deps: unknown[]) => Scanned[];
    return run(
      readFileSync,
      readdirSync,
      existsSync,
      statSync,
      fileURLToPath,
      join,
      dirname,
      relative,
      pathToFileURL(guardPath).href,
    );
  }

  /** The longest substantive code line in a module — long enough to be unique to
   * it, and never a comment, so the guard's comment-stripping cannot remove it.
   * Matching on CONTENT rather than on the name the scan happens to assign makes
   * the pins independent of whether a fix reports "nostrwriter.ts",
   * "board/nostrwriter.ts" or an absolute path. */
  function fingerprint(file: string, dir = boardDir): string {
    const best = readFileSync(join(dir, file), "utf8")
      .split("\n")
      .map((l) => l.trimEnd())
      .filter((l) => {
        const t = l.trimStart();
        return t.length >= 40 && !t.startsWith("//") && !t.startsWith("*") && !t.startsWith("/*") && !t.startsWith("import");
      })
      .sort((a, b) => b.length - a.length)[0];
    if (!best) throw new Error(`ready-fe5 pin: no fingerprintable line in ${file}`);
    return best;
  }

  const boardModules = readdirSync(boardDir).filter(
    (f) => f.endsWith(".ts") && !f.includes(".test.") && !f.endsWith(".d.ts"),
  );

  it("MEASURED (ready-fe5): the guard's own scan, executed, does not contain the module holding the write-side keys", () => {
    const scanned = guardScannedSources();

    // ANTI-VACUITY: the guard's scan really ran and really returned modules —
    // the five its own vacuous-pass test names are all in what came back.
    expect(scanned.length, "shippedSources() returned nothing — this pin would be vacuous").toBeGreaterThan(8);
    for (const required of ["main.ts", "lib/keyring.ts", "lib/keyunwrap.ts", "lib/envelope.ts", "lib/carditems.ts"]) {
      const [dir, file] = required.includes("/") ? [join(srcDir, "lib"), required.split("/")[1]] : [srcDir, required];
      expect(
        scanned.filter((s) => s.code.includes(fingerprint(file, dir))).length,
        `POSITIVE CONTROL: ${required} is scanned and content-matching finds it`,
      ).toBe(1);
    }
    // …so a module NOT found below is a module the scan missed, not a module the
    // matching failed on.

    // ANTI-VACUITY: nostrwriter.ts really is where the write-side key material is.
    const writerSrc = readFileSync(join(boardDir, "nostrwriter.ts"), "utf8");
    expect(writerSrc, "nostrwriter.ts holds the write-side SealEnvelope {cek, epoch, ltk}").toContain("SealEnvelope");

    // PIN (ready-fe5) — the module that holds the CEK, the epoch and the LTK is
    // absent from the set the guard reads, so it could persist any of them and
    // the guard would stay green.
    // WHEN FIXED, replace this line with:
    //   expect(scanned.filter((s) => s.code.includes(fingerprint("nostrwriter.ts"))), "the write-side key module must be scanned").toHaveLength(1);
    expect(
      scanned.filter((s) => s.code.includes(fingerprint("nostrwriter.ts"))),
      "ready-fe5 fixed? swap this for the secure assertion above",
    ).toEqual([]);
  });

  it("MEASURED (ready-fe5): and it misses the whole write surface, not just that one module", () => {
    const scanned = guardScannedSources();

    // ANTI-VACUITY: there really is a write surface under src/board/ to miss.
    expect(boardModules.length, "src/board/ holds the write surface").toBeGreaterThan(8);
    expect(boardModules, "the write path itself").toEqual(expect.arrayContaining(["nostrwriter.ts", "writeevents.ts", "write.ts", "render.ts"]));

    const reached = boardModules.filter((m) => scanned.some((s) => s.code.includes(fingerprint(m))));

    // PIN (ready-fe5) — not one of them is reached. A partial fix (one module,
    // one root) also turns this red, at this line, listing what is still missed.
    // WHEN FIXED, replace this line with:
    //   expect(reached, "every shipped src/board/ module must be scanned").toEqual(boardModules);
    expect(reached, "ready-fe5 fixed? swap this for the secure assertion above").toEqual([]);
  });
});
