package main

import (
	"github.com/spf13/cobra"
)

var rejectCmd = &cobra.Command{
	Use:   "reject <item-id>",
	Short: "Reject a pending gate",
	Long: `Reject a pending gate escalation. Item remains in waiting status.

The item must be in waiting status with an unfulfilled gate. Sends a
work:gate-resolve message with resolution=rejected.

Convention §4.9: rejected → item remains waiting. The by party should revise
their approach and either resume (work:status → active) or re-gate with a new
question.

Example:
  rd reject ready-a1b --reason "Scope too broad, need to split into smaller pieces"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		if _, native := nostrNativeProject(); native {
			return runRejectNostr(itemID, reason)
		}
		return errNotNostrProject()
	},
}

func init() {
	rejectCmd.Flags().String("reason", "", "reason for rejecting")
	rootCmd.AddCommand(rejectCmd)
}
