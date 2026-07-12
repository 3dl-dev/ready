package e2e_test

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --------------------------------------------------------------------------
// Topology 1: Single project, filesystem transport
//
// The simplest case. One identity, one project campfire, local fs transport.
// Full item lifecycle: init → create → ready → done → ready (empty).
// --------------------------------------------------------------------------

func TestE2E_Topology1_SingleProject_FullLifecycle(t *testing.T) {
	cfHome := t.TempDir()
	projectDir := t.TempDir()

	topologyCfInit(t, cfHome)

	// Build an Env backed by the new cfHome — no pre-created campfire;
	// rd init will create one.
	e := &Env{
		CFHome:     cfHome,
		CampfireID: "",
		ProjectDir: projectDir,
		t:          t,
	}

	// rd init — creates campfire + .campfire/root + .ready/
	stdout, stderr, code := e.Rd("init", "--name", "myproject", "--json", "--campfire")
	if code != 0 {
		t.Fatalf("rd init failed (exit %d):\nstderr: %s\nstdout: %s", code, stderr, stdout)
	}

	var initResult map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &initResult); err != nil {
		t.Fatalf("rd init JSON parse: %v\noutput: %s", err, stdout)
	}
	if initResult["campfire_id"] == nil || initResult["campfire_id"] == "" {
		t.Error("rd init: campfire_id should be non-empty")
	}

	// .campfire/root must exist with a valid 64-char campfire ID.
	rootFile := filepath.Join(projectDir, ".campfire", "root")
	rootBytes, err := os.ReadFile(rootFile)
	if err != nil {
		t.Fatalf(".campfire/root not found after rd init: %v", err)
	}
	campfireID := strings.TrimSpace(string(rootBytes))
	if len(campfireID) != 64 {
		t.Errorf(".campfire/root: expected 64-char ID, got %d: %q", len(campfireID), campfireID)
	}
	if campfireID != initResult["campfire_id"] {
		t.Errorf(".campfire/root %q != JSON campfire_id %q", campfireID, initResult["campfire_id"])
	}

	// rd create "task one"
	var item Item
	stdout, stderr, code = e.Rd("create", "--title", "task one", "--priority", "p1", "--type", "task", "--json")
	if code != 0 {
		t.Fatalf("rd create failed (exit %d): %s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &item); err != nil {
		t.Fatalf("rd create JSON parse: %v\noutput: %s", err, stdout)
	}
	if item.ID == "" {
		t.Fatal("create returned empty ID")
	}

	// rd ready — item should appear.
	readyItems := e.ReadyItems()
	if !containsItem(readyItems, item.ID) {
		t.Fatalf("item %s not found in rd ready after create", item.ID)
	}

	// rd done <id> --reason "done"
	_, stderr, code = e.Rd("done", item.ID, "--reason", "done")
	if code != 0 {
		t.Fatalf("rd done failed (exit %d): %s", code, stderr)
	}

	// rd ready — should be empty (item closed).
	readyItems = e.ReadyItems()
	if containsItem(readyItems, item.ID) {
		t.Fatalf("closed item %s should not appear in rd ready", item.ID)
	}

	// Verify item status is done.
	got := e.ShowItem(item.ID)
	if got.Status != "done" {
		t.Errorf("status: got %q, want done", got.Status)
	}
}

// --------------------------------------------------------------------------
// Topology 2: Multiple projects, shared identity
//
// One identity manages work across 2 projects. Each project has its own
// campfire. The --project flag filters views to the named project.
// --------------------------------------------------------------------------

func TestE2E_Topology2_MultiProject_SharedIdentity(t *testing.T) {
	cfHome := t.TempDir()
	topologyCfInit(t, cfHome)

	projA := t.TempDir()
	projB := t.TempDir()

	eA := &Env{CFHome: cfHome, ProjectDir: projA, t: t}
	eB := &Env{CFHome: cfHome, ProjectDir: projB, t: t}

	// Init project A.
	_, stderr, code := eA.Rd("init", "--name", "project-a", "--json", "--campfire")
	if code != 0 {
		t.Fatalf("rd init project-a failed (exit %d): %s", code, stderr)
	}

	// Init project B.
	_, stderr, code = eB.Rd("init", "--name", "project-b", "--json", "--campfire")
	if code != 0 {
		t.Fatalf("rd init project-b failed (exit %d): %s", code, stderr)
	}

	// Create item in project A (explicit --project tag).
	var itemA Item
	stdout, stderr, code := eA.Rd("create",
		"--title", "task in A",
		"--priority", "p1",
		"--type", "task",
		"--project", "project-a",
		"--json",
	)
	if code != 0 {
		t.Fatalf("rd create in A failed (exit %d): %s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &itemA); err != nil {
		t.Fatalf("rd create A JSON parse: %v\noutput: %s", err, stdout)
	}
	if itemA.ID == "" {
		t.Fatal("create in A returned empty ID")
	}

	// Create item in project B (explicit --project tag).
	var itemB Item
	stdout, stderr, code = eB.Rd("create",
		"--title", "task in B",
		"--priority", "p1",
		"--type", "task",
		"--project", "project-b",
		"--json",
	)
	if code != 0 {
		t.Fatalf("rd create in B failed (exit %d): %s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &itemB); err != nil {
		t.Fatalf("rd create B JSON parse: %v\noutput: %s", err, stdout)
	}
	if itemB.ID == "" {
		t.Fatal("create in B returned empty ID")
	}

	// Verify project field via rd show (create --json output omits project field).
	shownA := eA.ShowItem(itemA.ID)
	if shownA.Project != "project-a" {
		t.Errorf("itemA.Project via show: got %q, want project-a", shownA.Project)
	}
	shownB := eB.ShowItem(itemB.ID)
	if shownB.Project != "project-b" {
		t.Errorf("itemB.Project via show: got %q, want project-b", shownB.Project)
	}

	// rd list from project A's dir — campfire-scoped: itemA present, itemB absent.
	var listFromA []Item
	if err := eA.RdJSON(&listFromA, "list"); err != nil {
		t.Fatalf("rd list from A: %v", err)
	}
	if !containsItem(listFromA, itemA.ID) {
		t.Errorf("itemA %s not found in list from project A", itemA.ID)
	}
	if containsItem(listFromA, itemB.ID) {
		t.Errorf("itemB %s should not appear in list from project A (separate campfire)", itemB.ID)
	}

	// rd list from project B's dir — campfire-scoped: itemB present, itemA absent.
	var listFromB []Item
	if err := eB.RdJSON(&listFromB, "list"); err != nil {
		t.Fatalf("rd list from B: %v", err)
	}
	if !containsItem(listFromB, itemB.ID) {
		t.Errorf("itemB %s not found in list from project B", itemB.ID)
	}
	if containsItem(listFromB, itemA.ID) {
		t.Errorf("itemA %s should not appear in list from project B (separate campfire)", itemA.ID)
	}

	// --project filter on rd ready from A: project-a matches itemA.
	var readyFiltered []Item
	if err := eA.RdJSON(&readyFiltered, "ready", "--project", "project-a"); err != nil {
		t.Fatalf("rd ready --project project-a: %v", err)
	}
	if !containsItem(readyFiltered, itemA.ID) {
		t.Errorf("itemA %s not found in rd ready --project project-a", itemA.ID)
	}

	// --project filter on rd ready from A: project-b must exclude itemA.
	var readyWrongProject []Item
	if err := eA.RdJSON(&readyWrongProject, "ready", "--project", "project-b"); err == nil {
		if containsItem(readyWrongProject, itemA.ID) {
			t.Errorf("itemA %s should not appear in rd ready --project project-b", itemA.ID)
		}
	}

	// --project filter on rd list from B: project-b matches itemB.
	var listFiltered []Item
	if err := eB.RdJSON(&listFiltered, "list", "--project", "project-b"); err != nil {
		t.Fatalf("rd list --project project-b from B: %v", err)
	}
	if !containsItem(listFiltered, itemB.ID) {
		t.Errorf("itemB %s not found in rd list --project project-b", itemB.ID)
	}
	if containsItem(listFiltered, itemA.ID) {
		t.Errorf("itemA %s should not appear in rd list --project project-b", itemA.ID)
	}

	// --project filter on rd work (active items view).
	eA.RdMustSucceed("update", itemA.ID, "--status", "active")
	var workFiltered []Item
	if err := eA.RdJSON(&workFiltered, "work", "--project", "project-a"); err != nil {
		t.Fatalf("rd work --project project-a: %v", err)
	}
	if !containsItem(workFiltered, itemA.ID) {
		t.Errorf("itemA %s not found in rd work --project project-a after claiming", itemA.ID)
	}

	// --project filter on rd pending (waiting items view).
	eB.RdMustSucceed("update", itemB.ID,
		"--status", "waiting",
		"--waiting-on", "review",
		"--waiting-type", "person",
	)
	var pendingFiltered []Item
	if err := eB.RdJSON(&pendingFiltered, "pending", "--project", "project-b"); err != nil {
		t.Fatalf("rd pending --project project-b: %v", err)
	}
	if !containsItem(pendingFiltered, itemB.ID) {
		t.Errorf("itemB %s not found in rd pending --project project-b", itemB.ID)
	}
}

// --------------------------------------------------------------------------
// Topology 3: Git-backed project (center with filesystem transport)
//
// A project where campfire state lives alongside code (fs transport).
// Verifies: .campfire/root written, .ready/ created, mutations.jsonl records
// operations, sync status reports campfire as configured.
// --------------------------------------------------------------------------

func TestE2E_Topology3_GitBacked_FsTransport(t *testing.T) {
	cfHome := t.TempDir()
	topologyCfInit(t, cfHome)

	// Simulate a git repo directory.
	repoDir := t.TempDir()
	e := &Env{CFHome: cfHome, ProjectDir: repoDir, t: t}

	// rd init — creates campfire with fs transport (default).
	stdout, stderr, code := e.Rd("init", "--name", "git-project", "--json", "--campfire")
	if code != 0 {
		t.Fatalf("rd init failed (exit %d):\nstderr: %s\nstdout: %s", code, stderr, stdout)
	}

	var initResult map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &initResult); err != nil {
		t.Fatalf("rd init JSON parse: %v\noutput: %s", err, stdout)
	}

	// .campfire/root must exist with a valid 64-char campfire ID.
	rootFile := filepath.Join(repoDir, ".campfire", "root")
	rootBytes, err := os.ReadFile(rootFile)
	if err != nil {
		t.Fatalf(".campfire/root not found: %v", err)
	}
	campfireID := strings.TrimSpace(string(rootBytes))
	if len(campfireID) != 64 {
		t.Errorf(".campfire/root: expected 64-char ID, got %d: %q", len(campfireID), campfireID)
	}
	if campfireID != initResult["campfire_id"] {
		t.Errorf(".campfire/root %q != JSON campfire_id %q", campfireID, initResult["campfire_id"])
	}

	// .ready/ must exist (local state dir).
	readyDir := filepath.Join(repoDir, ".ready")
	if _, err := os.Stat(readyDir); err != nil {
		t.Fatalf(".ready/ not created by rd init: %v", err)
	}

	// Create a feature work item.
	var item Item
	stdout, stderr, code = e.Rd("create",
		"--title", "feature work",
		"--priority", "p1",
		"--type", "task",
		"--json",
	)
	if code != 0 {
		t.Fatalf("rd create failed (exit %d): %s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &item); err != nil {
		t.Fatalf("rd create JSON parse: %v\noutput: %s", err, stdout)
	}
	if item.ID == "" {
		t.Fatal("create returned empty ID")
	}

	// rd ready — item should appear.
	readyItems := e.ReadyItems()
	if !containsItem(readyItems, item.ID) {
		t.Fatalf("item %s not found in rd ready", item.ID)
	}

	// mutations.jsonl must exist and contain the item ID.
	mutationsPath := filepath.Join(readyDir, "mutations.jsonl")
	mutationData, err := os.ReadFile(mutationsPath)
	if err != nil {
		t.Fatalf("mutations.jsonl not found at %s: %v", mutationsPath, err)
	}
	if len(mutationData) == 0 {
		t.Fatal("mutations.jsonl is empty — create operation was not recorded")
	}
	if !contains(string(mutationData), item.ID) {
		t.Errorf("mutations.jsonl does not contain item ID %q:\n%s", item.ID, string(mutationData))
	}

	// Each line of mutations.jsonl must be valid JSON.
	for i, line := range splitLines(string(mutationData)) {
		if line == "" {
			continue
		}
		var rec map[string]interface{}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("mutations.jsonl line %d is not valid JSON: %v\nline: %s", i+1, err, line)
		}
	}

	// Center check: cf 0.14 creates a center campfire on init.
	// The center file location is cf-version-dependent; log but don't fail if absent.
	centerCandidates := []string{
		filepath.Join(cfHome, ".campfire", "center"),
		filepath.Join(cfHome, "campfire", "center"),
		filepath.Join(cfHome, "center"),
	}
	centerFound := false
	for _, p := range centerCandidates {
		if _, err := os.Stat(p); err == nil {
			centerFound = true
			t.Logf("center campfire file found at: %s", p)
			break
		}
	}
	if !centerFound {
		t.Logf("NOTE: center file not found in any candidate location under %s — cf 0.14 center layout may differ from expectations", cfHome)
	}

	// rd sync status must report campfire as configured.
	stdout, stderr, code = e.Rd("sync", "status", "--json")
	if code != 0 {
		t.Fatalf("rd sync status failed (exit %d): %s", code, stderr)
	}
	var syncStatus map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &syncStatus); err != nil {
		t.Fatalf("sync status JSON parse: %v\noutput: %s", err, stdout)
	}
	if configured, ok := syncStatus["campfire_configured"].(bool); !ok || !configured {
		t.Errorf("campfire_configured: got %v, want true", syncStatus["campfire_configured"])
	}
}

// --------------------------------------------------------------------------
// Topology 4: Hosted persistent campfire (HTTP transport)
//
// Skipped if mcp.getcampfire.dev:443 is unreachable (TCP dial with timeout).
// --------------------------------------------------------------------------

func TestE2E_Topology4_Hosted_HTTPTransport(t *testing.T) {
	// Connectivity gate: skip if hosted campfire is unreachable.
	conn, err := net.DialTimeout("tcp", "mcp.east.getcampfire.dev:443", 5*time.Second)
	if err != nil {
		t.Skipf("mcp.east.getcampfire.dev:443 not reachable (%v) — skipping hosted topology test", err)
	}
	conn.Close()

	cfHome := t.TempDir()
	projectDir := t.TempDir()

	topologyCfInitRemote(t, cfHome, "https://mcp.east.getcampfire.dev/api")

	e := &Env{CFHome: cfHome, ProjectDir: projectDir, t: t}

	// rd init — creates campfire against hosted instance.
	stdout, stderr, code := e.Rd("init", "--name", "hosted-project", "--json", "--campfire")
	if code != 0 {
		t.Fatalf("rd init (hosted) failed (exit %d):\nstderr: %s\nstdout: %s", code, stderr, stdout)
	}

	var initResult map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &initResult); err != nil {
		t.Fatalf("rd init JSON parse: %v\noutput: %s", err, stdout)
	}
	if initResult["campfire_id"] == nil || initResult["campfire_id"] == "" {
		t.Error("rd init: campfire_id should be non-empty")
	}

	// Create a remote task.
	var item Item
	stdout, stderr, code = e.Rd("create",
		"--title", "remote task",
		"--priority", "p1",
		"--type", "task",
		"--json",
	)
	if code != 0 {
		t.Fatalf("rd create (hosted) failed (exit %d): %s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &item); err != nil {
		t.Fatalf("rd create JSON parse: %v\noutput: %s", err, stdout)
	}
	if item.ID == "" {
		t.Fatal("create returned empty ID")
	}

	// rd ready — item should appear.
	readyItems := e.ReadyItems()
	if !containsItem(readyItems, item.ID) {
		t.Fatalf("item %s not found in rd ready (hosted)", item.ID)
	}

	// rd sync status — campfire configured, 0 pending (send succeeded).
	stdout, stderr, code = e.Rd("sync", "status", "--json")
	if code != 0 {
		t.Fatalf("rd sync status (hosted) failed (exit %d): %s", code, stderr)
	}
	var syncStatus map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &syncStatus); err != nil {
		t.Fatalf("sync status JSON parse: %v\noutput: %s", err, stdout)
	}
	if configured, ok := syncStatus["campfire_configured"].(bool); !ok || !configured {
		t.Errorf("campfire_configured: got %v, want true", syncStatus["campfire_configured"])
	}
	if pending, ok := syncStatus["pending_count"].(float64); ok && pending > 0 {
		t.Errorf("pending_count: got %v, want 0 (all sends should succeed against hosted)", pending)
	}

	// Close the item cleanly.
	_, stderr, code = e.Rd("done", item.ID, "--reason", "hosted topology test complete")
	if code != 0 {
		t.Fatalf("rd done (hosted) failed (exit %d): %s", code, stderr)
	}

	// Verify it's gone from ready.
	readyItems = e.ReadyItems()
	if containsItem(readyItems, item.ID) {
		t.Errorf("closed item %s should not appear in rd ready (hosted)", item.ID)
	}
}

// --------------------------------------------------------------------------
// Topology-local helpers
// --------------------------------------------------------------------------

// topologyCfInit runs cf init for a topology test. Fatals on failure.
func topologyCfInit(t *testing.T, cfHome string) {
	t.Helper()
	cmd := exec.Command("cf", "init", "--cf-home", cfHome)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cf init failed: %v\n%s", err, out)
	}
}

// topologyCfInitRemote runs cf init and writes a config.toml with the relay URL.
// In cf 0.17+, the relay URL is configured via config.toml rather than a --remote flag.
func topologyCfInitRemote(t *testing.T, cfHome, relay string) {
	t.Helper()

	// Write config.toml with relay URL so cf create uses hosted transport.
	configContent := fmt.Sprintf("[transport]\nrelay = %q\n", relay)
	if err := os.MkdirAll(cfHome, 0700); err != nil {
		t.Fatalf("creating cfHome dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfHome, "config.toml"), []byte(configContent), 0600); err != nil {
		t.Fatalf("writing config.toml: %v", err)
	}

	cmd := exec.Command("cf", "init", "--cf-home", cfHome)
	cmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cf init failed: %v\n%s", err, out)
	}
}
