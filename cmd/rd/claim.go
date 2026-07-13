package main

import (
	"github.com/spf13/cobra"
)

var claimCmd = &cobra.Command{
	Use:   "claim <item-id>",
	Short: "Claim a work item",
	Long: `Claim a work item — accept delegation and transition to active.

Sets by=sender and transitions the item to active status.

Example:
  rd claim ready-a1b
  rd claim ready-a1b --reason "Picking this up now"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. This is the
		// only write path — a non-nostr directory is not a ready project.
		if _, native := nostrNativeProject(); native {
			return runClaimNostr(itemID, reason)
		}
		return errNotNostrProject()
	},
}

func init() {
	claimCmd.Flags().String("reason", "", "reason for claiming")
	rootCmd.AddCommand(claimCmd)
}
