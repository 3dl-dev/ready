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

// TestLiveRelay_FullHistoryReplay is the ground-source, no-mock proof for
// ready-b5f: after several mutations against a LIVE self-hosted strfry relay
// (create, claim, progress-edit, title-edit, done --reason), `rd show`'s replay
// path (ProjectItems) reconstructs the FULL audit history — not just the
// latest-wins card — even after wiping the local log and reconciling purely from
// the relay. Gated behind RD_NOSTR_LIVE_RELAY=1, same as TestLiveRelay_ItemRoundTrip.
func TestLiveRelay_FullHistoryReplay(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live history-replay proof")
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
	itemID := fmt.Sprintf("ready-b5f-live-%d", time.Now().UnixNano())
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".ready", NostrLogFile)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(logPath),
		WriteRelays: []string{relay},
		PendingPath: filepath.Join(dir, ".ready", NostrPendingFile),
	}
	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{k.PubKeyHex()}}

	mustAccept := func(label string, res PublishResult, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: publish: %v", label, err)
		}
		for _, ev := range res.Events {
			if !ev.AnyRelay {
				t.Fatalf("%s: event kind %d id %s NOT accepted by relay (acks=%+v)", label, ev.Kind, ev.EventID, ev.Acks)
			}
		}
	}

	// Sequence timestamps off "now" (seconds, NIP-01 granularity) — a relay
	// rejects created_at that is unreasonably far in the future or past.
	now := time.Now().Unix()

	// 1. create (inbox).
	createCard := CardSpec{ItemID: itemID, Title: "b5f live history", Status: state.StatusInbox, Priority: "p1", Type: "task", BoardD: "ready"}
	res, err := pub.PublishItem(context.Background(), &board, createCard, now)
	mustAccept("create", res, err)

	// 2. claim -> active.
	claimCard := CardSpec{ItemID: itemID, Title: "b5f live history", Status: state.StatusActive, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), BoardD: "ready"}
	res, err = pub.PublishStatusChange(context.Background(), claimCard, "", now+1)
	mustAccept("claim", res, err)

	// 3. progress -> card-only edit (context), no status event.
	progressCard := CardSpec{ItemID: itemID, Title: "b5f live history", Status: state.StatusActive, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), Context: "progress note", BoardD: "ready"}
	res, err = pub.PublishCardEdit(context.Background(), progressCard, now+2)
	mustAccept("progress edit", res, err)

	// 4. edit -> another card-only edit (title), no status event. Proves editing
	// the addressable card does not erase history.
	editCard := CardSpec{ItemID: itemID, Title: "b5f live history (edited)", Status: state.StatusActive, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), Context: "progress note", BoardD: "ready"}
	res, err = pub.PublishCardEdit(context.Background(), editCard, now+3)
	mustAccept("title edit", res, err)

	// 5. done --reason -> terminal status event carrying the close reason.
	doneCard := CardSpec{ItemID: itemID, Title: "b5f live history (edited)", Status: state.StatusDone, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), Context: "progress note", BoardD: "ready"}
	res, err = pub.PublishStatusChange(context.Background(), doneCard, "implemented and merged; live-relay proof", now+4)
	mustAccept("done --reason", res, err)

	time.Sleep(1 * time.Second)

	assertFullHistory := func(t *testing.T, ctx string, items map[string]*state.Item) {
		t.Helper()
		it, ok := items[itemID]
		if !ok {
			t.Fatalf("[%s] item not reconstructed", ctx)
		}
		if it.Title != "b5f live history (edited)" {
			t.Errorf("[%s] current title = %q, want edited title (latest card should win)", ctx, it.Title)
		}
		if it.Status != state.StatusDone {
			t.Errorf("[%s] current status = %q, want done", ctx, it.Status)
		}
		if len(it.History) != 3 {
			t.Fatalf("[%s] history length = %d, want 3 (create, claim, done) — got %+v", ctx, len(it.History), it.History)
		}
		if it.History[2].Note != "implemented and merged; live-relay proof" {
			t.Errorf("[%s] close-with-reason not preserved: %q", ctx, it.History[2].Note)
		}
		t.Logf("[%s] PROVEN: full history replay = %+v", ctx, it.History)
	}

	// --- relay-offline read: local log alone reconstructs the FULL history ---
	localEvents, err := NewNostrLog(logPath).ReadAll()
	if err != nil || len(localEvents) < 6 {
		t.Fatalf("local log read: evs=%d err=%v", len(localEvents), err)
	}
	offlineItems := ProjectItems(localEvents, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	assertFullHistory(t, "relay-offline local-log read", offlineItems)

	// --- clean-cache reconcile: wipe local log, cache-fill purely from the relay ---
	if err := os.Remove(logPath); err != nil {
		t.Fatalf("wipe local log: %v", err)
	}
	freshLog := NewNostrLog(logPath)
	rr, err := ReconcileItem(context.Background(), []string{relay}, freshLog, itemID, k.PubKeyHex(), nostr.DefaultTimeout)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	t.Logf("reconcile: fetched=%d added=%d relay_errors=%v", rr.Fetched, rr.Added, rr.RelayErrors)
	// The relay enforces NIP-33 parameterized-replaceable semantics on the 30302
	// card (kind 30000-39999 range): it retains only the LATEST card per (kind,
	// pubkey, d), discarding all 4 earlier card revisions (create/claim/progress/
	// edit). The 3 status events (1630/1630/1631) are NOT addressable, so the
	// relay keeps every one. Reconcile therefore cache-fills exactly 4 events:
	// 1 card (current state) + 3 status events (full history) — proving the
	// hybrid design survives even a cache that never held the earlier cards.
	if rr.Added != 4 {
		t.Fatalf("expected exactly 4 events from relay (1 latest card + 3 status events; the relay replaces earlier addressable cards per NIP-33), added=%d", rr.Added)
	}
	rebuilt, err := freshLog.ReadAll()
	if err != nil {
		t.Fatalf("reread reconciled log: %v", err)
	}
	cleanItems := ProjectItems(rebuilt, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	assertFullHistory(t, "clean-cache relay-reconciled read", cleanItems)
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
