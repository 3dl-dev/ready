package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var delegateCmd = &cobra.Command{
	Use:   "delegate <item-id>",
	Short: "Delegate a work item to another party",
	Long: `Delegate a work item — assign or reassign the performer.

The --to flag is required and specifies the delegatee identity.
Identity types:
  - Person:           baron@3dl.dev
  - Claude agent:     claude-session-xyz
  - Open agent:       cf://agents/implementer
  - Rudi automaton:   atlas/worker-3

Example:
  rd delegate ready-a1b --to baron@3dl.dev
  rd delegate ready-a1b --to atlas/worker-3 --reason "Routing to automaton"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		to, _ := cmd.Flags().GetString("to")
		reason, _ := cmd.Flags().GetString("reason")

		if to == "" {
			return fmt.Errorf("--to is required")
		}

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		if _, native := nostrNativeProject(); native {
			return runDelegateNostr(itemID, to, reason)
		}
		return errNotNostrProject()
	},
}

func init() {
	delegateCmd.Flags().String("to", "", "identity to delegate to (required)")
	delegateCmd.Flags().String("reason", "", "reason for delegation")
	rootCmd.AddCommand(delegateCmd)
}
