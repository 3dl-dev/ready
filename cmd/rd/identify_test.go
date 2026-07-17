package main

import (
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/identity"
	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// TestIdentify_RequiresEmail verifies `rd identify` refuses without --add-email:
// the party handle is an email, so an alias with no email has no addressable slot.
func TestIdentify_RequiresEmail(t *testing.T) {
	cmd := identifyCmd
	cmd.SetArgs(nil)
	t.Cleanup(func() {
		_ = cmd.Flags().Set("add-email", "")
		_ = cmd.Flags().Set("add-key", "")
		_ = cmd.Flags().Set("label", "")
	})
	err := cmd.RunE(cmd, nil)
	if err == nil {
		t.Fatal("identify with no --add-email succeeded; want refusal")
	}
	if !strings.Contains(err.Error(), "add-email") {
		t.Errorf("refusal error %q does not mention --add-email", err.Error())
	}
}

// TestNextAliasCreatedAt_MonotonicPerHandle verifies the created_at stamper is
// strictly monotonic per party handle so a re-published alias supersedes (never
// ties) the one it replaces, and is scoped to the handle (a different handle's
// alias does not inflate this slot).
func TestNextAliasCreatedAt_MonotonicPerHandle(t *testing.T) {
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	future := time.Now().Unix() + 10_000
	ev, err := identity.BuildAliasEvent(k, identity.AliasSpec{
		Handle:  "baron@3dl.dev",
		Pubkeys: []string{k.PubKeyHex()},
		Emails:  []string{"baron@3dl.dev"},
	}, future)
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}
	// A different-handle alias that must NOT affect the baron@3dl.dev slot.
	other, err := identity.BuildAliasEvent(k, identity.AliasSpec{
		Handle:  "other@example.com",
		Pubkeys: []string{k.PubKeyHex()},
		Emails:  []string{"other@example.com"},
	}, future+50_000)
	if err != nil {
		t.Fatalf("BuildAliasEvent other: %v", err)
	}

	log := rdSync.NewNostrLog(t.TempDir() + "/nostr-log.jsonl")
	if err := log.Append(ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := log.Append(other); err != nil {
		t.Fatalf("Append other: %v", err)
	}

	got := nextAliasCreatedAt(log, "baron@3dl.dev")
	if got != future+1 {
		t.Fatalf("nextAliasCreatedAt = %d, want %d (newest same-handle + 1)", got, future+1)
	}
}

// TestIdentify_AddKeyNormalizedToLowercase is the ready-b66 hardening (2): a
// pubkey supplied via --add-key in mixed/upper case (e.g. pasted from a UI that
// upper-cases hex) must be normalized to lowercase before it lands in the
// published alias's "p" tag. nostr.Key.PubKeyHex() ALWAYS returns lowercase, so
// an uppercase p-tag can never match that key's own future signed events — the
// key would be silently unreachable from its own alias's trust closure. This
// drives the REAL `rd identify` RunE against a real nostr-native project (no mock
// of the code under test) and reads the published event back off the real
// NostrLog to assert the wire-level tag is lowercase.
//
// A fresh cobra.Command (mirroring identifyCmd's own flag registrations) is built
// and passed directly to identifyCmd.RunE — RunE reads flags off the `cmd`
// argument it is given, so this isolates the test from the shared global
// identifyCmd's flag state (which other tests in this file also mutate).
func TestIdentify_AddKeyNormalizedToLowercase(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)
	_ = owner

	other, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	lower := other.PubKeyHex()
	upper := strings.ToUpper(lower)
	if lower == upper {
		t.Fatalf("test key %q has no letters to case-flip; pick a different fixture", lower)
	}

	cmd := &cobra.Command{Use: "identify"}
	cmd.Flags().StringArray("add-email", nil, "")
	cmd.Flags().StringArray("add-key", nil, "")
	cmd.Flags().String("label", "", "")
	if err := cmd.Flags().Set("add-email", "baron@3dl.dev"); err != nil {
		t.Fatalf("set add-email: %v", err)
	}
	if err := cmd.Flags().Set("add-key", upper); err != nil {
		t.Fatalf("set add-key: %v", err)
	}

	if err := identifyCmd.RunE(cmd, nil); err != nil {
		t.Fatalf("identify RunE: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var found *nostr.Event
	for _, e := range events {
		if e.Kind == identity.KindPersonAlias {
			found = e
		}
	}
	if found == nil {
		t.Fatal("no person-alias event was published")
	}
	var pTags []string
	for _, tg := range found.Tags {
		if len(tg) >= 2 && tg[0] == "p" {
			pTags = append(pTags, tg[1])
		}
	}
	if strings.Contains(strings.Join(pTags, ","), upper) {
		t.Fatalf("published p tags = %v still contain the UPPERCASE --add-key %q, want normalized lowercase", pTags, upper)
	}
	foundLower := false
	for _, p := range pTags {
		if p == lower {
			foundLower = true
		}
	}
	if !foundLower {
		t.Fatalf("published p tags = %v do not contain the normalized lowercase key %q — uppercase --add-key was silently dropped", pTags, lower)
	}
}
