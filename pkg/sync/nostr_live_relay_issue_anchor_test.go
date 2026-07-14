// Ground-source, no-mock proof for ready-da7's NIP-34 issue-root anchor.
//
// TestLiveRelay_GenericNIP34ClientAssociatesStatusWithIssue plays the part of a
// GENERIC NIP-34 issue-tracker client (not rd): it never calls ProjectItems or
// any rd-specific projection code. It only does what a generic client can do —
// fetch a kind:1621 issue event by id, then fetch kind:1630-1632 status events
// whose "e" tags reference that issue id — and asserts that association works
// against a LIVE relay. It then separately asserts rd's OWN projection
// (ProjectItems) still reconstructs the item correctly from the SAME raw event
// set, proving the additive anchor changed nothing about rd's own read path.
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

func TestLiveRelay_GenericNIP34ClientAssociatesStatusWithIssue(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live NIP-34 issue-anchor proof")
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

	k := liveRelayKey(t)
	itemID := fmt.Sprintf("ready-da7-live-%d", time.Now().UnixNano())
	dir := t.TempDir()
	logPath := filepath.Join(dir, ".ready", NostrLogFile)
	pub := &Publisher{
		Key:         k,
		Log:         NewNostrLog(logPath),
		WriteRelays: []string{relay},
		PendingPath: filepath.Join(dir, ".ready", NostrPendingFile),
	}
	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{k.PubKeyHex()}}
	card := CardSpec{
		ItemID: itemID, Title: "NIP-34 issue-anchor live proof", Status: state.StatusActive,
		Priority: "p1", Type: "task", Context: "generic-client interop check (ready-da7)", BoardD: "ready",
	}

	// --- CREATE (with a close/change reason so we also prove reason-carry, ready-b5f
	// parity, holds on the additive path): publish board+card+issue+status. ---
	res, err := pub.PublishItemWithReason(context.Background(), &board, card, "claimed for the interop demo", time.Now().Unix())
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	var issueID, statusID, cardID string
	for _, ev := range res.Events {
		if !ev.AnyRelay {
			t.Fatalf("event kind %d id %s was NOT accepted by the relay (acks=%+v)", ev.Kind, ev.EventID, ev.Acks)
		}
		switch ev.Kind {
		case KindIssue:
			issueID = ev.EventID
		case KindStatusOpen:
			statusID = ev.EventID
		case KindCard:
			cardID = ev.EventID
		}
	}
	if issueID == "" || statusID == "" || cardID == "" {
		t.Fatalf("missing published issue/status/card ids: issue=%q status=%q card=%q", issueID, statusID, cardID)
	}
	time.Sleep(1 * time.Second) // let the relay index before querying it back

	ctx := context.Background()

	// --- GENERIC CLIENT STEP 1: fetch the kind:1621 issue by id (as if the client
	// had it bookmarked/linked from elsewhere — no rd-specific knowledge needed). ---
	issueEvents, err := nostr.FetchMany(ctx, relay, map[string]any{"ids": []string{issueID}})
	if err != nil {
		t.Fatalf("generic client: fetch issue by id: %v", err)
	}
	if len(issueEvents) != 1 || issueEvents[0].Kind != KindIssue {
		t.Fatalf("generic client: expected exactly 1 kind:1621 issue event, got %d: %+v", len(issueEvents), issueEvents)
	}
	if subj, _ := tagFirst(issueEvents[0].Tags, "subject"); subj != card.Title {
		t.Errorf("generic client: issue subject = %q, want %q", subj, card.Title)
	}

	// --- GENERIC CLIENT STEP 2: the CORE interop proof — fetch NIP-34 status
	// events (kinds 1630-1632) whose "e" tag references the issue, exactly the
	// query a generic issue-tracker client runs to show an issue's current status.
	// This uses ONLY the issue id — no rd extension tags, no 30302/NIP-100
	// knowledge at all. ---
	statusEvents, err := nostr.FetchMany(ctx, relay, map[string]any{
		"kinds": []int{KindStatusOpen, KindStatusResolved, KindStatusClosed},
		"#e":    []string{issueID},
	})
	if err != nil {
		t.Fatalf("generic client: fetch status events by issue e-tag: %v", err)
	}
	var found *nostr.Event
	for _, e := range statusEvents {
		if e.ID == statusID {
			found = e
		}
	}
	if found == nil {
		t.Fatalf("generic client: status event %s NOT found via #e=%s query (got %d events: %+v)", statusID, issueID, len(statusEvents), statusEvents)
	}
	// Confirm the reference carries the NIP-10 "root" marker a generic client
	// looks for to distinguish the issue root from a reply/comment reference.
	rootMarked := false
	for _, tg := range found.Tags {
		if len(tg) >= 4 && tg[0] == "e" && tg[1] == issueID && tg[3] == "root" {
			rootMarked = true
		}
	}
	if !rootMarked {
		t.Errorf("generic client: status event's issue e-tag missing \"root\" marker: %v", found.Tags)
	}
	if s, _ := tagFirst(found.Tags, "status"); s != state.StatusActive {
		t.Errorf("generic client: status tag = %q, want %q", s, state.StatusActive)
	}
	if found.Content != "claimed for the interop demo" {
		t.Errorf("generic client: status content (reason) = %q, want %q", found.Content, "claimed for the interop demo")
	}
	t.Logf("PROVEN: a generic NIP-34 client can fetch issue %s and, from it ALONE, find status event %s via the #e/root query", issueID, statusID)

	// --- rd's OWN projection, from the SAME raw relay data, is UNCHANGED: the
	// issue event is silently ignored (itemIDForEvent returns "" for kind 1621),
	// and the card+status anchor rd actually reads (the FIRST "a"/"e" tag, still
	// the 30302 card) still reconstructs the item correctly. ---
	all, err := nostr.FetchMany(ctx, relay, map[string]any{"authors": []string{k.PubKeyHex()}, "#d": []string{itemID}})
	if err != nil {
		t.Fatalf("rd projection: fetch by #d=%s: %v", itemID, err)
	}
	items := ProjectItems(all, ProjectOptions{Maintainers: map[string]bool{k.PubKeyHex(): true}})
	assertMatches(t, "rd's own projection unaffected by the additive issue anchor", items[itemID], card)
	if got := items[itemID].History; len(got) == 0 || got[len(got)-1].Note != "claimed for the interop demo" {
		t.Fatalf("rd projection: close-with-reason lost: history=%+v", got)
	}
}

// tagFirst returns the first value of the first tag named `name`, or "".
func tagFirst(tags [][]string, name string) (string, bool) {
	for _, tg := range tags {
		if len(tg) >= 2 && tg[0] == name {
			return tg[1], true
		}
	}
	return "", false
}
