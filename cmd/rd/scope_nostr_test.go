package main

// scope_nostr_test.go — fail-closed regression guard for nostrScopeForKey, the
// rewritten `rd ready --scope` authorization DECISION function (ready-cb6
// veracity fix). It was 0% covered; fail-closed was proven only by a one-off
// manual probe, never by CI. This makes fail-closed a CI GUARANTEE.
//
// Every grant here is a REAL owner-signed kind-39301 role-grant published via the
// production `rd grant`/`rd revoke` primitive (runNostrGrantRevoke) into the local
// authoritative log; nostrScopeForKey re-derives authority from that signed log via
// loadNostrAuthorityResolver -> DeriveLevels. Assertions check the derived DECISION
// (allowed + note), never err==nil.
//
// Coverage matrix mirrors the DELETED scope_test.go on the nostr path:
//   - owner (root principal)      -> ALLOWED (sees all)
//   - granted contributor         -> ALLOWED
//   - granted maintainer          -> ALLOWED
//   - revoked key                 -> DENIED (fail-closed): the unified
//                                     "publish role=revoked" drops it to level 0
//   - ungranted foreign key       -> DENIED (fail-closed): not a granted identity

import (
	"strings"
	"testing"

	"github.com/campfire-net/ready/pkg/nostr"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// freshKeyHex returns a brand-new secp256k1 pubkey hex for use as a grantee.
func freshKeyHex(t *testing.T) string {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k.PubKeyHex()
}

// TestNostrScopeForKey_OwnerAllowed proves the board owner (root principal) is
// always authorized for --scope, without any grant.
func TestNostrScopeForKey_OwnerAllowed(t *testing.T) {
	_, owner := setupNostrNativeProject(t)

	allowed, note := nostrScopeForKey(owner)
	if !allowed {
		t.Fatalf("owner (root principal) must be allowed for --scope, got denied: %q", note)
	}
	if note != "" {
		t.Fatalf("owner allow should carry no note, got %q", note)
	}
	assertNoDotCf(t)
}

// TestNostrScopeForKey_GrantedContributorAllowed proves a key holding a live
// contributor grant is authorized.
func TestNostrScopeForKey_GrantedContributorAllowed(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	contrib := freshKeyHex(t)
	if err := runNostrGrantRevoke(dir, contrib, rdSync.RoleContributor, "agent-contrib", 0, ""); err != nil {
		t.Fatalf("grant contributor: %v", err)
	}

	allowed, note := nostrScopeForKey(contrib)
	if !allowed {
		t.Fatalf("granted contributor must be allowed, got denied: %q", note)
	}
	assertNoDotCf(t)
}

// TestNostrScopeForKey_GrantedMaintainerAllowed proves a key holding a live
// maintainer grant is authorized.
func TestNostrScopeForKey_GrantedMaintainerAllowed(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	maint := freshKeyHex(t)
	if err := runNostrGrantRevoke(dir, maint, rdSync.RoleMaintainer, "agent-maint", 0, ""); err != nil {
		t.Fatalf("grant maintainer: %v", err)
	}

	allowed, note := nostrScopeForKey(maint)
	if !allowed {
		t.Fatalf("granted maintainer must be allowed, got denied: %q", note)
	}
	assertNoDotCf(t)
}

// TestNostrScopeForKey_RevokedDenied is the load-bearing fail-closed case: a key
// that was granted then REVOKED (unified publish-role=revoked) must be DENIED.
func TestNostrScopeForKey_RevokedDenied(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	revoked := freshKeyHex(t)
	if err := runNostrGrantRevoke(dir, revoked, rdSync.RoleContributor, "agent-x", 0, ""); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// Precondition: the grant makes it allowed.
	if allowed, note := nostrScopeForKey(revoked); !allowed {
		t.Fatalf("precondition: contributor should be allowed before revoke, got denied: %q", note)
	}
	// Now revoke via the same primitive `rd kill`/`rd revoke` uses.
	if err := runNostrGrantRevoke(dir, revoked, rdSync.RoleRevoked, "", 0, ""); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	allowed, note := nostrScopeForKey(revoked)
	if allowed {
		t.Fatalf("FAIL-CLOSED VIOLATED: revoked key is still allowed for --scope")
	}
	if !strings.Contains(note, "revoked") {
		t.Fatalf("revoked denial note = %q, want it to mention 'revoked'", note)
	}
	assertNoDotCf(t)
}

// TestNostrScopeForKey_UngrantedDenied is the second fail-closed case: a foreign
// key that was NEVER granted must be DENIED with a "not a granted identity" note.
func TestNostrScopeForKey_UngrantedDenied(t *testing.T) {
	setupNostrNativeProject(t)
	foreign := freshKeyHex(t)

	allowed, note := nostrScopeForKey(foreign)
	if allowed {
		t.Fatalf("FAIL-CLOSED VIOLATED: ungranted foreign key is allowed for --scope")
	}
	if !strings.Contains(note, "not a granted identity") {
		t.Fatalf("ungranted denial note = %q, want it to mention 'not a granted identity'", note)
	}
	assertNoDotCf(t)
}
