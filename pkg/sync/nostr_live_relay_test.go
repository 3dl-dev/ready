package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	"github.com/campfire-net/ready/pkg/state"
)

// TestLiveRelay_ItemRoundTrip is the ground-source, no-mock proof for ready-a13:
// an rd item round-trips through a LIVE self-hosted strfry relay.
//
//	create  -> publish 30302 card + 1630 status event to the live relay (OK,true)
//	           AND append them to the local authoritative log
//	WIPE the local log (simulate a clean cache)
//	read-back -> reconcile (cache-fill) the card+status FROM the relay into a fresh
//	             log, replay the log, assert the reconstructed CURRENT state matches
//	relay-off -> with the local log present and the relay unreachable, replay the
//	             local log alone and assert it STILL reconstructs (authority = log).
//
// Gated behind RD_NOSTR_LIVE_RELAY=1 so the default `go test ./...` stays green
// with no relay reachable. Endpoints come from pkg/rdconfig (never hardcoded);
// override with RD_NOSTR_RELAY_URL.
func TestLiveRelay_ItemRoundTrip(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live round-trip proof")
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

	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	// Unique item id per run so we never collide with a prior run's addressable card.
	itemID := fmt.Sprintf("ready-a13-live-%d", time.Now().UnixNano())
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".ready", NostrLogFile)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(logPath),
		WriteRelays: []string{relay},
		PendingPath: filepath.Join(dir, ".ready", NostrPendingFile),
	}
	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{ItemID: itemID, Title: "keystone live round-trip", Status: state.StatusActive, Priority: "p1", Type: "task", Context: "live proof <>&\"", BoardD: "ready"}

	// --- CREATE: publish to the live relay + local log ---
	res, err := pub.PublishItem(context.Background(), &board, card, time.Now().Unix())
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	var cardID, statusID string
	for _, ev := range res.Events {
		if !ev.AnyRelay {
			t.Fatalf("event kind %d id %s was NOT accepted by the relay (acks=%+v)", ev.Kind, ev.EventID, ev.Acks)
		}
		switch ev.Kind {
		case KindCard:
			cardID = ev.EventID
		case KindStatusOpen:
			statusID = ev.EventID
		}
		t.Logf("published kind %d id %s relay-accepted=%v", ev.Kind, ev.EventID, ev.AnyRelay)
	}
	if cardID == "" || statusID == "" {
		t.Fatalf("missing published card/status ids: card=%q status=%q", cardID, statusID)
	}
	// Give the relay a beat to index before querying it back.
	time.Sleep(1 * time.Second)

	// --- RELAY-OFFLINE READ: local log alone reconstructs current state ---
	localEvents, err := NewNostrLog(logPath).ReadAll()
	if err != nil || len(localEvents) < 2 {
		t.Fatalf("local log read: evs=%d err=%v", len(localEvents), err)
	}
	offlineItems := ProjectItems(localEvents, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	assertMatches(t, "relay-offline local-log read", offlineItems[itemID], card)

	// --- CLEAN CACHE: wipe the local log, reconcile FROM the relay, replay ---
	if err := os.Remove(logPath); err != nil {
		t.Fatalf("wipe local log: %v", err)
	}
	freshLog := NewNostrLog(logPath)
	if evs, _ := freshLog.ReadAll(); len(evs) != 0 {
		t.Fatalf("log not actually wiped")
	}
	rr, err := ReconcileItem(context.Background(), []string{relay}, freshLog, itemID, k.PubKeyHex(), nostr.DefaultTimeout)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	t.Logf("reconcile: fetched=%d added=%d relay_errors=%v", rr.Fetched, rr.Added, rr.RelayErrors)
	if rr.Added < 2 {
		t.Fatalf("expected to cache-fill at least the card+status from the relay, added=%d", rr.Added)
	}
	rebuilt, err := freshLog.ReadAll()
	if err != nil {
		t.Fatalf("reread reconciled log: %v", err)
	}
	cleanItems := ProjectItems(rebuilt, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	assertMatches(t, "clean-cache relay-reconciled read", cleanItems[itemID], card)
	t.Logf("PROVEN: item %s round-tripped through the live relay; state matches on clean-cache reconcile AND relay-offline local read", itemID)
}

func assertMatches(t *testing.T, ctx string, got *state.Item, want CardSpec) {
	t.Helper()
	if got == nil {
		t.Fatalf("[%s] item not reconstructed", ctx)
	}
	if got.Title != want.Title {
		t.Errorf("[%s] title = %q, want %q", ctx, got.Title, want.Title)
	}
	if got.Status != want.Status {
		t.Errorf("[%s] status = %q, want %q", ctx, got.Status, want.Status)
	}
	if got.Priority != want.Priority {
		t.Errorf("[%s] priority = %q, want %q", ctx, got.Priority, want.Priority)
	}
	if got.Type != want.Type {
		t.Errorf("[%s] type = %q, want %q", ctx, got.Type, want.Type)
	}
}
