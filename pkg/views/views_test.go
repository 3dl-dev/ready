package views_test

import (
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/3dl-dev/ready/pkg/views"
)

// makeItem builds a minimal state.Item for testing.
func makeItem(id, status, priority, eta, forParty, by string) *state.Item {
	return &state.Item{
		ID:       id,
		Title:    "Test: " + id,
		Status:   status,
		Priority: priority,
		ETA:      eta,
		For:      forParty,
		By:       by,
		Type:     "task",
	}
}

func futureETA(d time.Duration) string {
	return time.Now().Add(d).UTC().Format(time.RFC3339)
}

func pastETA(d time.Duration) string {
	return time.Now().Add(-d).UTC().Format(time.RFC3339)
}

func TestReadyFilter_NotTerminal(t *testing.T) {
	f := views.ReadyFilter()

	// Active item with near ETA should appear.
	item := makeItem("t1", state.StatusActive, "p1", futureETA(1*time.Hour), "a@b.com", "")
	if !f(item) {
		t.Error("expected active item with near ETA to be ready")
	}

	// Done item should not appear.
	item2 := makeItem("t2", state.StatusDone, "p1", futureETA(1*time.Hour), "a@b.com", "")
	if f(item2) {
		t.Error("expected done item to not be ready")
	}
}

func TestReadyFilter_Blocked(t *testing.T) {
	f := views.ReadyFilter()
	item := makeItem("t1", state.StatusBlocked, "p1", futureETA(1*time.Hour), "a@b.com", "")
	if f(item) {
		t.Error("expected blocked item to not be ready")
	}
}

func TestReadyFilter_ETAFarFuture(t *testing.T) {
	f := views.ReadyFilter()
	// ETA 10h away — still ready. ETA is for sorting, not filtering.
	// Work due in the future is still workable now.
	item := makeItem("t1", state.StatusInbox, "p2", futureETA(10*time.Hour), "a@b.com", "")
	if !f(item) {
		t.Error("expected item with ETA 10h away to still be ready (ETA is for sorting)")
	}
}

func TestReadyFilter_ScheduledExcluded(t *testing.T) {
	f := views.ReadyFilter()
	// Scheduled items are pending a future date — state isn't accurate yet.
	item := makeItem("t1", state.StatusScheduled, "p1", futureETA(1*time.Hour), "a@b.com", "")
	if f(item) {
		t.Error("expected scheduled item to not be ready")
	}
}

func TestReadyFilter_ETAWithin4h(t *testing.T) {
	f := views.ReadyFilter()
	item := makeItem("t1", state.StatusInbox, "p1", futureETA(2*time.Hour), "a@b.com", "")
	if !f(item) {
		t.Error("expected item with ETA 2h away to be ready")
	}
}

func TestWorkFilter(t *testing.T) {
	f := views.WorkFilter()

	active := makeItem("t1", state.StatusActive, "p1", "", "a@b.com", "me")
	if !f(active) {
		t.Error("expected active item to appear in work view")
	}

	inbox := makeItem("t2", state.StatusInbox, "p1", "", "a@b.com", "")
	if f(inbox) {
		t.Error("expected inbox item to not appear in work view")
	}
}

func TestPendingFilter(t *testing.T) {
	f := views.PendingFilter()

	for _, status := range []string{state.StatusWaiting, state.StatusScheduled, state.StatusBlocked} {
		item := makeItem("t1", status, "p1", "", "a@b.com", "")
		if !f(item) {
			t.Errorf("expected %s item to appear in pending view", status)
		}
	}

	for _, status := range []string{state.StatusInbox, state.StatusActive, state.StatusDone} {
		item := makeItem("t1", status, "p1", "", "a@b.com", "")
		if f(item) {
			t.Errorf("expected %s item to not appear in pending view", status)
		}
	}
}

func TestOverdueFilter(t *testing.T) {
	f := views.OverdueFilter()

	overdue := makeItem("t1", state.StatusInbox, "p1", pastETA(1*time.Hour), "a@b.com", "")
	if !f(overdue) {
		t.Error("expected past-ETA item to appear in overdue view")
	}

	future := makeItem("t2", state.StatusInbox, "p1", futureETA(1*time.Hour), "a@b.com", "")
	if f(future) {
		t.Error("expected future-ETA item to not appear in overdue view")
	}

	done := makeItem("t3", state.StatusDone, "p1", pastETA(1*time.Hour), "a@b.com", "")
	if f(done) {
		t.Error("expected done item with past ETA to not appear in overdue view")
	}
}

func TestDelegatedFilter(t *testing.T) {
	f := views.DelegatedFilter("baron@3dl.dev")

	// baron delegated to someone else, active.
	delegated := makeItem("t1", state.StatusActive, "p1", "", "baron@3dl.dev", "agent@3dl.dev")
	if !f(delegated) {
		t.Error("expected delegated item to appear in delegated view")
	}

	// baron doing it himself.
	self := makeItem("t2", state.StatusActive, "p1", "", "baron@3dl.dev", "baron@3dl.dev")
	if f(self) {
		t.Error("expected self-assigned item to not appear in delegated view")
	}

	// Not baron's item.
	other := makeItem("t3", state.StatusActive, "p1", "", "other@3dl.dev", "agent@3dl.dev")
	if f(other) {
		t.Error("expected other's item to not appear in baron's delegated view")
	}
}

func TestMyWorkFilter(t *testing.T) {
	f := views.MyWorkFilter("me@test.com")

	assigned := makeItem("t1", state.StatusActive, "p1", "", "boss@test.com", "me@test.com")
	if !f(assigned) {
		t.Error("expected assigned item to appear in my-work view")
	}

	notMe := makeItem("t2", state.StatusActive, "p1", "", "boss@test.com", "other@test.com")
	if f(notMe) {
		t.Error("expected other's item to not appear in my-work view")
	}

	done := makeItem("t3", state.StatusDone, "p1", "", "boss@test.com", "me@test.com")
	if f(done) {
		t.Error("expected done item to not appear in my-work view")
	}
}

func TestApply(t *testing.T) {
	items := []*state.Item{
		makeItem("t1", state.StatusActive, "p1", "", "a@b.com", ""),
		makeItem("t2", state.StatusDone, "p1", "", "a@b.com", ""),
		makeItem("t3", state.StatusActive, "p2", "", "a@b.com", ""),
	}

	f := views.WorkFilter()
	result := views.Apply(items, f)
	if len(result) != 2 {
		t.Errorf("expected 2 items, got %d", len(result))
	}
}

func TestNamed_ReturnsNilForUnknown(t *testing.T) {
	f := views.Named("nonexistent", "")
	if f != nil {
		t.Error("expected nil filter for unknown view name")
	}
}

func TestNamed_AllViewsResolvable(t *testing.T) {
	for _, name := range views.AllNames() {
		f := views.Named(name, "test@test.com")
		if f == nil {
			t.Errorf("expected non-nil filter for view %q", name)
		}
	}
}

func TestFocusFilter_NoGate(t *testing.T) {
	f := views.FocusFilter("")

	// Active item with near ETA and no gate — should appear.
	item := makeItem("t1", state.StatusActive, "p1", futureETA(1*time.Hour), "a@b.com", "")
	if !f(item) {
		t.Error("expected active near-ETA item to appear in focus view with no gate filter")
	}

	// Done item — should not appear.
	done := makeItem("t2", state.StatusDone, "p1", futureETA(1*time.Hour), "a@b.com", "")
	if f(done) {
		t.Error("expected done item to not appear in focus view")
	}

	// Item with far ETA — still appears (ETA is for sorting, not filtering).
	far := makeItem("t3", state.StatusActive, "p1", futureETA(10*time.Hour), "a@b.com", "")
	if !f(far) {
		t.Error("expected far-ETA active item to appear in focus view (ETA is for sorting)")
	}

	// Scheduled item — should NOT appear (pending a date).
	sched := makeItem("t4", state.StatusScheduled, "p1", futureETA(1*time.Hour), "a@b.com", "")
	if f(sched) {
		t.Error("expected scheduled item to not appear in focus view")
	}
}

func TestFocusFilter_WithGate(t *testing.T) {
	// Add gate to makeItem by constructing directly.
	nearETA := futureETA(1 * time.Hour)

	designItem := &state.Item{
		ID: "t1", Status: state.StatusActive, Priority: "p1",
		ETA: nearETA, For: "a@b.com", Type: "task", Gate: "design",
	}
	noGateItem := &state.Item{
		ID: "t2", Status: state.StatusActive, Priority: "p1",
		ETA: nearETA, For: "a@b.com", Type: "task", Gate: "",
	}
	budgetItem := &state.Item{
		ID: "t3", Status: state.StatusActive, Priority: "p1",
		ETA: nearETA, For: "a@b.com", Type: "task", Gate: "budget",
	}

	f := views.FocusFilter("design")

	if !f(designItem) {
		t.Error("expected design gate item to appear with FocusFilter(design)")
	}
	if f(noGateItem) {
		t.Error("expected no-gate item to not appear with FocusFilter(design)")
	}
	if f(budgetItem) {
		t.Error("expected budget gate item to not appear with FocusFilter(design)")
	}
}

// TestReadyFilter_AllTerminalStatuses verifies all terminal statuses are excluded.
func TestReadyFilter_AllTerminalStatuses(t *testing.T) {
	f := views.ReadyFilter()
	terminalStatuses := []string{state.StatusDone, state.StatusCancelled, state.StatusFailed}

	for _, status := range terminalStatuses {
		item := makeItem("t1", status, "p1", futureETA(1*time.Hour), "a@b.com", "")
		if f(item) {
			t.Errorf("expected terminal status %q to be excluded from ready view", status)
		}
	}
}

// TestDelegatedFilter_NoByEmpty verifies that items without a "by" assignment are excluded.
func TestDelegatedFilter_NoByEmpty(t *testing.T) {
	f := views.DelegatedFilter("baron@3dl.dev")

	// Item with for=identity but by="" should not appear.
	item := makeItem("t1", state.StatusActive, "p1", "", "baron@3dl.dev", "")
	if f(item) {
		t.Error("expected item with empty 'by' to not appear in delegated view")
	}
}

// TestDelegatedFilter_RequiresActiveStatus verifies non-active items are excluded.
func TestDelegatedFilter_RequiresActiveStatus(t *testing.T) {
	f := views.DelegatedFilter("baron@3dl.dev")

	for _, status := range []string{state.StatusInbox, state.StatusWaiting, state.StatusDone} {
		item := makeItem("t1", status, "p1", "", "baron@3dl.dev", "agent@3dl.dev")
		if f(item) {
			t.Errorf("expected delegated item with status %q to not appear", status)
		}
	}
}

// TestMyWorkFilter_IncludesAllNonTerminalStatuses verifies all non-terminal statuses are included.
func TestMyWorkFilter_IncludesAllNonTerminalStatuses(t *testing.T) {
	f := views.MyWorkFilter("me@test.com")
	nonTerminalStatuses := []string{
		state.StatusInbox, state.StatusActive, state.StatusScheduled,
		state.StatusWaiting, state.StatusBlocked,
	}

	for _, status := range nonTerminalStatuses {
		item := makeItem("t1", status, "p1", "", "boss@test.com", "me@test.com")
		if !f(item) {
			t.Errorf("expected my-work item with status %q to appear", status)
		}
	}
}

// TestApply_EmptyInput returns empty result for empty items list.
func TestApply_EmptyInput(t *testing.T) {
	f := views.ReadyFilter()
	result := views.Apply([]*state.Item{}, f)
	if len(result) != 0 {
		t.Errorf("expected empty result for empty input, got %d items", len(result))
	}
}

// TestApply_AllItemsMatching returns all items when all match the filter.
func TestApply_AllItemsMatching(t *testing.T) {
	items := []*state.Item{
		makeItem("t1", state.StatusActive, "p1", "", "a@b.com", "me"),
		makeItem("t2", state.StatusActive, "p1", "", "a@b.com", "me"),
		makeItem("t3", state.StatusActive, "p1", "", "a@b.com", "me"),
	}

	f := views.WorkFilter()
	result := views.Apply(items, f)
	if len(result) != 3 {
		t.Errorf("expected 3 items when all match, got %d", len(result))
	}
}

// TestApply_NoItemsMatching returns empty result when no items match.
func TestApply_NoItemsMatching(t *testing.T) {
	items := []*state.Item{
		makeItem("t1", state.StatusInbox, "p1", "", "a@b.com", ""),
		makeItem("t2", state.StatusDone, "p1", "", "a@b.com", ""),
		makeItem("t3", state.StatusWaiting, "p1", "", "a@b.com", ""),
	}

	f := views.WorkFilter() // only active items
	result := views.Apply(items, f)
	if len(result) != 0 {
		t.Errorf("expected 0 items when none match, got %d", len(result))
	}
}

// TestApply_PreservesOrder preserves the order of filtered items.
func TestApply_PreservesOrder(t *testing.T) {
	items := []*state.Item{
		makeItem("t1", state.StatusActive, "p1", "", "a@b.com", ""),
		makeItem("t2", state.StatusDone, "p1", "", "a@b.com", ""),
		makeItem("t3", state.StatusActive, "p1", "", "a@b.com", ""),
		makeItem("t4", state.StatusInbox, "p1", "", "a@b.com", ""),
		makeItem("t5", state.StatusActive, "p1", "", "a@b.com", ""),
	}

	f := views.WorkFilter()
	result := views.Apply(items, f)
	if len(result) != 3 {
		t.Errorf("expected 3 active items, got %d", len(result))
	}
	if result[0].ID != "t1" || result[1].ID != "t3" || result[2].ID != "t5" {
		t.Error("expected order to be preserved: t1, t3, t5")
	}
}

// TestReadyFilter_MultipleFiltersCanBeComposed verifies filters can be chained.
func TestReadyFilter_MultipleFiltersCanBeComposed(t *testing.T) {
	readyFilter := views.ReadyFilter()
	myWorkFilter := views.MyWorkFilter("me@test.com")

	// Manually compose: item must be ready AND assigned to me.
	item := &state.Item{
		ID: "t1", Status: state.StatusActive, Priority: "p1",
		ETA: futureETA(1 * time.Hour), For: "a@b.com", By: "me@test.com", Type: "task",
	}
	if !readyFilter(item) {
		t.Error("expected ready filter to pass on active item")
	}
	if !myWorkFilter(item) {
		t.Error("expected my-work filter to pass on item assigned to me")
	}

	// Now check a done item — ready filter should fail.
	doneItem := &state.Item{
		ID: "t2", Status: state.StatusDone, Priority: "p1",
		ETA: futureETA(1 * time.Hour), For: "a@b.com", By: "me@test.com", Type: "task",
	}
	if readyFilter(doneItem) {
		t.Error("expected ready filter to reject done item")
	}
}

// TestPendingFilter_ExcludesOtherStatuses verifies non-pending statuses are excluded.
func TestPendingFilter_ExcludesOtherStatuses(t *testing.T) {
	f := views.PendingFilter()

	excludedStatuses := []string{state.StatusInbox, state.StatusActive, state.StatusDone,
		state.StatusCancelled, state.StatusFailed}

	for _, status := range excludedStatuses {
		item := makeItem("t1", status, "p1", "", "a@b.com", "")
		if f(item) {
			t.Errorf("expected status %q to be excluded from pending view", status)
		}
	}
}

// TestOverdueFilter_AllTerminalStatuses verifies no terminal statuses appear as overdue.
func TestOverdueFilter_AllTerminalStatuses(t *testing.T) {
	f := views.OverdueFilter()
	terminalStatuses := []string{state.StatusDone, state.StatusCancelled, state.StatusFailed}

	for _, status := range terminalStatuses {
		item := makeItem("t1", status, "p1", pastETA(1*time.Hour), "a@b.com", "")
		if f(item) {
			t.Errorf("expected terminal status %q to not appear as overdue", status)
		}
	}
}

// TestNamed_DelegatedRequiresIdentity verifies delegated view requires identity.
func TestNamed_DelegatedRequiresIdentity(t *testing.T) {
	// With empty identity, delegated filter should return false for all items.
	f := views.Named(views.ViewDelegated, "")
	item := makeItem("t1", state.StatusActive, "p1", "", "user@test.com", "agent@test.com")
	if f(item) {
		t.Error("expected delegated filter with empty identity to return false")
	}
}

// TestNamed_MyWorkRequiresIdentity verifies my-work view requires identity.
func TestNamed_MyWorkRequiresIdentity(t *testing.T) {
	// With empty identity, my-work filter should return false for all items.
	f := views.Named(views.ViewMyWork, "")
	item := makeItem("t1", state.StatusActive, "p1", "", "boss@test.com", "me@test.com")
	if f(item) {
		t.Error("expected my-work filter with empty identity to return false")
	}
}

// TestFocusFilter_MatchesReadyBehavior verifies FocusFilter("") behaves like ReadyFilter.
func TestFocusFilter_MatchesReadyBehavior(t *testing.T) {
	readyFilter := views.ReadyFilter()
	focusFilter := views.FocusFilter("")

	testCases := []*state.Item{
		makeItem("t1", state.StatusActive, "p1", futureETA(1*time.Hour), "a@b.com", ""),
		makeItem("t2", state.StatusDone, "p1", "", "a@b.com", ""),
		makeItem("t3", state.StatusScheduled, "p1", "", "a@b.com", ""),
		makeItem("t4", state.StatusBlocked, "p1", "", "a@b.com", ""),
	}

	for _, item := range testCases {
		readyResult := readyFilter(item)
		focusResult := focusFilter(item)
		if readyResult != focusResult {
			t.Errorf("expected FocusFilter(\"\") to match ReadyFilter for item %s, got ready=%v focus=%v",
				item.ID, readyResult, focusResult)
		}
	}
}
