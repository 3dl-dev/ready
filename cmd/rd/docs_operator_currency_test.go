package main

// docs_operator_currency_test.go — ready-290 done-condition test: the
// operator-facing docs (relay-runbook.md, nostr-signing.md,
// convention/work-management.md) are current with v0.16.2 — no doc presents
// `rd relay sync-allowlist` as part of normal onboarding, `rd pin-board` as
// the user-facing command (it's a hidden alias for `rd link` now), a
// required-role `rd grant <pubkey> <role>` flow, or campfire as the CURRENT
// coordination substrate. docs/design/* is explicitly out of scope — those
// are point-in-time design records where campfire references are legitimate
// archival history.

import (
	"os"
	"strings"
	"testing"
)

func readDocOrFatalOC(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// TestRelayRunbook_UsesLinkNotPinBoard: pin-board is a hidden alias for `rd
// link` now (ready-b58/link rename). The runbook must tell operators to pin
// a board with the real user-facing command, `rd link`, not `rd pin-board`.
func TestRelayRunbook_UsesLinkNotPinBoard(t *testing.T) {
	doc := readDocOrFatalOC(t, "../../docs/relay-runbook.md")
	if strings.Contains(doc, "rd pin-board") {
		t.Errorf("docs/relay-runbook.md still tells operators to run `rd pin-board` — that's a hidden alias now, use `rd link`")
	}
	if !strings.Contains(doc, "rd link") {
		t.Errorf("docs/relay-runbook.md does not mention `rd link` (the current board-pinning command)")
	}
}

// TestRelayRunbook_SyncAllowlistFramedOptional: `rd relay sync-allowlist` is
// an OPTIONAL, advanced tool for operators who run their OWN locked relay.
// `rd grant <pubkey>` alone authorizes on any public relay (rd enforces
// write-authz app-side). The runbook must say so explicitly and must not
// read like sync-allowlist is a required step to invite someone.
func TestRelayRunbook_SyncAllowlistFramedOptional(t *testing.T) {
	doc := readDocOrFatalOC(t, "../../docs/relay-runbook.md")
	lower := strings.ToLower(doc)

	if !strings.Contains(lower, "optional") {
		t.Errorf("docs/relay-runbook.md never says the sync-allowlist workflow is OPTIONAL")
	}
	if !strings.Contains(lower, "own locked relay") {
		t.Errorf("docs/relay-runbook.md does not scope sync-allowlist to operators running their OWN locked relay")
	}
	if !strings.Contains(lower, "not required") && !strings.Contains(lower, "is not needed") {
		t.Errorf("docs/relay-runbook.md does not say sync-allowlist is not required to invite/authorize someone")
	}
	if !strings.Contains(doc, "rd grant") {
		t.Errorf("docs/relay-runbook.md does not mention `rd grant`, which alone authorizes on any public relay")
	}
}

// TestRelayRunbook_NoRequiredRoleGrant: the happy-path `rd grant` example
// must not force an explicit role arg — role is optional and defaults to
// contributor per the real --help.
func TestRelayRunbook_NoRequiredRoleGrant(t *testing.T) {
	doc := readDocOrFatalOC(t, "../../docs/relay-runbook.md")
	forbidden := []string{
		"rd grant <pubkeyHex> contributor --label",
	}
	for _, s := range forbidden {
		if strings.Contains(doc, s) {
			t.Errorf("docs/relay-runbook.md still shows a required-role grant example: %q", s)
		}
	}
}

// TestConventionWorkManagementDoc_NotPresentedAsCurrent: this document
// specifies the original campfire-native convention (WG-4 draft). Campfire
// was retired 2026-07; the doc must carry an explicit status marker near the
// top saying so and pointing at the real, current rd nostr-native commands,
// so a reader does not conclude `cf work create` etc. are live commands.
func TestConventionWorkManagementDoc_NotPresentedAsCurrent(t *testing.T) {
	doc := readDocOrFatalOC(t, "../../docs/convention/work-management.md")
	lower := strings.ToLower(doc)

	// Status marker must appear in the document header (first ~40 lines),
	// not buried at the bottom.
	lines := strings.Split(doc, "\n")
	headEnd := len(lines)
	if headEnd > 40 {
		headEnd = 40
	}
	head := strings.ToLower(strings.Join(lines[:headEnd], "\n"))

	if !strings.Contains(head, "retired") && !strings.Contains(head, "superseded") && !strings.Contains(head, "historical") {
		t.Errorf("docs/convention/work-management.md header does not mark the campfire convention as retired/superseded/historical")
	}
	if !strings.Contains(lower, "nostr") {
		t.Errorf("docs/convention/work-management.md never points at the current nostr-native rd commands")
	}
}

// TestNostrSigningDoc_NoStaleOnboardingOrPinBoard: nostr-signing.md must not
// present the old invite/join-only onboarding flow, `rd pin-board` as a
// user-facing command, or a required-role `rd grant` invocation. (Its
// existing `.cf`/campfire mentions are explicitly marked historical and are
// out of scope — this test only guards against regressions.)
func TestNostrSigningDoc_NoStaleOnboardingOrPinBoard(t *testing.T) {
	doc := readDocOrFatalOC(t, "../../docs/nostr-signing.md")
	if strings.Contains(doc, "rd pin-board") {
		t.Errorf("docs/nostr-signing.md tells the reader to run `rd pin-board` — use `rd link`")
	}
	if strings.Contains(doc, "rd grant <pubkeyHex> contributor") || strings.Contains(doc, "rd grant <pubkey> contributor") {
		t.Errorf("docs/nostr-signing.md shows a required-role `rd grant` invocation — role is optional")
	}
}
