package foldvectors

// cases_notes.go — the PROGRESS TRAIL (ready-ed4, spec §2.7, §5.7-§5.10).
//
// These are the vectors that keep the Go fold and web/board's TypeScript fold
// telling the same story about an item's history. The trail is now assembled from
// two sources — kind-1111 note events and whatever a LEGACY card still carries
// inline — and if the two folds split or ordered those differently, the same
// events would render as two different histories depending on which reader you
// asked. That is invisible to the Go test suite alone, which is exactly what the
// vector file exists to catch.

import (
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// note builds and signs a kind-1111 progress note carrying BOTH the card
// coordinate (first "a") and the board coordinate (second "a") — the live write
// path's shape, and what a board-scoped reader needs to fetch it at all.
func (b *builder) note(k *nostr.Key, itemID, at, text string, createdAt int64, env *rdsync.Envelope) (*nostr.Event, error) {
	return rdsync.BuildNoteEvent(k, rdsync.NoteSpec{
		ItemID:     itemID,
		At:         at,
		Text:       text,
		BoardCoord: b.boardCoord,
		Enc:        env,
	}, createdAt)
}

// vNotesFoldIntoTrail is the base case: a card whose content is JUST the
// description, plus three note events. The card contributes Context; the notes
// contribute Notes, in ascending `at`. Nothing about the card grows.
func (b *builder) vNotesFoldIntoTrail() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID:  "ready-v90",
		Title:   "Item with a trail",
		Status:  state.StatusActive,
		Context: "the base description",
	}, t0)
	if err != nil {
		return err
	}
	n1, err := b.note(b.owner, "ready-v90", "2026-07-30T10:00Z", "first note", t0+10, nil)
	if err != nil {
		return err
	}
	n2, err := b.note(b.owner, "ready-v90", "2026-07-30T11:00Z", "second note", t0+20, nil)
	if err != nil {
		return err
	}
	n3, err := b.note(b.owner, "ready-v90", "2026-07-30T12:00Z", "third note", t0+30, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID:          "ready-v90",
		MsgID:       c.ID,
		Title:       "Item with a trail",
		Context:     "the base description",
		Description: "the base description",
		Status:      state.StatusActive,
		CreatedAt:   nanos(t0),
		UpdatedAt:   nanos(t0),
		Notes: []state.ProgressNote{
			{At: "2026-07-30T10:00Z", Text: "first note", MsgID: n1.ID},
			{At: "2026-07-30T11:00Z", Text: "second note", MsgID: n2.ID},
			{At: "2026-07-30T12:00Z", Text: "third note", MsgID: n3.ID},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "notes_fold_into_trail",
		SpecClauses: []string{"2.7", "5.7", "5.8", "5.9"},
		Note: "Three kind-1111 notes on a card whose content is only the description. Context stays " +
			"the BASE description (that separation is what stops the card growing until the item " +
			"cannot be published), the notes land in Notes ordered by ascending `at`, and each " +
			"carries its own event id as msg_id. Events are supplied OUT of chronological order to " +
			"prove the fold sorts rather than preserving arrival order.",
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: b.ownerPub,
		// Deliberately out of order on the wire.
		Events: []*nostr.Event{n2, c, n3, n1},
		Expect: Expect{Items: items, Views: vw(map[string][]string{"ready": {"ready-v90"}, "work": {"ready-v90"}, "focus": {"ready-v90"}})},
	})
}

// vLegacyCardTrailSplits is the RECOVERY vector: a card written by the
// pre-ready-ed4 appender, with its whole trail welded into the content. The fold
// must split it — Context back to the base description, the inline notes into
// Notes with NO msg_id. That empty msg_id is what makes the next write mint the
// missing events, so an already-over-limit item (vms-760) becomes writable again.
func (b *builder) vLegacyCardTrailSplits() error {
	legacy := "the base description" +
		"\n\n[2026-07-30T10:00Z] legacy note one" +
		"\n\n[2026-07-30T11:00Z] legacy note two\n\nwith a second paragraph"
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID:  "ready-v91",
		Title:   "Legacy item",
		Status:  state.StatusActive,
		Context: legacy,
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID:          "ready-v91",
		MsgID:       c.ID,
		Title:       "Legacy item",
		Context:     "the base description",
		Description: "the base description",
		Status:      state.StatusActive,
		CreatedAt:   nanos(t0),
		UpdatedAt:   nanos(t0),
		Notes: []state.ProgressNote{
			{At: "2026-07-30T10:00Z", Text: "legacy note one"},
			{At: "2026-07-30T11:00Z", Text: "legacy note two\n\nwith a second paragraph"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "legacy_card_trail_splits",
		SpecClauses: []string{"5.7", "5.9"},
		Note: "A card carrying its whole progress trail inline, exactly as every pre-ready-ed4 rd " +
			"wrote it. The fold splits it: Context drops back to the base description, the inline " +
			"notes become Notes with NO msg_id (they have no event of their own YET — that emptiness " +
			"is what makes the next card republish mint them, so compaction cannot delete the trail " +
			"from a relay's copy). The second note's trailing blank-line paragraph is a CONTINUATION " +
			"of that note, not a third note.",
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: b.ownerPub,
		Events:   []*nostr.Event{c},
		Expect:   Expect{Items: items, Views: vw(map[string][]string{"ready": {"ready-v91"}, "work": {"ready-v91"}, "focus": {"ready-v91"}})},
	})
}

// vLegacyTrailMergesWithNoteEvents pins the §5.9 tie-break that only a mixed item
// exercises: a legacy card that ALSO has note events, with a timestamp collision
// between the two sources. Card-embedded notes come first on a tie.
func (b *builder) vLegacyTrailMergesWithNoteEvents() error {
	legacy := "base" +
		"\n\n[2026-07-30T10:00Z] from the card" +
		"\n\n[2026-07-30T12:00Z] later, from the card"
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID:  "ready-v92",
		Title:   "Mixed trail",
		Status:  state.StatusActive,
		Context: legacy,
	}, t0)
	if err != nil {
		return err
	}
	// Same `at` as the card's first note — the tie-break case.
	tie, err := b.note(b.owner, "ready-v92", "2026-07-30T10:00Z", "same minute, from an event", t0+10, nil)
	if err != nil {
		return err
	}
	mid, err := b.note(b.owner, "ready-v92", "2026-07-30T11:00Z", "between the two card notes", t0+20, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID:          "ready-v92",
		MsgID:       c.ID,
		Title:       "Mixed trail",
		Context:     "base",
		Description: "base",
		Status:      state.StatusActive,
		CreatedAt:   nanos(t0),
		UpdatedAt:   nanos(t0),
		Notes: []state.ProgressNote{
			{At: "2026-07-30T10:00Z", Text: "from the card"},
			{At: "2026-07-30T10:00Z", Text: "same minute, from an event", MsgID: tie.ID},
			{At: "2026-07-30T11:00Z", Text: "between the two card notes", MsgID: mid.ID},
			{At: "2026-07-30T12:00Z", Text: "later, from the card"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "legacy_trail_merges_with_note_events",
		SpecClauses: []string{"5.7", "5.9"},
		Note: "A legacy card that has ALSO taken notes since — the state every recovering item passes " +
			"through. Ordering is by ascending `at`; on a TIE the card-embedded note comes first, " +
			"because a note recovered from the card always predates the event that will later mint " +
			"it. An event-sourced note whose `at` falls BETWEEN two card notes interleaves correctly, " +
			"which a naive 'card notes then event notes' concatenation would get wrong.",
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: b.ownerPub,
		Events:   []*nostr.Event{c, mid, tie},
		Expect:   Expect{Items: items, Views: vw(map[string][]string{"ready": {"ready-v92"}, "work": {"ready-v92"}, "focus": {"ready-v92"}})},
	})
}

// vNoteFromNonAuthoritativeSignerFolds pins §5.10: a note folds on read-trust
// ALONE, with none of the author-or-maintainer narrowing §6.3 applies to status
// events. A granted contributor recording their work is the normal workflow.
func (b *builder) vNoteFromNonAuthoritativeSignerFolds() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID:  "ready-v93",
		Title:   "Owner's item",
		Status:  state.StatusActive,
		Context: "base",
	}, t0)
	if err != nil {
		return err
	}
	// b.agent is trusted for READ but is neither the card's author nor a board
	// maintainer — its STATUS events are excluded by §6.3, its notes are not.
	n, err := b.note(b.agent, "ready-v93", "2026-07-30T10:00Z", "contributor note", t0+10, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID:          "ready-v93",
		MsgID:       c.ID,
		Title:       "Owner's item",
		Context:     "base",
		Description: "base",
		Status:      state.StatusActive,
		CreatedAt:   nanos(t0),
		UpdatedAt:   nanos(t0),
		Notes:       []state.ProgressNote{{At: "2026-07-30T10:00Z", Text: "contributor note", MsgID: n.ID}},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "note_from_non_authoritative_signer_folds",
		SpecClauses: []string{"5.10"},
		Note: "A note signed by a READ-TRUSTED key that is NOT the item author and NOT a board " +
			"maintainer. It folds — a note cannot change one field of the item, it only appends a " +
			"line to the trail, so read-trust IS the whole authority rule. The same key's STATUS " +
			"event would be excluded by §6.3; requiring that rank here would silently drop every " +
			"note an agent key writes on an owner-authored item.",
		Options:  Options{Trusted: trust(b.ownerPub, b.agentPub)},
		Identity: b.ownerPub,
		Events:   []*nostr.Event{c, n},
		Expect:   Expect{Items: items, Views: vw(map[string][]string{"ready": {"ready-v93"}, "work": {"ready-v93"}, "focus": {"ready-v93"}})},
	})
}

// vNoteEventsSameMinuteTiebreak pins §5.9 RULE 4 — ties between two NOTE EVENTS
// resolve on ascending created_at, then ascending event id.
//
// It exists because the first version of this vector set did not cover it, and a
// mutation that REVERSED the TypeScript fold's created_at comparison passed all
// 60 vectors: every other case had distinct `at` values, so the final sort on
// `at` masked whatever order the event pass produced. A burst of notes inside one
// minute is exactly what an orchestrator writes, so the masked rule is the one
// most likely to fire in production — on the very workload that motivated
// ready-ed4.
func (b *builder) vNoteEventsSameMinuteTiebreak() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID:  "ready-v94",
		Title:   "Burst of notes",
		Status:  state.StatusActive,
		Context: "base",
	}, t0)
	if err != nil {
		return err
	}
	const sameMinute = "2026-07-30T10:00Z"
	// Distinct created_at, SAME display minute: order must follow created_at.
	early, err := b.note(b.owner, "ready-v94", sameMinute, "written first", t0+10, nil)
	if err != nil {
		return err
	}
	late, err := b.note(b.owner, "ready-v94", sameMinute, "written second", t0+11, nil)
	if err != nil {
		return err
	}
	// SAME created_at as each other AND the same minute: order must follow event id.
	tieA, err := b.note(b.owner, "ready-v94", sameMinute, "same second, note A", t0+12, nil)
	if err != nil {
		return err
	}
	tieB, err := b.note(b.owner, "ready-v94", sameMinute, "same second, note B", t0+12, nil)
	if err != nil {
		return err
	}
	lowID, highID := tieA, tieB
	lowText, highText := "same second, note A", "same second, note B"
	if tieB.ID < tieA.ID {
		lowID, highID = tieB, tieA
		lowText, highText = highText, lowText
	}
	items, err := itemsJSON(&state.Item{
		ID:          "ready-v94",
		MsgID:       c.ID,
		Title:       "Burst of notes",
		Context:     "base",
		Description: "base",
		Status:      state.StatusActive,
		CreatedAt:   nanos(t0),
		UpdatedAt:   nanos(t0),
		Notes: []state.ProgressNote{
			{At: sameMinute, Text: "written first", MsgID: early.ID},
			{At: sameMinute, Text: "written second", MsgID: late.ID},
			{At: sameMinute, Text: lowText, MsgID: lowID.ID},
			{At: sameMinute, Text: highText, MsgID: highID.ID},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "note_events_same_minute_tiebreak",
		SpecClauses: []string{"5.9"},
		Note: "Four notes sharing ONE display minute — the burst an orchestrator actually writes. " +
			"Because every `at` is equal, the `at` sort cannot decide anything and rule 4 alone " +
			"fixes the order: ascending created_at, then ascending event id for the two minted in " +
			"the same second. Supplied in reverse on the wire. Without this vector a reader could " +
			"reverse its event ordering entirely and still pass every other case.",
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: b.ownerPub,
		Events:   []*nostr.Event{highID, lowID, late, early, c},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v94"}, "work": {"ready-v94"}, "focus": {"ready-v94"}}),
		},
	})
}
