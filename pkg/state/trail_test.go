package state

import (
	"fmt"
	"strings"
	"testing"
)

// oldAppender reproduces the PRE-ready-ed4 `rd progress` body verbatim (see the
// pre-change cmd/rd/aliases.go progressCmd): the note is appended to the existing
// context with a "\n\n[<ts>] " separator, or becomes the whole context when there
// was none. It is the ground truth SplitCardTrail must invert — every legacy card
// on every board was produced by exactly this.
func oldAppender(context, ts, note string) string {
	if context != "" {
		return context + "\n\n[" + ts + "] " + note
	}
	return "[" + ts + "] " + note
}

// TestTrailRoundTrip is the invariant the whole ready-ed4 recovery rests on: for
// any card content the old appender could have produced,
// AssembleTrail(SplitCardTrail(content)) == content.
//
// It matters because the fold SPLITS every legacy card and the write path then
// republishes only the base plus separately-minted note events. If the split and
// the re-assembly disagreed by so much as a newline, recovering a bricked item
// would silently rewrite its history — and the items this fix exists to rescue
// are precisely the ones with the most history to lose.
func TestTrailRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		base    string
		notes   []string
		wantNum int
	}{
		{"no notes at all", "just a description", nil, 0},
		{"empty description, one note", "", []string{"first"}, 1},
		{"description plus notes", "desc", []string{"a", "b", "c"}, 3},
		{
			"a note containing blank lines is ONE note, not three",
			"desc",
			[]string{"line one\n\nline two\n\nline three"},
			1,
		},
		{
			"a multi-paragraph description stays whole",
			"para one\n\npara two\n\npara three",
			[]string{"note"},
			1,
		},
		{
			"a note whose text starts with a bracket but not a timestamp",
			"desc",
			[]string{"[not a timestamp] still one note"},
			1,
		},
		{
			"an empty note body",
			"desc",
			[]string{""},
			1,
		},
		{
			"a note containing a line that LOOKS like a timestamped block but is mid-paragraph",
			"desc",
			// Two, not one: this text is byte-for-byte indistinguishable from two
			// notes — see TestTrailRoundTrip_AmbiguousNoteText.
			[]string{"see below\n\n[2026-01-01T00:00Z] this WILL split — documented there"},
			2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			content := tc.base
			for i, n := range tc.notes {
				content = oldAppender(content, fmt.Sprintf("2026-07-%02dT10:%02dZ", 1+i, i), n)
			}
			base, notes := SplitCardTrail(content)
			item := &Item{Context: base, Notes: notes}
			if got := AssembleTrail(item); got != content {
				t.Fatalf("round trip lost data.\n got: %q\nwant: %q", got, content)
			}
			if len(notes) != tc.wantNum {
				t.Errorf("split produced %d notes, want %d — the round trip can hold while the note BOUNDARIES are wrong, which is what a reader sees", len(notes), tc.wantNum)
			}
		})
	}
}

// TestTrailRoundTrip_AmbiguousNoteText documents — and pins — the ONE case the
// format genuinely cannot distinguish: a note whose own text contains a paragraph
// beginning with a well-formed "[<ts>] " prefix is indistinguishable from two
// notes, because that is byte-for-byte what two notes look like.
//
// This is a property of the LEGACY in-card format, not something this change
// introduces, and it is why the format is being left behind rather than extended:
// a note event carries its timestamp in a tag where no amount of note text can
// forge it. The round trip still holds (the reassembled string is identical) —
// only the note COUNT differs — so no card content is ever corrupted by the split.
func TestTrailRoundTrip_AmbiguousNoteText(t *testing.T) {
	content := oldAppender("desc", "2026-07-01T10:00Z", "see below\n\n[2026-01-01T00:00Z] embedded")
	base, notes := SplitCardTrail(content)
	if got := AssembleTrail(&Item{Context: base, Notes: notes}); got != content {
		t.Fatalf("round trip is NOT byte-exact even in the ambiguous case:\n got: %q\nwant: %q", got, content)
	}
	if len(notes) != 2 {
		t.Fatalf("expected the ambiguous content to split into 2 notes (that is what it is indistinguishable from), got %d", len(notes))
	}
	if notes[0].Text != "see below" || notes[1].Text != "embedded" {
		t.Fatalf("ambiguous split produced unexpected texts: %q / %q", notes[0].Text, notes[1].Text)
	}
}

// TestSplitCardTrail_LeavesModernCardsAlone: a card written after ready-ed4
// carries no timestamped blocks, so the split must be a pure identity on it —
// base == content, no notes. Anything else would mean a normal description that
// happens to contain a bracket gets shredded into a fake trail.
func TestSplitCardTrail_LeavesModernCardsAlone(t *testing.T) {
	for _, content := range []string{
		"",
		"a plain description",
		"multi\n\nparagraph\n\ndescription",
		"[TODO] a description that opens with a bracket",
		"[2026-07-30T20:00Z extra] not a note prefix either",
		"[2026-07-30T20:00 Z] a stamp missing its Z placement is not a prefix",
	} {
		base, notes := SplitCardTrail(content)
		if base != content || len(notes) != 0 {
			t.Errorf("SplitCardTrail(%q) = (%q, %d notes); want the content unchanged and no notes", content, base, len(notes))
		}
	}
}

// TestAssembleTrail_NoteSeparatorMatchesTheOldFormat pins the rendered shape
// byte-for-byte against the old appender, so `rd show`, `rd log`'s note parser
// and the board's detail pane keep reading exactly what they always read. A drift
// here (an extra space, a single newline) would be invisible to a "contains"
// assertion and would break `rd log`'s regex on the very next release.
func TestAssembleTrail_NoteSeparatorMatchesTheOldFormat(t *testing.T) {
	item := &Item{
		Context: "desc",
		Notes: []ProgressNote{
			{At: "2026-07-30T20:00Z", Text: "one"},
			{At: "2026-07-30T21:00Z", Text: "two"},
		},
	}
	want := oldAppender(oldAppender("desc", "2026-07-30T20:00Z", "one"), "2026-07-30T21:00Z", "two")
	if got := AssembleTrail(item); got != want {
		t.Fatalf("AssembleTrail does not reproduce the old in-card format.\n got: %q\nwant: %q", got, want)
	}
}

// TestAssembleTrail_EmptyContextStartsWithTheNote covers the branch the old
// appender had its own special case for: an item created with no description
// must not render a leading blank paragraph.
func TestAssembleTrail_EmptyContextStartsWithTheNote(t *testing.T) {
	got := AssembleTrail(&Item{Notes: []ProgressNote{{At: "2026-07-30T20:00Z", Text: "only note"}}})
	want := "[2026-07-30T20:00Z] only note"
	if got != want {
		t.Fatalf("AssembleTrail with an empty context = %q, want %q", got, want)
	}
	if strings.HasPrefix(got, "\n") {
		t.Errorf("assembled trail starts with a newline — an item with no description would render a leading blank line")
	}
}

// TestSplitCardTrail_MatchesTheLogParsersShape documents a DELIBERATE property:
// the note-block pattern here recognises the same SHAPE cmd/rd/timeline.go's
// progressNotePattern has always recognised — four-two-two digits, T, two-two,
// Z — not a semantically valid date. A structurally well-formed but impossible
// timestamp therefore reads as a note prefix in both.
//
// Keeping the two patterns aligned is the point. The split and `rd log`'s parser
// must agree about where a note begins, and the only strings either will ever see
// in the wild were written by the old appender, which always emitted a real
// timestamp. Tightening this one and not the other would create a class of card
// the fold splits one way and the log renders another. (The one deliberate
// difference: this pattern consumes exactly ONE following space, where the log
// parser accepts any single whitespace — that strictness is what makes the split
// byte-reversible, which the log parser never needed to be.)
func TestSplitCardTrail_MatchesTheLogParsersShape(t *testing.T) {
	content := oldAppender("desc", "2026-13-45T99:99Z", "structurally well-formed, semantically impossible")
	base, notes := SplitCardTrail(content)
	if len(notes) != 1 {
		t.Fatalf("expected the structurally-shaped stamp to read as a note prefix (matching timeline.go's parser), got %d notes", len(notes))
	}
	if got := AssembleTrail(&Item{Context: base, Notes: notes}); got != content {
		t.Fatalf("round trip broken for a structurally-shaped stamp:\n got: %q\nwant: %q", got, content)
	}
}

// TestFormatNoteTimestamp pins the fallback display stamp's layout, which §5.8
// requires to be fixed-width and zero-padded so that LEXICOGRAPHIC ordering of
// two stamps is chronological ordering. The whole trail sort depends on it.
func TestFormatNoteTimestamp(t *testing.T) {
	// 2026-08-01T20:17:58Z -> minute precision, seconds dropped, always UTC.
	if got, want := FormatNoteTimestamp(1785615478), "2026-08-01T20:17Z"; got != want {
		t.Fatalf("FormatNoteTimestamp = %q, want %q", got, want)
	}
	// Single-digit month/day/hour/minute must be zero-padded, or string ordering
	// stops being chronological ordering.
	got := FormatNoteTimestamp(1767229380) // 2026-01-01T01:03Z
	if len(got) != len("2006-01-02T15:04Z") {
		t.Fatalf("FormatNoteTimestamp produced %q (%d chars); the layout must be fixed width", got, len(got))
	}
	earlier := FormatNoteTimestamp(1767229380)
	later := FormatNoteTimestamp(1785615478)
	if !(earlier < later) {
		t.Fatalf("lexicographic order disagrees with chronological order: %q >= %q", earlier, later)
	}
}
