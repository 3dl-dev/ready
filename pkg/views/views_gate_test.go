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
