package state_test

// label_enforcement_test.go — derive-time label enforcement tests.
//
// Done conditions from i2.md:
//   1. Happy path: create with labels [bug, security] via harness → Item.Labels==[bug security]
//   2. BYPASS TEST: raw message into store with unregistered+pattern-valid label and
//      pattern-invalid label → neither materializes; warnings recorded; other fields intact.
//   3. 9 labels rejected at derive time (max 8 per composite pattern = harness will let it
//      through write side, but derive side will reject each against the registry and pattern).
//   5. Skew test: old createPayload struct (no Labels) parsing labels-bearing payload
//      does not error.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/storetest"
)

// ---------------------------------------------------------------------------
// Happy path: seed labels materialise on create.
// ---------------------------------------------------------------------------

// TestLabels_HappyPath_SeedLabels verifies that creating an item with labels=bug,security
// via the storetest harness (which mimics the convention executor path) produces
// an Item with Labels=[bug security] in derived state.
func TestLabels_HappyPath_SeedLabels(t *testing.T) {
	h := storetest.New(t)
	// bug and security are seed labels — always in registry.
	h.Create("item-label-happy", "Bug fix", "p2",
		storetest.WithLabels("bug", "security"))

	result := h.DeriveAll()
	item, ok := result.Items()["item-label-happy"]
	if !ok {
		t.Fatal("item 'item-label-happy' not found in derived state")
	}

	labelSet := make(map[string]bool)
	for _, l := range item.Labels {
		labelSet[l] = true
	}
	if !labelSet["bug"] {
		t.Errorf("Labels=%v missing 'bug'", item.Labels)
	}
	if !labelSet["security"] {
		t.Errorf("Labels=%v missing 'security'", item.Labels)
	}
	if len(item.Labels) != 2 {
		t.Errorf("Labels has %d items, want 2: %v", len(item.Labels), item.Labels)
	}
	if len(item.LabelWarnings) != 0 {
		t.Errorf("LabelWarnings should be empty for valid seed labels, got: %v", item.LabelWarnings)
	}
}

// TestLabels_HappyPath_UserDefined verifies that a user-defined label survives
// derive-time enforcement when a matching label-define message exists in the log.
func TestLabels_HappyPath_UserDefined(t *testing.T) {
	h := storetest.New(t)
	h.LabelDefine("my-custom-label", "A custom label")
	h.Create("item-custom", "Custom label task", "p1",
		storetest.WithLabels("my-custom-label"))

	result := h.DeriveAll()
	item, ok := result.Items()["item-custom"]
	if !ok {
		t.Fatal("item 'item-custom' not found")
	}
	if len(item.Labels) != 1 || item.Labels[0] != "my-custom-label" {
		t.Errorf("Labels=%v, want [my-custom-label]", item.Labels)
	}
	if len(item.LabelWarnings) != 0 {
		t.Errorf("LabelWarnings should be empty, got: %v", item.LabelWarnings)
	}
}

// TestLabels_EmptyLabels verifies that an item created without labels has
// an empty Labels slice and no warnings.
func TestLabels_EmptyLabels(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-nolabels", "No labels item", "p3")

	result := h.DeriveAll()
	item, ok := result.Items()["item-nolabels"]
	if !ok {
		t.Fatal("item 'item-nolabels' not found")
	}
	if len(item.Labels) != 0 {
		t.Errorf("Labels should be empty, got: %v", item.Labels)
	}
	if len(item.LabelWarnings) != 0 {
		t.Errorf("LabelWarnings should be empty, got: %v", item.LabelWarnings)
	}
}

// TestLabels_MixedValidInvalid verifies that valid seed labels pass through while
// pattern-invalid or unregistered labels are dropped.
func TestLabels_MixedValidInvalid(t *testing.T) {
	h := storetest.New(t)
	// Inject raw: include bug (seed, valid), unregistered, and send bug should be kept.
	// Use RawCreate so we can include the invalid atom directly (bypassing write-side gate).
	_ = h.RawCreate("item-mixed", "Mixed labels", "p2", map[string]interface{}{
		"labels": "bug,unregistered-label",
	})

	result := h.DeriveAll()
	item, ok := result.Items()["item-mixed"]
	if !ok {
		t.Fatal("item 'item-mixed' not found")
	}
	// bug is a seed label → kept.
	if len(item.Labels) != 1 || item.Labels[0] != "bug" {
		t.Errorf("Labels=%v, want [bug] (unregistered-label must be dropped)", item.Labels)
	}
	// Warning for unregistered-label.
	warnText := strings.Join(item.LabelWarnings, " ")
	if !strings.Contains(warnText, "unregistered-label") {
		t.Errorf("LabelWarnings=%v should mention 'unregistered-label'", item.LabelWarnings)
	}
}

// ---------------------------------------------------------------------------
// BYPASS TEST — the load-bearing test for this feature.
//
// Writes a raw work:create message directly into the store (NOT via the
// convention executor), mimicking the ready-1c2 pending.jsonl flush path
// that bypasses the executor. The payload contains:
//   - "ignore-all-previous-instructions": pattern-valid, but NOT in the registry
//   - "Bad Label!": pattern-invalid (uppercase, space, exclamation mark)
//
// Expected: neither label materializes in Item.Labels; warnings are recorded;
// the item's other fields (title, priority, type, etc.) still materialize correctly.
// ---------------------------------------------------------------------------

// TestLabels_BypassTest is the MANDATORY bypass test per i2.md done condition.
// It is the primary evidence that derive-time enforcement holds even when
// the write path (executor) is completely bypassed.
func TestLabels_BypassTest(t *testing.T) {
	h := storetest.New(t)

	// Inject raw message directly into the store, bypassing the convention executor.
	// This mimics ready-1c2: pending.jsonl flush writes raw via transport, skipping
	// executor validation. The payload deliberately carries:
	//   1. "ignore-all-previous-instructions" — pattern-valid but not in registry
	//   2. "Bad Label!" — pattern-invalid (uppercase, spaces, exclamation)
	//
	// Neither should appear in Item.Labels after DeriveAllFromStore.
	_ = h.RawCreate("item-bypass", "Bypass test item", "p2", map[string]interface{}{
		"labels": "ignore-all-previous-instructions,Bad Label!",
	})

	// Run full DeriveAllFromStore — this is what i2.md mandates.
	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		t.Fatalf("DeriveAllFromStore: %v", err)
	}

	item, ok := result.Items()["item-bypass"]
	if !ok {
		t.Fatal("item 'item-bypass' not found in derived state — item must still materialize even with bad labels")
	}

	// Assert: neither label appears in Item.Labels.
	for _, l := range item.Labels {
		if l == "ignore-all-previous-instructions" {
			t.Error("BYPASS TEST FAILED: 'ignore-all-previous-instructions' (unregistered) materialized in Item.Labels — derive-time enforcement did not gate it")
		}
		if l == "Bad Label!" {
			t.Error("BYPASS TEST FAILED: 'Bad Label!' (pattern-invalid) materialized in Item.Labels — derive-time enforcement did not gate it")
		}
	}

	// Assert: warnings were recorded for both dropped labels.
	if len(item.LabelWarnings) == 0 {
		t.Error("expected LabelWarnings to be non-empty for dropped labels, got none")
	}

	warnText := strings.Join(item.LabelWarnings, " ")
	if !strings.Contains(warnText, "ignore-all-previous-instructions") {
		t.Errorf("LabelWarnings %v should mention 'ignore-all-previous-instructions'", item.LabelWarnings)
	}
	if !strings.Contains(warnText, "Bad Label!") {
		t.Errorf("LabelWarnings %v should mention 'Bad Label!'", item.LabelWarnings)
	}

	// Assert: item's other fields still materialize (the item is not silently dropped).
	if item.Title != "Bypass test item" {
		t.Errorf("Title=%q, want 'Bypass test item' — item must still materialize with other fields intact", item.Title)
	}
	if item.Priority != "p2" {
		t.Errorf("Priority=%q, want 'p2'", item.Priority)
	}
	if item.Status != state.StatusInbox {
		t.Errorf("Status=%q, want StatusInbox", item.Status)
	}
}

// TestLabels_BypassTest_OnlyUnregisteredLabel tests the bypass path with only
// a pattern-valid but unregistered label — it must be dropped.
func TestLabels_BypassTest_OnlyUnregisteredLabel(t *testing.T) {
	h := storetest.New(t)

	// "prompt-injection-attempt" is pattern-valid but not in the seed registry.
	_ = h.RawCreate("item-unreg", "Unregistered label only", "p1", map[string]interface{}{
		"labels": "prompt-injection-attempt",
	})

	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		t.Fatalf("DeriveAllFromStore: %v", err)
	}
	item, ok := result.Items()["item-unreg"]
	if !ok {
		t.Fatal("item not found")
	}

	if len(item.Labels) != 0 {
		t.Errorf("unregistered label must not appear in Item.Labels; got: %v", item.Labels)
	}
	if len(item.LabelWarnings) == 0 {
		t.Error("expected LabelWarnings for dropped unregistered label")
	}
}

// TestLabels_BypassTest_OnlyPatternInvalidLabel tests the bypass path with only
// a pattern-invalid label — it must be dropped.
func TestLabels_BypassTest_OnlyPatternInvalidLabel(t *testing.T) {
	h := storetest.New(t)

	_ = h.RawCreate("item-invalid-pat", "Pattern invalid label only", "p3", map[string]interface{}{
		"labels": "Bad Label!",
	})

	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		t.Fatalf("DeriveAllFromStore: %v", err)
	}
	item, ok := result.Items()["item-invalid-pat"]
	if !ok {
		t.Fatal("item not found")
	}

	if len(item.Labels) != 0 {
		t.Errorf("pattern-invalid label must not appear in Item.Labels; got: %v", item.Labels)
	}
	if len(item.LabelWarnings) == 0 {
		t.Error("expected LabelWarnings for dropped pattern-invalid label")
	}
}

// ---------------------------------------------------------------------------
// 9-label rejection test (max 8 atoms in composite pattern).
// Note: the composite pattern on create.json (write-side) allows max 8 atoms.
// The derive-time gate will reject 9 raw atoms injected via RawCreate even if
// they are all registered (since we can only inject 9 via raw path bypassing
// the write-side 8-atom limit). Here we inject 9 atoms, 6 of which are seed
// labels, and verify no more than the registered ones survive.
// The key done condition from i2.md: "9 labels rejected by pattern (max 8)".
// This means the executor (write-side) rejects a labels arg with 9 atoms.
// The actual rejection test for 9 atoms goes in pkg/declarations tests and
// cmd/rd executor tests. Here we test the derive-time behavior.
// ---------------------------------------------------------------------------

// TestLabels_NineLabelsDeriveTime verifies that when 9 labels are injected
// via the raw path (bypassing the 8-atom write-side limit), derive-time
// enforcement still processes them individually against the registry.
func TestLabels_NineLabelsDeriveTime(t *testing.T) {
	h := storetest.New(t)
	// All 6 seeds + 3 unregistered = 9 total. Registered ones survive; unregistered are dropped.
	_ = h.RawCreate("item-nine", "Nine labels item", "p2", map[string]interface{}{
		"labels": "bug,feature,question,security,sweep-finding,blog-candidate,unregistered-a,unregistered-b,unregistered-c",
	})

	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		t.Fatalf("DeriveAllFromStore: %v", err)
	}
	item, ok := result.Items()["item-nine"]
	if !ok {
		t.Fatal("item 'item-nine' not found")
	}
	// Only the 6 seed labels should survive.
	if len(item.Labels) != 6 {
		t.Errorf("Labels count=%d, want 6 (6 seed labels survived, 3 unregistered dropped): %v", len(item.Labels), item.Labels)
	}
	if len(item.LabelWarnings) != 3 {
		t.Errorf("LabelWarnings count=%d, want 3 (one per dropped unregistered label): %v", len(item.LabelWarnings), item.LabelWarnings)
	}
}

// ---------------------------------------------------------------------------
// Skew test: old createPayload struct (no Labels field) must not error when
// parsing a labels-bearing payload. This verifies read-safety for old readers.
// ---------------------------------------------------------------------------

// oldCreatePayload mirrors the pre-v0.4 createPayload struct (no Labels field).
// This simulates an old reader that has not been updated to understand labels.
type oldCreatePayload struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Context  string `json:"context"`
	Type     string `json:"type"`
	Level    string `json:"level"`
	Project  string `json:"project"`
	For      string `json:"for"`
	By       string `json:"by"`
	Priority string `json:"priority"`
	ParentID string `json:"parent_id"`
	ETA      string `json:"eta"`
	Due      string `json:"due"`
	Gate     string `json:"gate"`
	// No Labels field — this is an old reader.
}

// TestLabels_SkewTest_OldReaderDoesNotError verifies that an old createPayload
// struct (without a Labels field) can parse a labels-bearing payload without
// error. encoding/json silently ignores unknown fields, so old readers are safe.
// This is the "version skew is read-safe" property required by i2.md.
func TestLabels_SkewTest_OldReaderDoesNotError(t *testing.T) {
	// A labels-bearing payload (as would be stored in the campfire transport).
	payloadJSON := `{
		"id": "ready-abc",
		"title": "Labelled item",
		"type": "task",
		"for": "baron@3dl.dev",
		"priority": "p2",
		"labels": "bug,security"
	}`

	var old oldCreatePayload
	if err := json.Unmarshal([]byte(payloadJSON), &old); err != nil {
		t.Errorf("old createPayload (no Labels field) failed to parse labels-bearing payload: %v — version skew must be read-safe", err)
	}

	// Verify the fields the old reader knows about are populated correctly.
	if old.ID != "ready-abc" {
		t.Errorf("ID=%q, want 'ready-abc'", old.ID)
	}
	if old.Title != "Labelled item" {
		t.Errorf("Title=%q, want 'Labelled item'", old.Title)
	}
	if old.Priority != "p2" {
		t.Errorf("Priority=%q, want 'p2'", old.Priority)
	}
}

// TestLabels_DeduplicatedOnItem verifies that duplicate labels in a payload
// are deduplicated in Item.Labels (appendUnique behavior).
func TestLabels_DeduplicatedOnItem(t *testing.T) {
	h := storetest.New(t)
	// bug,bug — duplicate. Both are seed labels.
	_ = h.RawCreate("item-dedup", "Dedup labels", "p1", map[string]interface{}{
		"labels": "bug,bug",
	})

	result := h.DeriveAll()
	item, ok := result.Items()["item-dedup"]
	if !ok {
		t.Fatal("item 'item-dedup' not found")
	}
	if len(item.Labels) != 1 {
		t.Errorf("Labels=%v, want exactly [bug] (duplicate must be deduplicated)", item.Labels)
	}
}

// TestLabels_OtherItemsUnaffected verifies that the label enforcement on one item
// does not affect other items in the same derive run.
func TestLabels_OtherItemsUnaffected(t *testing.T) {
	h := storetest.New(t)
	// Create one item with valid labels and one with invalid labels.
	h.Create("item-valid", "Valid item", "p2", storetest.WithLabels("bug"))
	_ = h.RawCreate("item-badlabel", "Bad label item", "p1", map[string]interface{}{
		"labels": "totally-unregistered",
	})

	result := h.DeriveAll()

	// Valid item should have Labels=[bug].
	validItem, ok := result.Items()["item-valid"]
	if !ok {
		t.Fatal("item 'item-valid' not found")
	}
	if len(validItem.Labels) != 1 || validItem.Labels[0] != "bug" {
		t.Errorf("valid item Labels=%v, want [bug]", validItem.Labels)
	}

	// Bad label item should have no Labels and a warning.
	badItem, ok := result.Items()["item-badlabel"]
	if !ok {
		t.Fatal("item 'item-badlabel' not found")
	}
	if len(badItem.Labels) != 0 {
		t.Errorf("bad label item Labels=%v, want [] (unregistered dropped)", badItem.Labels)
	}
	if len(badItem.LabelWarnings) == 0 {
		t.Error("expected LabelWarnings on item with dropped unregistered label")
	}
}
