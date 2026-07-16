package main

import (
	"path/filepath"
	"testing"

	"github.com/3dl-dev/ready/pkg/rdconfig"
)

func rw(url string) rdconfig.RelayEndpoint {
	return rdconfig.RelayEndpoint{URL: url, Read: true, Write: true}
}

// TestRelayCascade_ProjectWins: a relay declared on THIS project's config wins
// over anything inherited.
func TestRelayCascade_ProjectWins(t *testing.T) {
	dir := setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	// Home config also has a relay — the project's must win.
	_ = rdconfig.Save(RDHome(), &rdconfig.Config{RelayEndpoints: []rdconfig.RelayEndpoint{rw("wss://home.example")}})
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{RelayEndpoints: []rdconfig.RelayEndpoint{rw("wss://project.example")}}); err != nil {
		t.Fatal(err)
	}
	if got := nostrWriteRelays(); len(got) != 1 || got[0] != "wss://project.example" {
		t.Errorf("project relay must win, got %v", got)
	}
}

// TestRelayCascade_InheritsFromAncestor: a project with no relay policy inherits
// from an ancestor directory's .ready/config.json.
func TestRelayCascade_InheritsFromAncestor(t *testing.T) {
	dir := setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	ancestor := filepath.Dir(dir) // the temp base above <base>/project
	if err := rdconfig.SaveSyncConfig(ancestor, &rdconfig.SyncConfig{RelayEndpoints: []rdconfig.RelayEndpoint{rw("wss://umbrella.example")}}); err != nil {
		t.Fatal(err)
	}
	if got := nostrWriteRelays(); len(got) != 1 || got[0] != "wss://umbrella.example" {
		t.Errorf("must inherit ancestor relay, got %v", got)
	}
}

// TestRelayCascade_InheritsFromHome: no project/ancestor policy → the home rd.json
// relay is the machine-wide default.
func TestRelayCascade_InheritsFromHome(t *testing.T) {
	setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	if err := rdconfig.Save(RDHome(), &rdconfig.Config{RelayEndpoints: []rdconfig.RelayEndpoint{rw("wss://home.example")}}); err != nil {
		t.Fatal(err)
	}
	if got := nostrReadRelays(); len(got) != 1 || got[0] != "wss://home.example" {
		t.Errorf("must inherit home relay, got %v", got)
	}
}

// TestRelayCascade_LocalOnlyStopsInheritance: --local (RelaysLocalOnly) opts out —
// even when an ancestor/home relay exists, the project stays local (no leak).
func TestRelayCascade_LocalOnlyStopsInheritance(t *testing.T) {
	dir := setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	_ = rdconfig.Save(RDHome(), &rdconfig.Config{RelayEndpoints: []rdconfig.RelayEndpoint{rw("wss://home.example")}})
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{RelaysLocalOnly: true}); err != nil {
		t.Fatal(err)
	}
	if got := nostrWriteRelays(); len(got) != 0 {
		t.Errorf("--local must not inherit any relay, got %v", got)
	}
}

// TestRelayCascade_EnvOverrideWins: RD_NOSTR_RELAY_URL beats the whole cascade.
func TestRelayCascade_EnvOverrideWins(t *testing.T) {
	dir := setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR_RELAY_URL", "wss://override.example")
	_ = rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{RelaysLocalOnly: true})
	if got := nostrWriteRelays(); len(got) != 1 || got[0] != "wss://override.example" {
		t.Errorf("env override must win, got %v", got)
	}
}
