package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/state"
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

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			// Resolve the item.
			item, err := byIDFromJSONLOrStore(s, itemID)
			if err != nil {
				return err
			}

			// Check item has an unfulfilled gate.
			if item.GateMsgID == "" {
				return fmt.Errorf("item %s has no pending gate to approve", item.ID)
			}
			if item.Status != state.StatusWaiting {
				return fmt.Errorf("item %s is not waiting (status=%s)", item.ID, item.Status)
			}

			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}
			decl, err := loadDeclaration("gate-resolve")
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"target":     item.GateMsgID,
				"resolution": "approved",
			}
			if reason != "" {
				argsMap["reason"] = reason
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}

			// rd->nostr hybrid publish (ready-2cf): approving a gate transitions the
			// item back to active and clears the waiting/gate state, mirroring
			// pkg/state.handleWorkGateResolve. Publish a status change (active) with
			// the approval reason so the item leaves the gates view and the approval
			// lands in the audit-history replay. Clearing Gate/Waiting* in-memory
			// makes the refreshed card drop those tags. AFTER enforcement;
			// best-effort.
			item.Status = state.StatusActive
			item.Gate = ""
			item.WaitingType = ""
			item.WaitingOn = ""
			item.WaitingSince = ""
			item.GateMsgID = ""
			if nostrErr := publishItemStatusChangeNostr(item, reason); nostrErr != nil {
				warnNostrPublishFailure("gate approved; campfire durable", nostrErr)
			}

			if jsonOutput {
				out := map[string]interface{}{
					"id":          item.ID,
					"msg_id":      msg.ID,
					"campfire_id": campfireID,
					"resolution":  "approved",
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Printf("approved gate for %s\n", item.ID)
			return nil
		})
	},
}

func init() {
	approveCmd.Flags().String("reason", "", "reason for approving")
	rootCmd.AddCommand(approveCmd)
}
