// BP-4 (ready-295) actor-key proofs: an owner and a named agent on ONE host resolve
// to DISTINCT on-disk keys with DISTINCT pubkeys, while the default actor resolves to
// EXACTLY the legacy single-key path so an existing install migrates with zero churn.
// Every key here is a real GenerateKey persisted through the race-safe create path;
// assertions check the pubkeys and paths, never err==nil. Design §2/§8 (BP-4).
package nostr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// (d) The DEFAULT actor (no $RD_ACTOR / "owner") resolves to EXACTLY the legacy
// single-key path (DefaultKeyPath / nostr-identity.json). This is the zero-migration
// guarantee: an existing single-key install's key IS the owner key, loaded unchanged.
func TestActorKeyPath_OwnerIsLegacyPathZeroMigration(t *testing.T) {
	rdHome := t.TempDir()
	legacy := DefaultKeyPath(rdHome)

	for _, actor := range []string{"", OwnerActor} {
		got, err := ActorKeyPath(rdHome, actor)
		if err != nil {
			t.Fatalf("ActorKeyPath(%q): %v", actor, err)
		}
		if got != legacy {
			t.Fatalf("actor %q resolved to %q, want legacy owner path %q", actor, got, legacy)
		}
	}

	// An existing owner key on disk is LOADED unchanged when the default actor is used
	// (no regeneration, no relocation) — the zero-migration property in the concrete.
	pre, err := LoadOrCreatePortfolioKey(legacy, rdHome)
	if err != nil {
		t.Fatalf("seed owner key: %v", err)
	}
	ownerPath, _ := ActorKeyPath(rdHome, OwnerActor)
	post, err := LoadOrCreatePortfolioKey(ownerPath, rdHome)
	if err != nil {
		t.Fatalf("reload owner key: %v", err)
	}
	if pre.PubKeyHex() != post.PubKeyHex() {
		t.Fatalf("default actor regenerated/relocated the owner key: %s != %s", pre.PubKeyHex(), post.PubKeyHex())
	}
}

// (a) A named agent actor selects a DISTINCT on-disk key file (keys/<sanitized>.json)
// with a DISTINCT pubkey from the owner — the core "distinct keys, one host" outcome.
func TestActorKeyPath_NamedAgentIsDistinctKey(t *testing.T) {
	rdHome := t.TempDir()

	ownerPath, err := ActorKeyPath(rdHome, OwnerActor)
	if err != nil {
		t.Fatalf("owner path: %v", err)
	}
	agentPath, err := ActorKeyPath(rdHome, "agent:pm")
	if err != nil {
		t.Fatalf("agent path: %v", err)
	}

	// Design §8: $RD_ACTOR=agent:pm -> $RD_HOME/keys/agent-pm.json.
	wantAgent := filepath.Join(rdHome, "keys", "agent-pm.json")
	if agentPath != wantAgent {
		t.Fatalf("agent key path = %q, want %q", agentPath, wantAgent)
	}
	if agentPath == ownerPath {
		t.Fatalf("agent and owner resolved to the SAME path %q", agentPath)
	}

	owner, err := LoadOrCreatePortfolioKey(ownerPath, rdHome)
	if err != nil {
		t.Fatalf("create owner key: %v", err)
	}
	agent, err := LoadOrCreatePortfolioKey(agentPath, rdHome)
	if err != nil {
		t.Fatalf("create agent key: %v", err)
	}
	if owner.PubKeyHex() == agent.PubKeyHex() {
		t.Fatalf("owner and agent share a pubkey %s — keys are NOT distinct", owner.PubKeyHex())
	}
	// Both files exist on disk, at distinct locations.
	if _, err := os.Stat(ownerPath); err != nil {
		t.Fatalf("owner key not persisted: %v", err)
	}
	if _, err := os.Stat(agentPath); err != nil {
		t.Fatalf("agent key not persisted: %v", err)
	}
	// Reload the agent key: it is durable (per-actor, not per-process) and stable.
	agent2, err := LoadOrCreatePortfolioKey(agentPath, rdHome)
	if err != nil {
		t.Fatalf("reload agent key: %v", err)
	}
	if agent.PubKeyHex() != agent2.PubKeyHex() {
		t.Fatalf("agent key not durable across loads: %s != %s", agent.PubKeyHex(), agent2.PubKeyHex())
	}
}

// SanitizeActorID collapses unsafe characters and makes path traversal impossible —
// no separator and no ".." can survive, so a hostile $RD_ACTOR cannot escape keys/.
func TestSanitizeActorID_SafeFilenameNoTraversal(t *testing.T) {
	cases := map[string]string{
		"agent:pm":       "agent-pm",
		"ceo-automaton":  "ceo-automaton",
		"worker_pool_1":  "worker_pool_1",
		"a.b":            "a-b",
		"../../etc/pass": "------etc-pass",
		"a/b\\c":         "a-b-c",
	}
	for in, want := range cases {
		got, err := SanitizeActorID(in)
		if err != nil {
			t.Fatalf("SanitizeActorID(%q): %v", in, err)
		}
		if got != want {
			t.Fatalf("SanitizeActorID(%q) = %q, want %q", in, got, want)
		}
		if strings.ContainsAny(got, `/\`) || strings.Contains(got, "..") {
			t.Fatalf("sanitized actor %q still contains a traversal token", got)
		}
	}

	// Empty / all-invalid ids are rejected — never silently attributed to a fallback.
	if _, err := SanitizeActorID(""); err == nil {
		t.Fatal("expected error for empty actor id")
	}
	if _, err := SanitizeActorID("/../"); err == nil {
		t.Fatal("expected error for all-invalid actor id")
	}

	// A traversal-looking actor id still yields a path confined UNDER keys/.
	rdHome := t.TempDir()
	p, err := ActorKeyPath(rdHome, "../../evil")
	if err != nil {
		t.Fatalf("ActorKeyPath(traversal): %v", err)
	}
	keysDir := filepath.Join(rdHome, "keys")
	if !strings.HasPrefix(filepath.Clean(p), keysDir+string(filepath.Separator)) {
		t.Fatalf("traversal actor escaped keys/: %q not under %q", p, keysDir)
	}
}
