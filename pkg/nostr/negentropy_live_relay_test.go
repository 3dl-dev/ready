package nostr

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/rdconfig"
)

// TestLiveRelay_Negentropy is the ground-source proof that rd's NIP-77 client
// interoperates with a LIVE strfry relay. It publishes N unique events under a
// fresh throwaway key, then runs negentropy reconciliation from three vantage
// points and asserts the measured diff is exactly correct:
//
//	empty local set   -> Need == all N ids, Have == 0   (download everything)
//	full local set    -> Need == 0, Have == 0           (converged: near-zero cost)
//	partial local set -> Need == the missing ids, Have == the extra local id
//
// The Have case also proves the UPLOAD direction: an id the client has that the
// relay lacks is reported for upload. Gated behind RD_NOSTR_LIVE_RELAY=1.
func TestLiveRelay_Negentropy(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live negentropy proof")
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
	t.Logf("live relay: %s", relay)

	// Sign with the allowlisted portfolio key (ready-266): the locked relays
	// reject non-admitted authors, so uploads must use an admitted key.
	k := liveRelayKey(t)

	// A unique "d" tag namespace so this run's events are isolated from prior runs.
	runTag := fmt.Sprintf("rd797-neg-%d", time.Now().UnixNano())
	const kind = 30078 // NIP-78 app-specific addressable — arbitrary, isolated set.

	const n = 8
	var published []*Event
	for i := 0; i < n; i++ {
		e := &Event{
			CreatedAt: time.Now().Unix() + int64(i),
			Kind:      kind,
			Tags:      [][]string{{"d", fmt.Sprintf("%s-%d", runTag, i)}, {"t", runTag}},
			Content:   fmt.Sprintf("negentropy proof %d", i),
		}
		if err := e.Sign(k); err != nil {
			t.Fatalf("sign %d: %v", i, err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), DefaultTimeout)
		accepted, msg, err := Publish(ctx, relay, e)
		cancel()
		if err != nil || !accepted {
			t.Fatalf("publish %d: accepted=%v msg=%q err=%v", i, accepted, msg, err)
		}
		published = append(published, e)
	}
	time.Sleep(1 * time.Second) // let strfry index

	// The negentropy filter selects exactly this run's events on the relay.
	filter := map[string]any{"kinds": []int{kind}, "#t": []string{runTag}, "authors": []string{k.PubKeyHex()}}

	itemsFor := func(evs []*Event) []NegItem {
		var out []NegItem
		for _, e := range evs {
			it, err := NegItemFromEvent(e)
			if err != nil {
				t.Fatalf("neg item: %v", err)
			}
			out = append(out, it)
		}
		return out
	}

	// --- 1) empty local set: must NEED all n, HAVE none ---
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	res, err := NegentropyReconcile(ctx, relay, filter, nil)
	cancel()
	if err != nil {
		t.Fatalf("neg (empty): %v", err)
	}
	t.Logf("empty-local: need=%d have=%d sent=%dB recv=%dB rounds=%d",
		len(res.Need), len(res.Have), res.BytesSent, res.BytesReceived, res.RoundTrips)
	if len(res.Need) != n || len(res.Have) != 0 {
		t.Fatalf("empty-local diff wrong: need=%d (want %d) have=%d (want 0)", len(res.Need), n, len(res.Have))
	}

	// --- 2) full local set: converged, near-zero cost ---
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	resFull, err := NegentropyReconcile(ctx, relay, filter, itemsFor(published))
	cancel()
	if err != nil {
		t.Fatalf("neg (full): %v", err)
	}
	t.Logf("full-local: need=%d have=%d sent=%dB recv=%dB rounds=%d",
		len(resFull.Need), len(resFull.Have), resFull.BytesSent, resFull.BytesReceived, resFull.RoundTrips)
	if len(resFull.Need) != 0 || len(resFull.Have) != 0 {
		t.Fatalf("full-local should be converged: need=%d have=%d", len(resFull.Need), len(resFull.Have))
	}

	// --- 3) partial local set: hold all but the last relay event, plus one extra
	// local-only event the relay never saw. Must NEED the 1 missing + HAVE the extra.
	extra := &Event{
		CreatedAt: time.Now().Unix() + 1000,
		Kind:      kind,
		Tags:      [][]string{{"d", runTag + "-local-only"}, {"t", runTag}},
		Content:   "local-only, never published",
	}
	if err := extra.Sign(k); err != nil {
		t.Fatalf("sign extra: %v", err)
	}
	local := append(itemsFor(published[:n-1]), mustItem(t, extra))
	ctx, cancel = context.WithTimeout(context.Background(), 30*time.Second)
	resPart, err := NegentropyReconcile(ctx, relay, filter, local)
	cancel()
	if err != nil {
		t.Fatalf("neg (partial): %v", err)
	}
	t.Logf("partial-local: need=%v have=%v sent=%dB recv=%dB rounds=%d",
		resPart.Need, resPart.Have, resPart.BytesSent, resPart.BytesReceived, resPart.RoundTrips)
	if len(resPart.Need) != 1 || resPart.Need[0] != published[n-1].ID {
		t.Fatalf("partial NEED wrong: got %v want [%s]", resPart.Need, published[n-1].ID)
	}
	if len(resPart.Have) != 1 || resPart.Have[0] != extra.ID {
		t.Fatalf("partial HAVE wrong: got %v want [%s]", resPart.Have, extra.ID)
	}
	t.Logf("PROVEN: rd NIP-77 negentropy interoperates with live strfry; diff exact in all three vantage points")
}

func mustItem(t *testing.T, e *Event) NegItem {
	t.Helper()
	it, err := NegItemFromEvent(e)
	if err != nil {
		t.Fatalf("neg item: %v", err)
	}
	return it
}
