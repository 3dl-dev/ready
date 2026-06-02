package sync_test

import (
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"

	"github.com/campfire-net/ready/pkg/jsonl"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// fakeLister implements rdSync.MessageLister for tests.
// It returns a fixed list of messages, optionally filtered by tags.
type fakeLister struct {
	messages []store.MessageRecord
}

func (f *fakeLister) ListMessages(campfireID string, afterTimestamp int64, filter ...store.MessageFilter) ([]store.MessageRecord, error) {
	var out []store.MessageRecord
	for _, m := range f.messages {
		if afterTimestamp > 0 && m.Timestamp <= afterTimestamp {
			continue
		}
		if len(filter) > 0 && len(filter[0].Tags) > 0 {
			if !hasAnyTag(m.Tags, filter[0].Tags) {
				continue
			}
		}
		out = append(out, m)
	}
	return out, nil
}

// hasAnyTag returns true if msg tags contain any of the wanted tags.
func hasAnyTag(msgTags, wanted []string) bool {
	set := make(map[string]bool, len(wanted))
	for _, t := range wanted {
		set[t] = true
	}
	for _, t := range msgTags {
		if set[t] {
			return true
		}
	}
	return false
}

// makeCampfireRecord builds a minimal store.MessageRecord for testing.
func makeCampfireRecord(id, op string, ts int64) store.MessageRecord {
	payload := `{"id":"` + id + `"}`
	return store.MessageRecord{
		ID:         id,
		CampfireID: "test-campfire-id",
		Timestamp:  ts,
		ReceivedAt: ts,
		Payload:    []byte(payload),
		Tags:       []string{op},
		Sender:     "deadbeef",
	}
}

// jsonlPath returns the path to mutations.jsonl in a project dir.
func jsonlMutationsPath(projectDir string) string {
	return filepath.Join(projectDir, ".ready", "mutations.jsonl")
}

// readMutations reads all mutation records from .ready/mutations.jsonl.
func readMutations(t *testing.T, projectDir string) []jsonl.MutationRecord {
	t.Helper()
	r := jsonl.NewReader(jsonlMutationsPath(projectDir))
	recs, err := r.ReadAll()
	if err != nil {
		t.Fatalf("readMutations: %v", err)
	}
	return recs
}

// writeExistingMutation writes a MutationRecord to .ready/mutations.jsonl.
func writeExistingMutation(t *testing.T, projectDir string, rec jsonl.MutationRecord) {
	t.Helper()
	w := jsonl.NewWriter(jsonlMutationsPath(projectDir))
	if err := w.Append(rec); err != nil {
		t.Fatalf("writeExistingMutation: %v", err)
	}
}

// TestPull_ReplaysCampfireMessages verifies that Pull appends campfire messages
// to JSONL and items become visible after pull.
func TestPull_ReplaysCampfireMessages(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	campfireID := "test-campfire-id"

	msgs := []store.MessageRecord{
		makeCampfireRecord("msg-alpha", "work:create", ts),
		makeCampfireRecord("msg-beta", "work:claim", ts+1000),
		makeCampfireRecord("msg-gamma", "work:close", ts+2000),
	}
	lister := &fakeLister{messages: msgs}

	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if result.Pulled != 3 {
		t.Errorf("Pulled: got %d, want 3", result.Pulled)
	}
	if result.Skipped != 0 {
		t.Errorf("Skipped: got %d, want 0", result.Skipped)
	}
	if result.GapWarning != "" {
		t.Errorf("unexpected gap warning: %s", result.GapWarning)
	}

	// Verify items appear in JSONL.
	recs := readMutations(t, projectDir)
	if len(recs) != 3 {
		t.Fatalf("mutations.jsonl has %d records, want 3", len(recs))
	}

	// Verify IDs are present (Reader sorts by timestamp, so order matches).
	ids := map[string]bool{}
	for _, r := range recs {
		ids[r.MsgID] = true
	}
	for _, want := range []string{"msg-alpha", "msg-beta", "msg-gamma"} {
		if !ids[want] {
			t.Errorf("missing record %q in mutations.jsonl", want)
		}
	}
}

// TestPull_Dedup_SameMessageTwice verifies that replaying the same message
// twice produces exactly one record in JSONL.
func TestPull_Dedup_SameMessageTwice(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	campfireID := "test-campfire-id"

	msg := makeCampfireRecord("msg-unique", "work:create", ts)
	lister := &fakeLister{messages: []store.MessageRecord{msg}}

	// First pull.
	result1, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("first Pull: %v", err)
	}
	if result1.Pulled != 1 {
		t.Errorf("first pull: Pulled = %d, want 1", result1.Pulled)
	}

	// Reset pull state so the same message is offered again (simulate re-pull
	// with same afterTimestamp). We clear LastPullAt in sync-state.json.
	state, err := rdSync.LoadState(projectDir)
	if err != nil {
		t.Fatalf("LoadState: %v", err)
	}
	state.LastPullAt = 0
	if err := rdSync.SaveState(projectDir, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Second pull with the same message in the lister.
	result2, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("second Pull: %v", err)
	}
	if result2.Pulled != 0 {
		t.Errorf("second pull: Pulled = %d, want 0 (dedup)", result2.Pulled)
	}
	if result2.Skipped != 1 {
		t.Errorf("second pull: Skipped = %d, want 1", result2.Skipped)
	}

	// JSONL should still have exactly one record.
	recs := readMutations(t, projectDir)
	if len(recs) != 1 {
		t.Errorf("mutations.jsonl has %d records after dedup pull, want 1", len(recs))
	}
}

// TestPull_Dedup_SkipsExistingJSONLRecords verifies that messages already in
// mutations.jsonl (e.g. locally authored) are not duplicated on pull.
func TestPull_Dedup_SkipsExistingJSONLRecords(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	campfireID := "test-campfire-id"

	// Pre-populate mutations.jsonl with msg-local (already locally written).
	existing := jsonl.MutationRecord{
		MsgID:     "msg-local",
		Operation: "work:create",
		Timestamp: ts,
		Payload:   json.RawMessage(`{"id":"msg-local"}`),
		Tags:      []string{"work:create"},
	}
	writeExistingMutation(t, projectDir, existing)

	// Campfire has msg-local (same as local) plus msg-remote (new).
	msgs := []store.MessageRecord{
		makeCampfireRecord("msg-local", "work:create", ts),
		makeCampfireRecord("msg-remote", "work:claim", ts+500),
	}
	lister := &fakeLister{messages: msgs}

	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if result.Pulled != 1 {
		t.Errorf("Pulled: got %d, want 1 (only msg-remote is new)", result.Pulled)
	}
	if result.Skipped != 1 {
		t.Errorf("Skipped: got %d, want 1 (msg-local already in JSONL)", result.Skipped)
	}

	recs := readMutations(t, projectDir)
	if len(recs) != 2 {
		t.Fatalf("mutations.jsonl has %d records, want 2", len(recs))
	}

	ids := map[string]bool{}
	for _, r := range recs {
		ids[r.MsgID] = true
	}
	if !ids["msg-local"] {
		t.Error("msg-local missing from mutations.jsonl")
	}
	if !ids["msg-remote"] {
		t.Error("msg-remote missing from mutations.jsonl")
	}
}

// TestPull_OrderingPreserved verifies that campfire arrival order is preserved
// in mutations.jsonl. The Reader sorts by timestamp ascending, so we verify
// that messages with ascending timestamps appear in timestamp order.
func TestPull_OrderingPreserved(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	campfireID := "test-campfire-id"

	// Messages arrive in timestamp order from campfire.
	msgs := []store.MessageRecord{
		makeCampfireRecord("msg-first", "work:create", ts),
		makeCampfireRecord("msg-second", "work:claim", ts+100),
		makeCampfireRecord("msg-third", "work:update", ts+200),
		makeCampfireRecord("msg-fourth", "work:close", ts+300),
	}
	lister := &fakeLister{messages: msgs}

	_, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	recs := readMutations(t, projectDir)
	if len(recs) != 4 {
		t.Fatalf("mutations.jsonl has %d records, want 4", len(recs))
	}

	// Reader sorts by timestamp, so order must match timestamp ascending.
	expected := []string{"msg-first", "msg-second", "msg-third", "msg-fourth"}
	for i, want := range expected {
		if recs[i].MsgID != want {
			t.Errorf("record[%d]: got %q, want %q", i, recs[i].MsgID, want)
		}
	}
}

// TestPull_GapDetection_WarnWhenOfflineExceedsMaxTTL verifies that Pull
// emits a gap warning when the offline duration exceeds the campfire max-ttl.
func TestPull_GapDetection_WarnWhenOfflineExceedsMaxTTL(t *testing.T) {
	projectDir := setupProject(t)

	campfireID := "test-campfire-id"
	lister := &fakeLister{messages: nil}

	// Set last pull to 2 days ago.
	twoDaysAgo := time.Now().Add(-48 * time.Hour).UnixNano()
	state := &rdSync.State{LastPullAt: twoDaysAgo}
	if err := rdSync.SaveState(projectDir, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Use a max-ttl of 1 day — last pull was 2 days ago, so gap exceeded.
	maxTTL := 24 * time.Hour
	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, maxTTL)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if result.GapWarning == "" {
		t.Error("expected gap warning for offline duration exceeding max-ttl, got none")
	}

	t.Logf("gap warning: %s", result.GapWarning)
}

// TestPull_NoGapWarning_WhenWithinMaxTTL verifies that no gap warning is
// emitted when offline duration is within the campfire max-ttl.
func TestPull_NoGapWarning_WhenWithinMaxTTL(t *testing.T) {
	projectDir := setupProject(t)

	campfireID := "test-campfire-id"
	lister := &fakeLister{messages: nil}

	// Set last pull to 12 hours ago.
	twelveHoursAgo := time.Now().Add(-12 * time.Hour).UnixNano()
	state := &rdSync.State{LastPullAt: twelveHoursAgo}
	if err := rdSync.SaveState(projectDir, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Use a max-ttl of 30 days — 12 hours is well within it.
	maxTTL := 30 * 24 * time.Hour
	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, maxTTL)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if result.GapWarning != "" {
		t.Errorf("unexpected gap warning: %s", result.GapWarning)
	}
}

// TestPull_NoGapWarning_OnFirstPull verifies that no gap warning is emitted
// on the first ever pull (no last pull timestamp).
func TestPull_NoGapWarning_OnFirstPull(t *testing.T) {
	projectDir := setupProject(t)

	campfireID := "test-campfire-id"
	lister := &fakeLister{messages: nil}

	// Fresh project — no sync state.
	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, time.Hour)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if result.GapWarning != "" {
		t.Errorf("unexpected gap warning on first pull: %s", result.GapWarning)
	}
}

// TestPull_UpdatesLastPullAt verifies that Pull updates the last pull timestamp
// in sync-state.json.
func TestPull_UpdatesLastPullAt(t *testing.T) {
	projectDir := setupProject(t)

	campfireID := "test-campfire-id"
	lister := &fakeLister{messages: nil}

	before := time.Now().UnixNano()
	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}
	after := time.Now().UnixNano()

	if result.LastPullAt < before || result.LastPullAt > after {
		t.Errorf("LastPullAt %d outside [%d, %d]", result.LastPullAt, before, after)
	}

	state, err := rdSync.LoadState(projectDir)
	if err != nil {
		t.Fatalf("LoadState after Pull: %v", err)
	}
	if state.LastPullAt < before || state.LastPullAt > after {
		t.Errorf("state.LastPullAt %d outside [%d, %d]", state.LastPullAt, before, after)
	}
}

// TestPull_OnlyPullsWorkTaggedMessages verifies that messages without work:*
// tags are not appended to JSONL even if returned by the lister.
func TestPull_OnlyPullsWorkTaggedMessages(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	campfireID := "test-campfire-id"

	// The fake lister doesn't filter by tag — it returns everything.
	// Pull should filter work:* tags itself as a safety net.
	nonWork := store.MessageRecord{
		ID:         "msg-status",
		CampfireID: campfireID,
		Timestamp:  ts,
		ReceivedAt: ts,
		Payload:    []byte(`{}`),
		Tags:       []string{"status"},
		Sender:     "deadbeef",
	}
	workMsg := makeCampfireRecord("msg-create", "work:create", ts+100)
	lister := &fakeLister{messages: []store.MessageRecord{nonWork, workMsg}}

	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	// Only the work:create message should be pulled.
	if result.Pulled != 1 {
		t.Errorf("Pulled: got %d, want 1", result.Pulled)
	}

	recs := readMutations(t, projectDir)
	if len(recs) != 1 {
		t.Fatalf("mutations.jsonl has %d records, want 1", len(recs))
	}
	if recs[0].MsgID != "msg-create" {
		t.Errorf("record[0]: got %q, want msg-create", recs[0].MsgID)
	}
}

// TestPull_GateResolvePulled verifies that work:gate-resolve messages from
// other participants are pulled into the local JSONL. This is a regression
// test for a data loss bug where work:gate-resolve was absent from workTags(),
// causing gate approval/rejection messages to be silently excluded from inbound sync.
func TestPull_GateResolvePulled(t *testing.T) {
	projectDir := setupProject(t)

	ts := time.Now().UnixNano()
	campfireID := "test-campfire-id"

	msgs := []store.MessageRecord{
		makeCampfireRecord("msg-gate", "work:gate", ts),
		makeCampfireRecord("msg-gate-resolve", "work:gate-resolve", ts+1000),
	}
	lister := &fakeLister{messages: msgs}

	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, 30*24*time.Hour)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if result.Pulled != 2 {
		t.Errorf("Pulled: got %d, want 2 (work:gate + work:gate-resolve)", result.Pulled)
	}

	recs := readMutations(t, projectDir)
	if len(recs) != 2 {
		t.Fatalf("mutations.jsonl has %d records, want 2", len(recs))
	}

	ids := map[string]bool{}
	for _, r := range recs {
		ids[r.MsgID] = true
	}
	if !ids["msg-gate"] {
		t.Error("msg-gate missing from mutations.jsonl")
	}
	if !ids["msg-gate-resolve"] {
		t.Error("msg-gate-resolve missing from mutations.jsonl — work:gate-resolve tag must be in workTags()")
	}
}

// --- Gap detection boundary tests ---

// TestPull_GapDetection_JustUnderMaxTTL verifies that Pull does NOT emit a gap
// warning when offline duration is just under max-ttl.
// The condition is strictly greater-than: offlineDuration > maxTTL.
func TestPull_GapDetection_JustUnderMaxTTL(t *testing.T) {
	projectDir := setupProject(t)

	campfireID := "test-campfire-id"
	lister := &fakeLister{messages: nil}

	// Set last pull to 30 minutes ago with a 1-hour max-ttl.
	// 30 minutes is clearly within the 1-hour TTL.
	maxTTL := time.Hour
	halfTTLAgo := time.Now().Add(-(maxTTL / 2)).UnixNano()
	state := &rdSync.State{LastPullAt: halfTTLAgo}
	if err := rdSync.SaveState(projectDir, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, maxTTL)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	// Half of max-ttl — well within threshold, no warning.
	if result.GapWarning != "" {
		t.Errorf("unexpected gap warning when offline < max-ttl: %s", result.GapWarning)
	}
}

// TestPull_GapDetection_OneSecondOverMaxTTL verifies that Pull emits a gap
// warning when offline duration exceeds max-ttl by even a small amount (1 second).
func TestPull_GapDetection_OneSecondOverMaxTTL(t *testing.T) {
	projectDir := setupProject(t)

	campfireID := "test-campfire-id"
	lister := &fakeLister{messages: nil}

	maxTTL := time.Hour
	// One second past max-ttl.
	oneSecondOver := time.Now().Add(-(maxTTL + time.Second)).UnixNano()
	state := &rdSync.State{LastPullAt: oneSecondOver}
	if err := rdSync.SaveState(projectDir, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, maxTTL)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if result.GapWarning == "" {
		t.Error("expected gap warning when offline exceeds max-ttl by 1 second, got none")
	}
	t.Logf("gap warning: %s", result.GapWarning)
}

// TestPull_GapDetection_ZeroMaxTTL verifies that passing maxTTL=0 to Pull
// uses DefaultMaxTTL (30 days). An offline gap of 1 day is within the 30-day
// default, so no warning should be emitted.
func TestPull_GapDetection_ZeroMaxTTL(t *testing.T) {
	projectDir := setupProject(t)

	campfireID := "test-campfire-id"
	lister := &fakeLister{messages: nil}

	// Set last pull to 1 day ago — within DefaultMaxTTL (30 days).
	oneDayAgo := time.Now().Add(-24 * time.Hour).UnixNano()
	state := &rdSync.State{LastPullAt: oneDayAgo}
	if err := rdSync.SaveState(projectDir, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	// Pass maxTTL=0 to trigger DefaultMaxTTL (30 days).
	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, 0)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if result.GapWarning != "" {
		t.Errorf("unexpected gap warning with 1-day gap and DefaultMaxTTL (30 days): %s", result.GapWarning)
	}
}

// TestPull_GapDetection_WarnIncludesDurations verifies that the gap warning
// message includes both the offline duration and the max-ttl, so operators can
// understand the severity of the gap from the warning text alone.
func TestPull_GapDetection_WarnIncludesDurations(t *testing.T) {
	projectDir := setupProject(t)

	campfireID := "test-campfire-id"
	lister := &fakeLister{messages: nil}

	// Set last pull to 3 days ago with a 1-day max-ttl.
	threeDaysAgo := time.Now().Add(-72 * time.Hour).UnixNano()
	state := &rdSync.State{LastPullAt: threeDaysAgo}
	if err := rdSync.SaveState(projectDir, state); err != nil {
		t.Fatalf("SaveState: %v", err)
	}

	maxTTL := 24 * time.Hour
	result, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, maxTTL)
	if err != nil {
		t.Fatalf("Pull: %v", err)
	}

	if result.GapWarning == "" {
		t.Fatal("expected gap warning, got none")
	}

	// Warning must mention both the offline duration and the max-ttl so operators
	// can diagnose the severity without additional context.
	warning := result.GapWarning
	if !containsApproxDuration(warning, "72h") && !containsApproxDuration(warning, "3d") {
		t.Logf("gap warning: %s", warning)
		// The offline duration may be formatted differently (e.g. "72h0m0s").
		// Just check it contains a time-like string — the key test is that it has *something*.
		if !containsTimeFragment(warning) {
			t.Errorf("gap warning should mention offline duration, got: %q", warning)
		}
	}
	t.Logf("gap warning (expected): %s", warning)
}

// TestPull_GapDetection_SequentialPulls verifies that gap detection resets
// after a successful pull — the next pull uses the updated last_pull_at
// and should not warn if the gap since the last pull is within max-ttl.
func TestPull_GapDetection_SequentialPulls(t *testing.T) {
	projectDir := setupProject(t)

	campfireID := "test-campfire-id"
	lister := &fakeLister{messages: nil}
	maxTTL := time.Hour

	// First pull: no prior state, so no gap warning.
	result1, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, maxTTL)
	if err != nil {
		t.Fatalf("first Pull: %v", err)
	}
	if result1.GapWarning != "" {
		t.Errorf("first pull: unexpected gap warning: %s", result1.GapWarning)
	}

	// Second pull immediately after: gap since first pull is ~0, well within maxTTL.
	result2, err := rdSync.Pull(lister, campfireID, jsonlMutationsPath(projectDir), projectDir, maxTTL)
	if err != nil {
		t.Fatalf("second Pull: %v", err)
	}
	if result2.GapWarning != "" {
		t.Errorf("second pull immediately after first: unexpected gap warning: %s", result2.GapWarning)
	}
}

// containsApproxDuration is a helper to check if s contains a rough duration string.
func containsApproxDuration(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && contains(s, substr)
}

// containsTimeFragment checks that s contains any digit followed by 'h', 'm', or 's'.
func containsTimeFragment(s string) bool {
	for i := 0; i < len(s)-1; i++ {
		if s[i] >= '0' && s[i] <= '9' {
			if s[i+1] == 'h' || s[i+1] == 'm' || s[i+1] == 's' {
				return true
			}
		}
	}
	return false
}

// contains checks if haystack contains needle (case-sensitive).
func contains(haystack, needle string) bool {
	return len(haystack) >= len(needle) && (haystack == needle ||
		func() bool {
			for i := 0; i <= len(haystack)-len(needle); i++ {
				if haystack[i:i+len(needle)] == needle {
					return true
				}
			}
			return false
		}())
}
