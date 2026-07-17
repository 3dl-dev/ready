package main

import (
	"encoding/json"
	"testing"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// TestPublishBoardCmd_ArgValidation proves `rd log publish` rejects the
// mutually-exclusive combinations of an item-id argument and --board, and
// requires exactly one of the two (ready-866). No relay/network involved — pure
// flag/arg dispatch.
func TestPublishBoardCmd_ArgValidation(t *testing.T) {
	setupNostrNativeProject(t)

	// --board with an item-id argument is rejected.
	nostrPublishCmd.Flags().Set("board", "true")
	err := nostrPublishCmd.RunE(nostrPublishCmd, []string{"ready-abc"})
	nostrPublishCmd.Flags().Set("board", "false")
	if err == nil {
		t.Fatal("publish --board <item-id>: expected an error (mutually exclusive), got nil")
	}

	// No item-id and no --board is rejected too (nothing to publish).
	err = nostrPublishCmd.RunE(nostrPublishCmd, nil)
	if err == nil {
		t.Fatal("publish with no item-id and no --board: expected an error, got nil")
	}
}

// TestNostrNative_PublishBoardCmd_PublishesEveryLocalEvent is the ready-866
// CLI-level proof (non-live-relay; the live-relay ground-source proof is
// pkg/sync.TestLiveRelay_BoardPublishConverges): it seeds MULTIPLE items purely
// via runCreateNostr (each landing in the local authoritative log), points the
// write relay at an unreachable address (matching Group A's contract in
// nostr_test.go — durability is proven by the log append succeeding and the
// relay attempt failing fast, not by an actual delivery), then runs
// `rd log publish --board` and asserts the publish attempted EVERY item's
// events — not just one — proving the board scope, not the single-item scope,
// drove the call.
func TestNostrNative_PublishBoardCmd_PublishesEveryLocalEvent(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	t.Setenv("RD_NOSTR_RELAY_URL", unreachableRelayURL)

	const n = 3
	ids := make([]string, n)
	for i := 0; i < n; i++ {
		id, err := runCreateNostr(dir, nostrCreateSpec{
			title: "board item", itemType: "task", priority: "p2", context: "ctx",
		})
		if err != nil {
			t.Fatalf("runCreateNostr %d: %v", i, err)
		}
		ids[i] = id
	}

	// Sanity: every seeded item is present in the LOCAL log projection before we
	// exercise the board publish.
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	preEvents, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read local log: %v", err)
	}
	byID := rdSync.ProjectItems(preEvents, rdSync.ProjectOptions{})
	for _, id := range ids {
		if _, ok := byID[id]; !ok {
			t.Fatalf("seed sanity: item %s missing from local log projection", id)
		}
	}

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })

	if err := nostrPublishCmd.Flags().Set("board", "true"); err != nil {
		t.Fatalf("set --board: %v", err)
	}
	t.Cleanup(func() { nostrPublishCmd.Flags().Set("board", "false") })

	stdout := captureStdoutPipe(t, func() {
		if err := nostrPublishCmd.RunE(nostrPublishCmd, nil); err != nil {
			t.Fatalf("publish --board: %v", err)
		}
	})

	var res rdSync.PublishResult
	if err := json.Unmarshal([]byte(stdout), &res); err != nil {
		t.Fatalf("unmarshal publish result: %v\nstdout=%s", err, stdout)
	}

	// Every item contributed at least a card event to the attempted publish set —
	// proving --board swept the WHOLE local log, not just one item's current state
	// (the single-item `rd log publish <id>` path this test would otherwise be
	// indistinguishable from).
	seenD := map[string]bool{}
	for _, ev := range res.Events {
		if ev.Kind == rdSync.KindCard {
			// EventAck does not carry the card's "d" tag, so instead assert on
			// count: every item's card contributes exactly one KindCard ack.
			seenD[ev.EventID] = true
		}
	}
	if len(seenD) < n {
		t.Fatalf("publish --board attempted %d distinct card event(s), want at least %d (one per seeded item) — got %d total events",
			len(seenD), n, len(res.Events))
	}
	// Every attempted event must have failed to reach the unreachable relay and
	// been buffered — the durability contract (log append already happened at
	// create time; this call's ONLY job was the relay attempt).
	if !res.Buffered {
		t.Fatalf("publish --board against an unreachable relay should report Buffered=true, got %+v", res)
	}
	for _, ev := range res.Events {
		if ev.AnyRelay {
			t.Fatalf("event kind %d id %s unexpectedly reached the unreachable relay", ev.Kind, ev.EventID)
		}
	}
}
