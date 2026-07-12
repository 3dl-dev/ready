package e2e_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestE2E_JoinAutoSyncPull verifies that rd join pulls existing items automatically
// so that rd list shows them without the user running rd sync pull (ready-5cd).
//
// Done condition: after member runs rd join, rd list --all --json shows both
// pre-existing items created by the owner WITHOUT any rd sync pull command.
//
// Uses real campfire filesystem transport. No mocks.
func TestE2E_JoinAutoSyncPull(t *testing.T) {
	// Two isolated CF_HOME dirs — owner and member.
	ownerCFHome := t.TempDir()
	memberCFHome := t.TempDir()
	ownerProjectDir := t.TempDir()
	memberProjectDir := t.TempDir()

	// Shared HOME and a shared beacon directory so the two isolated CF_HOME
	// identities can discover each other's campfire. As of cf 0.30+,
	// beacon.DefaultBeaconDir() is CF_HOME-scoped (isolated --cf-home
	// invocations no longer scan the global beacon dir), so simulating a
	// shared-filesystem multi-machine join requires an explicit shared
	// CF_BEACON_DIR — the same knob real separate machines on a shared mount use.
	sharedHome := os.Getenv("HOME")
	sharedBeaconDir := t.TempDir()

	envFor := func(cfHome string) []string {
		return []string{
			"PATH=" + os.Getenv("PATH"),
			"HOME=" + sharedHome,
			"CF_HOME=" + cfHome,
			"CF_BEACON_DIR=" + sharedBeaconDir,
		}
	}

	runCmd := func(t *testing.T, env []string, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(args[0], args[1:]...)
		cmd.Env = env
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s failed: %v\n%s", name, err, out)
		}
	}

	// Initialize both identities.
	runCmd(t, envFor(ownerCFHome), "cf init (owner)", "cf", "init", "--cf-home", ownerCFHome)
	runCmd(t, envFor(memberCFHome), "cf init (member)", "cf", "init", "--cf-home", memberCFHome)

	ownerEnv := envFor(ownerCFHome)

	rdInDir := func(dir string, env []string, args ...string) (stdout, stderr string, code int) {
		t.Helper()
		cmd := exec.Command(rdBinary, args...)
		cmd.Dir = dir
		cmd.Env = env
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

	// Owner: rd init — creates project campfire and publishes beacon.
	_, initStderr, initCode := rdInDir(ownerProjectDir, ownerEnv, "init", "--name", "auto-sync-test", "--campfire")
	if initCode != 0 {
		t.Fatalf("rd init (owner) failed (exit %d): %s", initCode, initStderr)
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
		t.Fatalf("campfire ID wrong length %d: %q", len(campfireID), campfireID)
	}

	// Owner: create 2 items BEFORE the member joins.
	var item1, item2 Item

	item1Out, item1Stderr, item1Code := rdInDir(ownerProjectDir, ownerEnv,
		"create", "--title", "pre-join item alpha", "--priority", "p1", "--type", "task", "--json",
	)
	if item1Code != 0 {
		t.Fatalf("rd create item 1 (owner) failed (exit %d): %s", item1Code, item1Stderr)
	}
	if err := json.Unmarshal([]byte(item1Out), &item1); err != nil {
		t.Fatalf("parse item 1 JSON: %v\noutput: %s", err, item1Out)
	}

	item2Out, item2Stderr, item2Code := rdInDir(ownerProjectDir, ownerEnv,
		"create", "--title", "pre-join item beta", "--priority", "p2", "--type", "task", "--json",
	)
	if item2Code != 0 {
		t.Fatalf("rd create item 2 (owner) failed (exit %d): %s", item2Code, item2Stderr)
	}
	if err := json.Unmarshal([]byte(item2Out), &item2); err != nil {
		t.Fatalf("parse item 2 JSON: %v\noutput: %s", err, item2Out)
	}

	// Owner: admit member so they can join.
	memberPubKey := memberPubKeyHex(t, memberCFHome)
	_, admitStderr, admitCode := rdInDir(ownerProjectDir, ownerEnv, "admit", memberPubKey)
	if admitCode != 0 {
		t.Fatalf("rd admit (owner) failed (exit %d): %s", admitCode, admitStderr)
	}

	// Member: rd join — auto-sync pull should fetch the 2 pre-existing items.
	memberEnv := envFor(memberCFHome)
	_, joinStderr, joinCode := rdInDir(memberProjectDir, memberEnv, "join", campfireID)
	if joinCode != 0 {
		t.Fatalf("rd join (member) failed (exit %d): %s", joinCode, joinStderr)
	}

	// Member: rd list --all --json — must show both pre-existing items
	// WITHOUT running rd sync pull. This is the done condition for ready-5cd.
	listOut, listStderr, listCode := rdInDir(memberProjectDir, memberEnv, "list", "--all", "--json")
	if listCode != 0 {
		t.Fatalf("rd list (member) failed (exit %d): %s", listCode, listStderr)
	}

	var items []Item
	if err := json.Unmarshal([]byte(listOut), &items); err != nil {
		t.Fatalf("parse list JSON: %v\noutput: %s", err, listOut)
	}

	if !containsItem(items, item1.ID) {
		t.Errorf("pre-join item %s (%q) not found in member's rd list after join (auto-sync failed)",
			item1.ID, item1.Title)
	}
	if !containsItem(items, item2.ID) {
		t.Errorf("pre-join item %s (%q) not found in member's rd list after join (auto-sync failed)",
			item2.ID, item2.Title)
	}
	if t.Failed() {
		t.Logf("rd join stderr: %s", joinStderr)
		t.Logf("items in member's list (%d):", len(items))
		for _, it := range items {
			t.Logf("  - %s: %s", it.ID, it.Title)
		}
	}
}
