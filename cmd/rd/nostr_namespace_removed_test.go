package main

// nostr_namespace_removed_test.go — regression gate for ready-f58: the `rd nostr`
// namespace is GONE. nostr is the substrate, not a user-typed mode. The duplicate
// subcommands were deleted (grant/revoke/migrate/parity/show/ready), pin-board was
// promoted to top-level, and the surviving relay/log operations moved under the
// new `rd relay` and `rd log` namespaces. This test fails loudly if any of that
// regresses.

import (
	"testing"

	"github.com/spf13/cobra"
)

// findChild returns the immediate subcommand of parent whose Name() == name, or nil.
func findChild(parent *cobra.Command, name string) *cobra.Command {
	for _, c := range parent.Commands() {
		if c.Name() == name {
			return c
		}
	}
	return nil
}

// TestNostrNamespaceGone asserts there is no `rd nostr` command anywhere in the
// tree — a user must never be able to type `rd nostr`.
func TestNostrNamespaceGone(t *testing.T) {
	if c := findChild(rootCmd, "nostr"); c != nil {
		t.Fatalf("`rd nostr` still registered (%s); the nostr namespace must be gone", c.CommandPath())
	}
}

// TestPromotedAndNewNamespacesExist asserts the surviving surfaces are reachable
// at their new locations.
func TestPromotedAndNewNamespacesExist(t *testing.T) {
	// pin-board promoted to top-level.
	if findChild(rootCmd, "pin-board") == nil {
		t.Error("`rd pin-board` missing — pin-board must be promoted to top-level")
	}

	// `rd relay` namespace with sync-allowlist + flush.
	relay := findChild(rootCmd, "relay")
	if relay == nil {
		t.Fatal("`rd relay` namespace missing")
	}
	for _, sub := range []string{"sync-allowlist", "flush"} {
		if findChild(relay, sub) == nil {
			t.Errorf("`rd relay %s` missing", sub)
		}
	}

	// `rd log` namespace with merge-log + publish + put.
	logc := findChild(rootCmd, "log")
	if logc == nil {
		t.Fatal("`rd log` namespace missing")
	}
	for _, sub := range []string{"merge-log", "publish", "put"} {
		if findChild(logc, sub) == nil {
			t.Errorf("`rd log %s` missing", sub)
		}
	}
}

// TestMigratedUniqueFlagsPreserved asserts the unique capabilities that used to
// live only on deleted `rd nostr` subcommands survived onto their top-level hosts.
func TestMigratedUniqueFlagsPreserved(t *testing.T) {
	// `rd nostr show --reconcile` -> `rd show --reconcile`.
	if showCmd.Flags().Lookup("reconcile") == nil {
		t.Error("`rd show --reconcile` missing — the unique reconcile flag from `rd nostr show` was lost")
	}
	// `rd nostr ready --reconcile` -> `rd ready --reconcile`.
	if readyCmd.Flags().Lookup("reconcile") == nil {
		t.Error("`rd ready --reconcile` missing — the unique reconcile flag from `rd nostr ready` was lost")
	}
	// `rd nostr revoke --from` (retroactive repudiation) -> `rd revoke --from`.
	if revokeCmd.Flags().Lookup("from") == nil {
		t.Error("`rd revoke --from` missing — the retroactive repudiation flag from `rd nostr revoke` was lost")
	}
}
