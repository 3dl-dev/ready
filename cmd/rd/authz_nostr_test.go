package main

// authz_nostr_test.go — the kind-39301 role-grant is the SOLE authz path on the
// nostr-native default path (ready-477). These tests prove:
//
//   - `rd grant`/`rd revoke`/`rd kill` on a nostr-native project publish an
//     owner-signed kind-39301 role-grant (NOT a cf-authority delegation grant) and do
//     NOT touch the relay write-allowlist file (ready-dc2) — with ZERO campfire .cf
//     provisioning (assertNoDotCf).
//   - `rd sessions` lists grant-holders derived from the signed log (owner +
//     non-revoked grantees), excluding revoked keys.
//   - fail-closed ingestion at the cmd wiring: nostrTrustSet admits an owner-GRANTED
//     key and DROPS an ungranted foreign key.
//
// Every event is real (schnorr-signed via the wire builders) and re-verified inside
// the derivation; assertions check derived authz state, never err==nil.

import (
	"os"
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

// defaultAllowlistPathForTest resolves the same file `rd relay sync-allowlist` would
// write, relative to the (chdir'd) project dir — CWD is the temp project, which is not
// a git repo, so defaultAllowlistFile() falls back to the repo-relative path. Grant
// and revoke must NEVER create this file (ready-dc2), which these tests assert.
func defaultAllowlistPathForTest(dir string) string {
	return filepath.Join(dir, "scripts", "relay-policy", "write-allowlist.json")
}

// TestGrantNative_PublishesGrantDoesNotTouchAllowlist proves `rd grant` on a
// nostr-native project (1) publishes an owner-signed kind-39301 grant into the local
// authoritative log and (2) does NOT create or modify the relay write-allowlist file
// (ready-dc2 — grant is a complete invite via the signed 39301 alone; the app-layer
// trusted set is the authorization gate, so there is no relay-file dependency) —
// WITHOUT provisioning any campfire .cf state.
func TestGrantNative_PublishesGrantDoesNotTouchAllowlist(t *testing.T) {
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

	// (2) The relay write-allowlist file must NOT have been created or modified.
	if _, statErr := os.Stat(defaultAllowlistPathForTest(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("grant created/modified write-allowlist.json (stat err=%v); grant must not touch it at all", statErr)
	}

	// The whole flow must never touch campfire .cf state.
	assertNoDotCf(t)
}

// TestRevokeNative_DropsFromTrustSetNoAllowlist proves `rd revoke`/`rd kill` on a
// nostr-native project publish a role=revoked kind-39301 grant that (1) drops the key
// to operator level 0 in the derived levels and removes it from the active
// grant-holder ("trusted") set nostrSessionHolders reports — so it is no longer an
// authorized writer — and (2) does NOT touch the relay write-allowlist file
// (ready-dc2), the unified "revocation = publish role=revoked" model with no
// cf-authority delegation path invoked.
//
// NOTE on read-trust: prospective (default) revocation deliberately KEEPS the key in
// the read-ingest trust set (nostrTrustSet) so its PRE-revocation items still project
// — past authoritative events stay honored. The write-side authority (operator level
// 0 / excluded from active holders) is what denies it NEW winning writes; that is the
// "removed from the trusted set" the done condition means, asserted below.
func TestRevokeNative_DropsFromTrustSetNoAllowlist(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)
	boardD := projectPrefix(dir)

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	grantee := gk.PubKeyHex()

	if err := runNostrGrantRevoke(dir, grantee, rdSync.RoleContributor, "agent-pm", 0, ""); err != nil {
		t.Fatalf("grant: %v", err)
	}
	// Precondition: the granted key is an active holder (authorized writer) after grant.
	preEvents, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	if !holderPresent(nostrSessionHolders(preEvents, owner, boardD), grantee) {
		t.Fatalf("precondition: grantee should be an active grant-holder after grant")
	}

	// Now revoke (the same primitive kill uses on the native path).
	if err := runNostrGrantRevoke(dir, grantee, rdSync.RoleRevoked, "", 0, ""); err != nil {
		t.Fatalf("revoke: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	// (1a) Derived operator level for the grantee is 0 (revoked): no longer an
	// authorized writer.
	levels, _ := rdSync.DeriveLevels(events, owner, boardD)
	if lvl, ok := levels[grantee]; !ok || lvl != rdSync.LevelRevoked {
		t.Fatalf("grantee level after revoke = (%d, present=%v), want 0/revoked", levels[grantee], ok)
	}
	// (1b) The revoked key is removed from the active grant-holder ("trusted") set.
	if holderPresent(nostrSessionHolders(events, owner, boardD), grantee) {
		t.Fatalf("revoked grantee still listed as an active grant-holder — revoke did not remove its write authority")
	}

	// (2) The relay write-allowlist file must NOT have been created or modified.
	if _, statErr := os.Stat(defaultAllowlistPathForTest(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("revoke created/modified write-allowlist.json (stat err=%v); revoke must not touch it at all", statErr)
	}
	assertNoDotCf(t)
}

// holderPresent reports whether pubkey appears in the active grant-holder list.
func holderPresent(holders []nostrHolder, pubkey string) bool {
	for _, h := range holders {
		if h.Pubkey == pubkey {
			return true
		}
	}
	return false
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

// TestGrantNative_NoRole_DefaultsContributor_NoAllowlistWrite is the ready-dc2 done
// condition: `rd grant <pubkey>` with NO role arg is a COMPLETE invite in one command.
// It (1) is accepted by the Args validator (no "accepts 2 arg(s)" error), (2) defaults
// the role to contributor and publishes a valid signed kind-39301 grant, (3) does NOT
// create or modify scripts/relay-policy/write-allowlist.json AT ALL (the relay-file
// regen footgun is gone), and (4) lands the granted key in nostrTrustSet while an
// ungranted key stays out — the load-bearing app-layer trusted-set gate that
// authorization now rests on entirely. Exercises the real grantCmd Args + RunE wiring.
func TestGrantNative_NoRole_DefaultsContributor_NoAllowlistWrite(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)

	// grantCmd is a package-global cobra command that sibling tests mutate flags on;
	// reset the flags this path reads to their defaults so leftover state can't leak.
	for name, def := range map[string]string{
		"label": "", "claim": "", "projects-root": "", "all-boards": "false", "from": "0",
	} {
		if err := grantCmd.Flags().Set(name, def); err != nil {
			t.Fatalf("reset flag %s: %v", name, err)
		}
	}

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	grantee := gk.PubKeyHex()

	// `rd grant <pubkey>` — ONE positional arg, no role.
	if err := grantCmd.Args(grantCmd, []string{grantee}); err != nil {
		t.Fatalf("grant with one arg rejected by Args validator: %v — `rd grant <pubkey>` must no longer error", err)
	}
	if err := grantCmd.RunE(grantCmd, []string{grantee}); err != nil {
		t.Fatalf("grant <pubkey> (no role): %v", err)
	}

	// (2) A real signed kind-39301 contributor grant is now in the log.
	grants := readGrantEventsForTest(t, dir, grantee)
	if len(grants) != 1 {
		t.Fatalf("expected exactly 1 kind-39301 grant, got %d", len(grants))
	}
	if role, _ := tagVal(grants[0].Tags, "role"); role != rdSync.RoleContributor {
		t.Fatalf("defaulted grant role = %q, want contributor", role)
	}
	if err := grants[0].Verify(); err != nil {
		t.Fatalf("grant does not verify: %v", err)
	}

	// (3) grant MUST NOT create or modify the relay write-allowlist file at all.
	if _, statErr := os.Stat(defaultAllowlistPathForTest(dir)); !os.IsNotExist(statErr) {
		t.Fatalf("grant created/modified write-allowlist.json (stat err=%v); grant must not touch it at all", statErr)
	}

	// (4) Load-bearing app-layer gate: granted key admitted, ungranted foreign key dropped.
	self, err := nostrSelfPubkey()
	if err != nil {
		t.Fatal(err)
	}
	set := nostrTrustSet(dir, self)
	if !set[grantee] {
		t.Fatalf("granted key absent from nostrTrustSet — ingestion would drop a legit contributor's items")
	}
	fk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	if set[fk.PubKeyHex()] {
		t.Fatalf("fail-closed violated: ungranted foreign key admitted to nostrTrustSet — its items would project")
	}
	assertNoDotCf(t)
}
