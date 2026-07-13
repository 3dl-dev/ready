package main

// help_campfire_test.go — acceptance test for the nostr-native cutover (ready-9ac):
// the operator-facing help surface must not mention "campfire" anywhere. rd is a
// nostr-native tool; the campfire backend is an implementation detail being
// retired, and leaking it into --help confuses users.

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// commandHelpText renders the user-facing help for a single command exactly the
// way `rd <cmd> --help` (and `rd --help`) surfaces it: the Short and Long blurbs
// plus the full usage string (which enumerates subcommands with their Short
// descriptions and every flag with its usage). This is the same text the required
// `rd --help | grep -c campfire` acceptance check reads.
func commandHelpText(c *cobra.Command) string {
	return c.Short + "\n" + c.Long + "\n" + c.UsageString()
}

// TestRootHelpHasNoCampfire is the required acceptance gate: `rd --help` must
// contain ZERO 'campfire' occurrences (case-insensitive, so 'Campfire' is caught
// too). The root usage string also enumerates every top-level subcommand's Short
// and every persistent flag, so this alone covers the top-level help surface.
func TestRootHelpHasNoCampfire(t *testing.T) {
	help := commandHelpText(rootCmd)
	if n := strings.Count(strings.ToLower(help), "campfire"); n != 0 {
		t.Errorf("`rd --help` mentions 'campfire' %d time(s); must be 0.\n----\n%s", n, help)
	}
}

// TestAllSubcommandHelpHasNoCampfire extends the acceptance goal to EVERY
// subcommand's help, per the ready-9ac done condition. A single reported command
// path pinpoints the offending Short/Long/flag to fix.
func TestAllSubcommandHelpHasNoCampfire(t *testing.T) {
	var offenders []string
	var walk func(c *cobra.Command)
	walk = func(c *cobra.Command) {
		if strings.Contains(strings.ToLower(commandHelpText(c)), "campfire") {
			offenders = append(offenders, c.CommandPath())
		}
		for _, sub := range c.Commands() {
			walk(sub)
		}
	}
	walk(rootCmd)
	if len(offenders) > 0 {
		t.Errorf("help text still mentions 'campfire' in: %v", offenders)
	}
}
