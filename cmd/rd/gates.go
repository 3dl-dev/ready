package main

import (
	"fmt"

	"github.com/3dl-dev/ready/pkg/views"
	"github.com/spf13/cobra"
)

var gatesCmd = &cobra.Command{
	Use:   "gates",
	Short: "Show items with unfulfilled gate escalations",
	Long: `Show work items that have a pending gate awaiting human resolution.

Items appear in the gates view when:
  - status=waiting
  - waiting_type=gate
  - a work:gate message has been sent but no work:gate-resolve has been received

Use 'rd approve <item-id>' or 'rd reject <item-id>' to resolve a gate.

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
		for _, item := range items {
			waitingOn := item.WaitingOn
			if waitingOn == "" {
				waitingOn = "(no description)"
			}
			fmt.Printf("  %-16s  %-8s  %-36s  %s\n",
				item.ID, item.Priority, truncate(waitingOn, 36), item.Title)
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
