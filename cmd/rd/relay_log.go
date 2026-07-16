package main

import (
	"github.com/spf13/cobra"
)

// ready-f58: nostr is the SUBSTRATE, not a user-typed mode — the `rd nostr`
// namespace is gone. The operations that genuinely operate on the nostr transport
// (rather than duplicating a top-level work-management verb) now live under two
// intent-named namespaces:
//
//	rd relay …  — relay-facing operations: sync-allowlist, flush.
//	rd log …    — local authoritative-log operations: merge-log, publish, put.
//
// The subcommand RunE bodies live where they always have (cmd/rd/nostr.go,
// cmd/rd/nostr_grant.go); only their PARENT changed. This file owns the two parent
// commands so the registration is discoverable in one place.

// relayCmd groups relay-facing operations (relay write-allowlist regeneration and
// the offline-buffer flush). These act on the replaceable relay caches — the local
// signed-event log stays authoritative.
var relayCmd = &cobra.Command{
	Use:    "relay",
	Hidden: true, // substrate/ops plumbing — not part of the work-management surface
	Short:  "Relay-facing operations (write-allowlist sync, offline-buffer flush)",
	Long: `Operate on the replaceable relay caches that back rd's nostr transport.

The local append-only signed-event log (.ready/nostr-log.jsonl) is the source of
truth; relays are replaceable caches. These operations regenerate/push the relay
write-allowlist and drain the offline publish buffer to the relays.`,
}

// logCmd is dual-purpose. As `rd log <item-id>` it renders an item's unified
// timeline — status transitions (item.History) and progress notes (timestamped
// blocks in item.Context) merged into one chronological view. Its subcommands
// (merge-log, publish, put) are low-level authoritative-log escape hatches.
//
// The dispatch is unambiguous: an item ID never collides with a subcommand name,
// so `rd log ready-abc` runs this RunE while `rd log merge-log …` dispatches down.
var logCmd = &cobra.Command{
	Use:   "log <item-id>",
	Short: "Show an item's unified timeline (status history + progress notes)",
	Long: `Show a work item's unified timeline.

Progress notes (appended by 'rd progress') and status transitions (claim, status
changes, close) are stored separately — progress in the context field, transitions
in the audit history. 'rd log <item-id>' merges both into ONE chronological view,
answering "what happened to this item, in order".

Example:
  rd log ready-a1b
  rd log ready-a1b --json

Advanced subcommands operate directly on the local append-only signed-event log
(.ready/nostr-log.jsonl): merge-log merges another machine's committed log
(relay-free convergence); publish re-emits an item's current state; put creates or
updates an item by explicit id.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		return renderTimeline(args[0])
	},
}

func init() {
	rootCmd.AddCommand(relayCmd)
	rootCmd.AddCommand(logCmd)
}
