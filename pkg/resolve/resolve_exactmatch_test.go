package resolve_test

import (
	"testing"

	"github.com/campfire-net/ready/pkg/resolve"
)

// TestByIDFromJSONLExact_PrefixRejected verifies ByIDFromJSONLExact rejects prefixes.
//
// Security regression for ready-afa5: the admit operation uses the exact-only
// resolver to prevent an attacker's item from being selected via a crafted
// prefix collision. (The former store-backed ByIDExact test was deleted with the
// campfire store read path in the nostr-native cutover, ready-cb6; this JSONL
// path is the surviving read spine and carries the same security guarantee.)
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
