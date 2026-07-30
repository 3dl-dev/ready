// writeevents.gatestatus.test.ts — ready-e51 (veracity audit of the M2 wave).
//
// THE GUARD. buildWrite's gate_approve/gate_reject branch refuses an item whose
// status is not `waiting` or `blocked`, mirroring rd's §9.2 (`Status ∈ {waiting,
// blocked}` — widened by ready-e0e, because the ruling is usually what unblocks
// the chain). Deleting it — `if (false) {` in place of the status comparison —
// left 837/837 vitest green on this tree.
//
// WHY NOTHING COULD SEE IT, and why the fix is a TABLE HERE rather than another
// end-to-end case. Every other test drives this branch through a projection, and
// a projection cannot produce the shapes the clause excludes: the fold PROMOTES
// any non-terminal, non-blocked item that declares a gate to `waiting`, and
// CLEARS every gate field on a terminal one. So an active/inbox/scheduled item
// carrying a live gate does not exist downstream of the fold, and no
// harness-altitude test can construct one. This is the same conclusion
// pkg/views reached for the identical clause on the Go side —
// TestGatesFilter_StatusClauseIsExhaustive says in its own header that "at the
// CLI altitude the status clause is unfalsifiable" and pins it with a table
// instead. This file is that table's TypeScript counterpart.
//
// AND IT IS NOT A DEAD BRANCH. buildWrite takes `items` from the CALLER, and the
// caller is a second, independently-written fold (lib/fold.ts, the TS mirror of
// pkg/sync.ProjectItems). "The fold guarantees this cannot happen" is exactly
// the assumption ready-e0e and ready-186 both falsified — two implementations of
// one predicate, disagreeing, with nothing comparing them. The guard is the
// write path's own defence against that, so it gets its own witness.
import { describe, expect, it } from "vitest";
import { xOnlyPubkey } from "../lib/schnorrsign";
import type { Item } from "../lib/state";
import {
  StatusActive,
  StatusBlocked,
  StatusCancelled,
  StatusDone,
  StatusFailed,
  StatusInbox,
  StatusScheduled,
  StatusWaiting,
} from "../lib/state";
import { buildWrite, WriteRefusedError, type WriteEnv } from "./writeevents";

const SECRET = "b7e151628aed2a6abf7158809cf4f3c762e7160f38b4da56a784d9045190cfef";
const OWNER = xOnlyPubkey(SECRET);
const BOARD_D = "proj";

/** An item carrying a LIVE gate in whatever status the row names. The gate
 * fields are held constant across every row, so status is the only variable —
 * the property pkg/views.TestGatesFilter_StatusClauseIsExhaustive relies on to
 * make its table discriminate per clause. */
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

describe("buildWrite's gate ruling refuses every status rd's §9.2 excludes", () => {
  const cases: { status: string; admitted: boolean; why: string }[] = [
    { status: StatusWaiting, admitted: true, why: "the plain pending-gate case" },
    {
      status: StatusBlocked,
      admitted: true,
      why: "ready-e0e: blocked-and-gated is the ordinary design gate, and the ruling is what unblocks the chain",
    },
    { status: StatusInbox, admitted: false, why: "an untriaged item has no pending escalation to rule on" },
    { status: StatusActive, admitted: false, why: "an active item is being worked, not awaiting a ruling" },
    { status: StatusScheduled, admitted: false, why: "rd gate forces a scheduled item to waiting" },
    { status: StatusDone, admitted: false, why: "resolving a gate on a closed item is impossible" },
    { status: StatusCancelled, admitted: false, why: "resolving a gate on a closed item is impossible" },
    { status: StatusFailed, admitted: false, why: "resolving a gate on a closed item is impossible" },
  ];

  for (const op of ["gate_approve", "gate_reject"] as const) {
    for (const c of cases) {
      it(`${op} on a ${c.status} item is ${c.admitted ? "built" : "refused"} — ${c.why}`, () => {
        const env = envWith(gatedItem(c.status));
        if (c.admitted) {
          const built = buildWrite(env, { op, itemId: "g-1", reason: "ruled" });
          // Not trivially empty: an admitted ruling really publishes events.
          expect(built.length).toBeGreaterThan(0);
          return;
        }
        let thrown: unknown;
        try {
          buildWrite(env, { op, itemId: "g-1", reason: "ruled" });
        } catch (e) {
          thrown = e;
        }
        expect(thrown, `${op} on ${c.status} must be refused`).toBeInstanceOf(WriteRefusedError);
        // THE CODE, not merely "it threw": every excluded row must be refused by
        // the STATUS clause specifically. That matters most for the terminal
        // rows — the gate branch does NOT call refuseTerminal, so this clause is
        // the ONLY thing standing between a closed-but-still-gated item and a
        // signed ruling on it. Delete the clause and `gate_approve` on a done
        // item builds and signs, which is a state rd itself refuses (§9.2).
        expect((thrown as WriteRefusedError).code).toBe("not_waiting");
      });
    }
  }

  // ANTI-TAUTOLOGY for the whole table: the refusals above are about STATUS, not
  // about the gate being absent. An item with no gate at all is refused by a
  // DIFFERENT clause, so "everything is refused" cannot explain the rows.
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
