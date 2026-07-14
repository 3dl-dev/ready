package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/rdconfig"
)

// TestPinBoard_RefusesOnCampfireBackedProject verifies that `rd nostr pin-board`
// refuses (ready-88d) when .campfire/root exists, instead of silently pinning
// a nostr board coordinate that would orphan the project's existing campfire
// history (initNostr() already guards the analogous case for `rd init`; this
// closes the same gap for pin-board, found by ready-2fd).
func TestPinBoard_RefusesOnCampfireBackedProject(t *testing.T) {
	projectDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// A campfire-backed project: .campfire/root + .ready/ with pre-existing
	// (non-board) state that must not be silently orphaned.
	campfireDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		t.Fatalf("mkdir .campfire: %v", err)
	}
	idBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	campfireID := hex.EncodeToString(idBytes)
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(campfireID), 0600); err != nil {
		t.Fatalf("writing .campfire/root: %v", err)
	}
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	preExisting := &rdconfig.SyncConfig{CampfireID: campfireID, ProjectName: "guardtest"}
	if err := rdconfig.SaveSyncConfig(projectDir, preExisting); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	ownerBytes := make([]byte, 32)
	if _, err := rand.Read(ownerBytes); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	owner := hex.EncodeToString(ownerBytes)

	cmd := nostrPinBoardCmd
	cmd.SetArgs(nil)
	if err := cmd.Flags().Set("owner", owner); err != nil {
		t.Fatalf("set owner flag: %v", err)
	}
	if err := cmd.Flags().Set("board-d", "guardtest"); err != nil {
		t.Fatalf("set board-d flag: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Flags().Set("owner", "")
		_ = cmd.Flags().Set("board-d", "")
		_ = cmd.Flags().Set("force", "false")
	})

	err = cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("pin-board on a campfire-backed project succeeded; want refusal")
	}
	if !strings.Contains(err.Error(), "rd migrate") {
		t.Errorf("refusal error %q does not point to 'rd migrate'", err.Error())
	}
	if !strings.Contains(err.Error(), "--force") {
		t.Errorf("refusal error %q does not mention --force escape hatch", err.Error())
	}

	// No history orphaned: the pre-existing sync config (campfire identity)
	// must be untouched, and Board must remain unset.
	got, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig after refusal: %v", err)
	}
	if got.Board != "" {
		t.Errorf("Board = %q after refused pin-board; want empty (unpinned)", got.Board)
	}
	if got.CampfireID != campfireID {
		t.Errorf("CampfireID = %q after refused pin-board; want unchanged %q", got.CampfireID, campfireID)
	}
	if got.ProjectName != "guardtest" {
		t.Errorf("ProjectName = %q after refused pin-board; want unchanged %q", got.ProjectName, "guardtest")
	}
}

// TestPinBoard_ForceOverridesGuard verifies --force still allows pinning on a
// campfire-backed project (explicit opt-in, not a silent default).
func TestPinBoard_ForceOverridesGuard(t *testing.T) {
	projectDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	campfireDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		t.Fatalf("mkdir .campfire: %v", err)
	}
	idBytes := make([]byte, 32)
	if _, err := rand.Read(idBytes); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	campfireID := hex.EncodeToString(idBytes)
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(campfireID), 0600); err != nil {
		t.Fatalf("writing .campfire/root: %v", err)
	}
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	if err := rdconfig.SaveSyncConfig(projectDir, &rdconfig.SyncConfig{CampfireID: campfireID, ProjectName: "forcetest"}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	ownerBytes := make([]byte, 32)
	if _, err := rand.Read(ownerBytes); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	owner := hex.EncodeToString(ownerBytes)

	cmd := nostrPinBoardCmd
	cmd.SetArgs(nil)
	if err := cmd.Flags().Set("owner", owner); err != nil {
		t.Fatalf("set owner flag: %v", err)
	}
	if err := cmd.Flags().Set("board-d", "forcetest"); err != nil {
		t.Fatalf("set board-d flag: %v", err)
	}
	if err := cmd.Flags().Set("force", "true"); err != nil {
		t.Fatalf("set force flag: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Flags().Set("owner", "")
		_ = cmd.Flags().Set("board-d", "")
		_ = cmd.Flags().Set("force", "false")
	})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("pin-board --force on campfire-backed project: %v", err)
	}

	got, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig after forced pin: %v", err)
	}
	wantCoord := "30301:" + owner + ":forcetest"
	if got.Board != wantCoord {
		t.Errorf("Board = %q, want %q", got.Board, wantCoord)
	}
}
