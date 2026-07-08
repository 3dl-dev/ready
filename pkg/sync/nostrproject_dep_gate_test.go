// Deterministic (no relay) proof for ready-82c: the attention engine (pkg/views,
// consuming pkg/state.Item) computes the SAME readiness set whether items are
// derived from campfire messages (pkg/state's DeriveAll, see
// pkg/state/state_dep_test.go and pkg/state/state_gate_test.go) or projected
// from a nostr event log (ProjectItems, this package). Same scenarios, same
// expected outcomes -- a substrate swap, not a behavior change.
//
// Wire encoding exercised here (ready-a14 DEVIATIONS, nailed down in ready-82c):
//   - dep (rd dep add <blocked> <blocker>) -> repeated "i" tags on the blocked
//     item's 30302 card, one per blocker item ID.
//   - gate (rd gate) -> "gate" / "waiting_type" / "waiting_on" rd-extension tags
//     on the card, plus status=waiting via the "s" tag (or a NIP-34 status
//     event); "GateMsgID" (views.GatesFilter's non-empty check) is derived from
//     the winning card's event id while status=waiting && waiting_type=gate.
package sync

import (
	"testing"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/views"
)

// buildCard is a small test helper: build+sign a card event, fatal on error.
func buildCard(t *testing.T, k *nostr.Key, spec CardSpec, createdAt int64) *nostr.Event {
	t.Helper()
	e, err := BuildCardEvent(k, spec, createdAt)
	if err != nil {
		t.Fatalf("build card %s: %v", spec.ItemID, err)
	}
	return e
}

// TestNostrProjection_DepChain mirrors pkg/state's TestDerive_DepTreeChain: a
// 3-item chain (t01 blocks t02 blocks t03) produces the same BlockedBy/Blocks/
// Status relationships when derived from nostr cards instead of campfire
// work:block messages.
func TestNostrProjection_DepChain(t *testing.T) {
	k := testKey(t)
	events := []*nostr.Event{
		buildCard(t, k, CardSpec{ItemID: "ready-t01", Title: "Step 1", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready"}, 1700000000),
		buildCard(t, k, CardSpec{ItemID: "ready-t02", Title: "Step 2", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready", Deps: []string{"ready-t01"}}, 1700000100),
		buildCard(t, k, CardSpec{ItemID: "ready-t03", Title: "Step 3", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready", Deps: []string{"ready-t02"}}, 1700000200),
	}

	items := ProjectItems(events, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	t01, t02, t03 := items["ready-t01"], items["ready-t02"], items["ready-t03"]
	if t01 == nil || t02 == nil || t03 == nil {
		t.Fatalf("one or more items not projected: t01=%v t02=%v t03=%v", t01, t02, t03)
	}

	if t01.Status == state.StatusBlocked {
		t.Errorf("t01 should not be blocked, got %q", t01.Status)
	}
	if len(t01.Blocks) != 1 || t01.Blocks[0] != "ready-t02" {
		t.Errorf("expected t01.Blocks=[ready-t02], got %v", t01.Blocks)
	}
	if len(t01.BlockedBy) != 0 {
		t.Errorf("expected t01.BlockedBy=[], got %v", t01.BlockedBy)
	}

	if t02.Status != state.StatusBlocked {
		t.Errorf("expected t02 blocked, got %q", t02.Status)
	}
	if len(t02.BlockedBy) != 1 || t02.BlockedBy[0] != "ready-t01" {
		t.Errorf("expected t02.BlockedBy=[ready-t01], got %v", t02.BlockedBy)
	}
	if len(t02.Blocks) != 1 || t02.Blocks[0] != "ready-t03" {
		t.Errorf("expected t02.Blocks=[ready-t03], got %v", t02.Blocks)
	}

	if t03.Status != state.StatusBlocked {
		t.Errorf("expected t03 blocked, got %q", t03.Status)
	}
	if len(t03.BlockedBy) != 1 || t03.BlockedBy[0] != "ready-t02" {
		t.Errorf("expected t03.BlockedBy=[ready-t02], got %v", t03.BlockedBy)
	}

	// Readiness parity: only t01 is actionable (attention engine, unchanged).
	all := []*state.Item{t01, t02, t03}
	ready := views.Apply(all, views.ReadyFilter())
	assertIDSet(t, "dep chain ready set", ready, []string{"ready-t01"})
}

// TestNostrProjection_DepResolvedWhenBlockerTerminal mirrors pkg/state's
// TestDerive_ImplicitUnblockCleansIndex: once the blocker reaches a terminal
// status, the dependent item is no longer blocked (no explicit "unblock"
// event needed -- unlike campfire, nostr deps are read off the current
// materialized card each time).
func TestNostrProjection_DepResolvedWhenBlockerTerminal(t *testing.T) {
	k := testKey(t)
	events := []*nostr.Event{
		buildCard(t, k, CardSpec{ItemID: "ready-t01", Title: "Blocker", Status: state.StatusDone, Priority: "p1", Type: "task", BoardD: "ready"}, 1700000000),
		buildCard(t, k, CardSpec{ItemID: "ready-t02", Title: "Blocked", Status: state.StatusActive, Priority: "p2", Type: "task", BoardD: "ready", Deps: []string{"ready-t01"}}, 1700000100),
	}

	items := ProjectItems(events, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	t02 := items["ready-t02"]
	if t02 == nil {
		t.Fatal("ready-t02 not projected")
	}
	if t02.Status == state.StatusBlocked {
		t.Errorf("expected t02 unblocked once blocker is terminal, got %q", t02.Status)
	}
	ready := views.Apply([]*state.Item{items["ready-t01"], t02}, views.ReadyFilter())
	assertIDSet(t, "blocker-terminal ready set", ready, []string{"ready-t02"})
}

// TestNostrProjection_UnknownDepNonBlocking verifies a declared dep on an item
// not present in this event set (not yet ingested / cross-project) does not
// block -- mirrors pkg/state's rule that both ends of a block edge must
// resolve locally (crossdep.go handles cross-project refs separately).
func TestNostrProjection_UnknownDepNonBlocking(t *testing.T) {
	k := testKey(t)
	events := []*nostr.Event{
		buildCard(t, k, CardSpec{ItemID: "ready-t02", Title: "Blocked", Status: state.StatusActive, Priority: "p2", Type: "task", BoardD: "ready", Deps: []string{"ready-unknown"}}, 1700000100),
	}
	items := ProjectItems(events, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	t02 := items["ready-t02"]
	if t02 == nil {
		t.Fatal("ready-t02 not projected")
	}
	if t02.Status == state.StatusBlocked {
		t.Errorf("expected unresolvable dep to be non-blocking, got %q", t02.Status)
	}
	if len(t02.BlockedBy) != 0 {
		t.Errorf("expected BlockedBy empty for unresolvable dep, got %v", t02.BlockedBy)
	}
}

// TestNostrProjection_Gate mirrors pkg/state's TestDerive_Gate: an item with a
// gate declared on its card is waiting, carries GateMsgID (views.GatesFilter's
// non-empty check), and is excluded from the ready view is NOT asserted here
// because pkg/views.ReadyFilter does not filter on waiting status (matches
// existing campfire-derived behavior -- ready-82c must not change semantics).
func TestNostrProjection_Gate(t *testing.T) {
	k := testKey(t)
	events := []*nostr.Event{
		buildCard(t, k, CardSpec{
			ItemID: "ready-t01", Title: "Test", Status: state.StatusWaiting, Priority: "p1", Type: "task", BoardD: "ready",
			Gate: "design", WaitingType: "gate", WaitingOn: "Confirm approach before implementing",
		}, 1700001000),
	}
	items := ProjectItems(events, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not projected")
	}
	if item.Status != state.StatusWaiting {
		t.Errorf("expected waiting, got %q", item.Status)
	}
	if item.WaitingType != "gate" {
		t.Errorf("expected waiting_type=gate, got %q", item.WaitingType)
	}
	if item.WaitingOn != "Confirm approach before implementing" {
		t.Errorf("expected WaitingOn preserved, got %q", item.WaitingOn)
	}
	if item.WaitingSince == "" {
		t.Error("expected WaitingSince to be set")
	}
	if item.GateMsgID == "" {
		t.Error("expected GateMsgID to be set for a pending gate")
	}
	if item.Gate != "design" {
		t.Errorf("expected Gate=design, got %q", item.Gate)
	}

	if !views.GatesFilter()(item) {
		t.Error("expected item to appear in the gates view")
	}
	// ReadyFilter semantics are unchanged: waiting (non-blocked, non-terminal,
	// non-scheduled) items still appear in ready, same as the campfire path.
	if !views.ReadyFilter()(item) {
		t.Error("expected waiting item to still be in the ready view (unchanged semantics)")
	}
}

// TestNostrProjection_GateResolved mirrors pkg/state's TestDerive_GateApproved:
// once the card is republished with the gate cleared (status active again),
// WaitingType/WaitingOn/WaitingSince/GateMsgID are all cleared and the item
// drops out of the gates view.
func TestNostrProjection_GateResolved(t *testing.T) {
	k := testKey(t)
	events := []*nostr.Event{
		buildCard(t, k, CardSpec{
			ItemID: "ready-t01", Title: "Test", Status: state.StatusWaiting, Priority: "p1", Type: "task", BoardD: "ready",
			Gate: "design", WaitingType: "gate", WaitingOn: "Confirm approach",
		}, 1700001000),
		// Gate approved: republish the card with the gate cleared.
		buildCard(t, k, CardSpec{ItemID: "ready-t01", Title: "Test", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready"}, 1700002000),
	}
	items := ProjectItems(events, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not projected")
	}
	if item.Status != state.StatusActive {
		t.Errorf("expected active after approval, got %q", item.Status)
	}
	if item.GateMsgID != "" {
		t.Errorf("expected GateMsgID cleared, got %q", item.GateMsgID)
	}
	if item.WaitingType != "" || item.WaitingOn != "" || item.WaitingSince != "" {
		t.Errorf("expected waiting fields cleared, got type=%q on=%q since=%q", item.WaitingType, item.WaitingOn, item.WaitingSince)
	}
	if views.GatesFilter()(item) {
		t.Error("expected resolved item to no longer appear in the gates view")
	}
}

// TestNostrProjection_ReadinessParity is the DONE-criteria proof: a small mixed
// dep+gate graph, projected from nostr events, produces the SAME ready set the
// campfire-derived attention engine would produce for the equivalent graph:
//   - t01: no deps, active                -> ready
//   - t02: blocked by t01 (active)        -> not ready (blocked)
//   - t03: waiting on a gate, no deps     -> ready (ReadyFilter doesn't exclude waiting)
//   - t04: done                           -> not ready (terminal)
//   - t05: blocked by t04 (terminal)      -> ready (blocker resolved, no longer blocked)
func TestNostrProjection_ReadinessParity(t *testing.T) {
	k := testKey(t)
	events := []*nostr.Event{
		buildCard(t, k, CardSpec{ItemID: "ready-t01", Title: "t01", Status: state.StatusActive, Priority: "p0", Type: "task", BoardD: "ready"}, 1700000000),
		buildCard(t, k, CardSpec{ItemID: "ready-t02", Title: "t02", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready", Deps: []string{"ready-t01"}}, 1700000100),
		buildCard(t, k, CardSpec{ItemID: "ready-t03", Title: "t03", Status: state.StatusWaiting, Priority: "p1", Type: "task", BoardD: "ready", Gate: "human", WaitingType: "gate", WaitingOn: "needs sign-off"}, 1700000200),
		buildCard(t, k, CardSpec{ItemID: "ready-t04", Title: "t04", Status: state.StatusDone, Priority: "p2", Type: "task", BoardD: "ready"}, 1700000300),
		buildCard(t, k, CardSpec{ItemID: "ready-t05", Title: "t05", Status: state.StatusActive, Priority: "p2", Type: "task", BoardD: "ready", Deps: []string{"ready-t04"}}, 1700000400),
	}
	itemsByID := ProjectItems(events, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	var all []*state.Item
	for _, id := range []string{"ready-t01", "ready-t02", "ready-t03", "ready-t04", "ready-t05"} {
		item, ok := itemsByID[id]
		if !ok {
			t.Fatalf("item %s not projected", id)
		}
		all = append(all, item)
	}

	ready := views.Apply(all, views.ReadyFilter())
	assertIDSet(t, "mixed dep+gate readiness parity", ready, []string{"ready-t01", "ready-t03", "ready-t05"})

	gates := views.Apply(all, views.GatesFilter())
	assertIDSet(t, "gates view parity", gates, []string{"ready-t03"})
}

// assertIDSet asserts got contains exactly the ids in want, order-independent.
func assertIDSet(t *testing.T, ctx string, got []*state.Item, want []string) {
	t.Helper()
	gotIDs := make(map[string]bool, len(got))
	for _, it := range got {
		gotIDs[it.ID] = true
	}
	wantIDs := make(map[string]bool, len(want))
	for _, id := range want {
		wantIDs[id] = true
	}
	if len(gotIDs) != len(wantIDs) {
		t.Errorf("[%s] got %d items %v, want %d items %v", ctx, len(gotIDs), idKeys(gotIDs), len(wantIDs), want)
		return
	}
	for id := range wantIDs {
		if !gotIDs[id] {
			t.Errorf("[%s] expected %s in result, got %v", ctx, id, idKeys(gotIDs))
		}
	}
}

func idKeys(m map[string]bool) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
