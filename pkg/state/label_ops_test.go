package state_test

// label_ops_test.go — regression coverage for the STILL-WIRED label-derivation
// handlers on the msgrec DeriveAll replay path (ready-cb6 veracity fix).
//
// handleWorkLabelAdd / handleWorkLabelRemove and the buildLabelRegistry
// define-overlay are reachable live via DeriveFromJSONLWithCampfire -> Derive ->
// DeriveAll (used by pkg/resolve + `rd nostr publish` when replaying a
// mutations.jsonl). Their store-backed tests were deleted in the store->msgrec
// flip; these port the same done-conditions onto real msgrec.MessageRecord
// replay, so the handlers are no longer 0% covered.
//
// Done conditions (from the deleted label_ops/label_registry tests):
//   1. label-add of a seed label -> Item.Labels contains it, no warnings
//   2. add-then-remove -> label absent
//   3. remove-then-add -> label present (add after remove)
//   4. label-add of an unregistered (pattern-valid) atom -> dropped + LabelWarning
//   5. label-add of a malformed atom -> dropped + LabelWarning, no panic
//   6. label-add targeting a nonexistent item -> DeriveResult warning, no phantom item
//   7. work:label-define overlays the registry so a later label-add of the
//      user-defined atom is admitted (buildLabelRegistry define-path)

import (
	"strings"
	"testing"

	msgrec "github.com/3dl-dev/ready/pkg/msgrec"
	"github.com/3dl-dev/ready/pkg/state"
)

// mkCreate builds a minimal work:create record for itemID.
func mkCreate(msgID, itemID string, ts int64) msgrec.MessageRecord {
	return makeMsg(msgID, []string{"work:create"}, map[string]interface{}{
		"id": itemID, "title": itemID, "type": "task",
		"for": "baron@3dl.dev", "priority": "p2",
	}, nil, ts)
}

// mkLabelAdd builds a work:label-add record for (itemID, label).
func mkLabelAdd(msgID, itemID, label string, ts int64) msgrec.MessageRecord {
	return makeMsg(msgID, []string{"work:label-add"}, map[string]interface{}{
		"id": itemID, "label": label,
	}, nil, ts)
}

// mkLabelRemove builds a work:label-remove record for (itemID, label).
func mkLabelRemove(msgID, itemID, label string, ts int64) msgrec.MessageRecord {
	return makeMsg(msgID, []string{"work:label-remove"}, map[string]interface{}{
		"id": itemID, "label": label,
	}, nil, ts)
}

// mkLabelDefine builds a work:label-define record adding label to the registry.
func mkLabelDefine(msgID, label, desc string, ts int64) msgrec.MessageRecord {
	return makeMsg(msgID, []string{"work:label-define"}, map[string]interface{}{
		"label": label, "description": desc,
	}, nil, ts)
}

func hasWarningContaining(warnings []string, sub string) bool {
	for _, w := range warnings {
		if strings.Contains(w, sub) {
			return true
		}
	}
	return false
}

// TestLabelAdd_SeedLabel proves handleWorkLabelAdd materializes a seed label
// (bug) on an existing item with no warnings.
func TestLabelAdd_SeedLabel(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		mkCreate("c1", "ready-l01", ts),
		mkLabelAdd("la1", "ready-l01", "bug", ts+1000),
	}
	res := state.DeriveAll(testCampfire, msgs)
	item := res.Items()["ready-l01"]
	if item == nil {
		t.Fatal("item ready-l01 not found")
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bug" {
		t.Fatalf("Labels=%v, want [bug]", item.Labels)
	}
	if len(item.LabelWarnings) != 0 {
		t.Fatalf("unexpected LabelWarnings for valid seed add: %v", item.LabelWarnings)
	}
}

// TestLabelAdd_ThenRemove proves handleWorkLabelRemove removes a previously added
// label.
func TestLabelAdd_ThenRemove(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		mkCreate("c1", "ready-l02", ts),
		mkLabelAdd("la1", "ready-l02", "bug", ts+1000),
		mkLabelRemove("lr1", "ready-l02", "bug", ts+2000),
	}
	res := state.DeriveAll(testCampfire, msgs)
	item := res.Items()["ready-l02"]
	if item == nil {
		t.Fatal("item ready-l02 not found")
	}
	if len(item.Labels) != 0 {
		t.Fatalf("Labels=%v, want [] after remove", item.Labels)
	}
}

// TestLabelRemove_ThenAdd proves remove-then-add yields the label present
// (remove of an absent label is a no-op, subsequent add applies).
func TestLabelRemove_ThenAdd(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		mkCreate("c1", "ready-l03", ts),
		mkLabelRemove("lr1", "ready-l03", "bug", ts+1000),
		mkLabelAdd("la1", "ready-l03", "bug", ts+2000),
	}
	res := state.DeriveAll(testCampfire, msgs)
	item := res.Items()["ready-l03"]
	if item == nil {
		t.Fatal("item ready-l03 not found")
	}
	if len(item.Labels) != 1 || item.Labels[0] != "bug" {
		t.Fatalf("Labels=%v, want [bug] after remove-then-add", item.Labels)
	}
}

// TestLabelAdd_Unregistered proves a pattern-valid but unregistered label is
// DROPPED at derive with a LabelWarning (read-side registry gate).
func TestLabelAdd_Unregistered(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		mkCreate("c1", "ready-l04", ts),
		mkLabelAdd("la1", "ready-l04", "wontfix", ts+1000), // valid pattern, not seeded
	}
	res := state.DeriveAll(testCampfire, msgs)
	item := res.Items()["ready-l04"]
	if item == nil {
		t.Fatal("item ready-l04 not found")
	}
	if len(item.Labels) != 0 {
		t.Fatalf("Labels=%v, want [] (unregistered label dropped)", item.Labels)
	}
	if !hasWarningContaining(item.LabelWarnings, "not in label registry") {
		t.Fatalf("LabelWarnings=%v, want a 'not in label registry' warning", item.LabelWarnings)
	}
}

// TestLabelAdd_Malformed proves a pattern-invalid label is dropped with a
// LabelWarning and no panic.
func TestLabelAdd_Malformed(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		mkCreate("c1", "ready-l05", ts),
		mkLabelAdd("la1", "ready-l05", "Bad_Label", ts+1000), // uppercase + underscore
	}
	res := state.DeriveAll(testCampfire, msgs)
	item := res.Items()["ready-l05"]
	if item == nil {
		t.Fatal("item ready-l05 not found")
	}
	if len(item.Labels) != 0 {
		t.Fatalf("Labels=%v, want [] (malformed label dropped)", item.Labels)
	}
	if !hasWarningContaining(item.LabelWarnings, "fails pattern validation") {
		t.Fatalf("LabelWarnings=%v, want a 'fails pattern validation' warning", item.LabelWarnings)
	}
}

// TestLabelAdd_NonexistentItem proves a label-add targeting an unknown item is
// recorded as a DeriveResult warning, creates no phantom item, and does not panic.
func TestLabelAdd_NonexistentItem(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		mkCreate("c1", "ready-l06", ts),
		mkLabelAdd("la1", "ready-ghost", "bug", ts+1000),
	}
	res := state.DeriveAll(testCampfire, msgs)
	if _, ok := res.Items()["ready-ghost"]; ok {
		t.Fatal("phantom item ready-ghost created from label-add on nonexistent item")
	}
	if !hasWarningContaining(res.Warnings(), "nonexistent item") {
		t.Fatalf("DeriveResult.Warnings()=%v, want a 'nonexistent item' warning", res.Warnings())
	}
}

// TestLabelDefine_OverlayAdmitsAdd proves the buildLabelRegistry define-overlay:
// a work:label-define adds a user atom to the registry so a subsequent label-add
// of that atom is ADMITTED (not dropped), and appears in DeriveResult.LabelRegistry.
func TestLabelDefine_OverlayAdmitsAdd(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		mkLabelDefine("ld1", "custom-tag", "A user-defined atom", ts),
		mkCreate("c1", "ready-l07", ts+1000),
		mkLabelAdd("la1", "ready-l07", "custom-tag", ts+2000),
	}
	res := state.DeriveAll(testCampfire, msgs)
	if _, ok := res.LabelRegistry()["custom-tag"]; !ok {
		t.Fatal("custom-tag absent from LabelRegistry — label-define overlay did not apply")
	}
	item := res.Items()["ready-l07"]
	if item == nil {
		t.Fatal("item ready-l07 not found")
	}
	if len(item.Labels) != 1 || item.Labels[0] != "custom-tag" {
		t.Fatalf("Labels=%v, want [custom-tag] (defined atom admitted)", item.Labels)
	}
	if len(item.LabelWarnings) != 0 {
		t.Fatalf("unexpected LabelWarnings for a defined atom: %v", item.LabelWarnings)
	}
}
