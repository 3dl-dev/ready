package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/campfire-net/ready/pkg/state"
)

// completePayload is the JSON payload for a work:close message sent by rd complete.
// It extends closePayload with agent-facing metadata fields.
type completePayload struct {
	Target     string `json:"target"`
	Resolution string `json:"resolution"`
	Reason     string `json:"reason,omitempty"`
	Branch     string `json:"branch,omitempty"`
	Session    string `json:"session,omitempty"`
}

// completeCmd closes an item with resolution=done, adding agent metadata (branch, session).
var completeCmd = &cobra.Command{
	Use:   "complete <item-id>",
	Short: "Signal a work item is finished (agent-facing)",
	Long: `Close a work item with resolution=done, recording agent metadata.

Agent-facing completion command. Equivalent to rd done but supports
--branch and --session flags for traceability in agent workflows.

Example:
  rd complete rudi-utt --reason "done" --branch work/rudi-utt
  rd complete rudi-utt --reason "implemented and merged" --branch work/rudi-utt --session abc123`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		reason, _ := cmd.Flags().GetString("reason")
		branch, _ := cmd.Flags().GetString("branch")
		session, _ := cmd.Flags().GetString("session")

		if reason == "" {
			return fmt.Errorf("--reason is required (why is this item being closed?)")
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

		// Build payload.
		p := completePayload{
			Target:     item.MsgID,
			Resolution: "done",
			Reason:     reason,
			Branch:     branch,
			Session:    session,
		}
		payloadBytes, err := json.Marshal(p)
		if err != nil {
			return fmt.Errorf("encoding payload: %w", err)
		}

		// Tags: same as done/close with resolution=done.
		tags := []string{"work:close", "work:resolution:done"}

		// Antecedents: the work:create message.
		antecedents := []string{item.MsgID}

		msg, campfireID, err := sendToProjectCampfire(agentID, s, string(payloadBytes), tags, antecedents)
		if err != nil {
			return err
		}

		// rd->nostr hybrid publish (ready-b5f): same status-change hook as close,
		// preserving the enforced close-with-reason in the audit trail replay.
		blockedByThis := item.Blocks
		item.Status = state.StatusDone
		if nostrErr := publishItemStatusChangeNostr(item, reason); nostrErr != nil {
			warnNostrPublishFailure("item completed; campfire durable", nostrErr)
		}
		// Implicit unblock parity (ready-2cf): re-publish cards this item unblocks.
		publishImplicitUnblockNostr(s, blockedByThis)

		if jsonOutput {
			out := map[string]interface{}{
				"id":          item.ID,
				"msg_id":      msg.ID,
				"campfire_id": campfireID,
				"resolution":  "done",
			}
			if branch != "" {
				out["branch"] = branch
			}
			if session != "" {
				out["session"] = session
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("closed %s (done)\n", item.ID)
		return nil
	},
}

func init() {
	completeCmd.Flags().String("reason", "", "reason for completing")
	completeCmd.Flags().String("branch", "", "git branch name where work was done")
	completeCmd.Flags().String("session", "", "session ID for traceability")
	rootCmd.AddCommand(completeCmd)
}
