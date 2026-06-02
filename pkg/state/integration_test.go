package state_test

// Integration tests derived from session log mining (32,199 Claude Code sessions).
// Each test replays a real agent workflow as a campfire message sequence through
// Derive(), then asserts the derived state and view filter results.
//
// These tests exercise the full message→state→view pipeline. They catch
// regressions at the boundaries agents actually hit, not at internal struct
// boundaries. Every scenario is grounded in observed usage counts.

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/views"
)

// makeMsgFrom is like makeMsg but allows specifying sender.
func makeMsgFrom(id, sender string, tags []string, payload interface{}, antecedents []string, ts int64) store.MessageRecord {
	p, _ := json.Marshal(payload)
	return store.MessageRecord{
		ID:          id,
		CampfireID:  testCampfire,
		Sender:      sender,
		Payload:     p,
		Tags:        tags,
		Antecedents: antecedents,
		Timestamp:   ts,
	}
}

// ---------------------------------------------------------------------------
// Scenario 1: Agent claims work with bd-style update --status in_progress --claim
// Evidence: 10,494 uses of bd update --claim, 72 uses of rd update --status in_progress
//
// The real agent sequence:
//   1. rd create (human creates item)
//   2. rd update --status in_progress --claim (agent grabs it)
//
// At the campfire level this produces:
//   work:create → work:status(to=active) → work:claim
//
// After the sequence, the item should be:
//   status=active, by=agent-sender, visible in "work" and "my-work" views.
// ---------------------------------------------------------------------------
func TestIntegration_AgentClaimViaStatusAndClaim(t *testing.T) {
	ts := now()
	agentKey := "agent-pubkey-abc123"

	msgs := []store.MessageRecord{
		// Human creates item
		makeMsgFrom("msg-create", "human-pubkey", []string{"work:create"}, map[string]interface{}{
			"id": "ready-001", "title": "Fix the widget", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
			"context": "Widget is broken in prod",
		}, nil, ts),
		// Agent sends work:status to=active (from --status in_progress, aliased to active)
		makeMsgFrom("msg-status", agentKey, []string{"work:status", "work:status:active"}, map[string]interface{}{
			"target": "msg-create", "to": "active",
		}, []string{"msg-create"}, ts+1000),
		// Agent sends work:claim (from --claim flag)
		makeMsgFrom("msg-claim", agentKey, []string{"work:claim"}, map[string]interface{}{
			"target": "msg-create",
		}, []string{"msg-create"}, ts+2000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-001"]
	if item == nil {
		t.Fatal("item not found")
	}

	// Status should be active (not in_progress)
	if item.Status != state.StatusActive {
		t.Errorf("expected status=active, got %q", item.Status)
	}
	// By should be the agent's key
	if item.By != agentKey {
		t.Errorf("expected by=%s, got %q", agentKey, item.By)
	}
	// Description should mirror context (bd-compat)
	if item.Description != "Widget is broken in prod" {
		t.Errorf("expected description to mirror context, got %q", item.Description)
	}
	// Context should also be set
	if item.Context != "Widget is broken in prod" {
		t.Errorf("expected context='Widget is broken in prod', got %q", item.Context)
	}

	// View assertions
	workFilter := views.WorkFilter()
	if !workFilter(item) {
		t.Error("item should appear in work view (status=active)")
	}
	myWorkFilter := views.MyWorkFilter(agentKey)
	if !myWorkFilter(item) {
		t.Error("item should appear in my-work view for the claiming agent")
	}
	delegatedFilter := views.DelegatedFilter("baron@3dl.dev")
	if !delegatedFilter(item) {
		t.Error("item should appear in delegated view for the for-party (baron)")
	}
}

// ---------------------------------------------------------------------------
// Scenario 2: Agent completes work — create → claim → close(done)
// Evidence: 2,894 uses of rd complete in session logs
//
// Sequence:
//   work:create → work:claim → work:close(done)
//
// After close: status=done, terminal, NOT in ready/work/my-work views.
// ---------------------------------------------------------------------------
func TestIntegration_AgentCompletesWork(t *testing.T) {
	ts := now()
	agentKey := "agent-impl-001"

	msgs := []store.MessageRecord{
		makeMsgFrom("msg-create", "human-pubkey", []string{"work:create"}, map[string]interface{}{
			"id": "ready-002", "title": "Implement feature", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsgFrom("msg-claim", agentKey, []string{"work:claim"}, map[string]interface{}{
			"target": "msg-create",
		}, []string{"msg-create"}, ts+1000),
		makeMsgFrom("msg-close", agentKey, []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target": "msg-create", "resolution": "done",
			"reason": "Implemented. Branch: work/ready-002",
		}, []string{"msg-create"}, ts+2000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-002"]
	if item == nil {
		t.Fatal("item not found")
	}

	if item.Status != state.StatusDone {
		t.Errorf("expected status=done, got %q", item.Status)
	}
	if !state.IsTerminal(item) {
		t.Error("expected terminal after close")
	}

	// Should NOT appear in active views
	if views.WorkFilter()(item) {
		t.Error("closed item should not appear in work view")
	}
	if views.MyWorkFilter(agentKey)(item) {
		t.Error("closed item should not appear in my-work view")
	}
	if views.ReadyFilter()(item) {
		t.Error("closed item should not appear in ready view")
	}
}

// ---------------------------------------------------------------------------
// Scenario 3: Multi-status list filter — inbox + active (OR semantics)
// Evidence: agents filter rd list --status inbox --status active
//
// Three items in different states. Filter should return exactly the two
// matching either status.
// ---------------------------------------------------------------------------
func TestIntegration_MultiStatusListFilter(t *testing.T) {
	ts := now()

	msgs := []store.MessageRecord{
		// Item 1: stays inbox
		makeMsg("msg-c1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-101", "title": "Inbox item", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
		}, nil, ts),
		// Item 2: claimed → active
		makeMsg("msg-c2", []string{"work:create"}, map[string]interface{}{
			"id": "ready-102", "title": "Active item", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsgFrom("msg-claim2", "agent-1", []string{"work:claim"}, map[string]interface{}{
			"target": "msg-c2",
		}, []string{"msg-c2"}, ts+1000),
		// Item 3: completed → done
		makeMsg("msg-c3", []string{"work:create"}, map[string]interface{}{
			"id": "ready-103", "title": "Done item", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-close3", []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target": "msg-c3", "resolution": "done", "reason": "done",
		}, []string{"msg-c3"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)

	// Collect items into a slice (simulates what rd list does)
	var all []*state.Item
	for _, item := range items {
		all = append(all, item)
	}

	// Filter: status IN (inbox, active) — OR semantics
	statusFilters := map[string]bool{"inbox": true, "active": true}
	var filtered []*state.Item
	for _, item := range all {
		if statusFilters[item.Status] {
			filtered = append(filtered, item)
		}
	}

	if len(filtered) != 2 {
		t.Fatalf("expected 2 items matching inbox|active, got %d", len(filtered))
	}

	// Verify we got the right ones
	ids := map[string]bool{}
	for _, item := range filtered {
		ids[item.ID] = true
	}
	if !ids["ready-101"] {
		t.Error("expected ready-101 (inbox) in filtered results")
	}
	if !ids["ready-102"] {
		t.Error("expected ready-102 (active) in filtered results")
	}
	if ids["ready-103"] {
		t.Error("ready-103 (done) should NOT be in filtered results")
	}
}

// ---------------------------------------------------------------------------
// Scenario 4: Description alias survives update cycle
// Evidence: 86 agent accesses of "description" field in JSON output
//
// After create with context, then update with new context, both context
// and description should reflect the update.
// ---------------------------------------------------------------------------
func TestIntegration_DescriptionAliasSurvivesUpdate(t *testing.T) {
	ts := now()

	msgs := []store.MessageRecord{
		makeMsg("msg-create", []string{"work:create"}, map[string]interface{}{
			"id": "ready-201", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
			"context": "Original context",
		}, nil, ts),
		makeMsg("msg-update", []string{"work:update"}, map[string]interface{}{
			"target":  "msg-create",
			"context": "Updated context with more detail",
		}, []string{"msg-create"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-201"]
	if item == nil {
		t.Fatal("item not found")
	}

	if item.Context != "Updated context with more detail" {
		t.Errorf("expected updated context, got %q", item.Context)
	}
	if item.Description != "Updated context with more detail" {
		t.Errorf("expected description to mirror updated context, got %q", item.Description)
	}

	// Verify JSON output has both fields
	data, err := json.Marshal(item)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}
	if m["context"] != m["description"] {
		t.Errorf("JSON context=%q != description=%q", m["context"], m["description"])
	}
}

// ---------------------------------------------------------------------------
// Scenario 5: Claim-then-close unblocks dependent items
// Evidence: block/unblock is used in dep wiring; close must auto-unblock
//
// Sequence:
//   create A, create B, block B on A, close A → B should be unblocked
// ---------------------------------------------------------------------------
func TestIntegration_CloseUnblocksDependents(t *testing.T) {
	ts := now()

	msgs := []store.MessageRecord{
		makeMsg("msg-cA", []string{"work:create"}, map[string]interface{}{
			"id": "ready-301", "title": "Blocker", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-cB", []string{"work:create"}, map[string]interface{}{
			"id": "ready-302", "title": "Blocked item", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Wire dependency: B blocked by A
		makeMsg("msg-block", []string{"work:block"}, map[string]interface{}{
			"blocker_id": "ready-301", "blocked_id": "ready-302",
			"blocker_msg": "msg-cA", "blocked_msg": "msg-cB",
		}, nil, ts+1000),
		// Close A
		makeMsg("msg-closeA", []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target": "msg-cA", "resolution": "done", "reason": "done",
		}, []string{"msg-cA"}, ts+2000),
	}

	items := state.Derive(testCampfire, msgs)

	blocker := items["ready-301"]
	blocked := items["ready-302"]
	if blocker == nil || blocked == nil {
		t.Fatal("items not found")
	}

	// Blocker should be done
	if blocker.Status != state.StatusDone {
		t.Errorf("blocker: expected done, got %q", blocker.Status)
	}
	// Blocked item should NOT be blocked anymore
	if state.IsBlocked(blocked) {
		t.Error("ready-302 should be unblocked after blocker closed")
	}
	// Blocked item should now appear in ready view
	if !views.ReadyFilter()(blocked) {
		t.Error("ready-302 should appear in ready view after unblock")
	}
}

// ---------------------------------------------------------------------------
// Scenario 6: Full agent lifecycle with delegation
// Evidence: delegate → claim → status updates → complete is the core loop
//
// Sequence:
//   create → delegate to agent → agent claims → agent sets waiting →
//   agent resumes active → agent closes done
//
// Validates all intermediate states and view membership.
// ---------------------------------------------------------------------------
func TestIntegration_FullAgentLifecycle(t *testing.T) {
	ts := now()
	managerKey := "manager-pubkey"
	agentKey := "agent-impl-pubkey"

	msgs := []store.MessageRecord{
		// Manager creates item for baron
		makeMsgFrom("msg-create", managerKey, []string{"work:create"}, map[string]interface{}{
			"id": "ready-401", "title": "Build the parser", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
			"context": "Parser spec is in docs/spec.md",
		}, nil, ts),
		// Manager delegates to agent
		makeMsgFrom("msg-delegate", managerKey, []string{"work:delegate"}, map[string]interface{}{
			"target": "msg-create", "to": agentKey,
		}, []string{"msg-create"}, ts+1000),
		// Agent claims (work:claim sets by=sender, status=active)
		makeMsgFrom("msg-claim", agentKey, []string{"work:claim"}, map[string]interface{}{
			"target": "msg-create",
		}, []string{"msg-create"}, ts+2000),
		// Agent hits a blocker — sets waiting
		makeMsgFrom("msg-waiting", agentKey, []string{"work:status", "work:status:waiting"}, map[string]interface{}{
			"target":       "msg-create",
			"to":           "waiting",
			"reason":       "Need design clarification",
			"waiting_on":   "baron",
			"waiting_type": "person",
		}, []string{"msg-create"}, ts+3000),
		// Baron answers — agent resumes
		makeMsgFrom("msg-active", agentKey, []string{"work:status", "work:status:active"}, map[string]interface{}{
			"target": "msg-create", "to": "active", "reason": "Got clarification",
		}, []string{"msg-create"}, ts+4000),
		// Agent completes
		makeMsgFrom("msg-close", agentKey, []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target": "msg-create", "resolution": "done",
			"reason": "Implemented. Branch: work/ready-401",
		}, []string{"msg-create"}, ts+5000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-401"]
	if item == nil {
		t.Fatal("item not found")
	}

	// Final state
	if item.Status != state.StatusDone {
		t.Errorf("expected done, got %q", item.Status)
	}
	if item.By != agentKey {
		t.Errorf("by should be agent after claim, got %q", item.By)
	}
	// Waiting fields should be cleared (status left waiting)
	if item.WaitingOn != "" {
		t.Errorf("waiting_on should be cleared after leaving waiting, got %q", item.WaitingOn)
	}
	if item.WaitingType != "" {
		t.Errorf("waiting_type should be cleared after leaving waiting, got %q", item.WaitingType)
	}
	// Description mirrors context
	if item.Description != "Parser spec is in docs/spec.md" {
		t.Errorf("description should mirror context, got %q", item.Description)
	}

	// Should be terminal and invisible in active views
	if !state.IsTerminal(item) {
		t.Error("should be terminal")
	}
	if views.WorkFilter()(item) {
		t.Error("should not be in work view")
	}
	if views.ReadyFilter()(item) {
		t.Error("should not be in ready view")
	}
}

// ---------------------------------------------------------------------------
// Scenario 7: Replay intermediate states from lifecycle
// Same as scenario 6 but verify state at each step by slicing msgs.
// This catches bugs where message ordering or state transitions are wrong.
// ---------------------------------------------------------------------------
func TestIntegration_LifecycleIntermediateStates(t *testing.T) {
	ts := now()
	agentKey := "agent-impl"

	allMsgs := []store.MessageRecord{
		makeMsgFrom("msg-c", "manager", []string{"work:create"}, map[string]interface{}{
			"id": "ready-501", "title": "Task", "type": "task",
			"for": "baron@3dl.dev", "priority": "p0",
		}, nil, ts),
		makeMsgFrom("msg-delegate", "manager", []string{"work:delegate"}, map[string]interface{}{
			"target": "msg-c", "to": agentKey,
		}, []string{"msg-c"}, ts+1000),
		makeMsgFrom("msg-claim", agentKey, []string{"work:claim"}, map[string]interface{}{
			"target": "msg-c",
		}, []string{"msg-c"}, ts+2000),
		makeMsgFrom("msg-wait", agentKey, []string{"work:status", "work:status:waiting"}, map[string]interface{}{
			"target": "msg-c", "to": "waiting", "waiting_on": "design", "waiting_type": "person",
		}, []string{"msg-c"}, ts+3000),
		makeMsgFrom("msg-resume", agentKey, []string{"work:status", "work:status:active"}, map[string]interface{}{
			"target": "msg-c", "to": "active",
		}, []string{"msg-c"}, ts+4000),
		makeMsgFrom("msg-done", agentKey, []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target": "msg-c", "resolution": "done", "reason": "done",
		}, []string{"msg-c"}, ts+5000),
	}

	// Note: ready view = not terminal AND not blocked AND eta < now+4h.
	// P0 ETA is now+1h, so active/waiting items with P0 ARE in ready view.
	// This matches convention spec §5: ready is attention-based, not status-based.
	steps := []struct {
		name         string
		msgCount     int
		wantStatus   string
		wantBy       string
		wantReady    bool
		wantWork     bool
		wantPending  bool
		wantTerminal bool
	}{
		{"after create", 1, state.StatusInbox, "", true, false, false, false},
		{"after delegate", 2, state.StatusInbox, agentKey, true, false, false, false},
		{"after claim", 3, state.StatusActive, agentKey, true, true, false, false},
		{"after waiting", 4, state.StatusWaiting, agentKey, true, false, true, false},
		{"after resume", 5, state.StatusActive, agentKey, true, true, false, false},
		{"after close", 6, state.StatusDone, agentKey, false, false, false, true},
	}

	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			items := state.Derive(testCampfire, allMsgs[:step.msgCount])
			item := items["ready-501"]
			if item == nil {
				t.Fatal("item not found")
			}
			if item.Status != step.wantStatus {
				t.Errorf("status: got %q, want %q", item.Status, step.wantStatus)
			}
			if item.By != step.wantBy {
				t.Errorf("by: got %q, want %q", item.By, step.wantBy)
			}

			// P0 ETA is now+1h — well within 4h ready window
			gotReady := views.ReadyFilter()(item)
			if gotReady != step.wantReady {
				t.Errorf("ready view: got %v, want %v", gotReady, step.wantReady)
			}
			gotWork := views.WorkFilter()(item)
			if gotWork != step.wantWork {
				t.Errorf("work view: got %v, want %v", gotWork, step.wantWork)
			}
			gotPending := views.PendingFilter()(item)
			if gotPending != step.wantPending {
				t.Errorf("pending view: got %v, want %v", gotPending, step.wantPending)
			}
			gotTerminal := state.IsTerminal(item)
			if gotTerminal != step.wantTerminal {
				t.Errorf("terminal: got %v, want %v", gotTerminal, step.wantTerminal)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Scenario 8: Gate lifecycle — create → gate → gate-resolve(approved) → close
// Evidence: gate operations are part of the convention; agents hit gates when
// they need human approval (budget, design, scope).
// ---------------------------------------------------------------------------
func TestIntegration_GateLifecycle(t *testing.T) {
	ts := now()

	msgs := []store.MessageRecord{
		makeMsg("msg-c", []string{"work:create"}, map[string]interface{}{
			"id": "ready-601", "title": "Gated task", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Agent gates for design approval
		makeMsg("msg-gate", []string{"work:gate"}, map[string]interface{}{
			"target":      "msg-c",
			"gate_type":   "design",
			"description": "Need approval for API shape",
		}, []string{"msg-c"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-601"]
	if item == nil {
		t.Fatal("item not found")
	}

	// After gate: waiting with gate details
	if item.Status != state.StatusWaiting {
		t.Errorf("expected waiting after gate, got %q", item.Status)
	}
	if item.WaitingType != "gate" {
		t.Errorf("expected waiting_type=gate, got %q", item.WaitingType)
	}
	if item.GateMsgID != "msg-gate" {
		t.Errorf("expected gate_msg_id=msg-gate, got %q", item.GateMsgID)
	}
	// Should appear in gates view
	if !views.GatesFilter()(item) {
		t.Error("gated item should appear in gates view")
	}
	// Should appear in pending view
	if !views.PendingFilter()(item) {
		t.Error("gated item should appear in pending view")
	}
	// Convention note: ready view = not terminal AND not blocked AND eta < now+4h.
	// A waiting item IS eligible for ready view if ETA is within window.
	// The gates view is the dedicated filter for gated items.

	// Now approve the gate
	msgs = append(msgs,
		makeMsg("msg-resolve", []string{"work:gate-resolve"}, map[string]interface{}{
			"target":     "msg-gate",
			"resolution": "approved",
			"reason":     "API shape looks good",
		}, []string{"msg-gate"}, ts+2000),
	)

	items = state.Derive(testCampfire, msgs)
	item = items["ready-601"]

	// After approval: active, gate cleared
	if item.Status != state.StatusActive {
		t.Errorf("expected active after gate-resolve(approved), got %q", item.Status)
	}
	if item.WaitingType != "" {
		t.Errorf("expected waiting_type cleared, got %q", item.WaitingType)
	}
	if item.GateMsgID != "" {
		t.Errorf("expected gate_msg_id cleared, got %q", item.GateMsgID)
	}
	// Should now appear in work view
	if !views.WorkFilter()(item) {
		t.Error("approved item should appear in work view")
	}
	// Should no longer appear in gates view
	if views.GatesFilter()(item) {
		t.Error("approved item should not appear in gates view")
	}
}

// ---------------------------------------------------------------------------
// Scenario 9: Dependency chain — A blocks B blocks C, close A, then B, verify C
// Evidence: dependency wiring is the most fragile part of state derivation
// ---------------------------------------------------------------------------
func TestIntegration_DependencyChain(t *testing.T) {
	ts := now()

	msgs := []store.MessageRecord{
		makeMsg("msg-cA", []string{"work:create"}, map[string]interface{}{
			"id": "ready-701", "title": "A", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-cB", []string{"work:create"}, map[string]interface{}{
			"id": "ready-702", "title": "B", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-cC", []string{"work:create"}, map[string]interface{}{
			"id": "ready-703", "title": "C", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// B blocked by A
		makeMsg("msg-block1", []string{"work:block"}, map[string]interface{}{
			"blocker_id": "ready-701", "blocked_id": "ready-702",
			"blocker_msg": "msg-cA", "blocked_msg": "msg-cB",
		}, nil, ts+1000),
		// C blocked by B
		makeMsg("msg-block2", []string{"work:block"}, map[string]interface{}{
			"blocker_id": "ready-702", "blocked_id": "ready-703",
			"blocker_msg": "msg-cB", "blocked_msg": "msg-cC",
		}, nil, ts+1000),
	}

	// Before any closes: A ready, B blocked, C blocked
	items := state.Derive(testCampfire, msgs)
	if !views.ReadyFilter()(items["ready-701"]) {
		t.Error("A should be ready")
	}
	if !state.IsBlocked(items["ready-702"]) {
		t.Error("B should be blocked by A")
	}
	if !state.IsBlocked(items["ready-703"]) {
		t.Error("C should be blocked by B")
	}

	// Close A
	msgs = append(msgs,
		makeMsg("msg-closeA", []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target": "msg-cA", "resolution": "done", "reason": "done",
		}, []string{"msg-cA"}, ts+2000),
	)
	items = state.Derive(testCampfire, msgs)

	// After closing A: B should be unblocked, C still blocked by B
	if state.IsBlocked(items["ready-702"]) {
		t.Error("B should be unblocked after A closed")
	}
	if !views.ReadyFilter()(items["ready-702"]) {
		t.Error("B should now be ready")
	}
	if !state.IsBlocked(items["ready-703"]) {
		t.Error("C should still be blocked by B")
	}

	// Close B
	msgs = append(msgs,
		makeMsg("msg-closeB", []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target": "msg-cB", "resolution": "done", "reason": "done",
		}, []string{"msg-cB"}, ts+3000),
	)
	items = state.Derive(testCampfire, msgs)

	// After closing B: C should be unblocked
	if state.IsBlocked(items["ready-703"]) {
		t.Error("C should be unblocked after B closed")
	}
	if !views.ReadyFilter()(items["ready-703"]) {
		t.Error("C should now be ready")
	}
}

// ---------------------------------------------------------------------------
// Scenario 10: Context update mirrors to description through full lifecycle
// Evidence: 86 description accesses + agents update context mid-work
//
// Verifies that description stays in sync with context across:
//   create → update context → claim → update context again → close
// ---------------------------------------------------------------------------
func TestIntegration_DescriptionMirrorsContextThroughLifecycle(t *testing.T) {
	ts := now()

	msgs := []store.MessageRecord{
		makeMsg("msg-c", []string{"work:create"}, map[string]interface{}{
			"id": "ready-801", "title": "Evolving", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
			"context": "v1: initial spec",
		}, nil, ts),
	}

	// After create
	items := state.Derive(testCampfire, msgs)
	if items["ready-801"].Description != "v1: initial spec" {
		t.Errorf("after create: description=%q", items["ready-801"].Description)
	}

	// Update context
	msgs = append(msgs, makeMsg("msg-u1", []string{"work:update"}, map[string]interface{}{
		"target": "msg-c", "context": "v2: added constraints",
	}, []string{"msg-c"}, ts+1000))

	items = state.Derive(testCampfire, msgs)
	if items["ready-801"].Description != "v2: added constraints" {
		t.Errorf("after update 1: description=%q", items["ready-801"].Description)
	}
	if items["ready-801"].Context != "v2: added constraints" {
		t.Errorf("after update 1: context=%q", items["ready-801"].Context)
	}

	// Claim
	msgs = append(msgs, makeMsgFrom("msg-cl", "agent", []string{"work:claim"}, map[string]interface{}{
		"target": "msg-c",
	}, []string{"msg-c"}, ts+2000))

	// Update context again while active
	msgs = append(msgs, makeMsg("msg-u2", []string{"work:update"}, map[string]interface{}{
		"target": "msg-c", "context": "v3: final spec with edge cases",
	}, []string{"msg-c"}, ts+3000))

	items = state.Derive(testCampfire, msgs)
	if items["ready-801"].Description != "v3: final spec with edge cases" {
		t.Errorf("after update 2: description=%q", items["ready-801"].Description)
	}
}

// ---------------------------------------------------------------------------
// Scenario 11: Clear sentinel clears description too
// Evidence: work:update with "-" clears fields; description must track
// ---------------------------------------------------------------------------
func TestIntegration_ClearSentinelClearsDescription(t *testing.T) {
	ts := now()

	msgs := []store.MessageRecord{
		makeMsg("msg-c", []string{"work:create"}, map[string]interface{}{
			"id": "ready-901", "title": "Clear test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
			"context": "Some context",
		}, nil, ts),
		makeMsg("msg-u", []string{"work:update"}, map[string]interface{}{
			"target":  "msg-c",
			"context": "-",
		}, []string{"msg-c"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-901"]
	if item.Context != "" {
		t.Errorf("context should be cleared, got %q", item.Context)
	}
	if item.Description != "" {
		t.Errorf("description should be cleared when context is cleared, got %q", item.Description)
	}
}

// ---------------------------------------------------------------------------
// Scenario 12: P0 item is immediately ready; P3 item is NOT ready (eta > 4h)
// Evidence: agents rely on ready view for work dispatch
// ---------------------------------------------------------------------------
func TestIntegration_PriorityETAReadyVisibility(t *testing.T) {
	ts := now()

	msgs := []store.MessageRecord{
		// P0: eta = now + 1h → within 4h → ready
		makeMsg("msg-c0", []string{"work:create"}, map[string]interface{}{
			"id": "ready-a01", "title": "P0 urgent", "type": "task",
			"for": "baron@3dl.dev", "priority": "p0",
		}, nil, ts),
		// P3: eta = now + 72h → outside 4h → NOT ready
		makeMsg("msg-c3", []string{"work:create"}, map[string]interface{}{
			"id": "ready-a02", "title": "P3 someday", "type": "task",
			"for": "baron@3dl.dev", "priority": "p3",
		}, nil, ts),
	}

	items := state.Derive(testCampfire, msgs)

	p0 := items["ready-a01"]
	p3 := items["ready-a02"]
	if p0 == nil || p3 == nil {
		t.Fatal("items not found")
	}

	if !views.ReadyFilter()(p0) {
		t.Errorf("P0 item should be in ready view (eta ~1h from now)")
	}
	if !views.ReadyFilter()(p3) {
		t.Errorf("P3 item should be in ready view (ETA is for sorting, not filtering)")
	}

	// P3 should be overdue-safe (ETA ~72h from now)
	if views.OverdueFilter()(p3) {
		t.Error("freshly created P3 should not be overdue")
	}
}

// ---------------------------------------------------------------------------
// Scenario 13: Explicit ETA override — agent sets eta via work:update
// Evidence: agents frequently adjust ETA during work
// ---------------------------------------------------------------------------
func TestIntegration_ETAOverrideAffectsReadyView(t *testing.T) {
	ts := now()
	farFuture := time.Now().Add(48 * time.Hour).UTC().Format(time.RFC3339)
	nearFuture := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)

	msgs := []store.MessageRecord{
		makeMsg("msg-c", []string{"work:create"}, map[string]interface{}{
			"id": "ready-b01", "title": "ETA test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
			"eta": farFuture,
		}, nil, ts),
	}

	// With far-future ETA: still ready (ETA is for sorting, not filtering)
	items := state.Derive(testCampfire, msgs)
	if !views.ReadyFilter()(items["ready-b01"]) {
		t.Error("item with far-future ETA should still be ready (ETA is for sorting only)")
	}

	// Update ETA to near-future: still ready
	msgs = append(msgs, makeMsg("msg-u", []string{"work:update"}, map[string]interface{}{
		"target": "msg-c",
		"eta":    nearFuture,
	}, []string{"msg-c"}, ts+1000))

	items = state.Derive(testCampfire, msgs)
	if !views.ReadyFilter()(items["ready-b01"]) {
		t.Error("item with near-future ETA should be ready")
	}
}

// ---------------------------------------------------------------------------
// Scenario 14: Multiple items, delegated view shows only delegated-and-active
// Evidence: delegated view is key for managers tracking agent work
// ---------------------------------------------------------------------------
func TestIntegration_DelegatedViewFiltering(t *testing.T) {
	ts := now()
	baron := "baron@3dl.dev"
	agent1 := "agent-1"
	agent2 := "agent-2"

	msgs := []store.MessageRecord{
		// Item for baron, delegated to agent1, agent claims (active)
		makeMsgFrom("msg-c1", baron, []string{"work:create"}, map[string]interface{}{
			"id": "ready-d01", "title": "Delegated active", "type": "task",
			"for": baron, "priority": "p1",
		}, nil, ts),
		makeMsgFrom("msg-del1", baron, []string{"work:delegate"}, map[string]interface{}{
			"target": "msg-c1", "to": agent1,
		}, []string{"msg-c1"}, ts+1000),
		makeMsgFrom("msg-cl1", agent1, []string{"work:claim"}, map[string]interface{}{
			"target": "msg-c1",
		}, []string{"msg-c1"}, ts+2000),

		// Item for baron, delegated to agent2, still inbox (not claimed)
		makeMsgFrom("msg-c2", baron, []string{"work:create"}, map[string]interface{}{
			"id": "ready-d02", "title": "Delegated not claimed", "type": "task",
			"for": baron, "priority": "p2",
		}, nil, ts),
		makeMsgFrom("msg-del2", baron, []string{"work:delegate"}, map[string]interface{}{
			"target": "msg-c2", "to": agent2,
		}, []string{"msg-c2"}, ts+1000),

		// Item for someone else — should not appear in baron's delegated view
		makeMsgFrom("msg-c3", "other", []string{"work:create"}, map[string]interface{}{
			"id": "ready-d03", "title": "Not for baron", "type": "task",
			"for": "other@3dl.dev", "priority": "p1",
		}, nil, ts),
	}

	items := state.Derive(testCampfire, msgs)

	delegated := views.DelegatedFilter(baron)

	// Item 1: for=baron, by=agent1, active → YES
	if !delegated(items["ready-d01"]) {
		t.Error("ready-d01 should appear in baron's delegated view (active, delegated)")
	}
	// Item 2: for=baron, by=agent2, but status=inbox (not active) → NO
	// delegate sets by but doesn't claim; claim sets status=active
	if delegated(items["ready-d02"]) {
		t.Error("ready-d02 should NOT appear in delegated view (not active/claimed)")
	}
	// Item 3: for=other → NO
	if delegated(items["ready-d03"]) {
		t.Error("ready-d03 should NOT appear in baron's delegated view (wrong for)")
	}
}

// ---------------------------------------------------------------------------
// Scenario 15: rd complete payload flows through Derive to done state
// Evidence: 2,894 uses of rd complete in session logs
//
// rd complete sends a work:close message with completePayload — an extended
// payload that adds branch and session fields on top of the base closePayload.
// Derive() uses closePayload (target/resolution/reason only), so the extra
// fields must be silently ignored.
//
// Sequence:
//   work:create → work:claim → work:close (completePayload with branch+session)
//
// After close: status=done, IsTerminal=true, NOT in ready/work/my-work views.
// ---------------------------------------------------------------------------
func TestIntegration_CompletePayloadDerives(t *testing.T) {
	ts := now()
	agentKey := "agent-impl-complete"

	msgs := []store.MessageRecord{
		makeMsgFrom("msg-cpc-create", "human-pubkey", []string{"work:create"}, map[string]interface{}{
			"id": "ready-cpc01", "title": "Implement feature X", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsgFrom("msg-cpc-claim", agentKey, []string{"work:claim"}, map[string]interface{}{
			"target": "msg-cpc-create",
		}, []string{"msg-cpc-create"}, ts+1000),
		// completePayload: includes branch and session in addition to base closePayload fields.
		// Derive() decodes via closePayload (target/resolution/reason) — extra fields must be ignored.
		makeMsgFrom("msg-cpc-close", agentKey, []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target":     "msg-cpc-create",
			"resolution": "done",
			"reason":     "Implemented feature",
			"branch":     "work/ready-xyz",
			"session":    "sess-123",
		}, []string{"msg-cpc-create"}, ts+2000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-cpc01"]
	if item == nil {
		t.Fatal("item not found")
	}

	if item.Status != state.StatusDone {
		t.Errorf("expected status=done, got %q", item.Status)
	}
	if !state.IsTerminal(item) {
		t.Error("expected IsTerminal=true after work:close(done)")
	}

	// Terminal items must NOT appear in active views.
	if views.WorkFilter()(item) {
		t.Error("done item must not appear in work view")
	}
	if views.ReadyFilter()(item) {
		t.Error("done item must not appear in ready view")
	}
	if views.MyWorkFilter(agentKey)(item) {
		t.Error("done item must not appear in my-work view")
	}
}

// ---------------------------------------------------------------------------
// Scenario 16: rd complete without branch/session — minimal path
// Evidence: minimal rd complete <id> --reason "done" invocation
//
// The branch and session flags are optional in completeCmd. A minimal close
// sends only target+resolution+reason (same as base closePayload). Derive()
// must handle this identically to the full payload.
// ---------------------------------------------------------------------------
func TestIntegration_CompleteWithoutBranch(t *testing.T) {
	ts := now()
	agentKey := "agent-impl-nobranch"

	msgs := []store.MessageRecord{
		makeMsgFrom("msg-cwb-create", "human-pubkey", []string{"work:create"}, map[string]interface{}{
			"id": "ready-cwb01", "title": "Quick fix", "type": "task",
			"for": "baron@3dl.dev", "priority": "p2",
		}, nil, ts),
		makeMsgFrom("msg-cwb-claim", agentKey, []string{"work:claim"}, map[string]interface{}{
			"target": "msg-cwb-create",
		}, []string{"msg-cwb-create"}, ts+1000),
		// Minimal payload: no branch, no session — matches `rd complete <id> --reason "done"`.
		makeMsgFrom("msg-cwb-close", agentKey, []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target":     "msg-cwb-create",
			"resolution": "done",
			"reason":     "done",
		}, []string{"msg-cwb-create"}, ts+2000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-cwb01"]
	if item == nil {
		t.Fatal("item not found")
	}

	if item.Status != state.StatusDone {
		t.Errorf("expected status=done, got %q", item.Status)
	}
	if !state.IsTerminal(item) {
		t.Error("expected IsTerminal=true after work:close(done)")
	}

	// Terminal items must NOT appear in active views.
	if views.WorkFilter()(item) {
		t.Error("done item must not appear in work view")
	}
	if views.ReadyFilter()(item) {
		t.Error("done item must not appear in ready view")
	}
	if views.MyWorkFilter(agentKey)(item) {
		t.Error("done item must not appear in my-work view")
	}
}

// ---------------------------------------------------------------------------
// Scenario 17: Status alias end-to-end
// Evidence: 72 uses of rd update --status in_progress
// ---------------------------------------------------------------------------
func TestIntegration_StatusAliasEndToEnd(t *testing.T) {
	ts := now()
	agentKey := "agent-key-alias-test"

	msgs := []store.MessageRecord{
		makeMsgFrom("msg-create-alias", "human-pubkey", []string{"work:create"}, map[string]interface{}{
			"id": "ready-alias-001", "title": "Alias test item", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsgFrom("msg-status-alias", agentKey, []string{"work:status", "work:status:active"}, map[string]interface{}{
			"target": "msg-create-alias", "to": "active",
		}, []string{"msg-create-alias"}, ts+1000),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-alias-001"]
	if item == nil {
		t.Fatal("item not found")
	}
	if item.Status != state.StatusActive {
		t.Errorf("expected status=active (canonical), got %q", item.Status)
	}
	if item.Status == "in_progress" {
		t.Error("derived state must not contain alias 'in_progress'")
	}
	if item.By != "" {
		t.Errorf("expected by empty (status alone doesn't claim), got %q", item.By)
	}
	if !views.WorkFilter()(item) {
		t.Error("item should appear in work view (status=active)")
	}
	if views.MyWorkFilter(agentKey)(item) {
		t.Error("item should NOT appear in my-work view for agent (no claim)")
	}
	if !views.ReadyFilter()(item) {
		t.Error("item should appear in ready view")
	}
}

// ---------------------------------------------------------------------------
// Scenario 18: All aliases produce canonical state
// ---------------------------------------------------------------------------
func TestIntegration_AllAliasesProduceCanonicalState(t *testing.T) {
	ts := now()

	// active (from in_progress alias)
	activeMsgs := []store.MessageRecord{
		makeMsg("msg-create-a1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-alias-a1", "title": "in_progress alias", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-status-a1", []string{"work:status", "work:status:active"}, map[string]interface{}{
			"target": "msg-create-a1", "to": "active",
		}, []string{"msg-create-a1"}, ts+1000),
	}
	activeItems := state.Derive(testCampfire, activeMsgs)
	activeItem := activeItems["ready-alias-a1"]
	if activeItem == nil {
		t.Fatal("ready-alias-a1 not found")
	}
	if activeItem.Status != state.StatusActive {
		t.Errorf("in_progress alias: expected active, got %q", activeItem.Status)
	}
	if !views.WorkFilter()(activeItem) {
		t.Error("in_progress alias: expected WorkFilter=true")
	}
	if state.IsTerminal(activeItem) {
		t.Error("in_progress alias: expected IsTerminal=false")
	}

	// inbox (from open alias)
	inboxMsgs := []store.MessageRecord{
		makeMsg("msg-create-b1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-alias-b1", "title": "open alias", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-status-b1", []string{"work:status", "work:status:inbox"}, map[string]interface{}{
			"target": "msg-create-b1", "to": "inbox",
		}, []string{"msg-create-b1"}, ts+1000),
	}
	inboxItems := state.Derive(testCampfire, inboxMsgs)
	inboxItem := inboxItems["ready-alias-b1"]
	if inboxItem == nil {
		t.Fatal("ready-alias-b1 not found")
	}
	if inboxItem.Status != state.StatusInbox {
		t.Errorf("open alias: expected inbox, got %q", inboxItem.Status)
	}
	if views.WorkFilter()(inboxItem) {
		t.Error("open alias: expected WorkFilter=false")
	}
	if state.IsTerminal(inboxItem) {
		t.Error("open alias: expected IsTerminal=false")
	}

	// done (from closed/completed alias, via work:close)
	doneMsgs := []store.MessageRecord{
		makeMsg("msg-create-c1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-alias-c1", "title": "closed alias", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-close-c1", []string{"work:close", "work:resolution:done"}, map[string]interface{}{
			"target": "msg-create-c1", "resolution": "done", "reason": "completed via alias",
		}, []string{"msg-create-c1"}, ts+1000),
	}
	doneItems := state.Derive(testCampfire, doneMsgs)
	doneItem := doneItems["ready-alias-c1"]
	if doneItem == nil {
		t.Fatal("ready-alias-c1 not found")
	}
	if doneItem.Status != state.StatusDone {
		t.Errorf("closed alias: expected done, got %q", doneItem.Status)
	}
	if !state.IsTerminal(doneItem) {
		t.Error("closed alias: expected IsTerminal=true")
	}
	if views.WorkFilter()(doneItem) {
		t.Error("closed alias: expected WorkFilter=false")
	}
	if views.ReadyFilter()(doneItem) {
		t.Error("closed alias: expected ReadyFilter=false")
	}
}
