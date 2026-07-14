package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
)

// saveAndClearRDHomeState isolates every input RDHome() reads: the --rd-home flag
// global, RD_HOME, XDG_CONFIG_HOME, HOME, and cwd. RD_HOME / XDG are cleared via
// t.Setenv("") (RDHome treats "" as unset), auto-restored at test end.
func saveAndClearRDHomeState(t *testing.T) {
	t.Helper()
	oldFlag := rdHomeFlag
	oldCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		rdHomeFlag = oldFlag
		_ = os.Chdir(oldCwd)
	})
	rdHomeFlag = ""
	t.Setenv("RD_HOME", "")
	t.Setenv("XDG_CONFIG_HOME", "")
}

func TestRDHome_FlagHighestPriority(t *testing.T) {
	saveAndClearRDHomeState(t)
	rdHomeFlag = "/tmp/flag-rd-home"
	t.Setenv("RD_HOME", "/tmp/env-rd-home")
	if got := RDHome(); got != "/tmp/flag-rd-home" {
		t.Fatalf("flag must win: got %q", got)
	}
}

func TestRDHome_EnvSecondPriority(t *testing.T) {
	saveAndClearRDHomeState(t)
	t.Setenv("RD_HOME", "/tmp/env-rd-home")
	if got := RDHome(); got != "/tmp/env-rd-home" {
		t.Fatalf("env must win when flag unset: got %q", got)
	}
}

func TestRDHome_WalkUpMarkerThirdPriority(t *testing.T) {
	saveAndClearRDHomeState(t)
	// A repo-like tree with a .rd/ marker at the top and a nested cwd below it.
	top := t.TempDir()
	marker := filepath.Join(top, ".rd")
	if err := os.MkdirAll(marker, 0o700); err != nil {
		t.Fatalf("mkdir .rd: %v", err)
	}
	nested := filepath.Join(top, "a", "b")
	if err := os.MkdirAll(nested, 0o700); err != nil {
		t.Fatalf("mkdir nested: %v", err)
	}
	if err := os.Chdir(nested); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	got := RDHome()
	// macOS /tmp is a symlink to /private/tmp; compare resolved paths.
	gotResolved, _ := filepath.EvalSymlinks(got)
	wantResolved, _ := filepath.EvalSymlinks(marker)
	if gotResolved != wantResolved {
		t.Fatalf("walk-up must find the .rd marker: got %q want %q", got, marker)
	}
}

func TestRDHome_DefaultXDG(t *testing.T) {
	saveAndClearRDHomeState(t)
	// cwd must have no .rd ancestor, else walk-up would pre-empt the default.
	empty := t.TempDir()
	if err := os.Chdir(empty); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got := RDHome(); got != filepath.Join("/tmp/xdg", "rd") {
		t.Fatalf("XDG_CONFIG_HOME default: got %q", got)
	}
}

func TestRDHome_DefaultConfigDir(t *testing.T) {
	saveAndClearRDHomeState(t)
	empty := t.TempDir()
	if err := os.Chdir(empty); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("home: %v", err)
	}
	if got := RDHome(); got != filepath.Join(home, ".config", "rd") {
		t.Fatalf("default ~/.config/rd: got %q", got)
	}
}

// TestMigrateRDHome_CopiesIdentityPreservingPubkey is the core migration proof
// (PROOF (b)): a legacy .cf nostr identity is COPIED forward into $RD_HOME with
// the IDENTICAL pubkey — never regenerated — and the legacy original is left in
// place for rollback.
func TestMigrateRDHome_CopiesIdentityPreservingPubkey(t *testing.T) {
	saveAndClearRDHomeState(t)
	base := t.TempDir()

	// Legacy .cf home with an existing identity. CFHome() resolves via the
	// --cf-home flag global (rdHome), so point it here.
	legacyHome := filepath.Join(base, ".cf")
	if err := os.MkdirAll(legacyHome, 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	legacyKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen: %v", err)
	}
	wantPub := legacyKey.PubKeyHex()
	legacyKeyPath := nostr.DefaultKeyPath(legacyHome)
	if err := nostr.SaveKeyFile(legacyKeyPath, legacyKey, legacyHome); err != nil {
		t.Fatalf("save legacy key: %v", err)
	}
	// A legacy rd.json to prove config is carried forward too.
	if err := rdconfig.Save(legacyHome, &rdconfig.Config{Org: "acme", TrustedPubkeys: []string{"deadbeef"}}); err != nil {
		t.Fatalf("save legacy rd.json: %v", err)
	}

	origCfHome := rdHome
	rdHome = legacyHome // CFHome() -> legacyHome
	t.Cleanup(func() { rdHome = origCfHome })

	// Fresh, empty rd home.
	newHome := filepath.Join(base, "rdhome")
	rdHomeFlag = newHome // RDHome() -> newHome

	if err := migrateRDHomeIfNeeded(newHome); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// The identity landed under $RD_HOME with the SAME pubkey.
	newKeyPath := nostr.DefaultKeyPath(newHome)
	migrated, err := nostr.LoadKeyFile(newKeyPath)
	if err != nil {
		t.Fatalf("load migrated key: %v", err)
	}
	if migrated.PubKeyHex() != wantPub {
		t.Fatalf("migration REGENERATED the identity: got pubkey %s want %s", migrated.PubKeyHex(), wantPub)
	}
	if migrated.SecretHex() != legacyKey.SecretHex() {
		t.Fatalf("migrated secret differs from legacy secret — not an identity-preserving copy")
	}
	// Legacy original left in place (rollback).
	if !fileExists(legacyKeyPath) {
		t.Fatalf("legacy key must be left in place for rollback")
	}
	// rd.json carried forward.
	newCfg, err := rdconfig.Load(newHome)
	if err != nil {
		t.Fatalf("load migrated rd.json: %v", err)
	}
	if newCfg.Org != "acme" || len(newCfg.TrustedPubkeys) != 1 || newCfg.TrustedPubkeys[0] != "deadbeef" {
		t.Fatalf("rd.json not carried forward: %+v", newCfg)
	}
}

// TestMigrateRDHome_NeverOverwritesExistingIdentity proves the never-clobber
// guarantee: when $RD_HOME already holds an identity, migration is a no-op even
// if a DIFFERENT legacy key exists — the existing identity is untouched.
func TestMigrateRDHome_NeverOverwritesExistingIdentity(t *testing.T) {
	saveAndClearRDHomeState(t)
	base := t.TempDir()

	legacyHome := filepath.Join(base, ".cf")
	if err := os.MkdirAll(legacyHome, 0o700); err != nil {
		t.Fatalf("mkdir legacy: %v", err)
	}
	legacyKey, _ := nostr.GenerateKey()
	if err := nostr.SaveKeyFile(nostr.DefaultKeyPath(legacyHome), legacyKey, legacyHome); err != nil {
		t.Fatalf("save legacy: %v", err)
	}

	origCfHome := rdHome
	rdHome = legacyHome
	t.Cleanup(func() { rdHome = origCfHome })

	newHome := filepath.Join(base, "rdhome")
	rdHomeFlag = newHome
	existingKey, _ := nostr.GenerateKey()
	wantPub := existingKey.PubKeyHex()
	if wantPub == legacyKey.PubKeyHex() {
		t.Fatalf("precondition: keys must differ")
	}
	if err := nostr.SaveKeyFile(nostr.DefaultKeyPath(newHome), existingKey, newHome); err != nil {
		t.Fatalf("save existing: %v", err)
	}

	if err := migrateRDHomeIfNeeded(newHome); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	got, err := nostr.LoadKeyFile(nostr.DefaultKeyPath(newHome))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got.PubKeyHex() != wantPub {
		t.Fatalf("migration overwrote an existing identity: got %s want %s", got.PubKeyHex(), wantPub)
	}
}

// TestPlanRDHomeMigration_NoLegacyKeyIsNoop verifies a clean install (no legacy
// .cf key) plans no copy — a fresh key will simply be generated under $RD_HOME.
func TestPlanRDHomeMigration_NoLegacyKeyIsNoop(t *testing.T) {
	saveAndClearRDHomeState(t)
	base := t.TempDir()
	legacyHome := filepath.Join(base, ".cf")
	if err := os.MkdirAll(legacyHome, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	origCfHome := rdHome
	rdHome = legacyHome
	t.Cleanup(func() { rdHome = origCfHome })
	newHome := filepath.Join(base, "rdhome")
	rdHomeFlag = newHome

	p := planRDHomeMigration(newHome)
	if p.KeyNeedsCopy {
		t.Fatalf("no legacy key present, but plan says KeyNeedsCopy: %+v", p)
	}
	if err := migrateRDHomeIfNeeded(newHome); err != nil {
		t.Fatalf("migrate (noop) should not error: %v", err)
	}
	if fileExists(nostr.DefaultKeyPath(newHome)) {
		t.Fatalf("no-op migration must not create a key file")
	}
}

// TestMigrateHomeCmd_DryRunWritesNothing drives the `rd migrate-home --dry-run`
// CLI surface: it prints the plan (a legacy key IS present, so the plan reports a
// copy) but must NOT create the destination key.
func TestMigrateHomeCmd_DryRunWritesNothing(t *testing.T) {
	saveAndClearRDHomeState(t)
	base := t.TempDir()
	legacyHome := filepath.Join(base, ".cf")
	if err := os.MkdirAll(legacyHome, 0o700); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	lk, _ := nostr.GenerateKey()
	if err := nostr.SaveKeyFile(nostr.DefaultKeyPath(legacyHome), lk, legacyHome); err != nil {
		t.Fatalf("save legacy: %v", err)
	}
	origCfHome := rdHome
	rdHome = legacyHome
	t.Cleanup(func() { rdHome = origCfHome })
	newHome := filepath.Join(base, "rdhome")
	rdHomeFlag = newHome

	p := planRDHomeMigration(newHome)
	if !p.KeyNeedsCopy {
		t.Fatalf("precondition: legacy key present should plan a copy: %+v", p)
	}
	if err := migrateHomeCmd.Flags().Set("dry-run", "true"); err != nil {
		t.Fatalf("set flag: %v", err)
	}
	t.Cleanup(func() { _ = migrateHomeCmd.Flags().Set("dry-run", "false") })
	if err := migrateHomeCmd.RunE(migrateHomeCmd, nil); err != nil {
		t.Fatalf("dry-run RunE: %v", err)
	}
	if fileExists(nostr.DefaultKeyPath(newHome)) {
		t.Fatalf("--dry-run must not write the destination key")
	}
}
