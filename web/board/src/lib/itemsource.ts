// The seam between verified relay events and the board's Item[] (see
// ../board/types.ts's header comment for why this is a seam and not a
// second fold implementation). ready-35b owns the conformant projection
// (docs/design/board-fold-spec.md, vector gate + live parity gate); this
// module exists so main.ts has exactly one place to wire that projection in
// once it lands, without touching the board UI itself.
//
// KNOWN GAP (tracked, not chased — see ready-445's close-out notes): today
// loadItems always rejects. Until ready-35b lands, the board UI's tests
// exercise ../board/render.ts directly with hand-built Item fixtures (the
// same "hand-author expected state from the spec" method the Go conformance
// vectors use), and the live page shows the empty-board state.
import type { Item } from "../board/types";
import type { NostrEvent } from "./nostrevent";

export class ItemProjectionNotImplementedError extends Error {
  constructor() {
    super(
      "event -> Item[] projection not implemented in the client yet — see ready-35b " +
        "(docs/design/board-fold-spec.md); wire ItemSource.loadItems there, not here",
    );
    this.name = "ItemProjectionNotImplementedError";
  }
}

export interface ItemSource {
  loadItems(events: NostrEvent[]): Item[];
}

export const unimplementedItemSource: ItemSource = {
  loadItems(_events: NostrEvent[]): Item[] {
    throw new ItemProjectionNotImplementedError();
  },
};
