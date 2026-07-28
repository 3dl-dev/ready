package main

import (
	"encoding/json"
	"strings"
	"testing"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// TestPublishBoardCmd_DryRunPublishesNothing is the ready-260 safety contract at
// the CLI seam. A bulk republish writes to production data across every board in
// a portfolio, so the operator must be able to see the shape of the write first —
// and "see it first" is worthless if the dry run can still touch a relay. This
// points the write relay at an unreachable address: a dry run that dialed
// anything would surface as a buffered/errored publish result, and the pending
// retry queue would grow.
func TestPublishBoardCmd_DryRunPublishesNothing(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	t.Setenv("RD_NOSTR_RELAY_URL", unreachableRelayURL)

	const n = 3
	for i := 0; i < n; i++ {
		if _, err := runCreateNostr(dir, nostrCreateSpec{
			title: "dry run item", itemType: "task", priority: "p2", context: "ctx",
		}); err != nil {
			t.Fatalf("runCreateNostr %d: %v", i, err)
		}
	}

	logBefore, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}

	origJSON := jsonOutput
	jsonOutput = true
	t.Cleanup(func() { jsonOutput = origJSON })
	if err := nostrPublishCmd.Flags().Set("board", "true"); err != nil {
		t.Fatalf("set --board: %v", err)
	}
	if err := nostrPublishCmd.Flags().Set("dry-run", "true"); err != nil {
		t.Fatalf("set --dry-run: %v", err)
	}
	t.Cleanup(func() {
		nostrPublishCmd.Flags().Set("board", "false")   //nolint:errcheck // test cleanup
		nostrPublishCmd.Flags().Set("dry-run", "false") //nolint:errcheck // test cleanup
	})

	stdout := captureStdoutPipe(t, func() {
		if err := nostrPublishCmd.RunE(nostrPublishCmd, nil); err != nil {
			t.Fatalf("publish --board --dry-run: %v", err)
		}
	})

	var plan rdSync.BoardPublishPlan
	if err := json.Unmarshal([]byte(stdout), &plan); err != nil {
		t.Fatalf("unmarshal plan: %v\nstdout=%s", err, stdout)
	}
	if plan.Items < n {
		t.Fatalf("plan reports %d items, want at least %d seeded", plan.Items, n)
	}
	if plan.Events == 0 || plan.ByKind[itoa(rdSync.KindCard)] < n {
		t.Fatalf("plan = %+v, want a per-kind breakdown covering %d cards", plan, n)
	}
	if !plan.HasBoardDefinition {
		t.Fatal("plan.HasBoardDefinition = false for a project whose board was just initialised")
	}
	if plan.BoardCoord == "" {
		t.Fatal("plan.BoardCoord is empty — the dry run must name the scope it would publish")
	}

	logAfter, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log after: %v", err)
	}
	if len(logAfter) != len(logBefore) {
		t.Fatalf("dry run changed the authoritative log (%d -> %d events)", len(logBefore), len(logAfter))
	}
}

// TestRelayRepairCmd_RequiresAnExplicitRelay locks the one thing a repair cannot
// be allowed to guess. A repair is defined against ONE relay's measured gap; if
// it silently fell back to the configured relay set it would publish the gap
// measured on one relay to relays that never had it, and the "accepted by ANY
// relay" report would be back in the middle of an operation whose entire purpose
// is to be unambiguous about a single relay.
func TestRelayRepairCmd_RequiresAnExplicitRelay(t *testing.T) {
	setupNostrNativeProject(t)
	t.Setenv("RD_NOSTR_RELAY_URL", unreachableRelayURL)

	err := relayRepairCmd.RunE(relayRepairCmd, nil)
	if err == nil || !strings.Contains(err.Error(), "--relay") {
		t.Fatalf("relay repair with no --relay: got %v, want a refusal naming --relay", err)
	}
}

// TestPublishBoardCmd_SafetyFlagsRequireBoard proves --dry-run and --relay are
// refused outside --board rather than silently ignored. An operator who typed
// --dry-run and got a real write to a production relay would have every reason
// to believe nothing had happened.
func TestPublishBoardCmd_SafetyFlagsRequireBoard(t *testing.T) {
	setupNostrNativeProject(t)

	if err := nostrPublishCmd.Flags().Set("dry-run", "true"); err != nil {
		t.Fatalf("set --dry-run: %v", err)
	}
	err := nostrPublishCmd.RunE(nostrPublishCmd, []string{"ready-abc"})
	nostrPublishCmd.Flags().Set("dry-run", "false") //nolint:errcheck // test cleanup
	if err == nil || !strings.Contains(err.Error(), "--board") {
		t.Fatalf("publish <item-id> --dry-run: got %v, want a refusal naming --board", err)
	}

	if err := nostrPublishCmd.Flags().Set("relay", "wss://example.invalid"); err != nil {
		t.Fatalf("set --relay: %v", err)
	}
	err = nostrPublishCmd.RunE(nostrPublishCmd, []string{"ready-abc"})
	nostrPublishCmd.Flags().Set("relay", "") //nolint:errcheck // test cleanup
	if err == nil || !strings.Contains(err.Error(), "--board") {
		t.Fatalf("publish <item-id> --relay: got %v, want a refusal naming --board", err)
	}
}
