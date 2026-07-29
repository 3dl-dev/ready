// Independent relay read-back audit (ready-260).
//
// WHY THIS EXISTS: a publish result proves nothing about any PARTICULAR relay.
// reduceEventOutcome (relayclass.go) returns "accepted" as soon as ANY write
// relay accepts an event, which is correct for durability — an event stored
// somewhere needs no retry — and useless as evidence that the relay a browser
// client actually reads from received anything. With two LAN relays that accept
// everything and one public relay, a total, permanent rejection by the public
// relay is invisible in rd's own success report. That is the ready-f7b failure
// ("rd board share reports success when the grant reached no relay") and, at
// bulk scale, it would mean confidently reporting a 24-board backfill that
// landed nowhere a reader can see.
//
// So verification here is a FRESH SUBSCRIPTION against ONE named relay, and the
// comparison is against the local log's PROJECTION, not against raw counts.
// Kinds 30301/30302/39301 are addressable: a relay retains exactly one event per
// (kind, pubkey, d) coordinate, so a fully-backfilled board legitimately shows
// far fewer events on the relay than the local log holds. Counting events would
// therefore report a correct backfill as a failure. What must match is:
//
//   - every addressable coordinate in scope exists on the relay, and the event
//     the relay retained is the SAME event the local log projects as the winner
//     (newerThan); and
//   - every non-addressable event (the append-only 1630/1631/1632 status chain)
//     is present by id.
package sync

import (
	"context"
	"fmt"
	"sort"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// auditPageLimit is the per-REQ event cap used when walking a board off a relay.
// 500 is strfry's default maxFilterLimit and the public relay's hard maximum —
// it answers a larger limit with a CLOSED frame rather than silently truncating.
const auditPageLimit = 500

// maxAuditPages bounds the `until` walk so a misbehaving relay cannot spin the
// audit forever. At auditPageLimit each, this covers boards far larger than any
// in the portfolio; exceeding it is reported as an error, never as a clean
// result, because a truncated read-back would understate what is on the relay
// and could turn a real gap into a false "match".
const maxAuditPages = 200

// isAddressableKind reports whether a kind is in NIP-01's addressable range
// (30000-39999), where a relay retains only the newest event per
// (kind, pubkey, d) coordinate. rd's boards (30301), cards (30302) and role
// grants (39301/39302) are all addressable; the NIP-34 status chain
// (1630/1631/1632/1633) is not.
func isAddressableKind(kind int) bool { return kind >= 30000 && kind < 40000 }

// BoardRelayAudit is the result of reading ONE board back off ONE relay and
// comparing it to the local authoritative log.
type BoardRelayAudit struct {
	Relay      string `json:"relay"`
	BoardCoord string `json:"board_coord"`

	// LocalEvents / RelayEvents are raw event counts in board scope. They are
	// DIAGNOSTIC ONLY and are expected to differ on a correctly-backfilled
	// board (the relay keeps one event per addressable coordinate; the log keeps
	// every historical re-materialization). Never use them as the pass criterion
	// — Match is the criterion.
	LocalEvents int `json:"local_events"`
	RelayEvents int `json:"relay_events"`

	// LocalItems / RelayItems are the DISTINCT work items in scope (distinct
	// kind-30302 "d" tags). This is the number a board reader renders, and the
	// one the ready-260 done-condition compares.
	LocalItems int `json:"local_items"`
	RelayItems int `json:"relay_items"`

	// BoardDefinitionOnRelay reports whether the relay serves a kind-30301 event
	// for this exact coordinate — what makes the board discoverable at all.
	BoardDefinitionOnRelay bool `json:"board_definition_on_relay"`

	// AddressableCoords is how many distinct addressable coordinates the local
	// log holds in scope; MissingCoords are absent from the relay entirely and
	// StaleCoords exist but retained an event OLDER than the local winner.
	AddressableCoords int      `json:"addressable_coords"`
	MissingCoords     []string `json:"missing_coords,omitempty"`
	StaleCoords       []string `json:"stale_coords,omitempty"`

	// MissingRegular counts local non-addressable events (the status chain) the
	// relay did not serve; MissingRegularSample is a bounded sample of their ids
	// for diagnosis.
	MissingRegular       int      `json:"missing_regular"`
	MissingRegularSample []string `json:"missing_regular_sample,omitempty"`

	// RelayVerifyFailures counts events the relay served whose schnorr signature
	// did NOT verify. A backfill republishes signed bytes verbatim, so this must
	// be zero; a non-zero value means the relay altered or fabricated events and
	// the audit must not be read as a pass.
	RelayVerifyFailures int `json:"relay_verify_failures"`

	// Match is the pass criterion: the board definition is present, no
	// addressable coordinate is missing or stale, no regular event is missing,
	// and nothing the relay served failed verification.
	Match bool `json:"match"`
}

// AuditBoardOnRelay reads boardCoord back off a SINGLE relay with fresh
// subscriptions and compares it to localEvents (the caller's authoritative log,
// unscoped — this function applies the same EventBelongsToBoard scope the
// publisher uses, so audit and publish can never disagree about what "the board"
// means).
//
// It performs two reads: the board's own 30301 definition (which carries no "a"
// tag pointing at itself, so the membership filter cannot find it), and a
// paginated `#a` walk over every member event.
func AuditBoardOnRelay(ctx context.Context, relayURL string, localEvents []*nostr.Event, boardCoord string) (BoardRelayAudit, error) {
	audit, _, err := auditBoard(ctx, relayURL, localEvents, boardCoord)
	return audit, err
}

// BoardRelayDelta is AuditBoardOnRelay plus the EVENTS that would close the gap:
// every local board-scoped event the relay is missing, and the current winner
// for every addressable coordinate the relay is missing or holds a stale
// version of. The events are returned in publish order (created_at ascending,
// same-second ties lowest-id last — see boardScopedEvents), so sending them
// leaves the relay's projection equal to the local log's.
//
// WHY A DELTA PATH EXISTS AT ALL (ready-260). The production relay is backed by
// a throttled store: under load a large share of writes come back as transient
// "error: save …" and land only partially, so one pass over a board never
// finishes it. Re-sending the WHOLE board each round does converge — every event
// is verbatim, so a re-send is a no-op for what landed — but each round then
// costs as much as the first, and a 23,000-event portfolio needs many rounds.
// Publishing only the measured gap makes each successive round cheap and makes
// convergence observable: the delta must shrink, and reaching zero is the same
// condition the audit reports as Match.
func BoardRelayDelta(ctx context.Context, relayURL string, localEvents []*nostr.Event, boardCoord string) (BoardRelayAudit, []*nostr.Event, error) {
	return auditBoard(ctx, relayURL, localEvents, boardCoord)
}

func auditBoard(ctx context.Context, relayURL string, localEvents []*nostr.Event, boardCoord string) (BoardRelayAudit, []*nostr.Event, error) {
	audit := BoardRelayAudit{Relay: relayURL, BoardCoord: boardCoord}
	_, boardD, err := splitBoardCoord(boardCoord)
	if err != nil {
		return audit, nil, err
	}

	// --- local side -------------------------------------------------------
	localAddressable := map[string]*nostr.Event{} // coordinate -> projected winner
	localRegular := map[string]bool{}             // event id set
	localByID := map[string]*nostr.Event{}        // id -> event, for delta assembly
	localItems := map[string]bool{}
	localSeen := map[string]bool{}
	for _, e := range localEvents {
		if e == nil || localSeen[e.ID] || !EventBelongsToBoard(e, boardCoord) {
			continue
		}
		localSeen[e.ID] = true
		localByID[e.ID] = e
		audit.LocalEvents++
		if e.Kind == KindCard {
			if d := tagValue(e, "d"); d != "" {
				localItems[d] = true
			}
		}
		if !isAddressableKind(e.Kind) {
			localRegular[e.ID] = true
			continue
		}
		c := coord(e.Kind, e.PubKey, tagValue(e, "d"))
		if cur, ok := localAddressable[c]; !ok || newerThan(e, cur) {
			localAddressable[c] = e
		}
	}
	audit.LocalItems = len(localItems)
	audit.AddressableCoords = len(localAddressable)

	// --- relay side -------------------------------------------------------
	relayEvents, err := fetchBoardEventsFromRelay(ctx, relayURL, boardCoord)
	if err != nil {
		return audit, nil, err
	}
	// The board DEFINITION is fetched by kind + "#d" only — NEVER "authors".
	// See ready-d84: the deployed relay's author index silently under-returns
	// (galtrader 108/371 by authors vs 371/371 by #a; a grants query returning 8
	// of 11), and a kind-30301 event carries no self-referential "a" tag, so it
	// is unreachable by the #a walk above. Dropping `authors` widens the match to
	// every author using this exact "d" string, which fetchPaged's client-side
	// EventBelongsToBoard check below narrows back to this owner's coordinate —
	// the same client-side-check-instead-of-relay-index pattern ready-5c5 used
	// for board discovery (merged 665218c).
	defEvents, err := fetchBoardDefinitionFromRelay(ctx, relayURL, boardD)
	if err != nil {
		return audit, nil, fmt.Errorf("sync: audit board-definition read from %s: %w", relayURL, err)
	}
	relayEvents = append(relayEvents, defEvents...)

	relayAddressable := map[string]*nostr.Event{}
	relayRegular := map[string]bool{}
	relayItems := map[string]bool{}
	relaySeen := map[string]bool{}
	for _, e := range relayEvents {
		if e == nil || relaySeen[e.ID] {
			continue
		}
		relaySeen[e.ID] = true
		if verr := e.Verify(); verr != nil {
			audit.RelayVerifyFailures++
			continue // an unverifiable event is not evidence of anything
		}
		if !EventBelongsToBoard(e, boardCoord) {
			continue
		}
		audit.RelayEvents++
		if e.Kind == KindCard {
			if d := tagValue(e, "d"); d != "" {
				relayItems[d] = true
			}
		}
		if e.Kind == KindBoard && BoardCoord(e.PubKey, tagValue(e, "d")) == boardCoord {
			audit.BoardDefinitionOnRelay = true
		}
		if !isAddressableKind(e.Kind) {
			relayRegular[e.ID] = true
			continue
		}
		c := coord(e.Kind, e.PubKey, tagValue(e, "d"))
		if cur, ok := relayAddressable[c]; !ok || newerThan(e, cur) {
			relayAddressable[c] = e
		}
	}
	audit.RelayItems = len(relayItems)

	// --- comparison -------------------------------------------------------
	for c, want := range localAddressable {
		got, ok := relayAddressable[c]
		switch {
		case !ok:
			audit.MissingCoords = append(audit.MissingCoords, c)
		case got.ID != want.ID && newerThan(want, got):
			// The relay retained an event the local log considers OLDER — the
			// regression this whole ordering discipline exists to prevent.
			audit.StaleCoords = append(audit.StaleCoords, c)
		}
	}
	sort.Strings(audit.MissingCoords)
	sort.Strings(audit.StaleCoords)

	var missingRegular []string
	for id := range localRegular {
		if !relayRegular[id] {
			missingRegular = append(missingRegular, id)
		}
	}
	sort.Strings(missingRegular)
	audit.MissingRegular = len(missingRegular)
	if len(missingRegular) > 10 {
		audit.MissingRegularSample = missingRegular[:10]
	} else {
		audit.MissingRegularSample = missingRegular
	}

	audit.Match = audit.BoardDefinitionOnRelay &&
		len(audit.MissingCoords) == 0 &&
		len(audit.StaleCoords) == 0 &&
		audit.MissingRegular == 0 &&
		audit.RelayVerifyFailures == 0

	// --- delta: the events that would close the gap ------------------------
	// The board's own 30301 is included whenever the relay does not serve it,
	// even though it carries no "a" tag and so never appears in MissingCoords'
	// membership-tag scan.
	deltaIDs := map[string]bool{}
	for _, id := range missingRegular {
		deltaIDs[id] = true
	}
	for _, c := range append(append([]string{}, audit.MissingCoords...), audit.StaleCoords...) {
		if e := localAddressable[c]; e != nil {
			deltaIDs[e.ID] = true
		}
	}
	if !audit.BoardDefinitionOnRelay {
		if e := localAddressable[boardCoord]; e != nil {
			deltaIDs[e.ID] = true
		}
	}
	var delta []*nostr.Event
	for id := range deltaIDs {
		if e := localByID[id]; e != nil {
			delta = append(delta, e)
		}
	}
	sortForPublish(delta)
	return audit, delta, nil
}

// fetchPaged walks baseFilter off one relay, paginating backwards with `until`
// because the relay caps a single REQ at auditPageLimit events and REFUSES
// (rather than truncates) a larger limit. baseFilter must not set "limit" or
// "until" itself — fetchPaged owns both. label is used only in error text.
//
// This is the ONE paginated-read primitive the whole audit uses, for BOTH the
// membership walk (`#a` filter) and the board-definition walk (`kind`+`#d`
// filter, ready-d84) — neither may be an unpaged, author-scoped REQ. See
// ready-d84: a single unpaged REQ proves nothing, and an `authors` filter is
// the one shape the deployed relay is measured to silently under-return on.
//
// `until` is inclusive in NIP-01, so each page re-serves the previous page's
// oldest second; dedup by id absorbs that.
//
// TERMINATION rests on the requested limit being one the relay honours exactly.
// auditPageLimit is deliberately the public relay's own maximum, and that relay
// REFUSES an over-large limit (a NIP-01 CLOSED frame) rather than silently
// truncating — so a page shorter than auditPageLimit provably means the relay
// has nothing older to give, and only a page of exactly auditPageLimit may have
// been cut short. A relay that silently capped BELOW a limit it accepted would
// break that inference; FetchMany surfaces the CLOSED refusal as an error, so
// such a relay shows up as a failed audit rather than a quietly short one.
//
// The one case `until` cannot walk past is a created_at PLATEAU: more events
// sharing a single second than one page can hold. `until` is inclusive, so the
// next page re-serves the same second and adds nothing. That is reported as an
// ERROR, never as a short result — a read-back that looks complete but is not
// would turn a real gap into a false "match", the worst outcome an audit can
// produce.
func fetchPaged(ctx context.Context, relayURL string, baseFilter map[string]any, label string) ([]*nostr.Event, error) {
	seen := map[string]bool{}
	var out []*nostr.Event
	var until int64
	for page := 0; page < maxAuditPages; page++ {
		filter := make(map[string]any, len(baseFilter)+2)
		for k, v := range baseFilter {
			filter[k] = v
		}
		filter["limit"] = auditPageLimit
		if until > 0 {
			filter["until"] = until
		}
		evs, err := nostr.FetchMany(ctx, relayURL, filter)
		if err != nil {
			return out, fmt.Errorf("sync: audit read page %d of %s from %s: %w", page, label, relayURL, err)
		}
		added := 0
		oldest := int64(0)
		for _, e := range evs {
			if e == nil {
				continue
			}
			if !seen[e.ID] {
				seen[e.ID] = true
				out = append(out, e)
				added++
			}
			if oldest == 0 || e.CreatedAt < oldest {
				oldest = e.CreatedAt
			}
		}
		if len(evs) < auditPageLimit {
			// The relay served every event at or below `until` — nothing older
			// exists to page to.
			return out, nil
		}
		if added == 0 {
			return out, fmt.Errorf("sync: audit read of %s from %s stalled: a full page of %d events shares created_at %d, so `until` cannot page further — the read-back would be silently incomplete", label, relayURL, len(evs), until)
		}
		until = oldest
	}
	return out, fmt.Errorf("sync: audit read of %s from %s exceeded %d pages — refusing to report a truncated read-back", label, relayURL, maxAuditPages)
}

// fetchBoardEventsFromRelay walks every event tagged with boardCoord off one
// relay via its `#a` coordinate tag. See fetchPaged for the pagination
// contract this relies on.
func fetchBoardEventsFromRelay(ctx context.Context, relayURL, boardCoord string) ([]*nostr.Event, error) {
	return fetchPaged(ctx, relayURL, map[string]any{"#a": []string{boardCoord}}, boardCoord)
}

// fetchBoardDefinitionFromRelay walks every kind-30301 event carrying #d=boardD
// off one relay. It deliberately filters on kind + "#d" ONLY — no "authors" —
// because a kind-30301 event carries no self-referential "a" tag (so it is
// unreachable via fetchBoardEventsFromRelay's #a walk) and the deployed relay's
// author index is measured to silently under-return (ready-d84, ready-5c5,
// ready-0ab). The caller narrows the result to the exact owner by checking
// EventBelongsToBoard against the target coordinate, the same client-side
// check ready-5c5 used for board discovery in place of the relay's author
// index.
func fetchBoardDefinitionFromRelay(ctx context.Context, relayURL, boardD string) ([]*nostr.Event, error) {
	return fetchPaged(ctx, relayURL, map[string]any{"kinds": []int{KindBoard}, "#d": []string{boardD}}, "board definition "+boardD)
}

// EventReadBack is the result of reading a SPECIFIC set of already-published
// events back off ONE relay by id with a fresh REQ.
//
// AuditBoardOnRelay answers "is this whole board complete on that relay", which
// on a 5,000-item board costs a paginated walk of the entire board. A key
// rotation publishes a handful of grants and needs the narrow question — "did
// these exact signed events reach the relay a reader reads from" — so this uses
// the same evidence standard (fresh subscription, independent schnorr verify,
// compare against what we hold locally) at the granularity of an id set.
type EventReadBack struct {
	Relay string `json:"relay"`
	// Want is how many distinct event ids were asked for.
	Want int `json:"want"`
	// Present are the ids the relay served whose bytes verify. Because
	// nostr.Event.Verify RE-DERIVES the id from the canonical serialization, an
	// id in this list means the relay served the identical signed event, not
	// merely something labelled with that id.
	Present []string `json:"present"`
	// Missing are the ids the relay did not serve at all (or served in a form
	// that failed verification, which is not evidence the event is there).
	Missing []string `json:"missing"`
	// Unverified are ids the relay answered with an event whose signature or id
	// derivation did NOT check out — the relay altered or fabricated it. These
	// are also counted as Missing, because a tampered answer proves nothing about
	// the real event being retrievable.
	Unverified []string `json:"unverified,omitempty"`
	// Match is the pass criterion: every wanted id came back verified.
	Match bool `json:"match"`
}

// VerifyEventsOnRelay reads want back off relayURL by id with a fresh
// subscription and reports which of them that relay actually serves.
//
// WHY IT EXISTS (ready-2b25): reduceEventOutcome (relayclass.go) reports an
// event as accepted as soon as ANY write relay accepts it, and the LAN relays
// accept everything — so rd's own publish report cannot tell you whether the
// PUBLIC relay took a write. For a CEK rotation that gap is not cosmetic: the
// new epoch's grants are the only way a member learns the new key, so a
// rotation whose grants reached only the LAN is a rotation that silently locked
// every remote member out of everything written from that moment on.
//
// It reads by id through nostr.FetchByIDs, which chunks at MaxREQIDs, so a
// rotation on a board with hundreds of members is one call.
func VerifyEventsOnRelay(ctx context.Context, relayURL string, want []*nostr.Event) (EventReadBack, error) {
	rb := EventReadBack{Relay: relayURL}
	wanted := make(map[string]bool, len(want))
	ids := make([]string, 0, len(want))
	for _, e := range want {
		if e == nil || e.ID == "" || wanted[e.ID] {
			continue
		}
		wanted[e.ID] = true
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	rb.Want = len(ids)
	if rb.Want == 0 {
		rb.Match = true
		return rb, nil
	}
	served, err := nostr.FetchByIDs(ctx, relayURL, ids)
	if err != nil {
		return rb, fmt.Errorf("sync: read-back of %d event(s) from %s: %w", len(ids), relayURL, err)
	}
	verified := map[string]bool{}
	tampered := map[string]bool{}
	for _, e := range served {
		if e == nil || !wanted[e.ID] {
			continue // the relay volunteered something we did not ask about
		}
		if e.Verify() != nil {
			tampered[e.ID] = true
			continue
		}
		verified[e.ID] = true
	}
	for _, id := range ids {
		switch {
		case verified[id]:
			rb.Present = append(rb.Present, id)
		default:
			rb.Missing = append(rb.Missing, id)
			if tampered[id] {
				rb.Unverified = append(rb.Unverified, id)
			}
		}
	}
	rb.Match = len(rb.Missing) == 0
	return rb, nil
}

// splitBoardCoord decomposes "30301:<owner>:<boardD>" into its owner and D-tag,
// reusing the ONE coordinate parser (parseBoardCoord, rolegrant.go) so the audit
// cannot drift from how the rest of the package reads a board coordinate.
func splitBoardCoord(boardCoord string) (owner, boardD string, err error) {
	owner, boardD, ok := parseBoardCoord(boardCoord)
	if !ok {
		return "", "", fmt.Errorf("sync: %q is not a board coordinate of the form %d:<owner-pubkey>:<board-d>", boardCoord, KindBoard)
	}
	return owner, boardD, nil
}

// PortfolioBoard is one board definition an owner has published to a relay.
type PortfolioBoard struct {
	D         string `json:"d"`
	Coord     string `json:"coord"`
	EventID   string `json:"event_id"`
	CreatedAt int64  `json:"created_at"`
	Title     string `json:"title,omitempty"`
	Verified  bool   `json:"verified"`
	// Archived reports whether THIS event (the one the relay actually served for
	// the coordinate) carries the archived marker (ready-a9b, IsBoardArchived).
	// Addressable events mean a compliant relay serves at most one 30301 per
	// (kind, pubkey, d), so this needs no separate latest-wins pass the way a
	// multi-relay merge would — it is exactly what that relay would hand a
	// browser today.
	Archived bool `json:"archived,omitempty"`
}

// FetchPortfolioBoards runs the ready-260 done-condition query against ONE
// relay: a fresh REQ for {kinds:[30301], authors:[owner]}, returning every board
// definition that relay will serve for that author, schnorr-verified
// independently. This is the portfolio-level counterpart to AuditBoardOnRelay —
// "which boards does a browser client see at all", as opposed to "is this one
// board complete".
func FetchPortfolioBoards(ctx context.Context, relayURL, owner string) ([]PortfolioBoard, error) {
	evs, err := nostr.FetchMany(ctx, relayURL, map[string]any{
		"kinds":   []int{KindBoard},
		"authors": []string{owner},
		"limit":   auditPageLimit,
	})
	if err != nil {
		return nil, fmt.Errorf("sync: portfolio board read from %s: %w", relayURL, err)
	}
	out := make([]PortfolioBoard, 0, len(evs))
	seen := map[string]bool{}
	for _, e := range evs {
		if e == nil || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		d := tagValue(e, "d")
		out = append(out, PortfolioBoard{
			D:         d,
			Coord:     BoardCoord(e.PubKey, d),
			EventID:   e.ID,
			CreatedAt: e.CreatedAt,
			Title:     tagValue(e, "title"),
			Verified:  e.Verify() == nil,
			Archived:  IsBoardArchived(e),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].D < out[j].D })
	return out, nil
}

// DefaultAuditTimeout is a generous per-audit deadline: the public relay is
// scale-to-zero, so a cold start alone has been observed to take longer than the
// 10s DefaultTimeout used for LAN relay operations.
const DefaultAuditTimeout = 90 * time.Second
