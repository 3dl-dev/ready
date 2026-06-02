// Package sync — inbound sync (pull) for the ready work management convention.
//
// Inbound sync replays campfire messages missed while offline into the local
// JSONL mutation log. The pull operation:
//
//  1. Reads campfire messages with work:* tags since last pull timestamp.
//  2. Deduplicates by message ID against existing JSONL records.
//  3. Appends new records to .ready/mutations.jsonl in campfire arrival order.
//  4. Updates last_pull_at in .ready/sync-state.json.
//  5. Warns if the offline gap exceeds the campfire's max-ttl (potential message loss).
//
// Conflict resolution: campfire message arrival order is canonical (last-writer-wins).
// No CRDT — work management is low-frequency, eventual consistency is sufficient.

package sync

import (
	"fmt"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"

	"github.com/campfire-net/ready/pkg/jsonl"
)

const (
	// DefaultMaxTTL is the assumed campfire max-ttl when none is configured.
	// 30 days is a conservative lower bound for most persistent campfires.
	DefaultMaxTTL = 30 * 24 * time.Hour
)

// PullResult summarises the outcome of a Pull call.
type PullResult struct {
	// Pulled is the number of new mutation records appended to JSONL.
	Pulled int

	// Skipped is the number of campfire messages skipped due to deduplication.
	Skipped int

	// GapWarning is non-empty when the offline duration exceeds the campfire
	// max-ttl, indicating that some messages may have been permanently lost.
	GapWarning string

	// LastPullAt is the timestamp of the pull (nanoseconds since epoch).
	LastPullAt int64
}

// MessageLister is the subset of store.Store needed by Pull.
// Defined as an interface so tests can inject a fake without a real SQLite store.
type MessageLister interface {
	ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error)
}

// Pull reads campfire messages with work:* tags since last pull, deduplicates
// against existing JSONL records, and appends new records in arrival order.
//
//   - s is the campfire message store.
//   - campfireID is the project campfire to read from.
//   - jsonlPath is the absolute path to .ready/mutations.jsonl.
//   - projectDir is the project root (for reading/writing sync state).
//   - maxTTL is the campfire retention window. Pass 0 to use DefaultMaxTTL.
//     Used only for gap detection — Pull does not enforce it.
func Pull(s MessageLister, campfireID, jsonlPath, projectDir string, maxTTL time.Duration) (PullResult, error) {
	if maxTTL <= 0 {
		maxTTL = DefaultMaxTTL
	}

	// Load sync state to get last pull timestamp.
	state, err := LoadState(projectDir)
	if err != nil {
		return PullResult{}, fmt.Errorf("sync: pull: loading state: %w", err)
	}

	now := time.Now()
	nowNano := now.UnixNano()
	var result PullResult
	result.LastPullAt = nowNano

	// Gap detection: if we've pulled before and the gap exceeds max-ttl, warn.
	if state.LastPullAt > 0 {
		lastPull := time.Unix(0, state.LastPullAt)
		offlineDuration := now.Sub(lastPull)
		if offlineDuration > maxTTL {
			result.GapWarning = fmt.Sprintf(
				"warning: offline for %s exceeds campfire max-ttl (%s) — some messages may be permanently lost",
				offlineDuration.Round(time.Second), maxTTL.Round(time.Second),
			)
		}
	}

	// Read campfire messages with work:* tags since last pull.
	// Use AfterReceivedAt to avoid clock-skew issues (same as poll cursor logic).
	afterTS := state.LastPullAt
	filter := store.MessageFilter{
		Tags: workTags(),
	}
	if afterTS > 0 {
		filter.AfterReceivedAt = afterTS
	}

	msgs, err := s.ListMessages(campfireID, afterTS, filter)
	if err != nil {
		return PullResult{}, fmt.Errorf("sync: pull: listing campfire messages: %w", err)
	}

	if len(msgs) == 0 {
		// Nothing to pull — update last pull timestamp and return.
		if updateErr := updatePullState(projectDir, state, nowNano); updateErr != nil {
			return result, fmt.Errorf("sync: pull: saving state: %w", updateErr)
		}
		return result, nil
	}

	// Build dedup set from existing JSONL records.
	known, err := buildKnownIDs(jsonlPath)
	if err != nil {
		return PullResult{}, fmt.Errorf("sync: pull: reading existing JSONL: %w", err)
	}

	// Append new records in campfire arrival order (msgs is already ordered by
	// ListMessages — timestamp ascending, which mirrors arrival order for this store).
	w := jsonl.NewWriter(jsonlPath)
	for _, msg := range msgs {
		if known[msg.ID] {
			result.Skipped++
			continue
		}
		// Only replay work:* messages.
		if !hasWorkTag(msg.Tags) {
			continue
		}
		rec := jsonl.FromMessageRecord(msg)
		if appendErr := w.Append(rec); appendErr != nil {
			return result, fmt.Errorf("sync: pull: appending record %s: %w", msg.ID, appendErr)
		}
		known[msg.ID] = true
		result.Pulled++
	}

	// Update sync state.
	if updateErr := updatePullState(projectDir, state, nowNano); updateErr != nil {
		// Non-fatal: records were written, just the cursor update failed.
		fmt.Printf("warning: sync: pull: could not update sync state: %v\n", updateErr)
	}

	return result, nil
}

// buildKnownIDs reads the JSONL file and returns a set of message IDs already present.
// Returns an empty map if the file does not exist.
func buildKnownIDs(jsonlPath string) (map[string]bool, error) {
	r := jsonl.NewReader(jsonlPath)
	recs, err := r.ReadAll()
	if err != nil {
		return nil, err
	}
	known := make(map[string]bool, len(recs))
	for _, rec := range recs {
		known[rec.MsgID] = true
	}
	return known, nil
}

// updatePullState updates the LastPullAt field in the sync state.
func updatePullState(projectDir string, s *State, pullAt int64) error {
	s.LastPullAt = pullAt
	return SaveState(projectDir, s)
}

// workTags returns the tag prefixes that indicate a convention message.
// ListMessages uses OR semantics — a message matches if it has ANY of these tags.
// We pass a single prefix "work" but ListMessages matches exact tags, so we
// enumerate the known work:* operation tags for the filter.
func workTags() []string {
	return []string{
		"work:create",
		"work:claim",
		"work:status",
		"work:block",
		"work:unblock",
		"work:gate",
		"work:gate-resolve",
		"work:delegate",
		"work:close",
		"work:update",
		"work:engage",
		"work:playbook-create",
	}
}

// hasWorkTag returns true if any tag in the list starts with the work: prefix.
func hasWorkTag(tags []string) bool {
	for _, t := range tags {
		if strings.HasPrefix(t, jsonl.WorkTagPrefix) && len(t) > len(jsonl.WorkTagPrefix) {
			return true
		}
	}
	return false
}
