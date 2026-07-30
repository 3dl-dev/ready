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
	"regexp"
	"sort"
	"strconv"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
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

	// PinnedBoard, when non-empty, is the authoritative board coordinate
	// "30301:<ownerPubkey>:<boardD>" this project is bound to (BP-3, design
	// docs/design/nostr-identity-model.md §4). When set it activates three
	// point-in-time / anti-escalation behaviours; when empty every one of them is
	// inert, so the pre-BP-3 projection is reproduced exactly (except the
	// board-maintainer union fix, which is unconditional):
	//
	//   - BOARD PINNING: a 30302 card whose "a" coordinate is not PinnedBoard is
	//     REJECTED at projection. This kills parallel-board self-escalation — a
	//     relay-admitted key otherwise forks its own 30301, self-grants maintainer
	//     on it, and publishes cards under its own "a".
	//
	//   - GRADED LEVELS: DeriveLevels is run for the board author parsed from this
	//     coordinate, yielding {pubkey→level} and each key's authoritative-until.
	//     The level≥2 set augments the status-authority maintainers for this
	//     coordinate (a revocable source alongside the board's "p" tags); an
	//     explicitly-revoked key (level 0) is stripped from it.
	//
	//   - POINT-IN-TIME READ-TRUST (prospective revocation, design §3): an event
	//     from a key with a bounded authoritative-until is honoured only if
	//     created_at < until. A revoked key's FUTURE events drop; its PAST
	//     authoritative events survive, so a completed item does NOT reopen when
	//     its past author is later revoked.
	PinnedBoard string

	// Decryptor, when non-nil, decrypts confidential free text (title,
	// description, waiting_on, labels, close reason) for a GRANTED member (epic
	// ready-216). It supplies the per-board CEK for a card/status event's (board
	// coordinate, cek_epoch). Nil — or a miss for the event's epoch, or an AEAD
	// failure — renders those fields as placeholderText while every CLEAR routing
	// field still renders (fail-closed, never raw ciphertext, never a panic). It is
	// injected so the read path is testable before keydist (ready-a8a) wires the
	// real grant-unwrap; production callers pass nil until then.
	Decryptor BoardDecryptor

	// EncryptedBoards, when non-nil, drives the FAIL-CLOSED FOLD GATE (ready-710):
	// on a board it marks confidential, a card lacking a well-formed enc envelope
	// is quarantined (never folded), except a genuine pre-cutover plaintext card
	// (grandfathered here, on replay). Nil leaves every board plaintext-legal (gate
	// inert). keydist (ready-a8a) populates it from the board's CEK-epoch state.
	EncryptedBoards EncryptedBoardSet
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

// grantTrusts reports whether pubkey is admitted to read-trust by the grant-derived
// level map for the pinned board (GAP-1, ready-7c1). Presence in `levels` means the
// key is the board author (bootstrap trust root) or holds a cap-valid winning grant —
// including a revoked one (level 0), whose PAST events must survive (prospective
// revocation; the until gate drops its future events). When no board is pinned
// `levels` is empty, so this contributes nothing and the pre-GAP-1 opts.Trusted gate
// stands alone.
func grantTrusts(levels map[string]int, pubkey string) bool {
	_, ok := levels[pubkey]
	return ok
}

// ProjectItems reconstructs current item state from a signed-event log slice
// (already read in append order). It returns a map keyed by rd item ID.
//
// Only events that pass an independent schnorr Verify are considered — a tampered
// or forged line in the log cannot influence the projection. This is the
// read-side trust gate mirroring pkg/state's derive-time enforcement.
func ProjectItems(events []*nostr.Event, opts ProjectOptions) map[string]*state.Item {
	// GRADED OPERATOR LEVELS (BP-3, design §3/§4): when a board is pinned, derive the
	// {pubkey→level} + authoritative-until maps from the signed 39301 role-grant
	// events for the pinned board's authority chain. `until` powers the point-in-time
	// read-trust gate below (prospective revocation); `levels≥2` augments the
	// status-authority maintainer set. Both are empty when no board is pinned, so the
	// gates are inert and the pre-BP-3 projection is reproduced.
	var levels map[string]int
	var until map[string]int64
	if opts.PinnedBoard != "" {
		if owner, boardD, ok := parseBoardCoord(opts.PinnedBoard); ok {
			// GAP-2 (ready-885): bind to the FULL pinned coordinate (owner + boardD), not
			// the owner alone, so a grant on a different boardD cannot bleed onto this board.
			levels, until = DeriveLevels(events, owner, boardD)
		}
	}

	// Winning card per item, and the ordered list of authoritative status events.
	winningCard := map[string]*nostr.Event{}
	statusEvents := map[string][]*nostr.Event{}
	// NOTE (ready-4ec rework): an earlier version of this function tracked
	// firstSeen — the MINIMUM created_at across every admitted card/status event
	// for an item id — and used it as Item.CreatedAt. That was SUBSET-sensitive: a
	// relay retains only the latest addressable 30302 per item (NIP-33), so a
	// machine bootstrapped via `rd join` holds the newest card + status events ONLY,
	// never historical card revisions. min() over "whatever happens to be present"
	// then disagreed with min() over the full local log for the identical item —
	// two machines could project different creation times from the identical
	// current state. It also let a non-authoritative status event (one from
	// neither the item author nor a board maintainer) lower CreatedAt merely by
	// existing with an early created_at, contradicting §6.4/§19.8's "contributes
	// neither state nor history" rule — this fold pass ran BEFORE the
	// status-authority filter below, so it couldn't tell authoritative from not.
	// CreatedAt is now read from a CARRIED "created" tag on the winning card itself
	// (CardSpec.CreatedAt / itemFromCard) — see itemFromCard, and its Item.CreatedAt
	// assignment in the loop below, which needs no override here.
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
	// LATEST-WINS board per coordinate (BP-3, design §1/§4 fix for the A4 live bug):
	// the pre-BP-3 code UNIONED the "p" tags of ALL historical 30301 board events for
	// a coordinate (`boardMaintainers` filled in the main loop), so a maintainer named
	// once was a maintainer FOREVER — the board could never be republished to REVOKE a
	// maintainer. We instead retain only the NEWEST board event per coordinate and
	// derive its maintainers from THAT event's "p" tags alone (built after the loop),
	// so a board republished without a "p" tag drops that maintainer.
	winningBoard := map[string]*nostr.Event{}
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
		//
		// GRANT-DERIVED READ-TRUST (GAP-1, ready-7c1 — "one signed source feeds
		// everything"): admission is opts.Trusted (bootstrap: self + Config.TrustedPubkeys)
		// UNIONED with the grant-derived membership for the pinned board (`levels`, which
		// includes the board author and every cap-valid grantee — see DeriveReadTrust).
		// So an owner-GRANTED contributor absent from rd.json is admitted by its
		// owner-signed grant alone, and projection agrees with the grant-fed ingestion
		// gate. This is non-circular: the board author is always in `levels` (bootstrap),
		// so owner-signed grants are always admitted and each admitted grant expands the
		// set. Fail-closed: a key with neither a grant nor a config/self entry is still
		// dropped. Prospective revocation is enforced by the until gate just below — a
		// revoked key stays in `levels` (level 0) so its PAST events survive; its future
		// events drop on `until`.
		if !opts.trusts(e.PubKey) && !grantTrusts(levels, e.PubKey) {
			continue
		}
		// POINT-IN-TIME READ-TRUST (BP-3, design §3 A1 — prospective revocation): a key
		// with a bounded authoritative-until (i.e. it was REVOKED) is honoured only for
		// events created BEFORE the revoke took effect. `until` holds the revoke's
		// effective time (`from`, else its created_at); non-revoked keys map to
		// +infinity and keys absent from the map (no grant / no pinned board) are
		// unbounded. Dropping only future events — not past ones — is what preserves the
		// audit trail: a completed item does NOT reopen when its past author is later
		// revoked, while the revoked key can author nothing NEW that projects.
		if u, ok := until[e.PubKey]; ok && e.CreatedAt >= u {
			continue
		}
		// Board (30301) events carry status-authority policy, not item state. Retain
		// only the NEWEST board per coordinate (latest-wins); its maintainers are
		// derived after the loop. This runs BEFORE the itemID guard below (a board's
		// "d" tag is the boardD, not an item).
		if e.Kind == KindBoard {
			seen[e.ID] = true
			coord := BoardCoord(e.PubKey, tagValue(e, "d"))
			if cur, ok := winningBoard[coord]; !ok || newerThan(e, cur) {
				winningBoard[coord] = e
			}
			continue
		}
		itemID := itemIDForEvent(e)
		if itemID == "" {
			continue
		}
		// BOARD PINNING (BP-3, design §4 A5): reject any card bound to a board other
		// than the pinned authoritative coordinate. Without this, any relay-admitted
		// key forks its own 30301, self-grants maintainer on it, and publishes cards
		// under its own "a" — a parallel-board self-escalation. Only cards are gated
		// here (they carry item state / authorship); status authority is already
		// per-coordinate bound. Inert when no board is pinned.
		if opts.PinnedBoard != "" && e.Kind == KindCard && tagValue(e, "a") != opts.PinnedBoard {
			continue
		}
		// FAIL-CLOSED FOLD GATE (ready-710): on a confidential board, a card OR a
		// NIP-34 status event lacking a well-formed enc envelope is quarantined — it
		// never enters winningCard / statusEvents, so its cleartext free text (a
		// card's title/description or a status event's close reason) cannot fold into
		// the projection or history. A genuine pre-cutover plaintext event is
		// grandfathered (only here — this loop IS the full-log replay). Inert when
		// EncryptedBoards is nil or the board is plaintext. Sibling to the board-pin
		// skip above; strfry can't validate payload shape, so this local fold is the
		// single enforcement point.
		if (e.Kind == KindCard || isStatusKind(e.Kind)) && shouldQuarantine(e, opts.EncryptedBoards) {
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

	// Derive board maintainers from the WINNING (newest) board per coordinate only —
	// NOT the monotonic union of all historical boards (the A4 bug). A board author is
	// always a maintainer of their own board; the newest board's "p" tags name the
	// rest, so republishing without a "p" tag revokes that maintainer.
	for coord, b := range winningBoard {
		addBoardMaintainer(coord, b.PubKey)
		for _, m := range tagValues(b, "p") {
			addBoardMaintainer(coord, m)
		}
	}
	// Fold the grant-derived level≥2 set into the PINNED coordinate's maintainers — a
	// revocable status-authority source alongside the board "p" tags (design §4
	// Gate B). We deliberately do NOT strip revoked keys from the maintainer set here:
	// revocation is PROSPECTIVE and is enforced upstream by the point-in-time
	// read-trust gate (a revoked key's future events are dropped before this loop ever
	// sees them; its PAST authoritative events must remain authoritative so a completed
	// item does not reopen — design §3 A1). A current-snapshot strip would erase that
	// past authority and reopen the item, which is exactly the bug being ruled out.
	if opts.PinnedBoard != "" {
		for k, lvl := range levels {
			if lvl >= LevelMaintainer {
				addBoardMaintainer(opts.PinnedBoard, k)
			}
		}
	}

	out := make(map[string]*state.Item, len(winningCard))
	for itemID, card := range winningCard {
		author := card.PubKey
		item := itemFromCard(card, opts.Decryptor)
		// TRUE CREATION TIME (ready-4ec rework): itemFromCard already set
		// item.CreatedAt from the winning card's CARRIED "created" tag (falling back
		// to the card's own created_at only when that tag is absent) — no override
		// needed here. See CardSpec.CreatedAt's doc for why a carried value, not a
		// scan over the admitted event set, is the subset-safe mechanism.

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
			// Close/change reason: plaintext status events carry it in Content
			// verbatim; a confidential status event carries sealed ciphertext, so a
			// granted member decrypts it and a non-member sees the placeholder — the
			// history entry (who/when/from→to) still renders regardless.
			note := s.Content
			if isConfidential(s) {
				if reason, ok := decryptStatusReason(s, opts.Decryptor); ok {
					note = reason
				} else {
					note = placeholderText
				}
			}
			item.History = append(item.History, state.HistoryEntry{
				Timestamp:  time.Unix(s.CreatedAt, 0).UTC().Format(time.RFC3339),
				FromStatus: prevStatus,
				ToStatus:   toStatus,
				ChangedBy:  changedBy,
				Note:       note,
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
	// DETERMINISTIC EDGE ORDER (ready-f5f / ready-e12): edges is built by ranging
	// over the items MAP, so its order is randomized per process run even though
	// the input event set is identical. Left unsorted, the appendUniqueStr calls
	// below populate BlockedBy/Blocks in that random order, so a blocked item's
	// BlockedBy array (and a blocker's Blocks array, for any blocker with 2+
	// blockees) differs byte-for-byte between runs on the exact same data — the
	// same "map order leaks into output" defect ready-e88 fixed for item ORDER,
	// here for item CONTENT. Sort by (blockedID, blockerID) ascending before
	// applying: this fixes BlockedBy's own order directly (it's grouped by
	// blockedID) and, because sorting by blockedID is the PRIMARY key, any
	// fixed blocker's edges remain a subsequence of the globally-sorted list —
	// so its Blocks entries land in ascending blockedID order too, without a
	// second explicit sort. IDs are content-addressed hex strings, so lexical
	// ordering is stable and requires no external state.
	sort.Slice(edges, func(i, j int) bool {
		if edges[i].blockedID != edges[j].blockedID {
			return edges[i].blockedID < edges[j].blockedID
		}
		return edges[i].blockerID < edges[j].blockerID
	})
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
// canonicalCreatedSecondsRe matches EXACTLY the decimal-integer format
// strconv.FormatInt(secs, 10) produces for a positive int64 secs: no sign, no
// leading zero, no whitespace, digits only. BuildCardEvent only ever emits a
// "created" tag via FormatInt when CreatedAt > 0 (nostrwire.go), so this is
// the WHOLE set of shapes a genuine tag can take.
var canonicalCreatedSecondsRe = regexp.MustCompile(`^[1-9][0-9]*$`)

// parseCanonicalCreatedTag parses a "created" tag value, returning (secs, true)
// only when raw is in EXACTLY that canonical form for some secs in
// (0, math.MaxInt64] — see canonicalCreatedSecondsRe's doc. Any other shape
// (empty, a leading '+', a leading zero, internal/leading/trailing whitespace,
// a fraction, non-digits, or a magnitude beyond int64) returns (0, false), and
// the caller falls back to the card's own created_at exactly as if the tag
// were absent.
//
// This exists because "just try to parse it" diverges between the two folds
// (ready-4ec rework 3): Go's strconv.ParseInt(raw, 10, 64) alone rejects
// whitespace and overflow but still accepts a leading '+' and leading zeros,
// while web/board/src/lib/fold.ts's BigInt(raw) alone accepts
// leading/trailing whitespace, treats "" as 0, and has no magnitude bound at
// all (silently producing a huge nanosecond timestamp instead of erroring).
// A relay-accepted, validly re-signed card can carry ANY tag value the
// signer chooses — a forged non-canonical "created" tag folded through both
// projections landed on two DIFFERENT CreatedAt values for the identical
// event. The canonical-format pre-check, mirrored byte-for-byte in
// parseCanonicalCreatedTag (TS) in fold.ts, is what makes both languages
// agree on the same accept/reject set for every input, adversarial or not —
// not just the well-formed happy path a single hand-written vector exercises.
// A strict canonical-integer rule is the obvious choice here: it exactly
// matches what the writer ever produces, so nothing legitimate is rejected,
// and it has no locale/whitespace/sign ambiguity for an adversary to exploit
// on one side but not the other.
func parseCanonicalCreatedTag(raw string) (int64, bool) {
	if !canonicalCreatedSecondsRe.MatchString(raw) {
		return 0, false
	}
	secs, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, false
	}
	return secs, true
}

func itemFromCard(e *nostr.Event, dec BoardDecryptor) *state.Item {
	itemID := tagValue(e, "d")
	// created_at is seconds; state.Item timestamps are unix nanos.
	tsNano := e.CreatedAt * int64(time.Second)
	// TRUE CREATION TIME (ready-4ec rework): read the CARRIED "created" tag when
	// present — see CardSpec.CreatedAt's doc for why a carried value, not a scan
	// over admitted events, is the only subset-safe mechanism. Falls back to this
	// card's OWN created_at when the tag is absent (a genesis card that has never
	// been republished since this field existed, or a pre-fix card from before it
	// did) — correct for that one bootstrap case, and the value CardSpecFromItem
	// then carries forward unchanged on every subsequent republish.
	createdAtNano := tsNano
	if raw := tagValue(e, "created"); raw != "" {
		if secs, ok := parseCanonicalCreatedTag(raw); ok {
			createdAtNano = secs * int64(time.Second)
		}
	}
	item := &state.Item{
		ID:          itemID,
		MsgID:       e.ID,
		Title:       tagValue(e, "title"),
		Status:      tagValue(e, "s"),
		Priority:    firstNonEmpty(tagValue(e, "priority"), tagValue(e, "rank")),
		Type:        tagValue(e, "itype"),
		Context:     e.Content,
		Description: e.Content,
		CreatedAt:   createdAtNano,
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
	// CONFIDENTIAL free-text substitution (epic ready-216): on a confidential card
	// the clear title/waiting_on tags are absent and Content is ciphertext; the l
	// tags are HMAC tokens. A granted member (decryptor holds the CEK) recovers the
	// exact plaintext title/description/waiting_on/labels from the sealed blob; a
	// non-member (or an epoch minted before the grant, or an AEAD failure)
	// fail-closes to a placeholder for the free-text fields. Every CLEAR routing
	// field above already rendered normally and is untouched here.
	if isConfidential(e) {
		if pl, ok := decryptCardPayload(e, dec); ok {
			item.Title = pl.Title
			item.Context = pl.Context
			item.Description = pl.Context
			item.WaitingOn = pl.WaitingOn
			if len(pl.Labels) > 0 {
				item.Labels = pl.Labels
			}
		} else {
			// FAIL-CLOSED, AND MARKED. Redacted is what stops the write path from
			// re-sealing this placeholder as the item's real content on the next
			// mutation (ready-76b) — the read substitution alone is safe, the
			// read-then-republish round-trip is what destroys data.
			item.Redacted = true
			item.Title = placeholderText
			item.Context = placeholderText
			item.Description = placeholderText
			// waiting_on is free text — hide it rather than expose ""/ciphertext;
			// the clear waiting_type still renders. Labels remain the opaque tokens
			// carried in the clear l tags (present but not readable).
			item.WaitingOn = ""
		}
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

// WinningBoardEvent returns the latest-wins (§4.5) kind-30301 event for exactly
// coord among events, applying the SAME tie-break newerThan uses for cards:
// greatest created_at, then lowest event id. Every candidate is independently
// schnorr-verified — events is typically a relay answer, and a relay is
// untrusted — so a forged or tampered event never wins and never masks (or
// fakes) an archived marker (ready-a9b). Returns (nil, false) when no verified
// kind-30301 event for coord exists in events.
//
// Appended at file end, not grouped next to newerThan, so this ready-a9b
// addition causes ZERO line-number drift for every ProjectItems clause this
// file already carries (board-fold-spec.md's citations are exact-line, see
// internal/foldvectors/citations_test.go) — inserting mid-file here would have
// shifted every citation below it for no reason connected to what changed.
//
// This is the read-side counterpart `rd board archive` (and the CLI portfolio
// gather's archived-board filter) uses to answer "what does this board's
// definition say right now" without duplicating ProjectItems' whole §3 gate
// sequence — board events carry no read-trust or confidential-envelope
// concerns (§2.1: "a board event carries status-authority policy only"), so
// dedup + verify is the entire rule.
func WinningBoardEvent(events []*nostr.Event, coord string) (*nostr.Event, bool) {
	var winner *nostr.Event
	for _, e := range events {
		if e == nil || e.Kind != KindBoard {
			continue
		}
		if BoardCoord(e.PubKey, tagValue(e, "d")) != coord {
			continue
		}
		if e.Verify() != nil {
			continue
		}
		if winner == nil || newerThan(e, winner) {
			winner = e
		}
	}
	if winner == nil {
		return nil, false
	}
	return winner, true
}
