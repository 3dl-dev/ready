package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

func grantMessage(t *testing.T, childHex string, ttl time.Duration, id string) protocol.Message {
	t.Helper()
	cbor, err := buildDelegationGrant(childHex, testGranterHex, "member", ttl)
	if err != nil {
		t.Fatalf("buildDelegationGrant: %v", err)
	}
	return protocol.Message{ID: id, Payload: cbor}
}

func revokeMessage(childHex, id string) protocol.Message {
	p, _ := json.Marshal(map[string]string{"child_pubkey": childHex})
	return protocol.Message{ID: id, Payload: p}
}

func TestActiveGrantHolders_ActiveIncluded(t *testing.T) {
	now := time.Now()
	holders := activeGrantHolders([]protocol.Message{grantMessage(t, testChildHex, time.Hour, "g1")}, nil, now)
	if len(holders) != 1 {
		t.Fatalf("got %d holders, want 1", len(holders))
	}
	h := holders[0]
	if h.Pubkey != testChildHex {
		t.Errorf("Pubkey: got %s, want %s", h.Pubkey, testChildHex)
	}
	if h.Convention != "work" {
		t.Errorf("Convention: got %q, want work", h.Convention)
	}
	if h.OpPattern != workOpPatternForRole("member") {
		t.Errorf("OpPattern: got %q, want %q", h.OpPattern, workOpPatternForRole("member"))
	}
	if h.GrantMsgID != "g1" {
		t.Errorf("GrantMsgID: got %q, want g1", h.GrantMsgID)
	}
	if h.TTLRemaining == "" {
		t.Error("TTLRemaining should be set for an active grant")
	}
}

func TestActiveGrantHolders_RevokedExcluded(t *testing.T) {
	now := time.Now()
	grants := []protocol.Message{grantMessage(t, testChildHex, time.Hour, "g1")}
	revokes := []protocol.Message{revokeMessage(testChildHex, "r1")}
	if got := activeGrantHolders(grants, revokes, now); len(got) != 0 {
		t.Errorf("revoked holder should be excluded, got %d", len(got))
	}
}

func TestActiveGrantHolders_ExpiredExcluded(t *testing.T) {
	now := time.Now()
	grants := []protocol.Message{grantMessage(t, testChildHex, -time.Hour, "g1")}
	if got := activeGrantHolders(grants, nil, now); len(got) != 0 {
		t.Errorf("expired holder should be excluded, got %d", len(got))
	}
}

func TestActiveGrantHolders_LatestPerKeyWins(t *testing.T) {
	now := time.Now()
	// Same key: an earlier expired grant superseded by a later active one.
	grants := []protocol.Message{
		grantMessage(t, testChildHex, -time.Hour, "g1"),
		grantMessage(t, testChildHex, time.Hour, "g2"),
	}
	holders := activeGrantHolders(grants, nil, now)
	if len(holders) != 1 || holders[0].GrantMsgID != "g2" {
		t.Errorf("latest grant should win: got %+v", holders)
	}
}
