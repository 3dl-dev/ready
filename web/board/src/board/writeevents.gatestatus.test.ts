// writeevents.gatestatus.test.ts — ready-e51 (veracity audit of the M2 wave).
//
// THE GUARD. buildWrite's gate_approve/gate_reject branch refuses an item whose
// status is not `waiting` or `blocked`, mirroring rd's §9.2 (`Status ∈ {waiting,
// blocked}` — widened by ready-e0e, because the ruling is usually what unblocks
// the chain). Deleting it — `if (false) {` in place of the status comparison —
// left 837/837 vitest green on this tree.
//
// THIS FILE IS THE REMAINDER, NOT THE PROOF. The proof lives in
// testdata/write.vectors.json, the file cmd/rd/writevectors_test.go and
// web/board/src/board/writeevents.vectors.test.ts BOTH replay, so rd and the
// browser are held to one artifact instead of two hand-written tables that can
// drift. Ten of the sixteen (status × verb) rows are expressed there:
//
//   waiting  + approve/reject → gate_approve, gate_reject_keeps_gate_open
//   blocked  + approve/reject → gate_approve_on_blocked_and_gated_item_never_authors_blocked
//                               gate_reject_on_blocked_and_gated_item_never_authors_blocked
//   done/cancelled/failed × approve/reject
//                            → gate_{approve,reject}_refuses_a_{done,cancelled,failed}_item_whose_gate_tag_survived
//
// The terminal six reach the STATUS clause rather than the no-pending-gate
// clause above it because §9.5 clears waiting_type/waiting_on/waiting_since/
// gate_msg_id on a terminal item but NOT `gate` — measured, not assumed: with
// the TS fold additionally clearing `gate` there, all six fail with "has no
// pending gate" instead of "is not waiting or blocked". So the vectors referee
// §9.5 across the two folds as well as the status clause across the two writers.
//
// WHAT REMAINS HERE, AND EXACTLY WHY IT CANNOT BE A VECTOR. Three rows —
// inbox, active, scheduled — times two verbs. A vector's seed is published as a
// real card and then FOLDED before the write path sees it, on both sides
// (cmd/rd's runApproveNostr → nostrResolveItem → pkg/sync.ProjectItems; the
// browser harness's envFor → lib/fold.ts's projectItems). §9.4 promotes any
// non-terminal, non-blocked item that declares a gate to `waiting`. So a vector
// seeded at inbox/active/scheduled with a live gate does not arrive at the
// write path in that status at all — it arrives as `waiting` and is ADMITTED.
// Measured on rd, not asserted: seeding each status through the real
// writevectors harness and calling the real runApproveNostr/runRejectNostr
// returns nil and appends 2 events for inbox, active and scheduled (folded
// status "waiting" in all three), and refuses for done, cancelled and failed.
// A vector for these rows would therefore pin §9.4's promotion, which
// testdata/fold.vectors.json already pins, and would say nothing about the
// status clause.
//
// AND THE CLAUSE IS STILL NOT DEAD FOR THEM. buildWrite takes `items` from the
// CALLER, and the caller is a second, independently-written fold. "The fold
// guarantees this cannot happen" is exactly the assumption ready-e0e and
// ready-186 both falsified — two implementations of one predicate, disagreeing,
// with nothing comparing them. These six rows are the write path's own defence
// against a caller whose promotion is wrong or absent.
import { describe, expect, it } from "vitest";
import { xOnlyPubkey } from "../lib/schnorrsign";
import type { Item } from "../lib/state";
import { StatusActive, StatusInbox, StatusScheduled, StatusWaiting } from "../lib/state";
import { buildWrite, WriteRefusedError, type WriteEnv } from "./writeevents";

const SECRET = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
const OWNER = xOnlyPubkey(SECRET);
const BOARD_D = "proj";

/** An item carrying a LIVE gate in whatever status the row names — the shape
 * §9.4's promotion is supposed to make unreachable, supplied directly because
 * that is the assumption under test. */
function gatedItem(status: string): Item {
  return {
    id: "g-1",
    msg_id: "abc",
    title: "Needs a ruling",
    context: "",
    type: "task",
    priority: "p1",
    status,
    for: OWNER,
    gate: "design",
    waiting_type: "gate",
    waiting_on: "budget approval",
    gate_msg_id: "abc",
    created_at: 0n,
    updated_at: 0n,
  };
}

function envWith(item: Item): WriteEnv {
  return {
    signer: OWNER,
    boardAuthor: OWNER,
    boardD: BOARD_D,
    boardTitle: BOARD_D,
    items: new Map([[item.id, item]]),
    issueEventIds: new Map(),
    createdAt: 1_780_000_000,
  };
}

describe("buildWrite's gate ruling refuses the un-promoted statuses no vector can express", () => {
  const cases: { status: string; why: string }[] = [
    { status: StatusInbox, why: "an untriaged item has no pending escalation to rule on" },
    { status: StatusActive, why: "an active item is being worked, not awaiting a ruling" },
    { status: StatusScheduled, why: "rd gate forces a scheduled item to waiting" },
  ];

  for (const op of ["gate_approve", "gate_reject"] as const) {
    for (const c of cases) {
      it(`${op} on a ${c.status} item is refused — ${c.why}`, () => {
        const env = envWith(gatedItem(c.status));
        let thrown: unknown;
        try {
          buildWrite(env, { op, itemId: "g-1", reason: "ruled" });
        } catch (e) {
          thrown = e;
        }
        expect(thrown, `${op} on ${c.status} must be refused`).toBeInstanceOf(WriteRefusedError);
        // THE CODE, not merely "it threw": the refusal has to come from the
        // STATUS clause specifically, or this row is indistinguishable from the
        // no-gate refusal below.
        expect((thrown as WriteRefusedError).code).toBe("not_waiting");
      });
    }
  }

  // ANTI-TAUTOLOGY: the refusals above are about STATUS, not about the gate
  // being absent, so "everything is refused" cannot explain them. The
  // cross-implementation version of this discriminator is the vector
  // gate_approve_refuses_with_no_pending_gate; this local copy exists so the
  // three rows above are self-discriminating without a second file.
  it("an item with no pending gate is refused as no_gate, not as not_waiting", () => {
    const item = gatedItem(StatusWaiting);
    item.gate = undefined;
    item.waiting_type = undefined;
    item.gate_msg_id = undefined;
    let thrown: unknown;
    try {
      buildWrite(envWith(item), { op: "gate_approve", itemId: "g-1", reason: "ruled" });
    } catch (e) {
      thrown = e;
    }
    expect((thrown as WriteRefusedError).code).toBe("no_gate");
  });
});
