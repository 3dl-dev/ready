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
func CardSpecFromItem(item *state.Item, boardD string) CardSpec {
	return CardSpec{
		ItemID:      item.ID,
		Title:       item.Title,
		Status:      item.Status,
		Priority:    item.Priority,
		Assignee:    item.By,
		Type:        item.Type,
		Context:     item.Context,
		BoardD:      boardD,
		Deps:        item.BlockedBy,
		Gate:        item.Gate,
		WaitingType: item.WaitingType,
		WaitingOn:   item.WaitingOn,
		Labels:      item.Labels,
		ETA:         item.ETA,
		Level:       item.Level,
		For:         item.For,
		ParentID:    item.ParentID,
		Due:         item.Due,
	}
}

// itemCreatedAtSecs returns the item's create timestamp in unix seconds, falling
// back to the earliest history entry (then to 1) when CreatedAt is unset.
func itemCreatedAtSecs(item *state.Item) int64 {
	if item.CreatedAt > 0 {
		return item.CreatedAt / int64(time.Second)
	}
	for _, h := range item.History {
		if s := historyEntrySecs(h); s > 0 {
			return s
		}
	}
	return 1
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
