package main

import (
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// fakeScopeClient implements scopeClient with canned membership + grant reads.
type fakeScopeClient struct {
	creator string
	grants  []protocol.Message
	revokes []protocol.Message
}

func (f *fakeScopeClient) GetMembership(string) (*store.Membership, error) {
	return &store.Membership{CreatorPubkey: f.creator}, nil
}

func (f *fakeScopeClient) Read(req protocol.ReadRequest) (*protocol.ReadResult, error) {
	for _, tag := range req.Tags {
		switch tag {
		case delegationGrantTag:
			return &protocol.ReadResult{Messages: f.grants}, nil
		case identityRevokedTag:
			return &protocol.ReadResult{Messages: f.revokes}, nil
		}
	}
	return &protocol.ReadResult{}, nil
}

func TestScopeForKey_OwnerAllowed(t *testing.T) {
	c := &fakeScopeClient{creator: testChildHex}
	ok, _ := scopeForKey(c, "cafe", testChildHex)
	if !ok {
		t.Error("creator (root principal) should be allowed")
	}
}

func TestScopeForKey_ActiveGrantCoveringClaimAllowed(t *testing.T) {
	c := &fakeScopeClient{grants: []protocol.Message{grantMessage(t, testChildHex, time.Hour, "g1")}}
	ok, note := scopeForKey(c, "cafe", testChildHex)
	if !ok {
		t.Errorf("member grant covers claim, should be allowed; note=%q", note)
	}
}

func TestScopeForKey_RevokedDenied(t *testing.T) {
	c := &fakeScopeClient{
		grants:  []protocol.Message{grantMessage(t, testChildHex, time.Hour, "g1")},
		revokes: []protocol.Message{revokeMessage(testChildHex, "r1")},
	}
	if ok, _ := scopeForKey(c, "cafe", testChildHex); ok {
		t.Error("revoked grant-holder should be denied")
	}
}

func TestScopeForKey_NoGrantDenied(t *testing.T) {
	c := &fakeScopeClient{}
	if ok, note := scopeForKey(c, "cafe", testChildHex); ok || note == "" {
		t.Errorf("key with no grant should be denied with a note; ok=%v note=%q", ok, note)
	}
}

func TestOpPatternCovers(t *testing.T) {
	if !opPatternCovers("*", "close") {
		t.Error("* should cover any op")
	}
	if !opPatternCovers("create|claim|update", "claim") {
		t.Error("pipe pattern should cover claim")
	}
	if opPatternCovers("create|claim", "close") {
		t.Error("pattern without close should not cover close")
	}
}
