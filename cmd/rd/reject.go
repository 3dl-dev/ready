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

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			// Resolve the item.
			item, err := byIDFromJSONLOrStore(s, itemID)
			if err != nil {
				return err
			}

			// Check item has an unfulfilled gate.
			if item.GateMsgID == "" {
				return fmt.Errorf("item %s has no pending gate to reject", item.ID)
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
				"resolution": "rejected",
			}
			if reason != "" {
				argsMap["reason"] = reason
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}

			if jsonOutput {
				out := map[string]interface{}{
					"id":          item.ID,
					"msg_id":      msg.ID,
					"campfire_id": campfireID,
					"resolution":  "rejected",
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Printf("rejected gate for %s\n", item.ID)
			return nil
		})
	},
}

func init() {
	rejectCmd.Flags().String("reason", "", "reason for rejecting")
	rootCmd.AddCommand(rejectCmd)
}
