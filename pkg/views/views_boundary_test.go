package views_test

import (
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/3dl-dev/ready/pkg/views"
)

// TestReadyFilter_ETADoesNotFilter tests that ETA no longer gates the ready view.
// ETA is for urgency sorting, not for filtering. Scheduled status is the gate
// for work that literally can't be done yet.
func TestReadyFilter_ETADoesNotFilter(t *testing.T) {
	f := views.ReadyFilter()

	// Item with ETA far in the future is still ready — it CAN be worked now.
	future := time.Now().Add(720 * time.Hour).UTC().Format(time.RFC3339) // 30 days
	item := makeItem("t1", state.StatusInbox, "p1", future, "a@b.com", "")
	if !f(item) {
		t.Error("expected item with ETA 30 days away to be ready (ETA is for sorting)")
	}

	// Item with ETA in the near future is also ready.
	near := time.Now().Add(1 * time.Hour).UTC().Format(time.RFC3339)
	item2 := makeItem("t2", state.StatusInbox, "p1", near, "a@b.com", "")
	if !f(item2) {
		t.Error("expected item with ETA 1h away to be ready")
	}

	// Only scheduled status excludes from ready.
	item3 := makeItem("t3", state.StatusScheduled, "p1", near, "a@b.com", "")
	if f(item3) {
		t.Error("expected scheduled item to NOT be ready regardless of ETA")
	}
}

// TestReadyFilter_EmptyETA tests that empty ETA makes item always ready.
func TestReadyFilter_EmptyETA(t *testing.T) {
	f := views.ReadyFilter()

	// Empty ETA should be ready regardless of status (as long as not terminal/blocked).
	item := makeItem("t1", state.StatusInbox, "p1", "", "a@b.com", "")
	if !f(item) {
		t.Error("expected item with empty ETA to be ready")
	}

	// Even with far-future implications, empty ETA means no ETA constraint.
	item2 := makeItem("t2", state.StatusActive, "p2", "", "a@b.com", "me")
	if !f(item2) {
		t.Error("expected active item with empty ETA to be ready")
	}
}

// TestReadyFilter_UnparseableETA tests that malformed ETA makes item ready.
func TestReadyFilter_UnparseableETA(t *testing.T) {
	f := views.ReadyFilter()

	// Garbage ETA should be treated as ready (if err != nil branch).
	item := makeItem("t1", state.StatusInbox, "p1", "not-a-date", "a@b.com", "")
	if !f(item) {
		t.Error("expected item with unparseable ETA to be ready")
	}

	// Another malformed format.
	item2 := makeItem("t2", state.StatusInbox, "p1", "2026-13-45", "a@b.com", "")
	if !f(item2) {
		t.Error("expected item with invalid date to be ready")
	}
}

// TestOverdueFilter_EmptyETA tests that empty ETA is never overdue.
func TestOverdueFilter_EmptyETA(t *testing.T) {
	f := views.OverdueFilter()

	// Empty ETA cannot be overdue (if item.ETA == "" returns false).
	item := makeItem("t1", state.StatusInbox, "p1", "", "a@b.com", "")
	if f(item) {
		t.Error("expected item with empty ETA to NOT be overdue")
	}
}

// TestOverdueFilter_UnparseableETA tests that malformed ETA is never overdue.
func TestOverdueFilter_UnparseableETA(t *testing.T) {
	f := views.OverdueFilter()

	// Unparseable ETA should not be overdue (if err != nil returns false).
	item := makeItem("t1", state.StatusInbox, "p1", "garbage", "a@b.com", "")
	if f(item) {
		t.Error("expected item with unparseable ETA to NOT be overdue")
	}

	item2 := makeItem("t2", state.StatusActive, "p1", "not-a-timestamp", "a@b.com", "me")
	if f(item2) {
		t.Error("expected item with malformed ETA to NOT be overdue")
	}
}

// TestOverdueFilter_ExactNow tests the boundary at exactly now.
func TestOverdueFilter_ExactNow(t *testing.T) {
	f := views.OverdueFilter()

	// Item with ETA 1 second in the past should be overdue.
	pastETA := time.Now().Add(-1 * time.Second).UTC().Format(time.RFC3339)
	item := makeItem("t1", state.StatusInbox, "p1", pastETA, "a@b.com", "")
	if !f(item) {
		t.Error("expected item with ETA 1s in past to be overdue")
	}

	// Item with ETA at exactly now is not strictly before now, so not overdue.
	// We can't test "exactly now" due to timing, but we can test 1 second in future.
	futureETA := time.Now().Add(1 * time.Second).UTC().Format(time.RFC3339)
	item2 := makeItem("t2", state.StatusInbox, "p1", futureETA, "a@b.com", "")
	if f(item2) {
		t.Error("expected item with ETA 1s in future to NOT be overdue")
	}
}

// TestDelegatedFilter_EmptyIdentity tests that empty identity always returns false.
func TestDelegatedFilter_EmptyIdentity(t *testing.T) {
	f := views.DelegatedFilter("")

	// Even a delegated active item should not match with empty identity.
	item := makeItem("t1", state.StatusActive, "p1", "", "baron@3dl.dev", "agent@3dl.dev")
	if f(item) {
		t.Error("expected DelegatedFilter(\"\") to always return false")
	}

	item2 := makeItem("t2", state.StatusActive, "p1", "", "user@test.com", "other@test.com")
	if f(item2) {
		t.Error("expected DelegatedFilter(\"\") to always return false for any item")
	}
}

// TestMyWorkFilter_EmptyIdentity tests that empty identity always returns false.
func TestMyWorkFilter_EmptyIdentity(t *testing.T) {
	f := views.MyWorkFilter("")

	// Even an active item assigned to someone should not match with empty identity.
	item := makeItem("t1", state.StatusActive, "p1", "", "boss@test.com", "me@test.com")
	if f(item) {
		t.Error("expected MyWorkFilter(\"\") to always return false")
	}

	item2 := makeItem("t2", state.StatusInbox, "p1", "", "boss@test.com", "agent@test.com")
	if f(item2) {
		t.Error("expected MyWorkFilter(\"\") to always return false for any item")
	}
}

// TestApply_NilFilter tests behavior when nil Filter is passed to Apply.
// This should panic because nil functions cannot be called.
func TestApply_NilFilter(t *testing.T) {
	items := []*state.Item{
		makeItem("t1", state.StatusActive, "p1", "", "a@b.com", ""),
	}

	// Calling Apply with nil Filter should panic.
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic when Apply is called with nil Filter")
		}
	}()

	views.Apply(items, nil)
	t.Error("should not reach here; Apply(items, nil) should panic")
}
