package main

import (
	"fmt"

	"github.com/3dl-dev/ready/pkg/views"
	"github.com/spf13/cobra"
)

var pendingCmd = &cobra.Command{
	Use:   "pending",
	Short: "Show items in waiting, scheduled, or blocked status",
	Long: `Show work items that are pending — waiting on something, scheduled for later,
or blocked by a dependency.

Items appear in the pending view when status is one of: waiting, scheduled, blocked.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		projectFilter, _ := cmd.Flags().GetString("project")

		items, err := allProjectItems()
		if err != nil {
			return fmt.Errorf("loading items: %w", err)
		}

		filter := views.PendingFilter()
		items = views.Apply(items, filter)
		items = filterByProject(items, projectFilter)

		sortByPriorityETA(items)

		if jsonOutput {
			return outputItemsJSON(items)
		}

		if len(items) == 0 {
			fmt.Println("nothing pending")
			return nil
		}

		printItemTable(items)
		return nil
	},
}

func init() {
	pendingCmd.Flags().String("project", "", "filter by project")
	rootCmd.AddCommand(pendingCmd)
}
