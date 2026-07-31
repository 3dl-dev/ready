// Default-armed PublishGuard (ready-fcf).
//
// PublishGuard (client.go) started life as a bare nil-able hook: pkg/sync's
// own init() (relayclass.go) was the ONLY thing that ever gave it board
// semantics, by REPLACING the variable wholesale with a closure that knows
// what a reserved coordinate is. That covers every caller that links
// pkg/sync, but arming the guard was therefore a LINK-TIME SIDE EFFECT of
// importing pkg/sync — a binary that imports pkg/nostr WITHOUT pkg/sync, or a
// file living INSIDE pkg/nostr itself (which cannot import pkg/sync at all;
// doing so would be an import cycle), never ran that init and found
// PublishGuard == nil. PROVEN (ready-fcf): a new pkg/nostr/*.go file calling
// bare Publish with a reserved coordinate reached the network with
// go build/vet/test ./... all green.
//
// The fix lives in this file: pkg/nostr now carries the MINIMUM board
// knowledge needed to fail closed on its own — its own copy of the reserved
// coordinate and the "does this event address it" check — and arms
// PublishGuard with that by default at package load, in every binary,
// including its own test binary. pkg/sync's init still runs afterward (in any
// binary that links it) and REPLACES this default with the production
// opt-in-aware closure; every existing caller's behavior is unchanged (the
// full test suite proves it), because a production write always goes through
// a binary that links pkg/sync. What changes is the binary or file that
// DOESN'T: it now inherits fail-closed instead of wide-open.
package nostr

import (
	"context"
	"fmt"
	"strings"
)

const (
	// defaultReservedBoardD is this package's own copy of pkg/sync's
	// reservedProductionBoardD (pkg/sync/nostroutbound.go) — THIS repo's own
	// live board D-tag. Duplicated, not imported: pkg/nostr cannot import
	// pkg/sync without a cycle, and holding this one string is the entire
	// point of ready-fcf — the minimum board knowledge that lets the guard
	// arm itself instead of waiting on some other package's init() to run
	// first.
	defaultReservedBoardD = "ready"
	// defaultBoardEventKind mirrors pkg/sync's KindBoard (30301, NIP-34
	// addressable) — the kind of the board event itself, as opposed to a
	// card/status event that merely REFERENCES a board via an "a" tag.
	defaultBoardEventKind = 30301
)

// DefaultReservedBoardD and DefaultBoardEventKind expose the two constants
// above, EXPORTED FOR EXACTLY ONE REASON: so pkg/sync can drift-test them
// against its own reservedProductionBoardD/KindBoard (pkg/sync's
// default_guard_drift_test.go). Production code has no reason to call either
// — the whole point of this package's default guard is that pkg/nostr does
// not expose or consume "what a board is" as a public concept.
//
// WHY A DRIFT TEST, NOT JUST THE EXISTING publishguard_test.go COVERAGE:
// TestPublishGuard_DefaultRefusesReservedBoardCoordinate builds its own test
// event FROM these same constants and asserts the guard refuses it — that
// proves the guard's LOGIC (kind+d-tag matching) is correct, but is
// tautological with respect to the constants' VALUE: change
// defaultReservedBoardD to any other string and that test still passes,
// because the test event and the guard being tested both read the same
// (now-wrong) value. Nothing anywhere previously asserted that this value
// equals pkg/sync's own reservedProductionBoardD/KindBoard — the actual
// production board coordinate — so the two copies could silently drift apart
// (this package's default guard would then stop protecting the REAL board)
// with every existing test still green. The drift test closes that: it reads
// BOTH copies and fails the instant they disagree.
func DefaultReservedBoardD() string { return defaultReservedBoardD }
func DefaultBoardEventKind() int    { return defaultBoardEventKind }

// defaultReservedBoardGuard is PublishGuard's out-of-the-box value (see
// package doc comment above). It has NO production opt-in — pkg/nostr does
// not know what "production" means, only pkg/sync does — so it refuses every
// reserved-coordinate event unconditionally. pkg/sync's init (relayclass.go)
// overwrites PublishGuard with a closure that DOES understand the opt-in;
// this function only ever runs for a call that never reached that init.
func defaultReservedBoardGuard(_ context.Context, e *Event) error {
	if !hitsDefaultReservedBoard(e) {
		return nil
	}
	return fmt.Errorf("nostr: refusing to publish kind %d event addressing the reserved board coordinate %q — PublishGuard is still running pkg/nostr's own default (ready-fcf): no pkg/sync init has replaced it with a production opt-in-aware guard, so this call site cannot be a sanctioned rd write path; route through sync.GuardedPublish, or give this event an isolated board D-tag", e.Kind, defaultReservedBoardD)
}

// hitsDefaultReservedBoard is pkg/nostr's own minimal copy of pkg/sync's
// hitsReservedBoard (nostroutbound.go): true when e IS the reserved board
// event itself (kind 30301, "d" == defaultReservedBoardD), or carries an "a"
// tag ("<kind>:<pubkey>:<d>" — see coord/BoardCoord in pkg/sync/nostrwire.go)
// whose d-component equals defaultReservedBoardD. Same duplication rationale
// as the constants above: checking the D-component regardless of kind or
// pubkey catches a card's board-membership tag and a status event's
// board-scope tag, not only the board event itself.
func hitsDefaultReservedBoard(e *Event) bool {
	if e == nil {
		return false
	}
	if e.Kind == defaultBoardEventKind && guardTagValue(e, "d") == defaultReservedBoardD {
		return true
	}
	for _, a := range guardTagValues(e, "a") {
		parts := strings.SplitN(a, ":", 3)
		if len(parts) == 3 && parts[2] == defaultReservedBoardD {
			return true
		}
	}
	return false
}

// guardTagValue / guardTagValues mirror pkg/sync/nostrwire.go's tagValue /
// tagValues exactly (same duplication rationale: pkg/nostr cannot import
// pkg/sync). Named distinctly from that package's copies only because both
// live in the same repo's grep history, not because the behavior differs.
func guardTagValue(e *Event, name string) string {
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

func guardTagValues(e *Event, name string) []string {
	var out []string
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			out = append(out, t[1])
		}
	}
	return out
}
