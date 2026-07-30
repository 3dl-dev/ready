package main

// parentid_required_test.go — regression coverage for ready-4140.
//
// THE DEFECT: cmd/rd/create.go registered --parent-id with default "" and
// never checked it was actually supplied. Omitting the flag was silently
// valid, so every item created without it was born an orphan. Measured
// 2026-07-30 (rd list --json --all piped through a parent_id counter):
// dontguess 187/642 orphans (29%), enterprise_ai_framework 108/118 (92%).
//
// THE FIX (create.go RunE): --parent-id is now required. Omitting it (flag
// never Changed) is rejected with a usage error before any nostr write is
// attempted. "none" remains the explicit, documented spelling for "no
// parent" (see parentid.go's isParentIDNone / resolveParentIDField, shared
// with `rd update --parent-id none`).
//
// This test asserts both halves of the DONE CONDITION on the REAL createCmd
// RunE (not a standalone mirror), per this file's established convention
// (type_alias_test.go, validate_flags_test.go do the same for other flags).

import (
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// resetCreateFlag forces flag `name` on the shared global createCmd back to
// value with Changed=false — i.e. the "never supplied on this invocation"
// state. This is needed because pflag.FlagSet.Set ALWAYS marks Changed=true
// (see flag.go: "if !flag.Changed { ... flag.Changed = true }" — Changed is
// latched, never unset by Set), and createCmd is a package-level global
// reused across the whole test binary. Other tests in this package
// legitimately call createCmd.Flags().Set("parent-id", ...) to exercise the
// accept path, which permanently latches Changed=true for the rest of the
// process unless something reaches past FlagSet.Set to the underlying
// pflag.Value directly (which does not touch Changed) and clears the
// Flag.Changed field itself. Without this, "absent flag" cannot be
// faithfully simulated once any earlier test has touched the same flag.
func resetCreateFlag(t *testing.T, name, val string) {
	t.Helper()
	f := createCmd.Flags().Lookup(name)
	if f == nil {
		t.Fatalf("flag %q not registered on createCmd", name)
	}
	if err := f.Value.Set(val); err != nil {
		t.Fatalf("resetting flag %q value: %v", name, err)
	}
	f.Changed = false
}

// TestCreate_ParentIDAbsent_Rejected verifies the DONE CONDITION's first
// half: omitting --parent-id entirely fails with a usage error, before ever
// reaching the nostr-native write path (isolateTempDir has no pinned board,
// so any pass-through would surface as a DIFFERENT error — "not a ready
// project" / "no pinned board" — not the parent-id message asserted below).
func TestCreate_ParentIDAbsent_Rejected(t *testing.T) {
	isolateTempDir(t)

	if err := createCmd.Flags().Set("type", "task"); err != nil {
		t.Fatalf("setting --type: %v", err)
	}
	if err := createCmd.Flags().Set("priority", "p1"); err != nil {
		t.Fatalf("setting --priority: %v", err)
	}
	// Simulate --parent-id never having been passed on this invocation.
	resetCreateFlag(t, "parent-id", "")
	t.Cleanup(func() {
		_ = createCmd.Flags().Set("type", "")
		_ = createCmd.Flags().Set("priority", "")
		resetCreateFlag(t, "parent-id", "")
	})

	err := createCmd.RunE(createCmd, []string{"Orphan attempt"})
	if err == nil {
		t.Fatal("createCmd.RunE: expected error when --parent-id is omitted, got nil")
	}
	if !strings.Contains(err.Error(), "--parent-id") {
		t.Errorf("error should mention --parent-id, got: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "required") {
		t.Errorf("error should say --parent-id is required, got: %q", err.Error())
	}
	// Must mention the escape hatch so an agent hitting this error can
	// self-correct without needing to read the source.
	if !strings.Contains(err.Error(), "none") {
		t.Errorf("error should mention the 'none' sentinel for explicit root items, got: %q", err.Error())
	}
}

// TestCreate_ParentIDEmptyString_Rejected covers the loophole an
// absent-only check (e.g. relying solely on cobra's MarkFlagRequired, which
// only asks "was Set() ever called") would miss: --parent-id explicitly set
// to "" is Changed=true but carries no real decision, and would otherwise
// fall through resolveParentIDField's "" => no-parent short-circuit exactly
// like the pre-fix default did. It must be rejected the same as an absent
// flag, not silently treated as "none".
func TestCreate_ParentIDEmptyString_Rejected(t *testing.T) {
	isolateTempDir(t)

	if err := createCmd.Flags().Set("type", "task"); err != nil {
		t.Fatalf("setting --type: %v", err)
	}
	if err := createCmd.Flags().Set("priority", "p1"); err != nil {
		t.Fatalf("setting --priority: %v", err)
	}
	// Explicitly pass an empty value (Changed=true, value="") — distinct from
	// never touching the flag at all.
	if err := createCmd.Flags().Set("parent-id", ""); err != nil {
		t.Fatalf("setting --parent-id: %v", err)
	}
	t.Cleanup(func() {
		_ = createCmd.Flags().Set("type", "")
		_ = createCmd.Flags().Set("priority", "")
		resetCreateFlag(t, "parent-id", "")
	})

	err := createCmd.RunE(createCmd, []string{"Empty parent-id attempt"})
	if err == nil {
		t.Fatal("createCmd.RunE: expected error for --parent-id \"\", got nil")
	}
	if !strings.Contains(err.Error(), "--parent-id") {
		t.Errorf("error should mention --parent-id, got: %q", err.Error())
	}
}

// TestCreate_ParentIDNone_ExplicitlyAccepted verifies the DONE CONDITION's
// second half: --parent-id none remains valid and is the explicit way to
// create a root item — end to end, through the real nostr-native write path,
// asserting the created item actually lands with an empty ParentID (not
// merely that RunE returned no error).
func TestCreate_ParentIDNone_ExplicitlyAccepted(t *testing.T) {
	setupNostrNativeProject(t)

	if err := createCmd.Flags().Set("type", "task"); err != nil {
		t.Fatalf("setting --type: %v", err)
	}
	if err := createCmd.Flags().Set("priority", "p1"); err != nil {
		t.Fatalf("setting --priority: %v", err)
	}
	if err := createCmd.Flags().Set("parent-id", "none"); err != nil {
		t.Fatalf("setting --parent-id: %v", err)
	}
	t.Cleanup(func() {
		_ = createCmd.Flags().Set("type", "")
		_ = createCmd.Flags().Set("priority", "")
		resetCreateFlag(t, "parent-id", "")
	})

	const title = "Explicit root item (ready-4140)"
	if err := createCmd.RunE(createCmd, []string{title}); err != nil {
		t.Fatalf("createCmd.RunE with --parent-id none: %v", err)
	}

	items, _, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("nostrProjectAllItems: %v", err)
	}
	var found *state.Item
	for _, it := range items {
		if it.Title == title {
			found = it
		}
	}
	if found == nil {
		t.Fatalf("created item %q not found in projection", title)
	}
	if found.ParentID != "" {
		t.Errorf("ParentID = %q, want empty (explicit root) after --parent-id none", found.ParentID)
	}
}
