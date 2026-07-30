package foldvectors

// cases_gatelifecycle.go — the GATE LIFECYCLE half of the suite (ready-882).
//
// WHY THIS FILE EXISTS. Before it, the corpus cited §9.4-§9.7 and §9.9 — the
// STANDING-STATE clauses (a card that declares a gate is promoted; a gated item
// that is also blocked keeps its gate; a terminal one drops all but `gate`) —
// and cited none of the TRANSITIONS: §9.1 open, §9.2 approve, §9.3 reject,
// §9.8 clear. A client could therefore render a gate correctly and still be
// wrong about every event sequence that produces or resolves one, which is the
// half a board actually watches change.
//
// EACH VECTOR IS THE FOLD-SIDE COMPLEMENT OF A WRITE VECTOR. testdata/
// write.vectors.json already pins what `rd gate` / `rd approve` / `rd reject`
// EMIT (spec §22.1-§22.5, vectors gate_open / gate_approve /
// gate_reject_keeps_gate_open). The event sequences below are those same wire
// shapes, hand-built here from §22, and what is asserted is the OTHER
// direction — what a reader must project from them (§9.1-§9.3, §9.8). The two
// suites meet in the middle: the writer's output is the reader's input, and
// neither file was derived by running the other side.
//
// THE THREE TRANSITIONS ARE NOT SYMMETRIC, WHICH IS EXACTLY THE DRIFT RISK:
//
//	open    -> a card that NOW carries the gate tags + a `waiting` status event.
//	approve -> a card that DROPS the gate tags + an `active` status event. The
//	           dropped tags are what makes the clear stick: §9.4 finds nothing to
//	           re-promote (§22.2). A client that cleared only the status would
//	           snap the item back to `waiting` on the next read.
// EVERY REPUBLISHED CARD HERE CARRIES A "created" TAG, because every live
// mutation does (§5.6's CardSpecFromItem re-emits the whole item, and §5.1 reads
// Item.CreatedAt off that carried tag rather than off the winning card's own
// wire created_at). So a gate, an approval and a rejection each leave CreatedAt
// at the item's TRUE creation time while UpdatedAt advances — which is the point
// of ready-4ec: before it, gating an item made it look brand new and inverted the
// staleness signal triage reads. A vector that omitted the tag would model a
// writer that no longer exists AND would assert exactly that inversion.
//
//	reject  -> the SAME card again + a status event RE-AFFIRMING the current
//	           status. Not a transition at all: a durable note on a still-open
//	           gate (§9.3, §22.3). A client that treated any status event as a
//	           resolution would silently clear a gate the owner refused to rule
//	           on — and §22.4 says approve and reject are indistinguishable on
//	           the wire EXCEPT by that status value, so guessing is not available.

import (
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// vGateOpen pins §9.1: the event pair `rd gate` publishes projects to the exact
// state §9.1 makes the writer VERIFY before it reports success — `GateMsgID !=
// ""` and `Status ∈ {waiting, blocked}` — which is also precisely what §13.10
// needs for the item to appear in the gates view.
//
// The pre-gate card version is in the log on purpose: the gate is a REVISION of
// a live card (§4.1 latest-wins), not a fresh one, so the vector also pins that
// the winning card is the gated one and that `created_at` tracks it (§5.1).
func (b *builder) vGateOpen() error {
	const desc = "need a design ruling"
	before, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v45", Title: "Needs a ruling", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// The §22.1 wire shape: s=waiting + gate + waiting_type=gate + waiting_on.
	gated, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v45", Title: "Needs a ruling", Status: state.StatusWaiting, Priority: "p1", Type: "task",
		Gate: "design", WaitingType: "gate", WaitingOn: desc, CreatedAt: t0,
	}, t0+100)
	if err != nil {
		return err
	}
	// §22.1: the description IS the status-event reason.
	opened, err := b.status(b.owner, "ready-v45", state.StatusWaiting, desc, t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v45", MsgID: gated.ID, Title: "Needs a ruling", Type: "task", Priority: "p1",
		Status: state.StatusWaiting, Gate: "design", WaitingType: "gate", WaitingOn: desc,
		WaitingSince: rfc(t0 + 100), GateMsgID: gated.ID,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusWaiting, ChangedBy: b.ownerPub, Note: desc},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "gate_open_projects_a_resolvable_gate",
		SpecClauses: []string{"9.1", "9.6", "13.10", "13.3", "13.5", "4.1", "5.1", "5.6", "6.5"},
		Note: "`rd gate` publishes a card revision carrying gate/waiting_type=gate/waiting_on plus a " +
			"kind-1630 status event whose reason IS the description (§22.1). §9.1 then makes the WRITER " +
			"re-read its own projection and refuse to report success unless the fold produced " +
			"gate_msg_id != \"\" AND status in {waiting, blocked} — so those two are not incidental " +
			"output here, they are the open's success condition, and they are what §13.10 needs to put " +
			"the item in the gates view. gate_msg_id is the WINNING card's event id (§9.6): there is no " +
			"gate event, and even the writer learns the id from the fold. waiting_since is derived from " +
			"updated_at, never written (§22.5). A client that projects the gate but leaves gate_msg_id " +
			"empty renders an escalation `rd gates` cannot see and `rd approve` cannot resolve. The gating card " +
			"is a REPUBLISH and carries \"created\": t0 (§5.6), so created_at stays at the item's true " +
			"creation time while updated_at moves to the gate — gating an item does not make it look new.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{before, gated, opened},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v45"}, "focus": {"ready-v45"},
				"pending": {"ready-v45"}, "gates": {"ready-v45"},
			}),
		},
	})
}

// vGateApprove pins §9.2 and §9.8: the approve pair drops the item back to
// `active` and leaves NO gate residue — and, critically, the fold does not
// re-promote it, because the republished card carries no gate tags for §9.4 to
// find.
func (b *builder) vGateApprove() error {
	gated, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v46", Title: "Approve me", Status: state.StatusWaiting, Priority: "p1", Type: "task",
		Gate: "design", WaitingType: "gate", WaitingOn: "need a ruling",
	}, t0)
	if err != nil {
		return err
	}
	opened, err := b.status(b.owner, "ready-v46", state.StatusWaiting, "need a ruling", t0, nil)
	if err != nil {
		return err
	}
	// The §22.2 wire shape: s=active and NONE of the three gate tags.
	approved, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v46", Title: "Approve me", Status: state.StatusActive, Priority: "p1", Type: "task",
		CreatedAt: t0,
	}, t0+100)
	if err != nil {
		return err
	}
	ruling, err := b.status(b.owner, "ready-v46", state.StatusActive, "looks good", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v46", MsgID: approved.ID, Title: "Approve me", Type: "task", Priority: "p1",
		Status: state.StatusActive,
		// gate, waiting_type, waiting_on, waiting_since, gate_msg_id: ALL empty.
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0), FromStatus: "", ToStatus: state.StatusWaiting, ChangedBy: b.ownerPub, Note: "need a ruling"},
			{Timestamp: rfc(t0 + 100), FromStatus: state.StatusWaiting, ToStatus: state.StatusActive, ChangedBy: b.ownerPub, Note: "looks good"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "gate_approve_clears_every_gate_field",
		SpecClauses: []string{"9.2", "9.8", "9.4", "6.5", "5.1", "5.6", "13.4", "13.10"},
		Note: "The full open-then-approve log. The approving card carries s=active and NONE of gate / " +
			"waiting_type / waiting_on (§22.2), so §9.8's no-declared-gate branch clears waiting_on, " +
			"waiting_type, waiting_since AND gate_msg_id, and §9.4 has nothing to promote — the item is " +
			"active, not waiting. `gate` is empty for the same reason the others are: it is a card tag " +
			"and the winning card no longer carries it. Both history rows survive the resolution (§6.5), " +
			"so the ruling is auditable after the gate is gone; §22.4 warns that approve and reject are " +
			"distinguishable ONLY by this waiting -> active transition, since there is no resolution tag " +
			"on the wire. The gates view is empty: a client that kept the earlier card's gate fields, or " +
			"cleared the status alone, would still be showing a resolved escalation as pending. The approving " +
			"card carries \"created\": t0 like every republish (§5.6), so resolving a gate does not " +
			"reset the item's age either.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{gated, opened, approved, ruling},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v46"}, "work": {"ready-v46"}, "focus": {"ready-v46"},
			}),
		},
	})
}

// vGateApproveUnderBlocking pins the second half of §9.2: approving resolves the
// GATE, never the BLOCK. §8.4 recomputes `blocked` on the next fold regardless of
// the published `active`, and the gate fields still clear.
func (b *builder) vGateApproveUnderBlocking() error {
	blocker, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v47a", Title: "Blocker", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	gated, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v47b", Title: "Blocked and gated", Status: state.StatusWaiting, Priority: "p0", Type: "decision",
		Deps: []string{"ready-v47a"}, Gate: "budget", WaitingType: "gate", WaitingOn: "spend approval",
	}, t0)
	if err != nil {
		return err
	}
	// §22.2 again, on an item whose blocker is still live: the dep tag stays, the
	// three gate tags go.
	approved, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v47b", Title: "Blocked and gated", Status: state.StatusActive, Priority: "p0", Type: "decision",
		Deps: []string{"ready-v47a"}, CreatedAt: t0,
	}, t0+100)
	if err != nil {
		return err
	}
	ruling, err := b.status(b.owner, "ready-v47b", state.StatusActive, "spend approved", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v47a", MsgID: blocker.ID, Title: "Blocker", Type: "task", Priority: "p1",
			Status: state.StatusActive, Blocks: []string{"ready-v47b"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v47b", MsgID: approved.ID, Title: "Blocked and gated", Type: "decision", Priority: "p0",
			Status: state.StatusBlocked, BlockedBy: []string{"ready-v47a"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
			History: []state.HistoryEntry{
				{Timestamp: rfc(t0 + 100), FromStatus: "", ToStatus: state.StatusActive, ChangedBy: b.ownerPub, Note: "spend approved"},
			},
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "gate_approve_under_blocking_clears_the_gate_not_the_block",
		SpecClauses: []string{"9.2", "9.8", "8.4", "9.9", "5.1", "5.6", "13.5", "13.10"},
		Note: "§9.2 resolves a gate on a BLOCKED item without unblocking it first — the ruling is often " +
			"exactly what unblocks the chain. The approving card publishes s=active, and the fold " +
			"overrides it back to `blocked` because ready-v47a is still live (§8.4, §9.9: the dep pass " +
			"runs before the gate pass). The gate fields clear anyway (§9.8), so the item leaves the " +
			"gates view while staying in pending: approving answered the question, it did not remove the " +
			"dependency. The published `active` survives in history as the ruling's audit row even though " +
			"it is not the item's status — history records what was WRITTEN, status is DERIVED.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{blocker, gated, approved, ruling},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v47a"}, "work": {"ready-v47a"}, "focus": {"ready-v47a"},
				"pending": {"ready-v47b"},
			}),
		},
	})
}

// vGateReject pins §9.3: reject changes NO field. The card is republished
// unchanged and the status event RE-AFFIRMS `waiting`, so the gate is still open
// and still resolvable — the ruling survives only as history.
func (b *builder) vGateReject() error {
	gated, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v48", Title: "Reject me", Status: state.StatusWaiting, Priority: "p1", Type: "task",
		Gate: "design", WaitingType: "gate", WaitingOn: "need a ruling",
	}, t0)
	if err != nil {
		return err
	}
	opened, err := b.status(b.owner, "ready-v48", state.StatusWaiting, "need a ruling", t0, nil)
	if err != nil {
		return err
	}
	// §22.3: the card is republished UNCHANGED — same tags, later created_at, so
	// a different event id — and the status event re-affirms the current status.
	republished, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v48", Title: "Reject me", Status: state.StatusWaiting, Priority: "p1", Type: "task",
		Gate: "design", WaitingType: "gate", WaitingOn: "need a ruling", CreatedAt: t0,
	}, t0+100)
	if err != nil {
		return err
	}
	ruling, err := b.status(b.owner, "ready-v48", state.StatusWaiting, "not yet", t0+100, nil)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v48", MsgID: republished.ID, Title: "Reject me", Type: "task", Priority: "p1",
		Status: state.StatusWaiting, Gate: "design", WaitingType: "gate", WaitingOn: "need a ruling",
		WaitingSince: rfc(t0 + 100), GateMsgID: republished.ID,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0 + 100),
		History: []state.HistoryEntry{
			{Timestamp: rfc(t0), FromStatus: "", ToStatus: state.StatusWaiting, ChangedBy: b.ownerPub, Note: "need a ruling"},
			{Timestamp: rfc(t0 + 100), FromStatus: state.StatusWaiting, ToStatus: state.StatusWaiting, ChangedBy: b.ownerPub, Note: "not yet"},
		},
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "gate_reject_keeps_the_gate_open",
		SpecClauses: []string{"9.3", "9.6", "6.5", "13.10", "13.5", "4.1", "5.1", "5.6"},
		Note: "Rejecting is not a state transition (§9.3, §22.3): the card comes back unchanged and the " +
			"status event re-affirms `waiting`, producing a SELF-TRANSITION history row (waiting -> " +
			"waiting) carrying the rejection reason. Every gate field survives and the item stays in the " +
			"gates view, so the escalation can be ruled on again. Note gate_msg_id MOVED to the " +
			"republished card's id: §9.6 binds the gate identity to the winning card, so it changes on " +
			"every republish — a client that cached the old id and used it as a stable gate handle would " +
			"be resolving a gate that no longer exists. Contrast gate_approve_clears_every_gate_field, " +
			"whose events are the same shape and differ only in the status value and the dropped tags " +
			"(§22.4): there is no resolution tag to distinguish them by.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  []*nostr.Event{gated, opened, republished, ruling},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v48"}, "focus": {"ready-v48"},
				"pending": {"ready-v48"}, "gates": {"ready-v48"},
			}),
		},
	})
}
