// Progress notes as their OWN events (ready-ed4) — the fix that makes an rd
// item's trail unbounded-safe.
//
// BEFORE: `rd progress` appended its note to Item.Context, and Item.Context is
// the addressable kind-30302 card's Content. Every note rewrote the whole card,
// larger, forever, against a 64 KiB relay cap. That is not a slow leak: most of
// ready-f75's measured 39,921 bytes were written by ONE day's orchestrator
// dispatch, in multi-thousand-character notes across four rework rounds. On
// another board it had already hit the wall — vms-760 could not publish a note
// at all and its live state was abandoned to a new item.
//
// AFTER: a note is a kind-1111 event of its own, referencing the item. The card
// carries only the base description and stops growing. The trail is assembled at
// READ time (ProjectItems → Item.Notes → state.AssembleTrail), so growth is
// bounded per EVENT, not per item, and no amount of progress can make the card
// unpublishable.
//
// WHY KIND 1111: NIP-22's generic comment kind is the standard "a text comment
// about another event", which is exactly what a progress note is; rd already
// sits on the NIP-34 issue/status family (1621, 1630-1632) that NIP-22 is the
// designated comment kind for. This is deliberately NOT a full NIP-22 event —
// rd carries the same rd-convention tags its kind-163x status events already
// carry (card-coordinate "a" FIRST, then the "d" item-id lookup tag, then the
// board-membership "a"), rather than NIP-22's uppercase root/parent tag pairs,
// because rd's own fold, its negentropy filters and the board's REQ filters all
// key off that convention and a second, divergent tag shape on one kind would
// mean two ways to say the same thing. The deviation is documented in
// board-fold-spec.md §2.7; a generic NIP-22 client sees an ordinary comment with
// extra tags it ignores.
//
// THE BOARD "a" TAG IS NOT OPTIONAL. It is what makes a note event match
// BoardSyncFilter and the browser's board-scoped REQ (ready-7ec found this
// exact hole for status events: they carried only the card coordinate, so a
// board-scoped sync silently fetched cards and missed every status event). A
// note that a board-scoped reader cannot fetch is a note the trail loses on any
// machine but the one that wrote it — which is the same continuity loss, just
// arriving by a different road.
package sync

import (
	"fmt"
	"sort"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// KindNote is the NIP-22 comment kind rd publishes one of per progress note.
const KindNote = 1111

// tagNoteTS is the clear tag carrying a note's DISPLAY timestamp in
// state.NoteTimestampLayout. It is carried rather than derived from the event's
// own created_at for the same reason the card carries a "created" tag: when the
// fold recovers a note that was embedded in a legacy card, that note's real time
// is the one in its "[<ts>] " prefix, while the event minting it is being
// created NOW. Deriving would silently restamp the whole recovered trail with
// the moment of recovery.
const tagNoteTS = "ts"

// isNoteKind reports whether e is a progress-note event. An explicit equality
// test, mirroring isStatusKind's explicit-allowlist shape (ready-816) rather
// than any range.
func isNoteKind(kind int) bool { return kind == KindNote }

// NoteSpec is the input to BuildNoteEvent: one progress note, plus the anchors
// that bind it to its item and board.
type NoteSpec struct {
	// ItemID is the rd item the note is about; becomes the "d" lookup tag.
	ItemID string
	// At is the note's display timestamp (state.NoteTimestampLayout); becomes
	// the "ts" tag. Empty emits no tag and the fold falls back to the event's own
	// created_at (spec §5.8).
	At string
	// Text is the note body; becomes the event Content (sealed when Enc is set).
	Text string
	// CardEventID, when set, anchors the note to the concrete card event id it
	// was written against ("e" tag) — advisory provenance, exactly as on a status
	// event. The authoritative anchor is the "a" card coordinate.
	CardEventID string
	// BoardCoord is the item's board-membership coordinate. See this file's
	// header: without it a board-scoped reader never fetches the note.
	BoardCoord string
	// Enc, when non-nil, seals Text into Content and adds the clear enc/cek_epoch
	// markers — a note is free text, so on a confidential board it MUST be sealed
	// exactly like a status event's close reason, or the fail-closed fold gate
	// (shouldQuarantine) will drop it and the trail will silently lose entries.
	Enc *Envelope
}

// BuildNoteEvent constructs and signs the kind-1111 progress-note event.
// createdAt MUST be seconds.
func BuildNoteEvent(k *nostr.Key, spec NoteSpec, createdAt int64) (*nostr.Event, error) {
	if spec.ItemID == "" {
		return nil, fmt.Errorf("sync: note event: empty item id")
	}
	// Card coordinate FIRST, then d — the same tag order BuildStatusEvent uses, so
	// tagValue(e, "a") (which reads only the first match) resolves to the card
	// coordinate on a note exactly as it does on a status event.
	tags := [][]string{
		{"a", CardCoord(k.PubKeyHex(), spec.ItemID)},
		{"d", spec.ItemID},
	}
	if spec.At != "" {
		tags = append(tags, []string{tagNoteTS, spec.At})
	}
	if spec.CardEventID != "" {
		tags = append(tags, []string{"e", spec.CardEventID})
	}
	if spec.BoardCoord != "" {
		tags = append(tags, []string{"a", spec.BoardCoord})
	}
	content := spec.Text
	if spec.Enc != nil {
		sealed, err := sealNotePayload(spec.Enc, spec.Text)
		if err != nil {
			return nil, fmt.Errorf("sync: seal note content: %w", err)
		}
		content = sealed
		tags = append(tags, encMarkerTags(spec.Enc)...)
	}
	e := &nostr.Event{
		Kind:      KindNote,
		CreatedAt: createdAt,
		Tags:      tags,
		Content:   content,
	}
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: sign note event: %w", err)
	}
	return e, nil
}

// foldedNote is a note event reduced to what the trail needs, plus the fields
// the ordering tiebreak reads. Kept internal: state.ProgressNote is the shape
// that leaves the fold.
type foldedNote struct {
	note      state.ProgressNote
	createdAt int64
}

// noteFromEvent projects a kind-1111 event onto a trail entry, decrypting its
// text on a confidential board. It returns ok=false when the note is
// confidential and this reader cannot open it — a note whose text cannot be read
// is DROPPED from the trail rather than rendered as a placeholder, because a
// trail is a sequence of things that were said and a row of "[encrypted]" lines
// carries no information while actively misrepresenting the item's history as
// having that many notes. The card's own fail-closed placeholder (§11) still
// tells the reader the item is unreadable to them.
func noteFromEvent(e *nostr.Event, dec BoardDecryptor) (foldedNote, bool) {
	text := e.Content
	if isConfidential(e) {
		plain, ok := decryptNoteText(e, dec)
		if !ok {
			return foldedNote{}, false
		}
		text = plain
	}
	at := tagValue(e, tagNoteTS)
	if at == "" {
		at = state.FormatNoteTimestamp(e.CreatedAt)
	}
	return foldedNote{
		note:      state.ProgressNote{At: at, Text: text, MsgID: e.ID},
		createdAt: e.CreatedAt,
	}, true
}

// assembleTrail merges the notes a legacy card still carries embedded in its
// Content with the item's own note events, into the item's ordered trail.
//
// THE ORDER IS NORMATIVE (board-fold-spec.md §5.9) and both readers implement
// it identically:
//
//  1. ascending "at", compared as a STRING — NoteTimestampLayout is fixed-width
//     and zero-padded, so lexicographic order IS chronological order and neither
//     reader has to parse a date (nor agree on what to do with an unparseable
//     one);
//  2. ties: card-embedded notes before note events — a recovered legacy note
//     always predates the event that will later mint it;
//  3. ties within card-embedded notes: their order in the card content;
//  4. ties within note events: ascending created_at, then ascending event id —
//     the same (created_at, id) total order §4 already uses everywhere else.
//
// The sort is stable so rules 3 and 4 fall out of input order for the card notes
// and are applied explicitly for the events.
func assembleTrail(cardNotes []state.ProgressNote, events []foldedNote) []state.ProgressNote {
	if len(cardNotes) == 0 && len(events) == 0 {
		return nil
	}
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].createdAt != events[j].createdAt {
			return events[i].createdAt < events[j].createdAt
		}
		return events[i].note.MsgID < events[j].note.MsgID
	})
	out := make([]state.ProgressNote, 0, len(cardNotes)+len(events))
	out = append(out, cardNotes...)
	for _, fe := range events {
		out = append(out, fe.note)
	}
	// Rule 1 with rule 2/3/4 preserved: a stable sort on "at" alone keeps the
	// card-notes-then-events order this slice was built in for every tie.
	sort.SliceStable(out, func(i, j int) bool { return out[i].At < out[j].At })
	return out
}

// PendingNotes returns the trail entries that have NO event of their own yet —
// the notes a legacy card still carries inline. Every card-publishing path mints
// an event for each of these alongside the compacted card (Publisher.publish*),
// so shrinking the card can never delete the trail from a relay's copy.
func PendingNotes(notes []state.ProgressNote) []state.ProgressNote {
	var out []state.ProgressNote
	for _, n := range notes {
		if n.MsgID == "" {
			out = append(out, n)
		}
	}
	return out
}
