// Package views implements the named view predicates for the work management
// convention. Views are defined in the convention spec §5 and implemented here
// as Go filter functions operating on derived Item state.
package views

import (
	"time"

	"github.com/campfire-net/ready/pkg/state"
)

// ViewName constants correspond to the named views in the convention spec.
const (
	ViewReady     = "ready"
	ViewWork      = "work"
	ViewPending   = "pending"
	ViewOverdue   = "overdue"
	ViewDelegated = "delegated"
	ViewMyWork    = "my-work"
	ViewGates     = "gates"
	ViewFocus     = "focus"
)

// Filter is a function that tests whether an item should appear in a view.
type Filter func(item *state.Item) bool

// Named returns the Filter for the given view name and identity.
// identity is the caller's public key hex or email — used for "for" and "by" fields.
// Returns nil if the view name is not recognized.
func Named(viewName, identity string) Filter {
	switch viewName {
	case ViewReady:
		return ReadyFilter()
	case ViewWork:
		return WorkFilter()
	case ViewPending:
		return PendingFilter()
	case ViewOverdue:
		return OverdueFilter()
	case ViewDelegated:
		return DelegatedFilter(identity)
	case ViewMyWork:
		return MyWorkFilter(identity)
	case ViewGates:
		return GatesFilter()
	case ViewFocus:
		return FocusFilter("")
	default:
		return nil
	}
}

// ReadyFilter returns items that can be worked right now:
//   - not in a terminal status (done, cancelled, failed)
//   - not blocked
//   - not scheduled (scheduled = pending a future date, state not yet accurate)
//
// ETA is for sorting (urgency), not filtering. Work due in October is still
// workable today — we don't sit idle because nothing is due within 4 hours.
func ReadyFilter() Filter {
	return func(item *state.Item) bool {
		if state.IsTerminal(item) {
			return false
		}
		if state.IsBlocked(item) {
			return false
		}
		if item.Status == state.StatusScheduled {
			return false
		}
		return true
	}
}

// WorkFilter returns items that are actively being worked on (status=active).
func WorkFilter() Filter {
	return func(item *state.Item) bool {
		return item.Status == state.StatusActive
	}
}

// PendingFilter returns items in waiting, scheduled, or blocked status.
func PendingFilter() Filter {
	return func(item *state.Item) bool {
		switch item.Status {
		case state.StatusWaiting, state.StatusScheduled, state.StatusBlocked:
			return true
		}
		return false
	}
}

// OverdueFilter returns items whose ETA is in the past and are not terminal.
func OverdueFilter() Filter {
	now := time.Now()
	return func(item *state.Item) bool {
		if state.IsTerminal(item) {
			return false
		}
		if item.ETA == "" {
			return false
		}
		eta, err := time.Parse(time.RFC3339, item.ETA)
		if err != nil {
			return false
		}
		return eta.Before(now)
	}
}

// DelegatedFilter returns items where for=identity, by!=identity, status=active.
// These are items the identity delegated to someone else that are in progress.
func DelegatedFilter(identity string) Filter {
	return func(item *state.Item) bool {
		if identity == "" {
			return false
		}
		return item.For == identity &&
			item.By != identity &&
			item.By != "" &&
			item.Status == state.StatusActive
	}
}

// MyWorkFilter returns items assigned to identity that are not terminal.
func MyWorkFilter(identity string) Filter {
	return func(item *state.Item) bool {
		if identity == "" {
			return false
		}
		return item.By == identity && !state.IsTerminal(item)
	}
}

// GatesFilter returns items that have an unfulfilled gate (status=waiting with
// waiting_type=gate and a non-empty GateMsgID). These are items awaiting human
// resolution before work can proceed.
// Convention spec §5: gates view — pending human escalations.
func GatesFilter() Filter {
	return func(item *state.Item) bool {
		return item.Status == state.StatusWaiting &&
			item.WaitingType == "gate" &&
			item.GateMsgID != ""
	}
}

// FocusFilter returns items that are in the ready view AND match the given gate type.
// If gateType is empty, it returns all ready items (equivalent to ReadyFilter).
func FocusFilter(gateType string) Filter {
	ready := ReadyFilter()
	return func(item *state.Item) bool {
		if !ready(item) {
			return false
		}
		if gateType == "" {
			return true
		}
		return item.Gate == gateType
	}
}

// LabelFilter returns a Filter that matches items carrying the given label atom.
// Matching is exact (no substring/glob). An item matches if atom appears in
// its Labels slice. The caller is responsible for AND-composing multiple
// LabelFilter predicates when more than one atom is requested.
func LabelFilter(atom string) Filter {
	return func(item *state.Item) bool {
		for _, l := range item.Labels {
			if l == atom {
				return true
			}
		}
		return false
	}
}

// Apply filters items using the provided filter function.
func Apply(items []*state.Item, f Filter) []*state.Item {
	var result []*state.Item
	for _, item := range items {
		if f(item) {
			result = append(result, item)
		}
	}
	return result
}

// AllNames returns the list of all recognized view names.
func AllNames() []string {
	return []string{
		ViewReady,
		ViewWork,
		ViewPending,
		ViewOverdue,
		ViewDelegated,
		ViewMyWork,
		ViewGates,
		ViewFocus,
	}
}
