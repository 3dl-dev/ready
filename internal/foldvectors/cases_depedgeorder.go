package foldvectors

import (
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// vDepEdgeArrayOrderIsDeterministic pins §8.1a — the edge sort — as a
// LANGUAGE-NEUTRAL contract rather than a Go-only regression test.
//
// WHY THIS VECTOR HAD TO EXIST. §8.1a made `Blocks`/`BlockedBy` array order
// deterministic by sorting edges on `(blockedID, blockerID)` before applying
// them, and spec §15.7 records that a vector written before that fix "would
// have been flaky by construction". But EVERY vector in this file tops out at
// ONE entry per edge array, and a one-element array has exactly one ordering —
// so the suite could not tell a conforming implementation from one that emits
// edges in whatever order it happened to visit items in. The Go side had
// TestReadyCmd_RunE_ByteIdenticalAcrossNRuns_WithDepEdges; the vector file,
// which is what an INDEPENDENT client is held to, had nothing.
//
// HOW IT DISCRIMINATES. Both halves are built so that the order an
// implementation reaches the edges in is the REVERSE of the order §8.1a
// requires — a client that skips the sort does not merely risk a different
// answer, it gets a deterministically WRONG one, on every run, in both
// directions of the edge:
//
//   - The BLOCKS half: v23a blocks three items, and their cards are appended to
//     the event list in DESCENDING id order (d, c, b). An implementation that
//     walks items in ingestion order and appends as it goes produces
//     Blocks: [d, c, b]; §8.1a requires [b, c, d].
//   - The BLOCKED_BY half: v23e carries its three `i` tags in DESCENDING id
//     order (h, g, f). An implementation that drains raw tags in tag order
//     produces BlockedBy: [h, g, f]; §8.1a requires [f, g, h].
//
// Neither half is redundant. §8.1a's sort is keyed on `blockedID` FIRST, so
// `BlockedBy` order is fixed by the sort directly while `Blocks` order is only
// a CONSEQUENCE of blockedID being the primary key. A sort that used
// `blockerID` as the primary key would still pass the blocked_by half and fail
// the blocks half.
func (b *builder) vDepEdgeArrayOrderIsDeterministic() error {
	blocker, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v23a", Title: "One blocker, three blockees", Status: state.StatusActive,
		Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	// Appended d, c, b — descending, so ingestion order is the reverse of the
	// order §8.1a requires in the blocker's Blocks array.
	blockedD, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v23d", Title: "Blockee D", Status: state.StatusActive, Priority: "p1", Type: "task",
		Deps: []string{"ready-v23a"},
	}, t0)
	if err != nil {
		return err
	}
	blockedC, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v23c", Title: "Blockee C", Status: state.StatusActive, Priority: "p1", Type: "task",
		Deps: []string{"ready-v23a"},
	}, t0)
	if err != nil {
		return err
	}
	blockedB, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v23b", Title: "Blockee B", Status: state.StatusActive, Priority: "p1", Type: "task",
		Deps: []string{"ready-v23a"},
	}, t0)
	if err != nil {
		return err
	}
	// Raw `i` tags in DESCENDING order, so tag order is the reverse of the order
	// §8.1a requires in this item's BlockedBy array.
	multiBlocked, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v23e", Title: "Three blockers, one blockee", Status: state.StatusActive,
		Priority: "p1", Type: "task",
		Deps: []string{"ready-v23h", "ready-v23g", "ready-v23f"},
	}, t0)
	if err != nil {
		return err
	}
	blockerF, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v23f", Title: "Blocker F", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	blockerG, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v23g", Title: "Blocker G", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}
	blockerH, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v23h", Title: "Blocker H", Status: state.StatusActive, Priority: "p1", Type: "task",
	}, t0)
	if err != nil {
		return err
	}

	items, err := itemsJSON(
		&state.Item{
			ID: "ready-v23a", MsgID: blocker.ID, Title: "One blocker, three blockees", Type: "task", Priority: "p1",
			Status: state.StatusActive,
			Blocks: []string{"ready-v23b", "ready-v23c", "ready-v23d"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v23b", MsgID: blockedB.ID, Title: "Blockee B", Type: "task", Priority: "p1",
			Status: state.StatusBlocked, BlockedBy: []string{"ready-v23a"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v23c", MsgID: blockedC.ID, Title: "Blockee C", Type: "task", Priority: "p1",
			Status: state.StatusBlocked, BlockedBy: []string{"ready-v23a"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v23d", MsgID: blockedD.ID, Title: "Blockee D", Type: "task", Priority: "p1",
			Status: state.StatusBlocked, BlockedBy: []string{"ready-v23a"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v23e", MsgID: multiBlocked.ID, Title: "Three blockers, one blockee", Type: "task", Priority: "p1",
			Status:    state.StatusBlocked,
			BlockedBy: []string{"ready-v23f", "ready-v23g", "ready-v23h"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v23f", MsgID: blockerF.ID, Title: "Blocker F", Type: "task", Priority: "p1",
			Status: state.StatusActive, Blocks: []string{"ready-v23e"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v23g", MsgID: blockerG.ID, Title: "Blocker G", Type: "task", Priority: "p1",
			Status: state.StatusActive, Blocks: []string{"ready-v23e"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
		&state.Item{
			ID: "ready-v23h", MsgID: blockerH.ID, Title: "Blocker H", Type: "task", Priority: "p1",
			Status: state.StatusActive, Blocks: []string{"ready-v23e"},
			CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
		},
	)
	if err != nil {
		return err
	}

	return b.add(Vector{
		Name:        "dep_edge_arrays_are_sorted_not_visit_order",
		SpecClauses: []string{"8.1a", "8.1", "8.5", "8.4"},
		Note: "§8.1a: BlockedBy and Blocks are ORDERED, not merely present. Both halves are built so " +
			"that visit order is the REVERSE of the required order — the blockees' cards arrive " +
			"descending (d, c, b) and the multi-blocked item's raw `i` tags are descending (h, g, f) — " +
			"so an implementation that appends edges in the order it reaches them fails on every run " +
			"rather than flaking. Sorting on `(blockedID, blockerID)` satisfies both halves; sorting on " +
			"blockerID first satisfies only the blocked_by half.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events: []*nostr.Event{
			blocker, blockedD, blockedC, blockedB,
			multiBlocked, blockerF, blockerG, blockerH,
		},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready":   {"ready-v23a", "ready-v23f", "ready-v23g", "ready-v23h"},
				"work":    {"ready-v23a", "ready-v23f", "ready-v23g", "ready-v23h"},
				"focus":   {"ready-v23a", "ready-v23f", "ready-v23g", "ready-v23h"},
				"pending": {"ready-v23b", "ready-v23c", "ready-v23d", "ready-v23e"},
			}),
		},
	})
}
