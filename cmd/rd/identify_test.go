package main

import (
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/identity"
	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
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
