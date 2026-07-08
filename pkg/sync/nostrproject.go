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
	"sort"
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

	// Trusted is the read-side authorization allowlist (ready-d53 web-of-trust):
	// the set of author pubkeys whose events may influence projected state at all.
	//
	// schnorr Verify (already enforced below) proves an event is internally
	// consistent, NOT that its author is authorized — any generated key produces
	// events that Verify. Without this gate a foreign key could publish a 30302
	// card for someone else's item; because the card projection is latest-wins
	// across ALL authors, that forged card would win, and worse, its author would
	// then be treated as the item AUTHOR — making the attacker's own status events
	// authoritative (a full state takeover). The trust gate closes this: an event
	// whose author is not in Trusted is dropped before it can influence the winning
	// card, the status authority, OR the history.
	//
	// Semantics: when Trusted is NON-NIL the allowlist is ENFORCED (untrusted-author
	// events are ignored). When Trusted is NIL the gate is DISABLED (every verified
	// event is considered) — this preserves the pre-ready-d53 behaviour for tests
	// and any legacy unconfigured path. Production callers always pass a non-nil set
	// containing at least the self pubkey (see rdconfig.Config.TrustSet).
	Trusted map[string]bool
}

// trusts reports whether pubkey is authorized under opts.Trusted. A nil Trusted
// set disables the gate (everything is trusted); a non-nil set enforces the
// allowlist.
func (opts ProjectOptions) trusts(pubkey string) bool {
	if opts.Trusted == nil {
		return true
	}
	return opts.Trusted[pubkey]
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
		// Web-of-trust authorization (ready-d53): Verify proved consistency, not
		// authority. Drop any event whose author is not in the trust allowlist so
		// an untrusted key can never influence the winning card, status authority,
		// or history — even if the event reached the local log (defence in depth
		// with the ingestion gate in reconcile()).
		if !opts.trusts(e.PubKey) {
			continue
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

		// Status authority + FULL HISTORY REPLAY (ready-b5f): collect every status
		// event authored by the item AUTHOR or a board MAINTAINER — not just the
		// newest one. The 30302 card is a latest-wins projection with NO history of
		// its own (per the epic's hybrid design); the append-only status-event chain
		// IS the audit trail, so every authoritative transition becomes a
		// HistoryEntry, in chronological order, each carrying its own reason
		// (close-with-reason survives exactly as published). A non-authoritative
		// (non-author/non-maintainer) status event is excluded entirely — it never
		// contributes state OR history, matching the NIP-34 authority rule.
		var authoritative []ranked
		for _, s := range statusEvents[itemID] {
			if s.ev.PubKey != author && !opts.Maintainers[s.ev.PubKey] {
				continue // not authoritative
			}
			authoritative = append(authoritative, s)
		}
		sort.Slice(authoritative, func(i, j int) bool {
			a, b := authoritative[i], authoritative[j]
			if a.ev.CreatedAt != b.ev.CreatedAt {
				return a.ev.CreatedAt < b.ev.CreatedAt
			}
			return a.idx < b.idx
		})

		prevStatus := ""
		for _, s := range authoritative {
			toStatus := tagValue(s.ev, "status")
			if toStatus == "" {
				toStatus = prevStatus
			}
			item.History = append(item.History, state.HistoryEntry{
				Timestamp:  time.Unix(s.ev.CreatedAt, 0).UTC().Format(time.RFC3339),
				FromStatus: prevStatus,
				ToStatus:   toStatus,
				ChangedBy:  s.ev.PubKey,
				Note:       s.ev.Content,
			})
			item.UpdatedAt = maxInt64(item.UpdatedAt, s.ev.CreatedAt*int64(time.Second))
			prevStatus = toStatus
		}
		if len(authoritative) > 0 {
			// The last authoritative status event still wins for CURRENT state —
			// identical to the prior latest-wins behavior, now with full history
			// alongside it instead of only the winning entry.
			item.Status = prevStatus
		}
		out[itemID] = item
	}
	applyDepAndGateStatus(out)
	return out
}

// applyDepAndGateStatus is the final projection pass, mirroring pkg/state's
// applyBlockStatus exactly (substrate swap — same semantics, different source):
//   - each item's declared deps (raw "i" tags, stashed in item.BlockedBy by
//     itemFromCard) are resolved against the other items in this projection;
//   - an item is set to StatusBlocked when at least one declared blocker is
//     itself present and non-terminal; BlockedBy/Blocks are populated for every
//     resolvable edge regardless of the blocker's terminal state (matches
//     pkg/state: BlockedBy records the dependency, not just active blockers);
//   - unresolvable deps (blocker not present in this event set — e.g. not yet
//     ingested) are dropped, same as an unknown campfire block edge.
//   - GateMsgID is (re)derived from the winning card's event id whenever the
//     item is waiting on a gate, so views.GatesFilter's "non-empty GateMsgID"
//     check behaves the same as the campfire-derived path.
func applyDepAndGateStatus(items map[string]*state.Item) {
	type edge struct{ blockerID, blockedID string }
	var edges []edge
	for id, item := range items {
		for _, dep := range item.BlockedBy {
			edges = append(edges, edge{blockerID: dep, blockedID: id})
		}
		item.BlockedBy = nil // rebuilt below from validated edges only
	}
	for _, e := range edges {
		blocker, blockerOK := items[e.blockerID]
		blocked, blockedOK := items[e.blockedID]
		if !blockerOK || !blockedOK {
			continue
		}
		if state.IsTerminal(blocked) {
			continue
		}
		if !state.IsTerminal(blocker) {
			blocked.Status = state.StatusBlocked
		}
		blocked.BlockedBy = appendUniqueStr(blocked.BlockedBy, e.blockerID)
		blocker.Blocks = appendUniqueStr(blocker.Blocks, e.blockedID)
	}

	for _, item := range items {
		if item.Status == state.StatusWaiting {
			if item.WaitingSince == "" {
				item.WaitingSince = time.Unix(0, item.UpdatedAt).UTC().Format(time.RFC3339)
			}
			if item.WaitingType == "gate" {
				item.GateMsgID = item.MsgID
			} else {
				item.GateMsgID = ""
			}
		} else {
			// Blocking (or any other non-waiting status) supersedes a declared
			// wait/gate, same as pkg/state's applyBlockStatus running after
			// handleWorkGate: the final status wins.
			item.WaitingOn = ""
			item.WaitingType = ""
			item.WaitingSince = ""
			item.GateMsgID = ""
		}
	}
}

// appendUniqueStr appends val to slice only if not already present.
func appendUniqueStr(slice []string, val string) []string {
	for _, v := range slice {
		if v == val {
			return slice
		}
	}
	return append(slice, val)
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
		// Raw declared deps ("i" tags) -- resolved into validated BlockedBy/Blocks
		// (and blocked-status) by applyDepAndGateStatus once all items are known.
		BlockedBy:   tagValues(e, "i"),
		Gate:        tagValue(e, "gate"),
		WaitingType: tagValue(e, "waiting_type"),
		WaitingOn:   tagValue(e, "waiting_on"),
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
