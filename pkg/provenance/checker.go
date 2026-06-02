// Package provenance implements the ProvenanceChecker interface for the
// work convention. It reads work:role-grant messages from the campfire
// store to determine each operator's level.
//
// Bootstrap rules (design doc §3.2):
//   - Campfire creator (first owner in membership record) is implicitly
//     "maintainer" = level 2.
//   - All others default to "contributor" = level 1 until a work:role-grant
//     message says otherwise.
//   - The latest work:role-grant for a pubkey is the active record.
//   - role="revoked" maps to level 0.
//
// Level mapping:
//
//	"maintainer" → 2
//	"contributor" → 1
//	"revoked"     → 0
//	(no grant)    → 1 (default contributor)
package provenance

import (
	"encoding/json"

	"github.com/campfire-net/campfire/cf-protocol/store"
)

// roleGrantPayload mirrors the fields in a work:role-grant message payload.
type roleGrantPayload struct {
	Pubkey string `json:"pubkey"`
	Role   string `json:"role"`
}

// roleToLevel maps a role string to a numeric operator level.
func roleToLevel(role string) int {
	switch role {
	case "maintainer":
		return 2
	case "contributor":
		return 1
	case "revoked":
		return 0
	default:
		// Unknown roles default to contributor (level 1).
		return 1
	}
}

// StoreChecker is a ProvenanceChecker backed by a campfire store. It reads
// work:role-grant messages from the store on construction and caches the
// derived levels in memory. Construct via NewStoreChecker.
type StoreChecker struct {
	// levels maps pubkey (hex) → operator level.
	levels map[string]int
	// creatorKey is the campfire creator's pubkey. The creator is always
	// level 2 unless explicitly revoked by a work:role-grant message.
	creatorKey string
}

// NewStoreChecker constructs a StoreChecker by reading all work:role-grant
// messages for campfireID from s.  creatorKey is the pubkey of the campfire
// creator (from store.Membership.CreatorPubkey); it receives an implicit
// maintainer grant unless a later work:role-grant overrides it.
//
// If the store cannot be read, an error is returned.
func NewStoreChecker(s store.Store, campfireID, creatorKey string) (*StoreChecker, error) {
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{
		Tags: []string{"work:role-grant"},
	})
	if err != nil {
		return nil, err
	}

	levels := make(map[string]int)
	for _, m := range msgs {
		var p roleGrantPayload
		if err := json.Unmarshal(m.Payload, &p); err != nil {
			continue
		}
		if p.Pubkey == "" || p.Role == "" {
			continue
		}
		// Latest message for each pubkey wins (msgs are in timestamp order).
		levels[p.Pubkey] = roleToLevel(p.Role)
	}

	return &StoreChecker{
		levels:     levels,
		creatorKey: creatorKey,
	}, nil
}

// Level returns the operator provenance level (0–2) for the given public key.
//
// Resolution order:
//  1. If a work:role-grant message exists for key, return its mapped level.
//  2. If key == creatorKey (bootstrap maintainer), return 2.
//  3. Otherwise return 1 (default contributor).
func (c *StoreChecker) Level(key string) int {
	if level, ok := c.levels[key]; ok {
		return level
	}
	if c.creatorKey != "" && key == c.creatorKey {
		return 2
	}
	return 1
}
