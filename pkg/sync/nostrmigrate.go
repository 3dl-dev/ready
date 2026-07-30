// Nostr migration + parity (ready-d65 — the CUTOVER, non-destructive scope).
//
// This file re-emits an EXISTING campfire rd item set as nostr events so the
// nostr projection reproduces every item item-for-item, then proves that parity
// field-for-field. It is the bulk, history-preserving counterpart to the live
// write-path hooks in cmd/rd (which only publish NEW mutations going forward):
// a migration must faithfully replay each item's ALREADY-ACCUMULATED audit trail.
//
// WHAT THE MIGRATION PRESERVES (epic ready-a14 hybrid model):
//   - id, title, status, priority, type, context, deps, gates, labels, eta,
//     assignee — all materialized onto ONE addressable 30302 card (current state).
//   - The FULL audit trail — one NIP-34 status event PER campfire history entry,
//     each carrying the entry's original created_at (second granularity), its
//     close/change reason (content), and — the migration's key move — the ORIGINAL
//     actor in an rd-extension "by" tag so provenance ("who did what") survives even
//     though the portfolio key is the only signer. ProjectItems reads that "by" tag
//     back (see nostrproject.go), reconstructing item-for-item history + provenance.
//
// IDEMPOTENCE: every event id is a content hash over (kind, pubkey, created_at,
// tags, content). Re-running the migration over the same item set re-derives the
// identical events, and NostrLog.AppendUnique / relay dedup drop the repeats — so a
// re-run adds nothing and can never fork or duplicate an item's history (ready-f92).
//
// SAME-SECOND ORDERING LIMITATION (accepted, ready-194): created_at is seconds, so
// two campfire history entries that share a second become two status events with
// equal created_at; replay orders them by the NIP-01 id tie-break (lowest id first),
// which is a deterministic total order but not necessarily the original wall-clock
// order. rd's parity is therefore asserted on the ORDER-INDEPENDENT projection of
// history — length + the multiset of (to_status, note, actor) — not on the exact
// sequence of a same-second cluster. See docs/nostr-migration.md.
package sync

import (
	"fmt"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// BuildHistoricalStatusEvent constructs and signs a NIP-34 status event that
// REPLAYS one pre-existing campfire history entry. It differs from
// BuildStatusEvent (the live-write builder) in two migration-specific ways:
//   - createdAt is the ENTRY's original timestamp (seconds), not "now", so replay
//     reconstructs the historical ordering and `rd show` timestamps.
//   - changedBy, when set, is carried in an rd-extension "by" tag so the ORIGINAL
//     actor survives even though the portfolio key signs the event. ProjectItems
//     prefers this tag over the signer when reconstructing HistoryEntry.ChangedBy.
func BuildHistoricalStatusEvent(k *nostr.Key, itemID, rdStatus, changedBy, reason string, createdAt int64) (*nostr.Event, error) {
	if itemID == "" {
		return nil, fmt.Errorf("sync: historical status event: empty item id")
	}
	if rdStatus == "" {
		return nil, fmt.Errorf("sync: historical status event: empty status")
	}
	tags := [][]string{
		{"a", CardCoord(k.PubKeyHex(), itemID)},
		{"d", itemID},
		{"status", rdStatus},
	}
	if changedBy != "" {
		tags = append(tags, []string{"by", changedBy})
	}
	e := &nostr.Event{
		Kind:      statusKindFor(rdStatus),
		CreatedAt: createdAt,
		Tags:      tags,
		Content:   reason,
	}
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: sign historical status event: %w", err)
	}
	return e, nil
}

// BuildHistoricalStatusEventWithBoard is BuildHistoricalStatusEvent PLUS an
// additional board-membership "a" tag (ready-7ec), when boardCoord is
// non-empty — the migration-replay counterpart to BuildStatusEventWithIssueRoot's
// board tag, so a board-scoped negentropy filter matches MIGRATED status events
// too, not just live-written ones. boardCoord == "" reproduces
// BuildHistoricalStatusEvent's output exactly, so every existing caller
// (including every current test) is untouched.
func BuildHistoricalStatusEventWithBoard(k *nostr.Key, itemID, rdStatus, changedBy, boardCoord, reason string, createdAt int64) (*nostr.Event, error) {
	e, err := BuildHistoricalStatusEvent(k, itemID, rdStatus, changedBy, reason, createdAt)
	if err != nil {
		return nil, err
	}
	if boardCoord == "" {
		return e, nil
	}
	e.Tags = append(e.Tags, []string{"a", boardCoord})
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: sign historical status event (board): %w", err)
	}
	return e, nil
}

// CardSpecFromItem materializes a wire CardSpec from a derived *state.Item's
// CURRENT state. It is the SINGLE source of truth for the item->card field mapping
// (ready-187): the migration, every live write-path republish (create/claim/
// progress/close), and `rd nostr publish` all route through it, so no publish path
// can silently omit a field and clobber it on the latest-wins card. It carries the
// full item — deps, gate, waiting, labels, eta, assignee AND the ready-187 additions
// (humanness level, assignment scope For, parent/child tree edge, due) — so a single
// latest-wins 30302 card reproduces the WHOLE item item-for-item.
//
// Status is carried verbatim, deliberately including StatusBlocked: this
// function also builds the CardSpec for brand-NEW items (publishItemFullCreateNostr),
// and a freshly-constructed *state.Item legitimately has no history yet to
// derive anything from — a caller (e.g. a test fixture, or a future template
// feature) that explicitly wants a new item to start out blocked must be able
// to say so. The ready-500 guard against REPUBLISHING a derived-blocked status
// therefore does NOT live here — see NonDerivedStatus below, called explicitly
// by every REPUBLISH hook (an existing item's projected current state being
// read back and re-emitted), never by create.
func CardSpecFromItem(item *state.Item, boardD string) CardSpec {
	return CardSpec{
		ItemID:   item.ID,
		Title:    item.Title,
		Status:   item.Status,
		Priority: item.Priority,
		Assignee: item.By,
		Type:     item.Type,
		// Context is the card's BASE description ONLY — the projection guarantees
		// item.Context never holds the progress trail (ready-ed4, nostrproject.go's
		// SplitCardTrail call). That is what stops a republish from re-inflating the
		// card with every note ever written on the item.
		Context: item.Context,
		// ...and the notes that have no event of their own yet ride along, so the
		// compacted card and the events preserving what it dropped are published in
		// the SAME batch. Empty on the overwhelmingly common path (a card written
		// after this change carries no inline trail to recover).
		PendingNotes: PendingNotes(item.Notes),
		BoardD:       boardD,
		Deps:         item.BlockedBy,
		Gate:         item.Gate,
		WaitingType:  item.WaitingType,
		WaitingOn:    item.WaitingOn,
		Labels:       item.Labels,
		ETA:          item.ETA,
		Level:        item.Level,
		For:          item.For,
		ParentID:     item.ParentID,
		Due:          item.Due,
		// CREATION TIME CARRY-FORWARD (ready-4ec rework): every live republish
		// (update/claim/close/cancel/delegate/gate/approve/dep add) funnels through
		// this function, so re-emitting the item's CURRENT CreatedAt as the card's
		// "created" tag on EVERY call is what makes the value immutable once set —
		// see CardSpec.CreatedAt's doc. itemCreatedAtSecs returns 0 (emit NO tag)
		// when the item's true creation time is genuinely unknown yet: a brand-new
		// live-created item (Item.CreatedAt unset, no history) must NOT freeze on a
		// fabricated value — it inherits its own genesis card's real event
		// created_at via itemFromCard's fallback, exactly once, then this function
		// carries THAT value forward on every subsequent call once the projection
		// reads it back into Item.CreatedAt.
		CreatedAt: itemCreatedAtSecs(item),
	}
}

// NonDerivedStatus is the ready-500 class-wide guard, generalizing ready-e0e's
// single-path reject fix: 'blocked' is DERIVED every fold by
// applyDepAndGateStatus's dep pass (pkg/sync/nostrproject.go), which only ever
// ADDS it from a live non-terminal blocker and NEVER clears a written one. Any
// *state.Item a caller resolved from the projection and is about to REPUBLISH
// (as opposed to originating fresh — see CardSpecFromItem's doc for why create
// is exempt) can carry that derived overlay in item.Status. Call this explicitly
// right before building the outbound CardSpec on every republish path — it is
// called from cmd/rd/nostr.go's publishItemStatusChangeNostr and
// publishItemCardEditNostr (the two hooks every live mutation command routes
// through) and from the manual `rd nostr publish` command, so the substitution
// covers every current republish call site uniformly. A brand new republish
// hook must call this too; CardSpecFromItem's own doc comment points back here
// so the omission is not silent.
//
// item.Status != StatusBlocked passes through unchanged: every explicit write
// (claim->active, close->terminal, gate/reject->waiting, approve->active,
// update --status <anything but blocked>) already assigns a real target status
// before reaching here, so this is a no-op for them — the risk is only ever a
// caller (e.g. delegate, or a field-only edit) that republishes an item without
// itself deciding a new status.
//
// When item.Status == StatusBlocked, the safe value to publish is the item's
// own last AUTHORITATIVE status that was NOT itself a derived/burned-in
// "blocked" — found by walking item.History from the tail backwards. This
// deliberately does not stop at the LAST entry: a PRE-BURNED-IN item — one
// already republished with status=blocked verbatim by exactly the buggy code
// this fix corrects — has "blocked" sitting as its own most recent history
// ToStatus, and trusting only that single entry would republish blocked again
// and PERPETUATE the burn-in instead of healing it. Walking back past any
// number of burned-in "blocked" entries recovers the real prior status.
//
// If no non-blocked entry exists anywhere in History — because History is
// empty (a card-only item with no authoritative status event at all: a
// non-maintainer's card-only republish on a multi-agent board strips every
// status event not authored by the winning card's author or a board
// maintainer, per pkg/sync/nostrproject.go's authoritative-status filter; a
// partial relay reconcile that delivers the card but not its status chain does
// the same), or because EVERY entry in it is itself "blocked" (fully
// burned-in, no real status ever recorded) — there is no authoritative,
// non-derived value left to recover. The fallback is then the same explicit
// default a brand-new `rd create` gives an item: state.StatusInbox. This is a
// deliberate, NAMED default — never a silent pass-through of the derived
// "blocked" value the guard exists to stop.
func NonDerivedStatus(item *state.Item) string {
	if item.Status != state.StatusBlocked {
		return item.Status
	}
	for i := len(item.History) - 1; i >= 0; i-- {
		if item.History[i].ToStatus != state.StatusBlocked {
			return item.History[i].ToStatus
		}
	}
	return state.StatusInbox
}

// itemCreatedAtSecs returns the item's create timestamp in unix seconds: the
// item's own CreatedAt when set, else the earliest parseable history entry (a
// pre-nostr/migrated item whose true creation time survives only in its history),
// else 0 — meaning genuinely unknown, NOT a fabricated sentinel. 0 is
// deliberately NOT coerced to any nonzero placeholder: CardSpecFromItem treats 0
// as "carry no created tag yet", and BuildCardEvent/itemFromCard's fallback (the
// card event's OWN created_at) supplies a correct value for exactly the one case
// this arises — a fresh item's first-ever card, before it has anything to carry.
// A fabricated nonzero default here would instead freeze that card's "created"
// tag at the fabricated value forever, which is worse than emitting no tag.
func itemCreatedAtSecs(item *state.Item) int64 {
	if item.CreatedAt > 0 {
		return item.CreatedAt / int64(time.Second)
	}
	for _, h := range item.History {
		if s := historyEntrySecs(h); s > 0 {
			return s
		}
	}
	return 0
}

// historyEntrySecs parses a history entry timestamp to unix seconds. Campfire
// timestamps are RFC3339(/Nano) UTC. Returns 0 on parse failure (the caller then
// spaces the event just after the card so it still replays in order).
func historyEntrySecs(h state.HistoryEntry) int64 {
	if h.Timestamp == "" {
		return 0
	}
	if t, err := time.Parse(time.RFC3339Nano, h.Timestamp); err == nil {
		return t.UTC().Unix()
	}
	if t, err := time.Parse(time.RFC3339, h.Timestamp); err == nil {
		return t.UTC().Unix()
	}
	return 0
}
