// Package resolve provides item ID resolution for the rd CLI.
// Item IDs are project-prefixed strings (e.g. "ready-a1b"). Resolution
// replays the local signed-event log (JSONL / nostr projection) and matches
// against derived item state.
package resolve

import (
	"fmt"
	"strings"

	"github.com/3dl-dev/ready/pkg/state"
)

// ErrNotFound is returned when an item ID cannot be resolved.
type ErrNotFound struct {
	ID string
}

func (e ErrNotFound) Error() string {
	return fmt.Sprintf("item %q not found", e.ID)
}

// ErrAmbiguous is returned when a prefix matches multiple items.
type ErrAmbiguous struct {
	Prefix   string
	Matches  []string
}

func (e ErrAmbiguous) Error() string {
	return fmt.Sprintf("prefix %q is ambiguous: matches %s", e.Prefix, strings.Join(e.Matches, ", "))
}

// ByIDFromJSONL resolves an item by its exact ID or a unique prefix from a
// local mutations.jsonl file. campfireID is used to label items that do not
// carry one in their record (pass empty string to infer from file).
func ByIDFromJSONL(path, campfireID, itemID string) (*state.Item, error) {
	items, err := state.DeriveFromJSONLWithCampfire(path, campfireID)
	if err != nil {
		return nil, fmt.Errorf("deriving state from JSONL: %w", err)
	}

	// Exact match first.
	if item, ok := items[itemID]; ok {
		return item, nil
	}

	// Prefix match.
	var matches []*state.Item
	for id, item := range items {
		if strings.HasPrefix(id, itemID) {
			matches = append(matches, item)
		}
	}
	switch len(matches) {
	case 0:
		return nil, ErrNotFound{ID: itemID}
	case 1:
		return matches[0], nil
	default:
		ids := make([]string, len(matches))
		for i, m := range matches {
			ids[i] = m.ID
		}
		return nil, ErrAmbiguous{Prefix: itemID, Matches: ids}
	}
}

// ByIDFromJSONLExact resolves an item by its exact ID from a local
// mutations.jsonl file — no prefix expansion.
// Use this for security-sensitive operations (e.g. admit) where a prefix
// collision could cause the wrong item to be selected.
func ByIDFromJSONLExact(path, campfireID, itemID string) (*state.Item, error) {
	items, err := state.DeriveFromJSONLWithCampfire(path, campfireID)
	if err != nil {
		return nil, fmt.Errorf("deriving state from JSONL: %w", err)
	}
	if item, ok := items[itemID]; ok {
		return item, nil
	}
	return nil, ErrNotFound{ID: itemID}
}

// AllItemsFromJSONL returns all items derived from a local mutations.jsonl file.
// campfireID is used as a label for items that do not carry one in their record
// (pass empty string to infer from file).
func AllItemsFromJSONL(path, campfireID string) ([]*state.Item, error) {
	items, err := state.DeriveFromJSONLWithCampfire(path, campfireID)
	if err != nil {
		return nil, fmt.Errorf("deriving state from JSONL: %w", err)
	}
	all := make([]*state.Item, 0, len(items))
	for _, item := range items {
		all = append(all, item)
	}
	return all, nil
}
