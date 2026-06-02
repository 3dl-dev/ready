package crossdep_test

// Tests for cross-campfire dep resolution.
//
// Done conditions covered:
// 5. item A in acme.backend deps on item B in acme.frontend; user is member
//    of both -> B's status shown
// 6. item A deps on B; user is NOT a member -> warning shown, A not blocked
// 7. ApplyBlocking: member of both, blocker non-terminal -> item blocked
// 8. ApplyBlocking: member of both, blocker terminal -> item NOT blocked
// 9. ApplyBlocking: alias resolution -> item blocked via named ref
// 10. ApplyBlocking: not a member -> item NOT blocked

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/crossdep"
	"github.com/campfire-net/ready/pkg/state"
)

const (
	backendCampfire  = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	frontendCampfire = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
)

// addMsgToStore writes a message to the store.
func addMsgToStore(t *testing.T, s store.Store, campfireID, msgID, tag string, payload interface{}, antecedents []string, ts int64) {
	t.Helper()
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	if _, err := s.AddMessage(store.MessageRecord{
		ID:          msgID,
		CampfireID:  campfireID,
		Sender:      "testkey",
		Payload:     raw,
		Tags:        []string{tag},
		Antecedents: antecedents,
		Timestamp:   ts,
		Signature:   []byte("fakesig"),
		ReceivedAt:  ts,
	}); err != nil {
		t.Fatalf("AddMessage %s %s: %v", tag, msgID, err)
	}
}

// joinCampfire adds a membership entry for the given campfire ID.
func joinCampfire(t *testing.T, s store.Store, campfireID string) {
	t.Helper()
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "invite",
		Role:         "full",
		JoinedAt:     time.Now().Unix(),
	}); err != nil {
		t.Fatalf("AddMembership %s: %v", campfireID[:8], err)
	}
}

// TestResolveDeps_MemberOfBoth verifies that when the user is a member of both
// campfires, the cross-campfire dep resolves and shows item B's status.
// Done condition 5.
func TestResolveDeps_MemberOfBoth(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()

	// Member of both campfires.
	joinCampfire(t, s, backendCampfire)
	joinCampfire(t, s, frontendCampfire)

	// Item B in frontend campfire — status: done.
	addMsgToStore(t, s, frontendCampfire, "msg-b01", "work:create", map[string]interface{}{
		"id": "frontend-b01", "title": "Frontend item B", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)
	addMsgToStore(t, s, frontendCampfire, "msg-b01-close", "work:close", map[string]interface{}{
		"target":     "msg-b01",
		"resolution": "done",
		"reason":     "completed",
	}, []string{"msg-b01"}, ts+100)

	// Item A in backend campfire — has cross-campfire dep on B.
	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts+200)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id":  "acme.frontend.frontend-b01",
		"blocked_id":  "backend-a01",
		"blocker_msg": "msg-b01",
		"blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+300)

	// Derive backend items.
	backendItems, err := state.DeriveFromStore(s, backendCampfire)
	if err != nil {
		t.Fatalf("DeriveFromStore: %v", err)
	}
	itemA := backendItems["backend-a01"]
	if itemA == nil {
		t.Fatal("backend-a01 not found after Derive")
	}

	// Item A must NOT be blocked (cross-campfire dep is non-blocking in single-campfire Derive).
	if itemA.Status == state.StatusBlocked {
		t.Errorf("item A should not be blocked by cross-campfire dep, got %q", itemA.Status)
	}

	// Warning must be recorded on item A.
	if len(itemA.CrossCampfireWarnings) == 0 {
		t.Fatal("expected CrossCampfireWarnings for cross-campfire dep")
	}

	// Set up alias: "acme.frontend" -> frontendCampfire.
	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	if err := aliases.Set("acme.frontend", frontendCampfire); err != nil {
		t.Fatalf("setting alias: %v", err)
	}

	// Resolve deps.
	results := crossdep.ResolveDeps(itemA, s, aliases)
	if len(results) == 0 {
		t.Fatal("expected at least one resolution result")
	}

	dep := results[0]
	if dep.Warning != "" {
		t.Errorf("expected successful resolution (member of both), got warning: %s", dep.Warning)
	}
	if dep.Item == nil {
		t.Fatal("expected resolved item B, got nil")
	}
	// Item B is done.
	if dep.Item.Status != state.StatusDone {
		t.Errorf("expected item B status %q, got %q", state.StatusDone, dep.Item.Status)
	}
}

// TestResolveDeps_NotMember verifies that when the user is NOT a member of the
// target campfire, a warning is returned and item A is NOT blocked.
// Done condition 6.
func TestResolveDeps_NotMember(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()

	// Only join backend — NOT frontend.
	joinCampfire(t, s, backendCampfire)

	// Item A with cross-campfire dep.
	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id":  "acme.frontend.frontend-b01",
		"blocked_id":  "backend-a01",
		"blocker_msg": "msg-b01",
		"blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+100)

	// Derive backend items.
	backendItems, err := state.DeriveFromStore(s, backendCampfire)
	if err != nil {
		t.Fatalf("DeriveFromStore: %v", err)
	}
	itemA := backendItems["backend-a01"]
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}

	// Item A must NOT be blocked.
	if itemA.Status == state.StatusBlocked {
		t.Errorf("item A should not be blocked when not member of dep campfire, got %q", itemA.Status)
	}

	// Set up alias pointing to frontendCampfire (not joined).
	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	if err := aliases.Set("acme.frontend", frontendCampfire); err != nil {
		t.Fatalf("setting alias: %v", err)
	}

	// Resolve deps — should produce a warning.
	results := crossdep.ResolveDeps(itemA, s, aliases)
	if len(results) == 0 {
		t.Fatal("expected resolution results")
	}

	dep := results[0]
	if dep.Warning == "" {
		t.Error("expected warning when not a member of target campfire")
	}
	if dep.Item != nil {
		t.Errorf("expected nil item when not a member, got: %+v", dep.Item)
	}
	// Warning should mention the dep and membership.
	if !strings.Contains(dep.Warning, "frontend-b01") {
		t.Errorf("warning should mention the dep ref, got: %s", dep.Warning)
	}
	if !strings.Contains(dep.Warning, "not a member") {
		t.Errorf("warning should mention 'not a member', got: %s", dep.Warning)
	}
}

// TestResolveDeps_NoAlias verifies that when the campfire name has no alias,
// a warning is returned (dep is non-blocking).
func TestResolveDeps_NoAlias(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()
	joinCampfire(t, s, backendCampfire)

	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id":  "unknown.org.some-item",
		"blocked_id":  "backend-a01",
		"blocker_msg": "msg-x",
		"blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+100)

	backendItems, _ := state.DeriveFromStore(s, backendCampfire)
	itemA := backendItems["backend-a01"]
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}

	// No alias configured — should warn.
	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	results := crossdep.ResolveDeps(itemA, s, aliases)
	if len(results) == 0 {
		t.Fatal("expected resolution results")
	}
	if results[0].Warning == "" {
		t.Error("expected warning when campfire name has no alias")
	}
	if results[0].Item != nil {
		t.Error("expected nil item when alias not found")
	}
}

// TestResolveDeps_AmbiguousItemID verifies that when multiple items in the
// target campfire share the same prefix, a warning is returned (no panic,
// no silent incorrect resolution).
func TestResolveDeps_AmbiguousItemID(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()
	joinCampfire(t, s, backendCampfire)
	joinCampfire(t, s, frontendCampfire)

	// Two items in frontend with the same prefix: "ready-abc" and "ready-abcdef".
	for _, id := range []string{"ready-abc", "ready-abcdef"} {
		addMsgToStore(t, s, frontendCampfire, "msg-"+id, "work:create", map[string]interface{}{
			"id": id, "title": id, "type": "task", "for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts)
		ts++
	}

	// Item A blocks on "ready-abc" (exact match exists, but prefix matches two).
	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task", "for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id": "acme.frontend.ready-abc", "blocked_id": "backend-a01",
		"blocker_msg": "msg-ready-abc", "blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+1)

	backendItems, _ := state.DeriveFromStore(s, backendCampfire)
	itemA := backendItems["backend-a01"]
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}

	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	if err := aliases.Set("acme.frontend", frontendCampfire); err != nil {
		t.Fatalf("setting alias: %v", err)
	}

	results := crossdep.ResolveDeps(itemA, s, aliases)
	if len(results) == 0 {
		t.Fatal("expected resolution results")
	}
	// Exact match exists ("ready-abc") so it should resolve successfully.
	dep := results[0]
	if dep.Item == nil {
		t.Errorf("expected exact match to resolve, got warning: %s", dep.Warning)
	}
}

// TestResolveDeps_AmbiguousPrefixOnly verifies that when only prefix matches
// exist (no exact match) and there are multiple, a warning is returned.
func TestResolveDeps_AmbiguousPrefixOnly(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()
	joinCampfire(t, s, backendCampfire)
	joinCampfire(t, s, frontendCampfire)

	// Two items both prefixed by "ready-x": "ready-xabc" and "ready-xdef".
	// Searching for "ready-x" (which has a hyphen, so it's a valid cross-campfire item ref)
	// will match both via prefix — no exact match.
	for i, id := range []string{"ready-xabc", "ready-xdef"} {
		addMsgToStore(t, s, frontendCampfire, "msg-"+id, "work:create", map[string]interface{}{
			"id": id, "title": id, "type": "task", "for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts+int64(i))
	}

	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task", "for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts+10)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id": "acme.frontend.ready-x", "blocked_id": "backend-a01",
		"blocker_msg": "msg-ready-x", "blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+11)

	backendItems, _ := state.DeriveFromStore(s, backendCampfire)
	itemA := backendItems["backend-a01"]
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}

	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	if err := aliases.Set("acme.frontend", frontendCampfire); err != nil {
		t.Fatalf("setting alias: %v", err)
	}

	results := crossdep.ResolveDeps(itemA, s, aliases)
	if len(results) == 0 {
		t.Fatal("expected resolution results")
	}
	dep := results[0]
	if dep.Item != nil {
		t.Error("expected nil item on ambiguous prefix match, got resolved item")
	}
	if !strings.Contains(dep.Warning, "ambiguous") {
		t.Errorf("expected 'ambiguous' in warning, got: %s", dep.Warning)
	}
}

// TestResolveDeps_ItemNotFound verifies that when the referenced item does not
// exist in the target campfire, a warning is returned.
func TestResolveDeps_ItemNotFound(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()
	joinCampfire(t, s, backendCampfire)
	joinCampfire(t, s, frontendCampfire)
	// frontend campfire is empty — no items.

	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task", "for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id": "acme.frontend.nonexistent-xyz", "blocked_id": "backend-a01",
		"blocker_msg": "msg-nonexistent", "blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+1)

	backendItems, _ := state.DeriveFromStore(s, backendCampfire)
	itemA := backendItems["backend-a01"]
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}

	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	if err := aliases.Set("acme.frontend", frontendCampfire); err != nil {
		t.Fatalf("setting alias: %v", err)
	}

	results := crossdep.ResolveDeps(itemA, s, aliases)
	if len(results) == 0 {
		t.Fatal("expected resolution results")
	}
	dep := results[0]
	if dep.Item != nil {
		t.Error("expected nil item when referenced item does not exist in target campfire")
	}
	if dep.Warning == "" {
		t.Error("expected warning when item not found in target campfire")
	}
}

// TestResolveDeps_LastSegmentFallbackDisabled verifies that when a full
// campfire name (e.g. "acme.frontend") has no alias, the resolution does NOT
// fall back to trying the last segment (e.g. "frontend") as an alias.
// This is a regression test for a security issue: if the user has a short
// alias "frontend" pointing to a different campfire, the dep should NOT
// resolve against that unintended campfire.
func TestResolveDeps_LastSegmentFallbackDisabled(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()

	// Join both campfires.
	joinCampfire(t, s, backendCampfire)
	joinCampfire(t, s, frontendCampfire)

	// Create an item in frontend.
	addMsgToStore(t, s, frontendCampfire, "msg-b01", "work:create", map[string]interface{}{
		"id": "frontend-b01", "title": "Frontend item B", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)

	// Item A blocks on "acme.frontend.frontend-b01".
	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts+1)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id": "acme.frontend.frontend-b01", "blocked_id": "backend-a01",
		"blocker_msg": "msg-b01", "blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+2)

	backendItems, _ := state.DeriveFromStore(s, backendCampfire)
	itemA := backendItems["backend-a01"]
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}

	// Set up aliases: NO alias for "acme.frontend", but DO have an alias
	// for the last segment "frontend" pointing to a different campfire ID.
	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	unintendedCampfireID := "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	if err := aliases.Set("frontend", unintendedCampfireID); err != nil {
		t.Fatalf("setting alias for 'frontend': %v", err)
	}

	// Resolve deps. Should fail because "acme.frontend" has no alias,
	// and the last-segment fallback is disabled.
	results := crossdep.ResolveDeps(itemA, s, aliases)
	if len(results) == 0 {
		t.Fatal("expected resolution results")
	}

	dep := results[0]
	// Should have a warning (not resolved).
	if dep.Warning == "" {
		t.Error("expected warning: full name 'acme.frontend' has no alias, should not fall back to 'frontend'")
	}
	// Should not have resolved to any item.
	if dep.Item != nil {
		t.Errorf("expected nil item (fallback disabled), but got resolved item: %+v", dep.Item)
	}
	// Verify the warning mentions it couldn't find the alias.
	if !strings.Contains(dep.Warning, "campfire not in local aliases") {
		t.Errorf("expected 'campfire not in local aliases' in warning, got: %s", dep.Warning)
	}
}

// TestApplyBlocking_MemberOfBoth verifies that when the user is a member of
// both campfires, ApplyBlocking resolves cross-campfire deps and blocks items
// whose blocker is non-terminal.
func TestApplyBlocking_MemberOfBoth(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()

	joinCampfire(t, s, backendCampfire)
	joinCampfire(t, s, frontendCampfire)

	// Item B in frontend campfire -- active (non-terminal).
	addMsgToStore(t, s, frontendCampfire, "msg-b01", "work:create", map[string]interface{}{
		"id": "frontend-b01", "title": "Frontend item B", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)

	// Item A in backend campfire -- cross-campfire dep on B via campfire ID ref.
	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts+200)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id":  frontendCampfire + ".frontend-b01",
		"blocked_id":  "backend-a01",
		"blocker_msg": "msg-b01",
		"blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+300)

	var items []*state.Item
	for _, cfID := range []string{backendCampfire, frontendCampfire} {
		derived, err := state.DeriveFromStore(s, cfID)
		if err != nil {
			t.Fatalf("DeriveFromStore %s: %v", cfID[:8], err)
		}
		for _, item := range derived {
			items = append(items, item)
		}
	}

	var itemA *state.Item
	for _, item := range items {
		if item.ID == "backend-a01" {
			itemA = item
			break
		}
	}
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}
	if itemA.Status == state.StatusBlocked {
		t.Error("item A should not be blocked before ApplyBlocking")
	}

	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)

	crossdep.ApplyBlocking(items, s, aliases)

	if itemA.Status != state.StatusBlocked {
		t.Errorf("expected item A status %q after ApplyBlocking, got %q", state.StatusBlocked, itemA.Status)
	}
	if len(itemA.BlockedBy) == 0 {
		t.Fatal("expected BlockedBy to contain cross-campfire ref")
	}
	found := false
	for _, b := range itemA.BlockedBy {
		if strings.Contains(b, "frontend-b01") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected BlockedBy to reference frontend-b01, got: %v", itemA.BlockedBy)
	}
}

// TestApplyBlocking_BlockerTerminal verifies that when the blocker is done,
// ApplyBlocking does NOT block the item.
func TestApplyBlocking_BlockerTerminal(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()

	joinCampfire(t, s, backendCampfire)
	joinCampfire(t, s, frontendCampfire)

	addMsgToStore(t, s, frontendCampfire, "msg-b01", "work:create", map[string]interface{}{
		"id": "frontend-b01", "title": "Frontend item B", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)
	addMsgToStore(t, s, frontendCampfire, "msg-b01-close", "work:close", map[string]interface{}{
		"target":     "msg-b01",
		"resolution": "done",
		"reason":     "completed",
	}, []string{"msg-b01"}, ts+100)

	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts+200)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id":  frontendCampfire + ".frontend-b01",
		"blocked_id":  "backend-a01",
		"blocker_msg": "msg-b01",
		"blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+300)

	var items []*state.Item
	for _, cfID := range []string{backendCampfire, frontendCampfire} {
		derived, err := state.DeriveFromStore(s, cfID)
		if err != nil {
			t.Fatalf("DeriveFromStore: %v", err)
		}
		for _, item := range derived {
			items = append(items, item)
		}
	}

	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	crossdep.ApplyBlocking(items, s, aliases)

	var itemA *state.Item
	for _, item := range items {
		if item.ID == "backend-a01" {
			itemA = item
			break
		}
	}
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}
	if itemA.Status == state.StatusBlocked {
		t.Errorf("item A should NOT be blocked when blocker is terminal, got status %q", itemA.Status)
	}
}

// TestApplyBlocking_AliasResolution verifies that ApplyBlocking resolves
// named cross-campfire refs via aliases.
func TestApplyBlocking_AliasResolution(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()

	joinCampfire(t, s, backendCampfire)
	joinCampfire(t, s, frontendCampfire)

	addMsgToStore(t, s, frontendCampfire, "msg-b01", "work:create", map[string]interface{}{
		"id": "frontend-b01", "title": "Frontend item B", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)

	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts+200)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id":  "acme.frontend.frontend-b01",
		"blocked_id":  "backend-a01",
		"blocker_msg": "msg-b01",
		"blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+300)

	var items []*state.Item
	for _, cfID := range []string{backendCampfire, frontendCampfire} {
		derived, err := state.DeriveFromStore(s, cfID)
		if err != nil {
			t.Fatalf("DeriveFromStore: %v", err)
		}
		for _, item := range derived {
			items = append(items, item)
		}
	}

	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	if err := aliases.Set("acme.frontend", frontendCampfire); err != nil {
		t.Fatalf("setting alias: %v", err)
	}

	crossdep.ApplyBlocking(items, s, aliases)

	var itemA *state.Item
	for _, item := range items {
		if item.ID == "backend-a01" {
			itemA = item
			break
		}
	}
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}
	if itemA.Status != state.StatusBlocked {
		t.Errorf("expected item A to be blocked via alias resolution, got status %q", itemA.Status)
	}
}

// TestApplyBlocking_NotMember verifies that when the user is NOT a member of
// the blocker's campfire, ApplyBlocking does NOT block the item.
func TestApplyBlocking_NotMember(t *testing.T) {
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	defer s.Close()

	ts := time.Now().UnixNano()

	joinCampfire(t, s, backendCampfire)

	addMsgToStore(t, s, backendCampfire, "msg-a01", "work:create", map[string]interface{}{
		"id": "backend-a01", "title": "Backend item A", "type": "task",
		"for": "baron@3dl.dev", "priority": "p1",
	}, nil, ts)
	addMsgToStore(t, s, backendCampfire, "msg-block-cross", "work:block", map[string]interface{}{
		"blocker_id":  frontendCampfire + ".frontend-b01",
		"blocked_id":  "backend-a01",
		"blocker_msg": "msg-b01",
		"blocked_msg": "msg-a01",
	}, []string{"msg-a01"}, ts+100)

	var items []*state.Item
	derived, _ := state.DeriveFromStore(s, backendCampfire)
	for _, item := range derived {
		items = append(items, item)
	}

	aliasDir := t.TempDir()
	aliases := naming.NewAliasStore(aliasDir)
	crossdep.ApplyBlocking(items, s, aliases)

	var itemA *state.Item
	for _, item := range items {
		if item.ID == "backend-a01" {
			itemA = item
			break
		}
	}
	if itemA == nil {
		t.Fatal("backend-a01 not found")
	}
	if itemA.Status == state.StatusBlocked {
		t.Error("item A should NOT be blocked when not a member of blocker's campfire")
	}
}

// TestShortID verifies that shortID returns the full string for short IDs
// and truncates to 12 chars with "..." for longer ones.
func TestShortID(t *testing.T) {
	cases := []struct {
		input string
		want  string
	}{
		{"abc", "abc"},
		{"1234567890ab", "1234567890ab"},     // exactly 12 -- no truncation
		{"1234567890abc", "1234567890ab..."}, // 13 chars -- truncate
		{"", ""},
		{backendCampfire, backendCampfire[:12] + "..."},
	}
	for _, c := range cases {
		got := crossdep.ShortIDForTest(c.input)
		if got != c.want {
			t.Errorf("shortID(%q) = %q, want %q", c.input, got, c.want)
		}
	}
}
