import { describe, expect, it } from "vitest";
import { makeItem } from "./testitem";
import { ageLabel, daysSince, statusLine } from "./statusline";

const NOW = Date.parse("2026-07-28T00:00:00Z");
const nowNanos = NOW * 1e6;

describe("statusLine", () => {
  it("yours takes priority when the viewer matches for/by, even if active", () => {
    const item = makeItem({ status: "active", by: "alice" });
    expect(statusLine(item, "alice", NOW)).toBe("yours");
  });

  it("waits on X for waiting/blocked items with waitingOn set", () => {
    expect(statusLine(makeItem({ status: "waiting", waitingOn: "bob" }), undefined, NOW)).toBe("waits on bob");
    expect(statusLine(makeItem({ status: "blocked", waitingOn: "ready-abc" }), undefined, NOW)).toBe(
      "waits on ready-abc",
    );
  });

  it("agent working for active items with no matching viewer", () => {
    expect(statusLine(makeItem({ status: "active", by: "someone-else" }), "alice", NOW)).toBe("agent working");
  });

  it("queued for inbox items", () => {
    expect(statusLine(makeItem({ status: "inbox" }), undefined, NOW)).toBe("queued");
  });

  it("falls back to untouched Nd", () => {
    const tenDaysAgoNanos = nowNanos - 10 * 24 * 60 * 60 * 1e9;
    const item = makeItem({ status: "done", updatedAt: tenDaysAgoNanos });
    expect(statusLine(item, undefined, NOW)).toBe("untouched 10d");
  });
});

describe("daysSince", () => {
  it("computes whole days from unix-nanos to now", () => {
    const fiveDaysAgo = nowNanos - 5 * 24 * 60 * 60 * 1e9;
    expect(daysSince(fiveDaysAgo, NOW)).toBe(5);
  });

  it("never goes negative for a future timestamp", () => {
    expect(daysSince(nowNanos + 1e15, NOW)).toBe(0);
  });
});

describe("ageLabel", () => {
  // ready-56b: the card age used to be `${daysSince()}d` for every item, so on
  // a live board where most work moved within the last day EVERY card read
  // "0d" and the column carried no information at all. The prototype's three
  // units are what make "11m" and "23d" tell different stories.
  it("uses minutes under an hour, hours under a day, days above", () => {
    const at = (ms: number) => (NOW - ms) * 1e6;
    expect(ageLabel(at(0), NOW)).toBe("0m");
    expect(ageLabel(at(11 * 60_000), NOW)).toBe("11m");
    expect(ageLabel(at(59 * 60_000), NOW)).toBe("59m");
    expect(ageLabel(at(60 * 60_000), NOW)).toBe("1h");
    expect(ageLabel(at(23 * 3_600_000), NOW)).toBe("23h");
    expect(ageLabel(at(24 * 3_600_000), NOW)).toBe("1d");
    expect(ageLabel(at(23 * 24 * 3_600_000), NOW)).toBe("23d");
  });

  it("never goes negative for a future timestamp", () => {
    expect(ageLabel(nowNanos + 1e15, NOW)).toBe("0m");
  });
});
