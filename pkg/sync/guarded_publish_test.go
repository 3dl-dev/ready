package sync

// ready-6d0 round 3, finding (3) — CLASS-level proof for GuardedPublish, the
// network choke point. These tests reproduce the veracity adversary's own A5
// probe VERBATIM: BuildCardEvent + a direct publish call with NO Publisher in
// the picture at all (Publisher.Production cannot protect a caller that never
// constructs a Publisher). The only difference from the probe that succeeded in
// round 2 is that the publish call here is GuardedPublish instead of
// nostr.Publish — proving the chokepoint itself refuses the write, not just
// that a mechanical scan would flag the file that made it (that is
// publish_chokepoint_test.go's job; this file proves the runtime behavior the
// scan is protecting).
import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/state"
)

// unreachableChokepointRelay never accepts a connection (RFC 5737 TEST-NET-1,
// nothing listens there) — same convention other live-relay tests use for "a
// dial would hang/fail here", so a test that reaches the dial at all is
// distinguishable by its error shape from one the guard stopped first.
const unreachableChokepointRelay = "ws://127.0.0.1:1"

// TestGuardedPublish_ReproducesAdversaryA5_RefusedBeforeDial reproduces probe A5
// from the round-2 veracity report: BuildCardEvent(k, CardSpec{BoardD:"ready"})
// then a direct publish call, no Publisher constructed anywhere. With
// production=false the call must be refused by the GUARD (error message
// identifies the chokepoint, not a transport/dial failure) — proving the event
// was never handed to pkg/nostr's Publish at all.
func TestGuardedPublish_ReproducesAdversaryA5_RefusedBeforeDial(t *testing.T) {
	k := testKey(t)
	card := CardSpec{
		ItemID: "ready-6d0-a5-repro", Title: "adversary A5 repro", Status: state.StatusActive,
		Priority: "p3", Type: "task", BoardD: reservedProductionBoardD,
	}
	ev, err := BuildCardEvent(k, card, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}

	accepted, _, err := GuardedPublish(context.Background(), unreachableChokepointRelay, ev, false)
	if err == nil {
		t.Fatal("GuardedPublish must refuse a reserved-coordinate event when production=false")
	}
	if accepted {
		t.Fatal("GuardedPublish reported accepted=true on a refused write")
	}
	if !strings.Contains(err.Error(), "chokepoint guard") {
		t.Fatalf("expected the GUARD's error (chokepoint guard), got a different error — "+
			"suggests the call reached the network instead of being refused pre-dial: %v", err)
	}
	// pkg/nostr's transport error is prefixed "nostr: dial ..." (dialRelay); the
	// guard's own message never carries that prefix. Check for the TRANSPORT
	// error's shape specifically, not the substring "dial" (which the guard's
	// own wording "refusing to dial" also legitimately contains).
	if strings.Contains(err.Error(), "nostr: dial") {
		t.Fatalf("error carries pkg/nostr's transport-dial prefix — the guard did not fire before network I/O: %v", err)
	}
}

// TestGuardedPublish_ProductionTrue_ReachesTheNetwork is the control proving
// the production flag genuinely gates network access rather than always
// refusing (or always allowing) regardless of its value: the SAME reserved-
// coordinate event, with production=true, must be handed to pkg/nostr's
// Publish — proven by the error shape flipping from the guard's message to a
// transport/dial failure against the deliberately unreachable relay.
func TestGuardedPublish_ProductionTrue_ReachesTheNetwork(t *testing.T) {
	k := testKey(t)
	card := CardSpec{
		ItemID: "ready-6d0-a5-repro-prod", Title: "adversary A5 repro (production)", Status: state.StatusActive,
		Priority: "p3", Type: "task", BoardD: reservedProductionBoardD,
	}
	ev, err := BuildCardEvent(k, card, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err = GuardedPublish(ctx, unreachableChokepointRelay, ev, true)
	if err == nil {
		t.Fatal("expected a transport error dialing an unreachable relay — got nil, meaning nothing actually attempted the network")
	}
	if strings.Contains(err.Error(), "chokepoint guard") {
		t.Fatalf("production=true still hit the guard — the toggle is not gating anything: %v", err)
	}
	if !strings.Contains(err.Error(), "nostr: dial") {
		t.Fatalf("expected pkg/nostr's transport-dial error proving the call reached nostr.Publish, got: %v", err)
	}
}

// TestGuardedPublish_AllowsIsolatedBoard_ReachesTheNetwork proves the guard is
// scoped to the reserved coordinate, not a blanket block on GuardedPublish
// itself: an ordinary, non-reserved board reaches the network (and the same
// unreachable-relay dial failure) even with production=false.
func TestGuardedPublish_AllowsIsolatedBoard_ReachesTheNetwork(t *testing.T) {
	k := testKey(t)
	card := CardSpec{
		ItemID: "ready-6d0-isolated-repro", Title: "isolated board", Status: state.StatusActive,
		Priority: "p3", Type: "task", BoardD: "ready-6d0-guarded-publish-fixture",
	}
	ev, err := BuildCardEvent(k, card, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err = GuardedPublish(ctx, unreachableChokepointRelay, ev, false)
	if err == nil {
		t.Fatal("expected a transport error dialing an unreachable relay")
	}
	if strings.Contains(err.Error(), "chokepoint guard") {
		t.Fatalf("a non-reserved board coordinate was refused by the guard — scoping is broken: %v", err)
	}
}
