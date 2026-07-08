// Nostr projection / replay (ready-a13).
//
// ProjectItems replays the local authoritative signed-event log and reconstructs
// the CURRENT state of every rd item — the read-back path. Two rules, straight
// from the epic design:
//
//   - Card state is LATEST-WINS on the addressable 30302 card (NIP-100). Among
//     all cards for an item, the one with the greatest (created_at, log-index)
//     wins. created_at is second-granularity (per epic); the append-only log
//     order breaks ties deterministically — the log is authoritative, so a later
//     line always wins a same-second tie.
//
//   - Status is STATUS-AUTHORITY: the most recent NIP-34 status event authored by
//     the item AUTHOR or a board MAINTAINER wins (NIP-34 rule). Status events from
//     other pubkeys are ignored. The exact rd status comes from the event's
//     "status" tag (not just the kind), so waiting/blocked/scheduled survive.
//
// The result is expressed as *state.Item so it feeds the existing projection/
// derive layer (pkg/state) rather than introducing a parallel item type.
package sync

import (
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
)

// ProjectOptions tunes replay. Author is the item author pubkey (the portfolio
// key that created the item); Maintainers are additional pubkeys whose status
// events are authoritative (board maintainers). When Author is empty, the author
// of each item's winning card is treated as the item author.
type ProjectOptions struct {
	Maintainers map[string]bool
}

// ProjectItems reconstructs current item state from a signed-event log slice
// (already read in append order). It returns a map keyed by rd item ID.
//
// Only events that pass an independent schnorr Verify are considered — a tampered
// or forged line in the log cannot influence the projection. This is the
// read-side trust gate mirroring pkg/state's derive-time enforcement.
func ProjectItems(events []*nostr.Event, opts ProjectOptions) map[string]*state.Item {
	type ranked struct {
		ev  *nostr.Event
		idx int
	}
	// Winning card per item, and the ordered list of authoritative status events.
	winningCard := map[string]ranked{}
	statusEvents := map[string][]ranked{}
	// author of the winning card per item (for status-authority checks).
	for idx, e := range events {
		if e == nil {
			continue
		}
		if err := e.Verify(); err != nil {
			continue // forged/tampered line — ignore
		}
		itemID := itemIDForEvent(e)
		if itemID == "" {
			continue
		}
		switch {
		case e.Kind == KindCard:
			cur, ok := winningCard[itemID]
			if !ok || newerThan(e, idx, cur.ev, cur.idx) {
				winningCard[itemID] = ranked{ev: e, idx: idx}
			}
		case isStatusKind(e.Kind):
			statusEvents[itemID] = append(statusEvents[itemID], ranked{ev: e, idx: idx})
		}
	}

	out := make(map[string]*state.Item, len(winningCard))
	for itemID, card := range winningCard {
		author := card.ev.PubKey
		item := itemFromCard(card.ev)

		// Status authority: newest status event from author-or-maintainer.
		var best ranked
		haveStatus := false
		for _, s := range statusEvents[itemID] {
			if s.ev.PubKey != author && !opts.Maintainers[s.ev.PubKey] {
				continue // not authoritative
			}
			if !haveStatus || newerThan(s.ev, s.idx, best.ev, best.idx) {
				best = s
				haveStatus = true
			}
		}
		if haveStatus {
			fromStatus := item.Status
			if st := tagValue(best.ev, "status"); st != "" {
				item.Status = st
			}
			item.UpdatedAt = maxInt64(item.UpdatedAt, best.ev.CreatedAt*int64(time.Second))
			// Preserve the audit trail: a status event carries the transition and,
			// for closes, the rd close-with-reason (in the event content).
			item.History = append(item.History, state.HistoryEntry{
				Timestamp:  time.Unix(best.ev.CreatedAt, 0).UTC().Format(time.RFC3339),
				FromStatus: fromStatus,
				ToStatus:   item.Status,
				ChangedBy:  best.ev.PubKey,
				Note:       best.ev.Content,
			})
		}
		out[itemID] = item
	}
	return out
}

// newerThan reports whether event a (at log index ai) is newer than event b (bi)
// under the (created_at, log-index) ordering — created_at first (seconds), then
// append order as the authoritative tiebreak.
func newerThan(a *nostr.Event, ai int, b *nostr.Event, bi int) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return ai > bi
}

// itemFromCard materializes a *state.Item from a 30302 card event's tags/content.
// This is the card->item projection; the state authority still comes from the
// status-authority pass in ProjectItems.
func itemFromCard(e *nostr.Event) *state.Item {
	itemID := tagValue(e, "d")
	// created_at is seconds; state.Item timestamps are unix nanos.
	tsNano := e.CreatedAt * int64(time.Second)
	item := &state.Item{
		ID:          itemID,
		MsgID:       e.ID,
		Title:       tagValue(e, "title"),
		Status:      tagValue(e, "s"),
		Priority:    firstNonEmpty(tagValue(e, "priority"), tagValue(e, "rank")),
		Type:        tagValue(e, "itype"),
		Context:     e.Content,
		Description: e.Content,
		CreatedAt:   tsNano,
		UpdatedAt:   tsNano,
	}
	if p := tagValue(e, "p"); p != "" {
		item.By = p
	}
	return item
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
