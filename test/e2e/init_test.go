package e2e_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ready-6ef cutover: the default `rd init` is now NOSTR-NATIVE — it pins a board
// coordinate and writes a signed nostr log, provisioning NO .campfire/ and NO .cf/.
// The old campfire-creating init (declarations, beacons, .campfire/root, --confirm)
// is preserved UNINVOKED as initCampfire (I7 deletes it). These tests assert the
// post-cutover default-path behaviour. The campfire-org REGISTER surface is
// campfire-vestigial (register requires a .campfire/root and has no nostr
// equivalent), so its tests build a campfire-backed project directly via the
// harness helper instead of via `rd init`.

// TestE2E_Init_NostrNative_DefaultsNameFromDirectory verifies the project name is
// inferred from the cwd and pinned in .ready/config.json (nostr-native default).
func TestE2E_Init_NostrNative_DefaultsNameFromDirectory(t *testing.T) {
	base := t.TempDir()
	projectDir := filepath.Join(base, "my-project")
	if err := os.MkdirAll(projectDir, 0755); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	rdHome := t.TempDir()
	cfHome := filepath.Join(t.TempDir(), "cf") // must NOT be created by the default path

	stdout, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--json")
	if code != 0 {
		t.Fatalf("rd init failed (exit %d):\nstderr: %s", code, stderr)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("JSON parse: %v\noutput: %s", err, stdout)
	}
	if result["name"] != "my-project" {
		t.Errorf("name: got %v, want my-project", result["name"])
	}
	if _, hasCampfire := result["campfire_id"]; hasCampfire {
		t.Errorf("nostr-native init must not emit campfire_id; got %v", result["campfire_id"])
	}

	// config.json pins the project name and a board coordinate.
	cfgData, err := os.ReadFile(filepath.Join(projectDir, ".ready", "config.json"))
	if err != nil {
		t.Fatalf("reading .ready/config.json: %v", err)
	}
	var cfg struct {
		Board       string `json:"board"`
		ProjectName string `json:"project_name"`
	}
	if err := json.Unmarshal(cfgData, &cfg); err != nil {
		t.Fatalf("parsing config.json: %v", err)
	}
	if cfg.ProjectName != "my-project" {
		t.Errorf("config project_name: got %q, want my-project", cfg.ProjectName)
	}
	if !strings.HasPrefix(cfg.Board, "30301:") {
		t.Errorf("config board: got %q, want a 30301:<owner>:<boardD> coordinate", cfg.Board)
	}
	if _, err := os.Stat(cfHome); err == nil {
		t.Errorf("nostr-native init must NOT provision the campfire home %q", cfHome)
	}
}

// TestE2E_Init_NostrNative_FailsIfAlreadyInitialized verifies a second init in an
// already-initialized dir is rejected.
func TestE2E_Init_NostrNative_FailsIfAlreadyInitialized(t *testing.T) {
	projectDir := t.TempDir()
	home := t.TempDir()
	rdHome := t.TempDir()
	cfHome := filepath.Join(t.TempDir(), "cf")

	if _, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--name", "test"); code != 0 {
		t.Fatalf("first init failed: %s", stderr)
	}
	_, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--name", "test")
	if code == 0 {
		t.Fatal("expected rd init to fail when already initialized")
	}
	if !strings.Contains(stderr, "already") {
		t.Errorf("expected 'already' in error, got: %q", stderr)
	}
}

// TestE2E_Init_NostrNative_ThenCreateClaimClose is the full default-path lifecycle
// (ready-6ef DONE#2 at the e2e layer): a fresh nostr-native `rd init`, then
// create → show → claim → close → list, all resolving through the nostr projection,
// with NO .cf/identity.json ever created anywhere.
func TestE2E_Init_NostrNative_ThenCreateClaimClose(t *testing.T) {
	projectDir := t.TempDir()
	home := t.TempDir()
	rdHome := t.TempDir()
	cfHome := filepath.Join(t.TempDir(), "cf")

	if _, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--name", "life"); code != 0 {
		t.Fatalf("rd init failed (exit %d): %s", code, stderr)
	}

	// create
	stdout, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome,
		"create", "--title", "First item", "--priority", "p1", "--type", "task", "--json")
	if code != 0 {
		t.Fatalf("rd create failed (exit %d): %s", code, stderr)
	}
	var item map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &item); err != nil {
		t.Fatalf("create JSON parse: %v\n%s", err, stdout)
	}
	id, _ := item["id"].(string)
	if id == "" {
		t.Fatal("created item has empty ID")
	}

	// show resolves it from the nostr projection
	showOut, showErr, showCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "show", id, "--json")
	if showCode != 0 {
		t.Fatalf("rd show failed (exit %d): %s", showCode, showErr)
	}
	var shown map[string]interface{}
	if err := json.Unmarshal([]byte(showOut), &shown); err != nil {
		t.Fatalf("show JSON parse: %v\n%s", err, showOut)
	}
	if shown["title"] != "First item" {
		t.Errorf("show title: got %v, want First item", shown["title"])
	}

	// claim, then close
	if _, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "claim", id); code != 0 {
		t.Fatalf("rd claim failed (exit %d): %s", code, stderr)
	}
	if _, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "close", id, "--reason", "done"); code != 0 {
		t.Fatalf("rd close failed (exit %d): %s", code, stderr)
	}

	// list --all shows the item as terminal
	listOut, listErr, listCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "list", "--all", "--json")
	if listCode != 0 {
		t.Fatalf("rd list failed (exit %d): %s", listCode, listErr)
	}
	var items []map[string]interface{}
	if err := json.Unmarshal([]byte(listOut), &items); err != nil {
		t.Fatalf("list JSON parse: %v\n%s", err, listOut)
	}
	var found map[string]interface{}
	for _, it := range items {
		if it["id"] == id {
			found = it
			break
		}
	}
	if found == nil {
		t.Fatalf("item %s not present in the nostr-projected list", id)
	}
	if found["status"] != "done" {
		t.Errorf("status: got %v, want done", found["status"])
	}

	// The default path must never provision a .cf identity or a .campfire pointer.
	if _, err := os.Stat(filepath.Join(cfHome, "identity.json")); err == nil {
		t.Errorf("default nostr path must NOT provision .cf/identity.json at %s", cfHome)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".campfire")); err == nil {
		t.Errorf("default nostr path must NOT create .campfire/ in the project dir")
	}
}
