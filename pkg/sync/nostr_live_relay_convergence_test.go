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

// TestLiveRelay_SameSecondConvergence is the ground-source, no-mock proof for
// ready-f92's cross-source convergence guarantee (the b6a HIGH / 523 same-second
// reconcile bug). It publishes TWO competing card edits stamped in the SAME
// created_at second to a LIVE strfry relay, then proves that three independent
// projections — computed over physically DIFFERENT event sets and append/fetch
// orders — all converge to the IDENTICAL winning card:
//
//	(1) the publisher's LOCAL log, which holds BOTH competing edit cards (append order);
//	(2) a fresh log reconciled purely FROM the relay, which — because strfry enforces
//	    NIP-33 parameterized-replaceable semantics — physically retained only ONE of
//	    the two cards (the NIP-01 lowest-id winner), discarding the loser;
//	(3) the SAME reconciled event set, permuted into a different append order.
//
// The old (created_at, append-index) tie-break failed exactly here: source (1)
// picks by local append order while source (2) is whatever card strfry kept, so
// two machines diverged. With the NIP-01 id tie-break, the local winner is BY
// CONSTRUCTION the same card the relay retained — local and relay-reconciled state
// are identical. Gated behind RD_NOSTR_LIVE_RELAY=1.
func TestLiveRelay_SameSecondConvergence(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live convergence proof")
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
	itemID := fmt.Sprintf("ready-f92-live-%d", time.Now().UnixNano())
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".ready", NostrLogFile)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(logPath),
		WriteRelays: []string{relay},
		PendingPath: filepath.Join(dir, ".ready", NostrPendingFile),
	}
	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	trust := map[string]bool{k.PubKeyHex(): true}
	opts := ProjectOptions{Maintainers: trust, Trusted: trust}

	mustAccept := func(label string, res PublishResult, err error) PublishResult {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: publish: %v", label, err)
		}
		for _, ev := range res.Events {
			if !ev.AnyRelay {
				t.Fatalf("%s: event kind %d id %s NOT accepted by relay (acks=%+v)", label, ev.Kind, ev.EventID, ev.Acks)
			}
		}
		return res
	}
	cardIDOf := func(res PublishResult) string {
		for _, ev := range res.Events {
			if ev.Kind == KindCard {
				return ev.EventID
			}
		}
		return ""
	}

	now := time.Now().Unix()
	// create (active) @now, so the item exists and is authoritative.
	cRes, cErr := pub.PublishItem(context.Background(), &board, CardSpec{ItemID: itemID, Title: "base", Status: state.StatusActive, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), BoardD: "ready"}, now)
	mustAccept("create", cRes, cErr)

	// TWO competing card edits stamped in the SAME created_at second (now+5).
	// Different titles => different content => different event ids. This is the
	// concurrent same-second edit that used to diverge. BOTH edits are appended to
	// the LOCAL log unconditionally (publishEvents Phase 1); the relay, however,
	// enforces NIP-33 replaceable + the NIP-01 created_at-tie rule and RETAINS ONLY
	// the lowest-id card — the higher-id sibling is rejected with "replaced: have
	// newer event". So we require no transport error, but NOT relay acceptance of
	// both (the loser's rejection is the relay agreeing with our tie-break).
	editSec := now + 5
	resX, xErr := pub.PublishCardEdit(context.Background(), CardSpec{ItemID: itemID, Title: "edit-X", Status: state.StatusActive, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), BoardD: "ready"}, editSec)
	if xErr != nil {
		t.Fatalf("edit-X publish: %v", xErr)
	}
	resY, yErr := pub.PublishCardEdit(context.Background(), CardSpec{ItemID: itemID, Title: "edit-Y", Status: state.StatusActive, Priority: "p1", Type: "task", Assignee: k.PubKeyHex(), BoardD: "ready"}, editSec)
	if yErr != nil {
		t.Fatalf("edit-Y publish: %v", yErr)
	}
	idX, idY := cardIDOf(resX), cardIDOf(resY)
	if idX == "" || idY == "" || idX == idY {
		t.Fatalf("expected two distinct edit card ids, got X=%q Y=%q", idX, idY)
	}
	// NIP-01 canonical winner = lowest id.
	wantTitle, winnerID := "edit-X", idX
	if idY < idX {
		wantTitle, winnerID = "edit-Y", idY
	}
	// The relay MUST have accepted the canonical (lowest-id) winner at publish time —
	// whichever order it arrived, strfry keeps the lowest id.
	winnerAccepted := false
	for _, res := range []PublishResult{resX, resY} {
		for _, ev := range res.Events {
			if ev.EventID == winnerID && ev.AnyRelay {
				winnerAccepted = true
			}
		}
	}
	if !winnerAccepted {
		t.Fatalf("relay did not accept the canonical winner card %s (acks X=%+v Y=%+v)", winnerID, resX.Events, resY.Events)
	}
	t.Logf("competing same-second cards: X=%s Y=%s -> canonical winner title=%q id=%s", idX, idY, wantTitle, winnerID)

	time.Sleep(1 * time.Second)

	// SOURCE 1: publisher's LOCAL log holds BOTH edit cards (append order).
	localEvents, err := NewNostrLog(logPath).ReadAll()
	if err != nil {
		t.Fatalf("local log read: %v", err)
	}
	localCards := 0
	for _, e := range localEvents {
		if e.Kind == KindCard && tagValue(e, "d") == itemID && e.CreatedAt == editSec {
			localCards++
		}
	}
	if localCards != 2 {
		t.Fatalf("local log should hold BOTH same-second edit cards, found %d", localCards)
	}
	localState := summarize(t, localEvents, opts, itemID)
	if localState.title != wantTitle {
		t.Errorf("LOCAL projection title = %q, want %q (NIP-01 lowest-id winner)", localState.title, wantTitle)
	}

	// SOURCE 2: fresh log reconciled purely FROM the relay. strfry replaced the
	// addressable card, keeping exactly ONE of the two same-second edits.
	relayLogPath := filepath.Join(t.TempDir(), ".ready", NostrLogFile)
	relayLog := NewNostrLog(relayLogPath)
	rr, err := ReconcileItem(context.Background(), []string{relay}, relayLog, itemID, trust, nostr.DefaultTimeout)
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	t.Logf("reconcile: fetched=%d added=%d relay_errors=%v", rr.Fetched, rr.Added, rr.RelayErrors)
	relayEvents, err := relayLog.ReadAll()
	if err != nil {
		t.Fatalf("reread reconciled log: %v", err)
	}
	relayCards := 0
	var keptCardID string
	for _, e := range relayEvents {
		if e.Kind == KindCard && tagValue(e, "d") == itemID {
			relayCards++
			keptCardID = e.ID
		}
	}
	if relayCards != 1 {
		t.Fatalf("relay should retain exactly ONE card per NIP-33 replaceable, kept %d", relayCards)
	}
	// The card the RELAY physically kept must be the SAME one local projection picks.
	if keptCardID != winnerID {
		t.Fatalf("relay kept card %s, but NIP-01 canonical winner is %s — relay/local tie-break disagree", keptCardID, winnerID)
	}
	relayState := summarize(t, relayEvents, opts, itemID)

	// SOURCE 3: the SAME reconciled events in a DIFFERENT append order.
	permutedState := summarize(t, permute(relayEvents, 12345), opts, itemID)

	// CONVERGENCE: all three independent sources project the identical state.
	if !equalSummary(localState, relayState) {
		t.Fatalf("DIVERGENCE local vs relay-reconciled:\n  local=%+v\n  relay=%+v", localState, relayState)
	}
	if !equalSummary(relayState, permutedState) {
		t.Fatalf("DIVERGENCE relay vs permuted:\n  relay=%+v\n  permuted=%+v", relayState, permutedState)
	}
	t.Logf("PROVEN: same-second competing edits converge across local-log, relay-reconciled, and permuted sources; winner=%q status=%q", localState.title, localState.status)
}
