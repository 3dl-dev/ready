package e2e_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestE2E_JoinAdmit_TwoSidedHandshake is the regression test for the two-sided
// join/admit handshake (ready-43e).
//
// Done condition (from item):
//   - Two isolated identities (owner CF_HOME, member CF_HOME) complete the full
//     join handshake: owner calls 'rd admit <member-pubkey>', member calls
//     'rd join <campfire-id>', member calls 'rd create "test item"', and item
//     appears in owner's 'rd list' output. Exit code 0 for all commands.
//
// No mocks. Two real CF_HOME dirs, two real campfire clients, real filesystem transport.
//
// Root cause fixed: 'rd join' previously used localCampfireBaseDir() (own CF_HOME)
// as the transport directory. When member and owner have different CF_HOMEs, the
// member's transport dir is empty — there is no campfire state there. The fix
// resolves the transport dir from the beacon published during 'rd init', which
// carries the owner's transport dir in Transport.Config["dir"].
func TestE2E_JoinAdmit_TwoSidedHandshake(t *testing.T) {
	// --- Setup: two isolated identities ---
	//
	// ready-6ef SURVIVE (campfire-org infra; I7 deletes this test with the campfire
	// code): the two-sided rd admit / rd join handshake is a campfire feature with no
	// nostr-native equivalent. The default `rd init` is now nostr-native, so the owner's
	// project campfire — and the beacon carrying its transport dir to the member, plus
	// the .ready/config.json campfire_id `rd admit` reads — are provisioned via the
	// hidden, explicit-only `rd init --campfire` legacy path (which runs the still-present
	// campfire-init code faithfully, without faking any of its outputs).

	ownerCFHome := t.TempDir()
	memberCFHome := t.TempDir()
	ownerProjectDir := t.TempDir()

	// Shared HOME so both identities share the same beacon directory
	// (~/.campfire/beacons/). This is the mechanism by which the member finds
	// the owner's transport dir during 'rd join'.
	sharedHome := os.Getenv("HOME")

	envFor := func(cfHome string) []string {
		return []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + sharedHome,
			"CF_HOME=" + cfHome,
		}
	}

	// cf init for owner identity.
	runCmd := func(t *testing.T, env []string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", name, err, out)
		}
	}

	runCmd(t, envFor(ownerCFHome), "cf init (owner)", "cf", "init", "--cf-home", ownerCFHome)
	runCmd(t, envFor(memberCFHome), "cf init (member)", "cf", "init", "--cf-home", memberCFHome)

	// Owner: rd init --campfire — creates project campfire, publishes beacon (transport
	// dir carried in Transport.Config["dir"]), and writes .ready/config.json campfire_id.
	ownerEnv := envFor(ownerCFHome)
	ownerRd := func(args ...string) (stdout, stderr string, code int) {
		t.Helper()
		cmd := exec.Command(rdBinary, args...)
		cmd.Dir = ownerProjectDir
		cmd.Env = ownerEnv
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		_ = cmd.Run()
		code = 0
		if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
			code = cmd.ProcessState.ExitCode()
		}
		return outBuf.String(), errBuf.String(), code
	}

	_, initStderr, initCode := ownerRd("init", "--name", "handshake-test", "--campfire")
	if initCode != 0 {
		t.Fatalf("rd init --campfire (owner) failed (exit %d): %s", initCode, initStderr)
	}

	// Read campfire ID from .campfire/root.
	campfireIDBytes, err := os.ReadFile(filepath.Join(ownerProjectDir, ".campfire", "root"))
	if err != nil {
		t.Fatalf("reading .campfire/root: %v", err)
	}
	campfireID := string(campfireIDBytes)
	for len(campfireID) > 0 && (campfireID[len(campfireID)-1] == '\n' || campfireID[len(campfireID)-1] == '\r') {
		campfireID = campfireID[:len(campfireID)-1]
	}
	if len(campfireID) != 64 {
		t.Fatalf("campfire ID has wrong length %d: %q", len(campfireID), campfireID)
	}

	// Read member identity public key from their identity.json.
	memberPubKey := memberPubKeyHex(t, memberCFHome)

	// Owner: rd admit <member-pubkey> — writes pre-admission record to transport dir.
	_, admitStderr, admitCode := ownerRd("admit", memberPubKey)
	if admitCode != 0 {
		t.Fatalf("rd admit (owner) failed (exit %d): %s", admitCode, admitStderr)
	}

	// Member: rd join <campfire-id> — resolves transport dir from beacon, completes join.
	// This is the fix under test: resolveTransportDir() finds the beacon published by
	// 'rd init' and returns the owner's transport dir, not the member's empty dir.
	memberEnv := envFor(memberCFHome)
	memberRd := func(args ...string) (stdout, stderr string, code int) {
		t.Helper()
		cmd := exec.Command(rdBinary, args...)
		cmd.Dir = ownerProjectDir
		cmd.Env = memberEnv
		var outBuf, errBuf bytes.Buffer
		cmd.Stdout = &outBuf
		cmd.Stderr = &errBuf
		_ = cmd.Run()
		code = 0
		if cmd.ProcessState != nil && !cmd.ProcessState.Success() {
			code = cmd.ProcessState.ExitCode()
		}
		return outBuf.String(), errBuf.String(), code
	}

	_, joinStderr, joinCode := memberRd("join", campfireID)
	if joinCode != 0 {
		t.Fatalf("rd join (member) failed (exit %d): %s", joinCode, joinStderr)
	}

	// Member: rd create "test item" — member creates an item in the shared campfire.
	createOut, createStderr, createCode := memberRd("create", "--title", "test item from member", "--priority", "p1", "--type", "task", "--json")
	if createCode != 0 {
		t.Fatalf("rd create (member) failed (exit %d): %s", createCode, createStderr)
	}

	var createdItem Item
	if err := json.Unmarshal([]byte(createOut), &createdItem); err != nil {
		t.Fatalf("parse created item JSON: %v\noutput: %s", err, createOut)
	}
	if createdItem.ID == "" {
		t.Fatal("rd create returned empty ID")
	}

	// Owner: rd list — the item created by the member must appear.
	listOut, listStderr, listCode := ownerRd("list", "--all", "--json")
	if listCode != 0 {
		t.Fatalf("rd list (owner) failed (exit %d): %s", listCode, listStderr)
	}

	var items []Item
	if err := json.Unmarshal([]byte(listOut), &items); err != nil {
		t.Fatalf("parse list JSON: %v\noutput: %s", err, listOut)
	}

	if !containsItem(items, createdItem.ID) {
		t.Errorf("item %s created by member not found in owner's rd list (two-sided handshake failure)", createdItem.ID)
		t.Logf("items in owner's list: %d", len(items))
		for _, item := range items {
			t.Logf("  - %s: %s", item.ID, item.Title)
		}
	}
}

// memberPubKeyHex reads the public key from a cf identity.json file and returns
// the hex-encoded public key.
func memberPubKeyHex(t *testing.T, cfHome string) string {
	t.Helper()

	identityPath := filepath.Join(cfHome, "identity.json")
	data, err := os.ReadFile(identityPath)
	if err != nil {
		t.Fatalf("reading member identity.json at %s: %v", identityPath, err)
	}

	var id struct {
		PublicKey []byte `json:"public_key"`
	}
	if err := json.Unmarshal(data, &id); err != nil {
		t.Fatalf("parsing member identity.json: %v", err)
	}

	return formatHex(id.PublicKey)
}

// formatHex formats a byte slice as a lowercase hex string.
func formatHex(b []byte) string {
	const hexChars = "0123456789abcdef"
	result := make([]byte, len(b)*2)
	for i, v := range b {
		result[i*2] = hexChars[v>>4]
		result[i*2+1] = hexChars[v&0xf]
	}
	return string(result)
}
