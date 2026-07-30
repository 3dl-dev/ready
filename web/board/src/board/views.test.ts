// Ports pkg/views/views_test.go's cases onto the TS predicates, so
// board-445's column membership is tested against the fold's own semantics
// (board-fold-spec.md §13), not against a hand-rolled status vocabulary —
// the specific named bug this item exists to avoid.
import { describe, expect, it } from "vitest";
import { makeItem } from "./testitem";
import { apply, columnize, gatesFilter, labelFilter, pendingFilter, readyFilter, workFilter } from "./views";
// ready-186: this repo carries TWO TypeScript ports of GatesFilter. They
// disagreed — ../lib/views.ts admitted `blocked` (citing ready-e0e) and this
// directory's did not — and nothing compared them, so the divergence was
// invisible to both suites. The cross-port case below is that comparison.
import { gatesFilter as libGatesFilter } from "../lib/views";
import type { Item as LibItem } from "../lib/state";

describe("readyFilter", () => {
  it("mirrors views.ReadyFilter: not terminal, not blocked, not scheduled", () => {
    const f = readyFilter();
    expect(f(makeItem({ status: "active" }))).toBe(true);
    expect(f(makeItem({ status: "inbox" }))).toBe(true);
    expect(f(makeItem({ status: "done" }))).toBe(false);
    expect(f(makeItem({ status: "cancelled" }))).toBe(false);
    expect(f(makeItem({ status: "failed" }))).toBe(false);
    expect(f(makeItem({ status: "blocked" }))).toBe(false);
    expect(f(makeItem({ status: "scheduled" }))).toBe(false);
  });

  it("does NOT filter on ETA (ETA sorts, it does not exclude — §13.3)", () => {
    const f = readyFilter();
    const overdue = makeItem({ status: "active", eta: "2000-01-01T00:00:00Z" });
    expect(f(overdue)).toBe(true);
  });
});

describe("workFilter", () => {
  it("mirrors views.WorkFilter: status === active, nothing else", () => {
    const f = workFilter();
    expect(f(makeItem({ status: "active" }))).toBe(true);
    expect(f(makeItem({ status: "inbox" }))).toBe(false);
    expect(f(makeItem({ status: "waiting" }))).toBe(false);
  });
});

describe("pendingFilter", () => {
  it("mirrors views.PendingFilter: waiting, scheduled, or blocked", () => {
    const f = pendingFilter();
    expect(f(makeItem({ status: "waiting" }))).toBe(true);
    expect(f(makeItem({ status: "scheduled" }))).toBe(true);
    expect(f(makeItem({ status: "blocked" }))).toBe(true);
    expect(f(makeItem({ status: "active" }))).toBe(false);
    expect(f(makeItem({ status: "done" }))).toBe(false);
  });
});

describe("gatesFilter", () => {
  it("mirrors views.GatesFilter: (waiting OR blocked) AND waitingType=gate AND gateMsgId non-empty — all three conjuncts matter", () => {
    const f = gatesFilter();
    expect(f(makeItem({ status: "waiting", waitingType: "gate", gateMsgId: "abc" }))).toBe(true);
    expect(f(makeItem({ status: "waiting", waitingType: "gate", gateMsgId: "" }))).toBe(false);
    expect(f(makeItem({ status: "waiting", waitingType: "human", gateMsgId: "abc" }))).toBe(false);
    expect(f(makeItem({ status: "active", waitingType: "gate", gateMsgId: "abc" }))).toBe(false);
  });

  // ready-186 / ready-e0e. §13.10 widened the first conjunct from `waiting`-only,
  // and §9.7 says the gate fields SURVIVE blocking, so blocked-and-gated is a
  // state the fold really produces — it is the ordinary shape of a design gate,
  // since the ruling is usually what unblocks the chain. This port dropped it
  // while ../lib/views.ts and pkg/views/views.go both kept it.
  it("admits BLOCKED and gated — the ordinary design gate, not an edge case (§13.10, ready-e0e)", () => {
    const f = gatesFilter();
    expect(f(makeItem({ status: "blocked", waitingType: "gate", gateMsgId: "abc" }))).toBe(true);
    // …and the other two conjuncts still bite on a blocked item.
    expect(f(makeItem({ status: "blocked", waitingType: "gate", gateMsgId: "" }))).toBe(false);
    expect(f(makeItem({ status: "blocked", waitingType: "human", gateMsgId: "abc" }))).toBe(false);
    // A merely blocked item — no gate declared at all — is NOT in the gates view.
    expect(f(makeItem({ status: "blocked" }))).toBe(false);
  });

  it("agrees with the repo's OTHER port of the same predicate, item for item", () => {
    const mine = gatesFilter();
    const theirs = libGatesFilter();
    const cases: { status: string; waitingType?: string; gateMsgId?: string }[] = [
      { status: "waiting", waitingType: "gate", gateMsgId: "abc" },
      { status: "blocked", waitingType: "gate", gateMsgId: "abc" },
      { status: "blocked", waitingType: "gate", gateMsgId: "" },
      { status: "blocked", waitingType: "human", gateMsgId: "abc" },
      { status: "blocked" },
      { status: "active", waitingType: "gate", gateMsgId: "abc" },
      { status: "scheduled", waitingType: "gate", gateMsgId: "abc" },
      { status: "done", waitingType: "gate", gateMsgId: "abc" },
      { status: "inbox", waitingType: "gate", gateMsgId: "abc" },
    ];
    for (const c of cases) {
      const board = makeItem({ status: c.status, waitingType: c.waitingType, gateMsgId: c.gateMsgId });
      const lib = {
        ...board,
        waiting_type: c.waitingType,
        gate_msg_id: c.gateMsgId,
      } as unknown as LibItem;
      expect(`${JSON.stringify(c)} -> ${mine(board)}`).toBe(`${JSON.stringify(c)} -> ${theirs(lib)}`);
    }
  });
});

describe("labelFilter", () => {
  it("exact match only, no substring/glob (§13.12)", () => {
    expect(labelFilter("bug")(makeItem({ labels: ["bug", "security"] }))).toBe(true);
    expect(labelFilter("bu")(makeItem({ labels: ["bug"] }))).toBe(false);
  });
});

describe("apply", () => {
  it("filters preserving input order (§13.1)", () => {
    const items = [makeItem({ id: "a", status: "active" }), makeItem({ id: "b", status: "done" }), makeItem({ id: "c", status: "active" })];
    expect(apply(items, workFilter()).map((i) => i.id)).toEqual(["a", "c"]);
  });
});

describe("columnize", () => {
  it("partitions into exactly three columns, terminal items in none", () => {
    const items = [
      makeItem({ id: "ready1", status: "inbox" }),
      makeItem({ id: "moving1", status: "active" }),
      makeItem({ id: "blocked1", status: "blocked" }),
      makeItem({ id: "waiting1", status: "waiting" }),
      makeItem({ id: "scheduled1", status: "scheduled" }),
      makeItem({ id: "done1", status: "done" }),
      makeItem({ id: "cancelled1", status: "cancelled" }),
    ];
    const cols = columnize(items);
    expect(cols.ready.map((i) => i.id)).toEqual(["ready1"]);
    expect(cols.moving.map((i) => i.id)).toEqual(["moving1"]);
    expect(cols.blocked.map((i) => i.id).sort()).toEqual(["blocked1", "scheduled1", "waiting1"]);
    // terminal items appear in no column
    const allColumnized = [...cols.ready, ...cols.moving, ...cols.blocked].map((i) => i.id);
    expect(allColumnized).not.toContain("done1");
    expect(allColumnized).not.toContain("cancelled1");
  });

  it("every non-terminal item lands in exactly one column (partition property)", () => {
    const statuses = ["inbox", "active", "waiting", "blocked", "scheduled"];
    const items = statuses.map((status, i) => makeItem({ id: `i${i}`, status }));
    const cols = columnize(items);
    const membership = new Map<string, number>();
    for (const bucket of [cols.ready, cols.moving, cols.blocked]) {
      for (const item of bucket) membership.set(item.id, (membership.get(item.id) ?? 0) + 1);
    }
    for (const item of items) expect(membership.get(item.id)).toBe(1);
  });
});
