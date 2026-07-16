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

// logCmd groups operations on the local authoritative signed-event log: merging
// another machine's committed log (the relay-free degrade floor), re-publishing an
// item's current state, and the put-by-id primitive the two-machine sync demos drive.
var logCmd = &cobra.Command{
	Use:    "log",
	Hidden: true, // low-level log escape hatches (merge/publish/put) — automatic or debug-only
	Short:  "Local authoritative-log operations (merge-log, publish, put)",
	Long: `Operate directly on the local append-only signed-event log
(.ready/nostr-log.jsonl) — the source of truth. Relays are replaceable caches.

merge-log merges another machine's committed log (relay-free convergence);
publish re-emits an item's current state to the log + relays; put creates or
updates an item by explicit id (used by the two-machine sync demos).`,
}

func init() {
	rootCmd.AddCommand(relayCmd)
	rootCmd.AddCommand(logCmd)
}
