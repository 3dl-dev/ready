package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/campfire-net/ready/pkg/state"
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

		// nostr-native default write path (ready-6ef): no .cf, secp256k1 signer.
		// Closes the delegate publisher gap (previously published NO nostr event).
		if _, native := nostrNativeProject(); native {
			return runDelegateNostr(itemID, to, reason)
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
		decl, err := loadDeclaration("delegate")
		if err != nil {
			return err
		}

		argsMap := map[string]any{
			"target": item.MsgID,
			"to":     to,
		}
		if item.By != "" {
			argsMap["from"] = item.By
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
				"to":          to,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("delegated %s to %s\n", item.ID, to)
		return nil
	},
}

func init() {
	delegateCmd.Flags().String("to", "", "identity to delegate to (required)")
	delegateCmd.Flags().String("reason", "", "reason for delegation")
	rootCmd.AddCommand(delegateCmd)
}
