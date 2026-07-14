package main

// authz_nostr_test.go — the kind-39301 role-grant is the SOLE authz path on the
// nostr-native default path (ready-477). These tests prove:
//
//   - `rd grant`/`rd revoke`/`rd kill` on a nostr-native project publish an
//     owner-signed kind-39301 role-grant (NOT a cf-authority delegation grant) AND
//     regenerate the relay write-allowlist from the signed log, surfacing the diff —
//     with ZERO campfire .cf provisioning (assertNoDotCf).
//   - `rd sessions` lists grant-holders derived from the signed log (owner +
//     non-revoked grantees), excluding revoked keys.
//   - fail-closed ingestion at the cmd wiring: nostrTrustSet admits an owner-GRANTED
//     key and DROPS an ungranted foreign key.
//
// Every event is real (schnorr-signed via the wire builders) and re-verified inside
// the derivation; assertions check derived authz state, never err==nil.

import (
	"path/filepath"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// readGrantEventsForTest returns every kind-39301 role-grant in the project's local
// authoritative log whose "p" (grantee) tag equals grantee.
func readGrantEventsForTest(t *testing.T, dir, grantee string) []*nostr.Event {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll log: %v", err)
	}
	var out []*nostr.Event
	for _, e := range events {
		if e.Kind != rdSync.KindRoleGrant {
			continue
		}
		if p, ok := tagVal(e.Tags, "p"); ok && p == grantee {
			out = append(out, e)
		}
	}
	return out
}

// defaultAllowlistPathForTest resolves the same file the grant-time regeneration
// writes, relative to the (chdir'd) project dir — CWD is the temp project, which is
// not a git repo, so defaultAllowlistFile() falls back to the repo-relative path.
func defaultAllowlistPathForTest(dir string) string {
	return filepath.Join(dir, "scripts", "relay-policy", "write-allowlist.json")
}

// TestGrantNative_PublishesGrantAndRegeneratesAllowlist proves `rd grant` on a
// nostr-native project (1) publishes an owner-signed kind-39301 grant into the local
// authoritative log and (2) regenerates the relay write-allowlist from that signed
// log so the grantee is admitted — WITHOUT provisioning any campfire .cf state.
func TestGrantNative_PublishesGrantAndRegeneratesAllowlist(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	grantee := gk.PubKeyHex()

	if err := runNostrGrantRevoke(dir, grantee, rdSync.RoleContributor, "agent-pm", 0, ""); err != nil {
		t.Fatalf("runNostrGrantRevoke(grant): %v", err)
	}

	// (1) A real, signed kind-39301 grant naming the grantee is now in the log.
	grants := readGrantEventsForTest(t, dir, grantee)
	if len(grants) != 1 {
		t.Fatalf("expected exactly 1 kind-39301 grant for grantee, got %d", len(grants))
	}
	if role, _ := tagVal(grants[0].Tags, "role"); role != rdSync.RoleContributor {
		t.Fatalf("grant role = %q, want contributor", role)
	}
	if err := grants[0].Verify(); err != nil {
		t.Fatalf("published grant does not verify: %v", err)
	}

	// (2) The relay write-allowlist was regenerated and now admits the grantee.
	file := defaultAllowlistPathForTest(dir)
	allow, err := readAllowlistFile(file)
	if err != nil {
		t.Fatalf("readAllowlistFile: %v", err)
	}
	if _, ok := allow[grantee]; !ok {
		t.Fatalf("grantee %s absent from regenerated allowlist %v — grant did not feed the relay allowlist", grantee, allow)
	}
	if allow[grantee] != "agent-pm" {
		t.Fatalf("allowlist label = %q, want the grant label 'agent-pm'", allow[grantee])
	}

	// The whole flow must never touch campfire .cf state.
	assertNoDotCf(t)
}

// TestRevokeNative_UnifiedRevocationPrunesAllowlist proves `rd revoke`/`rd kill` on a
// nostr-native project publish a role=revoked kind-39301 grant that (1) drops the key
// to operator level 0 in the derived levels and (2) prunes it from the regenerated
// relay write-allowlist — the unified "revocation = publish role=revoked" model, with
// no cf-authority delegation path invoked.
func TestRevokeNative_UnifiedRevocationPrunesAllowlist(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	grantee := gk.PubKeyHex()

	if err := runNostrGrantRevoke(dir, grantee, rdSync.RoleContributor, "agent-pm", 0, ""); err != nil {
		t.Fatalf("grant: %v", err)
	}
	file := defaultAllowlistPathForTest(dir)
	if allow, _ := readAllowlistFile(file); allow[grantee] == "" {
		t.Fatalf("precondition: grantee should be admitted after grant, allowlist=%v", allow)
	}

	// Now revoke (the same primitive kill uses on the native path).
	if err := runNostrGrantRevoke(dir, grantee, rdSync.RoleRevoked, "", 0, ""); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	// (1) Derived operator level for the grantee is 0 (revoked).
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	levels, _ := rdSync.DeriveLevels(events, owner, projectPrefix(dir))
	if lvl, ok := levels[grantee]; !ok || lvl != rdSync.LevelRevoked {
		t.Fatalf("grantee level after revoke = (%d, present=%v), want 0/revoked", levels[grantee], ok)
	}

	// (2) The grantee is pruned from the regenerated allowlist.
	allow, err := readAllowlistFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := allow[grantee]; ok {
		t.Fatalf("revoked grantee still in allowlist %v — revoke did not prune the relay admission", allow)
	}
	assertNoDotCf(t)
}

// TestNostrSessionHolders_OwnerAndNonRevoked proves the `rd sessions` derivation lists
// the owner (root principal) and every non-revoked grantee with its role, and EXCLUDES
// a revoked key — sourced purely from the signed kind-39301 log.
func TestNostrSessionHolders_OwnerAndNonRevoked(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ba := owner.PubKeyHex()
	const boardD = "ready"

	mk := func(t *testing.T) string {
		k, err := nostr.GenerateKey()
		if err != nil {
			t.Fatal(err)
		}
		return k.PubKeyHex()
	}
	contribPK := mk(t)
	maintPK := mk(t)
	revokedPK := mk(t)

	mkGrant := func(grantee, role string, ts int64) *nostr.Event {
		ev, err := rdSync.BuildRoleGrantEvent(owner, rdSync.RoleGrantSpec{
			BoardD: boardD, BoardAuthor: ba, Grantee: grantee, Role: role, Label: role + "-label",
		}, ts)
		if err != nil {
			t.Fatalf("BuildRoleGrantEvent: %v", err)
		}
		return ev
	}
	events := []*nostr.Event{
		mkGrant(contribPK, rdSync.RoleContributor, 100),
		mkGrant(maintPK, rdSync.RoleMaintainer, 110),
		mkGrant(revokedPK, rdSync.RoleContributor, 120),
		mkGrant(revokedPK, rdSync.RoleRevoked, 130),
	}

	holders := nostrSessionHolders(events, ba, boardD)
	byKey := map[string]nostrHolder{}
	for _, h := range holders {
		byKey[h.Pubkey] = h
	}

	if h, ok := byKey[ba]; !ok || h.Role != "owner" {
		t.Fatalf("owner holder = %+v (present=%v), want role owner", byKey[ba], ok)
	}
	if h, ok := byKey[contribPK]; !ok || h.Role != "contributor" {
		t.Fatalf("contributor holder = %+v (present=%v)", byKey[contribPK], ok)
	}
	if h, ok := byKey[maintPK]; !ok || h.Role != "maintainer" {
		t.Fatalf("maintainer holder = %+v (present=%v)", byKey[maintPK], ok)
	}
	if _, ok := byKey[revokedPK]; ok {
		t.Fatalf("revoked key must be EXCLUDED from active sessions, but was listed: %+v", byKey[revokedPK])
	}
}

// TestSessionsNostr_ListsFromLogNoDotCf proves `rd sessions` on a nostr-native
// project derives its listing purely from the signed log (never a campfire client)
// and provisions no .cf: after granting a contributor, runSessionsNostr succeeds and
// no campfire identity is created.
func TestSessionsNostr_ListsFromLogNoDotCf(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if err := runNostrGrantRevoke(dir, gk.PubKeyHex(), rdSync.RoleContributor, "agent", 0, ""); err != nil {
		t.Fatalf("grant: %v", err)
	}
	if err := runSessionsNostr(dir, false); err != nil {
		t.Fatalf("runSessionsNostr: %v", err)
	}
	assertNoDotCf(t)
}

// TestNostrTrustSetNative_AdmitsGrantedDropsUngranted proves the cmd-layer read-trust
// wiring is grant-derived and fail-closed: after `rd grant`, the granted key is in
// nostrTrustSet (admitted at every ingestion seam), while an ungranted foreign key is
// NOT — the load-bearing fail-closed property at the wiring level.
func TestNostrTrustSetNative_AdmitsGrantedDropsUngranted(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	self, err := nostrSelfPubkey()
	if err != nil {
		t.Fatal(err)
	}

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	granted := gk.PubKeyHex()
	if err := runNostrGrantRevoke(dir, granted, rdSync.RoleContributor, "agent", 0, ""); err != nil {
		t.Fatalf("grant: %v", err)
	}

	fk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	ungranted := fk.PubKeyHex()

	set := nostrTrustSet(dir, self)
	if !set[granted] {
		t.Fatalf("granted key %s is NOT in the read-trust set — ingestion would drop a legitimately granted contributor", granted)
	}
	if set[ungranted] {
		t.Fatalf("fail-closed violated: ungranted foreign key %s is in the read-trust set", ungranted)
	}
	assertNoDotCf(t)
}
