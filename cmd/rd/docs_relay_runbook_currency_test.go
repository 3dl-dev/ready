package main

// docs_relay_runbook_currency_test.go — ready-906 done-condition tests: two
// mechanically-checkable claims in docs/relay-runbook.md that must stay in
// sync with the code they describe, so drift in either direction goes red
// instead of silently going stale.

import (
	"os"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/rdconfig"
)

func readRelayRunbook(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile("../../docs/relay-runbook.md")
	if err != nil {
		t.Fatalf("reading docs/relay-runbook.md: %v", err)
	}
	return string(b)
}

// TestRelayRunbook_DefaultRelaysClaimMatchesCode: the runbook's "Endpoint
// config for rd" section asserts `rdconfig.DefaultRelays()` returns NONE (the
// ship default is local-only). That claim is code-coupled and previously
// drifted silently for the entire lifetime of the existing doc-currency
// suite — none of those tests touch this sentence. Guard both directions:
// if the doc's claim goes stale (code starts returning a baked-in default,
// or the doc is edited to claim otherwise) OR the code regresses to ship a
// default relay, this goes red.
func TestRelayRunbook_DefaultRelaysClaimMatchesCode(t *testing.T) {
	doc := readRelayRunbook(t)
	claimsNone := strings.Contains(doc, "`rdconfig.DefaultRelays()` returns **none**")
	codeReturnsNone := len(rdconfig.DefaultRelays()) == 0

	if claimsNone != codeReturnsNone {
		t.Errorf("doc claims DefaultRelays()==none: %v; code returns %d relays", claimsNone, len(rdconfig.DefaultRelays()))
	}
	if !claimsNone {
		t.Errorf("docs/relay-runbook.md no longer states the exact claim this test guards (`rdconfig.DefaultRelays()` returns **none**) — update the doc text or this test together")
	}
}

// TestRelayRunbook_RolloutSectionCoversBoardBinding: the "Updating
// relay_endpoints on an existing machine" section must name BOTH shadowing
// sources ahead of the home rd.json — a project's local .ready/config.json
// AND the project's COMMITTED .ready/board.json (ready-f12) — because
// resolveRelayConfig (cmd/rd/nostr.go) walks up from cwd and either file
// stops the walk before the home default is ever consulted. An operator who
// only edits ~/.config/rd/rd.json on a project carrying board.json relays
// sees no effect; the runbook must say so.
func TestRelayRunbook_RolloutSectionCoversBoardBinding(t *testing.T) {
	doc := readRelayRunbook(t)

	const marker = "## Updating relay_endpoints on an existing machine"
	start := strings.Index(doc, marker)
	if start == -1 {
		t.Fatalf("docs/relay-runbook.md is missing the %q section entirely", marker)
	}
	rest := doc[start+len(marker):]
	end := strings.Index(rest, "\n## ")
	if end == -1 {
		end = len(rest)
	}
	section := rest[:end]

	if !strings.Contains(section, "board.json") {
		t.Errorf("the %q section never mentions .ready/board.json — a committed board binding (ready-f12) shadows the home rd.json edit this section describes, and the section must say so", marker)
	}
	if !strings.Contains(section, "config.json") {
		t.Errorf("the %q section never mentions a project's .ready/config.json, which also shadows the home rd.json edit", marker)
	}
}
