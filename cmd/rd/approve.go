package main

import (
	"github.com/spf13/cobra"
)

var approveCmd = &cobra.Command{
	Use:   "approve <item-id>",
	Short: "Approve a pending gate",
	Long: `Approve a pending gate escalation. Transitions the item back to active.

The item must be in waiting status with an unfulfilled gate. Sends a
work:gate-resolve message with resolution=approved, targeting the gate message.

Convention §4.9: approved → item transitions to active.

Example:
  rd approve ready-a1b --reason "Approved, proceed with design approach"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		if _, native := nostrNativeProject(); native {
			return runApproveNostr(itemID, reason)
		}
		return errNotNostrProject()
	},
}

func init() {
	approveCmd.Flags().String("reason", "", "reason for approving")
	rootCmd.AddCommand(approveCmd)
}
