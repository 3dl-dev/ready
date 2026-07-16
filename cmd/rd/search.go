package main

import (
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

// matchesSearch reports whether an item matches a case-insensitive substring
// query across its title, context, and history notes. The query is lowercased
// internally, so callers may pass it in any case.
func matchesSearch(item *state.Item, query string) bool {
	if query == "" {
		return true
	}
	lowerQuery := strings.ToLower(query)
	if strings.Contains(strings.ToLower(item.Title), lowerQuery) {
		return true
	}
	if strings.Contains(strings.ToLower(item.Context), lowerQuery) {
		return true
	}
	for _, h := range item.History {
		if strings.Contains(strings.ToLower(h.Note), lowerQuery) {
			return true
		}
	}
	return false
}

// filterBySearch returns the items whose title, context, or history notes contain
// the query as a case-insensitive substring.
func filterBySearch(items []*state.Item, query string) []*state.Item {
	var out []*state.Item
	for _, item := range items {
		if matchesSearch(item, query) {
			out = append(out, item)
		}
	}
	return out
}

var searchCmd = &cobra.Command{
	Use:   "search <text>",
	Short: "Search items by text across title, context, and history notes",
	Long: `Case-insensitive substring search across every item's title, context, and
history notes. Answers "which items mention X" and "what did we try for X".

By default all items (including terminal ones — done/cancelled/failed) are
searched, so past attempts remain findable. Combine with --json for scripting.

Example:
  rd search "auth bug"
  rd search retry --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		query := args[0]
		offline, _ := cmd.Flags().GetBool("offline")

		autoReconcileBoardBestEffort(offline)

		items, err := allProjectItems()
		if err != nil {
			return fmt.Errorf("loading items: %w", err)
		}

		filtered := filterBySearch(items, query)

		sort.Slice(filtered, func(i, j int) bool {
			pi := priorityOrder(filtered[i].Priority)
			pj := priorityOrder(filtered[j].Priority)
			if pi != pj {
				return pi < pj
			}
			return filtered[i].ID < filtered[j].ID
		})

		if jsonOutput {
			return outputItemsJSON(filtered)
		}

		if len(filtered) == 0 {
			if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
				fmt.Printf("no items match %q\n", query)
			}
			return nil
		}

		// Pipe-friendly: bare IDs when not a TTY (mirrors `rd list`).
		if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
			printItemTable(filtered)
		} else {
			for _, item := range filtered {
				fmt.Println(item.ID)
			}
		}
		return nil
	},
}

func init() {
	searchCmd.Flags().Bool("offline", false, "read local only — skip the automatic relay reconcile")
	rootCmd.AddCommand(searchCmd)
}
