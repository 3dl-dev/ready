package main

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// ready-b878: rd update --parent-id is a card-only field edit (same shape as
// title/priority/eta/due/level) that sets Item.ParentID, giving a triage pass a
// way to adopt an EXISTING item into an epic — which rd dep add (a blocks edge,
// not a parent_id write) never did. These tests prove the write survives a LATER
// fold (not just the in-process item returned by the write call), that the
// orphan-rate metric ready-8da's done condition depends on is actually drivable,
// and that reparenting a currently-BLOCKED item never burns in a derived status
// (constraint 1 from the item's ruling — the ready-500/ready-e0e defect class):
// runUpdateNostr's field block only ever calls publishItemCardEditNostr (a
// card-only edit, no NIP-34 status event), so item.Status keeps being recomputed
// fresh by applyDepAndGateStatus on every fold regardless of what the card's
// stale "s" tag says.

// TestNostrNative_Reparent_ParentIDSurvivesRefold proves a reparent is not just
// an in-memory mutation on the object runUpdateNostr happened to touch: a FRESH
// projection built by re-reading the whole persisted log (nostrProjectAllItems,
// which calls log.ReadAll() + rdSync.ProjectItems with no caching — see
// cmd/rd/nostr.go's nostrProjectAllItems) still returns the new ParentID.
func TestNostrNative_Reparent_ParentIDSurvivesRefold(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	epic, err := runCreateNostr(dir, nostrCreateSpec{title: "Epic", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	orphan, err := runCreateNostr(dir, nostrCreateSpec{title: "Orphan", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create orphan: %v", err)
	}

	// Sanity: freshly created with no --parent-id, the item IS an orphan.
	before, err := nostrResolveItem(orphan)
	if err != nil {
		t.Fatalf("resolve before reparent: %v", err)
	}
	if before.ParentID != "" {
		t.Fatalf("freshly created item has ParentID=%q; want empty (orphan) before the reparent under test", before.ParentID)
	}

	if err := runUpdateNostr(orphan, nostrUpdateSpec{parentID: epic, hasFieldUpdate: true}); err != nil {
		t.Fatalf("reparent update: %v", err)
	}

	// re-fold: a SEPARATE call to nostrProjectAllItems, replaying the whole log
	// from disk again — not the item object the write call above touched.
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("re-fold nostrProjectAllItems: %v", err)
	}
	refolded, ok := byID[orphan]
	if !ok {
		t.Fatalf("re-fold: item %s not found in fresh projection", orphan)
	}
	if refolded.ParentID != epic {
		t.Fatalf("after reparent + re-fold: ParentID = %q; want %q (the fold must return the new parent, not just the write call's in-memory item)", refolded.ParentID, epic)
	}
	assertNoDotCf(t)
}

// TestNostrNative_Reparent_DrivesOrphanCountMetric is ready-8da's actual done
// condition made concrete: an orphan-rate metric computed purely off ParentID
// (== "") must be able to move via `rd update --parent-id`, unlike `rd dep add`
// (a Blocks edge that renders as a navigable tree but never touches ParentID —
// see ready-8da's first pass: 38 items wired via dep add, metric unchanged).
func TestNostrNative_Reparent_DrivesOrphanCountMetric(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	orphanCount := func() int {
		_, byID, err := nostrProjectAllItems()
		if err != nil {
			t.Fatalf("nostrProjectAllItems: %v", err)
		}
		n := 0
		for _, it := range byID {
			if it.ParentID == "" {
				n++
			}
		}
		return n
	}

	epic, err := runCreateNostr(dir, nostrCreateSpec{title: "Epic", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	a, err := runCreateNostr(dir, nostrCreateSpec{title: "A", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	b, err := runCreateNostr(dir, nostrCreateSpec{title: "B", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create B: %v", err)
	}

	// Three items, all currently orphans (epic included — nothing parents it).
	before := orphanCount()
	if before != 3 {
		t.Fatalf("orphan count before any reparent = %d; want 3 (epic, A, B all parentless)", before)
	}

	// rd dep add wires a Blocks edge, NOT a parent_id write (cmd/rd/dep.go's
	// tree-render treats it as an equivalent child for DISPLAY only) — this must
	// NOT move the metric, proving the pre-ready-b878 workaround's failure mode.
	if err := runDepAddNostr(a, epic); err != nil {
		t.Fatalf("dep add (workaround path): %v", err)
	}
	afterDepAdd := orphanCount()
	if afterDepAdd != before {
		t.Fatalf("orphan count after rd dep add = %d; want unchanged at %d (dep add is a Blocks edge, not a parent_id write)", afterDepAdd, before)
	}

	// The actual fix: rd update --parent-id DOES move the metric.
	if err := runUpdateNostr(a, nostrUpdateSpec{parentID: epic, hasFieldUpdate: true}); err != nil {
		t.Fatalf("reparent A under epic: %v", err)
	}
	afterReparentA := orphanCount()
	if afterReparentA != before-1 {
		t.Fatalf("orphan count after reparenting A = %d; want %d (one fewer orphan)", afterReparentA, before-1)
	}

	if err := runUpdateNostr(b, nostrUpdateSpec{parentID: epic, hasFieldUpdate: true}); err != nil {
		t.Fatalf("reparent B under epic: %v", err)
	}
	afterReparentB := orphanCount()
	if afterReparentB != before-2 {
		t.Fatalf("orphan count after reparenting A and B = %d; want %d", afterReparentB, before-2)
	}
	assertNoDotCf(t)
}

// TestNostrNative_Reparent_BlockedItemStatusUnchangedThenRecovers is constraint
// 1's exact failure mode from the item's ruling: ready-500 (open) records that
// runUpdateNostr already republishes item.Status verbatim on OTHER field edits;
// ready-e0e just fixed the identical class of bug in runRejectNostr (a status
// EVENT persisting "blocked" as a derived value forever). This test proves the
// reparent field edit does NOT introduce a second instance:
//  1. reparenting a currently-blocked item leaves its status "blocked" (visibly
//     unchanged immediately after the write + a re-fold), and
//  2. once the real blocker closes, a LATER re-fold recovers the item back to
//     its pre-block status — it is not stuck, because the card-only edit
//     publishItemCardEditNostr never emits a NIP-34 status EVENT, so the base
//     status chain (which is what applyDepAndGateStatus's dep pass is layered
//     on top of) was never touched by the reparent.
func TestNostrNative_Reparent_BlockedItemStatusUnchangedThenRecovers(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	epic, err := runCreateNostr(dir, nostrCreateSpec{title: "Epic", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	blocker, err := runCreateNostr(dir, nostrCreateSpec{title: "Blocker", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	blocked, err := runCreateNostr(dir, nostrCreateSpec{title: "Blocked", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create blocked: %v", err)
	}

	// blocked item's base status before any blocking, so the recovery assertion
	// below has a known target instead of an assumed constant.
	baseline, err := nostrResolveItem(blocked)
	if err != nil {
		t.Fatalf("resolve baseline: %v", err)
	}
	baseStatus := baseline.Status
	if baseStatus == state.StatusBlocked {
		t.Fatalf("test setup invalid: baseline status is already %q before any dep is wired", baseStatus)
	}

	if err := runDepAddNostr(blocked, blocker); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	it, err := nostrResolveItem(blocked)
	if err != nil {
		t.Fatalf("resolve after dep add: %v", err)
	}
	if it.Status != state.StatusBlocked {
		t.Fatalf("status after dep add = %q; want %q (test setup requires the item to actually be blocked)", it.Status, state.StatusBlocked)
	}

	// The mutation under test: reparent the BLOCKED item.
	if err := runUpdateNostr(blocked, nostrUpdateSpec{parentID: epic, hasFieldUpdate: true}); err != nil {
		t.Fatalf("reparent blocked item: %v", err)
	}

	// Re-fold (fresh log replay) immediately after the reparent: status must be
	// UNCHANGED (still blocked — constraint 1's "never introduce a second
	// burn-in" requirement means the reparent must not clear it early either;
	// blocked is still true, the blocker hasn't closed) and ParentID must have
	// moved.
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("re-fold after reparent: %v", err)
	}
	afterReparent := byID[blocked]
	if afterReparent == nil {
		t.Fatalf("re-fold: blocked item %s not found", blocked)
	}
	if afterReparent.Status != state.StatusBlocked {
		t.Fatalf("status after reparenting a blocked item = %q; want unchanged %q", afterReparent.Status, state.StatusBlocked)
	}
	if afterReparent.ParentID != epic {
		t.Fatalf("ParentID after reparenting a blocked item = %q; want %q — a blocked item must still be reparentable", afterReparent.ParentID, epic)
	}

	// Close the real blocker.
	if err := runCloseNostr(blocker, "done", "unblocking", "closed"); err != nil {
		t.Fatalf("close blocker: %v", err)
	}

	// Re-fold again (constraint 1's actual failure mode: a write path that
	// persisted "blocked" as a STATUS EVENT would leave the item stuck here
	// forever, since applyDepAndGateStatus only ever ADDS blocked from live
	// non-terminal blockers — it never clears a status baked into the event
	// chain itself). The reparent must NOT have caused this: recovery proves
	// the field-edit-only path never touched the base status chain.
	_, byID, err = nostrProjectAllItems()
	if err != nil {
		t.Fatalf("re-fold after blocker closes: %v", err)
	}
	recovered := byID[blocked]
	if recovered == nil {
		t.Fatalf("re-fold: blocked item %s not found after blocker closed", blocked)
	}
	if recovered.Status == state.StatusBlocked {
		t.Fatalf("status after the blocker closed = %q; item is STUCK — this is exactly constraint 1's failure mode (a derived status burned in as permanent)", recovered.Status)
	}
	if recovered.Status != baseStatus {
		t.Fatalf("status after recovery = %q; want it back to the pre-block baseline %q", recovered.Status, baseStatus)
	}
	// The reparent must have survived the whole sequence, not just the
	// immediately-after-write fold.
	if recovered.ParentID != epic {
		t.Fatalf("ParentID after the blocker closes = %q; want %q — the reparent must persist across further, unrelated folds", recovered.ParentID, epic)
	}
	assertNoDotCf(t)
}

// TestNostrNative_Reparent_SelfParentRejected proves the CLI-layer guard added
// in cmd/rd/update.go: --parent-id cannot name the item itself. Runs the REAL
// updateCmd (not a mirrored copy of the guard) so the test rots if the guard is
// ever removed from update.go, not just if this test's own copy drifts.
func TestNostrNative_Reparent_SelfParentRejected(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	id, err := runCreateNostr(dir, nostrCreateSpec{title: "Solo", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rootCmd.SetArgs([]string{"update", id, "--parent-id", id})
	err = rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected an error when --parent-id names the item itself, got nil")
	}
	if !containsStr(err.Error(), "cannot be the item itself") {
		t.Fatalf("expected the self-parent guard error, got: %v", err)
	}

	// The rejected update must not have written anything: ParentID stays empty.
	it, err := nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve after rejected self-parent: %v", err)
	}
	if it.ParentID != "" {
		t.Fatalf("ParentID after a rejected self-parent update = %q; want empty (no write should have happened)", it.ParentID)
	}
	assertNoDotCf(t)
}

// ready-ca3: the opus adversary on ready-b878/PR#159 found three input-handling
// gaps in --parent-id, reported rather than absorbed into that PR: (a) an
// unknown --parent-id was accepted SILENTLY, leaving the item orphaned worse
// than before while ready-8da's ParentID-based orphan metric read it as
// "adopted"; (b) --parent-id none stored the literal string "none", printing
// as `Parent:   none` — visually identical to no parent; (c) there was no way
// to clear a parent at all. The tests below prove the fix: unknown ids are
// rejected by name, the orphan count provably does not move on a rejected
// reparent, "none" clears instead of storing verbatim, and rd create
// validates identically to rd update.

// TestNostrNative_Update_UnknownParentIDRejected is ready-ca3(a): a
// --parent-id naming an item that does not exist in the LIVE nostr projection
// must be rejected with an error naming the missing id, not silently accepted.
func TestNostrNative_Update_UnknownParentIDRejected(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	orphan, err := runCreateNostr(dir, nostrCreateSpec{title: "Orphan", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create orphan: %v", err)
	}

	const typoedEpicID = "ready-doesnotexist"
	err = runUpdateNostr(orphan, nostrUpdateSpec{parentID: typoedEpicID, hasFieldUpdate: true})
	if err == nil {
		t.Fatalf("expected an error reparenting to a nonexistent id %q, got nil", typoedEpicID)
	}
	if !containsStr(err.Error(), typoedEpicID) {
		t.Fatalf("error %q does not name the missing id %q", err.Error(), typoedEpicID)
	}

	// The rejected update must not have written anything: ParentID stays empty,
	// not the unknown id.
	it, err := nostrResolveItem(orphan)
	if err != nil {
		t.Fatalf("resolve after rejected reparent: %v", err)
	}
	if it.ParentID != "" {
		t.Fatalf("ParentID after a rejected reparent = %q; want empty — the write must not have happened", it.ParentID)
	}
	assertNoDotCf(t)
}

// TestNostrNative_Update_UnknownParentIDDoesNotMoveOrphanCount is the actual
// point of ready-ca3(a) made concrete, mirroring
// TestNostrNative_Reparent_DrivesOrphanCountMetric's real-adoption case: a
// typo'd --parent-id must leave the ParentID-based orphan count (ready-8da's
// done condition) EXACTLY where it was — not improved, not worsened. Before
// the fix, the typo'd id was stored verbatim, which DID move (improve) the
// metric while actually leaving the item orphaned worse than before (pointing
// at nothing instead of "").
func TestNostrNative_Update_UnknownParentIDDoesNotMoveOrphanCount(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	orphanCount := func() int {
		_, byID, err := nostrProjectAllItems()
		if err != nil {
			t.Fatalf("nostrProjectAllItems: %v", err)
		}
		n := 0
		for _, it := range byID {
			if it.ParentID == "" {
				n++
			}
		}
		return n
	}

	a, err := runCreateNostr(dir, nostrCreateSpec{title: "A", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create A: %v", err)
	}
	before := orphanCount()
	if before != 1 {
		t.Fatalf("orphan count before any reparent = %d; want 1", before)
	}

	if err := runUpdateNostr(a, nostrUpdateSpec{parentID: "ready-nonexistent-epic", hasFieldUpdate: true}); err == nil {
		t.Fatalf("expected the reparent to a nonexistent parent to be rejected, got nil error")
	}

	after := orphanCount()
	if after != before {
		t.Fatalf("orphan count after a REJECTED reparent = %d; want unchanged at %d — a typo must not move the metric", after, before)
	}
	assertNoDotCf(t)
}

// TestNostrNative_Update_ParentIDNoneClearsParent is ready-ca3(b)+(c): "none"
// (case-insensitive, whitespace-trimmed) is the documented spelling that
// clears ParentID back to "" (orphan) rather than being stored as a literal
// dangling string. The clear survives a re-fold (a fresh log replay), proving
// it is a real card-only field edit and not just an in-memory mutation.
func TestNostrNative_Update_ParentIDNoneClearsParent(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	epic, err := runCreateNostr(dir, nostrCreateSpec{title: "Epic", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	child, err := runCreateNostr(dir, nostrCreateSpec{title: "Child", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create child: %v", err)
	}

	if err := runUpdateNostr(child, nostrUpdateSpec{parentID: epic, hasFieldUpdate: true}); err != nil {
		t.Fatalf("reparent under epic: %v", err)
	}
	it, err := nostrResolveItem(child)
	if err != nil {
		t.Fatalf("resolve after reparent: %v", err)
	}
	if it.ParentID != epic {
		t.Fatalf("ParentID after reparent = %q; want %q (test setup)", it.ParentID, epic)
	}

	// Clear it: whitespace and case variants of the sentinel must all work.
	for _, spelling := range []string{"none", "NONE", " None "} {
		if err := runUpdateNostr(child, nostrUpdateSpec{parentID: spelling, hasFieldUpdate: true}); err != nil {
			t.Fatalf("clear parent with spelling %q: %v", spelling, err)
		}
		it, err := nostrResolveItem(child)
		if err != nil {
			t.Fatalf("resolve after clearing with spelling %q: %v", spelling, err)
		}
		if it.ParentID != "" {
			t.Fatalf("ParentID after --parent-id %q = %q; want empty (cleared), NOT the literal sentinel string", spelling, it.ParentID)
		}
		// Re-parent for the next spelling in the loop.
		if err := runUpdateNostr(child, nostrUpdateSpec{parentID: epic, hasFieldUpdate: true}); err != nil {
			t.Fatalf("re-reparent before next spelling: %v", err)
		}
	}

	// Final clear, then prove it survives a FRESH fold (separate
	// nostrProjectAllItems call, replaying the whole log from disk), not just
	// the in-process item the write call touched.
	if err := runUpdateNostr(child, nostrUpdateSpec{parentID: "none", hasFieldUpdate: true}); err != nil {
		t.Fatalf("final clear: %v", err)
	}
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("re-fold: %v", err)
	}
	refolded, ok := byID[child]
	if !ok {
		t.Fatalf("re-fold: item %s not found", child)
	}
	if refolded.ParentID != "" {
		t.Fatalf("ParentID after clearing + re-fold = %q; want empty — the clear must persist, not just live in-memory", refolded.ParentID)
	}
	assertNoDotCf(t)
}

// TestNostrNative_Update_ParentIDNoneRejectedAsLiteral is the counterpart to
// the clear test: prove the literal string "none" is NEVER visible as a
// stored ParentID anywhere an item resolves — the one behavior ready-ca3(b)
// explicitly forbids, since `rd show` printing `Parent:   none` was
// indistinguishable from having no parent while actually being a dangling
// pointer to a nonexistent item.
func TestNostrNative_Update_ParentIDNoneRejectedAsLiteral(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	orphan, err := runCreateNostr(dir, nostrCreateSpec{title: "Orphan", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := runUpdateNostr(orphan, nostrUpdateSpec{parentID: "none", hasFieldUpdate: true}); err != nil {
		t.Fatalf("update --parent-id none: %v", err)
	}
	it, err := nostrResolveItem(orphan)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if it.ParentID == "none" {
		t.Fatalf("ParentID stored the literal string %q — ready-ca3(b)'s exact defect: this reads identically to no parent in `rd show` while being a dangling pointer", it.ParentID)
	}
	if it.ParentID != "" {
		t.Fatalf("ParentID after --parent-id none = %q; want empty", it.ParentID)
	}
}

// TestNostrNative_Create_UnknownParentIDRejected is the parity half of
// ready-ca3's done condition: `rd create --parent-id` must validate IDENTICALLY
// to `rd update --parent-id`, not diverge (before this fix, create performed
// no validation at all — cmd/rd/create.go's parentID flowed straight into the
// item struct).
func TestNostrNative_Create_UnknownParentIDRejected(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	const typoedEpicID = "ready-doesnotexist"
	id, err := runCreateNostr(dir, nostrCreateSpec{title: "Child", itemType: "task", priority: "p2", parentID: typoedEpicID})
	if err == nil {
		t.Fatalf("expected create with a nonexistent --parent-id to be rejected, got id %q", id)
	}
	if !containsStr(err.Error(), typoedEpicID) {
		t.Fatalf("error %q does not name the missing id %q", err.Error(), typoedEpicID)
	}
}

// TestNostrNative_Create_ParentIDNoneMeansNoParent proves rd create treats the
// same "none" sentinel the same way rd update does: no parent, not a literal
// stored string — the two commands agree instead of diverging.
func TestNostrNative_Create_ParentIDNoneMeansNoParent(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	id, err := runCreateNostr(dir, nostrCreateSpec{title: "Solo", itemType: "task", priority: "p2", parentID: "none"})
	if err != nil {
		t.Fatalf("create with --parent-id none: %v", err)
	}
	it, err := nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if it.ParentID != "" {
		t.Fatalf("ParentID after create --parent-id none = %q; want empty, matching rd update's clear semantics", it.ParentID)
	}
}

// TestNostrNative_Create_ValidParentIDAccepted is the create-side positive
// case matching TestNostrNative_Reparent_ParentIDSurvivesRefold: a
// --parent-id naming a real, existing item is stored and survives a re-fold.
func TestNostrNative_Create_ValidParentIDAccepted(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	epic, err := runCreateNostr(dir, nostrCreateSpec{title: "Epic", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create epic: %v", err)
	}
	child, err := runCreateNostr(dir, nostrCreateSpec{title: "Child", itemType: "task", priority: "p2", parentID: epic})
	if err != nil {
		t.Fatalf("create child under valid parent %q: %v", epic, err)
	}
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("re-fold: %v", err)
	}
	refolded, ok := byID[child]
	if !ok {
		t.Fatalf("re-fold: item %s not found", child)
	}
	if refolded.ParentID != epic {
		t.Fatalf("ParentID after create + re-fold = %q; want %q", refolded.ParentID, epic)
	}
}

// TestNostrNative_UpdateCmd_UnknownParentIDRejected runs the REAL updateCmd
// (not a mirrored copy of the validation), the same style as
// TestNostrNative_Reparent_SelfParentRejected, so this test rots if the CLI
// wiring to runUpdateNostr / nostrUpdateSpec.parentID is ever broken, not just
// if a copy of the check drifts.
func TestNostrNative_UpdateCmd_UnknownParentIDRejected(t *testing.T) {
	setupNostrNativeProject(t)
	dir := mustDir(t)

	orphan, err := runCreateNostr(dir, nostrCreateSpec{title: "Orphan", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	rootCmd.SetArgs([]string{"update", orphan, "--parent-id", "ready-nonexistent"})
	err = rootCmd.Execute()
	if err == nil {
		t.Fatalf("expected an error updating --parent-id to a nonexistent id via the real CLI, got nil")
	}
	if !containsStr(err.Error(), "ready-nonexistent") {
		t.Fatalf("expected the missing id named in the error, got: %v", err)
	}

	it, err := nostrResolveItem(orphan)
	if err != nil {
		t.Fatalf("resolve after rejected CLI update: %v", err)
	}
	if it.ParentID != "" {
		t.Fatalf("ParentID after a rejected CLI reparent = %q; want empty", it.ParentID)
	}
	assertNoDotCf(t)
}
