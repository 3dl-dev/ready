package e2e_test

import (
	"bytes"
	"os/exec"
	"strings"
	"testing"
)

// TestBuildVerification_SubcommandsBuildFromSource verifies that the rd binary
// built from source contains the join and revoke subcommands. (admit and
// register were removed under the nostr-native cutover — kind-39301 grants
// replace campfire admission; see ready-9ac.)
// This test ensures CI produces a valid binary with all required capabilities.
func TestBuildVerification_SubcommandsBuildFromSource(t *testing.T) {
	// Verify join --help works
	cmd := exec.Command(rdBinary, "join", "--help")
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("join --help failed: %v\noutput: %s", err, out.String())
	}
	joinOutput := out.String()
	if !strings.Contains(joinOutput, "Join") && !strings.Contains(joinOutput, "Usage") {
		t.Errorf("join --help missing expected output")
	}

	// Verify revoke --help works
	cmd = exec.Command(rdBinary, "revoke", "--help")
	out.Reset()
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("revoke --help failed: %v\noutput: %s", err, out.String())
	}
	revokeOutput := out.String()
	if !strings.Contains(revokeOutput, "Revoke") && !strings.Contains(revokeOutput, "Usage") {
		t.Errorf("revoke --help missing expected output")
	}

	// Verify --help shows all three commands
	cmd = exec.Command(rdBinary, "--help")
	out.Reset()
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("rd --help failed: %v\noutput: %s", err, out.String())
	}
	helpOutput := out.String()

	requiredSubcommands := []string{"join", "revoke"}
	for _, subcmd := range requiredSubcommands {
		if !strings.Contains(helpOutput, subcmd) {
			t.Errorf("rd --help missing subcommand: %s", subcmd)
		}
	}
}
