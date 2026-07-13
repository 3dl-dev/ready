package main

// engage_test.go — nostr-native, store-free playbook + engage integration tests
// (ready-a4a). Proves the rebuilt surface:
//   - rd playbook create -> rd playbook list round-trips via .ready/playbooks.jsonl
//   - rd engage instantiates N items + dep edges into the nostr log (asserted via
//     the projection), with NO .cf ever written (assertNoDotCf).

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/campfire-net/ready/pkg/playbook"
	rdSync "github.com/campfire-net/ready/pkg/sync"
	"github.com/campfire-net/ready/pkg/state"
)

// TestPlaybookCreateList_RoundTrip drives the real playbook create/list cobra
// commands on a nostr-native project and proves the template round-trips through
// .ready/playbooks.jsonl — no campfire store, no .cf.
func TestPlaybookCreateList_RoundTrip(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)

	itemsFile := filepath.Join(t.TempDir(), "items.json")
	itemsJSON := `[
	  {"title":"Triage {{env}}","type":"task","priority":"p0"},
	  {"title":"Postmortem","type":"task","priority":"p2","deps":[0]}
	]`
	if err := os.WriteFile(itemsFile, []byte(itemsJSON), 0o600); err != nil {
		t.Fatalf("write items file: %v", err)
	}

	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("flag set: %v", err)
		}
	}
	must(playbookCreateCmd.Flags().Set("id", "sre-incident"))
	must(playbookCreateCmd.Flags().Set("items-file", itemsFile))
	must(playbookCreateCmd.Flags().Set("description", "incident response"))
	t.Cleanup(func() {
		_ = playbookCreateCmd.Flags().Set("id", "")
		_ = playbookCreateCmd.Flags().Set("items-file", "")
		_ = playbookCreateCmd.Flags().Set("description", "")
	})

	if err := playbookCreateCmd.RunE(playbookCreateCmd, []string{"SRE Incident"}); err != nil {
		t.Fatalf("playbook create RunE: %v", err)
	}

	// The JSONL file must exist under .ready/ — store-free, project-local.
	pbPath := filepath.Join(dir, rdSync.ReadyDir, playbook.PlaybooksFile)
	if _, err := os.Stat(pbPath); err != nil {
		t.Fatalf("expected %s to exist after playbook create: %v", pbPath, err)
	}

	// list reads it back with the dep edge + description intact.
	store := playbooksStore(dir)
	got, err := store.List()
	if err != nil {
		t.Fatalf("store.List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("List = %d playbooks; want 1", len(got))
	}
	pb := got[0]
	if pb.ID != "sre-incident" || pb.Title != "SRE Incident" || pb.Description != "incident response" {
		t.Fatalf("round-trip mismatch: %+v", pb)
	}
	if len(pb.Items) != 2 || len(pb.Items[1].Deps) != 1 || pb.Items[1].Deps[0] != 0 {
		t.Fatalf("dep edge lost on round-trip: %+v", pb.Items)
	}

	// playbook list RunE must succeed on the same project.
	if err := playbookListCmd.RunE(playbookListCmd, nil); err != nil {
		t.Fatalf("playbook list RunE: %v", err)
	}

	assertNoDotCf(t)
}

// TestEngage_InstantiatesItemsAndDepEdges drives rd engage on a nostr-native
// project and proves it materializes N items + the dep edge into the nostr
// projection, attributed to the secp256k1 signer, with NO .cf written.
func TestEngage_InstantiatesItemsAndDepEdges(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)

	// Register a 2-item template store-free (item[1] depends on item[0]).
	tmpl := &playbook.PlaybookTemplate{
		ID:    "deploy",
		Title: "Deploy",
		Items: []playbook.TemplateItem{
			{Title: "Build {{svc}}", Type: "task", Priority: "p1"},
			{Title: "Release {{svc}}", Type: "task", Priority: "p1", Deps: []int{0}},
		},
	}
	if err := playbooksStore(dir).Add(tmpl); err != nil {
		t.Fatalf("Add template: %v", err)
	}

	if err := engageCmd.Flags().Set("var", "svc=api"); err != nil {
		t.Fatalf("set --var: %v", err)
	}
	t.Cleanup(func() { _ = engageCmd.Flags().Set("var", "") })

	if err := engageCmd.RunE(engageCmd, []string{"deploy"}); err != nil {
		t.Fatalf("engage RunE: %v", err)
	}

	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}

	// Locate the two instantiated items by their substituted titles.
	var build, release *state.Item
	for _, it := range byID {
		switch it.Title {
		case "Build api":
			build = it
		case "Release api":
			release = it
		}
	}
	if build == nil || release == nil {
		t.Fatalf("engage did not create both items (build=%v release=%v) in the projection", build, release)
	}

	// Variable substitution proves the {{svc}} placeholder resolved.
	if build.Title != "Build api" || release.Title != "Release api" {
		t.Fatalf("variable substitution failed: build=%q release=%q", build.Title, release.Title)
	}

	// The dep edge landed: Release is blocked by Build.
	if !sliceContains(release.BlockedBy, build.ID) {
		t.Fatalf("dep edge not instantiated: release %s BlockedBy = %v; want to contain build %s",
			release.ID, release.BlockedBy, build.ID)
	}
	// Build has no incoming dep.
	if len(build.BlockedBy) != 0 {
		t.Fatalf("build %s should have no dep edge, got BlockedBy = %v", build.ID, build.BlockedBy)
	}

	// Attributed to the secp256k1 signer (default --for = signer).
	if build.For != owner || release.For != owner {
		t.Fatalf("engaged items not attributed to signer %q: build.For=%q release.For=%q", owner, build.For, release.For)
	}
	// Build (no incoming dep) starts in inbox; Release projects to blocked because
	// its dep on the still-open Build is honored by the projection — the dep edge
	// is live, not just recorded.
	if build.Status != state.StatusInbox {
		t.Fatalf("build status = %q; want inbox", build.Status)
	}
	if release.Status != state.StatusBlocked {
		t.Fatalf("release status = %q; want blocked (its dep on open Build must gate it)", release.Status)
	}

	assertNoDotCf(t)
}

// TestEngage_PlaybookNotFound proves engage errors cleanly when the playbook is
// absent from .ready/playbooks.jsonl (no panic, no .cf).
func TestEngage_PlaybookNotFound(t *testing.T) {
	setupNostrNativeProject(t)
	if err := engageCmd.RunE(engageCmd, []string{"does-not-exist"}); err == nil {
		t.Fatalf("engage of missing playbook = nil error; want not-found")
	}
	assertNoDotCf(t)
}
