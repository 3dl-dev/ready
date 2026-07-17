package main

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/rdconfig"
)

// ready-8ff: the binding command is `rd link`, not `rd pin-board`. pin-board
// stays as a HIDDEN deprecated alias routing to the SAME RunE (do not delete
// it this release) — verified by comparing function pointers via reflection is
// not possible in Go, so these tests instead verify the user-visible contract:
// both commands are wired to rootCmd, `rd link` is visible, `rd pin-board` is
// hidden, and both produce identical linking behavior.

// TestLinkCmd_Visible verifies `rd link` is NOT hidden — it is the primary
// binding command and must show up in `rd --help`.
func TestLinkCmd_Visible(t *testing.T) {
	if nostrLinkCmd.Hidden {
		t.Error("nostrLinkCmd.Hidden = true; want false so 'rd link' is listed in 'rd --help'")
	}
	if findChild(rootCmd, "link") == nil {
		t.Error("`rd link` missing from rootCmd — must be a top-level command")
	}
}

// TestPinBoardCmd_HiddenDeprecatedAlias verifies `rd pin-board` is now HIDDEN
// (ready-8ff supersedes ready-24a's unhide) but still registered and runnable —
// it is a deprecated alias, not deleted.
func TestPinBoardCmd_HiddenDeprecatedAlias(t *testing.T) {
	if !nostrPinBoardCmd.Hidden {
		t.Error("nostrPinBoardCmd.Hidden = false; want true — pin-board is now a hidden deprecated alias for 'rd link'")
	}
	if findChild(rootCmd, "pin-board") == nil {
		t.Error("`rd pin-board` missing from rootCmd — it must stay registered as a hidden alias, not be deleted")
	}
}

// TestLinkCmd_BarePrintsLinkedBoard verifies bare `rd link` (no args, no
// flags) prints the currently-linked board instead of erroring or re-linking.
func TestLinkCmd_BarePrintsLinkedBoard(t *testing.T) {
	projectDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	ownerBytes := make([]byte, 32)
	if _, err := rand.Read(ownerBytes); err != nil {
		t.Fatalf("rand.Read: %v", err)
	}
	owner := hex.EncodeToString(ownerBytes)
	coord := "30301:" + owner + ":ready"
	if err := rdconfig.SaveSyncConfig(projectDir, &rdconfig.SyncConfig{Board: coord}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	cmd := nostrLinkCmd
	cmd.SetArgs(nil)
	t.Cleanup(func() {
		_ = cmd.Flags().Set("owner", "")
		_ = cmd.Flags().Set("board-d", "")
		_ = cmd.Flags().Set("force", "false")
	})

	origStdout := os.Stdout
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("os.Pipe: %v", perr)
	}
	os.Stdout = w
	runErr := cmd.RunE(cmd, nil)
	w.Close()
	os.Stdout = origStdout

	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()

	if runErr != nil {
		t.Fatalf("bare `rd link` RunE error: %v", runErr)
	}
	out := b.String()
	if !strings.Contains(out, "linked to board: ready") {
		t.Errorf("bare `rd link` output %q does not report the linked board d (ready)", out)
	}
	if !strings.Contains(out, owner[:8]) {
		t.Errorf("bare `rd link` output %q does not show the owner prefix %q", out, owner[:8])
	}

	// Bare `rd link` must NOT mutate the config — it only reports status.
	got, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if got.Board != coord {
		t.Errorf("Board = %q after bare `rd link`; want unchanged %q", got.Board, coord)
	}
}

// TestLinkCmd_BarePrintsNotLinked verifies bare `rd link` on an unlinked
// project reports the unlinked state and points at `rd follow`.
func TestLinkCmd_BarePrintsNotLinked(t *testing.T) {
	projectDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	if err := rdconfig.SaveSyncConfig(projectDir, &rdconfig.SyncConfig{ProjectName: "unlinked"}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	cmd := nostrLinkCmd
	cmd.SetArgs(nil)
	t.Cleanup(func() {
		_ = cmd.Flags().Set("owner", "")
		_ = cmd.Flags().Set("board-d", "")
		_ = cmd.Flags().Set("force", "false")
	})

	origStdout := os.Stdout
	r, w, perr := os.Pipe()
	if perr != nil {
		t.Fatalf("os.Pipe: %v", perr)
	}
	os.Stdout = w
	runErr := cmd.RunE(cmd, nil)
	w.Close()
	os.Stdout = origStdout

	var b strings.Builder
	buf := make([]byte, 4096)
	for {
		n, readErr := r.Read(buf)
		if n > 0 {
			b.Write(buf[:n])
		}
		if readErr != nil {
			break
		}
	}
	r.Close()

	if runErr != nil {
		t.Fatalf("bare `rd link` on unlinked project RunE error: %v", runErr)
	}
	out := b.String()
	if !strings.Contains(out, "not linked") {
		t.Errorf("bare `rd link` on unlinked project output %q does not say 'not linked'", out)
	}
	if !strings.Contains(out, "rd follow") {
		t.Errorf("bare `rd link` on unlinked project output %q does not point at 'rd follow'", out)
	}
}

// TestPinBoardCmd_RoutesToSameLinkBehavior verifies the hidden `rd pin-board`
// alias still performs a real link (same behavior it always had) — it is
// deprecated, not disabled.
func TestPinBoardCmd_RoutesToSameLinkBehavior(t *testing.T) {
	projectDir := t.TempDir()
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	if err := rdconfig.SaveSyncConfig(projectDir, &rdconfig.SyncConfig{}); err != nil {
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
	if err := cmd.Flags().Set("board-d", "aliastest"); err != nil {
		t.Fatalf("set board-d flag: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Flags().Set("owner", "")
		_ = cmd.Flags().Set("board-d", "")
		_ = cmd.Flags().Set("force", "false")
	})

	if err := cmd.RunE(cmd, nil); err != nil {
		t.Fatalf("hidden `rd pin-board` alias RunE: %v", err)
	}

	got, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	wantCoord := "30301:" + owner + ":aliastest"
	if got.Board != wantCoord {
		t.Errorf("Board = %q after `rd pin-board` alias; want %q", got.Board, wantCoord)
	}
}
