package provenance_test

import (
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/provenance"
	"github.com/campfire-net/ready/pkg/storetest"
)

const (
	testCampfire  = storetest.DefaultCampfire
	creatorKey    = "aaaa0000000000000000000000000000000000000000000000000000000000000000"
	contributorA  = "bbbb0000000000000000000000000000000000000000000000000000000000000000"
	contributorB  = "cccc0000000000000000000000000000000000000000000000000000000000000000"
	unknownKey    = "dddd0000000000000000000000000000000000000000000000000000000000000000"
)

// roleGrantPayload mirrors the work:role-grant convention payload.
type roleGrantPayload struct {
	Pubkey    string `json:"pubkey"`
	Role      string `json:"role"`
	GrantedAt string `json:"granted_at"`
}

// addRoleGrant inserts a work:role-grant message into the store.
// ts controls ordering (later = higher priority); use distinct values per test.
func addRoleGrant(t *testing.T, s store.Store, sender, pubkey, role string, tsOffset int64) {
	t.Helper()
	payload, err := json.Marshal(roleGrantPayload{
		Pubkey:    pubkey,
		Role:      role,
		GrantedAt: "2026-01-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("marshal role grant: %v", err)
	}
	msgID := fmt.Sprintf("%s-%s-%d", pubkey[:8], role, tsOffset)
	ts := time.Now().UnixNano() + tsOffset
	ok, err := s.AddMessage(store.MessageRecord{
		ID:         msgID,
		CampfireID: testCampfire,
		Sender:     sender,
		Payload:    payload,
		Tags:       []string{"work:role-grant"},
		Timestamp:  ts,
		ReceivedAt: ts,
		Signature:  []byte("fakesig-" + msgID),
	})
	if err != nil {
		t.Fatalf("addRoleGrant: AddMessage error: %v", err)
	}
	if !ok {
		t.Fatalf("addRoleGrant: AddMessage returned ok=false (message was not inserted)")
	}
}

// TestCreatorIsLevel2ByDefault verifies that the campfire creator receives
// level 2 without any explicit role-grant message.
func TestCreatorIsLevel2ByDefault(t *testing.T) {
	h := storetest.New(t)
	checker, err := provenance.NewStoreChecker(h.Store, testCampfire, creatorKey)
	if err != nil {
		t.Fatalf("NewStoreChecker: %v", err)
	}
	if got := checker.Level(creatorKey); got != 2 {
		t.Errorf("creator level = %d, want 2", got)
	}
}

// TestContributorIsLevel1ByDefault verifies that a key with no role-grant
// message and not the creator defaults to level 1.
func TestContributorIsLevel1ByDefault(t *testing.T) {
	h := storetest.New(t)
	checker, err := provenance.NewStoreChecker(h.Store, testCampfire, creatorKey)
	if err != nil {
		t.Fatalf("NewStoreChecker: %v", err)
	}
	if got := checker.Level(contributorA); got != 1 {
		t.Errorf("contributor level = %d, want 1", got)
	}
	if got := checker.Level(unknownKey); got != 1 {
		t.Errorf("unknown key level = %d, want 1", got)
	}
}

// TestRoleGrantElevatesKey verifies that a work:role-grant message with
// role="maintainer" elevates the target key to level 2.
func TestRoleGrantElevatesKey(t *testing.T) {
	h := storetest.New(t)
	addRoleGrant(t, h.Store, creatorKey, contributorA, "maintainer", 1000)

	checker, err := provenance.NewStoreChecker(h.Store, testCampfire, creatorKey)
	if err != nil {
		t.Fatalf("NewStoreChecker: %v", err)
	}
	if got := checker.Level(contributorA); got != 2 {
		t.Errorf("elevated key level = %d, want 2", got)
	}
}

// TestRoleGrantRevokesSetsLevel0 verifies that a work:role-grant message
// with role="revoked" sets the target key to level 0.
func TestRoleGrantRevokesSetsLevel0(t *testing.T) {
	h := storetest.New(t)
	addRoleGrant(t, h.Store, creatorKey, contributorB, "revoked", 1000)

	checker, err := provenance.NewStoreChecker(h.Store, testCampfire, creatorKey)
	if err != nil {
		t.Fatalf("NewStoreChecker: %v", err)
	}
	if got := checker.Level(contributorB); got != 0 {
		t.Errorf("revoked key level = %d, want 0", got)
	}
}

// TestRoleGrantCreatorCanBeRevoked verifies that even the campfire creator
// can be revoked by an explicit work:role-grant with role="revoked".
func TestRoleGrantCreatorCanBeRevoked(t *testing.T) {
	h := storetest.New(t)
	// Someone grants a revoke for the creator (unusual but possible).
	addRoleGrant(t, h.Store, contributorA, creatorKey, "revoked", 1000)

	checker, err := provenance.NewStoreChecker(h.Store, testCampfire, creatorKey)
	if err != nil {
		t.Fatalf("NewStoreChecker: %v", err)
	}
	if got := checker.Level(creatorKey); got != 0 {
		t.Errorf("revoked creator level = %d, want 0", got)
	}
}

// TestLatestRoleGrantWins verifies that when multiple role-grant messages
// exist for the same key, the latest one (by timestamp) determines the level.
func TestLatestRoleGrantWins(t *testing.T) {
	h := storetest.New(t)
	// First grant: contributor → maintainer.
	addRoleGrant(t, h.Store, creatorKey, contributorA, "maintainer", 1000)
	// Later grant: revoke.
	addRoleGrant(t, h.Store, creatorKey, contributorA, "revoked", 2000)

	checker, err := provenance.NewStoreChecker(h.Store, testCampfire, creatorKey)
	if err != nil {
		t.Fatalf("NewStoreChecker: %v", err)
	}
	// Latest grant (revoked) must win.
	if got := checker.Level(contributorA); got != 0 {
		t.Errorf("level after revoke = %d, want 0", got)
	}
}
