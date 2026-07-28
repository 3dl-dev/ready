// Relay bytes -> renderable board items (ready-c4b). The browser slice of
// pkg/sync/nostrproject.go's ProjectItems: the ORDERED admission gates, the
// latest-wins card projection, and itemFromCard's decrypt-or-placeholder.
//
// Only the part of the fold this page needs is here — no status-event replay, no
// dependency edges, no gate resolution (that projection is ready-445's). What IS
// here is every gate that decides whether a byte from an untrusted relay is
// allowed to become visible text, in the same order the Go fold applies them:
//
//	verify signature -> item id present -> board pin -> confidential fold gate
//	-> latest-wins -> decrypt or placeholder
//
// THE FAIL-CLOSED RULE, stated once because everything below is an instance of
// it: on a confidential card, the ONLY source of a title is the sealed Content.
// The clear `title` tag is not a fallback, is not a hint, and is not read at
// all — pkg/sync/nostrwire.go's BuildCardEvent does not emit it in confidential
// mode, so a confidential card carrying one is an attack shape, not a legacy
// shape. When the sealed Content will not open, the item renders PLACEHOLDER.
// Never ciphertext, never a partial string, never the clear tag, never blank.

import type { NostrEvent } from "./nostrevent";
import { tagValue, verifyEvent } from "./nostrevent";
import type { BoardKeyring } from "./keyring";
import {
  PLACEHOLDER,
  cekEpochOf,
  decodeCardPayload,
  encWellFormed,
  isConfidential,
  openContent,
} from "./envelope";

/** KIND_CARD = 30302, matching pkg/sync/nostrwire.go's KindCard. */
export const KIND_CARD = 30302;

/**
 * BoardItem is one rendered row. `decrypted` is the honest signal the UI styles
 * on: false means the free text is a placeholder, and the UI must SAY so rather
 * than showing an empty cell that reads as "this item has no title".
 */
export interface BoardItem {
  id: string;
  title: string;
  context: string;
  waitingOn: string;
  labels: string[];
  status: string;
  priority: string;
  type: string;
  /** true when this card is sealed (carries an enc marker). */
  confidential: boolean;
  /** true when the sealed free text was successfully opened. Always true for a
   * grandfathered plaintext card; false for every confidential card whose CEK
   * is missing or whose AEAD failed. */
  decrypted: boolean;
  /** The card's cek_epoch, or null for a plaintext card. Surfaced so the UI can
   * explain WHICH epoch a reader is missing. */
  epoch: number | null;
}

/** newerThan is nostrproject.go's latest-wins comparator verbatim: newer
 * created_at wins; on a tie the LOWEST event id wins, so the result does not
 * depend on relay delivery order. */
function newerThan(a: NostrEvent, b: NostrEvent): boolean {
  if (a.created_at !== b.created_at) return a.created_at > b.created_at;
  return a.id < b.id;
}

/**
 * shouldQuarantine is the fail-closed fold gate, ported from
 * pkg/sync/envelope.go's shouldQuarantine.
 *
 * On a confidential board (the keyring knows a cutover for it), a card without a
 * well-formed enc envelope is dropped ENTIRELY — it never becomes an item, so
 * its cleartext title cannot reach the DOM. The single exception is a genuine
 * pre-cutover plaintext card, grandfathered on created_at so the one-time
 * migration to confidential mode does not erase a board's history.
 *
 * A v-shaped card — enc marker present but malformed, unknown version, body too
 * short, or cleartext smuggled into Content behind the marker — is NOT legacy
 * and is NOT grandfathered. That is the `!isConfidential(e)` clause: carrying
 * the marker forfeits the exception.
 *
 * The gate is inert on a plaintext board (cutover === null).
 */
export function shouldQuarantine(e: NostrEvent, cutover: number | null): boolean {
  if (cutover === null) return false;
  if (encWellFormed(e)) return false;
  if (e.created_at < cutover && !isConfidential(e)) return false;
  return true;
}

/**
 * itemFromCard materializes one BoardItem, port of nostrproject.go's
 * itemFromCard for the fields this page renders.
 *
 * The decrypt path takes the placeholder exit for EVERY defect, and they are
 * deliberately indistinguishable to the caller: no epoch key, an epoch that does
 * not parse, an unknown enc version, a tampered ciphertext byte, a tampered
 * Poly1305 tag byte, a truncated envelope, a payload that is not the expected
 * JSON. An AEAD cannot tell "wrong key" from "tampered" and neither should this.
 */
export function itemFromCard(e: NostrEvent, coord: string, keyring: BoardKeyring | null): BoardItem {
  const base = {
    id: tagValue(e, "d"),
    status: tagValue(e, "s"),
    priority: tagValue(e, "priority") || tagValue(e, "rank"),
    type: tagValue(e, "itype"),
  };

  if (!isConfidential(e)) {
    // Plaintext (grandfathered pre-cutover, or a plaintext board). The clear
    // tags ARE the content here — this branch is only ever reached for a card
    // the fold gate admitted.
    return {
      ...base,
      title: tagValue(e, "title"),
      context: e.content,
      waitingOn: tagValue(e, "waiting_on"),
      labels: e.tags.filter((t) => t.length >= 2 && t[0] === "l").map((t) => t[1]),
      confidential: false,
      decrypted: true,
      epoch: null,
    };
  }

  const epoch = cekEpochOf(e);
  const sealed = {
    ...base,
    title: PLACEHOLDER,
    context: "",
    waitingOn: "",
    labels: [] as string[],
    confidential: true,
    decrypted: false,
    epoch,
  };
  if (epoch === null || keyring === null) return sealed;

  const cek = keyring.cek(coord, epoch);
  if (cek === null) return sealed;

  const raw = openContent(cek, e.content);
  if (raw === null) return sealed;

  const payload = decodeCardPayload(raw);
  if (payload === null) return sealed;

  return {
    ...sealed,
    title: payload.title,
    context: payload.context,
    waitingOn: payload.waitingOn,
    labels: payload.labels,
    decrypted: true,
  };
}

export interface ProjectCardsOptions {
  /** The pinned board coordinate. A card whose "a" tag is anything else is
   * dropped: without this, any key forks its own 30301 and publishes cards under
   * it (the parallel-board self-escalation nostrproject.go's board pin blocks). */
  coord: string;
  /** The reader's key material, or null for an identity that holds none (a
   * read-only npub login). Null does not disable the fold gate — a stranger
   * still must not see post-cutover cleartext. */
  keyring: BoardKeyring | null;
}

/**
 * projectCards folds a relay snapshot into the board's items.
 *
 * Gate order matches ProjectItems and is normative — in particular signature
 * verification runs BEFORE anything reads a tag or opens an envelope, so a
 * forged card carrying perfectly valid ciphertext is dropped without ever being
 * decrypted. Duplicate event ids are idempotent (first occurrence wins; the id
 * is a content hash, so copies are byte-identical).
 *
 * Whether the board is confidential is read from the keyring's cutover, which is
 * derived from owner-signed grants — NOT from the cards themselves. A reader
 * holding no CEK still learns the board is confidential and still quarantines
 * post-cutover cleartext.
 */
export function projectCards(events: NostrEvent[], opts: ProjectCardsOptions): BoardItem[] {
  const cutover = opts.keyring?.cutover(opts.coord) ?? null;
  const seen = new Set<string>();
  const winning = new Map<string, NostrEvent>();

  for (const e of events) {
    if (!e || e.kind !== KIND_CARD) continue;
    if (seen.has(e.id)) continue;
    // SECURITY: a relay is untrusted. This runs first, and nothing downstream
    // re-checks it. Removing it is the mutation carditems.test.ts's forged-card
    // case goes red on.
    if (!verifyEvent(e)) continue;
    const itemID = tagValue(e, "d");
    if (itemID === "") continue;
    if (tagValue(e, "a") !== opts.coord) continue;
    if (shouldQuarantine(e, cutover)) continue;

    seen.add(e.id);
    const cur = winning.get(itemID);
    if (!cur || newerThan(e, cur)) winning.set(itemID, e);
  }

  const out = [...winning.values()].map((e) => itemFromCard(e, opts.coord, opts.keyring));
  out.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
  return out;
}
