package resolve_test

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/resolve"
)

const testCampfire = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

// openTempStore creates a temporary SQLite store for testing.
func openTempStore(t *testing.T) store.Store {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("opening temp store: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// addTestItem adds a work:create message for the given item ID to the store.
func addTestItem(t *testing.T, s store.Store, campfireID, msgID, itemID, title string) {
	t.Helper()
	payload, _ := json.Marshal(map[string]interface{}{
		"id":       itemID,
		"title":    title,
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p1",
	})
	ts := time.Now().UnixNano()
	_, err := s.AddMessage(store.MessageRecord{
		ID:         msgID,
		CampfireID: campfireID,
		Sender:     "testkey",
		Payload:    payload,
		Tags:       []string{"work:create"},
		Timestamp:  ts,
		Signature:  []byte("fakesig-" + msgID), // non-nil: required by NOT NULL constraint
		ReceivedAt: ts,
	})
	if err != nil {
		t.Fatalf("adding message: %v", err)
	}
}

// addMembership registers a campfire membership in the store.
func addMembership(t *testing.T, s store.Store, campfireID string) {
	t.Helper()
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "invite",
		Role:         "full",
		JoinedAt:     time.Now().Unix(),
	}); err != nil {
		// Ignore duplicate errors.
		_ = err
	}
}

// TestByID_ExactMatchBeforePrefix verifies that when itemID="ready-a1b" and
// both "ready-a1b" and "ready-a1bc" exist, ByID returns "ready-a1b" exactly
// rather than returning ErrAmbiguous.
// This is the bug fix: without exact-first logic, HasPrefix matches both IDs,
// causing ErrAmbiguous even though an exact match exists.
func TestByID_ExactMatchBeforePrefix(t *testing.T) {
	s := openTempStore(t)
	addMembership(t, s, testCampfire)

	// Add two items where one ID is a prefix of the other.
	addTestItem(t, s, testCampfire, "msg-a1b", "ready-a1b", "Short ID")
	addTestItem(t, s, testCampfire, "msg-a1bc", "ready-a1bc", "Longer ID")

	// Resolve the exact ID "ready-a1b" — should not be ambiguous.
	item, err := resolve.ByID(s, "ready-a1b")
	if err != nil {
		t.Fatalf("expected exact match for 'ready-a1b', got error: %v", err)
	}
	if item.ID != "ready-a1b" {
		t.Errorf("expected ID=ready-a1b, got %q", item.ID)
	}
	if item.Title != "Short ID" {
		t.Errorf("expected title='Short ID', got %q", item.Title)
	}
}

// TestByID_PrefixMatchWhenNoExact verifies that prefix matching still works
// when there is no exact match — e.g., "ready-a1" matches "ready-a1b" uniquely.
func TestByID_PrefixMatchWhenNoExact(t *testing.T) {
	s := openTempStore(t)
	addMembership(t, s, testCampfire)

	addTestItem(t, s, testCampfire, "msg-a1b2", "ready-a1b2", "Unique item")

	item, err := resolve.ByID(s, "ready-a1b")
	if err != nil {
		t.Fatalf("expected prefix match for 'ready-a1b', got error: %v", err)
	}
	if item.ID != "ready-a1b2" {
		t.Errorf("expected ID=ready-a1b2, got %q", item.ID)
	}
}

// TestByID_AmbiguousPrefix verifies that ErrAmbiguous is returned when a
// prefix matches multiple items but no exact match exists.
func TestByID_AmbiguousPrefix(t *testing.T) {
	s := openTempStore(t)
	addMembership(t, s, testCampfire)

	addTestItem(t, s, testCampfire, "msg-x1", "ready-x1a", "Item A")
	addTestItem(t, s, testCampfire, "msg-x2", "ready-x1b", "Item B")

	_, err := resolve.ByID(s, "ready-x1")
	if err == nil {
		t.Fatal("expected ErrAmbiguous, got nil")
	}
	if _, ok := err.(resolve.ErrAmbiguous); !ok {
		t.Errorf("expected ErrAmbiguous, got %T: %v", err, err)
	}
}

// TestByID_NotFound verifies that ErrNotFound is returned for unknown IDs.
func TestByID_NotFound(t *testing.T) {
	s := openTempStore(t)
	addMembership(t, s, testCampfire)

	_, err := resolve.ByID(s, "ready-zzz")
	if err == nil {
		t.Fatal("expected ErrNotFound, got nil")
	}
	if _, ok := err.(resolve.ErrNotFound); !ok {
		t.Errorf("expected ErrNotFound, got %T: %v", err, err)
	}
}

// TestByIDExact_PrefixRejected verifies that ByIDExact does not expand prefixes —
// a prefix that would match via ByID (prefix fallback) must return ErrNotFound.
//
// Security regression for ready-afa5: the admit operation uses ByIDExact to
// prevent an attacker's item from being selected via a crafted prefix collision.
func TestByIDExact_PrefixRejected(t *testing.T) {
	s := openTempStore(t)
	addMembership(t, s, testCampfire)

	// Item whose ID would be matched by prefix "ready-a1b" via ByID.
	addTestItem(t, s, testCampfire, "msg-a1bx", "ready-a1bxyz", "Attacker item")

	// ByID (prefix-allowing) would match via prefix — prerequisite check.
	item, err := resolve.ByID(s, "ready-a1b")
	if err != nil || item == nil {
		t.Fatalf("prerequisite: ByID should match prefix 'ready-a1b', got err=%v item=%v", err, item)
	}

	// ByIDExact must NOT match — the prefix is not an exact ID.
	_, err = resolve.ByIDExact(s, "ready-a1b")
	if err == nil {
		t.Fatal("ByIDExact must reject prefix 'ready-a1b' with ErrNotFound, got nil")
	}
	if _, ok := err.(resolve.ErrNotFound); !ok {
		t.Errorf("expected resolve.ErrNotFound, got %T: %v", err, err)
	}
}

// TestByIDExact_FullIDAccepted verifies that ByIDExact resolves a full exact ID.
func TestByIDExact_FullIDAccepted(t *testing.T) {
	s := openTempStore(t)
	addMembership(t, s, testCampfire)

	addTestItem(t, s, testCampfire, "msg-full", "ready-fullid", "Full ID item")
	// A second item that shares a prefix — must not interfere.
	addTestItem(t, s, testCampfire, "msg-full2", "ready-fullidx", "Other item")

	item, err := resolve.ByIDExact(s, "ready-fullid")
	if err != nil {
		t.Fatalf("ByIDExact full ID: unexpected error: %v", err)
	}
	if item.ID != "ready-fullid" {
		t.Errorf("item.ID = %q, want ready-fullid", item.ID)
	}
}

// TestByIDFromJSONLExact_PrefixRejected verifies ByIDFromJSONLExact rejects prefixes.
//
// Security regression for ready-afa5: parallel of TestByIDExact_PrefixRejected
// but for the JSONL code path used when a mutations.jsonl is present.
func TestByIDFromJSONLExact_PrefixRejected(t *testing.T) {
	ts := int64(1700000000000000000)
	// writeJSONL and createMut are defined in resolve_jsonl_test.go (same package).
	path := writeJSONL(t, []mutJSON{
		createMut("msg-pfxlong", "ready-pfxlong", "Prefix-only match item", ts),
	})

	// ByIDFromJSONL (prefix-allowing) would match "ready-pfx" — prerequisite check.
	item, err := resolve.ByIDFromJSONL(path, jsonlResolveCampfire, "ready-pfx")
	if err != nil || item == nil {
		t.Fatalf("prerequisite: ByIDFromJSONL should match prefix 'ready-pfx', got err=%v item=%v", err, item)
	}

	// ByIDFromJSONLExact must NOT match — the prefix is not an exact ID.
	_, err = resolve.ByIDFromJSONLExact(path, jsonlResolveCampfire, "ready-pfx")
	if err == nil {
		t.Fatal("ByIDFromJSONLExact must reject prefix 'ready-pfx' with ErrNotFound, got nil")
	}
	if _, ok := err.(resolve.ErrNotFound); !ok {
		t.Errorf("expected resolve.ErrNotFound, got %T: %v", err, err)
	}
}

// TestByIDFromJSONLExact_FullIDAccepted verifies ByIDFromJSONLExact resolves full IDs.
func TestByIDFromJSONLExact_FullIDAccepted(t *testing.T) {
	ts := int64(1700000000000000001)
	path := writeJSONL(t, []mutJSON{
		createMut("msg-exact1", "ready-exact1", "Exact ID item", ts),
		createMut("msg-exact1x", "ready-exact1x", "Similar prefix item", ts+1),
	})

	item, err := resolve.ByIDFromJSONLExact(path, jsonlResolveCampfire, "ready-exact1")
	if err != nil {
		t.Fatalf("ByIDFromJSONLExact full ID: unexpected error: %v", err)
	}
	if item.ID != "ready-exact1" {
		t.Errorf("item.ID = %q, want ready-exact1", item.ID)
	}
}
