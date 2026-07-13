package main

// byid_store_test.go — unit test for byIDFromJSONLOrStore's campfire-store
// fallback branch (relocated from the deleted admit_test.go, which tested the
// now-removed campfire admit command). This test exercises byIDFromJSONLOrStore
// (still live, in root.go), not any admit code.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
)

// TestByIDFromJSONLOrStore_StorePathUsedWhenNoJSONL verifies that
// byIDFromJSONLOrStore falls through to the campfire store (resolve.ByID)
// when there is no .ready/ project directory in scope — the store branch
// that is normally shadowed by the JSONL branch in a live rd project.
func TestByIDFromJSONLOrStore_StorePathUsedWhenNoJSONL(t *testing.T) {
	// Change the working directory to a temp dir with no .ready/ and no
	// .campfire/root so that readyProjectDir() returns ("", false) and
	// jsonlPath() returns "".  We restore the original cwd after the test.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	emptyDir := t.TempDir()
	if err := os.Chdir(emptyDir); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// Build a real SQLite store in a separate temp dir.
	storeDir := t.TempDir()
	s, err := store.Open(filepath.Join(storeDir, "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	defer s.Close()

	// Campfire and item setup.
	const campfireID = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	wantPubkey := pubkeyHex("e5")
	wantTitle := "Join Request Eve"
	wantID := "proj-eve42"

	// Register membership so state.DeriveFromStore can read from this campfire.
	if addErr := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: t.TempDir(),
		JoinProtocol: "invite",
		Role:         "full",
		JoinedAt:     time.Now().Unix(),
	}); addErr != nil {
		t.Fatalf("AddMembership: %v", addErr)
	}

	// Write a work:create message with a For field (the join-request pubkey).
	payload, _ := json.Marshal(map[string]interface{}{
		"id":    wantID,
		"title": wantTitle,
		"type":  "task",
		"for":   wantPubkey,
	})
	ts := time.Now().UnixNano()
	if _, addErr := s.AddMessage(store.MessageRecord{
		ID:         "msg-eve-store",
		CampfireID: campfireID,
		Sender:     "testkey",
		Payload:    payload,
		Tags:       []string{"work:create"},
		Timestamp:  ts,
		Signature:  []byte("fakesig-msg-eve-store"),
		ReceivedAt: ts,
	}); addErr != nil {
		t.Fatalf("AddMessage: %v", addErr)
	}

	// byIDFromJSONLOrStore must use the store branch (no JSONL path in scope).
	item, err := byIDFromJSONLOrStore(s, wantID)
	if err != nil {
		t.Fatalf("byIDFromJSONLOrStore via store: unexpected error: %v", err)
	}
	if item.ID != wantID {
		t.Errorf("item.ID = %q, want %q", item.ID, wantID)
	}
	if item.Title != wantTitle {
		t.Errorf("item.Title = %q, want %q", item.Title, wantTitle)
	}
	if item.For != wantPubkey {
		t.Errorf("item.For = %q, want requester pubkey %q", item.For, wantPubkey)
	}

	// Verify ErrNotFound when the ID doesn't exist.
	_, err = byIDFromJSONLOrStore(s, "proj-nonexistent")
	if err == nil {
		t.Fatal("expected ErrNotFound for unknown item ID, got nil")
	}
}
