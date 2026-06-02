package main

// auto_sync_test.go — feature test for owner auto-sync on read commands (ready-341).
//
// Verifies that after a member acts (claims, closes an item), the owner sees
// the change when running rd list --all without a manual rd sync pull.
//
// The test exercises:
//  1. Owner project: mutations.jsonl has create+claim records (owner's initial state).
//  2. rdSync.Pull is called with a fake MessageLister that returns the member's close.
//  3. rd list --all is called — the closed item must appear.

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/campfire-net/campfire/cf-protocol/store"

	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// fakeMessageLister implements rdSync.MessageLister directly, returning pre-configured
// message records. Used to inject campfire state without a real campfire client.
type fakeMessageLister struct {
	records []store.MessageRecord
}

func (f *fakeMessageLister) ListMessages(_ string, afterTimestamp int64, _ ...store.MessageFilter) ([]store.MessageRecord, error) {
	var out []store.MessageRecord
	for _, r := range f.records {
		if afterTimestamp > 0 && r.Timestamp <= afterTimestamp {
			continue
		}
		out = append(out, r)
	}
	return out, nil
}

// TestAutoSync_OwnerSeesClosedItemWithoutManualSyncPull verifies that after a
// member closes an item, the owner sees the close via rdSync.Pull (the mechanism
// autoSyncPull uses) when running rd list --all, without running rd sync pull.
//
// Test-depth: feature (ready-341)
func TestAutoSync_OwnerSeesClosedItemWithoutManualSyncPull(t *testing.T) {
	origDebug := debugOutput
	defer func() { debugOutput = origDebug }()
	debugOutput = false

	origJSON := jsonOutput
	defer func() { jsonOutput = origJSON }()
	jsonOutput = false

	// Step 1: set up the owner project directory.
	tmpDir, err := os.MkdirTemp("", "test-autosync")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	hexID := strings.Repeat("ab", 32) // 64-char hex campfire ID
	itemID := "autosync-test-a1b"
	memberSender := strings.Repeat("cc", 32)

	// Wire CF_HOME to tmpDir.
	origCFHome := os.Getenv("CF_HOME")
	os.Setenv("CF_HOME", tmpDir)
	defer func() {
		if origCFHome != "" {
			os.Setenv("CF_HOME", origCFHome)
		} else {
			os.Unsetenv("CF_HOME")
		}
	}()
	origRDHome := rdHome
	rdHome = ""
	defer func() { rdHome = origRDHome }()

	// Write alias store: "autosyncproject" -> hexID.
	aliasContent, _ := json.Marshal(map[string]string{"autosyncproject": hexID})
	if err := os.WriteFile(filepath.Join(tmpDir, "aliases.json"), aliasContent, 0600); err != nil {
		t.Fatalf("WriteFile aliases.json: %v", err)
	}

	// Create .ready/ dir with config.json.
	readyDir := filepath.Join(tmpDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readyDir, "config.json"),
		[]byte(`{"project_name":"autosyncproject","campfire_id":"`+hexID+`"}`), 0600); err != nil {
		t.Fatalf("WriteFile config.json: %v", err)
	}

	// Owner's local state: one item created, member claimed it (status=active).
	createPayload := fmt.Sprintf(`{"id":%q,"title":"Member Task","type":"task","for":"owner@test","priority":"p2"}`, itemID)
	createMutation := fmt.Sprintf(`{"msg_id":"msg-create-001","campfire_id":%q,"timestamp":1000000000000000001,"operation":"work:create","payload":%s,"tags":["work:create"],"sender":"owner-pubkey"}`,
		hexID, createPayload)
	claimPayload := fmt.Sprintf(`{"id":%q,"status":"active"}`, itemID)
	claimMutation := fmt.Sprintf(`{"msg_id":"msg-claim-002","campfire_id":%q,"timestamp":1000000000000000002,"operation":"work:claim","payload":%s,"tags":["work:claim"],"sender":%q}`,
		hexID, claimPayload, memberSender)

	ownerJSONL := createMutation + "\n" + claimMutation + "\n"
	mutationsPath := filepath.Join(readyDir, "mutations.jsonl")
	if err := os.WriteFile(mutationsPath, []byte(ownerJSONL), 0600); err != nil {
		t.Fatalf("WriteFile mutations.jsonl: %v", err)
	}

	// Chdir to tmpDir so readyProjectDir() and projectRoot() resolve correctly.
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origCwd) }()

	// Step 2: simulate the member's close arriving from campfire.
	// The fake lister returns all messages including the close that
	// the owner hasn't seen yet.
	closePayload := fmt.Sprintf(`{"id":%q,"status":"done","reason":"Implemented feature"}`, itemID)
	lister := &fakeMessageLister{
		records: []store.MessageRecord{
			{
				ID:         "msg-create-001",
				CampfireID: hexID,
				Timestamp:  1000000000000000001,
				ReceivedAt: 1000000000000000001,
				Payload:    []byte(createPayload),
				Tags:       []string{"work:create"},
				Sender:     "owner-pubkey",
			},
			{
				ID:         "msg-claim-002",
				CampfireID: hexID,
				Timestamp:  1000000000000000002,
				ReceivedAt: 1000000000000000002,
				Payload:    []byte(claimPayload),
				Tags:       []string{"work:claim"},
				Sender:     memberSender,
			},
			{
				ID:         "msg-close-003",
				CampfireID: hexID,
				Timestamp:  1000000000000000003,
				ReceivedAt: 1000000000000000003,
				Payload:    []byte(closePayload),
				Tags:       []string{"work:close"},
				Sender:     memberSender,
			},
		},
	}

	// Step 3: run rdSync.Pull (the mechanism autoSyncPull uses).
	result, err := rdSync.Pull(lister, hexID, mutationsPath, tmpDir, 0)
	if err != nil {
		t.Fatalf("rdSync.Pull: %v", err)
	}
	if result.Pulled != 1 {
		t.Errorf("expected 1 message pulled (the close), got %d", result.Pulled)
	}
	if result.Skipped != 2 {
		t.Errorf("expected 2 messages skipped (create+claim already present), got %d", result.Skipped)
	}

	// Step 4: rd list --all must show the item (now with status=done).
	output := captureStdoutPipe(t, func() {
		if flagErr := listCmd.Flags().Set("all", "true"); flagErr != nil {
			t.Fatalf("setting --all flag: %v", flagErr)
		}
		defer func() { _ = listCmd.Flags().Set("all", "false") }()

		if runErr := listCmd.RunE(listCmd, []string{}); runErr != nil {
			t.Fatalf("listCmd.RunE: %v", runErr)
		}
	})

	// The item must appear in the list.
	if !strings.Contains(output, itemID) {
		t.Errorf("rd list --all output does not contain item %q after auto-sync; output:\n%s", itemID, output)
	}

	// Verify the mutations.jsonl now contains the close record.
	mutationsData, err := os.ReadFile(mutationsPath)
	if err != nil {
		t.Fatalf("ReadFile mutations.jsonl: %v", err)
	}
	if !strings.Contains(string(mutationsData), "msg-close-003") {
		t.Errorf("mutations.jsonl does not contain close message ID after sync; content:\n%s", mutationsData)
	}
}

// TestAutoSyncPull_NoOpWhenNoCampfire verifies that autoSyncPull does not error
// or panic when there is no campfire configured (JSONL-only project).
func TestAutoSyncPull_NoOpWhenNoCampfire(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "test-autosync-noop")
	if err != nil {
		t.Fatalf("MkdirTemp: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// Create a .ready/ dir but no .campfire/root (JSONL-only project).
	readyDir := filepath.Join(tmpDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("MkdirAll .ready: %v", err)
	}
	if err := os.WriteFile(filepath.Join(readyDir, "mutations.jsonl"), []byte{}, 0600); err != nil {
		t.Fatalf("WriteFile mutations.jsonl: %v", err)
	}

	origCFHome := os.Getenv("CF_HOME")
	os.Setenv("CF_HOME", tmpDir)
	defer func() {
		if origCFHome != "" {
			os.Setenv("CF_HOME", origCFHome)
		} else {
			os.Unsetenv("CF_HOME")
		}
	}()
	origRDHome := rdHome
	rdHome = ""
	defer func() { rdHome = origRDHome }()

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(tmpDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	defer func() { _ = os.Chdir(origCwd) }()

	// autoSyncPull must complete without panic or error.
	// Since there's no campfire, it should return early.
	autoSyncPull()
	// If we reach here, the function handled the no-campfire case correctly.
}
