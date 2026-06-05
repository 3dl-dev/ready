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

// ---------------------------------------------------------------------------
// Fix 1 derive assertion: 9 fully-registered atoms all materialize.
//
// The executor write-side pattern (^[a-z0-9][a-z0-9,-]*$) has no atom-count
// cap — it intentionally accepts 9+ atoms because nested quantifiers are
// prohibited by the campfire executor. The 8-atom cap from the original
// composite pattern was descoped when that pattern was rejected by the
// executor. Derive time has NO atom-count cap: all registered atoms survive.
// ---------------------------------------------------------------------------

// TestLabels_NineRegisteredAtoms_AllMaterialize verifies that when 9 atoms are
// injected via RawCreate and ALL 9 are in the label registry, all 9 survive
// derive-time enforcement. Derive-time has no atom-count cap — it filters only
// by pattern validity and registry membership. Atom-count capping was descoped
// along with the composite write-side pattern.
func TestLabels_NineRegisteredAtoms_AllMaterialize(t *testing.T) {
	h := storetest.New(t)

	// Register all 9 atoms. Without registry membership each would be dropped.
	// label1..label9 are user-defined here (not seed labels).
	atoms := []string{"label1", "label2", "label3", "label4", "label5", "label6", "label7", "label8", "label9"}
	for _, atom := range atoms {
		h.LabelDefine(atom, "registered for 9-atom derive test")
	}

	nineLabels := strings.Join(atoms, ",")
	if strings.Count(nineLabels, ",") != 8 {
		t.Fatalf("test setup: expected 9 atoms (8 commas), got %d commas", strings.Count(nineLabels, ","))
	}

	// Inject via RawCreate — bypasses write-side atom-count gate (if any).
	_ = h.RawCreate("item-nine-reg", "Nine registered labels", "p2", map[string]interface{}{
		"labels": nineLabels,
	})

	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		t.Fatalf("DeriveAllFromStore: %v", err)
	}
	item, ok := result.Items()["item-nine-reg"]
	if !ok {
		t.Fatal("item 'item-nine-reg' not found in derived state")
	}

	// All 9 registered atoms must materialize — no derive-time atom-count cap exists.
	if len(item.Labels) != 9 {
		t.Errorf("Labels count=%d, want 9 (all 9 registered atoms must survive derive): %v",
			len(item.Labels), item.Labels)
	}
	if len(item.LabelWarnings) != 0 {
		t.Errorf("LabelWarnings should be empty (all atoms registered): %v", item.LabelWarnings)
	}
}

// ---------------------------------------------------------------------------
// Fix 2: derive-level boundary test for 32/33-char atom length.
//
// labelAtomPattern is ^[a-z0-9][a-z0-9-]{0,31}$ — 1 + up to 31 = max 32 chars.
// A 33-char atom must be DROPPED with a warning naming the atom.
// A 32-char atom that is registered must MATERIALIZE.
// ---------------------------------------------------------------------------

// TestLabels_AtomLengthBoundary_33Dropped_32Kept verifies the derive-time
// atom length boundary at 32 characters (pattern: ^[a-z0-9][a-z0-9-]{0,31}$).
// A 33-char atom is dropped with a warning naming the atom; a 32-char registered
// atom materializes without warnings.
func TestLabels_AtomLengthBoundary_33Dropped_32Kept(t *testing.T) {
	h := storetest.New(t)

	// 32 chars: 'a' + 31 'b's — exactly at the boundary.
	atom32 := "a" + strings.Repeat("b", 31)
	if len(atom32) != 32 {
		t.Fatalf("test setup: atom32 has length %d, want 32", len(atom32))
	}
	// Register the 32-char atom so it passes the registry gate.
	h.LabelDefine(atom32, "32-char boundary atom")

	// 33 chars: 'a' + 32 'b's — one over the limit, fails pattern.
	atom33 := "a" + strings.Repeat("b", 32)
	if len(atom33) != 33 {
		t.Fatalf("test setup: atom33 has length %d, want 33", len(atom33))
	}
	// No LabelDefine for atom33 — it would fail the pattern gate before registry anyway.

	_ = h.RawCreate("item-boundary", "Atom length boundary", "p2", map[string]interface{}{
		"labels": atom32 + "," + atom33,
	})

	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		t.Fatalf("DeriveAllFromStore: %v", err)
	}
	item, ok := result.Items()["item-boundary"]
	if !ok {
		t.Fatal("item 'item-boundary' not found in derived state")
	}

	// The 32-char atom must materialize (registered, pattern-valid).
	if len(item.Labels) != 1 || item.Labels[0] != atom32 {
		t.Errorf("Labels=%v, want [%s] (32-char registered atom must materialize)", item.Labels, atom32)
	}

	// The 33-char atom must be dropped, and the warning must name the atom.
	if len(item.LabelWarnings) == 0 {
		t.Error("expected LabelWarnings for the 33-char atom that exceeds pattern limit")
	}
	warnText := strings.Join(item.LabelWarnings, " ")
	if !strings.Contains(warnText, atom33) {
		t.Errorf("LabelWarnings=%v should name the dropped 33-char atom %q", item.LabelWarnings, atom33)
	}
}

// ---------------------------------------------------------------------------
// Fix 3: derive-level double-comma test.
//
// The write-side pattern ^[a-z0-9][a-z0-9,-]*$ permits commas inside the
// scalar, so "a,,b" passes the write-side gate. At derive time, splitting on
// "," produces an empty atom "" which the code handles via `if atom == "" {
// continue }`. No warning is emitted for empty atoms — only non-empty atoms
// that fail pattern or registry produce warnings.
// ---------------------------------------------------------------------------

// TestLabels_DoubleComma_EmptyAtomSilentlySkipped verifies that a labels value
// with a double comma (e.g., "a,,b") produces only the valid atoms in
// Item.Labels and emits NO warning for the empty atom — the empty string is
// silently skipped via `if atom == "" { continue }` in the derive loop.
func TestLabels_DoubleComma_EmptyAtomSilentlySkipped(t *testing.T) {
	h := storetest.New(t)
	// "a" and "b" are user-defined labels (not seed labels).
	h.LabelDefine("a", "single-char label a")
	h.LabelDefine("b", "single-char label b")

	// "a,,b" — double comma produces an empty atom between the two valid ones.
	_ = h.RawCreate("item-doublecomma", "Double comma labels", "p1", map[string]interface{}{
		"labels": "a,,b",
	})

	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		t.Fatalf("DeriveAllFromStore: %v", err)
	}
	item, ok := result.Items()["item-doublecomma"]
	if !ok {
		t.Fatal("item 'item-doublecomma' not found in derived state")
	}

	// Both "a" and "b" must materialize.
	labelSet := make(map[string]bool)
	for _, l := range item.Labels {
		labelSet[l] = true
	}
	if !labelSet["a"] {
		t.Errorf("Labels=%v missing 'a' (should materialize as registered)", item.Labels)
	}
	if !labelSet["b"] {
		t.Errorf("Labels=%v missing 'b' (should materialize as registered)", item.Labels)
	}
	if len(item.Labels) != 2 {
		t.Errorf("Labels=%v, want exactly [a b] (2 items, no empty atom)", item.Labels)
	}

	// No warning for the empty atom — it is silently skipped, not warned about.
	if len(item.LabelWarnings) != 0 {
		t.Errorf("LabelWarnings should be empty (empty atom silently skipped, a and b are registered): %v",
			item.LabelWarnings)
	}
}
