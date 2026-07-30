// The board's WRITE affordances (drag-a-card-between-columns = a status
// write; approving a gate = a gate-resolve write) per ready-445's
// 2026-07-27 spec correction: v1 is full read+write, per the epic's owner
// scope change and the locked design ("drag a card between columns = a
// status write"). This item builds the UI affordance ONLY.
//
// WHAT THIS IS NOT: event construction and publish. That is ready-b2b ("The
// browser writes events rd reads back identically"), ready-186 ("Approving a
// gate in the browser unblocks the work") and ready-4359 ("A change made in
// the browser is visible in rd on another machine"), each behind
// board-fold-spec.md Part II (§16-27) and its own vector suite (ready-bc5) —
// mutation event shapes are a much larger conformance surface than the read
// projection this item builds, and hand-rolling them here would bypass both
// the spec clauses and that vector suite.
//
// SHAPE CHOSEN: a narrow, board-UI-facing interface (move a card's status,
// approve/deny a gate) rather than a generic "publish this event" escape
// hatch — the board only ever needs to express user INTENTS (move this card
// to Moving, approve this gate), never raw event construction; keeping the
// interface intent-shaped means whatever ready-b2b lands underneath it plugs
// in without the board's call sites changing. See decisions in the item's
// close-out summary for the alternative considered and rejected.
import type { Status } from "./types";

export class WriteNotImplementedError extends Error {
  constructor(action: string) {
    super(
      `board write path not implemented: ${action} — event construction and ` +
        `publish belong to ready-b2b/ready-186/ready-4359, behind ` +
        `board-fold-spec.md Part II and the ready-bc5 vector suite`,
    );
    this.name = "WriteNotImplementedError";
  }
}

export interface BoardWriter {
  /** Drag a card from one column to another: request a status change. */
  moveStatus(itemId: string, toStatus: Status): Promise<void>;
  /** Resolve a gate from the detail pane's gate banner. */
  resolveGate(itemId: string, approve: boolean, reason?: string): Promise<void>;
  /** Take the item: status active, assigned to the signing key. */
  claim(itemId: string, reason?: string): Promise<void>;
  /** Close it — resolution is an rd close resolution (done/cancelled/failed). */
  close(itemId: string, resolution?: string, reason?: string): Promise<void>;
  /** Retitle. */
  setTitle(itemId: string, title: string): Promise<void>;
  /** Re-prioritise (p0..p3). */
  setPriority(itemId: string, priority: string): Promise<void>;
  /** Add (present=true) or remove (present=false) one label atom. */
  setLabel(itemId: string, label: string, present: boolean): Promise<void>;
  /** A plain sentence explaining why this identity cannot write, or undefined
   * when it can. The UI states it up front instead of only after a failed
   * drag — a read-only board must SAY it is read-only (ready-b2b's security
   * model: no NIP-07 provider means read-only, said plainly, never a prompt
   * for a secret key). */
  whyReadOnly?(): string | undefined;
}

/** The FALLBACK BoardWriter: what a workspace mounted with no writer gets (a
 * test harness, or a render path that has no board/relay context). The real one
 * is NostrBoardWriter (nostrwriter.ts), wired in main.ts. Every method rejects;
 * callers (the drag handler, the gate banner's approve/deny buttons, the detail
 * pane's actions) MUST catch it and revert whatever optimistic UI change they
 * made — see render.ts's handleDrop, which reverts the card to its prior
 * column and shows the message. */
export const unimplementedWriter: BoardWriter = {
  moveStatus(_itemId: string, _toStatus: Status): Promise<void> {
    return Promise.reject(new WriteNotImplementedError(`moveStatus(${_itemId} -> ${_toStatus})`));
  },
  resolveGate(_itemId: string, _approve: boolean): Promise<void> {
    return Promise.reject(new WriteNotImplementedError(`resolveGate(${_itemId})`));
  },
  claim(_itemId: string): Promise<void> {
    return Promise.reject(new WriteNotImplementedError(`claim(${_itemId})`));
  },
  close(_itemId: string, _resolution?: string): Promise<void> {
    return Promise.reject(new WriteNotImplementedError(`close(${_itemId})`));
  },
  setTitle(_itemId: string, _title: string): Promise<void> {
    return Promise.reject(new WriteNotImplementedError(`setTitle(${_itemId})`));
  },
  setPriority(_itemId: string, _priority: string): Promise<void> {
    return Promise.reject(new WriteNotImplementedError(`setPriority(${_itemId})`));
  },
  setLabel(_itemId: string, _label: string, _present: boolean): Promise<void> {
    return Promise.reject(new WriteNotImplementedError(`setLabel(${_itemId})`));
  },
};
