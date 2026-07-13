package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/ready/pkg/state"
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

		// nostr-native default write path (ready-6ef): no .cf, secp256k1 signer.
		if _, native := nostrNativeProject(); native {
			return runGateNostr(itemID, gateType, description)
		}

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			// Resolve the item.
			item, err := byIDFromJSONLOrStore(s, itemID)
			if err != nil {
				return err
			}

			// Check not already terminal.
			if state.IsTerminal(item) {
				return fmt.Errorf("item %s is already %s", item.ID, item.Status)
			}

			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}
			decl, err := loadDeclaration("gate")
			if err != nil {
				return err
			}

			// Fire-and-forget gate (no futures, D5).
			argsMap := map[string]any{
				"target":    item.MsgID,
				"gate_type": gateType,
			}
			if description != "" {
				argsMap["description"] = description
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}

			// rd->nostr hybrid publish (ready-2cf): rd gate transitions the item to
			// waiting (waiting_type=gate), mirroring pkg/state.handleWorkGate exactly.
			// Publish a status change carrying the gate description as the reason so
			// (a) the card materializes s=waiting + waiting_type/waiting_on tags
			// (the projection then derives GateMsgID → views.GatesFilter sees a
			// pending gate) and (b) the gate action lands in the audit-history
			// replay. AFTER enforcement; best-effort.
			item.Status = state.StatusWaiting
			item.WaitingType = "gate"
			item.WaitingOn = description
			if nostrErr := publishItemStatusChangeNostr(item, description); nostrErr != nil {
				warnNostrPublishFailure("gate sent; campfire durable", nostrErr)
			}

			if jsonOutput {
				out := map[string]interface{}{
					"id":          item.ID,
					"msg_id":      msg.ID,
					"campfire_id": campfireID,
					"gate_type":   gateType,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Printf("gate sent for %s (%s)\n", item.ID, gateType)
			return nil
		})
	},
}

func init() {
	gateCmd.Flags().String("gate-type", "", "gate type: budget, design, scope, review, human, stall, periodic")
	gateCmd.Flags().String("description", "", "description of what needs human review")
	rootCmd.AddCommand(gateCmd)
}
