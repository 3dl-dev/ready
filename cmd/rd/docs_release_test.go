package main

// docs/releasing.md is the procedure a cold start follows to build the board and cut
// a release. This file holds it to the repository.
//
// WHY A GUARD AT ALL. Two documents in this repo have already gone false while every
// test stayed green: the relay runbook described an architecture that had been
// replaced (ready-906), and a cert-renewal section shipped an `az` command that did
// not execute (ready-199). Both read plausibly. The lesson taken from them, and
// applied here, is that a doc claim which is mechanically checkable MUST be checked —
// a doc asserting something false is worse than a missing doc, because it is followed.
//
// WHAT THIS DELIBERATELY DOES NOT CHECK: prose, rationale, or ordering. Those are for
// a human. It checks only the claims that are facts about this repository — trigger
// paths, commands, filenames — so the doc cannot quietly stop describing the
// workflows it is about.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func readRepoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", rel))
	if err != nil {
		t.Fatalf("read %s: %v", rel, err)
	}
	return string(b)
}

// TestReleasingDoc_DescribesTheWorkflowsThatActuallyExist pins every claim in
// docs/releasing.md that is a fact about this tree rather than an opinion about it.
func TestReleasingDoc_DescribesTheWorkflowsThatActuallyExist(t *testing.T) {
	doc := readRepoFile(t, "docs/releasing.md")
	pages := readRepoFile(t, ".github/workflows/pages.yml")
	release := readRepoFile(t, ".github/workflows/release.yml")

	// The two workflow files the doc routes the reader to must be the two that carry
	// these triggers. If either moves, the doc's table is a dead end.
	for _, c := range []struct{ claim, in, file string }{
		{".github/workflows/pages.yml", pages, "the doc names pages.yml as the board's deploy workflow"},
		{".github/workflows/release.yml", release, "the doc names release.yml as the binary release workflow"},
	} {
		if !strings.Contains(doc, c.claim) {
			t.Errorf("docs/releasing.md no longer names %s — %s", c.claim, c.file)
		}
	}

	// THE DEPLOY TRIGGER. The doc tells a reader that merging to main IS the deploy,
	// for changes under site/ or web/. If those path filters change, following the doc
	// means merging and waiting for a deploy that never comes.
	if !strings.Contains(pages, "branches:") || !strings.Contains(pages, "- main") {
		t.Error("pages.yml no longer deploys on push to main, but docs/releasing.md says merging to main is the deploy")
	}
	for _, p := range []string{"'site/**'", "'web/**'"} {
		if !strings.Contains(pages, p) {
			t.Errorf("pages.yml no longer filters on %s, but docs/releasing.md tells the reader that path triggers the board deploy", p)
		}
	}

	// THE RELEASE TRIGGER. The doc's entire release procedure is "push a v* tag".
	if !strings.Contains(release, "tags:") || !strings.Contains(release, "'v*'") {
		t.Error("release.yml no longer fires on a v* tag, but docs/releasing.md gives `git tag vX.Y.Z && git push origin vX.Y.Z` as the whole trigger")
	}

	// THE RELEASE->PAGES HAND-OFF, and the warning attached to it. `release:
	// published` never fires because the release is authored by GITHUB_TOKEN; the
	// workflow_run trigger exists for that reason. If someone "fixes" it to the
	// obvious-looking wiring, the doc's warning becomes actively misleading and the
	// site stops picking up version stamps.
	if !strings.Contains(pages, "workflow_run:") {
		t.Error("pages.yml no longer uses workflow_run, but docs/releasing.md explains at length why it must — and warns against replacing it with `release: types: [published]`, which never fires")
	}

	// THE BUILD COMMANDS. `npm ci` (not `npm install`) is what CI runs and what the
	// doc tells a human to run; they must not diverge.
	if !strings.Contains(pages, "npm ci") {
		t.Error("pages.yml no longer runs `npm ci`, but docs/releasing.md tells the reader to use it BECAUSE that is what CI runs")
	}
	if !strings.Contains(doc, "npm ci") || !strings.Contains(doc, "npx vitest run") {
		t.Error("docs/releasing.md no longer carries the build/test commands it exists to give")
	}

	// THE LIVE SCRIPTS AND THEIR RECEIPTS. The doc's central claim is that a receipt
	// is the only durable evidence these scripts ever passed. Both scripts and the
	// receipt guard must exist for that to be actionable advice.
	for _, rel := range []string{
		"web/board/scripts/live-write-roundtrip.mjs",
		"web/board/scripts/live-roundtrip-both-ways.mjs",
		"web/board/receipts_test.go",
	} {
		if _, err := os.Stat(filepath.Join("..", "..", rel)); err != nil {
			t.Errorf("docs/releasing.md sends the reader to %s, which does not exist: %v", rel, err)
		}
		if !strings.Contains(doc, filepath.Base(rel)) {
			t.Errorf("docs/releasing.md no longer mentions %s", filepath.Base(rel))
		}
	}
}
