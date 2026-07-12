package e2e_test

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// TestE2E_Upgrade_ReadsLegacyFlatLayoutStore proves the upgrade story: when the
// new rd (campfire v0.32.0, bucketed message layout) is pointed at a store whose
// messages are in the pre-0.31 FLAT layout (messages/*.cbor rather than
// messages/<YYYY-MM>/<DD>/*.cbor), it still reads them — so an existing campfire's
// items survive the drop-in with no migration step.
//
// The owner's message store is flattened to the legacy layout BEFORE a fresh
// member joins, so the member's empty store must read the flat-layout messages
// over the transport (a cache cannot mask the dual-read).
func TestE2E_Upgrade_ReadsLegacyFlatLayoutStore(t *testing.T) {
	ownerCFHome := t.TempDir()
	memberCFHome := t.TempDir()
	ownerProjectDir := t.TempDir()
	memberProjectDir := t.TempDir()

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

	rdInDir := func(t *testing.T, dir string, env []string, args ...string) (stdout, stderr string, code int) {
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

	// Identities.
	runCmd(t, envFor(ownerCFHome), "cf init (owner)", "cf", "init", "--cf-home", ownerCFHome)
	runCmd(t, envFor(memberCFHome), "cf init (member)", "cf", "init", "--cf-home", memberCFHome)
	ownerEnv := envFor(ownerCFHome)

	// Owner: init project + create two items (written to the bucketed layout).
	if _, stderr, code := rdInDir(t, ownerProjectDir, ownerEnv, "init", "--name", "upgrade-test", "--campfire"); code != 0 {
		t.Fatalf("rd init (owner) failed (exit %d): %s", code, stderr)
	}
	campfireID := readCampfireRoot(t, ownerProjectDir)

	var item1, item2 Item
	mustCreate(t, rdInDir, ownerProjectDir, ownerEnv, "pre-upgrade alpha", &item1)
	mustCreate(t, rdInDir, ownerProjectDir, ownerEnv, "pre-upgrade beta", &item2)

	// Simulate a pre-0.31 store: flatten the owner's bucketed messages to the
	// legacy flat layout (messages/*.cbor).
	moved := flattenMessageStore(t, filepath.Join(ownerCFHome, "campfires", campfireID))
	if moved == 0 {
		t.Fatalf("flattenMessageStore moved 0 files — expected bucketed messages to flatten")
	}

	// Owner admits the member.
	memberPubKey := memberPubKeyHex(t, memberCFHome)
	if _, stderr, code := rdInDir(t, ownerProjectDir, ownerEnv, "admit", memberPubKey); code != 0 {
		t.Fatalf("rd admit (owner) failed (exit %d): %s", code, stderr)
	}

	// Member joins and lists — a fresh store reading the owner's FLAT-layout
	// transport. The items must come through (dual-read).
	memberEnv := envFor(memberCFHome)
	if _, stderr, code := rdInDir(t, memberProjectDir, memberEnv, "join", campfireID); code != 0 {
		t.Fatalf("rd join (member) failed (exit %d): %s", code, stderr)
	}
	listOut, listStderr, listCode := rdInDir(t, memberProjectDir, memberEnv, "list", "--all", "--json")
	if listCode != 0 {
		t.Fatalf("rd list (member) failed (exit %d): %s", listCode, listStderr)
	}
	var items []Item
	if err := json.Unmarshal([]byte(listOut), &items); err != nil {
		t.Fatalf("parse list JSON: %v\noutput: %s", err, listOut)
	}
	if !containsItem(items, item1.ID) || !containsItem(items, item2.ID) {
		t.Errorf("legacy flat-layout items not read after upgrade: have %d items, want %s and %s",
			len(items), item1.ID, item2.ID)
		for _, it := range items {
			t.Logf("  - %s: %s", it.ID, it.Title)
		}
	}
}

// flattenMessageStore moves every bucketed message file
// (messages/<YYYY-MM>/<DD>/*.cbor) up to the flat messages/ directory and removes
// the bucket directories, reproducing the pre-0.31 on-disk layout. Returns the
// number of message files moved.
func flattenMessageStore(t *testing.T, campfireDir string) int {
	t.Helper()
	msgDir := filepath.Join(campfireDir, "messages")
	monthDirs, err := os.ReadDir(msgDir)
	if err != nil {
		t.Fatalf("reading message dir %s: %v", msgDir, err)
	}
	moved := 0
	for _, ym := range monthDirs {
		if !ym.IsDir() {
			continue // already-flat file
		}
		ymPath := filepath.Join(msgDir, ym.Name())
		dayDirs, err := os.ReadDir(ymPath)
		if err != nil {
			continue
		}
		for _, d := range dayDirs {
			if !d.IsDir() {
				continue
			}
			dayPath := filepath.Join(ymPath, d.Name())
			files, err := os.ReadDir(dayPath)
			if err != nil {
				continue
			}
			for _, f := range files {
				if filepath.Ext(f.Name()) != ".cbor" {
					continue
				}
				if err := os.Rename(filepath.Join(dayPath, f.Name()), filepath.Join(msgDir, f.Name())); err != nil {
					t.Fatalf("flattening %s: %v", f.Name(), err)
				}
				moved++
			}
		}
		if err := os.RemoveAll(ymPath); err != nil {
			t.Fatalf("removing bucket dir %s: %v", ymPath, err)
		}
	}
	return moved
}

// readCampfireRoot reads and trims the campfire ID from a project's .campfire/root.
func readCampfireRoot(t *testing.T, projectDir string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(projectDir, ".campfire", "root"))
	if err != nil {
		t.Fatalf("reading .campfire/root: %v", err)
	}
	id := string(bytes.TrimSpace(b))
	if len(id) != 64 {
		t.Fatalf("campfire ID wrong length %d: %q", len(id), id)
	}
	return id
}

// mustCreate runs `rd create --json` and decodes the created item.
func mustCreate(t *testing.T, rdInDir func(*testing.T, string, []string, ...string) (string, string, int), dir string, env []string, title string, out *Item) {
	t.Helper()
	stdout, stderr, code := rdInDir(t, dir, env, "create", "--title", title, "--priority", "p2", "--type", "task", "--json")
	if code != 0 {
		t.Fatalf("rd create %q failed (exit %d): %s", title, code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), out); err != nil {
		t.Fatalf("parse create JSON for %q: %v\noutput: %s", title, err, stdout)
	}
}
