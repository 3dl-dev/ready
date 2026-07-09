package playbook_test

import (
	"encoding/json"
	"fmt"
	"regexp"
	"testing"

	"github.com/campfire-net/ready/pkg/playbook"
)

var itemIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{2,63}$`)

// sampleItemsJSON is a valid template items JSON for testing.
var sampleItemsJSON = []byte(`[
  {
    "title": "Step 1: {{project}} setup",
    "type": "task",
    "level": "task",
    "priority": "p1",
    "context": "Set up the {{project}} scaffolding",
    "deps": []
  },
  {
    "title": "Step 2: {{project}} implementation",
    "type": "task",
    "level": "task",
    "priority": "p1",
    "context": "Implement the core feature for {{project}}",
    "deps": [0]
  }
]`)

func TestParse_Valid(t *testing.T) {
	tmpl, err := playbook.Parse("test-playbook", "Test Playbook", "A test playbook", sampleItemsJSON)
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	if tmpl.ID != "test-playbook" {
		t.Errorf("expected ID test-playbook, got %q", tmpl.ID)
	}
	if tmpl.Title != "Test Playbook" {
		t.Errorf("expected title 'Test Playbook', got %q", tmpl.Title)
	}
	if len(tmpl.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(tmpl.Items))
	}
	if tmpl.Items[0].Title != "Step 1: {{project}} setup" {
		t.Errorf("unexpected title: %q", tmpl.Items[0].Title)
	}
	if len(tmpl.Items[1].Deps) != 1 || tmpl.Items[1].Deps[0] != 0 {
		t.Errorf("unexpected deps on item[1]: %v", tmpl.Items[1].Deps)
	}
}

func TestParse_InvalidID(t *testing.T) {
	_, err := playbook.Parse("BAD_ID", "Title", "", sampleItemsJSON)
	if err == nil {
		t.Fatal("expected error for invalid ID")
	}
}

func TestParse_EmptyTitle(t *testing.T) {
	_, err := playbook.Parse("valid-id", "", "", sampleItemsJSON)
	if err == nil {
		t.Fatal("expected error for empty title")
	}
}

func TestParse_EmptyItems(t *testing.T) {
	_, err := playbook.Parse("valid-id", "Title", "", []byte(`[]`))
	if err == nil {
		t.Fatal("expected error for empty items")
	}
}

func TestParse_InvalidJSON(t *testing.T) {
	_, err := playbook.Parse("valid-id", "Title", "", []byte(`not json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

func TestParse_InvalidType(t *testing.T) {
	bad := []byte(`[{"title":"T","type":"badtype","priority":"p1","deps":[]}]`)
	_, err := playbook.Parse("valid-id", "Title", "", bad)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

func TestParse_InvalidPriority(t *testing.T) {
	bad := []byte(`[{"title":"T","type":"task","priority":"high","deps":[]}]`)
	_, err := playbook.Parse("valid-id", "Title", "", bad)
	if err == nil {
		t.Fatal("expected error for invalid priority")
	}
}

func TestParse_OutOfRangeDep(t *testing.T) {
	bad := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[5]}]`)
	_, err := playbook.Parse("valid-id", "Title", "", bad)
	if err == nil {
		t.Fatal("expected error for out-of-range dep index")
	}
}

func TestParse_SelfDep(t *testing.T) {
	bad := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[0]}]`)
	_, err := playbook.Parse("valid-id", "Title", "", bad)
	if err == nil {
		t.Fatal("expected error for self-dependency")
	}
}

func TestValidate_CircularDep(t *testing.T) {
	// A→B→A cycle: item 0 deps on 1, item 1 deps on 0.
	circular := []byte(`[
		{"title":"A","type":"task","priority":"p1","deps":[1]},
		{"title":"B","type":"task","priority":"p1","deps":[0]}
	]`)
	_, err := playbook.Parse("circ-pb", "Circular", "", circular)
	if err == nil {
		t.Fatal("expected error for circular dependency")
	}
}

func TestValidate_ThreeNodeCycle(t *testing.T) {
	// A→B→C→A
	circular := []byte(`[
		{"title":"A","type":"task","priority":"p1","deps":[2]},
		{"title":"B","type":"task","priority":"p1","deps":[0]},
		{"title":"C","type":"task","priority":"p1","deps":[1]}
	]`)
	_, err := playbook.Parse("circ3-pb", "Circular3", "", circular)
	if err == nil {
		t.Fatal("expected error for 3-node circular dependency")
	}
}

func TestParseFull(t *testing.T) {
	full := map[string]interface{}{
		"id":    "full-pb",
		"title": "Full Playbook",
		"items": []map[string]interface{}{
			{"title": "Item 1", "type": "task", "priority": "p2", "deps": []int{}},
		},
	}
	data, _ := json.Marshal(full)
	tmpl, err := playbook.ParseFull(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tmpl.ID != "full-pb" {
		t.Errorf("expected ID full-pb, got %q", tmpl.ID)
	}
}

func TestExpand_VariableSubstitution(t *testing.T) {
	tmpl, err := playbook.Parse("sre-pb", "SRE Playbook", "", sampleItemsJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	vars := map[string]string{"project": "myapp"}
	items, err := playbook.Expand(tmpl, "myapp", vars)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}

	if len(items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(items))
	}
	if items[0].Title != "Step 1: myapp setup" {
		t.Errorf("unexpected title after substitution: %q", items[0].Title)
	}
	if items[0].Context != "Set up the myapp scaffolding" {
		t.Errorf("unexpected context after substitution: %q", items[0].Context)
	}
	if items[1].Title != "Step 2: myapp implementation" {
		t.Errorf("unexpected title after substitution: %q", items[1].Title)
	}
}

func TestExpand_IDGeneration(t *testing.T) {
	tmpl, err := playbook.Parse("id-pb", "ID Test", "", sampleItemsJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	items, err := playbook.Expand(tmpl, "myproject", nil)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}

	seen := map[string]bool{}
	for _, item := range items {
		if !itemIDPattern.MatchString(item.ID) {
			t.Errorf("item ID %q does not match required pattern", item.ID)
		}
		if seen[item.ID] {
			t.Errorf("duplicate item ID %q", item.ID)
		}
		seen[item.ID] = true
	}
}

func TestExpand_DepWiring(t *testing.T) {
	tmpl, err := playbook.Parse("dep-pb", "Dep Test", "", sampleItemsJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	items, err := playbook.Expand(tmpl, "proj", nil)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}

	// item[0] has no deps
	if len(items[0].Deps) != 0 {
		t.Errorf("item[0] expected no deps, got %v", items[0].Deps)
	}
	// item[1] should depend on item[0]'s ID
	if len(items[1].Deps) != 1 {
		t.Fatalf("item[1] expected 1 dep, got %d", len(items[1].Deps))
	}
	if items[1].Deps[0] != items[0].ID {
		t.Errorf("item[1] dep ID %q != item[0] ID %q", items[1].Deps[0], items[0].ID)
	}
}

func TestExpand_ItemCount(t *testing.T) {
	// 4 items, chain A→B→C→D
	chain := []byte(`[
		{"title":"A","type":"task","priority":"p1","deps":[]},
		{"title":"B","type":"task","priority":"p1","deps":[0]},
		{"title":"C","type":"task","priority":"p1","deps":[1]},
		{"title":"D","type":"task","priority":"p1","deps":[2]}
	]`)
	tmpl, err := playbook.Parse("chain-pb", "Chain", "", chain)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	items, err := playbook.Expand(tmpl, "chain", nil)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	if len(items) != 4 {
		t.Errorf("expected 4 items, got %d", len(items))
	}
	// Verify chain
	if items[3].Deps[0] != items[2].ID {
		t.Errorf("chain dep mismatch: D.dep=%q, C.id=%q", items[3].Deps[0], items[2].ID)
	}
}

func TestExpand_UnknownVariableLeftAsIs(t *testing.T) {
	tmpl, err := playbook.Parse("var-pb", "Var Test", "", sampleItemsJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	// No variables provided — {{project}} stays as-is.
	items, err := playbook.Expand(tmpl, "proj", nil)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	if items[0].Title != "Step 1: {{project}} setup" {
		t.Errorf("expected unresolved placeholder, got %q", items[0].Title)
	}
}

func TestExpand_NoProject(t *testing.T) {
	tmpl, err := playbook.Parse("np-pb", "No Project", "", sampleItemsJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	_, err = playbook.Expand(tmpl, "", nil)
	if err == nil {
		t.Fatal("expected error for empty project")
	}
}

func TestItemsJSON(t *testing.T) {
	tmpl, err := playbook.Parse("ij-pb", "Items JSON", "", sampleItemsJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	data, err := tmpl.ItemsJSON()
	if err != nil {
		t.Fatalf("ItemsJSON failed: %v", err)
	}
	// Re-parse to verify round-trip.
	var items []playbook.TemplateItem
	if err := json.Unmarshal(data, &items); err != nil {
		t.Fatalf("re-parse failed: %v", err)
	}
	if len(items) != len(tmpl.Items) {
		t.Errorf("round-trip item count mismatch: %d != %d", len(items), len(tmpl.Items))
	}
}

// TestValidate_AllValidTypes checks all valid item types are accepted.
func TestValidate_AllValidTypes(t *testing.T) {
	types := []string{"task", "decision", "review", "reminder", "deadline", "prep", "message", "directive"}
	for _, ty := range types {
		itemJSON := []byte(fmt.Sprintf(`[{"title":"T","type":"%s","priority":"p1","deps":[]}]`, ty))
		_, err := playbook.Parse("type-test", "Test", "", itemJSON)
		if err != nil {
			t.Errorf("type %q should be valid, got error: %v", ty, err)
		}
	}
}

// TestValidate_AllValidPriorities checks all valid priorities are accepted.
func TestValidate_AllValidPriorities(t *testing.T) {
	priorities := []string{"p0", "p1", "p2", "p3"}
	for _, pri := range priorities {
		itemJSON := []byte(fmt.Sprintf(`[{"title":"T","type":"task","priority":"%s","deps":[]}]`, pri))
		_, err := playbook.Parse("pri-test", "Test", "", itemJSON)
		if err != nil {
			t.Errorf("priority %q should be valid, got error: %v", pri, err)
		}
	}
}

// TestValidate_AllValidLevels checks all valid levels are accepted.
func TestValidate_AllValidLevels(t *testing.T) {
	levels := []string{"", "epic", "task", "subtask"}
	for _, lv := range levels {
		levelField := ""
		if lv != "" {
			levelField = fmt.Sprintf(`,"level":"%s"`, lv)
		}
		itemJSON := []byte(fmt.Sprintf(`[{"title":"T","type":"task","priority":"p1","deps":[]%s}]`, levelField))
		_, err := playbook.Parse("level-test", "Test", "", itemJSON)
		if err != nil {
			t.Errorf("level %q should be valid, got error: %v", lv, err)
		}
	}
}

// TestValidate_InvalidLevel checks invalid levels are rejected.
func TestValidate_InvalidLevel(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","level":"badlevel","priority":"p1","deps":[]}]`)
	_, err := playbook.Parse("bad-level", "Test", "", itemJSON)
	if err == nil {
		t.Fatal("expected error for invalid level")
	}
}

// TestValidate_InvalidType checks invalid types are rejected.
func TestValidate_InvalidType(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"badtype","priority":"p1","deps":[]}]`)
	_, err := playbook.Parse("bad-type", "Test", "", itemJSON)
	if err == nil {
		t.Fatal("expected error for invalid type")
	}
}

// TestParse_InvalidIDPattern checks various invalid ID patterns.
func TestParse_InvalidIDPattern(t *testing.T) {
	invalidIDs := []string{
		"",        // empty
		"A",       // uppercase
		"a",       // too short
		"ab",      // exactly 2 chars
		"_abc",    // starts with underscore
		"-abc",    // starts with dash
		"abc_def", // has underscore
		"abc def", // has space
	}
	for _, id := range invalidIDs {
		_, err := playbook.Parse(id, "Title", "", sampleItemsJSON)
		if err == nil {
			t.Errorf("ID %q should be invalid", id)
		}
	}
}

// TestParse_ValidIDPattern checks valid ID patterns.
func TestParse_ValidIDPattern(t *testing.T) {
	validIDs := []string{
		"a-b",          // 3 chars with dash
		"a1b",          // 3 chars with digit
		"abc-def",      // longer with dash
		"sre-incident", // typical playbook ID
		"test-pb-123",  // longer with numbers
	}
	for _, id := range validIDs {
		_, err := playbook.Parse(id, "Title", "", sampleItemsJSON)
		if err != nil {
			t.Errorf("ID %q should be valid, got error: %v", id, err)
		}
	}
}

// TestParse_EmptyDescription is allowed.
func TestParse_EmptyDescription(t *testing.T) {
	tmpl, err := playbook.Parse("desc-test", "Title", "", sampleItemsJSON)
	if err != nil {
		t.Fatalf("empty description should be allowed: %v", err)
	}
	if tmpl.Description != "" {
		t.Errorf("expected empty description, got %q", tmpl.Description)
	}
}

// TestParse_WithDescription preserves description.
func TestParse_WithDescription(t *testing.T) {
	desc := "This is a detailed description"
	tmpl, err := playbook.Parse("desc-test2", "Title", desc, sampleItemsJSON)
	if err != nil {
		t.Fatalf("parse with description failed: %v", err)
	}
	if tmpl.Description != desc {
		t.Errorf("expected description %q, got %q", desc, tmpl.Description)
	}
}

// TestValidate_EmptyItemTitle checks that empty item titles are rejected.
func TestValidate_EmptyItemTitle(t *testing.T) {
	itemJSON := []byte(`[{"title":"","type":"task","priority":"p1","deps":[]}]`)
	_, err := playbook.Parse("empty-title", "Title", "", itemJSON)
	if err == nil {
		t.Fatal("expected error for empty item title")
	}
}

// TestValidate_WhitespaceOnlyItemTitle checks that whitespace-only titles are rejected.
func TestValidate_WhitespaceOnlyItemTitle(t *testing.T) {
	itemJSON := []byte(`[{"title":"   ","type":"task","priority":"p1","deps":[]}]`)
	_, err := playbook.Parse("ws-title", "Title", "", itemJSON)
	if err == nil {
		t.Fatal("expected error for whitespace-only item title")
	}
}

// TestValidate_MultipleItems checks a playbook with many items.
func TestValidate_MultipleItems(t *testing.T) {
	itemJSON := []byte(`[
		{"title":"A","type":"task","priority":"p1","deps":[]},
		{"title":"B","type":"task","priority":"p1","deps":[]},
		{"title":"C","type":"task","priority":"p1","deps":[]},
		{"title":"D","type":"task","priority":"p1","deps":[0]},
		{"title":"E","type":"task","priority":"p1","deps":[1,2]}
	]`)
	tmpl, err := playbook.Parse("multi", "Multi", "", itemJSON)
	if err != nil {
		t.Fatalf("multi-item playbook failed: %v", err)
	}
	if len(tmpl.Items) != 5 {
		t.Errorf("expected 5 items, got %d", len(tmpl.Items))
	}
	if len(tmpl.Items[4].Deps) != 2 {
		t.Errorf("expected item[4] to have 2 deps, got %d", len(tmpl.Items[4].Deps))
	}
}

// TestValidate_MultipleDepsOnSingleItem checks items with multiple deps.
func TestValidate_MultipleDepsOnSingleItem(t *testing.T) {
	itemJSON := []byte(`[
		{"title":"A","type":"task","priority":"p1","deps":[]},
		{"title":"B","type":"task","priority":"p1","deps":[]},
		{"title":"C","type":"task","priority":"p1","deps":[0,1]}
	]`)
	tmpl, err := playbook.Parse("multi-dep", "MultiDep", "", itemJSON)
	if err != nil {
		t.Fatalf("multi-dep playbook failed: %v", err)
	}
	if len(tmpl.Items[2].Deps) != 2 {
		t.Errorf("expected 2 deps, got %d", len(tmpl.Items[2].Deps))
	}
	if tmpl.Items[2].Deps[0] != 0 || tmpl.Items[2].Deps[1] != 1 {
		t.Errorf("unexpected dep values: %v", tmpl.Items[2].Deps)
	}
}

// TestExpand_ComplexGraph tests expansion with a more complex dependency graph.
func TestExpand_ComplexGraph(t *testing.T) {
	itemJSON := []byte(`[
		{"title":"Setup","type":"task","priority":"p0","deps":[]},
		{"title":"Build","type":"task","priority":"p1","deps":[0]},
		{"title":"Test","type":"task","priority":"p1","deps":[1]},
		{"title":"Review","type":"decision","priority":"p1","deps":[2]},
		{"title":"Deploy","type":"task","priority":"p0","deps":[3]}
	]`)
	tmpl, err := playbook.Parse("complex", "Complex", "", itemJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	expanded, err := playbook.Expand(tmpl, "proj", nil)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	if len(expanded) != 5 {
		t.Errorf("expected 5 items, got %d", len(expanded))
	}
	// Verify chain: Deploy→Review→Test→Build→Setup
	if len(expanded[4].Deps) != 1 || expanded[4].Deps[0] != expanded[3].ID {
		t.Errorf("Deploy should depend on Review")
	}
	if len(expanded[3].Deps) != 1 || expanded[3].Deps[0] != expanded[2].ID {
		t.Errorf("Review should depend on Test")
	}
}

// TestExpand_VariableWithWhitespace tests variable substitution with whitespace.
func TestExpand_VariableWithWhitespace(t *testing.T) {
	itemJSON := []byte(`[
		{"title":"Prepare {{ project }}","type":"task","priority":"p1","deps":[]}
	]`)
	tmpl, err := playbook.Parse("ws-var", "WS Var", "", itemJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	vars := map[string]string{"project": "myapp"}
	expanded, err := playbook.Expand(tmpl, "myapp", vars)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	if expanded[0].Title != "Prepare myapp" {
		t.Errorf("expected 'Prepare myapp', got %q", expanded[0].Title)
	}
}

// TestExpand_MultipleVariables tests substitution with multiple distinct variables.
func TestExpand_MultipleVariables(t *testing.T) {
	itemJSON := []byte(`[
		{"title":"Setup {{env}} on {{region}}","type":"task","priority":"p1","context":"Config: {{config}}","deps":[]}
	]`)
	tmpl, err := playbook.Parse("multi-var", "Multi Var", "", itemJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	vars := map[string]string{
		"env":    "production",
		"region": "us-west",
		"config": "standard",
	}
	expanded, err := playbook.Expand(tmpl, "myapp", vars)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	if expanded[0].Title != "Setup production on us-west" {
		t.Errorf("expected title with both vars, got %q", expanded[0].Title)
	}
	if expanded[0].Context != "Config: standard" {
		t.Errorf("expected context 'Config: standard', got %q", expanded[0].Context)
	}
}

// TestExpand_DuplicateIDGeneration verifies that a single Expand call never
// produces duplicate item IDs.
//
// The previous version of this test drew IDs from 5 independent Expand calls
// (3 items each) and asserted no collisions across all of them. That is not a
// guarantee Expand actually makes or needs to make — each call is
// independent, and the underlying generator originally drew a single 3-hex-
// character suffix (a 4096-value space) with no collision checking at all.
// Across 15 draws the birthday-bound chance of a coincidental collision is
// ~2.6%, which is exactly what surfaced as an intermittent full-suite
// failure (ready-d67): passing >97% of the time made it look flaky rather
// than broken, but the test was gambling on entropy either way.
//
// generateItemID now tracks and avoids collisions deterministically within a
// single Expand call (extending the hex suffix on collision, mirroring
// cmd/rd's generateID from ready-e7c). This test proves that contract
// deterministically — not probabilistically — by requesting more items than
// the initial 3-char suffix space (4096) can hold. By pigeonhole, at least
// one collision against the initial candidate is mathematically guaranteed,
// forcing the retry/extend path on every run rather than gambling on it.
func TestExpand_DuplicateIDGeneration(t *testing.T) {
	const n = 5000 // > 4096 possible 3-char hex suffixes: forces collisions by pigeonhole
	items := make([]map[string]any, n)
	for i := range items {
		items[i] = map[string]any{
			"title":    fmt.Sprintf("Item %d", i),
			"type":     "task",
			"priority": "p1",
			"deps":     []int{},
		}
	}
	itemJSON, err := json.Marshal(items)
	if err != nil {
		t.Fatalf("marshal items failed: %v", err)
	}

	tmpl, err := playbook.Parse("dup-id", "Dup ID", "", itemJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	expanded, err := playbook.Expand(tmpl, "proj", nil)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	if len(expanded) != n {
		t.Fatalf("expected %d items, got %d", n, len(expanded))
	}

	seen := make(map[string]bool, n)
	for _, item := range expanded {
		if !itemIDPattern.MatchString(item.ID) {
			t.Errorf("item ID %q does not match required pattern", item.ID)
		}
		if seen[item.ID] {
			t.Fatalf("duplicate ID %q generated", item.ID)
		}
		seen[item.ID] = true
	}
	if len(seen) != n {
		t.Errorf("expected %d unique IDs, got %d", n, len(seen))
	}
}

// TestExpand_TemplateIndexPreserved checks that TemplateIndex is correctly set.
func TestExpand_TemplateIndexPreserved(t *testing.T) {
	itemJSON := []byte(`[
		{"title":"A","type":"task","priority":"p1","deps":[]},
		{"title":"B","type":"task","priority":"p1","deps":[0]},
		{"title":"C","type":"task","priority":"p1","deps":[1]},
		{"title":"D","type":"task","priority":"p1","deps":[2]}
	]`)
	tmpl, err := playbook.Parse("idx", "Index", "", itemJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	expanded, err := playbook.Expand(tmpl, "proj", nil)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	for i, item := range expanded {
		if item.TemplateIndex != i {
			t.Errorf("item %d: expected TemplateIndex %d, got %d", i, i, item.TemplateIndex)
		}
	}
}

// ---------------------------------------------------------------------------
// Label validation tests (ready-ef7).
// ---------------------------------------------------------------------------

// TestValidate_LabelValid checks that a valid label atom passes validation.
func TestValidate_LabelValid(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":["bug"]}]`)
	_, err := playbook.Parse("lbl-valid", "Test", "", itemJSON)
	if err != nil {
		t.Fatalf("label 'bug' should be valid, got error: %v", err)
	}
}

// TestValidate_LabelMultiple checks that multiple valid labels pass validation.
func TestValidate_LabelMultiple(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":["bug","security","p0"]}]`)
	_, err := playbook.Parse("lbl-multi", "Test", "", itemJSON)
	if err != nil {
		t.Fatalf("multiple valid labels should be valid, got error: %v", err)
	}
}

// TestValidate_LabelInvalidPattern checks that an invalid label atom fails validation
// with an error that names the item index and pattern.
func TestValidate_LabelInvalidPattern(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":["Bad Label"]}]`)
	_, err := playbook.Parse("lbl-bad", "Test", "", itemJSON)
	if err == nil {
		t.Fatal("expected error for invalid label 'Bad Label'")
	}
	// Error must name the item index.
	if !contains(err.Error(), "item[0]") {
		t.Errorf("error should name item[0], got: %v", err)
	}
	// Error must mention the pattern.
	if !contains(err.Error(), "^[a-z0-9][a-z0-9-]{0,31}$") {
		t.Errorf("error should mention the atom pattern, got: %v", err)
	}
}

// TestValidate_LabelInvalidPatternUppercase checks that uppercase label atoms are rejected.
func TestValidate_LabelInvalidPatternUppercase(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":["BUG"]}]`)
	_, err := playbook.Parse("lbl-upper", "Test", "", itemJSON)
	if err == nil {
		t.Fatal("expected error for label 'BUG' (uppercase not allowed)")
	}
}

// TestValidate_LabelTooManyItems checks that more than 8 labels per item fails.
func TestValidate_LabelTooManyItems(t *testing.T) {
	// 9 labels — one over the max.
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":["a","b","c","d","e","f","g","h","i"]}]`)
	_, err := playbook.Parse("lbl-toomany", "Test", "", itemJSON)
	if err == nil {
		t.Fatal("expected error for 9 labels (max is 8)")
	}
	if !contains(err.Error(), "item[0]") {
		t.Errorf("error should name item[0], got: %v", err)
	}
}

// TestValidate_LabelExactlyMaxAllowed checks that exactly 8 labels per item passes.
func TestValidate_LabelExactlyMaxAllowed(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":["a","b","c","d","e","f","g","h"]}]`)
	_, err := playbook.Parse("lbl-max", "Test", "", itemJSON)
	if err != nil {
		t.Fatalf("exactly 8 labels should be valid, got error: %v", err)
	}
}

// TestValidate_LabelEmptyList checks that an empty labels list is valid.
func TestValidate_LabelEmptyList(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":[]}]`)
	_, err := playbook.Parse("lbl-empty", "Test", "", itemJSON)
	if err != nil {
		t.Fatalf("empty labels list should be valid, got error: %v", err)
	}
}

// TestValidate_LabelNoLabelsField checks that omitting labels entirely is valid.
func TestValidate_LabelNoLabelsField(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[]}]`)
	_, err := playbook.Parse("lbl-absent", "Test", "", itemJSON)
	if err != nil {
		t.Fatalf("item without labels field should be valid, got error: %v", err)
	}
}

// TestExpand_LabelsThreaded checks that Expand carries labels into ExpandedItem.
func TestExpand_LabelsThreaded(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":["bug","security"]}]`)
	tmpl, err := playbook.Parse("lbl-expand", "Test", "", itemJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	items, err := playbook.Expand(tmpl, "proj", nil)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].Labels) != 2 {
		t.Fatalf("expected 2 labels, got %d: %v", len(items[0].Labels), items[0].Labels)
	}
	if items[0].Labels[0] != "bug" || items[0].Labels[1] != "security" {
		t.Errorf("unexpected labels: %v", items[0].Labels)
	}
}

// TestExpand_NoLabelsEmpty checks that items without labels produce empty Labels.
func TestExpand_NoLabelsEmpty(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[]}]`)
	tmpl, err := playbook.Parse("lbl-none", "Test", "", itemJSON)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}
	items, err := playbook.Expand(tmpl, "proj", nil)
	if err != nil {
		t.Fatalf("expand failed: %v", err)
	}
	if len(items[0].Labels) != 0 {
		t.Errorf("expected no labels, got: %v", items[0].Labels)
	}
}

// TestValidate_ValidTypesFromDeclaration proves that validTypes derives from the
// create.json declaration — not a hardcoded map. This kills the duplication.
// The test verifies that ALL types from create.json are accepted, and that an
// unknown type is rejected. The exact set is not hardcoded here — it comes from
// the declaration, proving the duplication is dead.
func TestValidate_ValidTypesFromDeclaration(t *testing.T) {
	// Build a template with each type from the declaration — all should pass.
	knownTypes := []string{"task", "decision", "review", "reminder", "deadline", "prep", "message", "directive"}
	for _, ty := range knownTypes {
		itemJSON := []byte(fmt.Sprintf(`[{"title":"T","type":"%s","priority":"p1","deps":[]}]`, ty))
		_, err := playbook.Parse("decl-type-"+ty, "Test", "", itemJSON)
		if err != nil {
			t.Errorf("type %q from declaration should be valid, got error: %v", ty, err)
		}
	}

	// An unknown type must be rejected.
	itemJSON := []byte(`[{"title":"T","type":"unknown-type","priority":"p1","deps":[]}]`)
	_, err := playbook.Parse("decl-type-unknown", "Test", "", itemJSON)
	if err == nil {
		t.Fatal("expected error for type not in declaration")
	}
}

// TestValidate_LabelInvalidSecondItem checks that the error names the correct item index.
func TestValidate_LabelInvalidSecondItem(t *testing.T) {
	itemJSON := []byte(`[
		{"title":"First","type":"task","priority":"p1","deps":[],"labels":["good"]},
		{"title":"Second","type":"task","priority":"p1","deps":[],"labels":["Bad_Label"]}
	]`)
	_, err := playbook.Parse("lbl-second", "Test", "", itemJSON)
	if err == nil {
		t.Fatal("expected error for invalid label on second item")
	}
	if !contains(err.Error(), "item[1]") {
		t.Errorf("error should name item[1], got: %v", err)
	}
}

// contains is a helper for string substring checks.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		indexString(s, substr) >= 0)
}

func indexString(s, substr string) int {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return i
		}
	}
	return -1
}

// TestValidate_LargePlaybook checks a playbook with many items.
func TestValidate_LargePlaybook(t *testing.T) {
	// Build a large playbook with 20 items in sequence.
	items := make([]map[string]interface{}, 20)
	for i := 0; i < 20; i++ {
		deps := []int{}
		if i > 0 {
			deps = []int{i - 1}
		}
		items[i] = map[string]interface{}{
			"title":    fmt.Sprintf("Item %d", i),
			"type":     "task",
			"priority": "p1",
			"deps":     deps,
		}
	}
	data, _ := json.Marshal(items)
	tmpl, err := playbook.Parse("large", "Large", "", data)
	if err != nil {
		t.Fatalf("large playbook failed: %v", err)
	}
	if len(tmpl.Items) != 20 {
		t.Errorf("expected 20 items, got %d", len(tmpl.Items))
	}
}

// TestParseFull_InvalidJSON checks ParseFull with invalid JSON.
func TestParseFull_InvalidJSON(t *testing.T) {
	_, err := playbook.ParseFull([]byte(`not valid json`))
	if err == nil {
		t.Fatal("expected error for invalid JSON")
	}
}

// TestParseFull_MissingField checks ParseFull with missing required field.
func TestParseFull_MissingField(t *testing.T) {
	data := []byte(`{"id":"test","title":"Test"}`) // missing items
	_, err := playbook.ParseFull(data)
	if err == nil {
		t.Fatal("expected error for missing items field")
	}
}

// TestParseFull_RoundTrip does a full round-trip: Parse → ItemsJSON → ParseFull.
func TestParseFull_RoundTrip(t *testing.T) {
	tmpl, err := playbook.Parse("round", "Round", "Test description", sampleItemsJSON)
	if err != nil {
		t.Fatalf("initial parse failed: %v", err)
	}
	itemsData, err := tmpl.ItemsJSON()
	if err != nil {
		t.Fatalf("ItemsJSON failed: %v", err)
	}
	fullData, _ := json.Marshal(map[string]interface{}{
		"id":          tmpl.ID,
		"title":       tmpl.Title,
		"description": tmpl.Description,
		"items":       json.RawMessage(itemsData),
	})
	tmpl2, err := playbook.ParseFull(fullData)
	if err != nil {
		t.Fatalf("ParseFull failed: %v", err)
	}
	if tmpl2.ID != tmpl.ID || tmpl2.Title != tmpl.Title || tmpl2.Description != tmpl.Description {
		t.Errorf("round-trip metadata mismatch")
	}
	if len(tmpl2.Items) != len(tmpl.Items) {
		t.Errorf("round-trip item count mismatch: %d != %d", len(tmpl2.Items), len(tmpl.Items))
	}
}
