package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestLiveRelay_BoardPublishConverges is the ground-source, no-mock proof for
// ready-866 (ready-615 edge #4: 3dl had 547 cards locally but ~12 on relays — a
// fresh box could NOT converge from relays alone). It seeds N items that exist
// ONLY in the local authoritative log — a Publisher configured with ZERO write
// relays, modelling every item that was ever created/edited while the relay was
// unreachable or unset — then calls Publisher.PublishBoard against a REAL LIVE
// strfry relay (RD_NOSTR_LIVE_RELAY=1) and proves a FRESH, empty log — reconciled
// purely from that relay, no local knowledge, no scp — projects all N items with
// matching titles. Gated behind RD_NOSTR_LIVE_RELAY=1 (see liveRelayKey /
// scripts/lib/relay-probe.sh).
func TestLiveRelay_BoardPublishConverges(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live board-publish proof")
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

	// Allowlisted portfolio key: the locked relays reject non-admitted authors
	// (ready-266).
	k := liveRelayKey(t)
	dir := t.TempDir()
	log := NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile))

	uniq := time.Now().UnixNano()
	boardD := fmt.Sprintf("ready-866-live-%d", uniq)
	board := BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{k.PubKeyHex()}}
	boardCoord := BoardCoord(k.PubKeyHex(), boardD)

	// Phase 1 — seed N items LOCAL-ONLY: a Publisher with NO write relays, so
	// every board/card/status event lands in the authoritative log (durability
	// guarantee) but reaches NO relay. This is exactly the ready-615 edge #4
	// scenario: the local log is authoritative and ahead of the relays.
	offlinePub := &Publisher{Key: k, Log: log, WriteRelays: nil}
	const n = 5
	itemIDs := make([]string, n)
	now := time.Now().Unix()
	for i := 0; i < n; i++ {
		itemID := fmt.Sprintf("ready-866-live-%d-%d", uniq, i)
		itemIDs[i] = itemID
		card := CardSpec{
			ItemID: itemID, Title: fmt.Sprintf("local-only-%d", i),
			Status: state.StatusActive, Priority: "p2", Type: "task",
			Assignee: k.PubKeyHex(), BoardD: boardD,
		}
		var boardArg *BoardSpec
		if i == 0 {
			boardArg = &board // publish the 30301 board event itself once, also local-only
		}
		res, err := offlinePub.PublishItemWithReason(context.Background(), boardArg, card, "", now+int64(i))
		if err != nil {
			t.Fatalf("seed item %d: %v", i, err)
		}
		for _, ev := range res.Events {
			if ev.AnyRelay {
				t.Fatalf("seed item %d: event kind %d unexpectedly reached a relay with WriteRelays=nil", i, ev.Kind)
			}
		}
	}

	// Sanity: the LOCAL log already projects all N items (proves the seed did
	// what it claims — items exist locally before any relay push).
	trust := map[string]bool{k.PubKeyHex(): true}
	localEvents, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read local log: %v", err)
	}
	localItems := ProjectItems(localEvents, ProjectOptions{Maintainers: trust, Trusted: trust})
	for _, id := range itemIDs {
		if _, ok := localItems[id]; !ok {
			t.Fatalf("seed sanity: item %s missing from local projection", id)
		}
	}

	// Phase 2 — `rd log publish --board` equivalent: push EVERY local-log event
	// for this board to the LIVE relay, from the SAME local log (same Publisher
	// identity, now WITH the live relay wired).
	livePub := &Publisher{Key: k, Log: log, WriteRelays: []string{relay}}
	pubRes, err := livePub.PublishBoard(context.Background(), boardCoord)
	if err != nil {
		t.Fatalf("PublishBoard: %v", err)
	}
	// n items * (card + status) + 1 board event = 2n+1 events minimum.
	if len(pubRes.Events) < 2*n+1 {
		t.Fatalf("PublishBoard published %d events, want at least %d (n=%d cards+status + 1 board)", len(pubRes.Events), 2*n+1, n)
	}
	for _, ev := range pubRes.Events {
		if !ev.AnyRelay {
			t.Fatalf("PublishBoard: event kind %d id %s NOT accepted by relay (acks=%+v)", ev.Kind, ev.EventID, ev.Acks)
		}
	}

	// Phase 3 — FROM A CLEAN STORE: a brand-new, empty local log with zero prior
	// knowledge of these items. Reconcile purely from the relay and prove every
	// seeded item appears. This is the "fresh box converges from relays alone, no
	// scp" done condition.
	cleanDir := t.TempDir()
	cleanLog := NewNostrLog(filepath.Join(cleanDir, ".ready", NostrLogFile))
	rres, err := ReconcileBoard(context.Background(), []string{relay}, cleanLog, boardCoord, trust, 15*time.Second)
	if err != nil {
		t.Fatalf("ReconcileBoard: %v", err)
	}
	t.Logf("reconcile: fetched=%d added=%d relay_errors=%v", rres.Fetched, rres.Added, rres.RelayErrors)

	cleanEvents, err := cleanLog.ReadAll()
	if err != nil {
		t.Fatalf("read clean log: %v", err)
	}
	cleanItems := ProjectItems(cleanEvents, ProjectOptions{Maintainers: trust, Trusted: trust})
	for i, id := range itemIDs {
		it, ok := cleanItems[id]
		if !ok {
			t.Fatalf("FROM CLEAN STORE: item %s not found after PublishBoard + reconcile — a fresh box did NOT converge from relays alone", id)
		}
		wantTitle := fmt.Sprintf("local-only-%d", i)
		if it.Title != wantTitle {
			t.Fatalf("item %s: title = %q, want %q", id, it.Title, wantTitle)
		}
	}
	t.Logf("PROVEN: %d local-only items converged to a clean store via PublishBoard + relay reconcile", n)
}
