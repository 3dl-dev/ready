// Deterministic unit tests for ready-84e / BP-5: the relay write-allowlist
// regenerated from signed role-grants, and the no-lockout reconciliation plan.
//
// Every grant here is REAL (built + schnorr-signed via BuildRoleGrantEvent, the
// same helper BP-2 uses) so the derivation runs the actual Verify path. The
// assertions check the DERIVED admitted set, that a revoke prunes the key, and —
// the safety property that matters for the LIVE locked relays — that PlanAllowlist
// never removes a currently-admitted key that lacks an explicit revoke grant.
package sync

import (
	"reflect"
	"sort"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

const ownerLabel = "owner (board author)"

// TestDeriveAllowlist_AdmitsLevel1AndAbovePrunesRevoked proves the regenerated
// allowlist is exactly { boardAuthor } ∪ { non-revoked grantees }, with labels from
// each winning grant, and that a revoked key is pruned (design §4).
func TestDeriveAllowlist_AdmitsLevel1AndAbovePrunesRevoked(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	maint := testKey(t)
	contrib := testKey(t)
	gone := testKey(t)

	events := []*nostr.Event{
		grant(t, owner, ba, maint.PubKeyHex(), RoleMaintainer, 0, 1000),
		grant(t, owner, ba, contrib.PubKeyHex(), RoleContributor, 0, 1001),
		// gone is granted then revoked — the revoke must win and prune it.
		grant(t, owner, ba, gone.PubKeyHex(), RoleContributor, 0, 1002),
		grant(t, owner, ba, gone.PubKeyHex(), RoleRevoked, 0, 1003),
	}

	got := DeriveAllowlist(events, ba, testBoardD, ownerLabel)

	want := map[string]string{
		ba:                  ownerLabel,
		maint.PubKeyHex():   RoleMaintainer + "-label",
		contrib.PubKeyHex(): RoleContributor + "-label",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("DeriveAllowlist = %v\n  want %v", got, want)
	}
	if _, present := got[gone.PubKeyHex()]; present {
		t.Errorf("revoked key %s must be pruned from the allowlist, got present", gone.PubKeyHex())
	}
}

// TestDeriveAllowlist_EscalationCapViolationNotAdmitted proves a maintainer-signed
// maintainer grant (a cap violation) is ignored, so the would-be grantee is NOT
// admitted to the relay allowlist — the cap gates admission, not just level.
func TestDeriveAllowlist_EscalationCapViolationNotAdmitted(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	maint := testKey(t)
	sneak := testKey(t)

	events := []*nostr.Event{
		grant(t, owner, ba, maint.PubKeyHex(), RoleMaintainer, 0, 1000),
		// maint (a non-author level-2) tries to mint a new maintainer — cap violation.
		grant(t, maint, ba, sneak.PubKeyHex(), RoleMaintainer, 0, 1001),
	}
	got := DeriveAllowlist(events, ba, testBoardD, ownerLabel)
	if _, present := got[sneak.PubKeyHex()]; present {
		t.Errorf("cap-violating grantee %s must not be admitted, got present", sneak.PubKeyHex())
	}
}

// TestPlanAllowlist_AddsGrantedPreservesUnmanaged is the no-lockout core: granting a
// fresh agent ADDS it, while a currently-admitted key with NO grant at all (a live
// third-party tenant sharing the relay) is PRESERVED, never dropped.
func TestPlanAllowlist_AddsGrantedPreservesUnmanaged(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	p2 := testKey(t)    // machine-2: seeded with a maintainer grant.
	agent := testKey(t) // freshly granted contributor.
	tenant := "6c74c7bb0f0acb9ee4820f63b52f4209490eaef6fba7d1d2c34c2622413498f1"

	events := []*nostr.Event{
		grant(t, owner, ba, p2.PubKeyHex(), RoleMaintainer, 0, 1000),
		grant(t, owner, ba, agent.PubKeyHex(), RoleContributor, 0, 1001),
	}
	// Baseline = what the live relay currently admits: owner, p2, and an UNMANAGED
	// third-party tenant with no rd grant.
	baseline := map[string]string{
		ba:             "owner",
		p2.PubKeyHex(): "machine-2",
		tenant:         "third-party tenant",
	}

	plan := PlanAllowlist(events, ba, testBoardD, ownerLabel, baseline)

	// The tenant (no grant, no revoke) must survive.
	if _, ok := plan.Final[tenant]; !ok {
		t.Fatalf("unmanaged tenant key was dropped — that would lock it out of the live relay")
	}
	if !contains(plan.Preserved, tenant) {
		t.Errorf("tenant should be reported as Preserved, got %v", plan.Preserved)
	}
	// The agent must be added.
	if _, ok := plan.Final[agent.PubKeyHex()]; !ok {
		t.Errorf("granted agent %s missing from Final", agent.PubKeyHex())
	}
	if !contains(plan.Added, agent.PubKeyHex()) {
		t.Errorf("agent should be in Added, got %v", plan.Added)
	}
	// Nobody removed, no lockout.
	if len(plan.Removed) != 0 {
		t.Errorf("no key should be removed, got Removed=%v", plan.Removed)
	}
	if len(plan.LockoutViolations) != 0 {
		t.Fatalf("no-lockout invariant violated: %v", plan.LockoutViolations)
	}
}

// TestPlanAllowlist_RevokeRemovesOnlyRevokedKey proves that after a revoke, the
// agent is the ONLY key removed — the owner, machine-2, and the unmanaged tenant all
// stay admitted. This is demo steps (e)/(f): revoke -> next agent write rejected,
// while P1/P2 (and the tenant) stay admitted.
func TestPlanAllowlist_RevokeRemovesOnlyRevokedKey(t *testing.T) {
	owner := testKey(t)
	ba := owner.PubKeyHex()
	p2 := testKey(t)
	agent := testKey(t)
	tenant := "6c74c7bb0f0acb9ee4820f63b52f4209490eaef6fba7d1d2c34c2622413498f1"

	events := []*nostr.Event{
		grant(t, owner, ba, p2.PubKeyHex(), RoleMaintainer, 0, 1000),
		grant(t, owner, ba, agent.PubKeyHex(), RoleContributor, 0, 1001),
		grant(t, owner, ba, agent.PubKeyHex(), RoleRevoked, 0, 1002), // revoke.
	}
	baseline := map[string]string{
		ba:                "owner",
		p2.PubKeyHex():    "machine-2",
		agent.PubKeyHex(): "agent",
		tenant:            "third-party tenant",
	}

	plan := PlanAllowlist(events, ba, testBoardD, ownerLabel, baseline)

	if _, ok := plan.Final[agent.PubKeyHex()]; ok {
		t.Errorf("revoked agent must be pruned from Final")
	}
	if want := []string{agent.PubKeyHex()}; !reflect.DeepEqual(plan.Removed, want) {
		t.Errorf("Removed = %v, want exactly the revoked agent %v", plan.Removed, want)
	}
	for _, keep := range []string{ba, p2.PubKeyHex(), tenant} {
		if _, ok := plan.Final[keep]; !ok {
			t.Errorf("key %s must stay admitted after the revoke, got dropped", keep)
		}
	}
	if len(plan.LockoutViolations) != 0 {
		t.Fatalf("no-lockout invariant violated: %v", plan.LockoutViolations)
	}
}

func contains(s []string, v string) bool {
	i := sort.SearchStrings(s, v)
	return i < len(s) && s[i] == v
}
