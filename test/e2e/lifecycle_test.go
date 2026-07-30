package e2e_test

import (
	"strings"
	"testing"
)

// createItem creates an item with auto-generated ID and returns it.
// --for is omitted so it defaults to the current session identity.
func createItem(e *Env, t *testing.T, title, priority, itemType string) Item {
	t.Helper()
	var item Item
	if err := e.RdJSON(&item, "create",
		"--title", title,
		"--priority", priority,
		"--type", itemType,
		"--parent-id", "none",
	); err != nil {
		t.Fatalf("create %q: %v", title, err)
	}
	if item.ID == "" {
		t.Fatalf("create returned empty ID for %q", title)
	}
	return item
}

// TestE2E_Create_ReturnsJSON verifies create --json returns id and title.
func TestE2E_Create_ReturnsJSON(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Create JSON test", "p1", "task")
	if item.ID == "" {
		t.Error("id should be non-empty")
	}
	if item.Title != "Create JSON test" {
		t.Errorf("title: got %q, want %q", item.Title, "Create JSON test")
	}
}

// TestE2E_Create_DefaultsForToIdentity verifies that omitting --for sets the
// for field to the caller's identity public key hex.
func TestE2E_Create_DefaultsForToIdentity(t *testing.T) {
	e := NewEnv(t)
	wantPubKey := e.IdentityPubKeyHex()

	var item Item
	if err := e.RdJSON(&item, "create",
		"--title", "Default for test",
		"--priority", "p2",
		"--type", "task",
		"--parent-id", "none",
	); err != nil {
		t.Fatalf("create: %v", err)
	}
	got := e.ShowItem(item.ID)
	if got.For != wantPubKey {
		t.Errorf("for: got %q, want identity pubkey %q", got.For, wantPubKey)
	}
}

// TestE2E_Create_AutoGeneratesID verifies --id is not required.
func TestE2E_Create_AutoGeneratesID(t *testing.T) {
	e := NewEnv(t)
	item1 := createItem(e, t, "Item one", "p1", "task")
	item2 := createItem(e, t, "Item two", "p1", "task")
	if item1.ID == "" || item2.ID == "" {
		t.Fatal("both items should have generated IDs")
	}
	if item1.ID == item2.ID {
		t.Errorf("two items got the same ID: %q", item1.ID)
	}
}

// TestE2E_Create_AppearsInReady verifies a new item appears in rd ready with status=inbox.
func TestE2E_Create_AppearsInReady(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Ready test", "p1", "task")
	items := e.ReadyItems()
	got, ok := findItem(items, item.ID)
	if !ok {
		t.Fatalf("item %s not found in ready", item.ID)
	}
	if got.Status != "inbox" {
		t.Errorf("status: got %q, want inbox", got.Status)
	}
}

// TestE2E_Create_AppearsInList verifies a new item appears in rd list.
func TestE2E_Create_AppearsInList(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "List test", "p2", "task")
	if !containsItem(e.ListItems(), item.ID) {
		t.Fatalf("item %s not found in list", item.ID)
	}
}

// TestE2E_Create_ShowByID verifies rd show returns correct fields.
func TestE2E_Create_ShowByID(t *testing.T) {
	e := NewEnv(t)
	var item Item
	if err := e.RdJSON(&item, "create",
		"--title", "Show test",
		"--priority", "p2",
		"--type", "decision",
		"--for", "baron@example.com",
		"--context", "some context",
		"--parent-id", "none",
	); err != nil {
		t.Fatalf("create: %v", err)
	}
	got := e.ShowItem(item.ID)
	if got.Title != "Show test" {
		t.Errorf("title: got %q", got.Title)
	}
	if got.Type != "decision" {
		t.Errorf("type: got %q, want decision", got.Type)
	}
	if got.For != "baron@example.com" {
		t.Errorf("for: got %q", got.For)
	}
	if got.Context != "some context" {
		t.Errorf("context: got %q", got.Context)
	}
}

// TestE2E_Update_StatusActive verifies rd update --status active transitions status.
func TestE2E_Update_StatusActive(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Update status test", "p1", "task")
	e.RdMustSucceed("update", item.ID, "--status", "active")
	if got := e.ShowItem(item.ID); got.Status != "active" {
		t.Errorf("status: got %q, want active", got.Status)
	}
}

// TestE2E_Update_Priority verifies rd update --priority changes priority.
func TestE2E_Update_Priority(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Update priority test", "p2", "task")
	e.RdMustSucceed("update", item.ID, "--priority", "p0")
	if got := e.ShowItem(item.ID); got.Priority != "p0" {
		t.Errorf("priority: got %q, want p0", got.Priority)
	}
}

// TestE2E_Update_Context verifies rd update --context changes context.
func TestE2E_Update_Context(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Update context test", "p2", "task")
	e.RdMustSucceed("update", item.ID, "--context", "updated context")
	if got := e.ShowItem(item.ID); got.Context != "updated context" {
		t.Errorf("context: got %q", got.Context)
	}
}

// TestE2E_Update_StatusAlias verifies in_progress resolves to active.
func TestE2E_Update_StatusAlias(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Status alias test", "p1", "task")
	e.RdMustSucceed("update", item.ID, "--status", "in_progress")
	if got := e.ShowItem(item.ID); got.Status != "active" {
		t.Errorf("status: got %q, want active (in_progress alias)", got.Status)
	}
}

// TestE2E_Update_Claim verifies --claim sets status=active and by field.
func TestE2E_Update_Claim(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Claim test", "p1", "task")
	e.RdMustSucceed("update", item.ID, "--claim")
	got := e.ShowItem(item.ID)
	if got.Status != "active" {
		t.Errorf("status: got %q, want active", got.Status)
	}
	if got.By == "" {
		t.Error("by field is empty after --claim")
	}
}

// TestE2E_Close_RequiresReason verifies rd close without --reason fails.
func TestE2E_Close_RequiresReason(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Close no reason test", "p2", "task")
	_, _, code := e.Rd("close", item.ID)
	if code == 0 {
		t.Fatal("rd close without --reason should fail but exited 0")
	}
}

// TestE2E_Close_Done verifies rd close --reason sets status=done.
func TestE2E_Close_Done(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Close done test", "p2", "task")
	e.RdMustSucceed("close", item.ID, "--reason", "done")
	if got := e.ShowItem(item.ID); got.Status != "done" {
		t.Errorf("status: got %q, want done", got.Status)
	}
}

// TestE2E_Close_Cancelled verifies --resolution cancelled sets status=cancelled.
func TestE2E_Close_Cancelled(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Close cancelled test", "p2", "task")
	e.RdMustSucceed("close", item.ID, "--resolution", "cancelled", "--reason", "nope")
	if got := e.ShowItem(item.ID); got.Status != "cancelled" {
		t.Errorf("status: got %q, want cancelled", got.Status)
	}
}

// TestE2E_Close_DisappearsFromReady verifies closed item is gone from rd ready.
func TestE2E_Close_DisappearsFromReady(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Disappears from ready", "p1", "task")
	e.RdMustSucceed("close", item.ID, "--reason", "done")
	if containsItem(e.ReadyItems(), item.ID) {
		t.Fatal("closed item still appears in rd ready")
	}
}

// TestE2E_Complete_ClosesAsDone verifies rd complete closes item as done.
func TestE2E_Complete_ClosesAsDone(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Complete test", "p1", "task")
	e.RdMustSucceed("complete", item.ID, "--reason", "done", "--branch", "work/test")
	if got := e.ShowItem(item.ID); got.Status != "done" {
		t.Errorf("status: got %q, want done", got.Status)
	}
}

// TestE2E_UpdateThenClose_AgentWorkflow exercises the #1 agent workflow pattern.
func TestE2E_UpdateThenClose_AgentWorkflow(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Agent workflow test", "p1", "task")
	e.RdMustSucceed("update", item.ID, "--status", "active", "--claim")
	if got := e.ShowItem(item.ID); got.Status != "active" {
		t.Fatalf("after claim: status=%q, want active", got.Status)
	}
	e.RdMustSucceed("close", item.ID, "--reason", "done")
	if got := e.ShowItem(item.ID); got.Status != "done" {
		t.Errorf("after close: status=%q, want done", got.Status)
	}
}

// TestE2E_Create_ExplicitIDCollision verifies that re-using an existing ID fails.
func TestE2E_Create_ExplicitIDCollision(t *testing.T) {
	e := NewEnv(t)
	e.RdMustSucceed("create",
		"--id", "dup-001",
		"--title", "First",
		"--priority", "p1",
		"--type", "task",
		"--for", "test@example.com",
		"--parent-id", "none",
	)
	stderr := e.RdMustFail("create",
		"--id", "dup-001",
		"--title", "Duplicate",
		"--priority", "p1",
		"--type", "task",
		"--for", "test@example.com",
		"--parent-id", "none",
	)
	if !strings.Contains(stderr, "dup-001") {
		t.Errorf("expected error mentioning the duplicate ID, got: %q", stderr)
	}
}

// TestE2E_Show_NotFound verifies rd show on a nonexistent ID exits non-zero with a clear error.
func TestE2E_Show_NotFound(t *testing.T) {
	e := NewEnv(t)
	stderr := e.RdMustFail("show", "doesnotexist")
	if !strings.Contains(stderr, "not found") {
		t.Errorf("expected 'not found' in stderr, got: %q", stderr)
	}
}
