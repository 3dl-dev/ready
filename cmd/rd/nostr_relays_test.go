package main

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/rdconfig"
)

// TestNostrRelays_ResolveFromProjectConfig guards the BYOR path: relays a user
// configured in THIS project's .ready/config.json must be loaded and used by the
// read/write paths. Per-project (not global) so a --local project never inherits
// another project's relays (ready review [0]).
func TestNostrRelays_ResolveFromProjectConfig(t *testing.T) {
	dir := setupNostrCmdTest(t) // isolated project + chdir + RD_HOME
	t.Setenv("RD_NOSTR_RELAY_URL", "") // ensure the single-relay env override is off

	cfg := &rdconfig.SyncConfig{RelayEndpoints: []rdconfig.RelayEndpoint{
		{URL: "wss://rw.example.com", Read: true, Write: true},
		{URL: "wss://read-only.example.com", Read: true, Write: false},
	}}
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatalf("save sync config: %v", err)
	}

	w := nostrWriteRelays()
	if len(w) != 1 || w[0] != "wss://rw.example.com" {
		t.Errorf("nostrWriteRelays() = %v, want [wss://rw.example.com]", w)
	}
	r := nostrReadRelays()
	if len(r) != 2 {
		t.Errorf("nostrReadRelays() = %v, want both configured read relays", r)
	}
}

// TestNostrRelays_LocalOnlyWhenUnconfigured proves the ship default: a project with
// no relay config resolves to nothing (local-only) — no baked-in topology.
func TestNostrRelays_LocalOnlyWhenUnconfigured(t *testing.T) {
	setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR_RELAY_URL", "")

	if got := nostrWriteRelays(); len(got) != 0 {
		t.Errorf("unconfigured nostrWriteRelays() = %v, want local-only (empty)", got)
	}
	if got := nostrReadRelays(); len(got) != 0 {
		t.Errorf("unconfigured nostrReadRelays() = %v, want local-only (empty)", got)
	}
}

// TestNostrRelays_EnvOverrideWins proves RD_NOSTR_RELAY_URL still short-circuits
// config resolution (the demo/single-relay override).
func TestNostrRelays_EnvOverrideWins(t *testing.T) {
	setupNostrCmdTest(t)
	t.Setenv("RD_NOSTR_RELAY_URL", "wss://override.example.com")

	if got := nostrWriteRelays(); len(got) != 1 || got[0] != "wss://override.example.com" {
		t.Errorf("env override nostrWriteRelays() = %v, want [wss://override.example.com]", got)
	}
}
