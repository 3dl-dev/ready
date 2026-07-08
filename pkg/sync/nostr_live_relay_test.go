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

	// Allowlisted portfolio key: the locked relays reject non-admitted authors (ready-266).
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

	// Allowlisted portfolio key: the locked relays reject non-admitted authors (ready-266).
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

// TestLiveRelay_WriteAllowlistTrustGate is the ground-source, no-mock proof that
// TWO independent defence layers both hold against the LIVE relays:
//
//	LAYER 1 — relay-side write-allowlist (ready-266, THIS item): against every
//	  configured relay, the ALLOWLISTED portfolio key's write is ACCEPTED, and an
//	  UNTRUSTED random key's write is REJECTED by the relay itself (OK,false with
//	  the write-allowlist block reason). A relay round-trip then confirms the
//	  untrusted event never landed — the relay's stored set for the item carries
//	  ONLY the trusted author.
//
//	LAYER 2 — client-side web-of-trust drop (ready-d53): even if a poison event
//	  DID reach the local authoritative log (e.g. via a hostile/permissive relay,
//	  or a merged foreign log — a path the locked production relay now refuses),
//	  the client projection gate STILL drops it. This is proven by INJECTING the
//	  attacker's locally-built forged events directly into the log next to the
//	  trusted events and asserting the projection ignores the takeover. This keeps
//	  the d53 client-drop proof alive without a permissive production relay; its
//	  deterministic twin is TestProjection_TrustGate_* in nostrtrust_test.go.
//
// This REPLACES the old TestLiveRelay_TrustGate, which assumed a PERMISSIVE relay
// (it required the attacker's write to be accepted so the client gate could catch
// it). After the ready-266 lockdown the relay refuses that write, so the attacker
// acceptance is now itself the thing under test (layer 1), and the client-drop
// path (layer 2) is exercised by direct injection.
//
// Gated behind RD_NOSTR_LIVE_RELAY=1. Signs with the allowlisted portfolio key
// (liveRelayKey); relays come from pkg/rdconfig (both, unless RD_NOSTR_RELAY_URL
// overrides to one).
func TestLiveRelay_WriteAllowlistTrustGate(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with reachable locked strfry relays) to run the live write-allowlist + trust-gate proof")
	}
	var relays []string
	if r := os.Getenv("RD_NOSTR_RELAY_URL"); r != "" {
		relays = []string{r}
	} else {
		var cfg rdconfig.Config
		relays = cfg.WriteRelayURLs()
	}
	if len(relays) == 0 {
		t.Fatal("no write relays configured")
	}

	trusted := liveRelayKey(t) // the admitted (allowlisted) portfolio key
	attacker, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen attacker key: %v", err)
	}
	if trusted.PubKeyHex() == attacker.PubKeyHex() {
		t.Fatal("trusted/attacker key collision")
	}
	trustSet := map[string]bool{trusted.PubKeyHex(): true}

	for _, relay := range relays {
		relay := relay
		t.Run(relay, func(t *testing.T) {
			itemID := fmt.Sprintf("ready-266-live-%d", time.Now().UnixNano())
			board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{trusted.PubKeyHex()}}
			now := time.Now().Unix()

			newPub := func(k *nostr.Key) *Publisher {
				return &Publisher{
					Key:         k,
					Log:         NewNostrLog(filepath.Join(t.TempDir(), ".ready", NostrLogFile)),
					WriteRelays: []string{relay},
					PendingPath: filepath.Join(t.TempDir(), ".ready", NostrPendingFile),
				}
			}

			// LAYER 1a — the allowlisted key's write is ACCEPTED by the relay.
			trustedCard := CardSpec{ItemID: itemID, Title: "legit", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready"}
			res, err := newPub(trusted).PublishItem(context.Background(), &board, trustedCard, now)
			if err != nil {
				t.Fatalf("trusted publish: %v", err)
			}
			for _, ev := range res.Events {
				if !ev.AnyRelay {
					t.Fatalf("RELAY-ALLOWLIST FAILED: allowlisted event kind %d REJECTED by relay (acks=%+v)", ev.Kind, ev.Acks)
				}
			}
			t.Logf("LAYER 1a PROVEN: allowlisted key %s accepted by %s", trusted.PubKeyHex(), relay)

			// LAYER 1b — the untrusted key's write is REJECTED by the relay.
			attackCard := CardSpec{ItemID: itemID, Title: "HIJACKED", Status: state.StatusDone, Priority: "p0", Type: "task", BoardD: "ready"}
			ares, err := newPub(attacker).PublishStatusChange(context.Background(), attackCard, "seized", now+10)
			if err != nil {
				t.Fatalf("attacker publish attempt: %v", err)
			}
			for _, ev := range ares.Events {
				if ev.AnyRelay {
					t.Fatalf("RELAY-ALLOWLIST FAILED: untrusted key %s write was ACCEPTED by %s (acks=%+v)", attacker.PubKeyHex(), relay, ev.Acks)
				}
				// The rejection must carry a message and never be a silent OK,false.
				var sawReject bool
				for _, ack := range ev.Acks {
					if !ack.Accepted && ack.Message != "" {
						sawReject = true
					}
				}
				if !sawReject {
					t.Fatalf("untrusted write rejected but with no reason from %s (acks=%+v)", relay, ev.Acks)
				}
			}
			t.Logf("LAYER 1b PROVEN: untrusted key %s REJECTED by %s (acks=%+v)", attacker.PubKeyHex(), relay, ares.Events[0].Acks)

			time.Sleep(1 * time.Second)

			// LAYER 1c — relay integrity: fetch the item back with the gate DISABLED
			// (nil trust set merges anything the relay serves). The untrusted write
			// never landed, so the relay's stored set has ZERO attacker events.
			relayLog := NewNostrLog(filepath.Join(t.TempDir(), ".ready", NostrLogFile))
			rr, err := ReconcileItem(context.Background(), []string{relay}, relayLog, itemID, nil, nostr.DefaultTimeout)
			if err != nil {
				t.Fatalf("reconcile (ungated): %v", err)
			}
			relayEvents, err := relayLog.ReadAll()
			if err != nil {
				t.Fatalf("read relay-fetched log: %v", err)
			}
			if len(relayEvents) == 0 {
				t.Fatalf("relay served nothing for %s — cannot distinguish rejection from an empty fetch (fetched=%d)", itemID, rr.Fetched)
			}
			for _, e := range relayEvents {
				if e.PubKey == attacker.PubKeyHex() {
					t.Fatalf("RELAY INTEGRITY FAILED: untrusted event %s is present on %s despite the write-allowlist", e.ID, relay)
				}
			}
			t.Logf("LAYER 1c PROVEN: %s served %d event(s) for %s, NONE from the untrusted key", relay, len(relayEvents), itemID)

			// LAYER 2 — client-side drop of an INJECTED poison event. Build the
			// attacker's forged card+status locally (a LATER takeover) and merge
			// them into the log next to the trusted events, simulating a poison
			// event that somehow reached the local log. The projection trust gate
			// must still drop it.
			ac, err := BuildCardEvent(attacker, attackCard, now+10)
			if err != nil {
				t.Fatalf("build attacker card: %v", err)
			}
			as, err := BuildStatusEvent(attacker, itemID, state.StatusDone, ac.ID, "seized", now+10)
			if err != nil {
				t.Fatalf("build attacker status: %v", err)
			}
			if _, err := relayLog.AppendUnique([]*nostr.Event{ac, as}); err != nil {
				t.Fatalf("inject attacker events into local log: %v", err)
			}
			poisoned, err := relayLog.ReadAll()
			if err != nil {
				t.Fatalf("reread poisoned log: %v", err)
			}
			var sawInjected bool
			for _, e := range poisoned {
				if e.PubKey == attacker.PubKeyHex() {
					sawInjected = true
				}
			}
			if !sawInjected {
				t.Fatal("attacker events were not injected — cannot prove the client gate")
			}
			items := ProjectItems(poisoned, ProjectOptions{Maintainers: trustSet, Trusted: trustSet})
			it, ok := items[itemID]
			if !ok {
				t.Fatal("trusted item not projected")
			}
			if it.Title != "legit" || it.Status != state.StatusActive {
				t.Fatalf("CLIENT TRUST GATE FAILED: item taken over by an injected poison event — title=%q status=%q (want legit/active)", it.Title, it.Status)
			}
			t.Logf("LAYER 2 PROVEN: injected untrusted takeover dropped at projection; trusted state intact (item %s)", itemID)
		})
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
