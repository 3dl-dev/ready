// ready-69e class-level runtime proof.
//
// GuardedPublish (relayclass.go) has its OWN explicit check
// (`if !production && hitsReservedBoard(e) { … }`) before it ever calls
// pkg/nostr.Publish — so every test that goes THROUGH GuardedPublish
// (guarded_publish_test.go) is proving that check, not the new
// nostr.PublishGuard hook installed by this package's init(). This file calls
// pkg/nostr.Publish DIRECTLY, bypassing GuardedPublish's own check entirely,
// to prove the hook itself is a second, independent layer — the one that
// actually closes the class (any caller reaching pkg/nostr.Publish by any
// spelling, not only callers that route through GuardedPublish).
//
// This file deliberately calls pkg/nostr.Publish directly and is therefore
// listed in chokepointAllowlist (publish_chokepoint_test.go) alongside
// relayclass.go — its purpose IS to test that direct call being refused, not
// to sidestep the guard.
package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestPublishGuardHook_RefusesDirectCallBypassingGuardedPublish proves the
// nostr.PublishGuard hook (installed in relayclass.go's init) fires even when
// GuardedPublish is skipped entirely — the exact shape of a brand-new file
// that imports pkg/nostr and calls Publish directly, whatever spelling it
// uses (this test uses the plain unaliased spelling; alias/dot-import/
// function-value/reflection variants were proven equivalent in a throwaway
// experiment package and are not committed here — reflection and function
// values still execute this exact function body, and an alias/dot-import is
// just a different identifier for the same function).
func TestPublishGuardHook_RefusesDirectCallBypassingGuardedPublish(t *testing.T) {
	k := testKey(t)
	card := CardSpec{
		ItemID: "ready-69e-hook-repro", Title: "class-guard hook repro", Status: state.StatusActive,
		Priority: "p3", Type: "task", BoardD: reservedProductionBoardD,
	}
	ev, err := BuildCardEvent(k, card, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}

	// Calls pkg/nostr.Publish DIRECTLY — no GuardedPublish, no production
	// context stamped on ctx (context.Background() carries none of
	// publishProductionCtxKey). If the hook were not installed, or not wired,
	// this would proceed straight to a dial attempt.
	accepted, _, err := nostr.Publish(context.Background(), unreachableChokepointRelay, ev)
	if err == nil {
		t.Fatal("nostr.Publish must be refused by PublishGuard for a reserved-coordinate event with no production opt-in on ctx")
	}
	if accepted {
		t.Fatal("reported accepted=true on a refused write")
	}
	if !strings.Contains(err.Error(), "ready-69e class guard") {
		t.Fatalf("expected the PublishGuard hook's error (ready-69e class guard), got a different error — "+
			"suggests the call reached the network instead of being refused pre-dial: %v", err)
	}
	if strings.Contains(err.Error(), "nostr: dial") {
		t.Fatalf("error carries pkg/nostr's transport-dial prefix — the hook did not fire before network I/O: %v", err)
	}
}

// TestPublishGuardHook_ProductionCtxAllowsDirectCall proves the hook's
// production opt-in genuinely gates rather than always refusing: stamping ctx
// via withPublishProduction(ctx, true) — the same helper GuardedPublish uses —
// lets the identical reserved-coordinate event reach pkg/nostr.Publish's
// dial attempt.
func TestPublishGuardHook_ProductionCtxAllowsDirectCall(t *testing.T) {
	k := testKey(t)
	card := CardSpec{
		ItemID: "ready-69e-hook-repro-prod", Title: "class-guard hook repro (production)", Status: state.StatusActive,
		Priority: "p3", Type: "task", BoardD: reservedProductionBoardD,
	}
	ev, err := BuildCardEvent(k, card, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, _, err = nostr.Publish(withPublishProduction(ctx, true), unreachableChokepointRelay, ev)
	if err == nil {
		t.Fatal("expected a transport error dialing an unreachable relay — got nil, meaning nothing attempted the network")
	}
	if strings.Contains(err.Error(), "ready-69e class guard") {
		t.Fatalf("production context still hit the hook — the opt-in is not gating anything: %v", err)
	}
	if !strings.Contains(err.Error(), "nostr: dial") {
		t.Fatalf("expected pkg/nostr's transport-dial error proving the call reached the network, got: %v", err)
	}
}
