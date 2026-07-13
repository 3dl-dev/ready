package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var closeCmd = &cobra.Command{
	Use:   "close <item-id>",
	Short: "Close a work item",
	Long: `Close a work item with a resolution.

Resolution must be one of: done, cancelled, failed (default: done).

Example:
  rd close ready-a1b --reason "Implemented and merged"
  rd close ready-a1b --resolution cancelled --reason "No longer needed"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		resolution, _ := cmd.Flags().GetString("resolution")
		reason, _ := cmd.Flags().GetString("reason")

		if reason == "" {
			return fmt.Errorf("--reason is required (why is this item being closed?)")
		}

		if resolution == "" {
			resolution = "done"
		}

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		if _, native := nostrNativeProject(); native {
			return runCloseNostr(itemID, resolution, reason, "closed")
		}
		return errNotNostrProject()
	},
}

func init() {
	closeCmd.Flags().String("resolution", "done", "resolution: done, cancelled, failed")
	closeCmd.Flags().String("reason", "", "reason for closing")
	rootCmd.AddCommand(closeCmd)
}
