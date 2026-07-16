package main

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/rdconfig"
)

// TestNostrRelays_ResolveFromRDHomeConfig guards the BYOR path: relays a user
// configured (at `rd init --relay` or by editing rd.json) must actually be loaded
// and used by the read/write paths. Regression guard for the latent bug where
// nostrReadRelays/nostrWriteRelays used a zero-value Config and only ever returned
// the (now removed) hardcoded DefaultRelays(), silently ignoring rd.json.
func TestNostrRelays_ResolveFromRDHomeConfig(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RD_HOME", home)
	t.Setenv("RD_NOSTR_RELAY_URL", "") // ensure the single-relay env override is off

	cfg := &rdconfig.Config{RelayEndpoints: []rdconfig.RelayEndpoint{
		{URL: "wss://rw.example.com", Read: true, Write: true},
		{URL: "wss://read-only.example.com", Read: true, Write: false},
	}}
	if err := rdconfig.Save(home, cfg); err != nil {
		t.Fatalf("save config: %v", err)
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

// TestNostrRelays_LocalOnlyWhenUnconfigured proves the ship default: with no relay
// config on disk, both paths resolve to nothing (local-only) — no baked-in topology.
func TestNostrRelays_LocalOnlyWhenUnconfigured(t *testing.T) {
	home := t.TempDir()
	t.Setenv("RD_HOME", home)
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
	home := t.TempDir()
	t.Setenv("RD_HOME", home)
	t.Setenv("RD_NOSTR_RELAY_URL", "wss://override.example.com")

	if got := nostrWriteRelays(); len(got) != 1 || got[0] != "wss://override.example.com" {
		t.Errorf("env override nostrWriteRelays() = %v, want [wss://override.example.com]", got)
	}
}
