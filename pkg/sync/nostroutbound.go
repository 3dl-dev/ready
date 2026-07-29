// Nostr outbound publish (ready-a13).
//
// This is the nostr replacement for the campfire send path. Flow for publishing
// an rd item (the hybrid write, epic ready-a14):
//
//  1. Build + sign the events (board 30301, card 30302, status 1630..) via the
//     wire mapping.
//  2. Append every event to the LOCAL AUTHORITATIVE LOG. This ALWAYS happens and
//     is the durability guarantee — rd must work with every relay offline.
//  3. Publish to write relays, best-effort. Events that reach no relay are
//     buffered to nostr-pending.jsonl for a later flush (offline-buffer
//     semantics, mirroring the existing pending.jsonl). A relay failure NEVER
//     fails the operation: the event is already durable in the log.
package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// Publisher publishes rd item events to the local log and to write relays.
type Publisher struct {
	Key         *nostr.Key
	Log         *NostrLog
	WriteRelays []string
	// PendingPath is the offline relay-retry buffer (nostr-pending.jsonl). May be
	// empty to disable buffering (events still land in the authoritative log).
	PendingPath string
	// Timeout per relay publish. Zero uses nostr.DefaultTimeout.
	Timeout time.Duration

	// Production marks this Publisher as the sanctioned rd CLI write path — the
	// only context allowed to append/relay-publish events that address the
	// RESERVED production board coordinate (reservedProductionBoardD, this repo's
	// own live "ready" board; see .ready/config.json's pinned "board"). The zero
	// value is false, so EVERY Publisher not explicitly marked Production fails
	// closed the moment it tries to touch that coordinate (ready-fce rework).
	//
	// WHY THIS FIELD AND NOT A BOARD-NAME HEURISTIC: "ready" is not, in general,
	// an unsafe D-tag — it is THIS project's own real board, and the real `rd`
	// CLI must obviously go on writing to it. What distinguishes a legitimate
	// production write from a test/probe write hitting the identical coordinate
	// with the identical (real, allowlisted) key is not anything visible in the
	// event itself — it is WHO is making the call. So the guard is an explicit,
	// visible opt-in at Publisher construction, not an attempt to sniff "is this
	// a test" from environment or call stack (which a probe binary, run as a
	// plain `go run`/`go build` artifact outside `go test`, would trivially
	// evade — see scripts/relay-policy/probe/main.go, ready-6d0).
	//
	// Set to true ONLY by the sanctioned CLI construction sites: nostrPublisher()
	// (cmd/rd/nostr.go) and the two Publisher{} literals in cmd/rd/follow.go.
	// Every test, probe, or other ad hoc tool that builds a Publisher leaves this
	// false (the zero value) — the guard then blocks it if it ever addresses the
	// reserved coordinate, REGARDLESS of how the caller spelled its way there
	// (a hardcoded literal, a helper that forgot to isolate, a copy-pasted
	// probe): the check runs on the actual built events immediately before they
	// would become durable (Log.Append) or reach a relay (relayPublish), not on
	// a caller-supplied string the caller could simply not pass through a helper.
	Production bool
}

// reservedProductionBoardD is THIS repo's own live board D-tag. .ready/config.json
// pins this project's board coordinate to "30301:<owner>:ready" — the exact
// coordinate the real portfolio identity maintains in production. Any Publisher
// not marked Production that tries to append or relay-publish an event
// addressing this coordinate is refused by guardReservedBoard below (ready-fce).
const reservedProductionBoardD = "ready"

// hitsReservedBoard reports whether e itself addresses the reserved production
// board coordinate: either e IS the reserved 30301 board event (its own "d" tag
// equals reservedProductionBoardD), or it carries an "a" tag whose D-component
// equals reservedProductionBoardD (an "a" tag is "<kind>:<pubkey>:<d>" — see
// coord/BoardCoord in nostrwire.go). Checking the D-component regardless of kind
// or pubkey catches a card's board-membership tag, a status event's board-scope
// tag (ready-7ec, BuildStatusEventWithIssueRoot's second "a" tag), and any future
// tag shape addressing that board — the check runs on what was ACTUALLY built and
// signed, not on a spec field a caller could spell differently.
func hitsReservedBoard(e *nostr.Event) bool {
	if e == nil {
		return false
	}
	if e.Kind == KindBoard && tagValue(e, "d") == reservedProductionBoardD {
		return true
	}
	for _, a := range tagValues(e, "a") {
		parts := strings.SplitN(a, ":", 3)
		if len(parts) == 3 && parts[2] == reservedProductionBoardD {
			return true
		}
	}
	return false
}

// guardReservedBoard is the ready-fce write-path guard: called at the top of
// every Publisher method that appends to the authoritative log or relay-publishes
// (publishEvents, PublishEvents, PublishEventsUnique, PublishBoard) — the actual
// choke points where events become durable or reach the network, as opposed to
// requireIsolatedBoardD (live_relay_key_test.go), which was only a convention
// helper a caller had to remember to invoke. A Publisher not marked Production
// gets a hard error the instant any event in the batch addresses the reserved
// coordinate — before Log.Append and before any relay dial — so nothing lands in
// even a local log, let alone a live relay.
func (p *Publisher) guardReservedBoard(events []*nostr.Event) error {
	if p.Production {
		return nil
	}
	for _, e := range events {
		if hitsReservedBoard(e) {
			return fmt.Errorf("sync: refusing to publish kind %d event addressing the reserved production board coordinate %q — this Publisher is not marked Production (ready-fce guard); use an isolated board D-tag, or construct the Publisher via a sanctioned CLI path if this really is a production write", e.Kind, reservedProductionBoardD)
		}
	}
	return nil
}

// RelayAck records the per-relay outcome of a publish attempt.
type RelayAck struct {
	Relay    string
	Accepted bool
	Message  string
	Err      string
}

// EventAck records what happened to one signed event.
type EventAck struct {
	EventID string
	Kind    int
	Acks    []RelayAck
	// AnyRelay is true if at least one relay accepted (OK,true).
	AnyRelay bool
	// Permanent is true if no relay accepted the event AND at least one relay
	// rejected it for a permanent reason (malformed/disallowed) — it was
	// dead-lettered, not buffered for retry (ready-1c2).
	Permanent bool
	// Reason carries the relay message that classified a Permanent rejection.
	Reason string `json:",omitempty"`
}

// PublishResult summarises a PublishItem call.
type PublishResult struct {
	ItemID string
	Events []EventAck
	// Buffered is true if at least one event reached no relay for a TRANSIENT
	// reason and was buffered to the pending retry queue.
	Buffered bool
	// Rejected is true if at least one event was PERMANENTLY rejected by a relay
	// and dead-lettered to nostr-rejected.jsonl (ready-1c2). Distinct from
	// Buffered: a rejected event will NOT be retried.
	Rejected bool
}

// PublishItem materializes an item as board+card+status events, appends them to
// the authoritative log, and publishes to the write relays. createdAt is the
// item's timestamp in unix SECONDS (NIP-01 granularity). board may be nil to
// skip re-publishing the board event. Equivalent to PublishItemWithReason with
// reason="" — unchanged behaviour for every existing caller.
func (p *Publisher) PublishItem(ctx context.Context, board *BoardSpec, card CardSpec, createdAt int64) (PublishResult, error) {
	return p.PublishItemWithReason(ctx, board, card, "", createdAt)
}

// PublishItemWithReason is PublishItem plus an explicit NIP-34 status-event
// reason (ready-da7). It exists so the manual `rd nostr publish` path can carry
// an item's already-recorded close/change reason (its last history entry's
// note) through on republish — the create-time and status-change LIVE hooks
// never had this gap (they receive the reason as an explicit argument already);
// only the standalone republish command hand-carried an empty one. Also anchors
// the status event to the item's NIP-34 kind:1621 issue-root event (additive,
// generic-client interop — see BuildStatusEventWithIssueRoot), publishing that
// issue event ONCE per item the first time this (or PublishStatusChange) is
// called for it, never again on subsequent calls.
func (p *Publisher) PublishItemWithReason(ctx context.Context, board *BoardSpec, card CardSpec, reason string, createdAt int64) (PublishResult, error) {
	var res PublishResult
	res.ItemID = card.ItemID

	var events []*nostr.Event
	if board != nil {
		be, err := BuildBoardEvent(p.Key, *board, createdAt)
		if err != nil {
			return res, err
		}
		events = append(events, be)
	}
	ce, err := BuildCardEvent(p.Key, card, createdAt)
	if err != nil {
		return res, err
	}
	events = append(events, ce)

	issueID, issueEvent, err := p.ensureIssueEvent(card, createdAt)
	if err != nil {
		return res, err
	}
	if issueEvent != nil {
		events = append(events, issueEvent)
	}

	se, err := BuildStatusEventWithIssueRoot(p.Key, card.ItemID, card.Status, ce.ID, issueID, cardBoardCoord(p.Key, card), reason, createdAt, card.Enc)
	if err != nil {
		return res, err
	}
	events = append(events, se)

	return p.publishEvents(ctx, res, events)
}

// PublishStatusChange publishes a NIP-34 status event (with optional close/change
// reason) for an existing item, plus a refreshed card materializing the new
// current state. This is how status transitions (claim/done/fail/...) ride the
// hybrid model: history via the status event, current state via the card. The
// status event also carries the item's NIP-34 issue-root anchor (ready-da7,
// additive) — see PublishItemWithReason's doc for the one-issue-event-per-item
// invariant.
func (p *Publisher) PublishStatusChange(ctx context.Context, card CardSpec, reason string, createdAt int64) (PublishResult, error) {
	var res PublishResult
	res.ItemID = card.ItemID

	ce, err := BuildCardEvent(p.Key, card, createdAt)
	if err != nil {
		return res, err
	}

	issueID, issueEvent, err := p.ensureIssueEvent(card, createdAt)
	if err != nil {
		return res, err
	}

	se, err := BuildStatusEventWithIssueRoot(p.Key, card.ItemID, card.Status, ce.ID, issueID, cardBoardCoord(p.Key, card), reason, createdAt, card.Enc)
	if err != nil {
		return res, err
	}
	events := []*nostr.Event{ce}
	if issueEvent != nil {
		events = append(events, issueEvent)
	}
	events = append(events, se)
	return p.publishEvents(ctx, res, events)
}

// ensureIssueEvent returns the event id of card.ItemID's NIP-34 issue-root event
// (ready-da7), building+signing a NEW one for the caller to append+publish only
// when the item doesn't already have one in the local authoritative log. This
// keeps the interop anchor to exactly ONE extra event per item's lifetime — not
// one per status change/republish. Returns ("", nil, err) on a log read error;
// the caller then fails the whole publish (matching every other build-step
// error in this file) rather than silently skipping the anchor.
func (p *Publisher) ensureIssueEvent(card CardSpec, createdAt int64) (issueID string, newEvent *nostr.Event, err error) {
	// CONFIDENTIAL boards (ready-216): the NIP-34 kind:1621 issue event carries the
	// title (clear "subject" tag) and description (clear Content) for generic NIP-34
	// client interop. On a confidential board that would leak exactly the two most
	// sensitive free-text fields the envelope seals into the card — and a generic
	// client cannot read confidential content anyway — so suppress the issue-event
	// anchor entirely. The status event then simply carries no issue-root "e" tag
	// (BuildStatusEventWithIssueRoot treats issueEventID="" as "no anchor").
	if card.Enc != nil {
		return "", nil, nil
	}
	existing, err := p.Log.ReadAll()
	if err != nil {
		return "", nil, fmt.Errorf("sync: reading log for issue-root lookup: %w", err)
	}
	if id := FindIssueEventID(existing, card.ItemID); id != "" {
		return id, nil, nil
	}
	ie, err := BuildIssueEvent(p.Key, card, createdAt)
	if err != nil {
		return "", nil, err
	}
	return ie.ID, ie, nil
}

// PublishCardEdit publishes ONLY a refreshed 30302 card (no accompanying NIP-34
// status event) for a pure field edit — title/context/priority changes that do
// NOT change status (e.g. `rd progress`, `rd update --context`). This is the
// hybrid model's proof point (ready-b5f): the addressable card is a disposable,
// re-publishable CURRENT-state materialization; history lives exclusively in the
// append-only status-event chain built by PublishItem/PublishStatusChange. A
// card-only edit can therefore never add to, or erase, that history — `rd show`
// keeps replaying the exact same status transitions after any number of edits.
func (p *Publisher) PublishCardEdit(ctx context.Context, card CardSpec, createdAt int64) (PublishResult, error) {
	var res PublishResult
	res.ItemID = card.ItemID
	ce, err := BuildCardEvent(p.Key, card, createdAt)
	if err != nil {
		return res, err
	}
	return p.publishEvents(ctx, res, []*nostr.Event{ce})
}

// PublishBoard re-publishes EVERY event already durable in the local
// authoritative log — scoped to boardCoord when non-empty — to the write relays
// (ready-866, ready-615 edge #4: a project can accumulate many local-log-only
// events — created while a relay was unreachable, unset, or added after the
// fact — that the live per-mutation publish hooks never had a chance to send).
// `rd log publish` republishes ONE item at a time; PublishBoard is the
// board-level counterpart so a fresh box can converge from relays ALONE, with no
// scp of nostr-log.jsonl.
//
// Unlike PublishItem/PublishEvents, this does NOT append to the log (phase 1) —
// the events are already there by construction (they were read FROM the log).
// It only runs phase 2 (best-effort relay publish, buffer-on-failure), so a
// board publish is safe to re-run: every event is idempotent by nostr event id,
// and a relay that already holds an event answers OK,true (duplicate acceptance,
// not an error).
//
// boardCoord == "" publishes the WHOLE log unscoped (mirrors ReconcileAll's
// unscoped fallback for an unpinned project). A non-empty boardCoord selects:
//   - the 30301 board event itself, when its own coordinate (BoardCoord(author,
//     "d" tag)) equals boardCoord; and
//   - every other event (card, status, role-grant, ...) whose "a" tag equals
//     boardCoord — the same board-membership tag ReconcileBoard filters relay
//     queries by, so what this publishes is exactly what a board-scoped
//     reconcile on another machine will later fetch.
//
// ORDERING (ready-260): the scoped events are sent in ascending created_at
// order, ties broken by event id. Kinds 30301/30302/39301 are ADDRESSABLE — a
// relay keeps only the newest event per (kind, pubkey, d) coordinate — so the
// order a bulk republish sends them in decides what the relay's final
// projection is. Local-log APPEND order is not created_at order (events arrive
// from reconcile, negentropy download, and git-log merges in whatever order
// they were fetched), so sending in append order could hand a relay an OLDER
// card after a newer one. A well-behaved relay refuses the older one, but rd
// must not depend on that: sorting makes the last write per coordinate the
// newest by construction, so the relay's projection converges to the SAME
// latest-wins state the local log projects regardless of relay strictness.
//
// TRANSPORT (ready-260): a board publish uses the BATCHED relay path (one
// websocket per relay for the whole board) rather than one dial per event.
// Nothing about the bytes changes — each event still goes out verbatim with its
// original id, created_at and signature — only the number of connections does.
// A 4,500-event board over a scale-to-zero relay is minutes batched and hours
// dialed per event.
func (p *Publisher) PublishBoard(ctx context.Context, boardCoord string) (PublishResult, error) {
	scoped, err := p.boardScopedEvents(boardCoord)
	if err != nil {
		return PublishResult{}, err
	}
	if err := p.guardReservedBoard(scoped); err != nil {
		return PublishResult{}, err
	}
	var res PublishResult
	p.relayPublishBatch(ctx, &res, scoped)
	return res, nil
}

// boardScopedEvents reads the authoritative log and returns the events that
// belong to boardCoord (everything, when boardCoord is ""), id-deduped and
// sorted into the publish order PublishBoard's doc comment specifies. It is the
// SINGLE definition of "what a board publish would send", shared by the dry-run
// plan (PlanBoardPublish) and the real publish, so a plan can never describe a
// different event set than the write that follows it.
func (p *Publisher) boardScopedEvents(boardCoord string) ([]*nostr.Event, error) {
	events, err := p.Log.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("sync: reading log for board publish: %w", err)
	}
	seen := make(map[string]bool, len(events))
	scoped := make([]*nostr.Event, 0, len(events))
	for _, e := range events {
		if e == nil || seen[e.ID] {
			continue
		}
		if boardCoord == "" || EventBelongsToBoard(e, boardCoord) {
			seen[e.ID] = true
			scoped = append(scoped, e)
		}
	}
	sortForPublish(scoped)
	return scoped, nil
}

// sortForPublish orders events for a bulk republish: created_at ASCENDING, and
// on a same-second tie id DESCENDING.
//
// The tie direction is deliberate and is the inverse of the primary key. The
// canonical winner of a same-second collision between two addressable events is
// the one with the LOWEST id (newerThan / NIP-33), so sending ids in DESCENDING
// order within a second makes that winner the LAST event written for its
// coordinate. A relay implementing the NIP-33 tie-break retains it either way; a
// relay that simply keeps the last write retains it too. Sorting ties ascending
// would invert the winner on the latter.
//
// Shared by the full board publish and the delta repair (BoardRelayDelta), so a
// gap-closing re-send lands in the same order a full one would.
func sortForPublish(events []*nostr.Event) {
	sort.SliceStable(events, func(i, j int) bool {
		if events[i].CreatedAt != events[j].CreatedAt {
			return events[i].CreatedAt < events[j].CreatedAt
		}
		return events[i].ID > events[j].ID
	})
}

// PublishBoardDelta closes the measured gap between the local log and ONE relay
// for one board: it audits (a fresh read-back), publishes ONLY the events that
// relay is missing or holds a stale version of, and returns the audit taken
// BEFORE the repair plus the number of events sent.
//
// It publishes to relayURL alone, not to the configured write relay set — the
// gap it measured is that relay's gap, and mixing in relays that already hold
// the events would put the ambiguous "accepted by ANY relay" report back in the
// middle of a repair whose entire purpose is to be unambiguous about one relay.
func (p *Publisher) PublishBoardDelta(ctx context.Context, relayURL, boardCoord string) (BoardRelayAudit, int, error) {
	local, err := p.Log.ReadAll()
	if err != nil {
		return BoardRelayAudit{}, 0, fmt.Errorf("sync: reading log for board repair: %w", err)
	}
	audit, delta, err := BoardRelayDelta(ctx, relayURL, local, boardCoord)
	if err != nil {
		return audit, 0, err
	}
	if len(delta) == 0 {
		return audit, 0, nil
	}
	if err := p.guardReservedBoard(delta); err != nil {
		return audit, 0, err
	}
	one := *p
	one.WriteRelays = []string{relayURL}
	var res PublishResult
	one.relayPublishBatch(ctx, &res, delta)
	return audit, len(delta), nil
}

// BoardPublishPlan is the DRY-RUN description of a board publish (ready-260):
// exactly what PublishBoard would send, computed from the local authoritative
// log with no network access at all. A bulk republish writes to production data
// across every board in a portfolio, so the operator gets to see the shape of
// the write — how many events, of which kinds, over what time range, and
// whether the board's own definition event is even present locally — BEFORE any
// relay is dialed.
type BoardPublishPlan struct {
	// BoardCoord is the scope; "" means the whole local log (unpinned project).
	BoardCoord string `json:"board_coord"`
	// Relays are the write relays the real publish would target.
	Relays []string `json:"relays"`
	// Events is the total number of distinct events that would be sent.
	Events int `json:"events"`
	// ByKind counts the events per nostr kind (map key is the kind as a string
	// so the JSON form is stable and self-describing).
	ByKind map[string]int `json:"by_kind"`
	// Items is the number of DISTINCT work items in scope (distinct "d" tags
	// across kind-30302 cards) — the number a board reader ultimately renders,
	// as opposed to the raw event count which includes every historical
	// re-materialization of each card.
	Items int `json:"items"`
	// HasBoardDefinition reports whether a kind-30301 definition event for this
	// exact coordinate is present locally. False means the board cannot be made
	// discoverable by publishing, no matter how many cards go out — the reader
	// enumerates boards from 30301 events.
	HasBoardDefinition bool `json:"has_board_definition"`
	// OldestCreatedAt/NewestCreatedAt bound the scoped events' timestamps (0
	// when there are none).
	OldestCreatedAt int64 `json:"oldest_created_at"`
	NewestCreatedAt int64 `json:"newest_created_at"`
}

// PlanBoardPublish computes the dry run for PublishBoard(boardCoord). It reads
// only the local log and touches no relay, so it is safe to run against every
// project in a portfolio before deciding to write anything.
func (p *Publisher) PlanBoardPublish(boardCoord string) (BoardPublishPlan, error) {
	scoped, err := p.boardScopedEvents(boardCoord)
	if err != nil {
		return BoardPublishPlan{}, err
	}
	plan := BoardPublishPlan{
		BoardCoord: boardCoord,
		Relays:     append([]string(nil), p.WriteRelays...),
		Events:     len(scoped),
		ByKind:     map[string]int{},
	}
	items := map[string]bool{}
	for _, e := range scoped {
		plan.ByKind[strconv.Itoa(e.Kind)]++
		if e.Kind == KindCard {
			if d := tagValue(e, "d"); d != "" {
				items[d] = true
			}
		}
		if e.Kind == KindBoard && BoardCoord(e.PubKey, tagValue(e, "d")) == boardCoord {
			plan.HasBoardDefinition = true
		}
		if plan.OldestCreatedAt == 0 || e.CreatedAt < plan.OldestCreatedAt {
			plan.OldestCreatedAt = e.CreatedAt
		}
		if e.CreatedAt > plan.NewestCreatedAt {
			plan.NewestCreatedAt = e.CreatedAt
		}
	}
	plan.Items = len(items)
	return plan, nil
}

// EventBelongsToBoard reports whether e is a member of the board named by
// boardCoord ("30301:<author>:<boardD>"): either e IS that board's own 30301
// event, or e carries an "a" tag equal to boardCoord.
//
// Checks EVERY "a" tag (tagValues), not just the first: a NIP-34 status event
// carries the card's OWN coordinate as its FIRST "a" tag and the board
// coordinate as a SECOND, additive "a" tag (ready-7ec, BuildStatusEventWithIssueRoot)
// — the same any-match semantics BoardSyncFilter's relay-side "#a" filter uses
// (a relay matches ANY tag with that name, not just the first). Using tagValue
// (first-only) here would silently drop every status event from a board publish.
//
// EXPORTED (ready-54e) so cmd/rd/follow.go can call this ONE implementation
// instead of reimplementing the any-"a"-tag membership check locally.
func EventBelongsToBoard(e *nostr.Event, boardCoord string) bool {
	if e.Kind == KindBoard {
		return BoardCoord(e.PubKey, tagValue(e, "d")) == boardCoord
	}
	for _, a := range tagValues(e, "a") {
		if a == boardCoord {
			return true
		}
	}
	return false
}

// PublishEvents appends a pre-built signed-event slice to the authoritative log
// and best-effort publishes it to the write relays — the low-level primitive the
// ready-d65 migration drives (board + per-item card + history status events built
// by BuildItemMigrationEvents). Same durability contract as PublishItem: the log
// append MUST succeed; a relay failure only buffers, never fails.
func (p *Publisher) PublishEvents(ctx context.Context, events []*nostr.Event) (PublishResult, error) {
	return p.publishEvents(ctx, PublishResult{}, events)
}

// PublishEventsUnique is the RE-RUN-SAFE variant PublishEvents that the ready-d65
// migration uses. It appends only events whose id is not already in the local log
// (dedup by content-hash event id, via AppendUnique) and publishes ONLY those
// newly-added events to the relays. Re-migrating the same item set therefore adds
// nothing to the log and issues no redundant relay writes — the "idempotent by
// nostr event id, re-run safe" guarantee (a plain Append would balloon the log with
// byte-identical duplicates on every re-run, even though projection stays
// idempotent). Returns the number of events actually added.
func (p *Publisher) PublishEventsUnique(ctx context.Context, events []*nostr.Event) (PublishResult, int, error) {
	if err := p.guardReservedBoard(events); err != nil {
		return PublishResult{}, 0, err
	}
	// The log append MUST succeed regardless of any event's size (ready-c3e
	// REWORK — see nostrsize.go's doc): this path (ready-d65 migration) also
	// mints and appends NEW events, not a verbatim replay of history, so it
	// gets the SAME oversized-event handling as publishEvents below — refused
	// at the relay dial only, via splitOversized after the append, never here.
	added, err := p.Log.AppendUnique(events)
	if err != nil {
		return PublishResult{}, 0, fmt.Errorf("sync: appending unique to authoritative log: %w", err)
	}
	// When AppendUnique added nothing (a full re-run), skip the relays entirely — a
	// re-run issues zero redundant writes. Otherwise relay-publish the distinct input
	// events; relays dedup by id, so re-sending an already-present id is harmless, and
	// the costly disk log is the part AppendUnique already deduped. (We don't have the
	// exact "before" id set here, so this over-approximates the fresh set upward only,
	// never dropping a genuinely new event.)
	res := PublishResult{}
	deliverable := p.splitOversized(&res, distinctIfAdded(events, added))
	p.relayPublish(ctx, &res, deliverable)
	return res, added, nil
}

// distinctIfAdded returns the id-deduped input events when added>0, or nil when
// added==0 (nothing new — skip the relays). Deduping within the input means a slice
// with internal repeats publishes each id at most once.
func distinctIfAdded(events []*nostr.Event, added int) []*nostr.Event {
	if added == 0 {
		return nil
	}
	seen := make(map[string]bool, len(events))
	out := make([]*nostr.Event, 0, len(events))
	for _, e := range events {
		if e == nil || seen[e.ID] {
			continue
		}
		seen[e.ID] = true
		out = append(out, e)
	}
	return out
}

func (p *Publisher) publishEvents(ctx context.Context, res PublishResult, events []*nostr.Event) (PublishResult, error) {
	if err := p.guardReservedBoard(events); err != nil {
		return res, err
	}
	// Phase 1 — append to the authoritative log. This MUST succeed for EVERY
	// event, regardless of size — it is the durability guarantee independent
	// of any relay, and independent of whether the event can ever reach one
	// (ready-c3e REWORK). An earlier version of this guard refused an
	// oversized event here too, before Log.Append — which froze every future
	// status transition for the item, since PublishStatusChange/
	// PublishCardEdit rebuild and re-check the FULL current card on every
	// call. See nostrsize.go's doc for the full account.
	for _, e := range events {
		if err := p.Log.Append(e); err != nil {
			return res, fmt.Errorf("sync: appending to authoritative log: %w", err)
		}
	}
	// Phase 2 — publish to write relays, best-effort, EXCEPT any event this
	// client already knows exceeds every relay's size ceiling (ready-c3e):
	// splitOversized dead-letters those directly (no relay dial — the outcome
	// is already certain) and returns only the deliverable remainder. This
	// must not apply to PublishBoard/PublishBoardDelta's verbatim replay of
	// already-signed history (nostrsize.go's doc explains why).
	deliverable := p.splitOversized(&res, events)
	p.relayPublish(ctx, &res, deliverable)
	return res, nil
}

// splitOversized partitions events into those safe to hand to relayPublish and
// those this client already knows exceed maxEventWireSize (ready-c3e REWORK).
// An oversized event is NEVER dialed to a relay — the outcome is already
// certain — and is instead given the SAME disposition applyRelayOutcome gives
// a live relay's own "invalid: ... exceeds ... max" reply: dead-lettered (with
// the existing across-round dedup, finding 2 below, so retrying the identical
// oversized event never grows nostr-rejected.jsonl further) or, with no
// PendingPath configured, a stderr warning — res.Rejected is set either way.
// This is deliberately NOT an error return: by the time this runs the event is
// already durable in the local authoritative log (the caller's Log.Append, in
// publishEvents/PublishEventsUnique, always runs first), so refusing here only
// ever affects the relay attempt, never the local write.
func (p *Publisher) splitOversized(res *PublishResult, events []*nostr.Event) []*nostr.Event {
	deliverable := make([]*nostr.Event, 0, len(events))
	for _, e := range events {
		if e == nil {
			continue
		}
		n, over, err := oversizedEvent(e)
		if err != nil || !over {
			// Can't measure it, or it's within the limit — let the relay be
			// the judge, exactly as before this guard existed.
			deliverable = append(deliverable, e)
			continue
		}
		reason := fmt.Sprintf(
			"client-side size guard (ready-c3e): %s is %d bytes, exceeds the 64KiB (%d byte) relay limit every relay in this fleet enforces (strfry default) — no relay was dialed, since every relay in the fleet refuses this size; durable in the local authoritative log regardless — shrink its description/context/progress notes below the limit and republish to reach a relay",
			eventSubjectLabel(e), n, maxEventWireSize,
		)
		p.applyRelayOutcome(res, e, nil, outcomePermanent, reason)
	}
	return deliverable
}

// relayPublish publishes each event to every write relay, best-effort, recording
// per-relay acks and buffering any event that reaches no relay. It NEVER fails —
// the events are already durable in the local log by the time this runs.
func (p *Publisher) relayPublish(ctx context.Context, res *PublishResult, events []*nostr.Event) {
	timeout := p.Timeout
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}
	reachedRelay := false
	for _, e := range events {
		attempts, outcome, permReason := publishEventToRelays(ctx, p.WriteRelays, e, timeout, p.Production)
		if p.applyRelayOutcome(res, e, attempts, outcome, permReason) {
			reachedRelay = true
		}
	}

	// Auto-drain the offline backlog. If this publish proved relay connectivity,
	// flush any events earlier offline writes buffered to nostr-pending.jsonl —
	// so reconnecting and writing anything empties the queue on its own and no
	// human ever runs a manual flush. Best-effort and gated on a non-empty buffer
	// so it costs nothing on the common path; never fails the operation (the events
	// are already durable in the local log). Re-publish is idempotent by event id.
	if reachedRelay && p.PendingPath != "" && fileHasContent(p.PendingPath) {
		_, _ = FlushNostrPending(ctx, p.PendingPath, p.WriteRelays, timeout, p.Production)
	}
}

// applyRelayOutcome records ONE event's per-relay attempts on res and carries
// out the disposition its reduced outcome demands: accepted -> nothing, permanent
// -> dead-letter (ready-1c2), transient -> buffer for retry. It returns whether
// at least one relay accepted the event (the caller's auto-drain trigger).
//
// EXTRACTED (ready-260) so the per-event dial path (relayPublish) and the
// batched one-connection path (relayPublishBatch) share a SINGLE definition of
// what happens to an event after the relays have spoken. Duplicating this
// switch was the obvious alternative and the wrong one: the two paths would
// then be free to drift on dead-lettering, buffering, or ack shape, and a bulk
// backfill would silently behave differently from an ordinary write.
func (p *Publisher) applyRelayOutcome(res *PublishResult, e *nostr.Event, attempts []relayAttempt, outcome relayOutcome, permReason string) bool {
	ack := EventAck{EventID: e.ID, Kind: e.Kind}
	for _, a := range attempts {
		ra := RelayAck{Relay: a.Relay, Accepted: a.Accepted, Message: a.Message}
		if a.Err != nil {
			ra.Err = a.Err.Error()
		}
		// Classified outcome, not the raw bool: an OK,false "duplicate:" reply
		// means the relay already holds the event, so it counts as delivered.
		if a.Outcome == outcomeAccepted {
			ack.AnyRelay = true
		}
		ack.Acks = append(ack.Acks, ra)
	}

	switch outcome {
	case outcomeAccepted:
		// Stored by at least one relay — nothing to buffer.
	case outcomePermanent:
		// No relay will ever accept this exact event. Dead-letter it instead
		// of re-buffering forever (ready-1c2). Still durable in the log.
		ack.Permanent = true
		ack.Reason = permReason
		if p.PendingPath == "" {
			// No on-disk queue configured; the event remains durable in the
			// authoritative log. Surface it so it is not silently lost.
			res.Rejected = true
			fmt.Fprintf(os.Stderr, "warning: sync: relay permanently rejected event %s (%s); no pending path — retained in authoritative log only\n", e.ID, permReason)
			break
		}
		rejectedPath := rejectedPathFor(p.PendingPath)
		if deadLetterHasEvent(rejectedPath, e.ID) {
			// Already recorded by a PRIOR round/run (ready-c3e finding 2: a
			// permanent rejection re-discovered on every repair round used to
			// re-append here every time, growing the file without bound). The
			// fact hasn't changed — report it exactly as if freshly dead-lettered,
			// just don't write a redundant copy.
			res.Rejected = true
			break
		}
		if derr := appendRejectedEvent(rejectedPath, RejectedRecord{Event: e, Reason: permReason}); derr != nil {
			// Dead-letter write failed — fall back to the retry buffer so the
			// event is not lost from EVERY on-disk queue (symmetry with
			// FlushNostrPending; the fresh publish must not be the odd path).
			fmt.Fprintf(os.Stderr, "warning: sync: dead-letter write failed (%v); buffering %s for retry instead\n", derr, e.ID)
			if bufErr := appendPendingEvent(p.PendingPath, e); bufErr != nil {
				fmt.Fprintf(os.Stderr, "warning: sync: buffering nostr event to pending: %v\n", bufErr)
			}
			res.Buffered = true
			break
		}
		res.Rejected = true
		fmt.Fprintf(os.Stderr, "warning: sync: relay permanently rejected event %s (%s) — dead-lettered to %s, will NOT retry\n", e.ID, permReason, NostrRejectedFile)
	default: // outcomeTransient
		// Reached no relay for a retryable reason — buffer for later flush.
		// Already durable in the log.
		res.Buffered = true
		if p.PendingPath != "" {
			if bufErr := appendPendingEvent(p.PendingPath, e); bufErr != nil {
				fmt.Fprintf(os.Stderr, "warning: sync: buffering nostr event to pending: %v\n", bufErr)
			}
		}
	}
	res.Events = append(res.Events, ack)
	return ack.AnyRelay
}

// relayPublishBatch is relayPublish's BULK counterpart (ready-260): it publishes
// the whole event slice to each write relay over ONE websocket connection
// (GuardedPublishMany) instead of one dial per event, then applies the IDENTICAL
// per-event disposition via applyRelayOutcome — same classification, same
// dead-lettering, same buffering, same ack shape.
//
// The per-relay ack is retained for EVERY event, which is the point: a backfill's
// whole risk is that one relay (the public one) silently rejects everything while
// the LAN relays accept, and reduceEventOutcome then reports the event as
// accepted. The reduced outcome still drives buffering (an event durable on ANY
// relay needs no retry), but the caller can read the per-relay Acks to see
// exactly which relay took what — and the authoritative check remains an
// independent read-back (AuditBoardOnRelay), never this report.
func (p *Publisher) relayPublishBatch(ctx context.Context, res *PublishResult, events []*nostr.Event) {
	if len(events) == 0 {
		return
	}
	// attempts[i] accumulates one relayAttempt per write relay for events[i].
	attempts := make([][]relayAttempt, len(events))
	for _, relay := range p.WriteRelays {
		acks, err := GuardedPublishMany(ctx, relay, events, p.Production)
		for i := range events {
			var a relayAttempt
			if i < len(acks) {
				ak := acks[i]
				a = relayAttempt{Relay: relay, Accepted: ak.Accepted, Message: ak.Message, Err: ak.Err}
			} else {
				// PublishMany always returns len(events) acks; this is a
				// belt-and-braces fallback so a future contract change cannot
				// turn into an index panic mid-backfill.
				a = relayAttempt{Relay: relay, Err: err}
			}
			a.Outcome = classifyRelayResult(a.Accepted, a.Message, a.Err)
			attempts[i] = append(attempts[i], a)
		}
	}

	reachedRelay := false
	for i, e := range events {
		outcomes := make([]relayOutcome, 0, len(attempts[i]))
		permReason := ""
		for _, a := range attempts[i] {
			if a.Outcome == outcomePermanent && permReason == "" {
				permReason = relayLabel(a.Relay, a.Message)
			}
			outcomes = append(outcomes, a.Outcome)
		}
		if p.applyRelayOutcome(res, e, attempts[i], reduceEventOutcome(outcomes), permReason) {
			reachedRelay = true
		}
	}

	timeout := p.Timeout
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}
	if reachedRelay && p.PendingPath != "" && fileHasContent(p.PendingPath) {
		_, _ = FlushNostrPending(ctx, p.PendingPath, p.WriteRelays, timeout, p.Production)
	}
}

// fileHasContent reports whether path exists and is non-empty, so the auto-drain
// only dials relays when there is actually a backlog to flush.
func fileHasContent(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}

// appendPendingEvent appends a signed event to the nostr pending buffer (JSONL).
func appendPendingEvent(path string, e *nostr.Event) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	// Serialize against a concurrent FlushNostrPending rewrite (ready review [6]).
	unlock, err := lockPending(path)
	if err != nil {
		return err
	}
	defer unlock()
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	data, err := json.Marshal(e)
	if err != nil {
		return err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return f.Sync()
}
