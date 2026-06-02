package main

import (
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

func TestAuthorityResolver_Label(t *testing.T) {
	now := time.Now()
	grants := []protocol.Message{grantMessage(t, testChildHex, time.Hour, "g1")}
	revokes := []protocol.Message{}
	r := newAuthorityResolver(testGranterHex, grants, revokes, now)

	// Owner (creator == granterHex) → root principal.
	if got := r.label(testGranterHex); got != "owner (root principal)" {
		t.Errorf("owner label: got %q", got)
	}
	// Active grant-holder → convention:op-pattern.
	want := "work:" + workOpPatternForRole("member")
	if got := r.label(testChildHex); got != want {
		t.Errorf("granted label: got %q, want %q", got, want)
	}
	// Non-pubkey actor → empty (silent).
	if got := r.label("system"); got != "" {
		t.Errorf("non-pubkey actor: got %q, want empty", got)
	}
	// Unknown pubkey (no grant) → no delegation grant.
	unknown := "3333333333333333333333333333333333333333333333333333333333333333"
	if got := r.label(unknown); got != "no delegation grant" {
		t.Errorf("ungranted key: got %q", got)
	}
}

func TestAuthorityResolver_RevokedAndExpired(t *testing.T) {
	now := time.Now()
	// Revoked.
	r := newAuthorityResolver("", []protocol.Message{grantMessage(t, testChildHex, time.Hour, "g1")},
		[]protocol.Message{revokeMessage(testChildHex, "r1")}, now)
	if got := r.label(testChildHex); got != "revoked" {
		t.Errorf("revoked label: got %q", got)
	}
	// Expired.
	r2 := newAuthorityResolver("", []protocol.Message{grantMessage(t, testChildHex, -time.Hour, "g1")}, nil, now)
	if got := r2.label(testChildHex); got != "grant expired" {
		t.Errorf("expired label: got %q", got)
	}
}
