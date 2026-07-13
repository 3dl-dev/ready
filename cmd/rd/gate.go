package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var gateCmd = &cobra.Command{
	Use:   "gate <item-id>",
	Short: "Request human escalation on a work item",
	Long: `Request human escalation. Transitions the item to waiting (waiting_type=gate).

Gate types: budget, design, scope, review, human, stall, periodic

The human must approve or reject the gate before work can proceed.
Use 'rd approve <item-id>' or 'rd reject <item-id>' to resolve.

Note: In a full implementation this would be sent as --future so the agent can
block on 'cf await' until the human resolves it. This requires futures transport
support (TODO: add --future flag when the transport supports cf await).

Example:
  rd gate ready-a1b --gate-type design --description "Confirm approach before implementing"
  rd gate ready-a1b --gate-type budget --description "Approve spend of $500"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		gateType, _ := cmd.Flags().GetString("gate-type")
		description, _ := cmd.Flags().GetString("description")

		if gateType == "" {
			return fmt.Errorf("--gate-type is required: choose from budget, design, scope, review, human, stall, periodic")
		}

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		if _, native := nostrNativeProject(); native {
			return runGateNostr(itemID, gateType, description)
		}
		return errNotNostrProject()
	},
}

func init() {
	gateCmd.Flags().String("gate-type", "", "gate type: budget, design, scope, review, human, stall, periodic")
	gateCmd.Flags().String("description", "", "description of what needs human review")
	rootCmd.AddCommand(gateCmd)
}
