package playbook_test

// Integration tests for playbook label → engage → derive pipeline (ready-ef7).
// These tests use pkg/storetest to simulate what rd engage does: create items
// with the label scalar, then Derive to verify state materialization.
// "Full Derive, not a mocked expand" per the done condition.

import (
	"strings"
	"testing"

	"github.com/campfire-net/ready/pkg/playbook"
	"github.com/campfire-net/ready/pkg/storetest"
)

// TestLabelEngage_DeriveHasLabel simulates the full engage → derive pipeline:
// template with labels ["bug"] on an item → validate passes → Expand carries
// the label → storetest simulates work:create with labels → Derive returns
// item with Labels=["bug"].
func TestLabelEngage_DeriveHasLabel(t *testing.T) {
	// 1. Parse template with labels.
	itemJSON := []byte(`[{"title":"Fix the bug","type":"task","priority":"p1","deps":[],"labels":["bug"]}]`)
	tmpl, err := playbook.Parse("eng-bug", "Bugfix", "", itemJSON)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	// 2. Expand: verify the label is present in the ExpandedItem.
	items, err := playbook.Expand(tmpl, "eng", nil)
	if err != nil {
		t.Fatalf("Expand failed: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("expected 1 item, got %d", len(items))
	}
	if len(items[0].Labels) != 1 || items[0].Labels[0] != "bug" {
		t.Fatalf("ExpandedItem.Labels: expected [bug], got %v", items[0].Labels)
	}

	// 3. Simulate engage: write a work:create with labels to the store.
	// storetest.Harness.Create mirrors what engage.go sends.
	// "bug" is a seed label (declared in declarations/seed_labels.json), so
	// DeriveAllFromStore will accept it — no label-define needed.
	h := storetest.New(t)
	h.Create(items[0].ID, items[0].Title, items[0].Priority,
		storetest.WithType(items[0].Type),
		storetest.WithLabels(items[0].Labels...),
	)

	// 4. Full Derive: verify the item has Labels=["bug"].
	derived := h.DeriveAll()
	item, ok := derived.Items()[items[0].ID]
	if !ok {
		t.Fatalf("item %q not found in derived state", items[0].ID)
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bug" {
		t.Errorf("derived item Labels: expected [bug], got %v", item.Labels)
	}
}

// TestLabelEngage_NonRegistryLabelDropped simulates engage with a label not in
// the target campfire registry → derive drops the label, LabelWarnings reports it.
func TestLabelEngage_NonRegistryLabelDropped(t *testing.T) {
	// "my-custom-label" is not a seed label and no label-define is sent.
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":["my-custom-label"]}]`)
	tmpl, err := playbook.Parse("eng-noreg", "NoReg", "", itemJSON)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	items, err := playbook.Expand(tmpl, "eng", nil)
	if err != nil {
		t.Fatalf("Expand failed: %v", err)
	}

	// Simulate engage: send work:create with the label.
	h := storetest.New(t)
	h.Create(items[0].ID, items[0].Title, items[0].Priority,
		storetest.WithType(items[0].Type),
		storetest.WithLabels(items[0].Labels...),
	)

	// Derive: the label is absent from the registry (no seed + no label-define)
	// so it must be dropped and recorded in LabelWarnings.
	derived := h.DeriveAll()
	item, ok := derived.Items()[items[0].ID]
	if !ok {
		t.Fatalf("item %q not found in derived state", items[0].ID)
	}
	// Item must still be created (the drop is silent, not fatal).
	if item == nil {
		t.Fatal("item should be created even with non-registry label")
	}
	// The label must be absent from derived Labels.
	if len(item.Labels) != 0 {
		t.Errorf("expected no derived labels (dropped), got %v", item.Labels)
	}
	// LabelWarnings must mention the dropped label.
	found := false
	for _, w := range item.LabelWarnings {
		if strings.Contains(w, "my-custom-label") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected LabelWarnings to mention 'my-custom-label', got %v", item.LabelWarnings)
	}
}

// TestLabelEngage_LabelDefinedInRegistry checks that a label-defined atom is
// accepted by derive when the work:label-define precedes the work:create.
func TestLabelEngage_LabelDefinedInRegistry(t *testing.T) {
	itemJSON := []byte(`[{"title":"T","type":"task","priority":"p1","deps":[],"labels":["my-custom-label"]}]`)
	tmpl, err := playbook.Parse("eng-regdef", "RegDef", "", itemJSON)
	if err != nil {
		t.Fatalf("Parse failed: %v", err)
	}

	items, err := playbook.Expand(tmpl, "eng", nil)
	if err != nil {
		t.Fatalf("Expand failed: %v", err)
	}

	h := storetest.New(t)
	// Define the label first, then create the item.
	h.LabelDefine("my-custom-label", "A custom label for testing")
	h.Create(items[0].ID, items[0].Title, items[0].Priority,
		storetest.WithType(items[0].Type),
		storetest.WithLabels(items[0].Labels...),
	)

	derived := h.DeriveAll()
	item, ok := derived.Items()[items[0].ID]
	if !ok {
		t.Fatalf("item %q not found in derived state", items[0].ID)
	}
	if len(item.Labels) != 1 || item.Labels[0] != "my-custom-label" {
		t.Errorf("expected Labels=[my-custom-label], got %v", item.Labels)
	}
	if len(item.LabelWarnings) != 0 {
		t.Errorf("expected no LabelWarnings, got %v", item.LabelWarnings)
	}
}
