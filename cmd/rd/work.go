package main

import (
	"fmt"

	"github.com/campfire-net/ready/pkg/views"
	"github.com/spf13/cobra"
)

var workCmd = &cobra.Command{
	Use:   "work",
	Short: "Show items actively being worked on",
	Long: `Show work items with status=active — items currently being worked on.

Use --for to filter by the party the work is assigned to.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		forFilter, _ := cmd.Flags().GetString("for")
		projectFilter, _ := cmd.Flags().GetString("project")

		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		items, err := allItemsFromJSONLOrStore(s)
		if err != nil {
			return fmt.Errorf("loading items: %w", err)
		}

		var filter views.Filter
		if forFilter != "" {
			filter = views.MyWorkFilter(forFilter)
		} else {
			filter = views.WorkFilter()
		}
		items = views.Apply(items, filter)
		items = filterByProject(items, projectFilter)

		sortByPriorityETA(items)

		if jsonOutput {
			return outputItemsJSON(items)
		}

		if len(items) == 0 {
			fmt.Println("nothing active")
			return nil
		}

		printItemTable(items)
		return nil
	},
}

func init() {
	workCmd.Flags().String("for", "", "filter by party (by field) — shows my-work view for that identity")
	workCmd.Flags().String("project", "", "filter by project")
	rootCmd.AddCommand(workCmd)
}
