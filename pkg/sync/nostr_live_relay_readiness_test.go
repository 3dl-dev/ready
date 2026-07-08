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
	"github.com/campfire-net/ready/pkg/views"
)

// TestLiveRelay_ReadinessParity is the ground-source, no-mock proof for
// ready-82c: build a small dep+gate graph, publish it as nostr card events to
// a LIVE self-hosted strfry relay, WIPE the local log, cache-fill (reconcile)
// from the relay alone, project, and assert the attention engine
// (pkg/views.ReadyFilter / GatesFilter over the nostr-projected items) computes
// the SAME readiness set as the equivalent campfire-derived graph
// (pkg/state/state_dep_test.go + state_gate_test.go scenarios; see the
// deterministic mirror in nostrproject_dep_gate_test.go's
// TestNostrProjection_ReadinessParity for the identical expectation).
//
// Gated behind RD_NOSTR_LIVE_RELAY=1 so `go test ./...` stays green with no
// relay reachable. Endpoints come from pkg/rdconfig (never hardcoded); override
// with RD_NOSTR_RELAY_URL.
func TestLiveRelay_ReadinessParity(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live readiness-parity proof")
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
	// Unique item ids per run so we never collide with a prior run's addressable cards.
	run := time.Now().UnixNano()
	id := func(n string) string { return fmt.Sprintf("ready-82c-live-%d-%s", run, n) }

	dir := t.TempDir()
	logPath := filepath.Join(dir, ".ready", NostrLogFile)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(logPath),
		WriteRelays: []string{relay},
		PendingPath: filepath.Join(dir, ".ready", NostrPendingFile),
	}
	board := BoardSpec{BoardD: "ready-82c-live", Title: "ready-82c-live", Maintainers: []string{k.PubKeyHex()}}

	t01, t02, t03, t04, t05 := id("t01"), id("t02"), id("t03"), id("t04"), id("t05")
	cards := []CardSpec{
		{ItemID: t01, Title: "t01", Status: state.StatusActive, Priority: "p0", Type: "task", BoardD: board.BoardD},
		{ItemID: t02, Title: "t02", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: board.BoardD, Deps: []string{t01}},
		{ItemID: t03, Title: "t03", Status: state.StatusWaiting, Priority: "p1", Type: "task", BoardD: board.BoardD, Gate: "human", WaitingType: "gate", WaitingOn: "needs sign-off"},
		{ItemID: t04, Title: "t04", Status: state.StatusDone, Priority: "p2", Type: "task", BoardD: board.BoardD},
		{ItemID: t05, Title: "t05", Status: state.StatusActive, Priority: "p2", Type: "task", BoardD: board.BoardD, Deps: []string{t04}},
	}

	ctx := context.Background()
	now := time.Now().Unix()
	for i, card := range cards {
		var boardArg *BoardSpec
		if i == 0 {
			boardArg = &board
		}
		res, err := pub.PublishItem(ctx, boardArg, card, now+int64(i))
		if err != nil {
			t.Fatalf("publish %s: %v", card.ItemID, err)
		}
		for _, ev := range res.Events {
			if !ev.AnyRelay {
				t.Fatalf("event kind %d (item %s) NOT accepted by the relay (acks=%+v)", ev.Kind, card.ItemID, ev.Acks)
			}
		}
	}
	// Give the relay a beat to index before querying it back.
	time.Sleep(1 * time.Second)

	// --- WIPE the local log: simulate a clean cache, then reconcile-ALL from the relay ---
	if err := os.Remove(logPath); err != nil {
		t.Fatalf("wipe local log: %v", err)
	}
	freshLog := NewNostrLog(logPath)
	rr, err := ReconcileAll(ctx, []string{relay}, freshLog, k.PubKeyHex(), nostr.DefaultTimeout)
	if err != nil {
		t.Fatalf("reconcile-all: %v", err)
	}
	t.Logf("reconcile-all: fetched=%d added=%d relay_errors=%v", rr.Fetched, rr.Added, rr.RelayErrors)
	if rr.Added < len(cards) {
		t.Fatalf("expected to cache-fill at least %d events (one card per item), added=%d", len(cards), rr.Added)
	}

	rebuilt, err := freshLog.ReadAll()
	if err != nil {
		t.Fatalf("reread reconciled log: %v", err)
	}
	itemsByID := ProjectItems(rebuilt, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	var all []*state.Item
	for _, itemID := range []string{t01, t02, t03, t04, t05} {
		item, ok := itemsByID[itemID]
		if !ok {
			t.Fatalf("item %s not reconstructed from relay-reconciled log", itemID)
		}
		all = append(all, item)
	}

	// --- PRE-MIGRATION EXPECTATION (matches pkg/state's dep/gate replay semantics) ---
	wantBlocked := map[string]bool{t02: true, t03: false, t04: false, t05: false, t01: false}
	for _, item := range all {
		got := item.Status == state.StatusBlocked
		if got != wantBlocked[item.ID] {
			t.Errorf("item %s: blocked=%v, want %v (status=%q)", item.ID, got, wantBlocked[item.ID], item.Status)
		}
	}

	ready := views.Apply(all, views.ReadyFilter())
	wantReady := map[string]bool{t01: true, t02: false, t03: true, t04: false, t05: true}
	gotReady := map[string]bool{}
	for _, item := range ready {
		gotReady[item.ID] = true
	}
	for id, want := range wantReady {
		if gotReady[id] != want {
			t.Errorf("readiness parity: item %s ready=%v, want %v", id, gotReady[id], want)
		}
	}

	gates := views.Apply(all, views.GatesFilter())
	if len(gates) != 1 || gates[0].ID != t03 {
		t.Errorf("gates view parity: got %v, want [%s]", gates, t03)
	}

	t.Logf("PROVEN: dep+gate readiness set reconstructed from the LIVE relay matches the pre-migration expectation (ready=%v, blocked=%s, gated=%s)",
		[]string{t01, t03, t05}, t02, t03)
}
