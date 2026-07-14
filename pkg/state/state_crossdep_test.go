package state_test

// Tests for cross-campfire item reference handling in Derive().
//
// Cross-campfire deps:
//   - Format: "acme.frontend.item-abc" (campfire.name.item-id)
//   - Always NON-BLOCKING: item stays actionable, warning is recorded
//   - When user is member of both campfires, status can be shown (via crossdep pkg)

import (
	"encoding/json"
	"strings"
	"testing"

	msgrec "github.com/3dl-dev/ready/pkg/msgrec"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestDerive_CrossCampfireRef_NonBlocking verifies that a work:block message
// with a cross-campfire blocker_id does NOT block the local item.
// Done condition 6: item A deps on B; user NOT a member → warning shown, A not blocked.
func TestDerive_CrossCampfireRef_NonBlocking(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		// Item A is in acme.backend (the local campfire for this test).
		makeMsg("msg-a01", []string{"work:create"}, map[string]interface{}{
			"id": "backend-a01", "title": "Item A", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Block message: blocker is a cross-campfire ref (acme.frontend.item-b01).
		makeMsg("msg-block-1", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "acme.frontend.item-b01",
			"blocked_id":  "backend-a01",
			"blocker_msg": "some-remote-msg",
			"blocked_msg": "msg-a01",
		}, []string{"msg-a01"}, ts+100),
	}

	items := state.Derive(testCampfire, msgs)

	a01 := items["backend-a01"]
	if a01 == nil {
		t.Fatal("backend-a01 not found")
	}

	// Cross-campfire dep is non-blocking: item must NOT be in blocked status.
	if a01.Status == state.StatusBlocked {
		t.Errorf("cross-campfire dep should not block: got status %q", a01.Status)
	}

	// Warning should be recorded.
	if len(a01.CrossCampfireWarnings) == 0 {
		t.Fatal("expected CrossCampfireWarnings to be non-empty")
	}

	// Warning should mention the cross-campfire ref.
	found := false
	for _, w := range a01.CrossCampfireWarnings {
		if strings.Contains(w, "acme.frontend.item-b01") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning mentioning 'acme.frontend.item-b01', got: %v", a01.CrossCampfireWarnings)
	}

	// BlockedBy should not contain the cross-campfire ref.
	for _, b := range a01.BlockedBy {
		if strings.Contains(b, "acme.frontend") {
			t.Errorf("BlockedBy should not contain cross-campfire ref, got: %v", a01.BlockedBy)
		}
	}
}

// TestDerive_CrossCampfireRef_LocalDepStillBlocks verifies that a local dep
// still blocks normally even when a cross-campfire dep is also present.
func TestDerive_CrossCampfireRef_LocalDepStillBlocks(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		makeMsg("msg-local-blocker", []string{"work:create"}, map[string]interface{}{
			"id": "backend-blocker", "title": "Local blocker", "type": "task",
			"for": "baron@3dl.dev", "priority": "p0",
		}, nil, ts),
		makeMsg("msg-a01", []string{"work:create"}, map[string]interface{}{
			"id": "backend-a01", "title": "Item A", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts+100),
		// Local block edge: local-blocker blocks a01.
		makeMsg("msg-block-local", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "backend-blocker",
			"blocked_id":  "backend-a01",
			"blocker_msg": "msg-local-blocker",
			"blocked_msg": "msg-a01",
		}, []string{"msg-local-blocker", "msg-a01"}, ts+200),
		// Cross-campfire block edge: external ref also references a01.
		makeMsg("msg-block-cross", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "acme.frontend.item-b01",
			"blocked_id":  "backend-a01",
			"blocker_msg": "some-remote-msg",
			"blocked_msg": "msg-a01",
		}, []string{"msg-a01"}, ts+300),
	}

	items := state.Derive(testCampfire, msgs)

	a01 := items["backend-a01"]
	if a01 == nil {
		t.Fatal("backend-a01 not found")
	}

	// Local dep IS blocking.
	if a01.Status != state.StatusBlocked {
		t.Errorf("expected a01 blocked by local dep, got %q", a01.Status)
	}

	// Cross-campfire warning should still be recorded.
	if len(a01.CrossCampfireWarnings) == 0 {
		t.Error("expected cross-campfire warning even when local dep is blocking")
	}
}

// TestIsCrossCampfireRef verifies the cross-campfire reference detection logic.
func TestIsCrossCampfireRef(t *testing.T) {
	cases := []struct {
		ref      string
		expected bool
	}{
		{"acme.frontend.item-abc", true},
		{"acme.backend.ready-123", true},
		{"org.proj.sub-item-x01", true},
		{"ready-abc", false},   // no dot — local item ID
		{"simple", false},      // no dot, no hyphen
		{"no-dot-here", false}, // hyphen but no dot
		{"a.b", false},         // has dot but item part has no hyphen in right place
		{"a.b-c", true},        // minimal valid cross-campfire ref
	}

	for _, tc := range cases {
		got := state.IsCrossCampfireRef(tc.ref)
		if got != tc.expected {
			t.Errorf("IsCrossCampfireRef(%q) = %v, want %v", tc.ref, got, tc.expected)
		}
	}
}

// TestParseCrossCampfireRef verifies parsing of cross-campfire references.
func TestParseCrossCampfireRef(t *testing.T) {
	ref := state.ParseCrossCampfireRef("acme.frontend.item-abc")
	if ref == nil {
		t.Fatal("expected non-nil result for valid cross-campfire ref")
	}
	if ref.CampfireName != "acme.frontend" {
		t.Errorf("CampfireName: got %q, want %q", ref.CampfireName, "acme.frontend")
	}
	if ref.ItemID != "item-abc" {
		t.Errorf("ItemID: got %q, want %q", ref.ItemID, "item-abc")
	}
	if ref.Raw != "acme.frontend.item-abc" {
		t.Errorf("Raw: got %q, want %q", ref.Raw, "acme.frontend.item-abc")
	}

	// Non-cross-campfire ref returns nil.
	if r := state.ParseCrossCampfireRef("ready-abc"); r != nil {
		t.Errorf("expected nil for local item ID, got %+v", r)
	}
}

// TestDerive_StrandedItemReclaim verifies that when a work:role-grant with
// role=revoked is processed, in-progress items claimed by that pubkey are
// flipped back to inbox (§4.5 stranded-item reclaim rule).
func TestDerive_StrandedItemReclaim(t *testing.T) {
	const campfireID = "aaaa0000bbbb1111cccc2222dddd3333eeee4444ffff5555aaaa0000bbbb1111"
	const claimerKey = "1234567890abcdef1234567890abcdef1234567890abcdef1234567890abcdef"

	msgs := []msgrec.MessageRecord{
		{
			ID: "m1", CampfireID: campfireID, Sender: "admin",
			Tags: []string{"work:create"},
			Payload: mustMarshal(map[string]interface{}{
				"id": "task-001", "title": "A task", "type": "task",
				"for": "team@example.com", "priority": "p1",
			}),
			Timestamp: 1000,
		},
		{
			ID: "m2", CampfireID: campfireID, Sender: claimerKey,
			Tags: []string{"work:claim"},
			Payload: mustMarshal(map[string]interface{}{
				"target": "m1",
			}),
			Antecedents: []string{"m1"},
			Timestamp:   2000,
		},
		{
			ID: "m3", CampfireID: campfireID, Sender: "admin",
			Tags: []string{"work:role-grant"},
			Payload: mustMarshal(map[string]interface{}{
				"pubkey":     claimerKey,
				"role":       "revoked",
				"granted_at": int64(3000),
			}),
			Antecedents: []string{"m2"},
			Timestamp:   3000,
		},
	}

	items := state.Derive(campfireID, msgs)

	task := items["task-001"]
	if task == nil {
		t.Fatal("task-001 not found")
	}

	// After revocation, the item must be reclaimed: status=inbox, by="".
	if task.Status != state.StatusInbox {
		t.Errorf("expected status %q after revocation reclaim, got %q", state.StatusInbox, task.Status)
	}
	if task.By != "" {
		t.Errorf("expected By='' after revocation reclaim, got %q", task.By)
	}
}

// mustMarshal marshals v to JSON or panics.
func mustMarshal(v interface{}) []byte {
	data, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return data
}

// TestDerive_StrandedItemReclaim_IgnoresUnclaimedActive verifies that Pass 3
// does NOT reclaim active items with empty By — only items explicitly claimed
// by the revoked pubkey should be reclaimed.
func TestDerive_StrandedItemReclaim_IgnoresUnclaimedActive(t *testing.T) {
	const campfireID = "bbbb0000cccc1111dddd2222eeee3333ffff4444aaaa5555bbbb0000cccc1111"
	const someKey = "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789"

	msgs := []msgrec.MessageRecord{
		{
			ID: "m1", CampfireID: campfireID, Sender: "admin",
			Tags: []string{"work:create"},
			Payload: mustMarshal(map[string]interface{}{
				"id": "task-002", "title": "Unclaimed active task", "type": "task",
				"for": "team@example.com", "priority": "p1",
			}),
			Timestamp: 1000,
		},
		// Set active via work:status without a claim — By remains empty.
		{
			ID: "m2", CampfireID: campfireID, Sender: "admin",
			Tags: []string{"work:status"},
			Payload: mustMarshal(map[string]interface{}{
				"target": "m1",
				"to":     "active",
			}),
			Antecedents: []string{"m1"},
			Timestamp:   1500,
		},
		// Revoke someKey — which never claimed this item.
		{
			ID: "m3", CampfireID: campfireID, Sender: "admin",
			Tags: []string{"work:role-grant"},
			Payload: mustMarshal(map[string]interface{}{
				"pubkey":     someKey,
				"role":       "revoked",
				"granted_at": int64(2000),
			}),
			Timestamp: 2000,
		},
	}

	items := state.Derive(campfireID, msgs)
	task := items["task-002"]
	if task == nil {
		t.Fatal("task-002 not found")
	}

	// Item has no claimer — Pass 3 must NOT touch it.
	if task.Status != state.StatusActive {
		t.Errorf("unclaimed active item should remain active after unrelated revocation, got %q", task.Status)
	}
	if task.By != "" {
		t.Errorf("By should remain empty, got %q", task.By)
	}
}

// TestDerive_RoleGrant_RFC3339GrantedAt verifies that role-grant messages with
// RFC3339 formatted granted_at strings are correctly parsed and used.
// This test ensures backward compatibility with RFC3339 timestamps sent by
// admit.go and revoke.go (ready-430).
func TestDerive_RoleGrant_RFC3339GrantedAt(t *testing.T) {
	const campfireID = "cccc0000dddd1111eeee2222ffff3333aaaa4444bbbb5555cccc0000dddd1111"
	const granteeKey = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"

	// RFC3339 timestamp: 2026-04-08T15:30:45Z
	grantedAt := "2026-04-08T15:30:45Z"

	msgs := []msgrec.MessageRecord{
		{
			ID: "m1", CampfireID: campfireID, Sender: "admin",
			Tags: []string{"work:create"},
			Payload: mustMarshal(map[string]interface{}{
				"id": "task-003", "title": "A task with grantee", "type": "task",
				"for": "team@example.com", "priority": "p1",
			}),
			Timestamp: 1000,
		},
		// Claim by grantee
		{
			ID: "m2", CampfireID: campfireID, Sender: granteeKey,
			Tags: []string{"work:claim"},
			Payload: mustMarshal(map[string]interface{}{
				"target": "m1",
			}),
			Antecedents: []string{"m1"},
			Timestamp:   2000,
		},
		// Grant role with RFC3339 timestamp
		{
			ID: "m3", CampfireID: campfireID, Sender: "admin",
			Tags: []string{"work:role-grant"},
			Payload: mustMarshal(map[string]interface{}{
				"pubkey":     granteeKey,
				"role":       "member",
				"granted_at": grantedAt, // RFC3339 string, not int64
			}),
			Timestamp: 3000,
		},
		// Later, revoke using RFC3339 timestamp
		{
			ID: "m4", CampfireID: campfireID, Sender: "admin",
			Tags: []string{"work:role-grant"},
			Payload: mustMarshal(map[string]interface{}{
				"pubkey":     granteeKey,
				"role":       "revoked",
				"granted_at": "2026-04-08T16:00:00Z", // Another RFC3339 string
			}),
			Timestamp: 4000,
		},
	}

	items := state.Derive(campfireID, msgs)
	task := items["task-003"]
	if task == nil {
		t.Fatal("task-003 not found")
	}

	// The revocation (m4) with role=revoked triggers Pass 3: stranded-item reclaim.
	// Since the item is active and claimed by granteeKey, it should be moved to inbox.
	// This verifies that the RFC3339 timestamps were correctly parsed.
	if task.Status != state.StatusInbox {
		t.Errorf("expected status %q after revocation, got %q", state.StatusInbox, task.Status)
	}
	if task.By != "" {
		t.Errorf("expected By to be empty after revocation, got %q", task.By)
	}
}

// TestDerive_CrossCampfireRef_BlockedIsCross verifies that when the blocked item
// is a cross-campfire reference (and blocker is local), the warning is attached
// to the correct local item with the correct cross-campfire ref.
func TestDerive_CrossCampfireRef_BlockedIsCross(t *testing.T) {
	ts := now()
	msgs := []msgrec.MessageRecord{
		// Item A is local in acme.backend.
		makeMsg("msg-a01", []string{"work:create"}, map[string]interface{}{
			"id": "backend-a01", "title": "Item A", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Block message: blocker is local (backend-a01), blocked is cross-campfire ref.
		// This is an unusual but valid case: a local item is explicitly blocking a
		// remote item (e.g., "our work must complete before they can finish their task").
		makeMsg("msg-block-1", []string{"work:block"}, map[string]interface{}{
			"blocker_id":  "backend-a01",
			"blocked_id":  "acme.frontend.item-b01",
			"blocker_msg": "msg-a01",
			"blocked_msg": "some-remote-msg",
		}, []string{"msg-a01"}, ts+100),
	}

	items := state.Derive(testCampfire, msgs)

	a01 := items["backend-a01"]
	if a01 == nil {
		t.Fatal("backend-a01 not found")
	}

	// The blocker (a01) should NOT be blocked by the cross-campfire blocked reference.
	if a01.Status == state.StatusBlocked {
		t.Errorf("blocker should not be blocked when blocking a cross-campfire item, got %q", a01.Status)
	}

	// Warning should be recorded on the blocker (a01), mentioning the blocked cross-campfire ref.
	if len(a01.CrossCampfireWarnings) == 0 {
		t.Fatal("expected CrossCampfireWarnings on blocker when blocked_id is cross-campfire ref")
	}

	// Warning should mention the blocked cross-campfire ref (acme.frontend.item-b01).
	found := false
	var actualWarning string
	for _, w := range a01.CrossCampfireWarnings {
		actualWarning = w
		if strings.Contains(w, "acme.frontend.item-b01") {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected warning mentioning 'acme.frontend.item-b01', got: %q", actualWarning)
	}

	// Explicitly verify the warning does NOT mention backend-a01 (the blocker's ID)
	// when the cross-ref is shown. The warning should reference only the cross-campfire dep.
	for _, w := range a01.CrossCampfireWarnings {
		// Extract just the ref part of the warning message format:
		// "unresolved cross-campfire dep: <ref> (not a member — non-blocking)"
		if !strings.Contains(w, "acme.frontend.item-b01") && strings.Contains(w, "backend-a01") {
			t.Errorf("warning references wrong item ID: %s", w)
		}
	}
}
