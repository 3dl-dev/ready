package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
	"time"

	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/beacon"

	"github.com/campfire-net/ready/pkg/rdconfig"
)

// TestInitJoin_IdempotentWhenAlreadyMember verifies that re-running rd init
// (or auto-join via [rd].beacon) succeeds when ~/.cf/store.db already has a
// membership row for the campfire — the case where machine-1 wiped only
// .ready/ and .campfire/root but not the identity-scoped store.
//
// Without the GetMembership short-circuit, the SDK Join attempt would either
// reach the relay needlessly OR the local AdmitMember would fail with
// "UNIQUE constraint failed: campfire_memberships.campfire_id".
func TestInitJoin_IdempotentWhenAlreadyMember(t *testing.T) {
	// Isolated CF_HOME so we don't touch the user's real store.
	cfHome := t.TempDir()
	t.Setenv("CF_HOME", cfHome)

	// projectRoot() walks up from os.Getwd(); chdir to a clean dir so the
	// "already initialized" guard at the top of initJoin doesn't trip on the
	// real ready repo's .campfire/root.
	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	cleanCwd := t.TempDir()
	if err := os.Chdir(cleanCwd); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	// Pre-populate the store with a membership row for a synthetic campfire.
	campfirePub, campfirePriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	campfireID := hex.EncodeToString(campfirePub)

	s, err := store.Open(filepath.Join(cfHome, "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if addErr := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: t.TempDir(),
		JoinProtocol: "invite-only",
		Role:         "full",
		JoinedAt:     time.Now().Unix(),
		Description:  "pre-existing membership",
	}); addErr != nil {
		t.Fatalf("AddMembership: %v", addErr)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("store.Close: %v", err)
	}

	// Construct a real signed beacon for that campfire pointing at a fake
	// relay. The idempotent path never makes the network call, so the relay
	// URL only has to be syntactically valid.
	const fakeRelay = "https://relay.test.invalid/api"
	b, err := beacon.New(
		campfirePub,
		campfirePriv,
		"invite-only",
		[]string{"work:create"},
		beacon.TransportConfig{
			Protocol: "p2p-http",
			Config:   map[string]string{"endpoint": fakeRelay},
		},
		"test campfire",
	)
	if err != nil {
		t.Fatalf("beacon.New: %v", err)
	}
	bytes, err := cfencoding.Marshal(b)
	if err != nil {
		t.Fatalf("cfencoding.Marshal: %v", err)
	}
	beaconStr := "beacon:" + base64.StdEncoding.EncodeToString(bytes)

	// Project dir is empty — no .ready/, no .campfire/root.
	projectDir := t.TempDir()

	// Call initJoin. Must succeed via the idempotent short-circuit.
	if err := initJoin(projectDir, "test-project", beaconStr); err != nil {
		t.Fatalf("initJoin (idempotent path) returned error: %v", err)
	}

	// .campfire/root must contain the campfire ID.
	rootBytes, err := os.ReadFile(filepath.Join(projectDir, ".campfire", "root"))
	if err != nil {
		t.Fatalf("reading .campfire/root: %v", err)
	}
	if got := string(rootBytes); got != campfireID {
		t.Errorf(".campfire/root = %q, want %q", got, campfireID)
	}

	// .ready/config.json must reflect the joined campfire.
	cfg, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if cfg.CampfireID != campfireID {
		t.Errorf("SyncConfig.CampfireID = %q, want %q", cfg.CampfireID, campfireID)
	}
	if cfg.ProjectName != "test-project" {
		t.Errorf("SyncConfig.ProjectName = %q, want %q", cfg.ProjectName, "test-project")
	}
	if cfg.RelayURL != fakeRelay {
		t.Errorf("SyncConfig.RelayURL = %q, want %q", cfg.RelayURL, fakeRelay)
	}
	if cfg.Beacon != beaconStr {
		t.Errorf("SyncConfig.Beacon mismatch")
	}

	// Membership row must still exist exactly once (we did not delete and
	// re-insert).
	s2, err := store.Open(filepath.Join(cfHome, "store.db"))
	if err != nil {
		t.Fatalf("store.Open (verify): %v", err)
	}
	defer s2.Close()
	mem, err := s2.GetMembership(campfireID)
	if err != nil {
		t.Fatalf("GetMembership: %v", err)
	}
	if mem == nil {
		t.Fatal("membership row missing after idempotent re-init")
	}
	if mem.Description != "pre-existing membership" {
		t.Errorf("membership.Description = %q, want pre-existing row preserved", mem.Description)
	}
}

// TestInitJoin_IdempotentRunTwice verifies the helper handles being called
// twice in sequence without error — i.e. .ready/.campfire wiped and re-init.
func TestInitJoin_IdempotentRunTwice(t *testing.T) {
	cfHome := t.TempDir()
	t.Setenv("CF_HOME", cfHome)

	origDir, err := os.Getwd()
	if err != nil {
		t.Fatalf("os.Getwd: %v", err)
	}
	cleanCwd := t.TempDir()
	if err := os.Chdir(cleanCwd); err != nil {
		t.Fatalf("os.Chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origDir) })

	campfirePub, campfirePriv, _ := ed25519.GenerateKey(rand.Reader)
	campfireID := hex.EncodeToString(campfirePub)

	s, err := store.Open(filepath.Join(cfHome, "store.db"))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if err := s.AddMembership(store.Membership{
		CampfireID:   campfireID,
		TransportDir: t.TempDir(),
		JoinProtocol: "invite-only",
		Role:         "full",
		JoinedAt:     time.Now().Unix(),
	}); err != nil {
		t.Fatalf("AddMembership: %v", err)
	}
	s.Close()

	b, _ := beacon.New(
		campfirePub, campfirePriv, "invite-only", []string{"work:create"},
		beacon.TransportConfig{Protocol: "p2p-http", Config: map[string]string{"endpoint": "https://r.test/api"}},
		"")
	bytes, _ := cfencoding.Marshal(b)
	beaconStr := "beacon:" + base64.StdEncoding.EncodeToString(bytes)

	projectDir := t.TempDir()

	// First init.
	if err := initJoin(projectDir, "p", beaconStr); err != nil {
		t.Fatalf("first initJoin: %v", err)
	}

	// Wipe project state, leave the store.
	if err := os.RemoveAll(filepath.Join(projectDir, ".ready")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(projectDir, ".campfire")); err != nil {
		t.Fatal(err)
	}

	// Second init must also succeed via the idempotent path.
	if err := initJoin(projectDir, "p", beaconStr); err != nil {
		t.Fatalf("second initJoin (after wipe): %v", err)
	}

	// Project files restored.
	if _, err := os.Stat(filepath.Join(projectDir, ".campfire", "root")); err != nil {
		t.Errorf(".campfire/root not restored: %v", err)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".ready", "config.json")); err != nil {
		t.Errorf(".ready/config.json not restored: %v", err)
	}
}
