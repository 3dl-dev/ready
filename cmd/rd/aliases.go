package main

import (
	"fmt"
	"time"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/3dl-dev/ready/pkg/timeparse"
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

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		if _, native := nostrNativeProject(); !native {
			return errNotNostrProject()
		}

		// Cascade: close open descendants (recursive subtree) first, leaves-up.
		if cascade {
			allItems, err := allProjectItems()
			if err != nil {
				return fmt.Errorf("loading items for cascade: %w", err)
			}
			closedIDs, err := cascadeCloseDescendants(allItems, itemID, reason, func(child *state.Item, reason string) error {
				return runCloseNostr(child.ID, "cancelled", reason, "closed")
			})
			if err != nil {
				return err
			}
			for _, childID := range closedIDs {
				if !jsonOutput {
					fmt.Printf("closed %s (cancelled, cascaded)\n", childID)
				}
			}
		}

		return runCloseNostr(itemID, "cancelled", reason, "closed")
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

		// nostr-native write path (ready-cb6): defer is a card-only ETA edit.
		if _, native := nostrNativeProject(); !native {
			return errNotNostrProject()
		}
		return runUpdateNostr(itemID, nostrUpdateSpec{eta: etaRFC3339, hasFieldUpdate: true})
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

		// nostr-native write path (ready-cb6): progress appends to the card context.
		if _, native := nostrNativeProject(); !native {
			return errNotNostrProject()
		}
		item, err := nostrResolveItem(itemID)
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
		return runUpdateNostr(itemID, nostrUpdateSpec{context: newContext, hasFieldUpdate: true})
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

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		if _, native := nostrNativeProject(); !native {
			return errNotNostrProject()
		}
		return runCloseNostr(itemID, resolution, reason, "closed")
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
