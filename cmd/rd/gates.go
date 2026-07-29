package main

import (
	"fmt"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/3dl-dev/ready/pkg/views"
	"github.com/spf13/cobra"
)

var gatesCmd = &cobra.Command{
	Use:   "gates",
	Short: "Show items with unfulfilled gate escalations",
	Long: `Show work items that have a pending gate awaiting human resolution.

Items appear in the gates view when:
  - status=waiting OR status=blocked (a gate on a blocked item is still pending —
    the ruling is often exactly what unblocks it, ready-e0e)
  - waiting_type=gate
  - a work:gate message has been sent but no work:gate-resolve has been received

A [BLOCKED] item is not yet actionable even once the gate is resolved — it is
still waiting on its dependency.

Use 'rd approve <item-id>' or 'rd reject <item-id>' to resolve a gate; both work
on a blocked-and-gated item without requiring it to be unblocked first.

Convention spec §5: gates view — pending human escalations.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		projectFilter, _ := cmd.Flags().GetString("project")

		items, err := allProjectItems()
		if err != nil {
			return fmt.Errorf("loading items: %w", err)
		}

		// Apply the gates view filter.
		filter := views.GatesFilter()
		items = views.Apply(items, filter)
		items = filterByProject(items, projectFilter)

		sortByPriorityETA(items)

		if jsonOutput {
			return outputItemsJSON(items)
		}

		if len(items) == 0 {
			fmt.Println("no pending gates")
			return nil
		}

		// Print with gate-specific columns: item ID, priority, gate type (from WaitingOn), title.
		// A blocked-and-gated item is flagged [BLOCKED] so the human resolving the gate is
		// not misled into thinking the item is immediately actionable (ready-e0e).
		for _, item := range items {
			waitingOn := item.WaitingOn
			if waitingOn == "" {
				waitingOn = "(no description)"
			}
			title := item.Title
			if item.Status == state.StatusBlocked {
				title = "[BLOCKED] " + title
			}
			fmt.Printf("  %-16s  %-8s  %-36s  %s\n",
				item.ID, item.Priority, truncate(waitingOn, 36), title)
		}
		return nil
	},
}

// truncate returns s truncated to n runes, appending "..." if truncated.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	if n <= 3 {
		return string(runes[:n])
	}
	return string(runes[:n-3]) + "..."
}

func init() {
	gatesCmd.Flags().String("project", "", "filter by project")
	rootCmd.AddCommand(gatesCmd)
}
