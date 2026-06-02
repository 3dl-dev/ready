package main

import (
	"encoding/hex"
	"strings"
	"testing"
	"time"

	trust "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
)

const (
	testChildHex   = "1111111111111111111111111111111111111111111111111111111111111111"
	testGranterHex = "2222222222222222222222222222222222222222222222222222222222222222"
)

// TestBuildDelegationGrant_Shape verifies admit builds an owner-root (depth-0)
// cf-authority grant in exactly the shape DefaultGateEvaluator accepts: child +
// granter keys, no parent, a "work" capability scoped by role, a future expiry,
// and a 16-byte nonce.
func TestBuildDelegationGrant_Shape(t *testing.T) {
	before := time.Now().UnixNano()
	cbor, err := buildDelegationGrant(testChildHex, testGranterHex, "member", delegationGrantTTL)
	if err != nil {
		t.Fatalf("buildDelegationGrant: %v", err)
	}

	gp, err := trust.UnmarshalGrantPayloadCBOR(cbor)
	if err != nil {
		t.Fatalf("UnmarshalGrantPayloadCBOR: %v", err)
	}

	wantChild, _ := hex.DecodeString(testChildHex)
	wantGranter, _ := hex.DecodeString(testGranterHex)
	if hex.EncodeToString(gp.ChildPubkey) != testChildHex {
		t.Errorf("ChildPubkey: got %x, want %s", gp.ChildPubkey, testChildHex)
	}
	if hex.EncodeToString(gp.GranterPubKey) != testGranterHex {
		t.Errorf("GranterPubKey: got %x, want %s", gp.GranterPubKey, testGranterHex)
	}
	if len(wantChild) != 32 || len(wantGranter) != 32 {
		t.Fatalf("test key fixtures must be 32 bytes")
	}
	if gp.Depth != 0 {
		t.Errorf("Depth: got %d, want 0 (owner-root)", gp.Depth)
	}
	if gp.ParentGrantID != nil {
		t.Errorf("ParentGrantID: got %x, want nil (owner-root)", gp.ParentGrantID)
	}
	if len(gp.Capabilities) != 1 {
		t.Fatalf("Capabilities: got %d, want 1", len(gp.Capabilities))
	}
	cap := gp.Capabilities[0]
	if cap.Convention != "work" {
		t.Errorf("Capability.Convention: got %q, want work", cap.Convention)
	}
	if cap.OpPattern != workOpPatternForRole("member") {
		t.Errorf("Capability.OpPattern: got %q, want %q", cap.OpPattern, workOpPatternForRole("member"))
	}
	if cap.Until <= before {
		t.Errorf("Capability.Until: got %d, want > %d (future)", cap.Until, before)
	}
	if len(cap.Nonce) != 16 {
		t.Errorf("Capability.Nonce: got %d bytes, want 16", len(cap.Nonce))
	}
}

// TestWorkOpPatternForRole verifies maintainers get every op and everyone else
// gets the contributor-level subset.
func TestWorkOpPatternForRole(t *testing.T) {
	if got := workOpPatternForRole("maintainer"); got != "*" {
		t.Errorf("maintainer: got %q, want *", got)
	}
	for _, role := range []string{"member", "agent", "org-observer", ""} {
		got := workOpPatternForRole(role)
		if got == "*" || !strings.Contains(got, "create") || strings.Contains(got, "close") {
			t.Errorf("role %q: got %q — want contributor subset (no close, includes create)", role, got)
		}
	}
}

// TestBuildDelegationGrant_InvalidKeys rejects non-32-byte hex keys.
func TestBuildDelegationGrant_InvalidKeys(t *testing.T) {
	if _, err := buildDelegationGrant("zz", testGranterHex, "member", delegationGrantTTL); err == nil {
		t.Error("expected error for malformed child hex")
	}
	if _, err := buildDelegationGrant(testChildHex, "1234", "member", delegationGrantTTL); err == nil {
		t.Error("expected error for short granter hex")
	}
}

// TestPostDelegationGrant_PostsDelegationTag verifies postDelegationGrant sends a
// message tagged delegation:grant carrying a decodable grant for the child key.
func TestPostDelegationGrant_PostsDelegationTag(t *testing.T) {
	fake := &fakeAdmitClient{pubKeyHex: testGranterHex}
	id, err := postDelegationGrant(fake, "cafe", testChildHex, "member")
	if err != nil {
		t.Fatalf("postDelegationGrant: %v", err)
	}
	if id == "" {
		t.Error("expected a non-empty grant message ID")
	}
	if len(fake.sendCalls) != 1 {
		t.Fatalf("expected exactly 1 Send, got %d", len(fake.sendCalls))
	}
	sent := fake.sendCalls[0]
	if len(sent.Tags) != 1 || sent.Tags[0] != delegationGrantTag {
		t.Errorf("Send tags: got %v, want [%s]", sent.Tags, delegationGrantTag)
	}
	gp, err := trust.UnmarshalGrantPayloadCBOR(sent.Payload)
	if err != nil {
		t.Fatalf("sent payload is not a valid grant: %v", err)
	}
	if hex.EncodeToString(gp.ChildPubkey) != testChildHex {
		t.Errorf("sent grant ChildPubkey: got %x, want %s", gp.ChildPubkey, testChildHex)
	}
}
