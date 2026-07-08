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
	"sort"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
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

// cardSpecFromItem materializes a wire CardSpec from a derived *state.Item's
// CURRENT state — the same field mapping the live write path uses, kept here so
// the migration and the CLI agree byte-for-byte. It carries deps, gate, waiting,
// labels, eta and assignee so a single latest-wins card reproduces the whole item.
func cardSpecFromItem(item *state.Item, boardD string) CardSpec {
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

// BuildItemMigrationEvents re-emits ONE existing rd item as the nostr event set
// that reproduces it: the 30302 card materializing current state, plus one NIP-34
// status event per history entry (original timestamp, reason, and actor). Board is
// emitted by the caller (once per project), not here. The returned events are in
// publish order (card first, then history oldest→newest); the caller appends them
// to the authoritative log and best-effort publishes to the relays.
//
// The card's created_at is stamped just after the newest history entry so the
// single materialized card always wins latest-wins against nothing (it is the only
// card for this item), and never competes with a status event's second.
func BuildItemMigrationEvents(k *nostr.Key, boardD string, item *state.Item) ([]*nostr.Event, error) {
	if item == nil || item.ID == "" {
		return nil, fmt.Errorf("sync: migrate: nil/empty item")
	}

	// History events first (we need their max second to stamp the card).
	//
	// ORDER PRESERVATION vs the same-second limitation (ready-194): campfire's
	// history array is already in true chronological order (monotonic-nanosecond
	// authority). created_at is seconds, so two entries that fall in the SAME second
	// would tie on replay and be reordered by the NIP-01 id tie-break — which can
	// silently CORRUPT an item's CURRENT status (e.g. a create+cancel in one second
	// projecting back as "inbox" because the lower-id cancel sorts before the create).
	// A migration knows the ground-truth order, so it must not throw it away: within
	// ONE item's chain we stamp strictly-INCREASING created_at, nudging a colliding
	// entry forward by whole seconds. This preserves the terminal status and the full
	// per-item ordering exactly; the only cost is that a same-second entry's displayed
	// timestamp may shift up to a few seconds (the accepted seconds-granularity limit,
	// documented in docs/nostr-migration.md). CROSS-item same-second concurrency stays
	// the documented ready-194 limitation — this only orders an item against itself.
	base := itemCreatedAtSecs(item)
	var statusEvents []*nostr.Event
	var maxSec int64 = base
	var prevSec int64
	for i, h := range item.History {
		sec := historyEntrySecs(h)
		if sec == 0 {
			// Unparseable timestamp — space it deterministically after the base so
			// the chain still replays in recorded order without colliding.
			sec = base + int64(i)
		}
		if sec <= prevSec {
			sec = prevSec + 1 // strictly increasing: preserve the known chain order
		}
		prevSec = sec
		toStatus := h.ToStatus
		if toStatus == "" {
			toStatus = item.Status
		}
		if toStatus == "" {
			toStatus = state.StatusInbox
		}
		se, err := BuildHistoricalStatusEvent(k, item.ID, toStatus, h.ChangedBy, h.Note, sec)
		if err != nil {
			return nil, fmt.Errorf("sync: migrate %s history[%d]: %w", item.ID, i, err)
		}
		statusEvents = append(statusEvents, se)
		if sec > maxSec {
			maxSec = sec
		}
	}

	// The card materializes CURRENT state and must win latest-wins. Stamp it at the
	// item's UpdatedAt second, floored to just-after the newest status event.
	//
	// Why UpdatedAt and not maxSec-of-history: card-only edits (rd dep add, label
	// add/remove, defer) change the item's current state WITHOUT adding a history/
	// status transition, and they advance UpdatedAt but NOT the max history second.
	// If the card were stamped at maxSec, an edited item's card would carry the same
	// created_at as a PRIOR migration's card of the same item — so on a shared relay
	// the two same-second cards tie and the NIP-01 id tie-break can serve the STALE
	// content (observed: a 16-dep item read back with only its 4 original deps).
	// Stamping at UpdatedAt makes the card's created_at advance on every real edit, so
	// the current card deterministically beats any older one. It stays idempotent: for
	// a fixed source, UpdatedAt is fixed, so a re-run re-derives the identical card id.
	updatedSec := item.UpdatedAt / int64(time.Second)
	cardAt := maxSec
	if updatedSec > cardAt {
		cardAt = updatedSec
	}
	card := cardSpecFromItem(item, boardD)
	ce, err := BuildCardEvent(k, card, cardAt)
	if err != nil {
		return nil, fmt.Errorf("sync: migrate %s card: %w", item.ID, err)
	}

	events := make([]*nostr.Event, 0, len(statusEvents)+1)
	events = append(events, ce)
	events = append(events, statusEvents...)
	return events, nil
}

// ItemParity is the per-item result of comparing a campfire-derived SOURCE item
// against its nostr PROJECTION. Diffs is empty when the item is item-for-item
// identical on the compared fields.
type ItemParity struct {
	ItemID string   `json:"item_id"`
	Diffs  []string `json:"diffs,omitempty"`
}

// Match reports whether the item projected identically (no diffs).
func (p ItemParity) Match() bool { return len(p.Diffs) == 0 }

// CompareItem asserts item-for-item parity between a campfire-derived source item
// and its nostr projection, on exactly the fields ready-d65 DONE#3 enumerates:
// status, priority, type, deps, gate, history length + close-reasons, and
// provenance (the audit actors). History is compared ORDER-INDEPENDENTLY (a
// multiset of (to_status, note, actor)) so an accepted same-second reordering
// (ready-194) is not a false diff, while a lost/added/altered entry always is.
// A nil projection (item missing from the nostr side) is the strongest possible
// diff — a LOST item — and is reported as such.
func CompareItem(source, projected *state.Item) ItemParity {
	res := ItemParity{ItemID: source.ID}
	if projected == nil {
		res.Diffs = append(res.Diffs, "LOST: item absent from nostr projection")
		return res
	}
	add := func(field, a, b string) {
		if a != b {
			res.Diffs = append(res.Diffs, fmt.Sprintf("%s: campfire=[%s] nostr=[%s]", field, a, b))
		}
	}
	add("status", source.Status, projected.Status)
	add("priority", source.Priority, projected.Priority)
	add("type", source.Type, projected.Type)
	add("deps", joinSorted(source.BlockedBy), joinSorted(projected.BlockedBy))
	add("gate", boolStr(hasGate(source)), boolStr(hasGate(projected)))
	add("history_len", fmt.Sprint(len(source.History)), fmt.Sprint(len(projected.History)))
	add("close_reasons", multisetNotes(source.History), multisetNotes(projected.History))
	add("provenance", multisetActors(source.History), multisetActors(projected.History))
	return res
}

// ParityReport summarises a whole-item-set comparison.
type ParityReport struct {
	SourceCount    int          `json:"source_count"`
	ProjectedCount int          `json:"projected_count"`
	Matched        int          `json:"matched"`
	Mismatched     int          `json:"mismatched"`
	Items          []ItemParity `json:"items"`
}

// AllMatch reports whether every source item projected identically AND the item
// counts agree (no item lost, none silently added).
func (r ParityReport) AllMatch() bool {
	return r.Mismatched == 0 && r.SourceCount == r.ProjectedCount
}

// CompareItemSets compares a campfire-derived source set (keyed by id) against the
// nostr projection (keyed by id) item-for-item. Every source item must appear in
// the projection with identical compared fields; a projection-only id is also a
// mismatch (a silently-added item). Deterministic ordering by item id.
func CompareItemSets(source, projected map[string]*state.Item) ParityReport {
	rep := ParityReport{SourceCount: len(source), ProjectedCount: len(projected)}
	ids := make([]string, 0, len(source))
	for id := range source {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	seen := make(map[string]bool, len(source))
	for _, id := range ids {
		seen[id] = true
		p := CompareItem(source[id], projected[id])
		if p.Match() {
			rep.Matched++
		} else {
			rep.Mismatched++
		}
		rep.Items = append(rep.Items, p)
	}
	// Projection-only items (present on nostr but not in the source) — a silent add.
	extra := make([]string, 0)
	for id := range projected {
		if !seen[id] {
			extra = append(extra, id)
		}
	}
	sort.Strings(extra)
	for _, id := range extra {
		rep.Mismatched++
		rep.Items = append(rep.Items, ItemParity{ItemID: id, Diffs: []string{"EXTRA: item present in nostr projection but absent from campfire source"}})
	}
	return rep
}

func hasGate(i *state.Item) bool {
	return i.Gate != "" || i.WaitingType == "gate" || i.GateMsgID != ""
}

func boolStr(b bool) string {
	if b {
		return "true"
	}
	return "false"
}

func joinSorted(vals []string) string {
	cp := append([]string(nil), vals...)
	sort.Strings(cp)
	out := ""
	for i, v := range cp {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}

// multisetNotes returns the sorted multiset of non-empty history notes joined with
// a record separator, so it compares order-independently (same-second robust).
func multisetNotes(hist []state.HistoryEntry) string {
	vals := make([]string, 0, len(hist))
	for _, h := range hist {
		vals = append(vals, h.ToStatus+"\x1f"+h.Note)
	}
	sort.Strings(vals)
	out := ""
	for i, v := range vals {
		if i > 0 {
			out += "\x1e"
		}
		out += v
	}
	return out
}

// multisetActors returns the sorted multiset of history actors (ChangedBy),
// comparing provenance order-independently.
func multisetActors(hist []state.HistoryEntry) string {
	vals := make([]string, 0, len(hist))
	for _, h := range hist {
		vals = append(vals, h.ChangedBy)
	}
	sort.Strings(vals)
	out := ""
	for i, v := range vals {
		if i > 0 {
			out += ","
		}
		out += v
	}
	return out
}
