package main

// docs_relay_runbook_currency_test.go — ready-906 done-condition tests: two
// mechanically-checkable claims in docs/relay-runbook.md that must stay in
// sync with the code they describe, so drift in either direction goes red
// instead of silently going stale.

import (
	"os"
	"path/filepath"
	"reflect"
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

// rolloutSection extracts the "Updating relay_endpoints on an existing
// machine" section's body (up to the next "## " heading) from the runbook,
// shared by every test that inspects that section's content.
func rolloutSection(t *testing.T, doc string) string {
	t.Helper()
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
	return rest[:end]
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
	section := rolloutSection(t, doc)

	if !strings.Contains(section, "board.json") {
		t.Errorf("the rollout section never mentions .ready/board.json — a committed board binding (ready-f12) shadows the home rd.json edit this section describes, and the section must say so")
	}
	if !strings.Contains(section, "config.json") {
		t.Errorf("the rollout section never mentions a project's .ready/config.json, which also shadows the home rd.json edit")
	}
}

// TestRelayRunbook_ConfigJSONPrecedesBoardBindingAtSameLevel guards the
// rollout section's PRECEDENCE claim against the REAL resolution code, not
// the doc's prose: "At a given directory, a declaring .ready/config.json
// wins over that level's .ready/board.json; either one wins over the home
// file." A previous round's guard only string-matched "board.json" and
// "config.json" appearing SOMEWHERE in the section — coupled to doc text,
// never to relayPolicyAt (cmd/rd/nostr.go), so swapping the two lookup
// blocks inside it left every test green while silently reversing the
// precedence the doc promises operators.
//
// This test builds ALL THREE layers for real — a home rd.json, a committed
// .ready/board.json, and a machine-local .ready/config.json, each declaring
// a DISTINCT, distinguishable relay — and calls the actual
// resolveRelayConfig()/relayPolicyAt() cascade. It asserts the winner is
// exactly the config.json relay, matching the doc's stated precedence.
// Mutation-proof: swapping relayPolicyAt's config.json/board.json blocks (as
// the adversary did) flips the winner to board.json's relay and this test
// goes red — see the mutation note in the ready-906 item trail for the
// exact command and failure text.
func TestRelayRunbook_ConfigJSONPrecedesBoardBindingAtSameLevel(t *testing.T) {
	dir := isolatedProject(t)

	homeDir := RDHome()
	if err := os.MkdirAll(homeDir, 0o700); err != nil {
		t.Fatalf("mkdir RD_HOME: %v", err)
	}
	homeCfg := &rdconfig.Config{
		RelayEndpoints: []rdconfig.RelayEndpoint{{URL: "wss://home.example.test", Read: true, Write: true}},
	}
	if err := rdconfig.Save(homeDir, homeCfg); err != nil {
		t.Fatalf("saving home rd.json: %v", err)
	}

	// The committed board.json declares a relay at the SAME directory as the
	// config.json below — it must shadow the home file but LOSE to config.json.
	binding := &rdconfig.BoardBinding{
		RelayEndpoints: []rdconfig.RelayEndpoint{{URL: "wss://board.example.test", Read: true, Write: true}},
	}
	if err := rdconfig.SaveBoardBinding(dir, binding); err != nil {
		t.Fatalf("SaveBoardBinding: %v", err)
	}

	// The machine-local config.json declares at the SAME level — per the doc,
	// this must be the winner over both board.json and the home file.
	syncCfg := &rdconfig.SyncConfig{
		RelayEndpoints: []rdconfig.RelayEndpoint{{URL: "wss://config.example.test", Read: true, Write: true}},
	}
	if err := rdconfig.SaveSyncConfig(dir, syncCfg); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}

	eps := resolveRelayConfig()
	if len(eps) != 1 || eps[0].URL != "wss://config.example.test" {
		t.Fatalf("resolveRelayConfig() = %+v, want exactly [wss://config.example.test] — "+
			"the doc's precedence claim (config.json > board.json > home rd.json) is violated by the actual code",
			eps)
	}
}

// boardBindingWriters enumerates every non-test source file in this package
// that calls rdconfig.SaveBoardBinding, mapped to the CLI command name an
// operator following the runbook would recognize. This is the enumeration
// the rollout section's board.json bullet must name in full. A previous
// round's parenthetical named only "`rd init` or `rd link`", omitting `rd
// follow` (cmd/rd/follow.go -> bindFollowedBoard -> SaveBoardBinding) — an
// unguarded partial claim that would misdirect an operator on a
// follow-created project into editing the wrong file. Rather than
// hand-count writers again, TestRelayRunbook_BoardBindingWriterCountMatchesCode
// below re-derives the SET from the real source on every run, so a fourth
// writer added anywhere in this package — without updating this map — fails
// that test instead of silently becoming a fourth unguarded partial claim.
var boardBindingWriters = map[string]string{
	"init.go":        "rd init",
	"nostr_grant.go": "rd link",
	"follow.go":      "rd follow",
}

// TestRelayRunbook_BoardBindingWriterCountMatchesCode greps every non-test
// .go file in this package for a real SaveBoardBinding( call site and
// asserts the set matches boardBindingWriters exactly — not by name, by
// grepping the actual source. Add or remove a writer without updating the
// map (and, per the next test, the doc) and this goes red.
func TestRelayRunbook_BoardBindingWriterCountMatchesCode(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	got := map[string]bool{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		b, err := os.ReadFile(filepath.Join(".", name))
		if err != nil {
			t.Fatalf("reading %s: %v", name, err)
		}
		if strings.Contains(string(b), "SaveBoardBinding(") {
			got[name] = true
		}
	}
	want := map[string]bool{}
	for f := range boardBindingWriters {
		want[f] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("real SaveBoardBinding call sites %v != enumerated boardBindingWriters %v — "+
			"a writer was added or removed in cmd/rd without updating boardBindingWriters (and the "+
			"runbook's rollout section, guarded by TestRelayRunbook_RolloutSectionNamesAllBoardBindingWriters)",
			got, want)
	}
}

// TestRelayRunbook_RolloutSectionNamesAllBoardBindingWriters asserts the
// rollout section's board.json bullet names EVERY command in
// boardBindingWriters, not just the ones a previous round happened to know
// about.
func TestRelayRunbook_RolloutSectionNamesAllBoardBindingWriters(t *testing.T) {
	doc := readRelayRunbook(t)
	section := rolloutSection(t, doc)

	for file, cmd := range boardBindingWriters {
		if !strings.Contains(section, cmd) {
			t.Errorf("the rollout section never mentions %q, which writes .ready/board.json (%s) — "+
				"an operator on a project bound by that command will misjudge which file governs", cmd, file)
		}
	}
}
