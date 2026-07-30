// ready-ed4: progress notes as their own events — the wire shape, the
// reachability guarantees, the normative trail order, and the confidential path.
//
// Lives beside nostrnotes.go rather than in nostrwire_test.go so the note kind's
// tests sit with the code they pin; nostrwire_test.go's testKey/findTag helpers
// are package-level and shared.
package sync

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// TestBuildNoteEvent_AnchorsAndReachability pins the note event's wire shape
// against the two properties that make a note FINDABLE — both of which have
// burned this repo before, on status events (ready-7ec).
func TestBuildNoteEvent_AnchorsAndReachability(t *testing.T) {
	k := testKey(t)
	owner := testKey(t)
	board := BoardCoord(owner.PubKeyHex(), "proj")

	e, err := BuildNoteEvent(k, NoteSpec{
		ItemID:     "proj-1",
		At:         "2026-07-30T20:17Z",
		Text:       "did the thing",
		BoardCoord: board,
	}, 1000)
	if err != nil {
		t.Fatalf("BuildNoteEvent: %v", err)
	}
	if e.Kind != KindNote {
		t.Errorf("kind = %d, want %d", e.Kind, KindNote)
	}
	// The FIRST "a" tag must be the CARD coordinate: tagValue reads only the first
	// match, and itemIDForEvent's fall-back resolves the item id from it.
	if got, _ := findTag(e.Tags, "a"); got != CardCoord(k.PubKeyHex(), "proj-1") {
		t.Errorf("first 'a' tag = %q, want the card coordinate %q — rd's projection reads only the first match", got, CardCoord(k.PubKeyHex(), "proj-1"))
	}
	if got, _ := findTag(e.Tags, "d"); got != "proj-1" {
		t.Errorf("'d' tag = %q, want the item id", got)
	}
	if got, _ := findTag(e.Tags, "ts"); got != "2026-07-30T20:17Z" {
		t.Errorf("'ts' tag = %q, want the carried display timestamp", got)
	}
	if e.Content != "did the thing" {
		t.Errorf("content = %q, want the plaintext note", e.Content)
	}

	// THE BOARD COORDINATE MUST RIDE AS A SECOND "a" TAG. Without it a
	// board-scoped sync never fetches the note, and the trail exists only on the
	// machine that wrote it. Asserted THROUGH the real filter — BoardSyncFilter is
	// what negentropy and `rd nostr sync` actually use — not by eyeballing tags.
	if !matchesFilter(e, BoardSyncFilter(board, nil)) {
		t.Fatalf("a note event does NOT match BoardSyncFilter(%q) — a board-scoped sync would silently miss every progress note, the exact hole ready-7ec closed for status events", board)
	}
	// And it must resolve to its item, or the fold drops it at the item-id guard.
	if got := itemIDForEvent(e); got != "proj-1" {
		t.Errorf("itemIDForEvent = %q, want %q — the fold's item-id guard would drop this note", got, "proj-1")
	}
}

// TestBuildNoteEvent_NoTSTagFallsBackToCreatedAt covers §5.8's fallback: a note
// from a client that omits "ts" still renders with a sane timestamp, rather than
// an empty one that would sort to the very front of the trail.
func TestBuildNoteEvent_NoTSTagFallsBackToCreatedAt(t *testing.T) {
	k := testKey(t)
	e, err := BuildNoteEvent(k, NoteSpec{ItemID: "proj-1", Text: "no stamp"}, 1785615478)
	if err != nil {
		t.Fatalf("BuildNoteEvent: %v", err)
	}
	if _, ok := findTag(e.Tags, "ts"); ok {
		t.Fatalf("an empty NoteSpec.At must emit NO ts tag")
	}
	fn, ok := noteFromEvent(e, nil)
	if !ok {
		t.Fatalf("noteFromEvent refused a plaintext note")
	}
	if want := state.FormatNoteTimestamp(1785615478); fn.note.At != want {
		t.Errorf("fallback At = %q, want the event's own created_at rendered as %q", fn.note.At, want)
	}
}

// TestNoteTrail_OrderIsNormative pins §5.9's four-level order — the clause the
// browser port must match. The tie-breaks only surface when timestamps collide,
// which is exactly what a burst of notes within one minute produces, so "it
// looked right in my test" is not enough here.
func TestNoteTrail_OrderIsNormative(t *testing.T) {
	cardNotes := []state.ProgressNote{
		{At: "2026-07-30T20:00Z", Text: "card A"},
		{At: "2026-07-30T20:00Z", Text: "card B"},
	}
	events := []foldedNote{
		{note: state.ProgressNote{At: "2026-07-30T20:00Z", Text: "event later", MsgID: "ff"}, createdAt: 200},
		{note: state.ProgressNote{At: "2026-07-30T20:00Z", Text: "event earlier", MsgID: "aa"}, createdAt: 100},
		{note: state.ProgressNote{At: "2026-07-29T09:00Z", Text: "event oldest", MsgID: "zz"}, createdAt: 50},
	}
	got := assembleTrail(cardNotes, events)
	want := []string{"event oldest", "card A", "card B", "event earlier", "event later"}
	if len(got) != len(want) {
		t.Fatalf("got %d notes, want %d (%v)", len(got), len(want), noteTexts(got))
	}
	for i := range want {
		if got[i].Text != want[i] {
			t.Fatalf("trail order wrong at %d: got %q, want %q (full: %v)", i, got[i].Text, want[i], noteTexts(got))
		}
	}

	// Same-created_at events tie-break on ascending event id — the same total
	// order §4 uses everywhere else, and the only thing that makes two machines
	// holding the same event SET agree on one trail.
	tie := []foldedNote{
		{note: state.ProgressNote{At: "2026-07-30T20:00Z", Text: "id bbb", MsgID: "bbb"}, createdAt: 100},
		{note: state.ProgressNote{At: "2026-07-30T20:00Z", Text: "id aaa", MsgID: "aaa"}, createdAt: 100},
	}
	if got := assembleTrail(nil, tie); got[0].Text != "id aaa" || got[1].Text != "id bbb" {
		t.Errorf("same-created_at notes must order by ascending event id; got %v", noteTexts(got))
	}
}

func noteTexts(notes []state.ProgressNote) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = n.Text
	}
	return out
}

// TestPendingNotes_SelectsOnlyEventlessNotes pins the marker the entire recovery
// path turns on: a note with no MsgID is one the compacted card is about to drop,
// so an event MUST be minted for it. Wrong in either direction is a defect —
// under-selecting loses the trail from every relay, over-selecting re-mints the
// whole history on every single write, forever.
func TestPendingNotes_SelectsOnlyEventlessNotes(t *testing.T) {
	notes := []state.ProgressNote{
		{At: "2026-07-30T20:00Z", Text: "from a legacy card"},
		{At: "2026-07-30T20:01Z", Text: "already an event", MsgID: "abc"},
		{At: "2026-07-30T20:02Z", Text: "also from the card"},
	}
	got := PendingNotes(notes)
	if len(got) != 2 || got[0].Text != "from a legacy card" || got[1].Text != "also from the card" {
		t.Fatalf("PendingNotes = %v, want exactly the two notes with no MsgID", noteTexts(got))
	}
	if got := PendingNotes([]state.ProgressNote{{At: "x", Text: "y", MsgID: "id"}}); len(got) != 0 {
		t.Fatalf("PendingNotes on an all-event-backed trail = %v, want none — otherwise every write re-mints the whole history", noteTexts(got))
	}
}

// TestNoteEvent_ConfidentialRoundTripAndFailClosed proves a note on a
// confidential board is SEALED — its text appears nowhere in the clear on the
// wire — and that a reader without the key gets NOTHING rather than ciphertext.
//
// This matters more for notes than for almost any other field: a progress note is
// where an agent writes down what it actually found, so a plaintext note would
// leak the most detailed free text on the board.
func TestNoteEvent_ConfidentialRoundTripAndFailClosed(t *testing.T) {
	k := testKey(t)
	var cek [32]byte
	for i := range cek {
		cek[i] = byte(i + 1)
	}
	env := &Envelope{CEK: cek, Epoch: 3}
	const secret = "the credential rotated at 20:17 and the old one is revoked"

	e, err := BuildNoteEvent(k, NoteSpec{ItemID: "proj-1", At: "2026-07-30T20:17Z", Text: secret, Enc: env}, 1000)
	if err != nil {
		t.Fatalf("BuildNoteEvent (confidential): %v", err)
	}
	raw, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// Whole-event scan, not just Content: a leak into a tag would be just as fatal.
	for _, word := range []string{"credential", "revoked", "rotated"} {
		if strings.Contains(string(raw), word) {
			t.Fatalf("a confidential note's plaintext (%q) appears in the marshaled event: %s", word, raw)
		}
	}
	if got, _ := findTag(e.Tags, "enc"); got != "1" {
		t.Errorf("missing enc marker tag, got %q", got)
	}
	if got, _ := findTag(e.Tags, "cek_epoch"); got != "3" {
		t.Errorf("cek_epoch = %q, want 3", got)
	}

	// A holder of the key recovers it exactly.
	fn, ok := noteFromEvent(e, testNoteDecryptor{cek: cek, epoch: 3})
	if !ok {
		t.Fatalf("a key holder could not read its own confidential note")
	}
	if fn.note.Text != secret {
		t.Errorf("decrypted note = %q, want %q", fn.note.Text, secret)
	}

	// A reader WITHOUT the key gets no note at all — not ciphertext, and not a
	// placeholder row that would misrepresent the item as having had a note here.
	if _, ok := noteFromEvent(e, nil); ok {
		t.Errorf("a reader with no key was handed a note anyway — the confidential path must fail closed")
	}
	// Wrong epoch is the same as no key.
	if _, ok := noteFromEvent(e, testNoteDecryptor{cek: cek, epoch: 2}); ok {
		t.Errorf("a reader holding only a DIFFERENT epoch's key was handed the note — epoch scoping is not being enforced")
	}
}

// testNoteDecryptor is a minimal BoardDecryptor returning a fixed CEK for a fixed
// epoch, so the confidential note test needs no keyring or grant chain.
type testNoteDecryptor struct {
	cek   [32]byte
	epoch int
}

func (d testNoteDecryptor) CEK(_ string, epoch int) ([32]byte, bool) {
	if epoch != d.epoch {
		return [32]byte{}, false
	}
	return d.cek, true
}

// TestNote_QuarantinedOnConfidentialBoardWhenPlaintext proves the fail-closed
// fold gate (§11.3) covers notes too. Without this, a plaintext kind-1111 event
// on a confidential board would fold its cleartext straight into the item's
// trail — the same leak the gate already blocks for a card's description and a
// status event's close reason.
func TestNote_QuarantinedOnConfidentialBoardWhenPlaintext(t *testing.T) {
	k := testKey(t)
	owner := testKey(t)
	board := BoardCoord(owner.PubKeyHex(), "proj")

	plain, err := BuildNoteEvent(k, NoteSpec{ItemID: "proj-1", At: "2026-07-30T20:17Z", Text: "leaked", BoardCoord: board}, 2000)
	if err != nil {
		t.Fatalf("BuildNoteEvent: %v", err)
	}
	// Board went confidential at t=1000, so this t=2000 plaintext note is
	// post-cutover and must NOT be grandfathered.
	ebs := fixedEncryptedBoards{coord: board, cutover: 1000}
	if !shouldQuarantine(plain, ebs) {
		t.Fatalf("a POST-cutover plaintext note on a confidential board was NOT quarantined — its cleartext would fold into the item's trail")
	}
	// A genuine pre-cutover note is grandfathered, exactly as a pre-cutover card is.
	old, err := BuildNoteEvent(k, NoteSpec{ItemID: "proj-1", At: "2026-07-29T09:00Z", Text: "before cutover", BoardCoord: board}, 500)
	if err != nil {
		t.Fatalf("BuildNoteEvent: %v", err)
	}
	if shouldQuarantine(old, ebs) {
		t.Errorf("a genuine PRE-cutover plaintext note was quarantined — history written before the board went confidential must survive")
	}
}

// fixedEncryptedBoards marks exactly one coordinate confidential with a fixed
// cutover.
type fixedEncryptedBoards struct {
	coord   string
	cutover int64
}

func (f fixedEncryptedBoards) Cutover(coord string) (int64, bool) {
	if coord != f.coord {
		return 0, false
	}
	return f.cutover, true
}
