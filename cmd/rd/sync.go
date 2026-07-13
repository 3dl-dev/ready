package main

import (
	"github.com/spf13/cobra"
)

// syncCmd is the top-level `rd sync` — negentropy-sync the local nostr log with
// the relays so two machines converge on identical work-item state (ready-9ac
// promoted this from `rd nostr sync`; the former campfire `rd sync status/pull`
// surface is retired with the campfire command surface).
//
// The RunE body lives once on nostrSyncCmd (cmd/rd/nostr.go) and is reused here
// with THIS command as the scope, so the promoted surface and the substrate can
// never drift. nostrSyncCmd reads no flags of its own.
var syncCmd = &cobra.Command{
	Use:   "sync",
	Short: "Negentropy-sync the local nostr log with the relays (two-machine convergence)",
	Long: `Reconcile the local append-only nostr event log against the configured
relays via NIP-77 negentropy and perform the resulting download + upload, so two
machines converge on identical work-item state by transferring only the
difference. The download is web-of-trust gated: a relay cannot inject a
validly-signed event authored by an ungranted key.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return nostrSyncCmd.RunE(cmd, args)
	},
}

func init() {
	rootCmd.AddCommand(syncCmd)
}
