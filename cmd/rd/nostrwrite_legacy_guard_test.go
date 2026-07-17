package main

import (
	"os"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/rdconfig"
)

// ready-eaa: a write command on a legacy (campfire-era) .ready/config.json with
// no pinned nostr board must NOT tell the user to 'rd init' — that mints a
// COMPETING board under a fresh key, orphaning the project's real history
// (edge #7 in ready-615). It must instead point at 'rd pin-board' / 'rd follow
// <coord>' to pin or adopt the authoritative board this project already belongs
// to. A genuinely-uninitialized directory (no .ready/ at all) must still get the
// real 'rd init' guidance.

// TestClaim_LegacyProjectWithoutPinnedBoard_GetsPinFollowGuidance verifies the
// WRITE-path error on a legacy .ready/config.json (campfire-shaped: CampfireID +
// ProjectName set, no Board coordinate) explains the project predates the nostr
// backend and points at pin-board/follow, and does NOT suggest 'rd init'.
func TestClaim_LegacyProjectWithoutPinnedBoard_GetsPinFollowGuidance(t *testing.T) {
	projectDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// Legacy campfire-era config: a .ready/ directory exists with a config.json
	// that carries campfire identity fields but no Board coordinate — exactly the
	// on-disk shape a pre-nostr-cutover project left behind. No .campfire/root
	// marker required; nostrPinnedBoard only cares about .ready/config.json.
	legacy := &rdconfig.SyncConfig{
		CampfireID:  strings.Repeat("ab", 32),
		ProjectName: "legacyproj",
	}
	if err := rdconfig.SaveSyncConfig(projectDir, legacy); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	cmd := claimCmd
	cmd.SetArgs(nil)
	if err := cmd.Flags().Set("reason", ""); err != nil {
		t.Fatalf("set reason flag: %v", err)
	}

	err = cmd.RunE(cmd, []string{"ready-anything"})
	if err == nil {
		t.Fatal("claim on a legacy unpinned .ready project succeeded; want a write-guidance error")
	}

	msg := err.Error()
	if strings.Contains(msg, "rd init") {
		t.Errorf("legacy-project error %q suggests 'rd init' — this mints a competing board (edge #7)", msg)
	}
	// ready-8ff: the binding command is 'rd link' now, not 'rd pin-board'.
	if !strings.Contains(msg, "rd link") {
		t.Errorf("legacy-project error %q does not mention 'rd link'", msg)
	}
	if strings.Contains(msg, "pin-board") {
		t.Errorf("legacy-project error %q still mentions the deprecated 'rd pin-board'", msg)
	}
	if !strings.Contains(msg, "follow") {
		t.Errorf("legacy-project error %q does not mention 'rd follow'", msg)
	}
	if !strings.Contains(msg, "predates the nostr backend") {
		t.Errorf("legacy-project error %q does not explain the project predates the nostr backend", msg)
	}
}

// TestClaim_GenuinelyUninitializedDir_StillGetsInitGuidance verifies a directory
// with no .ready/ project at all (never initialized, no campfire history to
// orphan) still gets the real 'rd init' guidance — the fix must not blanket-
// suppress init guidance for every write failure, only the legacy-project case.
func TestClaim_GenuinelyUninitializedDir_StillGetsInitGuidance(t *testing.T) {
	projectDir := t.TempDir()

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// No .ready/, no .campfire/root — genuinely uninitialized directory.

	cmd := claimCmd
	cmd.SetArgs(nil)
	if err := cmd.Flags().Set("reason", ""); err != nil {
		t.Fatalf("set reason flag: %v", err)
	}

	err = cmd.RunE(cmd, []string{"ready-anything"})
	if err == nil {
		t.Fatal("claim in an uninitialized directory succeeded; want the not-a-project error")
	}

	msg := err.Error()
	if !strings.Contains(msg, "rd init") {
		t.Errorf("uninitialized-dir error %q does not suggest 'rd init'", msg)
	}
	if strings.Contains(msg, "pin-board") || strings.Contains(msg, "rd link") || strings.Contains(msg, "follow") {
		t.Errorf("uninitialized-dir error %q wrongly suggests link/pin-board/follow — nothing to link/adopt yet", msg)
	}
}
