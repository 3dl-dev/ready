// Write-path parity (ready-2cf): every rd mutation that changes item state must
// publish to nostr so a reader reconstructing purely from the log sees the SAME
// state as campfire. This file proves the wire+projection round-trip for the
// mutations wired in ready-2cf — dep add/remove, gate, approve, defer, label
// add/remove, and cascade-child close — at the substrate level (Publisher ->
// NostrLog -> ProjectItems), independent of the cmd/rd plumbing.
package sync

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
)

// newTestPublisher returns a relay-less Publisher whose events land in a fresh
// on-disk authoritative log (WriteRelays empty => every event is durable in the
// log, buffered=true, no network). Mirrors the production Publisher exactly minus
// the relay leg.
func newTestPublisher(t *testing.T, k *nostr.Key) (*Publisher, *NostrLog) {
	t.Helper()
	logPath := filepath.Join(t.TempDir(), "nostr-log.jsonl")
	log := NewNostrLog(logPath)
	return &Publisher{Key: k, Log: log, WriteRelays: nil}, log
}

func projectTrusted(t *testing.T, log *NostrLog, k *nostr.Key) map[string]*state.Item {
	t.Helper()
	events, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	trust := map[string]bool{k.PubKeyHex(): true}
	return ProjectItems(events, ProjectOptions{Maintainers: trust, Trusted: trust})
}

// cardFromItem mirrors cmd/rd's nostrCardSpecFromItem: build the FULL card from
// current item state so a re-publish never drops deps/gate/labels/eta.
func cardFromItem(it *state.Item, boardD string) CardSpec {
	return CardSpec{
		ItemID:      it.ID,
		Title:       it.Title,
		Status:      it.Status,
		Priority:    it.Priority,
		Assignee:    it.By,
		Type:        it.Type,
		Context:     it.Context,
		BoardD:      boardD,
		Deps:        it.BlockedBy,
		Gate:        it.Gate,
		WaitingType: it.WaitingType,
		WaitingOn:   it.WaitingOn,
		Labels:      it.Labels,
		ETA:         it.ETA,
	}
}

// TestWirePath_LabelsAndETARoundTrip is the narrow wire proof: a card carrying
// "l" and "eta" tags reconstructs Item.Labels/Item.ETA through the projection.
func TestWirePath_LabelsAndETARoundTrip(t *testing.T) {
	k := testKey(t)
	spec := CardSpec{
		ItemID: "ready-l01", Title: "labelled", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: "ready",
		Labels: []string{"bug", "blog-candidate"}, ETA: "2026-08-01T09:00:00Z",
	}
	ev, err := BuildCardEvent(k, spec, 1700000000)
	if err != nil {
		t.Fatalf("build card: %v", err)
	}
	if _, ok := findTag(ev.Tags, "l"); !ok {
		t.Fatalf("card is missing an \"l\" label tag: %v", ev.Tags)
	}
	if v, ok := findTag(ev.Tags, "eta"); !ok || v != spec.ETA {
		t.Fatalf("card eta tag = %q,%v; want %q", v, ok, spec.ETA)
	}
	trust := map[string]bool{k.PubKeyHex(): true}
	items := ProjectItems([]*nostr.Event{ev}, ProjectOptions{Maintainers: trust, Trusted: trust})
	it := items["ready-l01"]
	if it == nil {
		t.Fatal("item not projected")
	}
	if len(it.Labels) != 2 || it.Labels[0] != "bug" || it.Labels[1] != "blog-candidate" {
		t.Errorf("labels round-trip failed: got %v", it.Labels)
	}
	if it.ETA != spec.ETA {
		t.Errorf("eta round-trip failed: got %q want %q", it.ETA, spec.ETA)
	}
}

// TestWritePath_FullMutationParity drives the exact publish sequence the ready-2cf
// hooks emit for a realistic item lifecycle and asserts the nostr projection
// reconstructs readiness/deps/gates/labels/eta/history at every consequential
// step — this is the deterministic analogue of the live-relay demo.
func TestWritePath_FullMutationParity(t *testing.T) {
	k := testKey(t)
	pub, log := newTestPublisher(t, k)
	ctx := context.Background()
	const board = "ready"
	boardSpec := &BoardSpec{BoardD: board, Title: board, Maintainers: []string{k.PubKeyHex()}}

	// t=1 create parent + a blocker item (rd create hook: PublishItem).
	parent := &state.Item{ID: "ready-p01", Title: "parent", Status: state.StatusActive, Priority: "p1", Type: "task"}
	blocker := &state.Item{ID: "ready-b01", Title: "blocker", Status: state.StatusActive, Priority: "p1", Type: "task"}
	if _, err := pub.PublishItem(ctx, boardSpec, cardFromItem(parent, board), 1000); err != nil {
		t.Fatal(err)
	}
	if _, err := pub.PublishItem(ctx, boardSpec, cardFromItem(blocker, board), 1000); err != nil {
		t.Fatal(err)
	}

	// t=2 rd dep add ready-p01 ready-b01 (dep-add hook: card edit w/ new deps).
	parent.BlockedBy = []string{"ready-b01"}
	if _, err := pub.PublishCardEdit(ctx, cardFromItem(parent, board), 2000); err != nil {
		t.Fatal(err)
	}
	items := projectTrusted(t, log, k)
	if got := items["ready-p01"]; got == nil || got.Status != state.StatusBlocked {
		t.Fatalf("after dep add: parent status = %v; want blocked", statusOf(got))
	}
	if got := items["ready-b01"]; got == nil || len(got.Blocks) != 1 || got.Blocks[0] != "ready-p01" {
		t.Fatalf("after dep add: blocker.Blocks = %v; want [ready-p01]", blocksOf(got))
	}

	// t=3 rd label add ready-p01 bug (label-add hook: card edit w/ labels).
	parent.Labels = []string{"bug"}
	if _, err := pub.PublishCardEdit(ctx, cardFromItem(parent, board), 3000); err != nil {
		t.Fatal(err)
	}

	// t=4 rd defer ready-p01 (defer hook: card edit w/ eta).
	parent.ETA = "2026-09-01T09:00:00Z"
	if _, err := pub.PublishCardEdit(ctx, cardFromItem(parent, board), 4000); err != nil {
		t.Fatal(err)
	}

	// t=4.5 close the blocker (rd done ready-b01) so the parent is no longer
	// blocked — blocked-status supersedes a gate in the projection (matches
	// pkg/state), so the gate is only observable on an unblocked item.
	blocker.Status = state.StatusDone
	if _, err := pub.PublishStatusChange(ctx, cardFromItem(blocker, board), "done", 4500); err != nil {
		t.Fatal(err)
	}

	// t=5 rd gate ready-p01 (gate hook: status change -> waiting/gate).
	parent.Status = state.StatusWaiting
	parent.WaitingType = "gate"
	parent.WaitingOn = "need sign-off"
	if _, err := pub.PublishStatusChange(ctx, cardFromItem(parent, board), "need sign-off", 5000); err != nil {
		t.Fatal(err)
	}
	items = projectTrusted(t, log, k)
	gp := items["ready-p01"]
	if gp == nil || gp.Status != state.StatusWaiting || gp.WaitingType != "gate" || gp.GateMsgID == "" {
		t.Fatalf("after gate: parent = status=%v waiting_type=%q gateMsgID=%q; want waiting/gate/non-empty",
			statusOf(gp), gpWaitingType(gp), gpGateMsg(gp))
	}
	// Deps + labels + eta survive the gate re-publish (nostrCardSpecFromItem carries them).
	if len(gp.Labels) != 1 || gp.Labels[0] != "bug" {
		t.Errorf("after gate: labels clobbered: %v", gp.Labels)
	}
	if gp.ETA != "2026-09-01T09:00:00Z" {
		t.Errorf("after gate: eta clobbered: %q", gp.ETA)
	}

	// t=6 rd approve ready-p01 (approve hook: status change -> active, gate cleared).
	parent.Status = state.StatusActive
	parent.Gate, parent.WaitingType, parent.WaitingOn = "", "", ""
	if _, err := pub.PublishStatusChange(ctx, cardFromItem(parent, board), "approved: proceed", 6000); err != nil {
		t.Fatal(err)
	}
	items = projectTrusted(t, log, k)
	ap := items["ready-p01"]
	if ap == nil || ap.Status != state.StatusActive { // blocker is done => not blocked; gate cleared => active
		t.Fatalf("after approve: parent status = %v; want active", statusOf(ap))
	}
	if ap.WaitingType != "" || ap.GateMsgID != "" {
		t.Errorf("after approve: gate not cleared: waiting_type=%q gateMsgID=%q", ap.WaitingType, ap.GateMsgID)
	}

	// t=7 rd dep remove ready-p01 ready-b01 (dep-remove hook: card edit). The edge
	// is already inert (blocker terminal), but the card must drop the "i" tag so a
	// reader no longer records the dependency at all.
	parent.BlockedBy = nil
	if _, err := pub.PublishCardEdit(ctx, cardFromItem(parent, board), 7000); err != nil {
		t.Fatal(err)
	}
	items = projectTrusted(t, log, k)
	rp := items["ready-p01"]
	if rp == nil || rp.Status != state.StatusActive || len(rp.BlockedBy) != 0 {
		t.Fatalf("after dep remove: parent status=%v blockedBy=%v; want active/[]", statusOf(rp), blockedByOf(rp))
	}

	// t=8 rd label remove ready-p01 bug (label-remove hook: card edit).
	parent.Labels = nil
	if _, err := pub.PublishCardEdit(ctx, cardFromItem(parent, board), 8000); err != nil {
		t.Fatal(err)
	}
	items = projectTrusted(t, log, k)
	if lp := items["ready-p01"]; lp == nil || len(lp.Labels) != 0 {
		t.Fatalf("after label remove: labels = %v; want []", labelsOf(items["ready-p01"]))
	}

	// History parity: the gate (waiting) and approve (active) transitions are both
	// in the audit-history replay, each carrying its reason.
	fp := items["ready-p01"]
	if len(fp.History) < 2 {
		t.Fatalf("history replay too short: %d entries", len(fp.History))
	}
	var sawGate, sawApprove bool
	for _, h := range fp.History {
		if h.ToStatus == state.StatusWaiting && h.Note == "need sign-off" {
			sawGate = true
		}
		if h.ToStatus == state.StatusActive && h.Note == "approved: proceed" {
			sawApprove = true
		}
	}
	if !sawGate || !sawApprove {
		t.Errorf("history missing gate/approve transitions: sawGate=%v sawApprove=%v hist=%+v", sawGate, sawApprove, fp.History)
	}
}

// TestWritePath_CascadeChildrenPublish proves that cascade-closing a parent
// publishes a terminal status change for EACH descendant, so children reconstruct
// as cancelled on nostr (not just the parent).
func TestWritePath_CascadeChildrenPublish(t *testing.T) {
	k := testKey(t)
	pub, log := newTestPublisher(t, k)
	ctx := context.Background()
	const board = "ready"
	boardSpec := &BoardSpec{BoardD: board, Title: board, Maintainers: []string{k.PubKeyHex()}}

	parent := &state.Item{ID: "ready-c00", Title: "parent", Status: state.StatusActive, Priority: "p1", Type: "task"}
	child := &state.Item{ID: "ready-c01", Title: "child", Status: state.StatusActive, Priority: "p1", Type: "task", ParentID: "ready-c00"}
	grand := &state.Item{ID: "ready-c02", Title: "grandchild", Status: state.StatusActive, Priority: "p1", Type: "task", ParentID: "ready-c01"}
	for _, it := range []*state.Item{parent, child, grand} {
		if _, err := pub.PublishItem(ctx, boardSpec, cardFromItem(it, board), 1000); err != nil {
			t.Fatal(err)
		}
	}

	// cancel --cascade: each descendant closed (leaf-first), then the parent —
	// every one publishes a cancelled status change (ready-2cf cascade hook).
	for _, it := range []*state.Item{grand, child, parent} {
		it.Status = state.StatusCancelled
		if _, err := pub.PublishStatusChange(ctx, cardFromItem(it, board), "scope cut", 2000); err != nil {
			t.Fatal(err)
		}
	}

	items := projectTrusted(t, log, k)
	for _, id := range []string{"ready-c00", "ready-c01", "ready-c02"} {
		it := items[id]
		if it == nil || it.Status != state.StatusCancelled {
			t.Errorf("%s: status = %v; want cancelled (cascade child must publish)", id, statusOf(it))
			continue
		}
		last := it.History[len(it.History)-1]
		if last.ToStatus != state.StatusCancelled || last.Note != "scope cut" {
			t.Errorf("%s: last history = %s/%q; want cancelled/\"scope cut\"", id, last.ToStatus, last.Note)
		}
	}
}

// small nil-safe accessors for clearer failure messages.
func statusOf(it *state.Item) string {
	if it == nil {
		return "<nil>"
	}
	return it.Status
}
func blocksOf(it *state.Item) []string {
	if it == nil {
		return nil
	}
	return it.Blocks
}
func blockedByOf(it *state.Item) []string {
	if it == nil {
		return nil
	}
	return it.BlockedBy
}
func labelsOf(it *state.Item) []string {
	if it == nil {
		return nil
	}
	return it.Labels
}
func gpWaitingType(it *state.Item) string {
	if it == nil {
		return "<nil>"
	}
	return it.WaitingType
}
func gpGateMsg(it *state.Item) string {
	if it == nil {
		return "<nil>"
	}
	return it.GateMsgID
}
