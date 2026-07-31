package nostr

// ready-fcf mutation proof: PublishGuard defaults to armed at package load,
// with no OTHER package's init() involved — this file lives in package nostr
// itself and imports nothing from pkg/sync, reproducing exactly the binary
// shape the finding proved was wide open (a binary/test that links pkg/nostr
// without pkg/sync). Before the fix, PublishGuard was nil here and every one
// of these events would have reached dialRelay.
import (
	"context"
	"strings"
	"testing"
)

// unreachableGuardTestRelay never accepts a connection (RFC 5737 TEST-NET-1,
// nothing listens there) — same convention pkg/sync/guarded_publish_test.go
// uses: a call that reaches the dial at all is distinguishable by its error
// shape ("nostr: dial") from one the guard refused before any network I/O.
const unreachableGuardTestRelay = "ws://127.0.0.1:1"

// TestPublishGuard_DefaultsToArmed proves PublishGuard is non-nil the instant
// this package loads. It is the direct negation of the ready-fcf finding's own
// proof line ("go test ./pkg/nostr/ -v logs PublishGuard installed: false").
func TestPublishGuard_DefaultsToArmed(t *testing.T) {
	if PublishGuard == nil {
		t.Fatal("PublishGuard is nil in pkg/nostr's own test binary — the ready-fcf default guard is not installed, so a binary that never links pkg/sync is wide open")
	}
}

// TestPublishGuard_DefaultRefusesReservedBoardCoordinate is the ready-fcf
// mutation proof, board-event shape: a kind-30301 event whose own "d" tag IS
// the reserved coordinate must be refused pre-dial by the DEFAULT guard alone
// — this test never installs a custom PublishGuard.
func TestPublishGuard_DefaultRefusesReservedBoardCoordinate(t *testing.T) {
	e := &Event{Kind: defaultBoardEventKind, Tags: [][]string{{"d", defaultReservedBoardD}}}
	accepted, _, err := Publish(context.Background(), unreachableGuardTestRelay, e)
	if err == nil {
		t.Fatal("expected the default guard to refuse a reserved-coordinate board event, got nil error")
	}
	if accepted {
		t.Fatal("reported accepted=true on a refused write")
	}
	if strings.Contains(err.Error(), "nostr: dial") {
		t.Fatalf("error carries the transport-dial prefix — the default guard did not fire before network I/O: %v", err)
	}
}

// TestPublishGuard_DefaultRefusesReservedBoardViaATag proves the second
// hitsDefaultReservedBoard shape: a card/status event that merely REFERENCES
// the reserved board via an "a" tag ("<kind>:<pubkey>:<d>"), not the board
// event itself. The coordinate is assembled at runtime via strings.Join
// (never a literal in this file), the same technique the ready-fcf finding
// used, so the check is proven to run on the actual tag value rather than
// happening to match a compile-time constant.
func TestPublishGuard_DefaultRefusesReservedBoardViaATag(t *testing.T) {
	d := strings.Join([]string{"r", "e", "a", "d", "y"}, "")
	coord := strings.Join([]string{"30301", "deadbeef", d}, ":")
	e := &Event{Kind: 30302, Tags: [][]string{{"a", coord}}}
	accepted, _, err := Publish(context.Background(), unreachableGuardTestRelay, e)
	if err == nil {
		t.Fatal("expected the default guard to refuse an event whose \"a\" tag addresses the reserved board coordinate")
	}
	if accepted {
		t.Fatal("reported accepted=true on a refused write")
	}
	if strings.Contains(err.Error(), "nostr: dial") {
		t.Fatalf("error carries the transport-dial prefix — the default guard did not fire before network I/O: %v", err)
	}
}

// TestPublishGuard_DefaultAllowsNonReservedCoordinate proves the default guard
// is SCOPED, not blanket: a board event under an isolated/non-reserved D-tag
// must reach the dial attempt (and fail there, since the relay is
// unreachable) rather than being refused by the guard.
func TestPublishGuard_DefaultAllowsNonReservedCoordinate(t *testing.T) {
	e := &Event{Kind: defaultBoardEventKind, Tags: [][]string{{"d", "some-other-isolated-board"}}}
	_, _, err := Publish(context.Background(), unreachableGuardTestRelay, e)
	if err == nil {
		t.Fatal("expected a transport error dialing an unreachable relay — got nil, meaning nothing attempted the network")
	}
	if !strings.Contains(err.Error(), "nostr: dial") {
		t.Fatalf("expected the transport-dial error (proving the guard passed this non-reserved coordinate through), got: %v", err)
	}
}
