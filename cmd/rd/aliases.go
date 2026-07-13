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

// doneCmd closes an item with resolution=done.
var doneCmd = &cobra.Command{
	Use:   "done <item-id>",
	Short: "Close a work item as done",
	Long: `Close a work item with resolution=done.

Alias for: rd close <item-id> --resolution done

Example:
  rd done ready-a1b --reason "Implemented and merged"`,
	Args: cobra.ExactArgs(1),
	RunE: runCloseAlias("done"),
}

// failCmd closes an item with resolution=failed.
var failCmd = &cobra.Command{
	Use:   "fail <item-id>",
	Short: "Close a work item as failed",
	Long: `Close a work item with resolution=failed.

Alias for: rd close <item-id> --resolution failed

Example:
  rd fail ready-a1b --reason "Approach didn't work"`,
	Args: cobra.ExactArgs(1),
	RunE: runCloseAlias("failed"),
}

// cascadeCloseDescendants walks the subtree rooted at rootID depth-first and calls
// closeOne(child, reason) for each open descendant (leaves before parents).
// It is extracted from cancelCmd.RunE so it can be unit-tested with a stub closeOne.
func cascadeCloseDescendants(allItems []*state.Item, rootID string, reason string, closeOne func(child *state.Item, reason string) error) ([]string, error) {
	childrenOf := make(map[string][]*state.Item)
	for _, it := range allItems {
		if it.ParentID != "" {
			childrenOf[it.ParentID] = append(childrenOf[it.ParentID], it)
		}
	}
	var descendants []*state.Item
	var walk func(parentID string)
	walk = func(parentID string) {
		for _, child := range childrenOf[parentID] {
			walk(child.ID)
			if !state.IsTerminal(child) {
				descendants = append(descendants, child)
			}
		}
	}
	walk(rootID)

	var closedIDs []string
	for _, child := range descendants {
		if err := closeOne(child, reason); err != nil {
			return closedIDs, fmt.Errorf("closing child %s: %w", child.ID, err)
		}
		closedIDs = append(closedIDs, child.ID)
	}
	return closedIDs, nil
}

// cancelCmd closes an item with resolution=cancelled, optionally cascading to children.
var cancelCmd = &cobra.Command{
	Use:   "cancel <item-id>",
	Short: "Close a work item as cancelled",
	Long: `Close a work item with resolution=cancelled.

Alias for: rd close <item-id> --resolution cancelled

Use --cascade to also close all open children (items with parent_id = this item).

Example:
  rd cancel ready-a1b --reason "No longer needed"
  rd cancel ready-a1b --reason "Scope cut" --cascade`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		reason, _ := cmd.Flags().GetString("reason")
		cascade, _ := cmd.Flags().GetBool("cascade")

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

		if state.IsTerminal(item) {
			return fmt.Errorf("item %s is already %s", item.ID, item.Status)
		}

		var closedIDs []string

		// Cascade: close open descendants (recursive subtree).
		if cascade {
			allItems, err := allItemsFromJSONLOrStore(s)
			if err != nil {
				return fmt.Errorf("loading items for cascade: %w", err)
			}
			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}
			closeDecl, err := loadDeclaration("close")
			if err != nil {
				return err
			}
			closedIDs, err = cascadeCloseDescendants(allItems, item.ID, reason, func(child *state.Item, reason string) error {
				childArgs := map[string]any{
					"target":     child.MsgID,
					"resolution": "cancelled",
					"reason":     reason,
				}
				if _, _, err := executeConventionOp(agentID, s, exec, closeDecl, childArgs); err != nil {
					return err
				}
				// rd->nostr hybrid publish (ready-2cf): publish a status change for
				// EACH cascaded descendant, not just the parent — so cascade-closed
				// children get the same audit-history + terminal-status parity on
				// nostr as a directly-closed item. AFTER the child's close
				// enforcement succeeded; best-effort.
				childBlocks := child.Blocks
				child.Status = state.StatusCancelled
				if nostrErr := publishItemStatusChangeNostr(child, reason); nostrErr != nil {
					warnNostrPublishFailure(fmt.Sprintf("child %s cascaded; campfire durable", child.ID), nostrErr)
				}
				// Implicit unblock parity (ready-2cf): a cascaded child may itself
				// have been blocking other items — re-publish those cards too.
				publishImplicitUnblockNostr(s, childBlocks)
				return nil
			})
			if err != nil {
				return err
			}
		}

		// Close the parent item.
		exec, _, err := requireExecutor()
		if err != nil {
			return err
		}
		closeDecl, err := loadDeclaration("close")
		if err != nil {
			return err
		}
		parentArgs := map[string]any{
			"target":     item.MsgID,
			"resolution": "cancelled",
			"reason":     reason,
		}
		msg, campfireID, err := executeConventionOp(agentID, s, exec, closeDecl, parentArgs)
		if err != nil {
			return err
		}

		// rd->nostr hybrid publish (ready-b5f): cancel is a close-with-reason
		// status change (cascaded children each publish their own, above).
		blockedByParent := item.Blocks
		item.Status = state.StatusCancelled
		if nostrErr := publishItemStatusChangeNostr(item, reason); nostrErr != nil {
			warnNostrPublishFailure("item cancelled; campfire durable", nostrErr)
		}
		// Implicit unblock parity (ready-2cf): re-publish cards the parent unblocks.
		publishImplicitUnblockNostr(s, blockedByParent)

		if jsonOutput {
			out := map[string]interface{}{
				"id":          item.ID,
				"msg_id":      msg.ID,
				"campfire_id": campfireID,
				"resolution":  "cancelled",
				"cascaded":    closedIDs,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		if len(closedIDs) > 0 {
			for _, childID := range closedIDs {
				fmt.Printf("closed %s (cancelled, cascaded)\n", childID)
			}
		}
		fmt.Printf("closed %s (cancelled)\n", item.ID)
		return nil
	},
}

// deferCmd sends a work:update with a new ETA (defers an item).
var deferCmd = &cobra.Command{
	Use:   "defer <item-id>",
	Short: "Defer a work item to a later ETA",
	Long: `Defer a work item by updating its ETA.

Sends a work:update message with the new ETA. Supports relative time formats:
  2h        → now + 2 hours
  3d        → now + 3 days
  tomorrow  → next day 09:00 UTC
  next week → next Monday 09:00 UTC
  RFC3339   → absolute time passthrough
  YYYY-MM-DD → that date 09:00 UTC

Example:
  rd defer ready-a1b --eta 2h
  rd defer ready-a1b --eta "next week"
  rd defer ready-a1b --eta 2026-04-01`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		etaExpr, _ := cmd.Flags().GetString("eta")

		if etaExpr == "" {
			return fmt.Errorf("--eta is required")
		}

		// Parse the ETA expression.
		etaRFC3339, err := timeparse.Parse(etaExpr, time.Now())
		if err != nil {
			return fmt.Errorf("invalid --eta: %w", err)
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		item, err := byIDFromJSONLOrStore(s, itemID)
		if err != nil {
			return err
		}

		if state.IsTerminal(item) {
			return fmt.Errorf("item %s is already %s", item.ID, item.Status)
		}

		exec, _, err := requireExecutor()
		if err != nil {
			return err
		}
		decl, err := loadDeclaration("update")
		if err != nil {
			return err
		}

		argsMap := map[string]any{
			"target": item.MsgID,
			"eta":    etaRFC3339,
		}
		msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
		if err != nil {
			return err
		}

		// rd->nostr hybrid publish (ready-2cf): defer updates the item's ETA with no
		// status change — publish a refreshed card carrying the new "eta" tag so a
		// nostr reader reconstructs the same schedule. Card-only edit (history
		// untouched). AFTER enforcement; best-effort.
		item.ETA = etaRFC3339
		if nostrErr := publishItemCardEditNostr(item); nostrErr != nil {
			warnNostrPublishFailure("deferred; campfire durable", nostrErr)
		}

		if jsonOutput {
			out := map[string]interface{}{
				"id":          item.ID,
				"msg_id":      msg.ID,
				"campfire_id": campfireID,
				"eta":         etaRFC3339,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("deferred %s → %s\n", item.ID, etaRFC3339)
		return nil
	},
}

// progressCmd appends notes to an item's context field.
var progressCmd = &cobra.Command{
	Use:   "progress <item-id>",
	Short: "Append a progress note to a work item",
	Long: `Append notes to a work item's context field.

Sends a work:update message that appends --notes to the existing context.
This provides an audit trail of progress without overwriting prior context.

Example:
  rd progress ready-a1b --notes "Completed auth module, starting on UI"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		notes, _ := cmd.Flags().GetString("notes")

		if notes == "" {
			return fmt.Errorf("--notes is required")
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		item, err := byIDFromJSONLOrStore(s, itemID)
		if err != nil {
			return err
		}

		if state.IsTerminal(item) {
			return fmt.Errorf("item %s is already %s", item.ID, item.Status)
		}

		// Append notes to existing context with a timestamp separator.
		now := time.Now().UTC().Format("2006-01-02T15:04Z")
		newContext := item.Context
		if newContext != "" {
			newContext = newContext + "\n\n[" + now + "] " + notes
		} else {
			newContext = "[" + now + "] " + notes
		}

		exec, _, err := requireExecutor()
		if err != nil {
			return err
		}
		decl, err := loadDeclaration("update")
		if err != nil {
			return err
		}

		argsMap := map[string]any{
			"target":  item.MsgID,
			"context": newContext,
		}
		msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
		if err != nil {
			return err
		}

		// rd->nostr hybrid publish (ready-b5f): progress is a card-only edit (no
		// status change) — publish a refreshed card with NO status event, proving
		// the invariant that editing the addressable card does not touch history.
		item.Context = newContext
		if nostrErr := publishItemCardEditNostr(item); nostrErr != nil {
			warnNostrPublishFailure("progress noted; campfire durable", nostrErr)
		}

		if jsonOutput {
			out := map[string]interface{}{
				"id":          item.ID,
				"msg_id":      msg.ID,
				"campfire_id": campfireID,
				"context":     newContext,
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		fmt.Printf("progress noted on %s\n", item.ID)
		return nil
	},
}

// runCloseAlias returns a RunE function that closes an item with the given resolution.
func runCloseAlias(resolution string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		if reason == "" {
			return fmt.Errorf("--reason is required (why is this item being closed?)")
		}

		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()

		item, err := byIDFromJSONLOrStore(s, itemID)
		if err != nil {
			return err
		}

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
			"reason":     reason,
		}
		msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
		if err != nil {
			return err
		}

		// rd->nostr hybrid publish (ready-b5f): done/fail are close-with-reason
		// aliases; publish the same status-change event as close so the audit
		// trail replay preserves the reason exactly.
		blockedByThis := item.Blocks
		item.Status = closeResolutionToStatus(resolution)
		if nostrErr := publishItemStatusChangeNostr(item, reason); nostrErr != nil {
			warnNostrPublishFailure("item closed; campfire durable", nostrErr)
		}
		// Implicit unblock parity (ready-2cf): re-publish cards this item unblocks.
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
	}
}

func init() {
	doneCmd.Flags().String("reason", "", "reason for closing")
	rootCmd.AddCommand(doneCmd)

	failCmd.Flags().String("reason", "", "reason for closing")
	rootCmd.AddCommand(failCmd)

	cancelCmd.Flags().String("reason", "", "reason for closing")
	cancelCmd.Flags().Bool("cascade", false, "also close all open children")
	rootCmd.AddCommand(cancelCmd)

	deferCmd.Flags().String("eta", "", "new ETA: 2h, 3d, tomorrow, next week, RFC3339, or YYYY-MM-DD")
	rootCmd.AddCommand(deferCmd)

	progressCmd.Flags().String("notes", "", "progress notes to append to context")
	rootCmd.AddCommand(progressCmd)
}
