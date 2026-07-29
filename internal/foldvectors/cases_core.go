package foldvectors

// cases_core.go — projection, ordering, status authority, deps and gates.
// Every expected value here is authored from docs/design/board-fold-spec.md;
// see build.go's header for why they are not dumped from a run.

import (
	"encoding/json"
	"errors"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// vCardFieldProjection pins the whole card-tag -> item-field table (§5.1) plus
// the two "never set by the nostr fold" fields (§5.3, §14.9).
func (b *builder) vCardFieldProjection() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID:   "ready-v01",
		Title:    "Full field card",
		Status:   state.StatusInbox,
		Priority: "p1",
		Assignee: b.ownerPub,
		Type:     "task",
		Context:  "the whole description",
		Labels:   []string{"security"},
		ETA:      "2099-01-01T00:00:00Z",
		Level:    "l3",
		For:      "human:baron",
		ParentID: "ready-p00",
		Due:      "2099-02-01T00:00:00Z",
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID:          "ready-v01",
		MsgID:       c.ID,
		Title:       "Full field card",
		Context:     "the whole description",
		Description: "the whole description",
		Type:        "task",
		Level:       "l3",
		For:         "human:baron",
		By:          b.ownerPub,
		Priority:    "p1",
		Status:      state.StatusInbox,
		ETA:         "2099-01-01T00:00:00Z",
		Due:         "2099-02-01T00:00:00Z",
		ParentID:    "ready-p00",
		Labels:      []string{"security"},
		CreatedAt:   nanos(t0),
		UpdatedAt:   nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "card_field_projection",
		SpecClauses: []string{"5.1", "5.2", "5.3", "6.11", "4.6", "14.9"},
		Note: "One plaintext card carrying every projected tag. Pins the tag->field table, the " +
			"seconds->nanoseconds conversion, and the absence of campfire_id / label_warnings / " +
			"cross_campfire_warnings from the nostr JSON surface. No status event, so the card's " +
			"`s` tag stands as current status and history is empty (§6.11).",
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: b.ownerPub,
		Events:   []*nostr.Event{c},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v01"}, "focus": {"ready-v01"}, "my-work": {"ready-v01"},
			}),
		},
	})
}

// vLabelsFreeform records that the nostr fold applies NO label validation
// (§10.1, §10.2) — the campfire fold's drop-into-LabelWarnings behaviour has no
// nostr counterpart. This is the spec's open question §15.3; the vector pins
// today's behaviour, it does not bless it.
func (b *builder) vLabelsFreeform() error {
	labels := []string{"security", "NOT_AN_ATOM!!", "a-label-far-longer-than-the-campfire-32-char-atom-pattern-allows"}
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v02", Title: "Freeform labels", Status: state.StatusInbox,
		Priority: "p2", Type: "task", Labels: labels,
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v02", MsgID: c.ID, Title: "Freeform labels",
		Type: "task", Priority: "p2", Status: state.StatusInbox,
		Labels: labels, CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "labels_freeform_no_validation",
		SpecClauses: []string{"10.1", "10.2", "13.12", "13.9", "15.3"},
		Note: "An unknown/ill-formed label atom is KEPT VERBATIM by the nostr fold: no atom-pattern " +
			"check, no registry check, no label_warnings field. Asserting the campfire drop-with-warning " +
			"behaviour here would assert something the live fold does not do (spec §15.3 records the " +
			"question). Also pins LabelFilter's exact match and the empty-identity rule (§13.9): with " +
			"identity \"\", delegated and my-work match nothing.",
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: "",
		Events:   []*nostr.Event{c},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v02"}, "focus": {"ready-v02"}}),
			LabelViews: map[string][]string{
				"security":      {"ready-v02"},
				"NOT_AN_ATOM!!": {"ready-v02"},
				"secur":         {},
			},
		},
	})
}

// vCardLatestWins pins §4.1's primary key and §4.3's order-independence: the
// newer card is listed FIRST in the log, and still wins.
func (b *builder) vCardLatestWins() error {
	older, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v03", Title: "older", Status: state.StatusInbox, Priority: "p2", Type: "task",
		Context: "first write",
	}, t0)
	if err != nil {
		return err
	}
	newer, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v03", Title: "newer", Status: state.StatusActive, Priority: "p1", Type: "task",
		Context: "second write",
	}, t0+100)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v03", MsgID: newer.ID, Title: "newer",
		Context: "second write", Description: "second write",
		Type: "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(t0 + 100), UpdatedAt: nanos(t0 + 100),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "card_latest_wins_created_at",
		SpecClauses: []string{"4.1", "4.3"},
		Note: "Two cards for one item; the greater created_at wins the CONTENT contest even though it " +
			"appears FIRST in the log (§4.1/§4.3). CreatedAt (ready-4ec REWORK) is read from the winning " +
			"card's CARRIED \"created\" tag, not derived by scanning every admitted event -- a derived " +
			"min() is subset-sensitive (a relay retains only the latest addressable card, so a " +
			"relay-bootstrapped machine never sees the older card at all, and would disagree with a " +
			"full-log machine about the minimum). Neither card here carries a \"created\" tag (this " +
			"vector builds raw wire events directly, bypassing CardSpecFromItem's carry-forward), so " +
			"CreatedAt falls back to the winning card's OWN created_at -- the same value UpdatedAt " +
			"reports. A real CLI republish always forwards the prior CreatedAt via CardSpecFromItem, so " +
			"in production this reset does not occur; see TestProjection_CreatedAtSurvivesMutation " +
			"(pkg/sync/nostrreplay_test.go) for the carried-tag proof.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{newer, older},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v03"}, "work": {"ready-v03"}, "focus": {"ready-v03"},
			}),
		},
	})
}

// vCardTieLowestID pins §4.1's tiebreak: on equal created_at the
// lexicographically LOWEST event id wins (NIP-01 replaceable rule).
func (b *builder) vCardTieLowestID() error {
	alpha, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v04", Title: "alpha", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	beta, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v04", Title: "beta", Status: state.StatusInbox, Priority: "p3", Type: "bug",
	}, t0)
	if err != nil {
		return err
	}
	// The winner is a property of the fixture bytes (the id is a content hash),
	// so it is READ here rather than guessed — but WHICH of the two wins is the
	// spec claim, and it is asserted through the expected item below.
	winner, loser := alpha, beta
	if beta.ID < alpha.ID {
		winner, loser = beta, alpha
	}
	if !(winner.ID < loser.ID) {
		return errors.New("tie fixture produced identical event ids")
	}
	want := &state.Item{
		ID: "ready-v04", MsgID: winner.ID, Title: "alpha",
		Type: "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	}
	viewSets := map[string][]string{
		"ready": {"ready-v04"}, "work": {"ready-v04"}, "focus": {"ready-v04"},
	}
	if winner == beta {
		want.Title, want.Type, want.Priority, want.Status = "beta", "bug", "p3", state.StatusInbox
		viewSets = map[string][]string{"ready": {"ready-v04"}, "focus": {"ready-v04"}}
	}
	items, err := itemsJSON(want)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "card_tie_lowest_event_id_wins",
		SpecClauses: []string{"4.1", "4.3"},
		Note: "Two cards for one item at the SAME created_at. The lexicographically lowest event id is " +
			"retained (matching NIP-01's replaceable-event rule and strfry's tie-break), so the winner is " +
			"a pure function of the event set and never of log order. The two fixtures carry different " +
			"title/type/priority/status so the tie-break is observable in the projection, not just in MsgID.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{alpha, beta},
		Expect:  Expect{Items: items, Views: vw(viewSets)},
	})
}

// vCardCarriedCreatedTagSurvivesRepublish pins §5.1/§5.6's carried "created"
// tag mechanism (ready-4ec): a republished card's Item.CreatedAt comes from the
// CARRIED "created" tag, NOT from that card's own wire created_at, so an
// item's true creation time survives every later republish even though the
// card's own timestamp keeps advancing. card_latest_wins_created_at and
// trust_gate_disabled_admits_anyone (both in this corpus) build raw wire
// events with NO "created" tag at all -- they pin the FALLBACK path only, and
// by construction cannot distinguish "reads the tag" from "reads the event's
// own created_at", since the two agree whenever the tag is absent. THIS
// vector is the one place in the corpus where they would diverge (the
// republished card's own created_at is t0+500, but its carried tag says t0),
// so it is the cross-implementation proof that BOTH the Go fold
// (pkg/sync.ProjectItems, checked below by Build()) and the TS port
// (web/board/src/lib/fold.ts's itemFromCard, replayed by fold.vectors.test.ts)
// read the tag rather than the event's own timestamp.
func (b *builder) vCardCarriedCreatedTagSurvivesRepublish() error {
	// Genesis card @t0: no "created" tag (CreatedAt: 0 is the zero value), so
	// both folds fall back to this card's OWN created_at (t0) -- the one
	// bootstrap case where that fallback is correct.
	genesis, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v34", Title: "v0", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// Republish @t0+500: the card CARRIES "created": t0 explicitly (exactly
	// what CardSpecFromItem does on every live `rd` mutation), even though this
	// card's OWN wire created_at has moved on to t0+500. A fold that read the
	// card's own created_at instead of the tag would wrongly report t0+500.
	republished, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v34", Title: "v1 edited", Status: state.StatusActive, Priority: "p1",
		Assignee: b.ownerPub, Type: "task", CreatedAt: t0,
	}, t0+500)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v34", MsgID: republished.ID, Title: "v1 edited",
		Type: "task", Priority: "p1", Status: state.StatusActive, By: b.ownerPub,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 500),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "card_carried_created_tag_survives_republish",
		SpecClauses: []string{"4.1", "5.1", "5.6"},
		Note: "Genesis card @t0 carries no \"created\" tag (falls back to its own created_at, t0). " +
			"It is republished @t0+500 CARRYING \"created\": t0 (as CardSpecFromItem does on every " +
			"live mutation) even though the republished card's OWN wire created_at is t0+500. " +
			"Item.CreatedAt must read t0 (the carried tag), not t0+500 (the winning card's own " +
			"created_at) -- this is the one vector in the corpus where the tag and the event's own " +
			"timestamp actually disagree, so it is the cross-implementation (Go/TS) proof of the " +
			"carried-tag mechanism itself, not just of its absent-tag fallback.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{genesis, republished},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v34"}, "work": {"ready-v34"}, "focus": {"ready-v34"},
			}),
		},
	})
}

// vDedupByEventID pins §3.2: re-ingesting an identical event is a no-op, so a
// duplicated status event cannot fabricate a phantom history row.
func (b *builder) vDedupByEventID() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v05", Title: "Dedup", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	s, err := b.status(b.owner, "ready-v05", state.StatusActive, "claimed", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v05", MsgID: c.ID, Title: "Dedup",
		Type: "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.ownerPub, Note: "claimed"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "dedup_by_event_id",
		SpecClauses: []string{"3.2", "6.5"},
		Note:        "Card and status event each appear TWICE in the log. Exactly one history entry results.",
		Options:     Options{Trusted: trust(b.ownerPub)},
		Events:      []*nostr.Event{c, s, c, s},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v05"}, "work": {"ready-v05"}, "focus": {"ready-v05"}}),
		},
	})
}

// vStatusChainHistory pins §4.2 ordering, §6.5 history emission, §6.9 UpdatedAt
// and §6.10 current status.
func (b *builder) vStatusChainHistory() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v06", Title: "Chain", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	done, err := b.status(b.owner, "ready-v06", state.StatusDone, "shipped", t0+300, nil)
	if err != nil {
		return err
	}
	active, err := b.status(b.owner, "ready-v06", state.StatusActive, "claimed", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v06", MsgID: c.ID, Title: "Chain",
		Type: "task", Priority: "p1", Status: state.StatusDone,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 300),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.ownerPub, Note: "claimed"},
			{Timestamp: rfc(t0 + 300), FromStatus: state.StatusActive, ToStatus: state.StatusDone, ChangedBy: b.ownerPub, Note: "shipped"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "status_chain_history_replay",
		SpecClauses: []string{"4.2", "6.4", "6.5", "6.9", "6.10", "7.2"},
		Note: "Status events are supplied NEWEST-FIRST in the log and must be replayed in " +
			"(created_at, id) ascending order: from_status chains, the last entry sets current status, " +
			"UpdatedAt advances to the newest status event. The item is terminal, so every view is empty.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{c, done, active},
		Expect:  Expect{Items: items, Views: vw(nil)},
	})
}

// vStatusSameSecondTiebreak pins the second half of §4.2: same created_at is
// broken by ascending event id, and the winner decides CURRENT status.
func (b *builder) vStatusSameSecondTiebreak() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v07", Title: "Same second", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	toActive, err := b.status(b.owner, "ready-v07", state.StatusActive, "claimed", t0+100, nil)
	if err != nil {
		return err
	}
	toWaiting, err := b.status(b.owner, "ready-v07", state.StatusWaiting, "parked", t0+100, nil)
	if err != nil {
		return err
	}
	first, second := toActive, toWaiting
	if toWaiting.ID < toActive.ID {
		first, second = toWaiting, toActive
	}
	entry := func(e *nostr.Event, from string) state.HistoryEntry {
		st, note := state.StatusActive, "claimed"
		if e == toWaiting {
			st, note = state.StatusWaiting, "parked"
		}
		return state.HistoryEntry{Timestamp: rfc(t0 + 100), FromStatus: from, ToStatus: st, ChangedBy: b.ownerPub, Note: note}
	}
	h1 := entry(first, "")
	h2 := entry(second, h1.ToStatus)
	want := &state.Item{
		ID: "ready-v07", MsgID: c.ID, Title: "Same second",
		Type: "task", Priority: "p1", Status: h2.ToStatus,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{h1, h2},
	}
	// A waiting item with no card-declared gate is still READY (§13.3 excludes
	// only terminal / blocked / scheduled) but is also PENDING (§13.5).
	sets := map[string][]string{"ready": {"ready-v07"}, "focus": {"ready-v07"}}
	if want.Status == state.StatusActive {
		sets["work"] = []string{"ready-v07"}
	} else {
		sets["pending"] = []string{"ready-v07"}
	}
	items, err := itemsJSON(want)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "status_same_second_tiebreak_by_event_id",
		SpecClauses: []string{"4.2", "4.3", "6.10", "13.3", "13.5"},
		Note: "Two authoritative status events share a created_at and carry DIFFERENT statuses, so the " +
			"(created_at, id) tie-break is observable in both the history order and the resulting current " +
			"status. Inverting the tie-break flips this vector.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{c, toActive, toWaiting},
		Expect:  Expect{Items: items, Views: vw(sets)},
	})
}

// vStatusMissingStatusTag pins §6.6: a status event with no `status` tag
// inherits the previous status — the KIND is never consulted as a fallback.
func (b *builder) vStatusMissingStatusTag() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v08", Title: "No status tag", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	active, err := b.status(b.owner, "ready-v08", state.StatusActive, "claimed", t0+100, nil)
	if err != nil {
		return err
	}
	// Hand-built: no builder emits a status event without a status tag.
	bare := &nostr.Event{
		Kind:      rdsync.KindStatusOpen,
		CreatedAt: t0 + 200,
		Tags: [][]string{
			{"a", rdsync.CardCoord(b.ownerPub, "ready-v08")},
			{"d", "ready-v08"},
			{"a", b.boardCoord},
		},
		Content: "note without a status tag",
	}
	if err := bare.Sign(b.owner); err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v08", MsgID: c.ID, Title: "No status tag",
		Type: "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 200),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.ownerPub, Note: "claimed"},
			{Timestamp: rfc(t0 + 200), FromStatus: state.StatusActive, ToStatus: state.StatusActive, ChangedBy: b.ownerPub, Note: "note without a status tag"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "status_event_without_status_tag_inherits",
		SpecClauses: []string{"6.6", "6.5", "2.3"},
		Note: "A kind-1630 status event carrying no `status` tag still emits a history row, with " +
			"to_status inherited from the previous entry. The NIP-34 kind is NOT used as a fallback " +
			"status source.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{c, active, bare},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v08"}, "work": {"ready-v08"}, "focus": {"ready-v08"}}),
		},
	})
}

// vStatusNonAuthoritative pins §6.4: a READ-TRUSTED key that is neither the item
// author nor a board maintainer contributes neither state nor history.
func (b *builder) vStatusNonAuthoritative() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v09", Title: "Authority", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	hijack, err := b.status(b.agent, "ready-v09", state.StatusDone, "closing someone else's item", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v09", MsgID: c.ID, Title: "Authority",
		Type: "task", Priority: "p1", Status: state.StatusInbox,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "status_from_non_authority_ignored",
		SpecClauses: []string{"6.4", "6.11", "3.4"},
		Note: "The agent key IS read-trusted (it passes §3.4) but is neither the card author nor a board " +
			"maintainer, so its status event is excluded ENTIRELY — no status change and no history row. " +
			"Read-trust and status-authority are separate gates.",
		Options: Options{Trusted: trust(b.ownerPub, b.agentPub)},
		Events:  []*nostr.Event{c, hijack},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v09"}, "focus": {"ready-v09"}}),
		},
	})
}

// vBoardMaintainerAuthority pins §6.1 / §2.1: a board `p` tag confers
// status authority on the board's coordinate.
func (b *builder) vBoardMaintainerAuthority() error {
	board, err := rdsync.BuildBoardEvent(b.owner, rdsync.BoardSpec{
		BoardD: boardD, Title: "Ready", Maintainers: []string{b.maintPub},
	}, t0-100)
	if err != nil {
		return err
	}
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v10", Title: "Maintainer authority", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	s, err := b.status(b.maint, "ready-v10", state.StatusActive, "picked up by the second machine", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v10", MsgID: c.ID, Title: "Maintainer authority",
		Type: "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.maintPub, Note: "picked up by the second machine"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "board_p_tag_confers_status_authority",
		SpecClauses: []string{"2.1", "6.1", "6.4", "3.6"},
		Note: "A 30301 board naming the maintainer key in a `p` tag makes that key's status events " +
			"authoritative for cards whose `a` coordinate is that board. The board event itself produces " +
			"no item.",
		Options: Options{Trusted: trust(b.ownerPub, b.maintPub)},
		Events:  []*nostr.Event{board, c, s},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v10"}, "work": {"ready-v10"}, "focus": {"ready-v10"}}),
		},
	})
}

// vBoardLatestWinsRevokesMaintainer pins §4.5: only the WINNING board's `p` tags
// name maintainers, so republishing a board without a `p` tag revokes it.
func (b *builder) vBoardLatestWinsRevokesMaintainer() error {
	boardV1, err := rdsync.BuildBoardEvent(b.owner, rdsync.BoardSpec{
		BoardD: boardD, Title: "Ready", Maintainers: []string{b.maintPub},
	}, t0-200)
	if err != nil {
		return err
	}
	boardV2, err := rdsync.BuildBoardEvent(b.owner, rdsync.BoardSpec{
		BoardD: boardD, Title: "Ready",
	}, t0-100)
	if err != nil {
		return err
	}
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v11", Title: "Revoked maintainer", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	s, err := b.status(b.maint, "ready-v11", state.StatusDone, "no longer authorised", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v11", MsgID: c.ID, Title: "Revoked maintainer",
		Type: "task", Priority: "p1", Status: state.StatusInbox,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "board_latest_wins_revokes_maintainer",
		SpecClauses: []string{"4.5", "6.1", "6.4"},
		Note: "Historical boards are NOT unioned. The newest board for the coordinate drops the `p` tag, " +
			"so the former maintainer's status event is no longer authoritative and the item does not close.",
		Options: Options{Trusted: trust(b.ownerPub, b.maintPub)},
		Events:  []*nostr.Event{boardV1, boardV2, c, s},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v11"}, "focus": {"ready-v11"}}),
		},
	})
}

// vBySpoofGuard pins §6.7: the provenance-rewriting `by` tag is honoured only
// from a board maintainer.
func (b *builder) vBySpoofGuard() error {
	board, err := rdsync.BuildBoardEvent(b.owner, rdsync.BoardSpec{BoardD: boardD, Title: "Ready"}, t0-100)
	if err != nil {
		return err
	}
	cardA, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v12a", Title: "Spoof attempt", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	cardB, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v12b", Title: "Migrated entry", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// Signed by the card AUTHOR, who is not a board maintainer -> `by` ignored.
	spoof, err := rdsync.BuildHistoricalStatusEventWithBoard(b.agent, "ready-v12a", state.StatusActive,
		"victim@example.com", b.boardCoord, "attributing to someone else", t0+100)
	if err != nil {
		return err
	}
	// Signed by the board author (an implicit maintainer) -> `by` honoured.
	migrated, err := rdsync.BuildHistoricalStatusEventWithBoard(b.owner, "ready-v12b", state.StatusActive,
		"orig@campfire", b.boardCoord, "replayed campfire history", t0+100)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v12a", MsgID: cardA.ID, Title: "Spoof attempt",
			Type: "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
			History: []state.HistoryEntry{
				{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.agentPub, Note: "attributing to someone else"},
			},
		},
		&state.Item{
			ID: "ready-v12b", MsgID: cardB.ID, Title: "Migrated entry",
			Type: "task", Priority: "p1", Status: state.StatusActive,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
			History: []state.HistoryEntry{
				{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: "orig@campfire", Note: "replayed campfire history"},
			},
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "by_tag_spoof_guard",
		SpecClauses: []string{"6.7", "6.1"},
		Note: "Both items carry an rd-extension `by` tag. On ready-v12a the signer is the bare item " +
			"author, so `by` is IGNORED and changed_by falls back to the signer pubkey. On ready-v12b the " +
			"signer is the board author (an implicit maintainer), so the migrated campfire actor survives.",
		Options: Options{Trusted: trust(b.ownerPub, b.agentPub)},
		Events:  []*nostr.Event{board, cardA, cardB, spoof, migrated},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v12a", "ready-v12b"},
				"work":  {"ready-v12a", "ready-v12b"},
				"focus": {"ready-v12a", "ready-v12b"},
			}),
		},
	})
}

// vStatusKind1633 pins §2.3 / §15.4: kind 1633 is accepted by isStatusKind even
// though rd never writes it, so a foreign client's draft mutates rd state.
func (b *builder) vStatusKind1633() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v13", Title: "Draft kind", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	draft := &nostr.Event{
		Kind:      rdsync.KindStatusDraft,
		CreatedAt: t0 + 100,
		Tags: [][]string{
			{"a", rdsync.CardCoord(b.ownerPub, "ready-v13")},
			{"d", "ready-v13"},
			{"status", state.StatusActive},
			{"a", b.boardCoord},
		},
		Content: "draft transition from a foreign client",
	}
	if err := draft.Sign(b.owner); err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v13", MsgID: c.ID, Title: "Draft kind",
		Type: "task", Priority: "p1", Status: state.StatusActive,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.ownerPub, Note: "draft transition from a foreign client"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "status_kind_1633_folds_like_any_status",
		SpecClauses: []string{"2.3", "6.6", "14.10", "15.4"},
		Note: "isStatusKind is the range 1630..1633, so a kind-1633 draft event folds as an ordinary " +
			"status transition. rd never writes 1633; spec §15.4 records the open question of whether it " +
			"should be accepted at all. This vector pins the current behaviour so a decision to exclude " +
			"1633 is a DELIBERATE, visible change.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{c, draft},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v13"}, "work": {"ready-v13"}, "focus": {"ready-v13"}}),
		},
	})
}

// vStatusWithoutCard pins §3.11: no surviving card, no item — status events
// alone are neither an item nor an error.
func (b *builder) vStatusWithoutCard() error {
	s1, err := b.status(b.owner, "ready-v14", state.StatusActive, "claimed", t0, nil)
	if err != nil {
		return err
	}
	s2, err := b.status(b.owner, "ready-v14", state.StatusDone, "shipped", t0+100, nil)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "status_without_card_projects_nothing",
		SpecClauses: []string{"3.11"},
		Note:        "An item exists iff it has at least one surviving card. Orphan status events are silently inert.",
		Options:     Options{Trusted: trust(b.ownerPub)},
		Events:      []*nostr.Event{s1, s2},
		Expect:      Expect{Items: []json.RawMessage{}, Views: vw(nil)},
	})
}

// vDepChainBlocked pins §8.1 / §8.4 / §8.5 on a 3-item chain.
func (b *builder) vDepChainBlocked() error {
	a, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v15a", Title: "Step 1", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	bb, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v15b", Title: "Step 2", Status: state.StatusActive, Priority: "p1", Type: "task",
		Deps: []string{"ready-v15a"},
	}, t0)
	if err != nil {
		return err
	}
	cc, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v15c", Title: "Step 3", Status: state.StatusActive, Priority: "p1", Type: "task",
		Deps: []string{"ready-v15b"},
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v15a", MsgID: a.ID, Title: "Step 1", Type: "task", Priority: "p1",
			Status: state.StatusActive, Blocks: []string{"ready-v15b"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v15b", MsgID: bb.ID, Title: "Step 2", Type: "task", Priority: "p1",
			Status: state.StatusBlocked, BlockedBy: []string{"ready-v15a"}, Blocks: []string{"ready-v15c"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v15c", MsgID: cc.ID, Title: "Step 3", Type: "task", Priority: "p1",
			Status: state.StatusBlocked, BlockedBy: []string{"ready-v15b"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "dep_chain_blocks_downstream",
		SpecClauses: []string{"8.1", "8.4", "8.5", "7.6", "13.3", "13.5"},
		Note: "Raw `i` tags are drained into validated edges: blocked_by/blocks are populated on both " +
			"ends and `blocked` is DERIVED (it overrides the card's own `s` tag). Only the head of the " +
			"chain is ready; the tail is pending.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{a, bb, cc},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v15a"}, "work": {"ready-v15a"}, "focus": {"ready-v15a"},
				"pending": {"ready-v15b", "ready-v15c"},
			}),
		},
	})
}

// vDepTerminalEdges pins §8.3 (terminal BLOCKED item contributes no edge at all)
// and §8.4/§8.5 (a terminal BLOCKER still records the edge but stops blocking).
func (b *builder) vDepTerminalEdges() error {
	a, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v16a", Title: "Finished blocker", Status: state.StatusDone, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	bb, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v16b", Title: "Unblocked", Status: state.StatusActive, Priority: "p1", Type: "task",
		Deps: []string{"ready-v16a"},
	}, t0)
	if err != nil {
		return err
	}
	cc, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v16c", Title: "Closed while depending", Status: state.StatusDone, Priority: "p2", Type: "task",
		Deps: []string{"ready-v16d"},
	}, t0)
	if err != nil {
		return err
	}
	d, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v16d", Title: "Live blocker", Status: state.StatusActive, Priority: "p2", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v16a", MsgID: a.ID, Title: "Finished blocker", Type: "task", Priority: "p1",
			Status: state.StatusDone, Blocks: []string{"ready-v16b"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v16b", MsgID: bb.ID, Title: "Unblocked", Type: "task", Priority: "p1",
			Status: state.StatusActive, BlockedBy: []string{"ready-v16a"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v16c", MsgID: cc.ID, Title: "Closed while depending", Type: "task", Priority: "p2",
			Status: state.StatusDone, CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v16d", MsgID: d.ID, Title: "Live blocker", Type: "task", Priority: "p2",
			Status: state.StatusActive, CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "dep_terminal_edges",
		SpecClauses: []string{"8.3", "8.4", "8.5", "8.7"},
		Note: "Two distinct terminal rules. A terminal BLOCKER (v16a) still records blocks/blocked_by but " +
			"no longer sets `blocked` — this is why rd needs no explicit unblock event. A terminal BLOCKED " +
			"item (v16c) is skipped entirely: neither its blocked_by nor the blocker's blocks list gains " +
			"an entry. Inverting the blocker-terminal test flips v16b to blocked and empties the ready view.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{a, bb, cc, d},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v16b", "ready-v16d"},
				"work":  {"ready-v16b", "ready-v16d"},
				"focus": {"ready-v16b", "ready-v16d"},
			}),
		},
	})
}

// vDepUnresolvableAndCrossBoard pins §8.2 and §8.9 / §15.2: an unresolvable dep
// — including a cross-board reference — is dropped SILENTLY, with no warning
// field. The campfire fold's "non-blocking WITH warnings" has no nostr twin.
func (b *builder) vDepUnresolvableAndCrossBoard() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v17", Title: "Dangling deps", Status: state.StatusActive, Priority: "p1", Type: "task",
		Deps: []string{"ready-nosuchitem", "galtrader/galtrader-d0a"},
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v17", MsgID: c.ID, Title: "Dangling deps", Type: "task", Priority: "p1",
		Status: state.StatusActive, CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "dep_unresolvable_and_cross_board_dropped_silently",
		SpecClauses: []string{"8.2", "8.9", "15.2"},
		Note: "A dep on an item absent from this event set, and a cross-board `repo/item` reference, are " +
			"both dropped: no blocked_by entry, no blocking, and — the part that differs from the campfire " +
			"fold — NO cross_campfire_warnings field. Spec §15.2 records the question of whether the " +
			"warning should be restored; a vector asserting warnings here would assert behaviour the live " +
			"fold does not have.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{c},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v17"}, "work": {"ready-v17"}, "focus": {"ready-v17"}}),
		},
	})
}

// vDepCycle pins §8.6: cycles are not detected; every member blocks the others.
func (b *builder) vDepCycle() error {
	a, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v18a", Title: "Cycle A", Status: state.StatusActive, Priority: "p1", Type: "task",
		Deps: []string{"ready-v18b"},
	}, t0)
	if err != nil {
		return err
	}
	bb, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v18b", Title: "Cycle B", Status: state.StatusActive, Priority: "p1", Type: "task",
		Deps: []string{"ready-v18a"},
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v18a", MsgID: a.ID, Title: "Cycle A", Type: "task", Priority: "p1",
			Status: state.StatusBlocked, BlockedBy: []string{"ready-v18b"}, Blocks: []string{"ready-v18b"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v18b", MsgID: bb.ID, Title: "Cycle B", Type: "task", Priority: "p1",
			Status: state.StatusBlocked, BlockedBy: []string{"ready-v18a"}, Blocks: []string{"ready-v18a"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "dep_cycle_not_detected",
		SpecClauses: []string{"8.6", "8.4", "8.5"},
		Note:        "A 2-cycle deadlocks both items into `blocked`. The fold neither detects nor reports it.",
		Options:     Options{Trusted: trust(b.ownerPub)},
		Events:      []*nostr.Event{a, bb},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"pending": {"ready-v18a", "ready-v18b"}}),
		},
	})
}

// vGatePromotion pins §9.4 (card-declared gate promotes to waiting), §9.6
// (GateMsgID only for waiting_type=gate; WaitingSince derived) and §13.10.
func (b *builder) vGatePromotion() error {
	gated, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v19a", Title: "Gated", Status: state.StatusActive, Priority: "p0", Type: "decision",
		Gate: "design", WaitingType: "gate", WaitingOn: "needs a ruling on the envelope",
	}, t0)
	if err != nil {
		return err
	}
	waiting, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v19b", Title: "Waiting on a human", Status: state.StatusActive, Priority: "p1", Type: "task",
		WaitingType: "human", WaitingOn: "baron",
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v19a", MsgID: gated.ID, Title: "Gated", Type: "decision", Priority: "p0",
			Status: state.StatusWaiting, Gate: "design", WaitingType: "gate",
			WaitingOn: "needs a ruling on the envelope", WaitingSince: rfc(t0), GateMsgID: gated.ID,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v19b", MsgID: waiting.ID, Title: "Waiting on a human", Type: "task", Priority: "p1",
			Status: state.StatusWaiting, WaitingType: "human", WaitingOn: "baron", WaitingSince: rfc(t0),
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "gate_promotion_and_gate_msg_id",
		SpecClauses: []string{"9.4", "9.6", "13.3", "13.5", "13.10", "5.4"},
		Note: "Both cards declare a wait and carry `s=active`; both are PROMOTED to waiting at fold time. " +
			"gate_msg_id is set to the winning card's event id ONLY when waiting_type is exactly \"gate\", " +
			"which is what makes the gates view non-empty. Note a waiting item is still READY: §13.3 " +
			"excludes only terminal, blocked and scheduled.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{gated, waiting},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v19a", "ready-v19b"}, "focus": {"ready-v19a", "ready-v19b"},
				"pending": {"ready-v19a", "ready-v19b"}, "gates": {"ready-v19a"},
			}),
		},
	})
}

// vGateUnderBlocking pins §9.7: blocking supersedes the STATUS but never clears
// the gate fields.
func (b *builder) vGateUnderBlocking() error {
	blocker, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v20a", Title: "Blocker", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	gated, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v20b", Title: "Blocked and gated", Status: state.StatusActive, Priority: "p0", Type: "decision",
		Deps: []string{"ready-v20a"}, Gate: "budget", WaitingType: "gate", WaitingOn: "spend approval",
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v20a", MsgID: blocker.ID, Title: "Blocker", Type: "task", Priority: "p1",
			Status: state.StatusActive, Blocks: []string{"ready-v20b"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v20b", MsgID: gated.ID, Title: "Blocked and gated", Type: "decision", Priority: "p0",
			Status: state.StatusBlocked, BlockedBy: []string{"ready-v20a"},
			Gate: "budget", WaitingType: "gate", WaitingOn: "spend approval",
			WaitingSince: rfc(t0), GateMsgID: gated.ID,
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "gate_fields_persist_under_blocking",
		SpecClauses: []string{"9.7", "9.9", "8.4", "13.10"},
		Note: "The gated item also gains a live blocker. Status becomes `blocked` (the dep pass runs " +
			"first and the gate promotion checks it), but gate/waiting_type/waiting_on/waiting_since/" +
			"gate_msg_id all survive — the pending gate is still real. It STILL appears in the gates " +
			"view (ready-e0e): blocked-and-gated is the ordinary case for a design gate, since the " +
			"ruling is usually exactly what unblocks the chain, so GatesFilter accepts status=waiting " +
			"OR status=blocked, not waiting alone.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{blocker, gated},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v20a"}, "work": {"ready-v20a"}, "focus": {"ready-v20a"},
				"pending": {"ready-v20b"}, "gates": {"ready-v20b"},
			}),
		},
	})
}

// vGateTerminalClears pins §9.5 / §15.5: a terminal item clears waiting_on,
// waiting_type, waiting_since and gate_msg_id — but NOT `gate`.
func (b *builder) vGateTerminalClears() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v21", Title: "Closed while gated", Status: state.StatusActive, Priority: "p0", Type: "decision",
		Gate: "design", WaitingType: "gate", WaitingOn: "a ruling that never came",
	}, t0)
	if err != nil {
		return err
	}
	closed, err := b.status(b.owner, "ready-v21", state.StatusCancelled, "abandoned", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v21", MsgID: c.ID, Title: "Closed while gated", Type: "decision", Priority: "p0",
		Status: state.StatusCancelled, Gate: "design",
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusCancelled, ChangedBy: b.ownerPub, Note: "abandoned"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "gate_terminal_clears_all_but_gate",
		SpecClauses: []string{"9.5", "7.2", "15.5"},
		Note: "A cancelled item drops waiting_on / waiting_type / waiting_since / gate_msg_id but KEEPS " +
			"`gate`, so `rd show` still reports the escalation category it died under. Spec §15.5 records " +
			"the open question of whether the retained `gate` is deliberate provenance or a missed clear.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{c, closed},
		Expect:  Expect{Items: items, Views: vw(nil)},
	})
}

// vIssueDoesNotFold pins §2.4: the NIP-34 kind-1621 issue event carries a `d`
// tag with the item id and still produces nothing.
func (b *builder) vIssueDoesNotFold() error {
	spec := rdsync.CardSpec{
		ItemID: "ready-v22", Title: "Interop", Status: state.StatusInbox, Priority: "p1", Type: "task",
		Context: "has a NIP-34 issue anchor",
	}
	c, err := b.card(b.owner, spec, t0)
	if err != nil {
		return err
	}
	spec.BoardD, spec.BoardAuthor = boardD, b.ownerPub
	issue, err := rdsync.BuildIssueEvent(b.owner, spec, t0+10)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v22", MsgID: c.ID, Title: "Interop",
		Context: "has a NIP-34 issue anchor", Description: "has a NIP-34 issue anchor",
		Type: "task", Priority: "p1", Status: state.StatusInbox,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "issue_1621_does_not_fold",
		SpecClauses: []string{"2.4", "2.6", "3.7"},
		Note: "The kind-1621 issue event exists purely for generic-client interop. Despite carrying a `d` " +
			"tag naming the item, itemIDForEvent returns \"\" for it, so it neither creates nor mutates " +
			"an item — MsgID and UpdatedAt still come from the card.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{c, issue},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v22"}, "focus": {"ready-v22"}}),
		},
	})
}
