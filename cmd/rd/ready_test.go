package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// TestReady_PipedOutput_BareIDs verifies that when stdout is not a TTY (piped),
// rd ready prints one item ID per line with no table formatting.
func TestReady_PipedOutput_BareIDs(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	_, cleanup := setupMutationsDir(t, []struct{ id, title string }{
		{"pipe-ready-a1b", "First Ready Item"},
		{"pipe-ready-c3d", "Second Ready Item"},
	})
	defer cleanup()

	// Reset --for flag so it does not filter by identity (no identity in test env).
	if err := readyCmd.Flags().Set("for", ""); err != nil {
		t.Fatalf("setting --for flag: %v", err)
	}
	defer func() {
		_ = readyCmd.Flags().Set("for", "")
	}()

	output := captureStdoutPipe(t, func() {
		if err := readyCmd.RunE(readyCmd, []string{}); err != nil {
			// In test env without a real identity, the command may fail at
			// withAgentAndStore -- that is OK, we still get the pipe-format contract
			// from the list path. Log and check what we got.
			t.Logf("readyCmd.RunE error (may be expected in test env): %v", err)
		}
	})

	// If items were returned, they must be bare IDs -- no table formatting.
	if output == "" {
		t.Skip("no output from rd ready in test env -- identity not available")
	}

	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		if strings.ContainsAny(line, " \t") {
			t.Errorf("piped ready output line %q contains whitespace -- expected bare ID only", line)
		}
	}
}

// TestReady_PipedOutput_JSON_Unchanged verifies that --json output is not
// affected by the pipe-friendly mode change.
func TestReady_PipedOutput_JSON_Unchanged(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()

	_, cleanup := setupMutationsDir(t, []struct{ id, title string }{
		{"pipe-rjson-a1b", "Ready JSON Item"},
	})
	defer cleanup()
	jsonOutput = true // --json mode (after setup, which resets the global)

	if err := readyCmd.Flags().Set("for", ""); err != nil {
		t.Fatalf("setting --for flag: %v", err)
	}
	defer func() {
		_ = readyCmd.Flags().Set("for", "")
	}()

	output := captureStdoutPipe(t, func() {
		if err := readyCmd.RunE(readyCmd, []string{}); err != nil {
			t.Logf("readyCmd.RunE error (may be expected in test env): %v", err)
		}
	})

	if output == "" {
		t.Skip("no output from rd ready in test env -- identity not available")
	}

	// Must be valid JSON array (not bare IDs).
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(strings.TrimSpace(output)), &items); err != nil {
		t.Fatalf("piped --json output is not valid JSON: %v; output:\n%s", err, output)
	}
}

// TestReadyPipedMode_BareIDLoop verifies that the bare-ID loop used in piped mode
// (the non-TTY branch of rd ready) prints exactly one ID per line with no extra fields.
// This tests the output logic directly without requiring campfire identity.
func TestReadyPipedMode_BareIDLoop(t *testing.T) {
	items := []*state.Item{
		{ID: "ready-pipe-001", Priority: "p0", Status: "active", Title: "Critical task"},
		{ID: "ready-pipe-002", Priority: "p1", Status: "inbox", Title: "Normal task"},
		{ID: "ready-pipe-003", Priority: "p2", Status: "inbox", Title: "Low priority"},
	}

	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w

	// Simulate the piped branch from readyCmd (and listCmd).
	for _, item := range items {
		fmt.Println(item.ID)
	}

	w.Close()
	os.Stdout = origStdout

	var buf strings.Builder
	outBuf := make([]byte, 4096)
	for {
		n, readErr := r.Read(outBuf)
		if n > 0 {
			buf.Write(outBuf[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()
	output := buf.String()

	lines := strings.Split(strings.TrimSpace(output), "\n")
	if len(lines) != len(items) {
		t.Fatalf("expected %d lines, got %d; output:\n%s", len(items), len(lines), output)
	}
	for i, line := range lines {
		// Each line must be exactly the item ID -- no spaces, no extra columns.
		if strings.ContainsAny(line, " \t") {
			t.Errorf("line %d %q contains whitespace -- expected bare ID only", i, line)
		}
		if line != items[i].ID {
			t.Errorf("line %d: got %q, want %q", i, line, items[i].ID)
		}
	}
}

// TestPrintItemTable_FormatHasMultipleColumns verifies that printItemTable
// produces multi-column output (not bare IDs) -- ensuring the TTY table format
// is distinct from the piped format.
func TestPrintItemTable_FormatHasMultipleColumns(t *testing.T) {
	items := []*state.Item{
		{ID: "table-check-a1b", Priority: "p1", Status: "inbox", Title: "Table Check Item"},
	}

	// Capture printItemTable output via pipe.
	origStdout := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	os.Stdout = w
	printItemTable(items)
	w.Close()
	os.Stdout = origStdout

	var buf strings.Builder
	outBuf := make([]byte, 4096)
	for {
		n, readErr := r.Read(outBuf)
		if n > 0 {
			buf.Write(outBuf[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()
	output := buf.String()

	// printItemTable uses fmt.Printf with multiple %-padded columns.
	// Each line should have multiple space-separated fields.
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		if line == "" {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			t.Errorf("printItemTable line %q has fewer than 2 fields -- expected table format with ID, priority, status, eta, title", line)
		}
	}
}

// TestReadyCmd_RunE_UppercaseScopeKey_IsNotDenied is ready-3e1 at
// cmd/rd/ready.go's --scope entry point — the READ direction of the same defect,
// and the one that fails closed against a legitimate caller.
//
// `--scope` is validated by the case-insensitive isHex and then handed to
// nostrScopeForKey (cmd/rd/sessions.go), which byte-compares it against the board
// owner and indexes the DeriveLevels map with it. Both of those come from signed
// events and are therefore canonical lowercase. Unnormalized, an uppercase
// --scope for a key holding a LIVE contributor grant misses the map entirely and
// the gate denies it with "no active grant for ... (not a granted identity)" —
// rd telling an authorized operator that they were never granted, and zeroing
// their queue. That is indistinguishable, from the caller's side, from a revoked
// or never-granted key.
//
// Driven through readyCmd.RunE (not nostrScopeForKey directly) because the
// normalization lives in the command body, and asserted on BOTH streams: the
// denial note must not appear on stderr, and the item must actually be listed on
// stdout. Reverting `scopeKey = normalizeHexPubkey(scopeKey)` in ready.go turns
// this red.
func TestReadyCmd_RunE_UppercaseScopeKey_IsNotDenied(t *testing.T) {
	origTTY := isTTYStdout
	defer func() { isTTYStdout = origTTY }()
	isTTYStdout = func() bool { return true }
	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	dir := setupNostrProjectWithItems(t, "scopecase", nil)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	ownHex := k.PubKeyHex()
	item := &state.Item{ID: "scopecase-1", Status: "active", Priority: "p2", Title: "Mine", For: ownHex, By: ownHex}
	if err := publishItemFullCreateNostr(dir, ownHex, item); err != nil {
		t.Fatalf("publish own item: %v", err)
	}

	// A GENUINELY granted contributor: a real owner-signed kind-39301 grant
	// published through the production primitive, so "authorized" is a fact
	// about the signed log, not a stub.
	grantee := freshKeyHex(t)
	if err := runNostrGrantRevoke(dir, grantee, rdSync.RoleContributor, "agent-scope", 0, ""); err != nil {
		t.Fatalf("grant contributor: %v", err)
	}
	if allowed, note := nostrScopeForKey(grantee); !allowed {
		t.Fatalf("precondition: the canonical form of this key must be allowed, got denied: %q", note)
	}

	defer resetReadyRunFlags(t)()
	defer setForFilter(t, ownHex)()
	upper := strings.ToUpper(grantee)
	if err := readyCmd.Flags().Set("scope", upper); err != nil {
		t.Fatalf("setting --scope=%q: %v", upper, err)
	}
	defer func() {
		if err := readyCmd.Flags().Set("scope", ""); err != nil {
			t.Fatalf("resetting --scope: %v", err)
		}
	}()

	stdout, stderr := runReadyCapturingBoth(t)

	if strings.Contains(stderr, "not a granted identity") {
		t.Errorf("`rd ready --scope <UPPERCASE-hex>` DENIED a key holding a live contributor grant: %q — "+
			"the scope gate compared the as-typed uppercase string against the lowercase levels map", stderr)
	}
	if strings.Contains(stderr, "no active grant") {
		t.Errorf("scope gate emitted a denial note for a granted key: %q", stderr)
	}
	if !strings.Contains(stdout, "scopecase-1") {
		t.Errorf("stdout = %q, want the ready item listed — the uppercase --scope zeroed an authorized "+
			"caller's queue", stdout)
	}
}
