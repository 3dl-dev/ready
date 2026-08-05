// trail.ts is a PORT of pkg/state/trail.go, and a port's real risk is not "does
// it work" but "does it work the SAME". These tests therefore pin the two
// properties the Go side pins, in the same shapes:
//
//   - AssembleTrail(SplitCardTrail(content)) === content, for anything the
//     pre-ready-ed4 in-card appender could have produced; and
//   - the rendered separator is byte-identical to that appender's output, so the
//     browser's detail pane and `rd show` display the same trail.
//
// The cross-language agreement itself is proved by the shared vector file
// (fold.vectors.test.ts, cases_notes.go). These are the unit-level companions
// that localise a failure to this module instead of to "a vector disagrees".
import { describe, it, expect } from "vitest";
import { splitCardTrail, assembleTrail, formatNoteTimestamp } from "./trail";

/** oldAppender reproduces the PRE-ready-ed4 `rd progress` body verbatim — the
 * ground truth splitCardTrail must invert. Every legacy card on every board was
 * produced by exactly this. */
function oldAppender(context: string, ts: string, note: string): string {
  return context !== "" ? `${context}\n\n[${ts}] ${note}` : `[${ts}] ${note}`;
}

describe("splitCardTrail / assembleTrail round trip", () => {
  const cases: { name: string; base: string; notes: string[]; wantNotes: number }[] = [
    { name: "no notes at all", base: "just a description", notes: [], wantNotes: 0 },
    { name: "empty description, one note", base: "", notes: ["first"], wantNotes: 1 },
    { name: "description plus notes", base: "desc", notes: ["a", "b", "c"], wantNotes: 3 },
    {
      name: "a note containing blank lines is ONE note, not three",
      base: "desc",
      notes: ["line one\n\nline two\n\nline three"],
      wantNotes: 1,
    },
    {
      name: "a multi-paragraph description stays whole",
      base: "para one\n\npara two\n\npara three",
      notes: ["note"],
      wantNotes: 1,
    },
    {
      name: "a note whose text starts with a bracket but not a timestamp",
      base: "desc",
      notes: ["[not a timestamp] still one note"],
      wantNotes: 1,
    },
    { name: "an empty note body", base: "desc", notes: [""], wantNotes: 1 },
  ];

  for (const tc of cases) {
    it(tc.name, () => {
      let content = tc.base;
      tc.notes.forEach((n, i) => {
        content = oldAppender(content, `2026-07-${String(1 + i).padStart(2, "0")}T10:${String(i).padStart(2, "0")}Z`, n);
      });
      const { base, notes } = splitCardTrail(content);
      expect(assembleTrail({ context: base, notes })).toBe(content);
      expect(notes.length).toBe(tc.wantNotes);
    });
  }

  it("leaves a post-ready-ed4 card completely alone", () => {
    for (const content of [
      "",
      "a plain description",
      "multi\n\nparagraph\n\ndescription",
      "[TODO] a description that opens with a bracket",
      "[2026-07-30T20:00Z extra] not a note prefix either",
    ]) {
      const { base, notes } = splitCardTrail(content);
      expect(base).toBe(content);
      expect(notes).toEqual([]);
    }
  });

  it("keeps note text VERBATIM — no trimming — so the round trip is byte-exact", () => {
    // Trailing whitespace inside a note is preserved; trimming here would make
    // the recovered card content differ from the original by invisible bytes.
    const content = oldAppender("desc", "2026-07-30T10:00Z", "note with trailing spaces   ");
    const { base, notes } = splitCardTrail(content);
    expect(notes[0].text).toBe("note with trailing spaces   ");
    expect(assembleTrail({ context: base, notes })).toBe(content);
  });

  it("recovered notes carry NO msg_id — the marker that they need an event minted", () => {
    const { notes } = splitCardTrail(oldAppender("desc", "2026-07-30T10:00Z", "legacy"));
    expect(notes[0].msg_id).toBeUndefined();
  });
});

describe("assembleTrail rendering", () => {
  it("reproduces the old in-card format byte for byte", () => {
    const want = oldAppender(oldAppender("desc", "2026-07-30T20:00Z", "one"), "2026-07-30T21:00Z", "two");
    const got = assembleTrail({
      context: "desc",
      notes: [
        { at: "2026-07-30T20:00Z", text: "one" },
        { at: "2026-07-30T21:00Z", text: "two" },
      ],
    });
    expect(got).toBe(want);
  });

  it("does not emit a leading blank paragraph when the item has no description", () => {
    const got = assembleTrail({ context: "", notes: [{ at: "2026-07-30T20:00Z", text: "only note" }] });
    expect(got).toBe("[2026-07-30T20:00Z] only note");
    expect(got.startsWith("\n")).toBe(false);
  });

  it("is the identity on an item with no notes", () => {
    expect(assembleTrail({ context: "just a description", notes: [] })).toBe("just a description");
    expect(assembleTrail({ context: "just a description" })).toBe("just a description");
  });
});

describe("formatNoteTimestamp", () => {
  it("renders minute-precision UTC", () => {
    expect(formatNoteTimestamp(1785615478)).toBe("2026-08-01T20:17Z");
  });

  it("zero-pads every field, so string order IS chronological order", () => {
    // This is what lets both folds order the trail without parsing a date
    // (spec §5.8) — a single unpadded field would silently break the sort.
    const early = formatNoteTimestamp(1767229380); // 2026-01-01T01:03Z
    const late = formatNoteTimestamp(1785615478);
    expect(early).toHaveLength("2006-01-02T15:04Z".length);
    expect(late).toHaveLength("2006-01-02T15:04Z".length);
    expect(early < late).toBe(true);
    expect(early).toBe("2026-01-01T01:03Z");
  });
});
