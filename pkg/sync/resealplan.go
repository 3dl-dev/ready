// The re-seal DRY RUN (ready-43d, epic ready-336).
//
// THIS IS THE GATE BEFORE ANY WRITE. Eight live projects share this board set, and
// the operator approves per board before execution touches anything. So the value of
// this file is not that it plans a re-seal — it is that it plans one WITHOUT writing,
// and can be shown not to write rather than trusted not to.
//
// WHY "TRUSTED READ-ONLY" IS NOT ENOUGH HERE. This project has a documented case of a
// republish that was believed to be a no-op and was not (ready-500: a same-second
// replacement that sorted BEHIND the chain it meant to supersede, while every local
// signal reported success). A dry run inherits exactly that credibility problem, so
// BuildResealPlan is structured so the proof is available two independent ways:
//
//  1. STRUCTURALLY — it is handed a relay URL and calls only the paginated READ path
//     (fetchBoardEventsFromRelay). It constructs no Publisher, holds no key, and
//     signs nothing; there is no code path from here to a write. The test asserts
//     this the only way that cannot be argued with: against a fixture relay that
//     FAILS the test if a single EVENT frame ever arrives.
//  2. OBSERVATIONALLY — the caller hashes the local append-only log before and after
//     a live run and compares (scripts/resealplan does exactly this), which catches
//     any local-side write this reasoning missed.
//
// WHAT THE REPORT HAS TO ANSWER, per board, because these are the questions an
// operator cannot approve a board without: how many coordinates would be re-sealed,
// how many would be SKIPPED AND WHY, what the sealed sizes look like, which
// coordinates carry references that break, and how many readers on an older CEK
// epoch would lose access to history they can read today. That last one is the only
// irreversible human cost in the whole operation and it is the easiest to leave out.
package sync

import (
	"context"
	"fmt"
	"sort"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// Skip reasons. Every coordinate the pass will not touch carries exactly one, because
// "not re-sealed" without a reason is indistinguishable from "forgotten".
const (
	// SkipAlreadySealed: the coordinate's winning card already carries an envelope.
	// There is no plaintext copy for a relay to serve, and re-sealing it again would
	// mint a new event id per run and never converge.
	SkipAlreadySealed = "already-sealed"
	// SkipOverLimit: the sealed replacement would exceed the relay's 64 KiB ceiling,
	// so the write would be refused (ready-c3e) and the pass would HALT here.
	SkipOverLimit = "over-limit"
	// SkipForeignAuthor: the plaintext card is signed by another key. kind-30302 is
	// addressable on (kind, AUTHOR, d), so a replacement signed by the owner lands at
	// a DIFFERENT coordinate and evicts nothing — rd's own fold would show sealed
	// while the relay kept serving the plaintext (ready-a43, ready-e7a).
	SkipForeignAuthor = "foreign-author"
	// SkipBoardNotConfidential: the board has no CEK-bearing grant, so it was never
	// confidential. Its plaintext is intended, and sealing it would make it
	// unreadable to its own audience while achieving nothing this epic is for.
	SkipBoardNotConfidential = "board-not-confidential"
)

// CoordPlan is what the pass would do to ONE addressable coordinate, and why.
type CoordPlan struct {
	Coord   string `json:"coord"`
	ItemID  string `json:"item_id"`
	EventID string `json:"event_id"`
	Author  string `json:"author"`
	// RelayCreatedAt is the created_at of the card the RELAY is serving at this
	// coordinate right now. It is the only floor a replacement must actually beat:
	// latest-wins is decided on what the relay holds, and the local log cannot see
	// another machine's newer card (ready-500). An executor stamps strictly above
	// this, so it is carried on the plan rather than re-fetched by the caller —
	// re-fetching would open a window in which the two disagree.
	RelayCreatedAt int64 `json:"relay_created_at"`
	// Reseal is true when this coordinate would be re-sealed; when false,
	// SkipReason names which of the constants above applies.
	Reseal     bool   `json:"reseal"`
	SkipReason string `json:"skip_reason,omitempty"`
	// PlaintextBytes / SealedBytes are the measured current size and the projected
	// size after sealing (ProjectSealedWireSize). SealedBytes is 0 for a coordinate
	// that is already sealed, where there is nothing to project.
	PlaintextBytes int `json:"plaintext_bytes"`
	SealedBytes    int `json:"sealed_bytes,omitempty"`
	// BrokenRefs counts events that cite this card's CONCRETE event id in an "e"
	// tag. Every one of them stops resolving on the relay once the coordinate is
	// superseded. They are inert — no reader resolves an "e" pointer (ready-c9d) —
	// but the operator is told the number rather than left to discover it.
	BrokenRefs int `json:"broken_refs"`
}

// BoardResealPlan is one board's dry-run report: the unit the operator approves.
type BoardResealPlan struct {
	Board      string `json:"board"`
	BoardCoord string `json:"board_coord"`
	// Confidential is whether the board has ANY CEK-bearing role grant. A board
	// without one was never confidential, so nothing on it is in scope.
	Confidential bool `json:"confidential"`
	// CurrentEpoch is the highest CEK epoch the owner has distributed; the epoch a
	// re-seal would seal under. 0 on a non-confidential board.
	CurrentEpoch int `json:"current_epoch"`

	Cards       int            `json:"cards"`
	WouldReseal int            `json:"would_reseal"`
	Skipped     map[string]int `json:"skipped"`

	// LargestSealedBytes / TotalSealedBytes describe the sealed size distribution
	// over the coordinates that WOULD be re-sealed.
	LargestSealedBytes int `json:"largest_sealed_bytes"`
	TotalSealedBytes   int `json:"total_sealed_bytes"`
	// BrokenRefs is the sum of CoordPlan.BrokenRefs over re-sealed coordinates.
	BrokenRefs int `json:"broken_refs"`

	// ReadersLosingHistory lists grantees who hold a CEK epoch OLDER than
	// CurrentEpoch and are not revoked. Today they read this board's plaintext tail
	// like anyone else; after the pass those cards are sealed under CurrentEpoch,
	// which they do not hold. This is the irreversible human cost of the operation,
	// and ready-402 is where they get told.
	ReadersLosingHistory []string `json:"readers_losing_history"`

	Coords []CoordPlan `json:"coords"`
}

// BuildResealPlan reports what a re-seal of one board WOULD do, reading only.
//
// It takes the same paginated "#a" walk every other measurement in this epic uses —
// never an "authors" filter, which this relay is measured to answer with silence
// (ready-d84/ready-5c5/ready-0ab) — and derives cards, grants and the "e" reference
// graph from that ONE fetch, so a portfolio-wide plan costs one walk per board.
func BuildResealPlan(ctx context.Context, relayURL, owner, boardD, boardCoord string) (BoardResealPlan, error) {
	plan := BoardResealPlan{Board: boardD, BoardCoord: boardCoord, Skipped: map[string]int{}}
	evs, err := fetchBoardEventsFromRelay(ctx, relayURL, boardCoord)
	if err != nil {
		return plan, fmt.Errorf("sync: reseal plan for %s from %s: %w", boardD, relayURL, err)
	}

	// Winning card per addressable coordinate, plus the "e" reference graph. Both
	// come off the same pass; an unverifiable event is evidence of nothing and is
	// dropped before either.
	winners := map[string]*nostr.Event{}
	refsByEventID := map[string]int{}
	var grants []*nostr.Event
	for _, e := range evs {
		if e == nil || e.Verify() != nil {
			continue
		}
		switch {
		case e.Kind == KindCard:
			if !EventBelongsToBoard(e, boardCoord) {
				continue
			}
			c := coord(e.Kind, e.PubKey, tagValue(e, "d"))
			if cur, ok := winners[c]; !ok || newerThan(e, cur) {
				winners[c] = e
			}
		case e.Kind == KindRoleGrant:
			grants = append(grants, e)
		}
		if e.Kind != KindCard {
			for _, tg := range e.Tags {
				if len(tg) > 1 && tg[0] == "e" {
					refsByEventID[tg[1]]++
				}
			}
		}
	}

	plan.CurrentEpoch, plan.Confidential, plan.ReadersLosingHistory = epochStanding(grants, owner)

	for c, e := range winners {
		cp := CoordPlan{
			Coord:          c,
			ItemID:         tagValue(e, "d"),
			EventID:        e.ID,
			Author:         e.PubKey,
			RelayCreatedAt: e.CreatedAt,
			BrokenRefs:     refsByEventID[e.ID],
		}
		n, serr := marshaledEventSize(e)
		if serr != nil {
			return plan, fmt.Errorf("sync: measure %s: %w", e.ID, serr)
		}
		cp.PlaintextBytes = n

		switch {
		case tagValue(e, tagEnc) != "":
			cp.SkipReason = SkipAlreadySealed
		case !plan.Confidential:
			cp.SkipReason = SkipBoardNotConfidential
		case e.PubKey != owner:
			cp.SkipReason = SkipForeignAuthor
		default:
			proj, perr := ProjectSealedWireSize(e)
			if perr != nil {
				return plan, fmt.Errorf("sync: project %s: %w", cp.ItemID, perr)
			}
			cp.SealedBytes = proj.SealedBytes
			if proj.OverLimit {
				cp.SkipReason = SkipOverLimit
			} else {
				cp.Reseal = true
			}
		}
		if cp.Reseal {
			plan.WouldReseal++
			plan.TotalSealedBytes += cp.SealedBytes
			plan.BrokenRefs += cp.BrokenRefs
			if cp.SealedBytes > plan.LargestSealedBytes {
				plan.LargestSealedBytes = cp.SealedBytes
			}
		} else {
			plan.Skipped[cp.SkipReason]++
		}
		plan.Cards++
		plan.Coords = append(plan.Coords, cp)
	}
	sort.Slice(plan.Coords, func(i, j int) bool {
		if plan.Coords[i].ItemID != plan.Coords[j].ItemID {
			return plan.Coords[i].ItemID < plan.Coords[j].ItemID
		}
		return plan.Coords[i].Coord < plan.Coords[j].Coord
	})
	return plan, nil
}

// epochStanding derives, from a board's role grants: the highest CEK epoch the OWNER
// has distributed (the epoch a re-seal seals under), whether the board is
// confidential at all, and which non-revoked grantees hold only an OLDER epoch.
//
// Only OWNER-SIGNED grants count toward the epoch: DeriveBoardKeyring honours no
// other signer's CEK, so a grant from anyone else establishes nothing. Grantee
// standing is latest-wins per grantee across every per-epoch slot, which is how
// deriveGrants resolves a revoke that lives in a different slot from the grant it
// supersedes (ready-889/ready-be1).
func epochStanding(grants []*nostr.Event, owner string) (currentEpoch int, confidential bool, losing []string) {
	type standing struct {
		createdAt int64
		id        string
		role      string
		epoch     int
	}
	latest := map[string]standing{}
	for _, e := range grants {
		g, ok := parseRoleGrant(e)
		if !ok {
			continue
		}
		if e.PubKey == owner && g.WrappedCEK != "" {
			confidential = true
			if g.CEKEpoch > currentEpoch {
				currentEpoch = g.CEKEpoch
			}
		}
		cur, seen := latest[g.Grantee]
		if !seen || e.CreatedAt > cur.createdAt || (e.CreatedAt == cur.createdAt && e.ID < cur.id) {
			latest[g.Grantee] = standing{createdAt: e.CreatedAt, id: e.ID, role: g.Role, epoch: g.CEKEpoch}
		}
	}
	if !confidential {
		return 0, false, nil
	}
	for grantee, st := range latest {
		if st.role == RoleRevoked || grantee == owner {
			continue
		}
		if st.epoch < currentEpoch {
			losing = append(losing, grantee)
		}
	}
	sort.Strings(losing)
	return currentEpoch, true, losing
}
