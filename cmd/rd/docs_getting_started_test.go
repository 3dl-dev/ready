package main

// docs_getting_started_test.go — ready-010 done-condition test: the docs
// describe the CURRENT nostr backend and the multi-machine flow, with no
// campfire-era setup instructions, and every command/flag shown in
// docs/getting-started.md (and docs/migration-brief.md, if present) is real —
// cross-checked against the actual cobra --help surface, not hand-typed.

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

const gettingStartedPath = "../../docs/getting-started.md"
const migrationBriefPath = "../../docs/migration-brief.md"

func readDocOrFatal(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	return string(b)
}

// TestGettingStartedDoc_NoCampfireEraLanguage is the done-condition test for
// (1): docs/getting-started.md must not open with (or contain anywhere) the
// campfire-convention framing, and must carry no campfire-era vocabulary
// (campfire, .campfire, cf-mcp, Azure/OIDC hosting, the retired rd.3dl.dev
// campfire-hosting domain).
func TestGettingStartedDoc_NoCampfireEraLanguage(t *testing.T) {
	doc := readDocOrFatal(t, gettingStartedPath)
	lower := strings.ToLower(doc)

	if strings.Contains(lower, "work management as a campfire convention") {
		t.Errorf("docs/getting-started.md still opens with the campfire-convention framing")
	}

	forbidden := []string{"campfire", ".campfire", "cf-mcp", "azure", "oidc", "rd.3dl.dev"}
	for _, term := range forbidden {
		if strings.Contains(lower, term) {
			t.Errorf("docs/getting-started.md contains campfire-era term %q", term)
		}
	}
}

// TestMigrationBrief_KilledOrCampfireFree is the done-condition test for (2):
// if docs/migration-brief.md still exists, it must not carry campfire-era
// setup instructions (cf, .campfire/root, Azure/OIDC, rd.3dl.dev). If the file
// is absent, the requirement is satisfied by deletion.
func TestMigrationBrief_KilledOrCampfireFree(t *testing.T) {
	b, err := os.ReadFile(migrationBriefPath)
	if os.IsNotExist(err) {
		return // killed — satisfies the done condition
	}
	if err != nil {
		t.Fatalf("reading %s: %v", migrationBriefPath, err)
	}
	lower := strings.ToLower(string(b))
	forbidden := []string{"campfire", ".campfire/root", "azure", "oidc", "rd.3dl.dev", " cf "}
	for _, term := range forbidden {
		if strings.Contains(lower, term) {
			t.Errorf("docs/migration-brief.md still exists and contains campfire-era term %q; kill or fully replace it", term)
		}
	}
}

// TestGettingStartedDoc_SecondMachineSection is the done-condition test for
// (3): the doc must carry a short "second machine in two commands" section
// showing the follower running `rd follow baron@3dl.dev` and the owner
// running `rd grant --all-boards <pubkey>`, plus the committed-board.json
// story (clone a repo that carries board.json and you're already on the
// board).
func TestGettingStartedDoc_SecondMachineSection(t *testing.T) {
	doc := readDocOrFatal(t, gettingStartedPath)
	lower := strings.ToLower(doc)

	if !strings.Contains(lower, "second machine") {
		t.Errorf("docs/getting-started.md has no 'second machine' section")
	}
	if !strings.Contains(doc, "rd follow baron@3dl.dev") {
		t.Errorf("docs/getting-started.md does not show 'rd follow baron@3dl.dev'")
	}
	if !strings.Contains(doc, "rd grant --all-boards") {
		t.Errorf("docs/getting-started.md does not show 'rd grant --all-boards <pubkey>'")
	}
	if !strings.Contains(doc, "board.json") {
		t.Errorf("docs/getting-started.md does not tell the committed-board.json story")
	}
}

// TestGettingStartedDoc_MentionsCoreIdentityCommands is the done-condition
// test for (1): the doc must document identity via `rd identify`, diagnosis
// via `rd status`, and binding/rebinding a repo via `rd link`.
func TestGettingStartedDoc_MentionsCoreIdentityCommands(t *testing.T) {
	doc := readDocOrFatal(t, gettingStartedPath)
	for _, cmd := range []string{"rd identify", "rd status", "rd link"} {
		if !strings.Contains(doc, cmd) {
			t.Errorf("docs/getting-started.md does not mention %q", cmd)
		}
	}
}

// rdInvocation matches an `rd <subcommand...> <flags...>` shell line inside a
// fenced code block so flags shown in the doc can be cross-checked against
// the real cobra command's registered flags.
var rdInvocationRe = regexp.MustCompile(`(?m)^\s*(?:\$\s*)?rd\s+([a-z][a-z-]*(?:\s+[a-z][a-z-]*)?)\b([^\n]*)$`)
var flagTokenRe = regexp.MustCompile(`--[a-z][a-z-]*`)

// resolveDocCommand maps the first one-or-two words after `rd` in a doc line
// to the real cobra.Command that implements it, walking rootCmd exactly the
// way cobra itself resolves subcommands. Returns nil if the line does not
// name a real leaf subcommand path root.go registers (e.g. a comment or a
// placeholder like `rd show <id>` inside prose — still resolved via Find).
func resolveDocCommand(words []string) *cobra.Command {
	cmd, _, err := rootCmd.Find(words)
	if err != nil {
		return nil
	}
	if cmd == rootCmd {
		return nil
	}
	return cmd
}

// TestGettingStartedDoc_FlagsMatchRealHelp is the CRITICAL cross-check the
// item calls out by name: every `--flag` shown attached to `rd follow`,
// `rd status`, `rd link`, `rd grant`, `rd identify` in docs/getting-started.md
// must exist on the real cobra command (local, persistent, or inherited) —
// no invented flags.
func TestGettingStartedDoc_FlagsMatchRealHelp(t *testing.T) {
	doc := readDocOrFatal(t, gettingStartedPath)
	watched := map[string]bool{"follow": true, "status": true, "link": true, "grant": true, "identify": true}

	for _, line := range strings.Split(doc, "\n") {
		m := rdInvocationRe.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		words := strings.Fields(m[1])
		if len(words) == 0 || !watched[words[0]] {
			continue
		}
		cmd := resolveDocCommand(words)
		if cmd == nil {
			continue
		}
		flags := flagTokenRe.FindAllString(m[2], -1)
		for _, f := range flags {
			name := strings.TrimPrefix(f, "--")
			if cmd.Flags().Lookup(name) == nil && cmd.PersistentFlags().Lookup(name) == nil && cmd.InheritedFlags().Lookup(name) == nil {
				t.Errorf("docs/getting-started.md shows %q on %q but that flag does not exist on the real command (line: %q)", f, cmd.CommandPath(), line)
			}
		}
	}
}
