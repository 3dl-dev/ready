package state_test

// label_ops_test.go — tests for work:label-add and work:label-remove operations.
//
// Done conditions from i3.md:
//   1. add bug to existing item → Labels contains bug
//   2. remove bug → absent
//   3. interleaved add/remove orderings resolve by timestamp
//   4. add of unregistered pattern-valid label is dropped at derive with warning
//   5. add of malformed label rejected (no panic)
//   6. label-add for nonexistent item id → recorded warning, no panic, no phantom item

import (
	"strings"
	"testing"

	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/storetest"
)

// ---------------------------------------------------------------------------
// Happy path: add a seed label to an existing item.
// ---------------------------------------------------------------------------

// TestLabelOps_Add_SeedLabel verifies that label-add for a seed label materializes
// in Item.Labels.
func TestLabelOps_Add_SeedLabel(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-add-seed", "Add seed label test", "p2")
	h.AddLabel("item-add-seed", "bug")

	result := h.DeriveAll()
	item, ok := result.Items()["item-add-seed"]
	if !ok {
		t.Fatal("item 'item-add-seed' not found in derived state")
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bug" {
		t.Errorf("Labels=%v, want [bug]", item.Labels)
	}
	if len(item.LabelWarnings) != 0 {
		t.Errorf("LabelWarnings should be empty for valid seed label add, got: %v", item.LabelWarnings)
	}
}

// TestLabelOps_Add_UserDefined verifies that label-add works for user-defined labels
// that exist in the registry.
func TestLabelOps_Add_UserDefined(t *testing.T) {
	h := storetest.New(t)
	h.LabelDefine("custom-label", "A custom label")
	h.Create("item-add-user", "Add user-defined label test", "p1")
	h.AddLabel("item-add-user", "custom-label")

	result := h.DeriveAll()
	item, ok := result.Items()["item-add-user"]
	if !ok {
		t.Fatal("item 'item-add-user' not found")
	}
	if len(item.Labels) != 1 || item.Labels[0] != "custom-label" {
		t.Errorf("Labels=%v, want [custom-label]", item.Labels)
	}
}

// TestLabelOps_Add_ThenRemove verifies the basic add-then-remove cycle.
func TestLabelOps_Add_ThenRemove(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-add-remove", "Add then remove", "p2")
	h.AddLabel("item-add-remove", "bug")
	h.RemoveLabel("item-add-remove", "bug")

	result := h.DeriveAll()
	item, ok := result.Items()["item-add-remove"]
	if !ok {
		t.Fatal("item 'item-add-remove' not found")
	}
	if len(item.Labels) != 0 {
		t.Errorf("Labels=%v, want [] (bug was removed)", item.Labels)
	}
	if len(item.LabelWarnings) != 0 {
		t.Errorf("LabelWarnings should be empty, got: %v", item.LabelWarnings)
	}
}

// TestLabelOps_Remove_ThenAdd verifies that remove-then-add yields present.
func TestLabelOps_Remove_ThenAdd(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-remove-add", "Remove then add", "p2")
	// Bug is not on the item initially; remove is a no-op, then add makes it present.
	h.RemoveLabel("item-remove-add", "bug")
	h.AddLabel("item-remove-add", "bug")

	result := h.DeriveAll()
	item, ok := result.Items()["item-remove-add"]
	if !ok {
		t.Fatal("item 'item-remove-add' not found")
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bug" {
		t.Errorf("Labels=%v, want [bug] (add after remove should be present)", item.Labels)
	}
}

// TestLabelOps_Interleaved verifies complex interleaved add/remove sequences.
// Two labels, with cross-operations, resolve correctly by timestamp order.
func TestLabelOps_Interleaved(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-interleaved", "Interleaved add/remove", "p2")

	// Sequence: add bug, add security, remove bug, add bug, remove security
	// Final: bug=present, security=absent
	h.AddLabel("item-interleaved", "bug")
	h.AddLabel("item-interleaved", "security")
	h.RemoveLabel("item-interleaved", "bug")
	h.AddLabel("item-interleaved", "bug")
	h.RemoveLabel("item-interleaved", "security")

	result := h.DeriveAll()
	item, ok := result.Items()["item-interleaved"]
	if !ok {
		t.Fatal("item 'item-interleaved' not found")
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bug" {
		t.Errorf("Labels=%v, want [bug] (bug re-added, security removed)", item.Labels)
	}
}

// ---------------------------------------------------------------------------
// Duplicate add is idempotent.
// ---------------------------------------------------------------------------

// TestLabelOps_DuplicateAdd_Idempotent verifies that adding the same label twice
// does not duplicate it in Item.Labels.
func TestLabelOps_DuplicateAdd_Idempotent(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-dup-add", "Duplicate add test", "p2")
	h.AddLabel("item-dup-add", "bug")
	h.AddLabel("item-dup-add", "bug") // duplicate

	result := h.DeriveAll()
	item, ok := result.Items()["item-dup-add"]
	if !ok {
		t.Fatal("item 'item-dup-add' not found")
	}
	if len(item.Labels) != 1 {
		t.Errorf("Labels=%v, want exactly [bug] (duplicate add must be idempotent)", item.Labels)
	}
}

// TestLabelOps_DuplicateRemove_Idempotent verifies that removing the same label twice
// is idempotent — no panic, no error.
func TestLabelOps_DuplicateRemove_Idempotent(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-dup-remove", "Duplicate remove test", "p2")
	h.AddLabel("item-dup-remove", "bug")
	h.RemoveLabel("item-dup-remove", "bug")
	h.RemoveLabel("item-dup-remove", "bug") // duplicate remove is no-op

	result := h.DeriveAll()
	item, ok := result.Items()["item-dup-remove"]
	if !ok {
		t.Fatal("item 'item-dup-remove' not found")
	}
	if len(item.Labels) != 0 {
		t.Errorf("Labels=%v, want [] (bug removed)", item.Labels)
	}
}

// ---------------------------------------------------------------------------
// Enforcement: unregistered pattern-valid label dropped at derive with warning.
// ---------------------------------------------------------------------------

// TestLabelOps_Add_UnregisteredLabel verifies that label-add for an unregistered
// but pattern-valid label is dropped at derive time with a warning.
// This is the bypass-equivalent path per i3.md done condition.
func TestLabelOps_Add_UnregisteredLabel(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-unreg-add", "Unregistered label add", "p2")
	// Inject raw label-add message bypassing executor (storetest always injects directly)
	h.RawAddLabel("item-unreg-add", "unregistered-label")

	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		t.Fatalf("DeriveAllFromStore: %v", err)
	}
	item, ok := result.Items()["item-unreg-add"]
	if !ok {
		t.Fatal("item 'item-unreg-add' not found (item must still materialize)")
	}
	if len(item.Labels) != 0 {
		t.Errorf("Labels=%v, want [] (unregistered label must be dropped)", item.Labels)
	}
	if len(item.LabelWarnings) == 0 {
		t.Error("expected LabelWarnings for dropped unregistered label add")
	}
	warnText := strings.Join(item.LabelWarnings, " ")
	if !strings.Contains(warnText, "unregistered-label") {
		t.Errorf("LabelWarnings=%v should mention 'unregistered-label'", item.LabelWarnings)
	}
}

// TestLabelOps_Add_MalformedLabel verifies that label-add for a pattern-invalid label
// is dropped at derive time — no panic, warning recorded.
func TestLabelOps_Add_MalformedLabel(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-malformed-add", "Malformed label add", "p1")
	h.RawAddLabel("item-malformed-add", "Bad Label!")

	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		t.Fatalf("DeriveAllFromStore: %v", err)
	}
	item, ok := result.Items()["item-malformed-add"]
	if !ok {
		t.Fatal("item 'item-malformed-add' not found")
	}
	if len(item.Labels) != 0 {
		t.Errorf("Labels=%v, want [] (malformed label must be dropped)", item.Labels)
	}
	if len(item.LabelWarnings) == 0 {
		t.Error("expected LabelWarnings for dropped malformed label add")
	}
}

// ---------------------------------------------------------------------------
// Nonexistent item ID: warning recorded, no panic, no phantom item.
// ---------------------------------------------------------------------------

// TestLabelOps_Add_NonexistentItemID verifies that label-add for a nonexistent item
// ID records a warning in DeriveResult.Warnings(), produces no panic, and creates no
// phantom item. The test is mutation-sensitive: removing the warning emission from
// handleWorkLabelAdd causes this test to fail on the warning assertions.
func TestLabelOps_Add_NonexistentItemID(t *testing.T) {
	h := storetest.New(t)
	// Inject label-add referencing an item ID that does not exist in the log.
	h.RawAddLabelByItemID("nonexistent-item-id", "bug")

	result := h.DeriveAll()
	items := result.Items()

	// No phantom item must be created for the nonexistent id.
	if _, ok := items["nonexistent-item-id"]; ok {
		t.Error("phantom item 'nonexistent-item-id' must not be created by label-add")
	}

	// A warning must be recorded — observability for deleted item / typo / race.
	warnings := result.Warnings()
	if len(warnings) == 0 {
		t.Fatal("expected at least one warning for label-add targeting nonexistent item, got none")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "nonexistent-item-id") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("warning should mention 'nonexistent-item-id', got: %v", warnings)
	}
}

// TestLabelOps_Remove_NonexistentItemID verifies that label-remove for a nonexistent
// item ID records a warning in DeriveResult.Warnings(), produces no panic, and creates
// no phantom item. Symmetric to the add case; the warning emission in
// handleWorkLabelRemove must be present for this test to pass.
func TestLabelOps_Remove_NonexistentItemID(t *testing.T) {
	h := storetest.New(t)
	h.RawRemoveLabelByItemID("nonexistent-item-id", "bug")

	result := h.DeriveAll()
	items := result.Items()

	if _, ok := items["nonexistent-item-id"]; ok {
		t.Error("phantom item 'nonexistent-item-id' must not be created by label-remove")
	}

	// A warning must be recorded.
	warnings := result.Warnings()
	if len(warnings) == 0 {
		t.Fatal("expected at least one warning for label-remove targeting nonexistent item, got none")
	}
	found := false
	for _, w := range warnings {
		if strings.Contains(w, "nonexistent-item-id") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("warning should mention 'nonexistent-item-id', got: %v", warnings)
	}
}

// ---------------------------------------------------------------------------
// Combined create-then-add: labels from create + ops both appear.
// ---------------------------------------------------------------------------

// TestLabelOps_CreateWithLabel_ThenAddAnother verifies that items created with
// labels and then given additional labels via label-add get the combined set.
func TestLabelOps_CreateWithLabel_ThenAddAnother(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-combined", "Combined labels test", "p2",
		storetest.WithLabels("bug"))
	h.AddLabel("item-combined", "security")

	result := h.DeriveAll()
	item, ok := result.Items()["item-combined"]
	if !ok {
		t.Fatal("item 'item-combined' not found")
	}
	if len(item.Labels) != 2 {
		t.Errorf("Labels=%v, want [bug security] (both labels present)", item.Labels)
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
}

// TestLabelOps_CreateWithLabel_ThenRemoveIt verifies that a label set at create time
// can be removed by a subsequent label-remove op.
func TestLabelOps_CreateWithLabel_ThenRemoveIt(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-create-remove", "Create then remove label", "p2",
		storetest.WithLabels("bug", "security"))
	h.RemoveLabel("item-create-remove", "bug")

	result := h.DeriveAll()
	item, ok := result.Items()["item-create-remove"]
	if !ok {
		t.Fatal("item 'item-create-remove' not found")
	}
	// bug was removed; security remains
	if len(item.Labels) != 1 || item.Labels[0] != "security" {
		t.Errorf("Labels=%v, want [security] (bug removed, security retained)", item.Labels)
	}
}

// ---------------------------------------------------------------------------
// Other items unaffected.
// ---------------------------------------------------------------------------

// TestLabelOps_OtherItemsUnaffected verifies that label-add/remove on one item
// does not affect other items in the same derive run.
func TestLabelOps_OtherItemsUnaffected(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-a-labels", "Item A", "p2",
		storetest.WithLabels("bug"))
	h.Create("item-b-labels", "Item B", "p1")
	// Add security to item A, should not touch B
	h.AddLabel("item-a-labels", "security")

	result := h.DeriveAll()
	itemA, ok := result.Items()["item-a-labels"]
	if !ok {
		t.Fatal("item 'item-a-labels' not found")
	}
	itemB, ok := result.Items()["item-b-labels"]
	if !ok {
		t.Fatal("item 'item-b-labels' not found")
	}

	if len(itemA.Labels) != 2 {
		t.Errorf("item A Labels=%v, want [bug security]", itemA.Labels)
	}
	if len(itemB.Labels) != 0 {
		t.Errorf("item B Labels=%v, want [] (unaffected by ops on A)", itemB.Labels)
	}
}
