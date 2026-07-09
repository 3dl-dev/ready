package e2e_test

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// NewEnvOffline creates a fresh environment with NO campfire — JSONL-only mode.
// The identity is still created (needed for message signing) but no campfire is
// configured. The caller must run 'rd init --offline' to initialize .ready/.
func NewEnvOffline(t *testing.T) *Env {
	t.Helper()

	cfHome := t.TempDir()

	// cf init — create identity (still needed for message signing).
	initCmd := exec.Command("cf", "init", "--cf-home", cfHome)
	initCmd.Env = []string{
		"PATH=" + os.Getenv("PATH"),
		"HOME=" + os.Getenv("HOME"),
	}
	if out, err := initCmd.CombinedOutput(); err != nil {
		t.Fatalf("cf init failed: %v\n%s", err, out)
	}

	// Create project dir — no campfire root, no .ready/ yet.
	projectDir := t.TempDir()

	return &Env{
		CFHome:     cfHome,
		CampfireID: "", // no campfire in offline mode
		ProjectDir: projectDir,
		t:          t,
	}
}

// TestE2E_Offline_InitCreatesReadyDir verifies that rd init --offline
// creates .ready/ without a campfire.
func TestE2E_Offline_InitCreatesReadyDir(t *testing.T) {
	e := NewEnvOffline(t)

	stdout, stderr, code := e.Rd("init", "--offline", "--name", "offline-test", "--json")
	if code != 0 {
		t.Fatalf("rd init --offline failed (exit %d):\nstderr: %s\nstdout: %s", code, stderr, stdout)
	}

	var result map[string]interface{}
	if err := json.Unmarshal([]byte(stdout), &result); err != nil {
		t.Fatalf("parse init JSON: %v\noutput: %s", err, stdout)
	}

	if result["mode"] != "offline" {
		t.Errorf("mode: got %v, want offline", result["mode"])
	}
	if result["name"] != "offline-test" {
		t.Errorf("name: got %v, want offline-test", result["name"])
	}

	// Verify .ready/ was created.
	readyDir := filepath.Join(e.ProjectDir, ".ready")
	if _, err := os.Stat(readyDir); err != nil {
		t.Fatalf(".ready/ not created: %v", err)
	}

	// Verify project.json was written.
	projectJSON := filepath.Join(readyDir, "project.json")
	if _, err := os.Stat(projectJSON); err != nil {
		t.Fatalf(".ready/project.json not created: %v", err)
	}

	// Verify no .campfire/root was created.
	if _, err := os.Stat(filepath.Join(e.ProjectDir, ".campfire", "root")); err == nil {
		t.Error(".campfire/root should NOT exist in offline mode")
	}
}

// TestE2E_Offline_FullLifecycle verifies create → claim → update → close → list
// all work without a campfire.
func TestE2E_Offline_FullLifecycle(t *testing.T) {
	e := NewEnvOffline(t)

	// Initialize offline.
	_, _, code := e.Rd("init", "--offline", "--name", "lifecycle-test")
	if code != 0 {
		t.Fatalf("rd init --offline failed")
	}

	// Create.
	var item Item
	if err := e.RdJSON(&item, "create",
		"--title", "offline lifecycle item",
		"--priority", "p1",
		"--type", "task",
	); err != nil {
		t.Fatalf("create: %v", err)
	}
	if item.ID == "" {
		t.Fatal("create returned empty ID")
	}

	// List — item should appear.
	items := e.ListItems()
	if !containsItem(items, item.ID) {
		t.Fatalf("item %s not found in list after create", item.ID)
	}

	// Claim.
	_, stderr, code := e.Rd("claim", item.ID)
	if code != 0 {
		t.Fatalf("claim failed (exit %d):\nstderr: %s", code, stderr)
	}

	// Show — status should be active.
	got := e.ShowItem(item.ID)
	if got.Status != "active" {
		t.Errorf("status after claim: got %q, want active", got.Status)
	}

	// Update — change title.
	_, stderr, code = e.Rd("update", item.ID, "--title", "updated offline item")
	if code != 0 {
		t.Fatalf("update failed (exit %d):\nstderr: %s", code, stderr)
	}

	// Show — title should be updated.
	got = e.ShowItem(item.ID)
	if got.Title != "updated offline item" {
		t.Errorf("title after update: got %q, want updated offline item", got.Title)
	}

	// Close.
	_, stderr, code = e.Rd("close", item.ID, "--reason", "offline lifecycle test complete")
	if code != 0 {
		t.Fatalf("close failed (exit %d):\nstderr: %s", code, stderr)
	}

	// Show — status should be done.
	got = e.ShowItem(item.ID)
	if got.Status != "done" {
		t.Errorf("status after close: got %q, want done", got.Status)
	}

	// List (all including closed) — item should still appear.
	_, _, code = e.Rd("list", "--all", "--status", "done")
	if code != 0 {
		t.Fatalf("list --status done failed")
	}
}

// TestE2E_Offline_JSONLWriteSucceedsWithoutCampfire verifies that mutation records
// are written to .ready/mutations.jsonl even when no campfire is configured.
func TestE2E_Offline_JSONLWriteSucceedsWithoutCampfire(t *testing.T) {
	e := NewEnvOffline(t)

	_, _, code := e.Rd("init", "--offline", "--name", "jsonl-test")
	if code != 0 {
		t.Fatalf("rd init --offline failed")
	}

	// Create an item.
	var item Item
	if err := e.RdJSON(&item, "create",
		"--title", "jsonl write test",
		"--priority", "p2",
		"--type", "task",
	); err != nil {
		t.Fatalf("create: %v", err)
	}

	// Verify mutations.jsonl was written.
	mutationsPath := filepath.Join(e.ProjectDir, ".ready", "mutations.jsonl")
	data, err := os.ReadFile(mutationsPath)
	if err != nil {
		t.Fatalf("reading mutations.jsonl: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("mutations.jsonl is empty")
	}

	// Verify the record contains the item ID.
	content := string(data)
	if !contains(content, item.ID) {
		t.Errorf("mutations.jsonl does not contain item ID %q:\n%s", item.ID, content)
	}

	// Verify it's valid JSONL (each line is valid JSON).
	lines := splitLines(content)
	for i, line := range lines {
		if line == "" {
			continue
		}
		var rec map[string]interface{}
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Errorf("mutations.jsonl line %d is not valid JSON: %v\nline: %s", i+1, err, line)
		}
	}

	// Verify no pending.jsonl was created (no campfire failure).
	pendingPath := filepath.Join(e.ProjectDir, ".ready", "pending.jsonl")
	if _, err := os.Stat(pendingPath); err == nil {
		t.Error("pending.jsonl should NOT exist in JSONL-only mode (no campfire to fail)")
	}
}

// TestE2E_Offline_PendingAccumulatesOnCampfireFailure verifies that pending.jsonl
// accumulates mutations when the campfire send fails.
//
// We simulate this by setting up a campfire-backed project, then removing the
// transport directory so sends fail, and verifying pending.jsonl grows.
func TestE2E_Offline_PendingAccumulatesOnCampfireFailure(t *testing.T) {
	e := NewEnv(t) // campfire-backed

	// Create one item successfully.
	var item1 Item
	if err := e.RdJSON(&item1, "create",
		"--title", "item before transport failure",
		"--priority", "p1",
		"--type", "task",
	); err != nil {
		t.Fatalf("create item1: %v", err)
	}

	// Sabotage the campfire transport by removing the transport directory.
	// e.TransportDir is the authoritative path returned by cf create --json.
	campfireTransportDir := e.TransportDir
	if _, err := os.Stat(campfireTransportDir); err != nil {
		t.Fatalf("cannot locate campfire transport dir at %s — test environment broken: %v",
			campfireTransportDir, err)
	}

	// Remove the transport directory so future sends fail.
	if err := os.RemoveAll(campfireTransportDir); err != nil {
		t.Fatalf("removing transport dir: %v", err)
	}

	// Create another item — JSONL write should succeed, campfire send should fail
	// (buffered to pending.jsonl).
	var item2 Item
	stdout, stderr, code := e.Rd("create",
		"--title", "item after transport failure",
		"--priority", "p2",
		"--type", "task",
		"--json",
	)
	// Command should still succeed (JSONL write is phase 1).
	if code != 0 {
		t.Fatalf("create after transport failure should succeed (exit %d):\nstderr: %s", code, stderr)
	}
	if err := json.Unmarshal([]byte(stdout), &item2); err != nil {
		t.Fatalf("parse create JSON: %v\noutput: %s", err, stdout)
	}
	if item2.ID == "" {
		t.Fatal("create after transport failure returned empty ID")
	}

	// Verify item2 appears in JSONL-derived list.
	items := e.ListItems()
	if !containsItem(items, item2.ID) {
		t.Errorf("item2 %s not found after campfire failure", item2.ID)
	}

	// Verify pending.jsonl was created with at least one record.
	pendingPath := filepath.Join(e.ProjectDir, ".ready", "pending.jsonl")
	data, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatalf("pending.jsonl not created after campfire failure: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("pending.jsonl is empty — expected buffered mutations")
	}

	// Create a third item — pending.jsonl should grow.
	var item3 Item
	if err := e.RdJSON(&item3, "create",
		"--title", "third item also fails campfire",
		"--priority", "p3",
		"--type", "task",
	); err != nil {
		t.Fatalf("create item3: %v", err)
	}

	data2, err := os.ReadFile(pendingPath)
	if err != nil {
		t.Fatalf("reading pending.jsonl after item3: %v", err)
	}
	if len(data2) <= len(data) {
		t.Error("pending.jsonl should have grown after second campfire failure")
	}
}

// TestE2E_Offline_ReadyViewWorks verifies that rd ready works in JSONL-only mode
// and filters correctly: active items appear, done items do not.
func TestE2E_Offline_ReadyViewWorks(t *testing.T) {
	e := NewEnvOffline(t)

	_, _, code := e.Rd("init", "--offline", "--name", "ready-test")
	if code != 0 {
		t.Fatalf("rd init --offline failed")
	}

	// Create an active item (p0 — ETA will be now, should appear in ready view).
	var p0Item Item
	if err := e.RdJSON(&p0Item, "create",
		"--title", "urgent item",
		"--priority", "p0",
		"--type", "task",
	); err != nil {
		t.Fatalf("create p0: %v", err)
	}

	// Create a second item and close it — done items must NOT appear in ready view.
	var doneItem Item
	if err := e.RdJSON(&doneItem, "create",
		"--title", "completed item",
		"--priority", "p1",
		"--type", "task",
	); err != nil {
		t.Fatalf("create done item: %v", err)
	}
	_, stderr, code := e.Rd("close", doneItem.ID, "--reason", "finished")
	if code != 0 {
		t.Fatalf("close done item failed (exit %d): %s", code, stderr)
	}

	// rd ready should return the active p0 item.
	items := e.ReadyItems()
	if !containsItem(items, p0Item.ID) {
		t.Errorf("p0 item %s not found in ready view", p0Item.ID)
	}

	// The done item must NOT appear in ready view — verifies terminal status filtering.
	if containsItem(items, doneItem.ID) {
		t.Errorf("done item %s should NOT appear in ready view, but it did", doneItem.ID)
	}
}

// TestE2E_Offline_DepRemoveWorks verifies that 'rd dep remove' actually removes
// the blocking edge in pure --offline (JSONL-only) mode, where there is no
// campfire store to scan. Prior to the ready-b41 fix, findBlockMessage only
// scanned the (empty) campfire store, so dep remove was a silent no-op offline
// and the blocked item never became ready.
func TestE2E_Offline_DepRemoveWorks(t *testing.T) {
	e := NewEnvOffline(t)

	_, _, code := e.Rd("init", "--offline", "--name", "dep-remove-test")
	if code != 0 {
		t.Fatalf("rd init --offline failed")
	}

	var blocker, blocked Item
	if err := e.RdJSON(&blocker, "create", "--title", "Blocker", "--priority", "p1", "--type", "task"); err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	if err := e.RdJSON(&blocked, "create", "--title", "Blocked", "--priority", "p1", "--type", "task"); err != nil {
		t.Fatalf("create blocked: %v", err)
	}

	// Wire the dependency.
	_, stderr, code := e.Rd("dep", "add", blocked.ID, blocker.ID)
	if code != 0 {
		t.Fatalf("dep add failed (exit %d): %s", code, stderr)
	}

	// Confirm the block took effect: blocked item is absent from ready view.
	if containsItem(e.ReadyItems(), blocked.ID) {
		t.Fatal("blocked item should not be ready before dep remove")
	}
	got := e.ShowItem(blocked.ID)
	if !containsString(got.BlockedBy, blocker.ID) {
		t.Fatalf("blocked item should list %s in blocked_by, got %v", blocker.ID, got.BlockedBy)
	}

	// Remove the dependency — this is the offline path under test.
	_, stderr, code = e.Rd("dep", "remove", blocked.ID, blocker.ID)
	if code != 0 {
		t.Fatalf("dep remove failed (exit %d): %s", code, stderr)
	}

	// The blocking edge must actually be gone: blocked_by no longer lists the
	// blocker, and the item is ready again (blocker is still open/active).
	got = e.ShowItem(blocked.ID)
	if containsString(got.BlockedBy, blocker.ID) {
		t.Fatalf("blocked_by still lists %s after dep remove: %v", blocker.ID, got.BlockedBy)
	}
	if !containsItem(e.ReadyItems(), blocked.ID) {
		t.Fatal("item should be ready after dep remove (offline mode)")
	}
}

// --- helpers ---

// containsString reports whether ss contains target.
func containsString(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}

// contains checks if s contains substr.
func contains(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(substr) == 0 ||
		func() bool {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
			return false
		}())
}

// splitLines splits a string into lines, trimming trailing newline.
func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	var lines []string
	start := 0
	for i, c := range s {
		if c == '\n' {
			lines = append(lines, s[start:i])
			start = i + 1
		}
	}
	if start < len(s) {
		lines = append(lines, s[start:])
	}
	return lines
}
