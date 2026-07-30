// ready-daf, second half — THE BROWSER FOLD NOW MARKS WHAT IT COULD NOT READ.
//
// The read-side placeholder substitution is safe on its own; the
// read-then-REPUBLISH round trip is what destroys data. A card write rebuilds
// the whole latest-wins card from a projected item, so mutating an item whose
// free text this reader could not decrypt re-seals "[encrypted]" as the item's
// content, irreversibly — it destroyed four items on the ready board (ready-76b),
// which is why Go carries state.Item.Redacted and cmd/rd refuses to republish a
// marked item (refuseRedactedRepublish).
//
// The browser fold set the placeholder and carried NO such marker, so a future
// browser write path would have had no in-band way to refuse. This suite pins the
// marker end to end — fold Item, its JSON encoding, and the UI item the write
// path will actually hold — before that write path exists, because after the
// substitution has propagated the information "this reader could not read it" is
// no longer recoverable.
//
// Fixtures are the Go-signed confidential board (confidential.fixtures.ts), so
// "decrypted" and "not decrypted" are real AEAD outcomes, not flags set by the
// test.

import { describe, expect, it } from "vitest";
import { projectItems } from "./fold";
import type { ProjectOptions } from "./fold";
import { encodeItem } from "./state";
import { foldItemSource } from "./itemsource";
import { PLACEHOLDER } from "./envelope";
import type { EncryptedBoardSet, BoardDecryptor } from "./envelope";
import { hexToBytes } from "./sha256";
import {
  BOARD_COORD,
  CEK_EPOCH1,
  CUTOVER,
  boardEvent,
  cards,
  expectedPlaintext,
} from "./confidential.fixtures";

/** A reader holding epoch 1 and NOT epoch 2 — the ordinary post-rotation
 * member, so the same projection produces both outcomes at once. */
const epoch1Only: BoardDecryptor = {
  cek: (coord, epoch) => (coord === BOARD_COORD && epoch === 1 ? hexToBytes(CEK_EPOCH1) : null),
};

/** The board is confidential and the cutover IS established here — this suite is
 * about the marker, not about the ready-daf gate (main.grantswithheld.test.ts). */
const confidentialBoard: EncryptedBoardSet = {
  cutover: (coord) => (coord === BOARD_COORD ? { cutover: CUTOVER, ok: true } : { cutover: 0, ok: false }),
};

const opts: ProjectOptions = {
  trusted: null,
  maintainers: null,
  pinnedBoard: BOARD_COORD,
  decryptor: epoch1Only,
  encryptedBoards: confidentialBoard,
};

const SNAPSHOT = [boardEvent, ...cards];

describe("fold Item.redacted mirrors state.Item.Redacted", () => {
  it("marks the item whose sealed text did NOT open, and leaves the one that did unmarked", () => {
    const byId = projectItems(SNAPSHOT, opts);

    // conf-003 was sealed under epoch 2; this reader holds only epoch 1.
    const sealed = byId.get("conf-003")!;
    expect(sealed.title).toBe(PLACEHOLDER);
    expect(sealed.redacted).toBe(true);

    // conf-001 opened for real — same board, same projection, same call.
    const opened = byId.get("conf-001")!;
    expect(opened.title).toBe(expectedPlaintext[0].title);
    expect(opened.redacted).toBeFalsy();
  });

  it("leaves a plaintext card unmarked — the flag means 'could not read', not 'confidential board'", () => {
    // conf-006 predates the cutover and carries a clear title. A flag that
    // tracked the BOARD rather than this reader's decrypt outcome would mark it,
    // and a write path would then refuse a mutation that is perfectly safe.
    const item = projectItems(SNAPSHOT, opts).get("conf-006")!;
    expect(item.title).toBe("Legacy plaintext card");
    expect(item.redacted).toBeFalsy();
  });

  it("is NOT part of the JSON surface — Go's Redacted is `json:\"-\"`", () => {
    // encodeItem is the single encoder both the vector suite and live-parity go
    // through. Emitting `redacted` there would break byte-for-byte parity with
    // `rd list --json` for every reader who holds no key.
    const sealed = projectItems(SNAPSHOT, opts).get("conf-003")!;
    expect(sealed.redacted).toBe(true);
    expect(Object.keys(encodeItem(sealed))).not.toContain("redacted");
  });
});

describe("the UI item carries the marker too", () => {
  it("threads redacted through to the item a write path would hold", () => {
    // itemsource.toUIItem is the seam between the fold and board/types.ts's
    // Item. board/write.ts is intent-shaped and unimplemented; when it lands,
    // THIS is the field it must refuse on.
    const items = foldItemSource(opts, BOARD_COORD).loadItems(SNAPSHOT);
    const byId = new Map(items.map((i) => [i.id, i]));
    expect(byId.get("conf-003")!.redacted).toBe(true);
    expect(byId.get("conf-001")!.redacted).toBeFalsy();
    expect(byId.get("conf-006")!.redacted).toBeFalsy();
  });
});
