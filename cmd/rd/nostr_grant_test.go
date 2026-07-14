package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// TestAllowlistFileRoundtrip proves writeAllowlistFile emits stable, sorted-key JSON
// that readAllowlistFile parses back identically — the on-disk format the relay
// plugin reads is preserved byte-for-byte across a regenerate cycle.
func TestAllowlistFileRoundtrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "write-allowlist.json")
	in := map[string]string{
		"bbbb": "second",
		"aaaa": "first",
		"cccc": "third",
	}
	if err := writeAllowlistFile(path, in); err != nil {
		t.Fatalf("writeAllowlistFile: %v", err)
	}
	// Sorted-key, stable output.
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := "{\n  \"aaaa\": \"first\",\n  \"bbbb\": \"second\",\n  \"cccc\": \"third\"\n}\n"
	if string(got) != want {
		t.Errorf("file =\n%q\nwant\n%q", got, want)
	}
	back, err := readAllowlistFile(path)
	if err != nil {
		t.Fatalf("readAllowlistFile: %v", err)
	}
	if !reflect.DeepEqual(back, in) {
		t.Errorf("roundtrip = %v, want %v", back, in)
	}
}

// TestReadAllowlistFile_MissingIsEmpty proves a missing allowlist file reads as an
// empty map (not an error), so a first run has a clean empty baseline.
func TestReadAllowlistFile_MissingIsEmpty(t *testing.T) {
	m, err := readAllowlistFile(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("missing file should not error: %v", err)
	}
	if len(m) != 0 {
		t.Errorf("missing file should be empty map, got %v", m)
	}
}

func TestSplitCSV(t *testing.T) {
	cases := map[string][]string{
		"192.168.2.40,192.168.2.41": {"192.168.2.40", "192.168.2.41"},
		" a , b ,,c ":               {"a", "b", "c"},
		"":                          nil,
		"only":                      {"only"},
	}
	for in, want := range cases {
		if got := splitCSV(in); !reflect.DeepEqual(got, want) {
			t.Errorf("splitCSV(%q) = %v, want %v", in, got, want)
		}
	}
}

// TestResolveBoardAuthorD_PinnedWins proves that when a board is pinned in
// .ready/config.json, resolveBoardAuthorD returns the PINNED owner/boardD (not the
// signer / project prefix) — so a grant binds to the owner's authority chain even
// when signed by a non-owner actor.
func TestResolveBoardAuthorD_PinnedWins(t *testing.T) {
	dir := t.TempDir()
	owner := "a9f766ae56bbf466d2d361e5b1788b7cd689fd8e3b418e35b002b313f478db25"
	cfg := &rdconfig.SyncConfig{Board: "30301:" + owner + ":ready"}
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatal(err)
	}
	gotOwner, gotD, err := resolveBoardAuthorD(dir, "deadbeef")
	if err != nil {
		t.Fatalf("resolveBoardAuthorD: %v", err)
	}
	if gotOwner != owner || gotD != "ready" {
		t.Errorf("resolveBoardAuthorD = (%s,%s), want (%s,ready)", gotOwner, gotD, owner)
	}
}

// TestPublishRoleGrant_NonMaintainerRejectedClientSide is the MED-6 proof: a signer
// that is NOT the board owner and holds NO maintainer grant is rejected CLIENT-SIDE
// when it runs `rd nostr grant/revoke` — a clear early error, not a silently-ignored
// grant. The project's board is pinned to a FOREIGN owner, so the local signer is a
// plain contributor (absent from the derived level map) and MayGrant returns false.
func TestPublishRoleGrant_NonMaintainerRejectedClientSide(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)

	// Re-pin the board to a FOREIGN owner so the local signer is not the board author
	// and has no maintainer grant.
	foreign, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey (foreign owner): %v", err)
	}
	boardD := projectPrefix(dir)
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	cfg.Board = rdSync.BoardCoord(foreign.PubKeyHex(), boardD)
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey (grantee): %v", err)
	}

	// A plain contributor attempting to grant a contributor must be rejected client-side.
	err = publishRoleGrant(grantee.PubKeyHex(), rdSync.RoleContributor, "", 0, "")
	if err == nil || !strings.Contains(err.Error(), "escalation cap") {
		t.Fatalf("non-maintainer grant = %v, want an 'escalation cap' client-side rejection", err)
	}
	// And attempting to revoke must be rejected the same way.
	err = publishRoleGrant(grantee.PubKeyHex(), rdSync.RoleRevoked, "", 0, "")
	if err == nil || !strings.Contains(err.Error(), "escalation cap") {
		t.Fatalf("non-maintainer revoke = %v, want an 'escalation cap' client-side rejection", err)
	}
	assertNoDotCf(t)
}

// TestPublishRoleGrant_ClaimSingleUse is the ready-ce0 security-property (c) proof at
// the CLI seam: the owner binds a first self-minted key to claim-nonce N (ok); a
// SECOND grant reusing the SAME N for a DIFFERENT key is REFUSED client-side (one
// claim-nonce admits exactly one pubkey). Re-granting the SAME key under its own N is
// allowed (e.g. a later role change).
func TestPublishRoleGrant_ClaimSingleUse(t *testing.T) {
	setupNostrNativeProject(t)
	const claim = "cli-claim-01"
	a, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey a: %v", err)
	}
	b, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey b: %v", err)
	}
	if err := publishRoleGrant(a.PubKeyHex(), rdSync.RoleContributor, "a", 0, claim); err != nil {
		t.Fatalf("first --claim grant should succeed: %v", err)
	}
	err = publishRoleGrant(b.PubKeyHex(), rdSync.RoleContributor, "b", 0, claim)
	if err == nil || !strings.Contains(err.Error(), "already consumed") {
		t.Fatalf("second grant reusing claim = %v, want 'already consumed' refusal", err)
	}
	if err := publishRoleGrant(a.PubKeyHex(), rdSync.RoleContributor, "a2", 0, claim); err != nil {
		t.Fatalf("same-key re-grant under its own claim should succeed: %v", err)
	}
	assertNoDotCf(t)
}

// TestResolveBoardAuthorD_UnpinnedFallsBackToSigner proves that with no pin,
// resolveBoardAuthorD falls back to (signer, projectPrefix) — the owner signing their
// own board (pre-pin behaviour, zero migration).
func TestResolveBoardAuthorD_UnpinnedFallsBackToSigner(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "ready")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	gotOwner, gotD, err := resolveBoardAuthorD(dir, "cafebabe")
	if err != nil {
		t.Fatalf("resolveBoardAuthorD: %v", err)
	}
	if gotOwner != "cafebabe" || gotD != "ready" {
		t.Errorf("resolveBoardAuthorD = (%s,%s), want (cafebabe,ready)", gotOwner, gotD)
	}
}
