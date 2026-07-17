// Package site holds ground-source content checks for the marketing page
// (site/index.html, site/terminal.js). These are plain string/structure
// assertions against the shipped files — no HTML/JS framework, matching how
// the page itself is hand-authored static content.
package site

import (
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
)

// TestIndexHTML_CommandTable_LeadsWithFollowGrant proves the "commands"
// table (ready-b77 fix 2) documents the CURRENT one-command onboarding —
// `rd follow <owner>` to join from another machine keeping your own key,
// and `rd grant <pubkey>` with role optional (defaults to contributor,
// no required <role> argument) — plus `rd status` and `rd link`.
func TestIndexHTML_CommandTable_LeadsWithFollowGrant(t *testing.T) {
	html := readSiteFile(t, "index.html")

	if !strings.Contains(html, "<code>rd follow") {
		t.Fatalf("index.html: command table is missing an `rd follow <owner>` row")
	}
	if !strings.Contains(html, "<code>rd status</code>") {
		t.Fatalf("index.html: command table is missing an `rd status` row")
	}
	if !strings.Contains(html, "<code>rd link") {
		t.Fatalf("index.html: command table is missing an `rd link` row")
	}
	if strings.Contains(html, "rd grant &lt;pubkey&gt; &lt;role&gt;") {
		t.Fatalf("index.html: command table still documents `rd grant <pubkey> <role>` as if role were required")
	}
	if !strings.Contains(html, "rd grant &lt;pubkey&gt;</code>") {
		t.Fatalf("index.html: command table is missing the current `rd grant <pubkey>` (no required role) form")
	}
}

// TestIndexHTML_TeamTier_LeadsWithFollowNotToken proves the "Team & agents"
// tier card (ready-b77 fix 1, ~lines 305-352) leads with `rd follow` +
// `rd grant` as the PRIMARY onboarding, with the invite-token flow (if kept
// at all) appearing only after it, never before.
func TestIndexHTML_TeamTier_LeadsWithFollowNotToken(t *testing.T) {
	html := readSiteFile(t, "index.html")

	cardStart := strings.Index(html, `<span class="tier-label">Team &amp; agents</span>`)
	if cardStart == -1 {
		t.Fatalf("index.html: could not locate the 'Team & agents' tier card")
	}
	cardEnd := strings.Index(html[cardStart:], `<p class="tier-note">`)
	if cardEnd == -1 {
		t.Fatalf("index.html: could not locate the end of the 'Team & agents' tier-note")
	}
	card := html[cardStart : cardStart+cardEnd]

	followIdx := strings.Index(card, "rd follow")
	if followIdx == -1 {
		t.Fatalf("Team & agents tier code block does not mention `rd follow`")
	}
	if tokenIdx := strings.Index(card, "TOKEN=$(rd invite)"); tokenIdx != -1 && tokenIdx < followIdx {
		t.Fatalf("Team & agents tier still leads with `TOKEN=$(rd invite); rd join $TOKEN` ahead of `rd follow`")
	}
	if !strings.Contains(card, "rd grant --all-boards") {
		t.Fatalf("Team & agents tier does not show the owner's one-command `rd grant --all-boards <pubkey>` reply")
	}
}

// TestIndexHTML_SwarmSection_UsesFollow proves the "point a swarm at one
// queue" agents-section demo (ready-b77 fix 1, ~line 412) leads with
// `rd follow`, not the old `rd join $(rd invite)` one-liner.
func TestIndexHTML_SwarmSection_UsesFollow(t *testing.T) {
	html := readSiteFile(t, "index.html")

	sectionStart := strings.Index(html, "Point a swarm at one queue")
	if sectionStart == -1 {
		t.Fatalf("index.html: could not locate the 'Point a swarm at one queue' section")
	}
	sectionEnd := strings.Index(html[sectionStart:], "</div>\n      </div>")
	if sectionEnd == -1 {
		sectionEnd = 1500
	}
	section := html[sectionStart : sectionStart+sectionEnd]

	if !strings.Contains(section, "rd follow") {
		t.Fatalf("swarm section does not lead with `rd follow`")
	}
	if strings.Contains(section, "rd join $(rd invite)") {
		t.Fatalf("swarm section still uses the stale `rd join $(rd invite)` one-liner")
	}
}

// TestIndexHTML_Architecture_NoRelayAllowlistInFlow proves the federated
// architecture diagram (ready-b77 fix 3, ~line 566) no longer implies the
// relay write-allowlist file is part of the default flow: authz is
// app-layer signed grants checked at projection, and relays stay untrusted.
func TestIndexHTML_Architecture_NoRelayAllowlistInFlow(t *testing.T) {
	html := readSiteFile(t, "index.html")

	if strings.Contains(html, "write-allowlist: owner-rooted grants") {
		t.Fatalf("index.html: architecture diagram still frames the relay write-allowlist file as part of the default flow")
	}
	if !strings.Contains(html, "checked at projection") {
		t.Fatalf("index.html: architecture diagram is missing the 'checked at projection' authz framing")
	}
	if !strings.Contains(html, "untrusted") {
		t.Fatalf("index.html: architecture diagram no longer says relays are untrusted")
	}
}

// TestNoCampfireReferences proves neither the marketing HTML nor its
// terminal-demo script mentions campfire (ready-b77 fix 4) — campfire was
// retired and rd is nostr-native end to end.
func TestNoCampfireReferences(t *testing.T) {
	for _, name := range []string{"index.html", "terminal.js"} {
		content := strings.ToLower(readSiteFile(t, name))
		if strings.Contains(content, "campfire") {
			t.Fatalf("%s: still references campfire", name)
		}
	}
}

// TestIndexHTML_TagBalance is a coarse well-formedness sanity check (the
// item explicitly asks for open/close counts, not a full parser) over the
// structural tags touched by this edit.
func TestIndexHTML_TagBalance(t *testing.T) {
	html := readSiteFile(t, "index.html")

	checks := []struct {
		open, close string
	}{
		{"<section", "</section>"},
		{"<table>", "</table>"},
		{"<tr>", "</tr>"},
		{"<tbody>", "</tbody>"},
		{"<ul", "</ul>"},
	}
	for _, c := range checks {
		openRe := regexp.MustCompile(regexp.QuoteMeta(c.open))
		gotOpen := len(openRe.FindAllStringIndex(html, -1))
		gotClose := strings.Count(html, c.close)
		if gotOpen != gotClose {
			t.Fatalf("tag balance for %q/%q: %d open vs %d close", c.open, c.close, gotOpen, gotClose)
		}
	}
	// <td>...</td> pairs specifically inside the command table.
	tdOpen := strings.Count(html, "<td>")
	tdClose := strings.Count(html, "</td>")
	if tdOpen != tdClose {
		t.Fatalf("tag balance for <td>/</td>: %d open vs %d close", tdOpen, tdClose)
	}
}

// TestTerminalJS_Syntax proves terminal.js still parses as valid JavaScript
// after the content edits (ready-b77 fix 1: the team demo must show the
// current follow+grant flow).
func TestTerminalJS_Syntax(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node not available on this machine; skipping syntax check")
	}
	out, err := exec.Command("node", "--check", "terminal.js").CombinedOutput()
	if err != nil {
		t.Fatalf("terminal.js failed to parse: %v\n%s", err, out)
	}
}

// TestTerminalJS_TeamDemo_ShowsFollowGrant proves the "team" terminal demo
// (ready-b77 fix 1) demonstrates the current onboarding: `rd follow`
// followed by an owner `rd grant --all-boards <pubkey>` with no role
// argument (defaults to contributor).
func TestTerminalJS_TeamDemo_ShowsFollowGrant(t *testing.T) {
	js := readSiteFile(t, "terminal.js")

	if !strings.Contains(js, "rd follow") {
		t.Fatalf("terminal.js: no demo shows `rd follow`")
	}
	if !strings.Contains(js, "rd grant --all-boards") {
		t.Fatalf("terminal.js: no demo shows the owner's `rd grant --all-boards <pubkey>` reply")
	}
}

func readSiteFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}
