package state_test

import (
	"testing"

	"github.com/campfire-net/ready/pkg/storetest"
)

// TestLabelRegistry_SeedAtomsAlwaysPresent verifies that the six seed label atoms
// are present in every campfire's registry, even with an empty message log.
func TestLabelRegistry_SeedAtomsAlwaysPresent(t *testing.T) {
	h := storetest.New(t)
	result := h.DeriveAll()
	reg := result.LabelRegistry()

	seedNames := []string{"bug", "feature", "question", "security", "sweep-finding", "blog-candidate"}
	for _, name := range seedNames {
		def, ok := reg[name]
		if !ok {
			t.Errorf("seed label %q not found in registry", name)
			continue
		}
		if def.DefinedBy != "seed" {
			t.Errorf("seed label %q has DefinedBy=%q, want \"seed\"", name, def.DefinedBy)
		}
		if def.DefinedAt != 0 {
			t.Errorf("seed label %q has DefinedAt=%d, want 0", name, def.DefinedAt)
		}
	}
}

// TestLabelRegistry_UserDefinedAtomAdded verifies that a work:label-define message
// adds a new atom to the registry on top of the seed atoms.
func TestLabelRegistry_UserDefinedAtomAdded(t *testing.T) {
	h := storetest.New(t)
	h.LabelDefine("my-label", "A custom label")

	result := h.DeriveAll()
	reg := result.LabelRegistry()

	def, ok := reg["my-label"]
	if !ok {
		t.Fatal("user-defined label 'my-label' not found in registry")
	}
	if def.Name != "my-label" {
		t.Errorf("Name=%q, want 'my-label'", def.Name)
	}
	if def.Description != "A custom label" {
		t.Errorf("Description=%q, want 'A custom label'", def.Description)
	}
	if def.DefinedBy != storetest.DefaultSender {
		t.Errorf("DefinedBy=%q, want %q", def.DefinedBy, storetest.DefaultSender)
	}
	if def.DefinedAt == 0 {
		t.Error("DefinedAt is 0 for user-defined label, want non-zero timestamp")
	}
}

// TestLabelRegistry_SeedPlusTwoDefinedLabels verifies the done condition:
// a log with seed atoms + 2 user-defined labels yields a registry of 8 atoms.
func TestLabelRegistry_SeedPlusTwoDefinedLabels(t *testing.T) {
	h := storetest.New(t)
	h.LabelDefine("my-label-a", "First custom label")
	h.LabelDefine("my-label-b", "Second custom label")

	result := h.DeriveAll()
	reg := result.LabelRegistry()

	// 6 seeds + 2 defined = 8
	if len(reg) != 8 {
		t.Errorf("registry has %d atoms, want 8 (6 seeds + 2 defined)", len(reg))
		for k := range reg {
			t.Logf("  %s", k)
		}
	}
}

// TestLabelRegistry_LaterDefinitionWins verifies that when two work:label-define
// messages exist for the same label name, the later one (by timestamp) wins.
func TestLabelRegistry_LaterDefinitionWins(t *testing.T) {
	h := storetest.New(t)
	h.LabelDefine("my-label", "First description")
	h2 := h.WithSender("other-sender")
	h2.LabelDefine("my-label", "Second description")

	result := h.DeriveAll()
	reg := result.LabelRegistry()

	def, ok := reg["my-label"]
	if !ok {
		t.Fatal("'my-label' not found in registry")
	}
	if def.Description != "Second description" {
		t.Errorf("Description=%q, want 'Second description' (later message wins)", def.Description)
	}
}

// TestLabelRegistry_RetroactiveLegitimization verifies the demand-then-promote flow:
// a label-define message legitimizes earlier uses retroactively. The registry contains
// the atom regardless of whether the define message appears before or after use.
func TestLabelRegistry_RetroactiveLegitimization(t *testing.T) {
	h := storetest.New(t)
	// First define a label — simulate a "demand" scenario where define comes later.
	// Since harness uses auto-incrementing timestamps, this message will always be
	// earlier, but the key point is the registry membership is timestamp-independent.
	h.Create("item-a", "First item", "p2", storetest.WithContext("needs my-label"))
	h.LabelDefine("my-label", "Retroactively defined")

	result := h.DeriveAll()
	reg := result.LabelRegistry()

	if _, ok := reg["my-label"]; !ok {
		t.Error("retroactively defined label 'my-label' not in registry")
	}
}

// TestLabelRegistry_SeedDefinitionPreserved verifies that a user-defined label
// with the same name as a seed atom is preserved (user definition takes over).
func TestLabelRegistry_SeedDefinitionPreserved(t *testing.T) {
	h := storetest.New(t)
	// Redefining "bug" should update the description but the atom stays.
	h.LabelDefine("bug", "Redefined bug description")

	result := h.DeriveAll()
	reg := result.LabelRegistry()

	def, ok := reg["bug"]
	if !ok {
		t.Fatal("'bug' label not found in registry after user redefinition")
	}
	// User definition takes over — DefinedBy is the sender, not "seed".
	if def.DefinedBy == "seed" {
		t.Error("expected user-defined 'bug' to have non-seed DefinedBy after user label-define")
	}
	if def.Description != "Redefined bug description" {
		t.Errorf("Description=%q, want 'Redefined bug description'", def.Description)
	}
}

// TestLabelRegistry_ItemsUnaffected verifies that adding label-define messages
// does not affect work item derivation — items still derive correctly.
func TestLabelRegistry_ItemsUnaffected(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-1", "Test item", "p2")
	h.LabelDefine("custom-tag", "A custom tag for items")

	result := h.DeriveAll()
	items := result.Items()

	if _, ok := items["item-1"]; !ok {
		t.Error("work item 'item-1' not found in derived items after label-define messages")
	}
	if len(items) != 1 {
		t.Errorf("expected 1 item, got %d", len(items))
	}
}

// TestDeriveAll_ItemsMethodMatchesDeriveFromStore verifies that DeriveAll().Items()
// returns the same items as the existing DeriveFromStore call (no regression).
func TestDeriveAll_ItemsMethodMatchesDeriveFromStore(t *testing.T) {
	h := storetest.New(t)
	h.Create("item-a", "Item A", "p1")
	h.Create("item-b", "Item B", "p2")
	h.LabelDefine("my-tag", "A tag")

	result := h.DeriveAll()
	items := result.Items()

	if len(items) != 2 {
		t.Errorf("expected 2 items, got %d", len(items))
	}

	// Cross-check with the legacy DeriveFromStore path.
	legacyItems := h.Derive()
	if len(legacyItems) != len(items) {
		t.Errorf("DeriveAll items count %d != DeriveFromStore count %d", len(items), len(legacyItems))
	}
	for id := range items {
		if _, ok := legacyItems[id]; !ok {
			t.Errorf("item %q in DeriveAll but not in DeriveFromStore", id)
		}
	}
}

// TestLabelRegistry_LabelRegistryAccessorIsStable verifies that the
// LabelRegistry() accessor is available on *state.DeriveResult (interface contract
// consumed by I2/I4 downstream items). This is a compile-time contract test.
func TestLabelRegistry_LabelRegistryAccessorIsStable(t *testing.T) {
	h := storetest.New(t)
	result := h.DeriveAll()

	// If this compiles, the accessor is stable.
	registry := result.LabelRegistry()
	if registry == nil {
		t.Error("LabelRegistry() returned nil map")
	}
}
