package foldvectors

// cases_grantbinding.go — the two grant-replay clauses the escalation-cap
// vectors (cases_grantauthority.go, ready-ce8) leave uncovered: §12.2
// full-coordinate binding and §12.9 one replay behind every consumer
// (ready-882).
//
// The cap vectors all bind their grants to the OWNER's coordinate on purpose, so
// that a rejection there is unambiguously about the cap. That deliberate choice
// leaves the binding itself — WHICH board a grant speaks for — asserted nowhere,
// and it is the fail-closed rule with the widest blast radius in §12: a grant is
// a capability, and a client that scopes it too loosely lets a grant issued on
// one board admit its holder to another.
//
// Both vectors here follow cases_grantauthority.go's discipline, because the
// same trap applies: with `options.trusted == null` or `options.pinned_board ==
// ""` the whole grant machinery is inert and any expectation about it is
// vacuous. Both run with the gates ON, and vectors_test.go asserts that
// structurally.

import (
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// vGrantCoordinateBinding pins §12.2: only a grant whose `a` names BOTH
// `owner == boardAuthor` AND `d == boardD` is replayed.
//
// Each decoy is defective in exactly ONE of the two components and correct in
// the other, so neither can be rejected for any reason but the binding: both are
// signed by the real board owner, both carry a valid role, both would be
// cap-VALID (§12.5's owner branch) had they named this board. A client that
// matched on the owner alone, or on the `d` alone — the two obvious ways to get
// this half-right — admits exactly one decoy each and the vector says which.
func (b *builder) vGrantCoordinateBinding() error {
	// CORRECT BINDING: 30301:<owner>:ready.
	gAgent, err := b.grant(b.owner, b.agentPub, rdsync.RoleContributor, "owner admits the agent to THIS board", t0-300)
	if err != nil {
		return err
	}
	// WRONG d, right owner: a real grant the same owner issued on a DIFFERENT
	// board of their own. Replaying it here would let one board's membership leak
	// into every other board that owner runs.
	gOtherBoard, err := rdsync.BuildRoleGrantEvent(b.owner, rdsync.RoleGrantSpec{
		BoardD: "other-board", BoardAuthor: b.ownerPub, Grantee: b.outsiderPub,
		Role: rdsync.RoleContributor, Label: "owner admits the outsider to a DIFFERENT board",
	}, t0-200)
	if err != nil {
		return err
	}
	// WRONG owner, right d: a coordinate naming a different board author who
	// happens to use the same `d`. `d` values are project names ("ready"), so
	// collisions across owners are the NORM, not an attack shape.
	gForeignOwner, err := rdsync.BuildRoleGrantEvent(b.owner, rdsync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: b.agentPub, Grantee: b.maintPub,
		Role: rdsync.RoleContributor, Label: "a grant on ANOTHER owner's board of the same name",
	}, t0-100)
	if err != nil {
		return err
	}
	// POSITIVE CONTROL: the correctly-bound grantee's card folds.
	cAgent, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v49a", Title: "authored by the correctly-bound grantee", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	cOutsider, err := b.card(b.outsider, rdsync.CardSpec{
		ItemID: "ready-v49b", Title: "authored on the strength of another BOARD's grant", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0+100)
	if err != nil {
		return err
	}
	cMaint, err := b.card(b.maint, rdsync.CardSpec{
		ItemID: "ready-v49c", Title: "authored on the strength of another OWNER's grant", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0+200)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v49a", MsgID: cAgent.ID, Title: "authored by the correctly-bound grantee",
		Type: "task", Priority: "p1", Status: state.StatusInbox,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "grant_binding_requires_the_full_board_coordinate",
		SpecClauses: []string{"12.2", "12.1", "12.8", "3.4", "12.4"},
		Note: "Three owner-signed, cap-valid role=contributor grants; only ONE names this board's full " +
			"coordinate 30301:<owner>:ready. The decoys are each wrong in exactly one component and " +
			"right in the other — same owner but d=\"other-board\", and d=\"ready\" but a different board " +
			"author — so the only possible reason to reject them is §12.2's requirement that BOTH match. " +
			"Neither grantee enters the level map, so §12.8/§3.4 never admits them and ready-v49b and " +
			"ready-v49c produce NO items; ready-v49a, authored by the correctly-bound grantee, does " +
			"fold, so the two absences are about binding and not about trust wiring. Loosening either " +
			"half is a capability leak: match on the owner alone and one project's members can write to " +
			"every other project that owner runs; match on `d` alone and any stranger who names their " +
			"board \"ready\" can grant themselves write access to yours.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{gAgent, gOtherBoard, gForeignOwner, cAgent, cOutsider, cMaint},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{"ready": {"ready-v49a"}, "focus": {"ready-v49a"}}),
		},
	})
}

// vGrantReplayIsSharedByBothConsumers pins §12.9: `deriveGrants` is ONE replay
// standing behind every consumer, so the graded read-trust set and the level map
// cannot drift apart.
//
// The fold sees two of those consumers (`DeriveReadTrust` via §3.4/§12.8 and
// `DeriveLevels` via §6.2); `ClaimGrantee` and `DeriveAllowlist` are off the fold
// path entirely and no vector can reach them. What makes this case a §12.9 case
// rather than a restatement of either clause on its own is that the two visible
// consumers must return OPPOSITE answers for the SAME key from the SAME replay:
//
//	read-trust: a REVOKED key stays in the set (§12.8 says so explicitly, so its
//	            pre-revocation events remain admissible under §3.5) ->
//	            its own card still folds.
//	levels:     the same key is level 0 (§12.3) -> it is not a status authority,
//	            so its transition on an item it does NOT author is ignored.
//
// A client that kept two replays — the near-universal shortcut "trusted = anyone
// with a live grant" alongside a separate level map — gets exactly one of those
// two wrong, and which one tells you which replay diverged.
func (b *builder) vGrantReplayIsSharedByBothConsumers() error {
	gAgent, err := b.grant(b.owner, b.agentPub, rdsync.RoleContributor, "owner admits the agent machine", t0-300)
	if err != nil {
		return err
	}
	// The OWNER authors this item, so §6.4's author branch cannot explain the
	// agent's status event being honoured or ignored — only its level can.
	cOwner, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v50a", Title: "owner-authored item", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0-250)
	if err != nil {
		return err
	}
	cAgent, err := b.card(b.agent, rdsync.CardSpec{
		ItemID: "ready-v50b", Title: "authored by the agent before its revoke", Status: state.StatusInbox, Priority: "p1", Type: "task",
	}, t0-200)
	if err != nil {
		return err
	}
	// BEFORE the revoke instant, so §3.5 admits the EVENT: whether it lands is
	// purely a question of the agent's derived LEVEL.
	sAgent, err := b.status(b.agent, "ready-v50a", state.StatusActive, "agent claims an item it does not own", t0-150, nil)
	if err != nil {
		return err
	}
	revoke, err := b.grant(b.owner, b.agentPub, rdsync.RoleRevoked, "machine decommissioned", t0-100)
	if err != nil {
		return err
	}
	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v50a", MsgID: cOwner.ID, Title: "owner-authored item",
			Type: "task", Priority: "p1", Status: state.StatusInbox,
			CreatedAt: nanos(t0 - 250), UpdatedAt: nanos(t0 - 250),
		},
		&state.Item{
			ID: "ready-v50b", MsgID: cAgent.ID, Title: "authored by the agent before its revoke",
			Type: "task", Priority: "p1", Status: state.StatusInbox,
			CreatedAt: nanos(t0 - 200), UpdatedAt: nanos(t0 - 200),
		},
	)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "grant_replay_answers_read_trust_and_levels_consistently",
		SpecClauses: []string{"12.9", "12.8", "12.3", "6.2", "6.4", "3.5"},
		Note: "One grant set, one replay, two consumers, OPPOSITE answers about the SAME key — which is " +
			"what §12.9 buys by putting `deriveGrants` behind all of them. The agent is granted " +
			"contributor and later revoked. Read-trust KEEPS it (§12.8 retains revoked grantees at " +
			"level 0 precisely so their past events stay admissible), so ready-v50b — its own card, " +
			"authored before the revoke instant — folds. The level map simultaneously reports 0 " +
			"(§12.3), below the §6.2 threshold of 2, so its status event on ready-v50a is ignored and " +
			"the OWNER's item stays `inbox` with an EMPTY history: the event was admitted by §3.5 " +
			"(t0-150 is before the revoke at t0-100) and then discarded for want of authority, which is " +
			"the discrimination this vector exists for. Two separate replays get exactly one of these " +
			"wrong: drop revoked keys from read-trust and ready-v50b disappears; treat read-trust as " +
			"authority and a decommissioned machine closes items it does not own.",
		Options: Options{Trusted: trust(b.ownerPub), PinnedBoard: b.boardCoord},
		Events:  []*nostr.Event{gAgent, cOwner, cAgent, sAgent, revoke},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v50a", "ready-v50b"}, "focus": {"ready-v50a", "ready-v50b"},
			}),
		},
	})
}
