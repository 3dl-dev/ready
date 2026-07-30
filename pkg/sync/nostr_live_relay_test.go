package sync

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
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

	// A stable portfolio identity, not a throwaway: this proof reads its own writes
	// back, and a tenant-restricted relay (see liveRelayKey) admits only keys its
	// operator has allowed. It is NOT rd's own relay write-allowlist — that was
	// retired in ready-5fd and the shared LAN relays are open.
	k := liveRelayKey(t)
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
	boardD := liveTestBoardD(t)
	board := BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{ItemID: itemID, Title: "keystone live round-trip", Status: state.StatusActive, Priority: "p1", Type: "task", Context: "live proof <>&\"", BoardD: boardD}

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
	rr, err := ReconcileItem(context.Background(), []string{relay}, freshLog, itemID, map[string]bool{k.PubKeyHex(): true}, nostr.DefaultTimeout)
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

	// A stable portfolio identity, not a throwaway: this proof reads its own writes
	// back, and a tenant-restricted relay (see liveRelayKey) admits only keys its
	// operator has allowed. It is NOT rd's own relay write-allowlist — that was
	// retired in ready-5fd and the shared LAN relays are open.
	k := liveRelayKey(t)
	itemID := fmt.Sprintf("ready-b5f-live-%d", time.Now().UnixNano())
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".ready", NostrLogFile)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(logPath),
		WriteRelays: []string{relay},
		PendingPath: filepath.Join(dir, ".ready", NostrPendingFile),
	}
	boardD := liveTestBoardD(t)
	board := BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{k.PubKeyHex()}}

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
	createCard := CardSpec{ItemID: itemID, Title: "b5f live history", Status: state.StatusInbox, Priority: "p1", Type: "task", BoardD: boardD}
	res, err := pub.PublishItem(context.Background(), &board, createCard, now)
	mustAccept("create", res, err)

	// 2. claim -> active.
	claimCard := CardSpec{ItemID: itemID, Title: "b5f live history", Status: state.StatusActive, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), BoardD: boardD}
	res, err = pub.PublishStatusChange(context.Background(), claimCard, "", now+1)
	mustAccept("claim", res, err)

	// 3. progress -> card-only edit (context), no status event.
	progressCard := CardSpec{ItemID: itemID, Title: "b5f live history", Status: state.StatusActive, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), Context: "progress note", BoardD: boardD}
	res, err = pub.PublishCardEdit(context.Background(), progressCard, now+2)
	mustAccept("progress edit", res, err)

	// 4. edit -> another card-only edit (title), no status event. Proves editing
	// the addressable card does not erase history.
	editCard := CardSpec{ItemID: itemID, Title: "b5f live history (edited)", Status: state.StatusActive, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), Context: "progress note", BoardD: boardD}
	res, err = pub.PublishCardEdit(context.Background(), editCard, now+3)
	mustAccept("title edit", res, err)

	// 5. done --reason -> terminal status event carrying the close reason.
	doneCard := CardSpec{ItemID: itemID, Title: "b5f live history (edited)", Status: state.StatusDone, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), Context: "progress note", BoardD: boardD}
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
	rr, err := ReconcileItem(context.Background(), []string{relay}, freshLog, itemID, map[string]bool{k.PubKeyHex(): true}, nostr.DefaultTimeout)
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

// TestLiveRelay_OpenRelayIngestionTrustGate is the ground-source, no-mock proof of
// ready-5fd against a LIVE relay, and it REPLACES TestLiveRelay_WriteAllowlistTrustGate.
//
// WHY THE OLD TEST HAD TO GO. Its layers 1a/1b/1c asserted the portfolio-wide
// relay-side write-allowlist (ready-266) that THIS ITEM RETIRED
// (scripts/unlock-relays.sh removed the strfry writePolicy; scripts/lock-relays.sh
// must not be re-applied). Run against the real ws://192.168.2.40:7777 it failed on
// its own load-bearing line — "RELAY-ALLOWLIST FAILED: untrusted key ... write was
// ACCEPTED" — and `go test ./...` stayed green only because liveRelayKey's t.Skip
// fired first. A test that asserts a fence that no longer exists is a stronger false
// artifact than a stale comment, so the false assertions are gone. Nothing true was
// lost: the old test's one still-valid layer (client-side drop of an untrusted
// takeover) is layer 4 below and is STRENGTHENED — the poison events are no longer
// hand-injected into the log by the test, they are really published to the real relay
// by a real foreign key and really served back by the real relay.
//
// Four things are proven end to end, every one against real infrastructure:
//
//	1 THE RELAY IS OPEN (the retired fence, measured not assumed). Two keys generated
//	  fresh in this process — neither granted anything by rd, neither in any config,
//	  neither known to any allowlist — both publish successfully (OK,true). One plays
//	  the board owner; one plays an INTRUDER that publishes a LATER card+status
//	  takeover of the owner's item id ("HIJACKED"/done) tagged to the OWNER's board
//	  coordinate. If the relay still fenced writes by identity, the intruder's write
//	  would be refused here.
//
//	2 THE RELAY SERVES THE POISON BACK. An UNGATED reconcile (nil trust set) of the
//	  board coordinate really does return the intruder's events, and they really do
//	  land in the local authoritative log. This is what an rd machine would ingest if
//	  the in-product gate did not exist — i.e. there is no relay-edge layer under it.
//
//	3 THE IN-PRODUCT GATE REFUSES IT AT INGESTION. A fresh log reconciled from the
//	  SAME relay with the rdconfig-derived trust set (an empty Config.TrustedPubkeys
//	  means self-only) merges EXACTLY the owner's events and not one intruder event.
//	  The gate is the whole defence on an unfenced relay, and this is the seam every
//	  `rd ready` auto-reconcile takes.
//
//	4 AND PROJECTION DROPS IT TOO, with a positive control. Replaying the UNGATED log
//	  (which genuinely holds the relay-served takeover) with the trust set keeps the
//	  owner's title/status; replaying the same events with the gate DISABLED shows the
//	  takeover winning. So the trust set is what stops it, not latest-wins luck.
//
// NEEDS NO CREDENTIAL. With the fence retired every key can write, which is exactly
// the property under test, so unlike every other live test in this package this one
// deliberately does NOT call liveRelayKey — a skip for a missing admitted key would
// hide the very fact being measured.
//
// Gated behind RD_NOSTR_LIVE_RELAY=1 (the package convention) so `go test ./...`
// stays hermetic with no relay reachable. Point RD_NOSTR_RELAY_URL at an UNFENCED
// relay (the LAN strfry pair, ws://192.168.2.40:7777 / .41:7777) or configure write
// relays in rd.json. A relay that refuses a never-granted key is reported by name and
// stepped over — wss://relay.3dl.network does exactly that, with a THIRD-PARTY tenant
// message ("restricted: pubkey is not admitted to this relay's tenant write-allowlist")
// that is not rd's retired policy and that rd neither owns nor may rely on — and the
// test FAILS if no relay in the set turns out to be open.
func TestLiveRelay_OpenRelayIngestionTrustGate(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable UNFENCED strfry relay) to run the open-relay ingestion trust-gate proof")
	}
	var relays []string
	if r := os.Getenv("RD_NOSTR_RELAY_URL"); r != "" {
		relays = []string{r}
	} else {
		var cfg rdconfig.Config
		relays = cfg.WriteRelayURLs()
	}
	if len(relays) == 0 {
		t.Fatal("no write relays configured — set RD_NOSTR_RELAY_URL to an unfenced relay (e.g. ws://192.168.2.40:7777)")
	}

	// firstRefused returns the first event the relay did not accept, or nil.
	firstRefused := func(res PublishResult) *EventAck {
		for i := range res.Events {
			if !res.Events[i].AnyRelay {
				return &res.Events[i]
			}
		}
		return nil
	}
	// splitByAuthor partitions a log's event ids by author pubkey.
	splitByAuthor := func(t *testing.T, log *NostrLog, a, b string) (aIDs, bIDs, otherIDs []string) {
		t.Helper()
		evs, err := log.ReadAll()
		if err != nil {
			t.Fatalf("read log: %v", err)
		}
		for _, e := range evs {
			switch e.PubKey {
			case a:
				aIDs = append(aIDs, e.ID)
			case b:
				bIDs = append(bIDs, e.ID)
			default:
				otherIDs = append(otherIDs, e.ID)
			}
		}
		return aIDs, bIDs, otherIDs
	}

	openRelays := 0
	for _, relay := range relays {
		relay := relay
		t.Run(relay, func(t *testing.T) {
			// Both keys are freshly generated: never granted, never allowlisted,
			// never written to any config. Under ready-266 neither could have
			// written to these relays at all.
			owner := mustKey(t)
			intruder := mustKey(t)
			if owner.PubKeyHex() == intruder.PubKeyHex() {
				t.Fatal("owner/intruder key collision")
			}
			itemID := fmt.Sprintf("ready-5fd-live-%d", time.Now().UnixNano())
			boardD := liveTestBoardD(t)
			coord := BoardCoord(owner.PubKeyHex(), boardD)
			board := BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{owner.PubKeyHex()}}
			now := time.Now().Unix()
			ctx := context.Background()

			newPub := func(k *nostr.Key) *Publisher {
				dir := t.TempDir()
				return &Publisher{
					Key:         k,
					Log:         NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile)),
					WriteRelays: []string{relay},
					PendingPath: filepath.Join(dir, ".ready", NostrPendingFile),
				}
			}

			// --- LAYER 1 — THE RELAY IS OPEN ---
			// 1a: the owner (itself a never-granted fresh key) writes the board + card
			// + status. Under the retired fence this alone would have been refused.
			ownerCard := CardSpec{ItemID: itemID, Title: "legit", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: boardD}
			ores, err := newPub(owner).PublishItem(ctx, &board, ownerCard, now)
			if err != nil {
				t.Fatalf("owner publish: %v", err)
			}
			if bad := firstRefused(ores); bad != nil {
				// This relay still fences writes by identity. That is NOT rd's
				// ready-266 policy (retired) — wss://relay.3dl.network enforces a
				// tenant policy rd neither owns nor controls. Report it by name and
				// step over; the parent fails below if NO relay in the set is open.
				t.Logf("FENCED RELAY %s: refused a freshly generated never-granted key's kind %d write (acks=%+v) — this proof needs an unfenced relay; rd's own write-allowlist was retired in ready-5fd, so a refusal here is a policy rd does not own",
					relay, bad.Kind, bad.Acks)
				return
			}
			openRelays++
			t.Logf("LAYER 1a PROVEN: never-granted key %s accepted by %s (%d event(s))", owner.PubKeyHex(), relay, len(ores.Events))

			// 1b: the INTRUDER publishes a LATER takeover of the owner's item, tagged
			// to the OWNER's board coordinate so a board-scoped reconcile picks it up.
			// Built through the same production builders the owner's write used, so the
			// only difference between the two is AUTHORSHIP.
			attackCard := CardSpec{
				ItemID: itemID, Title: "HIJACKED", Status: state.StatusDone, Priority: "p0",
				Type: "task", BoardD: boardD, BoardAuthor: owner.PubKeyHex(),
			}
			ac, err := BuildCardEvent(intruder, attackCard, now+10)
			if err != nil {
				t.Fatalf("build intruder card: %v", err)
			}
			as, err := BuildStatusEventWithIssueRoot(intruder, itemID, state.StatusDone, ac.ID, "", coord, "seized", now+10, nil)
			if err != nil {
				t.Fatalf("build intruder status: %v", err)
			}
			ares, err := newPub(intruder).PublishEvents(ctx, []*nostr.Event{ac, as})
			if err != nil {
				t.Fatalf("intruder publish: %v", err)
			}
			if bad := firstRefused(ares); bad != nil {
				t.Fatalf("RETIRED-FENCE REGRESSION: %s refused the intruder's kind %d write (acks=%+v) while accepting another equally never-granted key — the relay is gating writes by identity again; ready-5fd requires it to be an open pipe (check that scripts/lock-relays.sh has not been re-applied)",
					relay, bad.Kind, bad.Acks)
			}
			t.Logf("LAYER 1b PROVEN: intruder key %s (never granted, in no allowlist) ACCEPTED by %s — the relay-edge fence is gone", intruder.PubKeyHex(), relay)

			// Give the relay a beat to index before querying it back.
			time.Sleep(1 * time.Second)

			// --- LAYER 2 — THE RELAY SERVES THE POISON BACK ---
			// Ungated reconcile (nil trust set disables the gate): this is what rd
			// would ingest with no client-side authorization at all.
			ungatedLog := NewNostrLog(filepath.Join(t.TempDir(), ".ready", NostrLogFile))
			ur, err := ReconcileBoard(ctx, []string{relay}, ungatedLog, coord, nil, nostr.DefaultTimeout)
			if err != nil {
				t.Fatalf("reconcile (ungated): %v", err)
			}
			if len(ur.RelayErrors) != 0 {
				t.Fatalf("ungated reconcile hit relay errors, so nothing was actually read back: %v", ur.RelayErrors)
			}
			t.Logf("LAYER 2: ungated reconcile of %s fetched=%d added=%d", coord, ur.Fetched, ur.Added)
			ownerIDs, intruderIDs, otherIDs := splitByAuthor(t, ungatedLog, owner.PubKeyHex(), intruder.PubKeyHex())
			if len(otherIDs) != 0 {
				t.Fatalf("isolated board %s carries %d event(s) from a third author — the per-run board is not isolated", coord, len(otherIDs))
			}
			if len(ownerIDs) == 0 {
				t.Fatalf("%s served none of the owner's events for %s — cannot tell a working gate from an empty fetch (fetched=%d)", relay, coord, ur.Fetched)
			}
			if len(intruderIDs) == 0 {
				t.Fatalf("%s served none of the intruder's events for %s despite accepting the write (fetched=%d) — without the poison actually arriving there is nothing for the gate to refuse", relay, coord, ur.Fetched)
			}
			t.Logf("LAYER 2 PROVEN: %s served the intruder back — %d owner event(s) + %d intruder event(s) reached an UNGATED local log", relay, len(ownerIDs), len(intruderIDs))

			// --- LAYER 3 — THE IN-PRODUCT GATE REFUSES IT AT INGESTION ---
			// The trust set is built the way production builds it (cmd/rd's
			// nostrTrustSet -> rdconfig.Config.TrustSet); an EMPTY TrustedPubkeys is
			// the default rd.json and must mean self-only, not open.
			trusted := (&rdconfig.Config{}).TrustSet(owner.PubKeyHex())
			if trusted[intruder.PubKeyHex()] {
				t.Fatal("precondition: intruder must not be in the rdconfig-derived trust set")
			}
			gatedLog := NewNostrLog(filepath.Join(t.TempDir(), ".ready", NostrLogFile))
			gr, err := ReconcileBoard(ctx, []string{relay}, gatedLog, coord, trusted, nostr.DefaultTimeout)
			if err != nil {
				t.Fatalf("reconcile (gated): %v", err)
			}
			if len(gr.RelayErrors) != 0 {
				t.Fatalf("gated reconcile hit relay errors, so the empty result proves nothing: %v", gr.RelayErrors)
			}
			t.Logf("LAYER 3: gated reconcile of %s fetched=%d added=%d (ungated was %d/%d)", coord, gr.Fetched, gr.Added, ur.Fetched, ur.Added)
			gotOwner, gotIntruder, gotOther := splitByAuthor(t, gatedLog, owner.PubKeyHex(), intruder.PubKeyHex())
			if len(gotIntruder) != 0 {
				t.Errorf("INGESTION TRUST GATE FAILED: %d intruder-authored event(s) %v reached the local authoritative log from %s — with the relay edge unfenced (ready-5fd) this gate is the ONLY thing between an open relay and rd's source of truth",
					len(gotIntruder), gotIntruder, relay)
			}
			if len(gotOther) != 0 {
				t.Errorf("gated log carries %d event(s) from an unexpected author", len(gotOther))
			}
			// The owner's events DID land: the gate is not simply refusing everything.
			// Compared against the ungated read of the same relay, so this needs no
			// hardcoded count and cannot drift with the event set PublishItem emits.
			if len(gotOwner) != len(ownerIDs) {
				t.Errorf("gated reconcile merged %d owner event(s), want %d (every owner event the same relay served ungated)", len(gotOwner), len(ownerIDs))
			}
			if gr.Fetched != len(ownerIDs) || gr.Added != len(ownerIDs) {
				t.Errorf("gated reconcile fetched=%d added=%d, want %d/%d — the gate drops before the merge, so both counts are the trusted-author count",
					gr.Fetched, gr.Added, len(ownerIDs), len(ownerIDs))
			}
			t.Logf("LAYER 3 PROVEN: gate admitted %d owner event(s) and ZERO of the %d intruder event(s) the same relay served", len(gotOwner), len(intruderIDs))

			// --- LAYER 4 — PROJECTION DROPS IT TOO, WITH A POSITIVE CONTROL ---
			// Project the UNGATED log: it genuinely holds the relay-served takeover
			// (no injection). PinnedBoard is deliberately NOT set so the ONLY variable
			// between the two projections below is the Trusted set — with a pinned
			// board, grant-derived levels would be a second, confounding reason to
			// drop the intruder.
			poisoned, err := ungatedLog.ReadAll()
			if err != nil {
				t.Fatalf("reread ungated log: %v", err)
			}
			maintainers := map[string]bool{owner.PubKeyHex(): true}
			guarded := ProjectItems(poisoned, ProjectOptions{Maintainers: maintainers, Trusted: trusted})
			it, ok := guarded[itemID]
			if !ok {
				t.Fatalf("owner's item %s did not project at all", itemID)
			}
			if it.Title != "legit" || it.Status != state.StatusActive {
				t.Errorf("PROJECTION TRUST GATE FAILED: item taken over by the relay-served intruder events — title=%q status=%q, want legit/active", it.Title, it.Status)
			}
			// POSITIVE CONTROL: same events, gate DISABLED (nil Trusted). The takeover
			// must win, proving the intruder's events really were a takeover and that
			// the Trusted set is what stopped it.
			if ungatedItem, ok := ProjectItems(poisoned, ProjectOptions{Maintainers: maintainers})[itemID]; !ok {
				t.Error("positive control: item did not project with the gate disabled")
			} else if ungatedItem.Title != "HIJACKED" || ungatedItem.Status != state.StatusDone {
				t.Errorf("positive control: with the trust gate DISABLED the intruder's takeover did NOT win (title=%q status=%q) — the guarded result above is therefore not attributable to the trust gate", ungatedItem.Title, ungatedItem.Status)
			}
			t.Logf("LAYER 4 PROVEN: relay-served takeover dropped at projection; same events with the gate disabled DO take the item over")
		})
	}
	if openRelays == 0 {
		t.Fatalf("no relay in %v accepted a freshly generated never-granted key, so ready-5fd's open-relay property could not be measured anywhere — point RD_NOSTR_RELAY_URL at the unfenced LAN strfry pair (ws://192.168.2.40:7777 / ws://192.168.2.41:7777)", relays)
	}
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
