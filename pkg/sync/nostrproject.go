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

// ProjectOptions tunes replay.
//
// Maintainers is an EXPLICIT supplementary set of pubkeys whose status events are
// authoritative (unioned with the board-derived maintainers below). It exists for
// tests and callers that construct an event set WITHOUT the 30301 board event.
//
// STATUS-AUTHORITY vs READ-TRUST (ready-b57): these are SEPARATE gates and must not
// be conflated.
//
//   - READ-TRUST (Trusted) decides who may enter the projection AT ALL — the full
//     web-of-trust allowlist (every admitted machine/identity). A read-untrusted
//     event is dropped before it influences anything.
//
//   - STATUS-AUTHORITY decides who may author an AUTHORITATIVE status transition on
//     a given item. Per NIP-34 that is the item AUTHOR (card author) OR a declared
//     BOARD MAINTAINER — NOT the whole trust set. The board's maintainers are read
//     from the 30301 board event's "p" tags (plus the board author, who maintains
//     their own board), bound per board coordinate (30301:<author>:<boardD>) via the
//     card's "a" tag. Passing the entire trust set as Maintainers (the pre-b57 prod
//     wiring) COLLAPSED this: any admitted key could author status on ANY item and
//     forge 'by' history. Production now passes Maintainers=nil and relies on the
//     board-derived set; the trust set stays in Trusted only.
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
	// Winning card per item, and the ordered list of authoritative status events.
	winningCard := map[string]*nostr.Event{}
	statusEvents := map[string][]*nostr.Event{}
	// STATUS-AUTHORITY source (ready-b57): board maintainers keyed by board
	// coordinate "30301:<boardAuthor>:<boardD>". Populated from the 30301 board
	// events in this SAME event set (trusted + verified). The board AUTHOR is an
	// implicit maintainer of their own board; the board's "p" tags name additional
	// maintainers (e.g. an admitted second machine the author co-signs authority to).
	// A card's "a" tag names the board it belongs to, so an item's authoritative
	// signers = its card author OR a maintainer of THAT board coordinate — never the
	// whole trust set. Deriving per-coordinate (the coordinate embeds the author) is
	// what stops a trusted key from minting status authority for another author's
	// item by publishing its OWN board.
	boardMaintainers := map[string]map[string]bool{}
	addBoardMaintainer := func(coord, pubkey string) {
		if coord == "" || pubkey == "" {
			return
		}
		set := boardMaintainers[coord]
		if set == nil {
			set = map[string]bool{}
			boardMaintainers[coord] = set
		}
		set[pubkey] = true
	}
	// DEDUP BY EVENT ID (ready-f92): re-ingesting the same signed event MUST be a
	// no-op. The local log AppendUnique already dedups on the write side, but the
	// projection is also fed by MergeFrom/reconcile unions and callers may pass an
	// event set with repeats; without this guard a duplicated status event would be
	// replayed twice and fabricate a phantom history entry (and a duplicated card
	// would still win, harmlessly, but the loop would do redundant work). Projection
	// is therefore idempotent on the event id: the FIRST occurrence of an id is
	// authoritative, later copies of the identical id are skipped. Because the id is
	// a content hash, two lines sharing an id are byte-identical, so "first wins" is
	// order-independent — it does not reintroduce an append-order dependence.
	seen := map[string]bool{}
	for _, e := range events {
		if e == nil {
			continue
		}
		if seen[e.ID] {
			continue // duplicate event id — already projected (idempotent)
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
		// Board (30301) events carry status-authority policy, not item state. Record
		// the board's maintainer set (author + "p" tags) keyed by its coordinate, then
		// move on — a board has no rd item id. This runs BEFORE the itemID guard below
		// (a board's "d" tag is the boardD, not an item).
		if e.Kind == KindBoard {
			seen[e.ID] = true
			coord := BoardCoord(e.PubKey, tagValue(e, "d"))
			addBoardMaintainer(coord, e.PubKey) // author maintains their own board
			for _, m := range tagValues(e, "p") {
				addBoardMaintainer(coord, m)
			}
			continue
		}
		itemID := itemIDForEvent(e)
		if itemID == "" {
			continue
		}
		seen[e.ID] = true
		switch {
		case e.Kind == KindCard:
			cur, ok := winningCard[itemID]
			if !ok || newerThan(e, cur) {
				winningCard[itemID] = e
			}
		case isStatusKind(e.Kind):
			statusEvents[itemID] = append(statusEvents[itemID], e)
		}
	}

	out := make(map[string]*state.Item, len(winningCard))
	for itemID, card := range winningCard {
		author := card.PubKey
		item := itemFromCard(card)

		// STATUS-AUTHORITY SET (ready-b57): who — besides the item author — may author
		// an authoritative status transition on THIS item. It is the maintainers of the
		// board this card belongs to (its "a" coordinate), unioned with any explicit
		// opts.Maintainers. NOT the whole trust set: read-trust (opts.Trusted) governs
		// who may enter projection at all; status-authority is the strictly narrower
		// author-or-board-maintainer rule. maintainerSigners excludes the author so we
		// can tell a board maintainer apart from a bare author (needed by the 'by' gate).
		maintainerSigners := map[string]bool{}
		if coord := tagValue(card, "a"); coord != "" {
			for m := range boardMaintainers[coord] {
				maintainerSigners[m] = true
			}
		}
		for m := range opts.Maintainers {
			maintainerSigners[m] = true
		}

		// Status authority + FULL HISTORY REPLAY (ready-b5f): collect every status
		// event authored by the item AUTHOR or a board MAINTAINER — not just the
		// newest one. The 30302 card is a latest-wins projection with NO history of
		// its own (per the epic's hybrid design); the append-only status-event chain
		// IS the audit trail, so every authoritative transition becomes a
		// HistoryEntry, in chronological order, each carrying its own reason
		// (close-with-reason survives exactly as published). A non-authoritative
		// (non-author/non-maintainer) status event is excluded entirely — it never
		// contributes state OR history, matching the NIP-34 authority rule.
		var authoritative []*nostr.Event
		for _, s := range statusEvents[itemID] {
			if s.PubKey != author && !maintainerSigners[s.PubKey] {
				continue // not authoritative: not the item author, not a board maintainer
			}
			authoritative = append(authoritative, s)
		}
		// DETERMINISTIC ORDERING (ready-f92): sort by (created_at asc, event-id asc)
		// — NEVER by log-append index. created_at is second-granularity, so
		// concurrent same-second transitions are ordered by event id (a content
		// hash), a stable total order that is a pure function of the event SET. This
		// is what makes replay CONVERGENT: the local log's append order, a relay
		// reconcile's fetch order, and a cross-machine MergeFrom union all project
		// the IDENTICAL history and current status, because none of them can change
		// the (created_at, id) key of any event. The old append-index tie-break
		// diverged whenever two machines held the same event set in different order.
		sort.Slice(authoritative, func(i, j int) bool {
			a, b := authoritative[i], authoritative[j]
			if a.CreatedAt != b.CreatedAt {
				return a.CreatedAt < b.CreatedAt
			}
			return a.ID < b.ID
		})

		prevStatus := ""
		for _, s := range authoritative {
			toStatus := tagValue(s, "status")
			if toStatus == "" {
				toStatus = prevStatus
			}
			// PROVENANCE PRESERVATION (ready-d65 migration) + 'BY' SPOOF GUARD (ready-b57):
			// the audit-trail actor. For live self-writes there is no "by" tag, so the
			// changer is the event AUTHOR (the portfolio pubkey that signed it). For
			// MIGRATED history the original campfire actor (email / pubkey) is carried in
			// an rd-extension "by" tag, because the portfolio key is the only thing that
			// can SIGN the re-emitted event yet the audit trail must still record WHO
			// originally acted.
			//
			// The "by" tag REWRITES provenance, so it is only honored from a signer
			// authorized to rewrite it: a BOARD MAINTAINER (the entity that runs the
			// migration and owns the board). A bare item author — or any other trusted
			// signer that is not a board maintainer — cannot attribute a transition to an
			// arbitrary third party: their "by" tag is ignored and ChangedBy falls back to
			// the signer pubkey. Production migrations are signed by the board's own
			// maintainer key, so legitimate provenance still survives item-for-item; a
			// spoofed "by" from a non-authoritative-for-provenance signer does not.
			changedBy := s.PubKey
			if by := tagValue(s, "by"); by != "" && maintainerSigners[s.PubKey] {
				changedBy = by
			}
			item.History = append(item.History, state.HistoryEntry{
				Timestamp:  time.Unix(s.CreatedAt, 0).UTC().Format(time.RFC3339),
				FromStatus: prevStatus,
				ToStatus:   toStatus,
				ChangedBy:  changedBy,
				Note:       s.Content,
			})
			item.UpdatedAt = maxInt64(item.UpdatedAt, s.CreatedAt*int64(time.Second))
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
		// CARD-DECLARED GATE/WAIT PROMOTION (ready-d65): the item's CURRENT waiting
		// state can be a DERIVED gate state that was never written as its own NIP-34
		// status transition — e.g. a campfire item gated via a work:gate message has
		// status "waiting" but a history array that ends at "inbox"/"active" (the gate
		// is current state, not an audit row). The status-authority chain therefore
		// leaves such an item non-waiting, dropping its gate. The 30302 card, being the
		// materialized CURRENT state, still carries the waiting_type/waiting_on/gate
		// tags, so promote a non-terminal, non-blocked item to waiting whenever the
		// card declares a gate/wait. This is faithful to the live write path too: an
		// active `rd gate` publishes a waiting status event AND a card with these tags
		// (so promotion is a no-op there), while `rd approve` clears them (so an
		// approved item is never promoted). Blocking still supersedes (checked first).
		declaresGate := item.WaitingType != "" || item.WaitingOn != "" || item.Gate != ""
		if item.Status != state.StatusBlocked && !state.IsTerminal(item) && declaresGate {
			item.Status = state.StatusWaiting
		}
		switch {
		case state.IsTerminal(item):
			// A terminal item carries no live gate/wait — clear any stale card tags.
			item.WaitingOn = ""
			item.WaitingType = ""
			item.WaitingSince = ""
			item.GateMsgID = ""
		case declaresGate:
			// GATE FIELDS PERSIST UNDER BLOCKING (ready-187): the card-declared
			// waiting_type/waiting_on/gate are the item's materialized CURRENT gate
			// state. pkg/state.applyBlockStatus sets status=blocked but NEVER clears
			// these fields, so a gated item that ALSO gains a blocking dep keeps its
			// gate (hasGate stays true) — status is blocked, but the pending gate is
			// still real. The prior code wiped the fields whenever status != waiting,
			// which silently DROPPED the gate on every blocked+gated item on nostr (a
			// data-integrity divergence from campfire; parity fails on it). Retain the
			// fields; only the STATUS is superseded by blocking. Derive the display
			// GateMsgID/WaitingSince the same as the waiting path.
			if item.WaitingSince == "" {
				item.WaitingSince = time.Unix(0, item.UpdatedAt).UTC().Format(time.RFC3339)
			}
			if item.WaitingType == "gate" {
				item.GateMsgID = item.MsgID
			} else {
				item.GateMsgID = ""
			}
		default:
			// No declared gate/wait — ensure the fields are empty.
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

// newerThan reports whether card event a should REPLACE the current winner b under
// the deterministic latest-wins order (ready-f92). The primary key is created_at
// (seconds). On a created_at TIE the NIP-01 replaceable-event rule applies: the
// event with the LOWEST id (lexicographically first hex) is retained as canonical,
// so a beats b on a tie iff a.ID < b.ID.
//
// This tie-break is a pure function of the two events — it does NOT depend on
// log-append index, relay fetch order, or merge order — which is exactly why two
// machines holding the identical event set project the identical winning card for
// same-second competing edits (the convergence bug from ready-b6a/523). It also
// matches strfry's own NIP-33 replaceable tie-break, so the relay's retained event
// and the locally projected winner agree.
func newerThan(a, b *nostr.Event) bool {
	if a.CreatedAt != b.CreatedAt {
		return a.CreatedAt > b.CreatedAt
	}
	return a.ID < b.ID
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
		Labels:      tagValues(e, "l"),
		ETA:         tagValue(e, "eta"),
		// Additive rd-extension tags (ready-187) — humanness level, assignment scope,
		// parent/child tree edge, and due date. A missing tag defaults to "" (old
		// cards written before ready-187), preserving backward compatibility.
		Level:    tagValue(e, "level"),
		For:      tagValue(e, "for"),
		ParentID: tagValue(e, "parent"),
		Due:      tagValue(e, "due"),
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
