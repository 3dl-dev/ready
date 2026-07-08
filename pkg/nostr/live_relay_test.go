package nostr

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/rdconfig"
)

// TestLiveRelay_PublishFetchVerifyTamper is the ground-source, no-mock proof of
// the full loop against a LIVE strfry relay: build a canonical event in Go,
// schnorr-sign it, publish it (relay must answer OK,true), read it back
// independently, Verify the round-tripped copy (ACCEPT), then tamper a byte and
// Verify must REJECT.
//
// It is gated behind RD_NOSTR_LIVE_RELAY=1 so the default `go test ./...` (which
// runs in CI with no relay reachable) stays green. Endpoints come from
// pkg/rdconfig defaults (never hardcoded here) and can be overridden with
// RD_NOSTR_RELAY_URL.
func TestLiveRelay_PublishFetchVerifyTamper(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live proof")
	}

	relay := os.Getenv("RD_NOSTR_RELAY_URL")
	if relay == "" {
		var cfg rdconfig.Config
		urls := cfg.WriteRelayURLs()
		if len(urls) == 0 {
			t.Fatal("no write relays configured")
		}
		relay = urls[0]
	}

	// Sign with the allowlisted portfolio key: the locked relays reject any
	// non-admitted author (ready-266), so a throwaway key would be refused.
	k := liveRelayKey(t)

	e := &Event{
		CreatedAt: time.Now().Unix(),
		Kind:      1,
		Tags:      [][]string{{"t", "rd-nostr-proof"}},
		Content:   fmt.Sprintf("ready-41d live proof %d <>&\"", time.Now().UnixNano()),
	}
	if err := e.Sign(k); err != nil {
		t.Fatalf("sign: %v", err)
	}
	t.Logf("event id: %s", e.ID)

	ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer cancel()

	accepted, msg, err := Publish(ctx, relay, e)
	if err != nil {
		t.Fatalf("publish to %s: %v", relay, err)
	}
	if !accepted {
		t.Fatalf("relay %s REJECTED a valid event: %q", relay, msg)
	}
	t.Logf("relay %s accepted event (OK,true): %q", relay, msg)

	// Independent read-back.
	fctx, fcancel := context.WithTimeout(context.Background(), DefaultTimeout)
	defer fcancel()
	got, err := Fetch(fctx, relay, e.ID)
	if err != nil {
		t.Fatalf("fetch back: %v", err)
	}
	if got.ID != e.ID {
		t.Fatalf("fetched id mismatch: got %s want %s", got.ID, e.ID)
	}

	// ACCEPT: the round-tripped copy verifies independently.
	if err := got.Verify(); err != nil {
		t.Fatalf("Verify rejected the relay-served event: %v", err)
	}
	t.Logf("independent Verify PASSED on relay-served event")

	// REJECT: tamper one byte of content, Verify must fail.
	got.Content += "X"
	if err := got.Verify(); err == nil {
		t.Fatal("Verify ACCEPTED a tampered relay-served event — tamper gate broken")
	}
	t.Logf("tamper-rejection PASSED")
}
