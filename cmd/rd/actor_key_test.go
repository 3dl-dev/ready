// BP-4 (ready-295) cmd/rd-level proof: $RD_ACTOR selects the signing key through the
// REAL nostrKey() resolution. Owner (default) and a named agent on ONE host load
// DISTINCT on-disk keys with DISTINCT pubkeys, and the agent key lands at
// $RD_HOME/keys/agent-pm.json — while the default actor stays on the legacy
// nostr-identity.json (zero migration). Hermetic: RD_HOME + HOME are redirected to
// temp dirs so no real ~/.cf identity is consulted.
package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

func TestNostrKey_RDActorSelectsDistinctKey(t *testing.T) {
	rdHome := t.TempDir()

	// Isolate identity resolution: RD_HOME wins over walk-up/XDG; HOME redirected so
	// migrateRDHomeIfNeeded finds no legacy ~/.cf key and the owner key is fresh.
	oldFlag := rdHomeFlag
	t.Cleanup(func() { rdHomeFlag = oldFlag })
	rdHomeFlag = ""
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", t.TempDir())

	// Default actor (RD_ACTOR unset) -> owner -> legacy nostr-identity.json.
	os.Unsetenv("RD_ACTOR")
	owner, err := nostrKey()
	if err != nil {
		t.Fatalf("owner nostrKey: %v", err)
	}
	if _, err := os.Stat(nostr.DefaultKeyPath(rdHome)); err != nil {
		t.Fatalf("owner key not at legacy path (zero-migration broken): %v", err)
	}

	// Named agent -> keys/agent-pm.json, a DISTINCT key.
	t.Setenv("RD_ACTOR", "agent:pm")
	agent, err := nostrKey()
	if err != nil {
		t.Fatalf("agent nostrKey: %v", err)
	}
	agentPath := filepath.Join(rdHome, "keys", "agent-pm.json")
	if _, err := os.Stat(agentPath); err != nil {
		t.Fatalf("agent key not persisted at %s: %v", agentPath, err)
	}
	if owner.PubKeyHex() == agent.PubKeyHex() {
		t.Fatalf("owner and agent share pubkey %s — $RD_ACTOR did not select a distinct key", owner.PubKeyHex())
	}

	// Re-selecting the owner returns the SAME key (durable, not per-process).
	os.Unsetenv("RD_ACTOR")
	owner2, err := nostrKey()
	if err != nil {
		t.Fatalf("owner reselect: %v", err)
	}
	if owner.PubKeyHex() != owner2.PubKeyHex() {
		t.Fatalf("owner key not stable across selections: %s != %s", owner.PubKeyHex(), owner2.PubKeyHex())
	}
}
