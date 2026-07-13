package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/timeparse"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update <item-id>",
	Short: "Update fields on a work item",
	Long: `Update one or more mutable fields on a work item.

Field flags: --title, --context, --priority, --eta, --due
Status flags: --status, --waiting-on, --waiting-type (auto-sets status=waiting when --waiting-on is used)
Note flag:    --note (used as reason for status transitions)

Examples:
  rd update ready-a1b --priority p0 --eta 2026-04-01T12:00:00Z
  rd update ready-a1b --title "New title" --context "Updated context"
  rd update ready-a1b --status waiting --waiting-on "vendor quote" --waiting-type vendor
  rd update ready-a1b --waiting-on "design review" --waiting-type person`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Helpful redirect for --blocks (agents may try bd-style dep wiring via update).
		if blocks, _ := cmd.Flags().GetString("blocks"); blocks != "" {
			return fmt.Errorf("--blocks is not a flag on rd update. Use: rd dep add <this-item> %s", blocks)
		}

		itemID := args[0]
		title, _ := cmd.Flags().GetString("title")
		context, _ := cmd.Flags().GetString("context")
		priority, _ := cmd.Flags().GetString("priority")
		eta, _ := cmd.Flags().GetString("eta")
		due, _ := cmd.Flags().GetString("due")
		level, _ := cmd.Flags().GetString("level")
		statusTo, _ := cmd.Flags().GetString("status")
		waitingOn, _ := cmd.Flags().GetString("waiting-on")
		waitingType, _ := cmd.Flags().GetString("waiting-type")
		note, _ := cmd.Flags().GetString("note")
		claim, _ := cmd.Flags().GetBool("claim")

		// --claim alone implies --status active (bd-compat: bd update --claim sets active).
		if claim && statusTo == "" {
			statusTo = state.StatusActive
		}

		// Auto-set status=waiting if --waiting-on is set without --status.
		if waitingOn != "" && statusTo == "" {
			statusTo = state.StatusWaiting
		}

		// Validate that at least one flag is set.
		hasFieldUpdate := title != "" || context != "" || priority != "" ||
			eta != "" || due != "" || level != ""
		hasStatusUpdate := statusTo != "" || waitingOn != ""

		if !hasFieldUpdate && !hasStatusUpdate && !claim {
			return fmt.Errorf("no fields to update: specify at least one of --title, --context, --priority, --eta, --due, --level, --status, --waiting-on, --claim")
		}

		// Resolve status aliases (bd-compat).
		if statusTo != "" {
			if canonical := resolveStatus(statusTo); canonical != statusTo {
				fmt.Fprintf(os.Stderr, "warning: status %q is a bd alias — using %q instead\n", statusTo, canonical)
				statusTo = canonical
			}
		}

		// Normalize ETA/due to UTC if provided.
		if eta != "" {
			normalized, err := timeparse.Parse(eta, time.Now())
			if err != nil {
				return fmt.Errorf("invalid --eta: %w", err)
			}
			eta = normalized
		}
		if due != "" {
			normalized, err := timeparse.Parse(due, time.Now())
			if err != nil {
				return fmt.Errorf("invalid --due: %w", err)
			}
			due = normalized
		}

		// nostr-native default write path (ready-6ef): no .cf, secp256k1 signer.
		if _, native := nostrNativeProject(); native {
			return runUpdateNostr(itemID, nostrUpdateSpec{
				title: title, context: context, priority: priority,
				eta: eta, due: due, level: level,
				statusTo: statusTo, waitingOn: waitingOn, waitingType: waitingType, note: note,
				hasFieldUpdate: hasFieldUpdate, hasStatusUpdate: hasStatusUpdate, claim: claim,
			})
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

		// Check not already terminal for non-terminal operations.
		if state.IsTerminal(item) && hasFieldUpdate {
			return fmt.Errorf("item %s is already %s", item.ID, item.Status)
		}

		exec, _, err := requireExecutor()
		if err != nil {
			return err
		}

		var lastMsgID string
		var lastCampfireID string

		// Send work:update if field updates are requested.
		if hasFieldUpdate {
			decl, err := loadDeclaration("update")
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"target": item.MsgID,
			}
			if title != "" {
				argsMap["title"] = title
			}
			if context != "" {
				argsMap["context"] = context
			}
			if priority != "" {
				argsMap["priority"] = priority
			}
			if eta != "" {
				argsMap["eta"] = eta
			}
			if due != "" {
				argsMap["due"] = due
			}
			if level != "" {
				argsMap["level"] = level
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}
			lastMsgID = msg.ID
			lastCampfireID = campfireID

			// rd->nostr hybrid publish (ready-b5f): a field-only edit (title/
			// context/priority/eta/due/level) is a card-only edit — no status
			// change, so publish the refreshed card with NO status event. Proves
			// editing the card never touches history.
			if title != "" {
				item.Title = title
			}
			if context != "" {
				item.Context = context
			}
			if priority != "" {
				item.Priority = priority
			}
			if nostrErr := publishItemCardEditNostr(item); nostrErr != nil {
				warnNostrPublishFailure("item updated; campfire durable", nostrErr)
			}
		}

		// Send work:status if a status transition is requested.
		if hasStatusUpdate {
			decl, err := loadDeclaration("status")
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"target": item.MsgID,
				"to":     statusTo,
			}
			if note != "" {
				argsMap["reason"] = note
			}
			if waitingOn != "" {
				argsMap["waiting_on"] = waitingOn
			}
			if waitingType != "" {
				argsMap["waiting_type"] = waitingType
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}
			lastMsgID = msg.ID
			lastCampfireID = campfireID

			// rd->nostr hybrid publish (ready-b5f): this IS a status change —
			// publish a NIP-34 status event so the audit trail replay sees it.
			item.Status = statusTo
			if nostrErr := publishItemStatusChangeNostr(item, note); nostrErr != nil {
				warnNostrPublishFailure("status updated; campfire durable", nostrErr)
			}
		}

		// Send work:claim if --claim is set.
		if claim {
			decl, err := loadDeclaration("claim")
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"target": item.MsgID,
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}
			lastMsgID = msg.ID
			lastCampfireID = campfireID

			// rd->nostr hybrid publish (ready-b5f): --claim is a status change too.
			item.Status = state.StatusActive
			item.By = agentID.PublicKeyHex()
			if nostrErr := publishItemStatusChangeNostr(item, ""); nostrErr != nil {
				warnNostrPublishFailure("item claimed; campfire durable", nostrErr)
			}
		}

		if jsonOutput {
			out := map[string]interface{}{
				"id":          item.ID,
				"msg_id":      lastMsgID,
				"campfire_id": lastCampfireID,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("updated %s\n", item.ID)
		return nil
	},
}

func init() {
	updateCmd.Flags().String("title", "", "new title")
	updateCmd.Flags().String("context", "", "new context/description")
	updateCmd.Flags().String("priority", "", "priority: p0, p1, p2, p3")
	updateCmd.Flags().String("eta", "", "ETA in RFC3339 format")
	updateCmd.Flags().String("due", "", "hard deadline in RFC3339 format")
	updateCmd.Flags().String("level", "", "level: epic, task, subtask")
	updateCmd.Flags().String("status", "", "status: inbox, active, scheduled, waiting, done, cancelled, failed")
	updateCmd.Flags().String("waiting-on", "", "what we are waiting on (auto-sets status=waiting if no --status given)")
	updateCmd.Flags().String("waiting-type", "", "waiting type: person, vendor, client, date, event, external, agent, gate")
	updateCmd.Flags().String("note", "", "note or reason (used as reason for status transitions)")
	updateCmd.Flags().String("blocks", "", "")
	_ = updateCmd.Flags().MarkHidden("blocks")
	updateCmd.Flags().Bool("claim", false, "claim the item: set by=sender and transition to active (bd-compat: bd update --claim)")
	rootCmd.AddCommand(updateCmd)
}
