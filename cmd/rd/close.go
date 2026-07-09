package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/campfire-net/ready/pkg/state"
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
		decl, err := loadDeclaration("close")
		if err != nil {
			return err
		}

		argsMap := map[string]any{
			"target":     item.MsgID,
			"resolution": resolution,
		}
		if reason != "" {
			argsMap["reason"] = reason
		}

		msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
		if err != nil {
			return err
		}

		// rd->nostr hybrid publish (ready-b5f): close is a status transition to a
		// terminal state, carrying the enforced close-with-reason. Publish it as a
		// NIP-34 status event so the audit trail replay preserves the reason exactly.
		blockedByThis := item.Blocks
		item.Status = closeResolutionToStatus(resolution)
		if nostrErr := publishItemStatusChangeNostr(item, reason); nostrErr != nil {
			warnNostrPublishFailure("item closed; campfire durable", nostrErr)
		}
		// Implicit unblock parity (ready-2cf): closing this item removes its dep
		// edge from every item it was blocking — re-publish those cards.
		publishImplicitUnblockNostr(s, blockedByThis)

		if jsonOutput {
			out := map[string]interface{}{
				"id":          item.ID,
				"msg_id":      msg.ID,
				"campfire_id": campfireID,
				"resolution":  resolution,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("closed %s (%s)\n", item.ID, resolution)
		return nil
	},
}

func init() {
	closeCmd.Flags().String("resolution", "done", "resolution: done, cancelled, failed")
	closeCmd.Flags().String("reason", "", "reason for closing")
	rootCmd.AddCommand(closeCmd)
}
