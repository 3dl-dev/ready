// Board/card inventory read (ready-207).
//
// WHY THIS EXISTS: ready-336 needs an exact, CURRENT count of every plaintext
// card coordinate across every live board before any re-seal pass can be
// sized. The 2026-07-29T04:19Z figure (5,446 cards, 4,043 plaintext / 1,403
// sealed across 24 live boards) was a one-off query against
// wss://relay.3dl.network; eight projects are actively writing, so it is
// stale by construction. This file makes that measurement RE-RUNNABLE code
// instead of a query nobody can reproduce.
//
// MEASUREMENT DISCIPLINE — non-negotiable, and the exact mistake ready-d84/
// ready-5c5/ready-0ab already paid for: NEVER filter this relay by
// "authors". It silently under-returns (galtrader: 108/371 cards by authors
// vs 371/371 by #a; a grants query: 8 of 11). Both functions below use only
// kind-only or #a/#d-tag filters, narrowing to a specific owner CLIENT-SIDE
// after the fetch — never server-side via "authors" — and both page through
// fetchPaged (boardaudit.go), which walks `until` backwards at
// auditPageLimit (>=500) and treats a same-second plateau as an ERROR rather
// than a silently short page. See fetchPaged's doc for the full argument.
package sync

import (
	"context"
	"fmt"
	"sort"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// LiveBoardDef is one non-archived kind-30301 board definition owned by a
// specific pubkey, as currently retained by a relay.
type LiveBoardDef struct {
	Coord     string `json:"coord"`
	D         string `json:"d"`
	EventID   string `json:"event_id"`
	CreatedAt int64  `json:"created_at"`
}

// DiscoverLiveBoards walks EVERY kind-30301 event on relayURL — a single
// {"kinds":[30301]} filter, paginated, with no "authors" and no "#d" — then
// keeps only the ones signed by owner (a client-side pubkey compare, never a
// relay-side "authors" filter), collapses to the latest-wins event per
// (owner, d) coordinate (the same rule WinningBoardEvent applies), and drops
// any coordinate whose winning event carries the archived marker
// (IsBoardArchived). This is the exact method the 2026-07-29T04:19Z
// portfolio measurement used to reach "56 definitions / 32 archived / 24
// live" (ready-228, ready-cab, ready-5c5), reproduced here as code so it can
// be re-run instead of re-typed.
func DiscoverLiveBoards(ctx context.Context, relayURL, owner string) ([]LiveBoardDef, error) {
	all, err := fetchPaged(ctx, relayURL, map[string]any{"kinds": []int{KindBoard}}, "all board definitions")
	if err != nil {
		return nil, fmt.Errorf("sync: discover live boards from %s: %w", relayURL, err)
	}
	byCoord := map[string]*nostr.Event{}
	for _, e := range all {
		if e == nil || e.Kind != KindBoard || e.PubKey != owner {
			continue
		}
		if e.Verify() != nil {
			continue // an unverifiable event is not evidence of a board
		}
		c := BoardCoord(e.PubKey, tagValue(e, "d"))
		if cur, ok := byCoord[c]; !ok || newerThan(e, cur) {
			byCoord[c] = e
		}
	}
	out := make([]LiveBoardDef, 0, len(byCoord))
	for c, e := range byCoord {
		if IsBoardArchived(e) {
			continue
		}
		out = append(out, LiveBoardDef{Coord: c, D: tagValue(e, "d"), EventID: e.ID, CreatedAt: e.CreatedAt})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].D < out[j].D })
	return out, nil
}

// CardCoordRow is one addressable kind-30302 coordinate on one live board, as
// currently retained by relayURL — the row ready-207's inventory (and every
// downstream re-seal item: ready-c53, ready-c9d, ready-43d) is keyed on.
//
// ItemID is the card's "d" tag. It is NOT guaranteed unique within a board:
// the 2026-07-29T04:19Z measurement found 4 item-ids on the "ready" board
// each carrying TWO addressable card events under different signing pubkeys
// (owner + a delegate key) — two distinct coordinates, same ItemID. Coord
// (kind:pubkey:d) is the real dedup key; ItemID is reporting convenience.
type CardCoordRow struct {
	Board      string `json:"board"`       // board D-tag (project name)
	BoardCoord string `json:"board_coord"` // the 30301:<owner>:<d> this card belongs to
	ItemID     string `json:"item_id"`     // card "d" tag
	Coord      string `json:"coord"`       // full coordinate: kind:pubkey:d
	Kind       int    `json:"kind"`
	EventID    string `json:"event_id"`
	WireBytes  int    `json:"wire_bytes"`
	Sealed     bool   `json:"sealed"`
	CreatedAt  int64  `json:"created_at"`
}

// BoardCardTotals is the plaintext/sealed roll-up ready-336's table reports,
// per board.
type BoardCardTotals struct {
	Board     string `json:"board"`
	Cards     int    `json:"cards"`
	Plaintext int    `json:"plaintext"`
	Sealed    int    `json:"sealed"`
}

// InventoryBoardCards walks every kind-30302 event tagged with boardCoord's
// "#a" off relayURL (fetchBoardEventsFromRelay's paginated #a walk — never
// "authors"), keeps the latest-wins event per addressable coordinate (the
// exact bytes a relay actually serves today for that coordinate), and
// returns one row per coordinate plus the board's plaintext/sealed totals.
//
// "sealed" uses the SAME test the 2026-07-29T04:19Z measurement and
// pkg/sync/envelope.go use: a clear ["enc","1"] marker tag (tagEnc). This
// deliberately does not attempt to decrypt anything — measuring what a
// reader holding NO key can see needs no key, and this function is never
// handed one.
func InventoryBoardCards(ctx context.Context, relayURL, boardD, boardCoord string) ([]CardCoordRow, BoardCardTotals, error) {
	totals := BoardCardTotals{Board: boardD}
	evs, err := fetchBoardEventsFromRelay(ctx, relayURL, boardCoord)
	if err != nil {
		return nil, totals, fmt.Errorf("sync: inventory board %s from %s: %w", boardD, relayURL, err)
	}
	winners := map[string]*nostr.Event{}
	for _, e := range evs {
		if e == nil || e.Kind != KindCard {
			continue
		}
		if e.Verify() != nil {
			continue // an unverifiable event is not evidence of anything
		}
		if !EventBelongsToBoard(e, boardCoord) {
			continue
		}
		c := coord(e.Kind, e.PubKey, tagValue(e, "d"))
		if cur, ok := winners[c]; !ok || newerThan(e, cur) {
			winners[c] = e
		}
	}
	rows := make([]CardCoordRow, 0, len(winners))
	for c, e := range winners {
		n, serr := marshaledEventSize(e)
		if serr != nil {
			return nil, totals, fmt.Errorf("sync: measure wire size of %s: %w", e.ID, serr)
		}
		sealed := tagValue(e, tagEnc) != ""
		rows = append(rows, CardCoordRow{
			Board:      boardD,
			BoardCoord: boardCoord,
			ItemID:     tagValue(e, "d"),
			Coord:      c,
			Kind:       e.Kind,
			EventID:    e.ID,
			WireBytes:  n,
			Sealed:     sealed,
			CreatedAt:  e.CreatedAt,
		})
		totals.Cards++
		if sealed {
			totals.Sealed++
		} else {
			totals.Plaintext++
		}
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].ItemID != rows[j].ItemID {
			return rows[i].ItemID < rows[j].ItemID
		}
		return rows[i].Coord < rows[j].Coord
	})
	return rows, totals, nil
}
