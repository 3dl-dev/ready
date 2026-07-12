package e2e_test

// Tests for cf 0.14 center adoption — campfire-agent-center-adoption.
//
// TestE2E_CenterAdoption verifies that rd init works correctly when a center
// campfire is present in the walk-up path. In non-interactive contexts (CI,
// automated tests), the authorize hook fires but returns false — the command
// must succeed regardless.
//
// TestE2E_CenterAdoption_SubsequentDirNoBlock verifies that a second rd init
// in a different project dir (same cfHome) also succeeds when a center is present.

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// setupCenterCampfireForTest creates a center campfire in a parent directory:
//   - parentDir/.campfire/center  (sentinel file containing campfire ID)
//   - cfHome is a subdirectory of parentDir
//
// The caller passes a pre-initialized cfHome (cf init already run there).
// Returns the center campfire ID.
func setupCenterCampfireForTest(t *testing.T, cfHome string) string {
	t.Helper()

	// Create the center campfire using the same identity.
	createCmd := exec.Command("cf", "create",
		"--cf-home", cfHome,
		"--description", "test-center",
		"--json",
	)
	createCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	out, err := createCmd.Output()
	if err != nil {
		t.Fatalf("cf create (center): %v\n%s", err, out)
	}

	// cf 0.16+ prints "Wrote <path>" before the JSON object; find the first '{'.
	jsonStart := bytes.IndexByte(out, '{')
	if jsonStart < 0 {
		t.Fatalf("cf create: no JSON object in output: %s", out)
	}
	var result struct {
		CampfireID string `json:"campfire_id"`
	}
	if err := json.Unmarshal(out[jsonStart:], &result); err != nil {
		t.Fatalf("cf create JSON parse: %v\noutput: %s", err, out)
	}
	if result.CampfireID == "" {
		t.Fatalf("cf create returned empty campfire_id")
	}

	// Write center sentinel to parentDir/.campfire/center so walk-up finds it.
	// walkUpForCenter starts from cfHome and walks UP, so the sentinel must be
	// in a parent directory's .campfire/center.
	parentDir := filepath.Dir(cfHome)
	campfireDir := filepath.Join(parentDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0755); err != nil {
		t.Fatalf("mkdir parent .campfire: %v", err)
	}
	sentinelPath := filepath.Join(campfireDir, "center")
	if err := os.WriteFile(sentinelPath, []byte(result.CampfireID), 0644); err != nil {
		t.Fatalf("write center sentinel: %v", err)
	}

	return result.CampfireID
}

// TestE2E_CenterAdoption verifies (ready-6ef SURVIVE — campfire-org infra; I7 deletes):
//  1. A campfire command succeeds with a center campfire present in the walk-up path
//  2. The authorize hook (WithAuthorizeFunc(centerAuthorize), wired in requireClient)
//     fires (non-interactively returns false — no crash) and does not block the command
//  3. The project is fully functional (create/ready/done lifecycle)
//
// Pre-cutover this drove the campfire-native `rd init --confirm`; the default `rd init`
// is now nostr-native and provisions NO campfire, so the campfire substrate is built
// directly via newCampfireProjectDir. The center-adoption hook is NOT init-specific — it
// is armed on every campfire client init (requireClient → protocol.Init), so a plain
// `rd create` on a campfire-backed project exercises exactly the same surviving code
// path with the center sentinel in the walk-up.
func TestE2E_CenterAdoption(t *testing.T) {
	// Create a structured directory hierarchy so walk-up finds the center:
	//   parentDir/
	//     .campfire/center  ← sentinel written here
	//     cf/               ← cfHome (identity + store)
	parentDir := t.TempDir()
	cfHome := filepath.Join(parentDir, "cf")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("mkdir cfHome: %v", err)
	}

	// Initialize identity in cfHome.
	initCmd := exec.Command("cf", "init", "--cf-home", cfHome)
	initCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("cf init: %v\n%s", err, out)
	}

	// Create center campfire and write sentinel.
	_ = setupCenterCampfireForTest(t, cfHome)

	// Verify sentinel exists where walk-up will find it.
	sentinelPath := filepath.Join(parentDir, ".campfire", "center")
	if _, err := os.Stat(sentinelPath); err != nil {
		t.Fatalf("center sentinel not found at %s: %v", sentinelPath, err)
	}

	// Build the campfire-backed project directly (rd init no longer creates campfires).
	e := &Env{CFHome: cfHome, t: t}
	projectDir, _ := e.newCampfireProjectDir(t)
	e.ProjectDir = projectDir

	// A campfire mutation runs requireClient → protocol.Init(WithAuthorizeFunc), so the
	// center-adoption authorize hook fires here (stdin is not a tty → returns false) and
	// must not block. Full lifecycle verification: create → ready → done.
	var item Item
	if err := e.RdJSON(&item, "create",
		"--title", "center adoption test item",
		"--priority", "p1",
		"--type", "task",
	); err != nil {
		t.Fatalf("rd create: %v", err)
	}
	if item.ID == "" {
		t.Fatal("created item has empty ID")
	}

	readyItems := e.ReadyItems()
	if !containsItem(readyItems, item.ID) {
		t.Fatalf("item %s not found in rd ready", item.ID)
	}

	_, doneStderr, doneCode := e.Rd("done", item.ID, "--reason", "center adoption test complete")
	if doneCode != 0 {
		t.Fatalf("rd done: exit %d: %s", doneCode, doneStderr)
	}

	got := e.ShowItem(item.ID)
	if got.Status != "done" {
		t.Errorf("status after done: got %q, want done", got.Status)
	}
}

// TestE2E_CenterAdoption_SubsequentDirNoBlock verifies that a second rd init
// in a different project dir (same cfHome, same center present) also succeeds.
// The hook returning false on the first init does NOT prevent the second from
// running — both succeed without interaction.
func TestE2E_CenterAdoption_SubsequentDirNoBlock(t *testing.T) {
	parentDir := t.TempDir()
	cfHome := filepath.Join(parentDir, "cf")
	if err := os.MkdirAll(cfHome, 0755); err != nil {
		t.Fatalf("mkdir cfHome: %v", err)
	}

	initCmd := exec.Command("cf", "init", "--cf-home", cfHome)
	initCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("cf init: %v\n%s", err, out)
	}

	_ = setupCenterCampfireForTest(t, cfHome)

	// Build two campfire-backed projects sharing the same cfHome (rd init no longer
	// creates campfires). The center sentinel sits in the shared walk-up path, so a
	// campfire mutation in EITHER project arms the authorize hook — the first firing
	// (returning false) must not block the second project's commands.
	eA := &Env{CFHome: cfHome, t: t}
	eB := &Env{CFHome: cfHome, t: t}
	projA, _ := eA.newCampfireProjectDir(t)
	projB, _ := eB.newCampfireProjectDir(t)
	eA.ProjectDir = projA
	eB.ProjectDir = projB

	// Both projects must be functional.
	var itemA, itemB Item
	if err := eA.RdJSON(&itemA, "create", "--title", "item in A", "--priority", "p2", "--type", "task"); err != nil {
		t.Fatalf("create in A: %v", err)
	}
	if err := eB.RdJSON(&itemB, "create", "--title", "item in B", "--priority", "p2", "--type", "task"); err != nil {
		t.Fatalf("create in B: %v", err)
	}

	// Verify campfire isolation: each project only sees its own item.
	aItems := eA.ListItems()
	bItems := eB.ListItems()

	if !containsItem(aItems, itemA.ID) {
		t.Errorf("itemA %s not found in proj-a list", itemA.ID)
	}
	if containsItem(aItems, itemB.ID) {
		t.Errorf("itemB %s should not appear in proj-a list", itemB.ID)
	}
	if !containsItem(bItems, itemB.ID) {
		t.Errorf("itemB %s not found in proj-b list", itemB.ID)
	}
	if containsItem(bItems, itemA.ID) {
		t.Errorf("itemA %s should not appear in proj-b list", itemA.ID)
	}

	// Verify .campfire/root IDs differ (separate project campfires).
	rootA := strings.TrimSpace(readFileOrFatal(t, filepath.Join(projA, ".campfire", "root")))
	rootB := strings.TrimSpace(readFileOrFatal(t, filepath.Join(projB, ".campfire", "root")))
	if rootA == rootB {
		t.Errorf("proj-a and proj-b have the same campfire ID %s — should be different", rootA[:8])
	}
}

// readFileOrFatal reads a file and fatals the test on error.
func readFileOrFatal(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(data)
}
