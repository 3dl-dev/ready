// The progress TRAIL — an item's `rd progress` notes — and the split between
// what a 30302 card CARRIES and what it merely DISPLAYS (ready-ed4).
//
// THE DEFECT THIS CLOSES: `rd progress` used to append its note directly onto
// Item.Context, and Item.Context is published verbatim as the addressable
// kind-30302 card's Content. Every note therefore rewrote the WHOLE card,
// larger, forever — monotonic, unbounded growth against a relay that hard-
// rejects an event over 64 KiB. Measured on the ready board 2026-07-30 over
// 2,364 kind-30302 events: ready-f75 at 39,921 bytes (60.9% of the cap),
// ready-6d0 at 37,156, ready-336 at 35,910, ready-c3e at 33,498; twelve events
// past 32 KiB. On another board it had already landed: vms-760 could no longer
// publish notes at all, and its live state had to be abandoned to a NEW item —
// the trail an agent reads to resume is exactly what got orphaned. ready-c3e's
// size guard turned that from a silent dead letter into a loud refusal, which
// is right and is NOT weakened here, but a loud refusal still leaves the item
// permanently unwritable.
//
// THE SPLIT THIS FILE DEFINES:
//
//   - Item.Context is, and stays, EXACTLY the card's Content — the item's base
//     description. It is the ONLY thing a card republish carries, so a card can
//     no longer grow with the trail. This direction is structural, not a
//     convention: there is no field on the item holding the assembled trail for
//     a write path to pick up by accident.
//   - Item.Notes is the trail, assembled by the fold from the item's own
//     kind-1111 note events (pkg/sync/nostrnotes.go) PLUS any notes still
//     embedded in a legacy card's Content, recovered by SplitCardTrail.
//   - AssembleTrail renders the two back into the one string `rd show` prints
//     and `rd log` parses. It is byte-identical to what the old in-card appender
//     produced, which is why every display path downstream is unchanged.
//
// THE RECOVERY FOR AN ALREADY-BRICKED ITEM falls straight out of that split.
// The fold runs SplitCardTrail on every card, so an over-limit legacy card
// projects as a SMALL Context plus a full Notes trail; the very next write
// republishes that small card and mints the missing note events alongside it
// (CardSpec.PendingNotes, pkg/sync). No migration command, no operator step —
// vms-760 recovers on its next `rd claim`/`rd progress`/close.
//
// ROUND-TRIP INVARIANT (asserted by TestTrailRoundTrip): for any content the
// old appender could have produced, AssembleTrail(SplitCardTrail(content)) ==
// content. SplitCardTrail therefore keeps a note's text VERBATIM — it does not
// trim it. Display trimming is a rendering choice and lives at the renderer
// (cmd/rd/timeline.go), not here, so `rd log` output is unchanged either way.
package state

import (
	"regexp"
	"strings"
	"time"
)

// NoteTimestampLayout is the display timestamp layout every progress note
// carries: minute precision, always UTC. It is the layout the pre-ready-ed4
// in-card appender used, so a legacy trail and a freshly minted note event
// render identically, and it is fixed-width and zero-padded so LEXICOGRAPHIC
// ordering of two such strings IS chronological ordering — which is what lets
// the fold order the trail without either reader having to parse a date
// (board-fold-spec.md §5.8).
const NoteTimestampLayout = "2006-01-02T15:04Z"

// noteBlockPattern matches the leading "[<ts>] " prefix of one note block in a
// legacy card's Content. The optional trailing space is consumed as part of the
// prefix (the old appender always wrote exactly one); anything after it is the
// note's text, verbatim.
var noteBlockPattern = regexp.MustCompile(`^\[(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}Z)\] ?`)

// ProgressNote is one entry in an item's progress trail.
type ProgressNote struct {
	// At is the note's display timestamp in NoteTimestampLayout. It is what the
	// trail is ORDERED by (lexicographically) and what AssembleTrail renders.
	At string `json:"at"`
	// Text is the note body, verbatim.
	Text string `json:"text"`
	// MsgID is the note event's own nostr event id.
	//
	// It is EMPTY for a note that is still embedded in a legacy card's Content
	// and has no event of its own yet — and that emptiness is load-bearing, not
	// cosmetic: it is precisely the set of notes the next card republish must
	// mint events for before the compacted card drops them (pkg/sync's
	// CardSpecFromItem → CardSpec.PendingNotes). Without that, compacting the
	// card would delete the trail from every relay's copy of the item, which is
	// the data loss this whole change exists to prevent.
	MsgID string `json:"msg_id,omitempty"`
}

// SplitCardTrail splits a 30302 card's Content into the base description and the
// progress notes the pre-ready-ed4 appender had embedded in it.
//
// A block is one "\n\n"-separated run. Leading blocks, up to the first one
// carrying a "[<ts>] " prefix, are the base description and are rejoined with
// "\n\n" exactly as they were. Each timestamped block starts a note; an
// untimestamped block AFTER one is a continuation of it (a note's own text may
// contain blank lines) and is reattached with the "\n\n" that separated them.
//
// A card with no timestamped block — every card written after this change, and
// every item that never took a progress note — returns (content, nil), so this
// is inert on the common path.
func SplitCardTrail(content string) (base string, notes []ProgressNote) {
	if content == "" {
		return "", nil
	}
	blocks := strings.Split(content, "\n\n")
	var baseBlocks []string
	for _, block := range blocks {
		m := noteBlockPattern.FindStringSubmatch(block)
		if m == nil {
			if len(notes) == 0 {
				baseBlocks = append(baseBlocks, block)
				continue
			}
			// Continuation of the note in progress — reattach verbatim, including
			// the separator that split() consumed.
			notes[len(notes)-1].Text += "\n\n" + block
			continue
		}
		notes = append(notes, ProgressNote{At: m[1], Text: block[len(m[0]):]})
	}
	return strings.Join(baseBlocks, "\n\n"), notes
}

// AssembleTrail renders an item's base Context plus its Notes into the single
// string that IS the item's readable trail — the exact string the pre-ready-ed4
// card Content held, so `rd show`, `rd log`'s note parser, and the board's
// detail pane all keep reading what they always read.
//
// This is the NORMATIVE assembly (board-fold-spec.md §5.7): the Go fold, the
// CLI renderers and web/board's TypeScript fold must all produce this string, or
// two readers of the same events would show two different trails.
func AssembleTrail(item *Item) string {
	if item == nil {
		return ""
	}
	if len(item.Notes) == 0 {
		return item.Context
	}
	var b strings.Builder
	b.WriteString(item.Context)
	for _, n := range item.Notes {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("[")
		b.WriteString(n.At)
		b.WriteString("] ")
		b.WriteString(n.Text)
	}
	return b.String()
}

// FormatNoteTimestamp renders a unix-SECONDS instant in NoteTimestampLayout. It
// is the fallback display timestamp for a note event that carries no "ts" tag
// (board-fold-spec.md §5.8), and what `rd progress` stamps a new note with.
func FormatNoteTimestamp(unixSeconds int64) string {
	return time.Unix(unixSeconds, 0).UTC().Format(NoteTimestampLayout)
}
