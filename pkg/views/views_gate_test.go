package views_test

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/3dl-dev/ready/pkg/views"
)

// makeGatedItem builds a state.Item in waiting+gate state for testing.
func makeGatedItem(id, gateMsgID string) *state.Item {
	item := makeItem(id, state.StatusWaiting, "p1", "", "boss@test.com", "agent@test.com")
	item.WaitingType = "gate"
	item.WaitingOn = "Needs human review"
	item.WaitingSince = "2026-03-25T10:00:00Z"
	item.GateMsgID = gateMsgID
	return item
}

// TestGatesFilter_PendingGate verifies that a waiting item with GateMsgID appears.
func TestGatesFilter_PendingGate(t *testing.T) {
	f := views.GatesFilter()

	item := makeGatedItem("t1", "msg-gate-123")
	if !f(item) {
		t.Error("expected gated item to appear in gates view")
	}
}

// TestGatesFilter_NoGate verifies that a waiting item without a gate does not appear.
func TestGatesFilter_NoGate(t *testing.T) {
	f := views.GatesFilter()

	// Waiting but not a gate (e.g. waiting on a vendor).
	item := makeItem("t1", state.StatusWaiting, "p1", "", "boss@test.com", "agent@test.com")
	item.WaitingType = "vendor"
	item.GateMsgID = ""
	if f(item) {
		t.Error("expected non-gate waiting item to not appear in gates view")
	}
}

// TestGatesFilter_GateMsgIDEmptyExcludes verifies that a waiting item with
// waiting_type=gate but no GateMsgID does not appear (gate already resolved).
func TestGatesFilter_GateMsgIDEmptyExcludes(t *testing.T) {
	f := views.GatesFilter()

	item := makeItem("t1", state.StatusWaiting, "p1", "", "boss@test.com", "agent@test.com")
	item.WaitingType = "gate"
	item.GateMsgID = "" // cleared after resolution
	if f(item) {
		t.Error("expected item with empty GateMsgID to not appear in gates view")
	}
}

// TestGatesFilter_BlockedItemAppears verifies that a gate raised on a BLOCKED item
// (status=blocked, not waiting — the ordinary case for a design gate, since the
// ruling is often exactly what unblocks the chain) still appears in the gates
// view. Regression test for ready-e0e: a gate on a blocked item used to be
// invisible because GatesFilter required status==waiting exactly.
func TestGatesFilter_BlockedItemAppears(t *testing.T) {
	f := views.GatesFilter()

	item := makeItem("t1", state.StatusBlocked, "p1", "", "boss@test.com", "agent@test.com")
	item.WaitingType = "gate"
	item.WaitingOn = "budget approval"
	item.GateMsgID = "msg-gate-123"
	if !f(item) {
		t.Error("expected a gate raised on a blocked item to appear in the gates view (ready-e0e)")
	}
}

// TestGatesFilter_BlockedNoGateExcludes verifies a merely-blocked item (no gate
// declared) still does not appear — blocking alone is not an escalation.
func TestGatesFilter_BlockedNoGateExcludes(t *testing.T) {
	f := views.GatesFilter()

	item := makeItem("t1", state.StatusBlocked, "p1", "", "boss@test.com", "agent@test.com")
	if f(item) {
		t.Error("expected a plain blocked item with no gate to not appear in the gates view")
	}
}

// TestGatesFilter_ActiveItemExcludes verifies that an active item does not appear.
func TestGatesFilter_ActiveItemExcludes(t *testing.T) {
	f := views.GatesFilter()

	item := makeItem("t1", state.StatusActive, "p1", "", "boss@test.com", "agent@test.com")
	item.GateMsgID = "msg-gate-123" // shouldn't matter — status is active
	if f(item) {
		t.Error("expected active item to not appear in gates view")
	}
}

// TestGatesFilter_TerminalItemExcludes verifies that a done item does not appear.
func TestGatesFilter_TerminalItemExcludes(t *testing.T) {
	f := views.GatesFilter()

	item := makeItem("t1", state.StatusDone, "p1", "", "boss@test.com", "agent@test.com")
	item.WaitingType = "gate"
	item.GateMsgID = "msg-gate-123"
	if f(item) {
		t.Error("expected done item to not appear in gates view")
	}
}

// TestGatesFilter_StatusClauseIsExhaustive pins the STATUS clause of
// GatesFilter — `Status == waiting || Status == blocked` — one status at a time,
// over EVERY status rd can mint (ready-d19).
//
// WHY A TABLE AND NOT MORE CLI COVERAGE: every case below holds waiting_type and
// GateMsgID fixed at their gate-present values, so the ONLY thing that can decide
// membership is the status clause. That makes the test discriminate per clause:
// deleting `Status == waiting` reddens the waiting row, deleting
// `Status == blocked` reddens the blocked row, and deleting the clause entirely
// reddens every excluded row. No end-to-end test can do this — the write path
// cannot produce an active/inbox/scheduled item that still carries a live
// GateMsgID (rd gate forces waiting; the terminal branch of
// pkg/sync.applyDepAndGateStatus strips the field on close), so at the CLI
// altitude the status clause is unfalsifiable. ready-d19's first round asserted
// there and wrongly recorded the clause as covered.
//
// This predicate is also the single cross-implementation contract the TypeScript
// ports must agree with (web/board/src/lib/views.ts, web/board/src/board/views.ts):
// (status == waiting OR status == blocked) AND waiting_type == "gate" AND
// gate_msg_id != "". Blocked is IN — blocked-and-gated is the ordinary case for a
// design gate, since the ruling is usually what unblocks the chain (ready-e0e).
func TestGatesFilter_StatusClauseIsExhaustive(t *testing.T) {
	cases := []struct {
		status string
		want   bool
		why    string
	}{
		{state.StatusWaiting, true, "the plain pending-gate case"},
		{state.StatusBlocked, true, "ready-e0e: a gate on a blocked item is still pending — the ruling is usually what unblocks the chain"},
		{state.StatusInbox, false, "an untriaged item has no pending escalation"},
		{state.StatusActive, false, "an active item is being worked, not awaiting a ruling"},
		{state.StatusScheduled, false, "rd gate forces a scheduled item to waiting; a scheduled row here would be a stale projection"},
		{state.StatusDone, false, "resolving a gate on a closed item is impossible — approve/reject both refuse it"},
		{state.StatusCancelled, false, "resolving a gate on a closed item is impossible — approve/reject both refuse it"},
		{state.StatusFailed, false, "resolving a gate on a closed item is impossible — approve/reject both refuse it"},
	}
	f := views.GatesFilter()
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			// Gate fields held constant across every row: status is the only variable.
			item := makeItem("t1", tc.status, "p1", "", "boss@test.com", "agent@test.com")
			item.WaitingType = "gate"
			item.WaitingOn = "budget approval"
			item.GateMsgID = "msg-gate-123"
			if got := f(item); got != tc.want {
				t.Errorf("GatesFilter(status=%s) = %v, want %v — %s", tc.status, got, tc.want, tc.why)
			}
		})
	}
}

// TestNamed_GatesViewResolvable verifies that the gates view is registered
// in the Named function.
func TestNamed_GatesViewResolvable(t *testing.T) {
	f := views.Named(views.ViewGates, "")
	if f == nil {
		t.Error("expected non-nil filter for gates view")
	}
}

// TestAllNames_IncludesGates verifies that gates is in AllNames.
func TestAllNames_IncludesGates(t *testing.T) {
	found := false
	for _, name := range views.AllNames() {
		if name == views.ViewGates {
			found = true
			break
		}
	}
	if !found {
		t.Error("expected 'gates' in AllNames()")
	}
}
