package e2e_test

import (
	"testing"
)

// TestE2E_Ready_ShowsInboxItems verifies a new P1 item appears in rd ready.
func TestE2E_Ready_ShowsInboxItems(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Ready shows inbox", "p1", "task")
	if !containsItem(e.ReadyItems(), item.ID) {
		t.Fatalf("item %s not found in ready", item.ID)
	}
}

// TestE2E_Ready_ExcludesTerminal verifies closed items don't appear in rd ready.
func TestE2E_Ready_ExcludesTerminal(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Terminal exclusion test", "p1", "task")
	e.RdMustSucceed("close", item.ID, "--reason", "done")
	if containsItem(e.ReadyItems(), item.ID) {
		t.Fatal("closed item should not appear in rd ready")
	}
}

// TestE2E_Ready_ExcludesBlocked verifies blocked items don't appear in rd ready.
func TestE2E_Ready_ExcludesBlocked(t *testing.T) {
	e := NewEnv(t)
	blocker := createItem(e, t, "Blocker", "p1", "task")
	blocked := createItem(e, t, "Blocked item", "p1", "task")
	e.RdMustSucceed("dep", "add", blocked.ID, blocker.ID)

	ready := e.ReadyItems()
	if !containsItem(ready, blocker.ID) {
		t.Error("blocker should be in ready")
	}
	if containsItem(ready, blocked.ID) {
		t.Error("blocked item should not be in ready")
	}
}

// TestE2E_List_DefaultExcludesTerminal verifies rd list excludes done/cancelled by default.
func TestE2E_List_DefaultExcludesTerminal(t *testing.T) {
	e := NewEnv(t)
	open := createItem(e, t, "Open item", "p2", "task")
	closed := createItem(e, t, "Closed item", "p2", "task")
	e.RdMustSucceed("close", closed.ID, "--reason", "done")

	var items []Item
	if err := e.RdJSON(&items, "list"); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !containsItem(items, open.ID) {
		t.Error("open item should be in list")
	}
	if containsItem(items, closed.ID) {
		t.Error("closed item should not be in default list")
	}
}

// TestE2E_List_AllIncludesTerminal verifies rd list --all includes terminal items.
func TestE2E_List_AllIncludesTerminal(t *testing.T) {
	e := NewEnv(t)
	open := createItem(e, t, "Open item", "p2", "task")
	closed := createItem(e, t, "Closed item", "p2", "task")
	e.RdMustSucceed("close", closed.ID, "--reason", "done")

	items := e.ListItems()
	if !containsItem(items, open.ID) {
		t.Error("open item should be in --all list")
	}
	if !containsItem(items, closed.ID) {
		t.Error("closed item should be in --all list")
	}
}

// TestE2E_List_MultiStatusOR verifies --status is repeatable with OR semantics.
func TestE2E_List_MultiStatusOR(t *testing.T) {
	e := NewEnv(t)
	inbox := createItem(e, t, "Inbox item", "p2", "task")
	active := createItem(e, t, "Active item", "p2", "task")
	done := createItem(e, t, "Done item", "p2", "task")
	e.RdMustSucceed("update", active.ID, "--status", "active")
	e.RdMustSucceed("close", done.ID, "--reason", "done")

	var items []Item
	if err := e.RdJSON(&items, "list", "--status", "inbox", "--status", "active"); err != nil {
		t.Fatalf("list --status inbox --status active: %v", err)
	}
	if !containsItem(items, inbox.ID) {
		t.Error("inbox item should match --status inbox")
	}
	if !containsItem(items, active.ID) {
		t.Error("active item should match --status active")
	}
	if containsItem(items, done.ID) {
		t.Error("done item should not match --status inbox --status active")
	}
}

// TestE2E_List_StatusAlias verifies in_progress alias resolves in list filter.
func TestE2E_List_StatusAlias(t *testing.T) {
	e := NewEnv(t)
	item := createItem(e, t, "Status alias list test", "p1", "task")
	e.RdMustSucceed("update", item.ID, "--status", "active")

	var items []Item
	if err := e.RdJSON(&items, "list", "--status", "in_progress"); err != nil {
		t.Fatalf("list --status in_progress: %v", err)
	}
	if !containsItem(items, item.ID) {
		t.Error("active item should match --status in_progress alias")
	}
}

// TestE2E_List_ByPriority verifies --priority filter works.
func TestE2E_List_ByPriority(t *testing.T) {
	e := NewEnv(t)
	p0 := createItem(e, t, "P0 item", "p0", "task")
	p2 := createItem(e, t, "P2 item", "p2", "task")

	var items []Item
	if err := e.RdJSON(&items, "list", "--priority", "p0"); err != nil {
		t.Fatalf("list --priority p0: %v", err)
	}
	if !containsItem(items, p0.ID) {
		t.Error("p0 item should match --priority p0")
	}
	if containsItem(items, p2.ID) {
		t.Error("p2 item should not match --priority p0")
	}
}

// TestE2E_List_JSONFieldContract verifies key JSON field types and aliases.
func TestE2E_List_JSONFieldContract(t *testing.T) {
	e := NewEnv(t)
	var created Item
	if err := e.RdJSON(&created, "create",
		"--title", "Field contract test",
		"--priority", "p1",
		"--type", "task",
		"--for", "test@example.com",
		"--context", "some context text",
		"--parent-id", "none",
	); err != nil {
		t.Fatalf("create: %v", err)
	}

	item, ok := findItem(e.ListItems(), created.ID)
	if !ok {
		t.Fatal("item not found in list")
	}
	if item.ID == "" {
		t.Error("id should be non-empty string")
	}
	if item.CreatedAt == 0 {
		t.Error("created_at should be non-zero int64")
	}
	if item.Context != "some context text" {
		t.Errorf("context: got %q", item.Context)
	}
	if item.Description != "some context text" {
		t.Errorf("description (alias for context): got %q", item.Description)
	}
}
