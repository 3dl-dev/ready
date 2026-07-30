// TS port of pkg/state/trail.go — the progress TRAIL and the split between what
// a 30302 card CARRIES and what it merely DISPLAYS (ready-ed4).
//
// This file exists so the browser assembles the SAME trail rd does. The item's
// note trail is now spread across the card's content (for a legacy card) and its
// own kind-1111 events; if the two folds disagreed about how those are split and
// re-joined, the same events would render as two different histories depending on
// which reader you asked — the exact failure the board's conformance suite is for.
// Keep this in lockstep with pkg/state/trail.go; a Go/TS divergence here is
// invisible to the Go test suite and only the board vitest suite catches it.

import type { Item } from "./state";

/** NoteTimestampLayout's rendered shape: minute precision, always UTC. Fixed
 * width and zero padded, so LEXICOGRAPHIC ordering of two of these IS
 * chronological ordering — which is what lets both folds order the trail
 * without either having to parse a date (spec §5.8). */
const NOTE_BLOCK_RE = /^\[(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}Z)\] ?/;

/** ProgressNote mirrors state.ProgressNote. */
export interface ProgressNote {
  /** Display timestamp; what the trail is ORDERED by and what renders. */
  at: string;
  /** Note body, verbatim. */
  text: string;
  /** The note event's own id, or absent for a note still embedded in a legacy
   * card's content with no event of its own yet (spec §5.7). */
  msg_id?: string;
}

/** splitCardTrail mirrors state.SplitCardTrail EXACTLY: a card's content splits
 * into the base description and the timestamped notes the pre-ready-ed4
 * `rd progress` had appended into it.
 *
 * Leading blocks up to the first timestamped one are the base, rejoined with
 * "\n\n" as they were. An untimestamped block AFTER a note is a continuation of
 * it (a note's own text may contain blank lines). Text is kept VERBATIM — no
 * trimming — so assembleTrail(splitCardTrail(c)) === c. */
export function splitCardTrail(content: string): { base: string; notes: ProgressNote[] } {
  if (content === "") return { base: "", notes: [] };
  const blocks = content.split("\n\n");
  const baseBlocks: string[] = [];
  const notes: ProgressNote[] = [];
  for (const block of blocks) {
    const m = NOTE_BLOCK_RE.exec(block);
    if (m === null) {
      if (notes.length === 0) {
        baseBlocks.push(block);
        continue;
      }
      notes[notes.length - 1].text += "\n\n" + block;
      continue;
    }
    notes.push({ at: m[1], text: block.slice(m[0].length) });
  }
  return { base: baseBlocks.join("\n\n"), notes };
}

/** assembleTrail mirrors state.AssembleTrail: the item's base context plus its
 * notes, rendered into the single string that IS the item's readable trail —
 * byte-identical to what the pre-ready-ed4 card content held (spec §5.7). */
export function assembleTrail(item: Pick<Item, "context" | "notes">): string {
  const base = item.context ?? "";
  const notes = item.notes ?? [];
  if (notes.length === 0) return base;
  let out = base;
  for (const n of notes) {
    if (out.length > 0) out += "\n\n";
    out += "[" + n.at + "] " + n.text;
  }
  return out;
}

/** formatNoteTimestamp mirrors state.FormatNoteTimestamp: a unix-SECONDS instant
 * in the note timestamp layout. The fallback display timestamp for a note event
 * that carries no "ts" tag (spec §5.8).
 *
 * Built from the UTC field getters rather than slicing toISOString() so the
 * output is the layout's own shape by construction — toISOString() happens to
 * agree today only because the layout is an ISO prefix. */
export function formatNoteTimestamp(unixSeconds: number): string {
  const d = new Date(unixSeconds * 1000);
  const p = (n: number, w = 2): string => String(n).padStart(w, "0");
  return (
    `${p(d.getUTCFullYear(), 4)}-${p(d.getUTCMonth() + 1)}-${p(d.getUTCDate())}` +
    `T${p(d.getUTCHours())}:${p(d.getUTCMinutes())}Z`
  );
}
