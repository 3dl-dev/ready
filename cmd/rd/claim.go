package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/campfire-net/ready/pkg/state"
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

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

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
		decl, err := loadDeclaration("claim")
		if err != nil {
			return err
		}

		argsMap := map[string]any{
			"target": item.MsgID,
		}
		if reason != "" {
			argsMap["reason"] = reason
		}

		msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
		if err != nil {
			return err
		}

		// rd->nostr hybrid publish (ready-b5f): claim is a status transition
		// (-> active) with assignee=sender; publish it as a NIP-34 status event so
		// the audit trail replay sees this step. Best-effort/no-op when disabled.
		item.Status = state.StatusActive
		item.By = agentID.PublicKeyHex()
		if nostrErr := publishItemStatusChangeNostr(item, reason); nostrErr != nil {
			fmt.Fprintf(os.Stderr, "warning: nostr publish failed (item claimed; campfire durable): %v\n", nostrErr)
		}

		if jsonOutput {
			out := map[string]interface{}{
				"id":          item.ID,
				"msg_id":      msg.ID,
				"campfire_id": campfireID,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("claimed %s\n", item.ID)
		return nil
	},
}

func init() {
	claimCmd.Flags().String("reason", "", "reason for claiming")
	rootCmd.AddCommand(claimCmd)
}
