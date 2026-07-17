package main

import (
	"fmt"
	"os"
	"sort"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/3dl-dev/ready/pkg/views"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List work items",
	Long: `List work items across all projects.

Filters (all optional, combinable):
  --status     filter by status (repeatable, OR semantics)
  --resolution filter by outcome of closed items (done, failed, cancelled)
  --for       filter by 'for' party
  --by        filter by 'by' party
  --project   filter by project
  --priority  filter by priority (p0, p1, p2, p3)
  --type      filter by type
  --label     filter by label atom (repeatable, AND semantics)
  --all       include terminal items (done, cancelled, failed)

By default, terminal items (done, cancelled, failed) are excluded.

Example:
  rd list                                    all open items
  rd list --all                              include done/cancelled
  rd list --status inbox --status active     OR filter
  rd list --by atlas/worker-3 --json         machine-readable
  rd list --priority p0 --priority p1        urgent items only
  rd list --label bug                        items tagged 'bug'
  rd list --label bug --label security       items tagged both 'bug' AND 'security'
  rd list --resolution failed                items we tried and that failed`,
	RunE: func(cmd *cobra.Command, args []string) error {
		statusFilters, _ := cmd.Flags().GetStringArray("status")
		resolutionFilter, _ := cmd.Flags().GetString("resolution")
		forFilter, _ := cmd.Flags().GetString("for")
		byFilter, _ := cmd.Flags().GetString("by")
		projectFilter, _ := cmd.Flags().GetString("project")
		priorityFilter, _ := cmd.Flags().GetString("priority")
		typeFilter, _ := cmd.Flags().GetString("type")
		labelFilters, _ := cmd.Flags().GetStringArray("label")
		all, _ := cmd.Flags().GetBool("all")
		offline, _ := cmd.Flags().GetBool("offline")

		// --resolution is a first-class outcome filter. A resolution IS the terminal
		// status of a closed item, so it resolves to an explicit --status entry
		// (which also lifts the default terminal-item exclusion). Answers "what did
		// we try for X and how did it go" via `rd list --resolution failed`.
		if resolutionFilter != "" {
			canonical, err := validateResolution(resolutionFilter)
			if err != nil {
				cmd.SilenceUsage = true
				return err
			}
			statusFilters = append(statusFilters, canonical)
		}

		// Reads auto-reconcile from the read relays first (no-op when local-only,
		// best-effort, --offline skips) so the list reflects other machines.
		autoReconcileBoardBestEffort(offline)

		items, err := allProjectItems()
		if err != nil {
			return fmt.Errorf("loading items: %w", err)
		}

		// Apply filters. The 'for' filter is party-aware (ready-99d, edge #6): an
		// explicit --for <pubkey|email> matches items whose For is ANY pubkey or email
		// in that identity's party, not just the verbatim value. Expansion happens here
		// (not inside applyListFilters, whose exact-match signature many tests pin), so
		// applyListFilters is called with the For predicate lifted out. A --for that
		// resolves to no party collapses to {forFilter}, i.e. the prior exact match.
		// --for "" / unset stays show-all (no For scoping), preserving list semantics.
		filtered := applyListFilters(items, statusFilters, "", byFilter, projectFilter, priorityFilter, typeFilter, all)
		if forFilter != "" {
			idset := nostrPartyIdentitySet(forFilter)
			filtered = views.Apply(filtered, func(item *state.Item) bool {
				return idset[item.For]
			})
		}

		// Apply label filters (AND semantics: item must carry all requested atoms).
		if len(labelFilters) > 0 {
			for _, atom := range labelFilters {
				filtered = views.Apply(filtered, views.LabelFilter(atom))
			}
			// Emit a stderr hint for any atom that does not appear in any item's
			// labels — it may be an unknown atom or a typo.
			if len(filtered) == 0 {
				printUnknownLabelHints(labelFilters)
			}
		}

		// Sort by priority then ID.
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
				fmt.Println("no items found")
			}
			return nil
		}

		// Pipe-friendly output: print bare IDs when stdout is not a TTY so
		// scripts can do: for id in $(rd list); do ...; done
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

// applyListFilters filters items according to the list command's flag values.
// statusFilters uses OR semantics: an item matches if its status equals any of the
// provided values. When statusFilters is empty and all is false, terminal items
// (done, cancelled, failed) are excluded by default.
func applyListFilters(items []*state.Item, statusFilters []string, forFilter, byFilter, projectFilter, priorityFilter, typeFilter string, all bool) []*state.Item {
	var filtered []*state.Item
	for _, item := range items {
		if !all && state.IsTerminal(item) && len(statusFilters) == 0 {
			continue
		}
		if len(statusFilters) > 0 {
			matched := false
			for _, sf := range statusFilters {
				if item.Status == resolveStatus(sf) {
					matched = true
					break
				}
			}
			if !matched {
				continue
			}
		}
		if forFilter != "" && item.For != forFilter {
			continue
		}
		if byFilter != "" && item.By != byFilter {
			continue
		}
		if projectFilter != "" && item.Project != projectFilter {
			continue
		}
		if priorityFilter != "" && item.Priority != priorityFilter {
			continue
		}
		if typeFilter != "" && item.Type != typeFilter {
			continue
		}
		filtered = append(filtered, item)
	}
	return filtered
}

// validateResolution validates a --resolution value and returns the canonical
// terminal status it maps to. Accepts the three terminal resolutions
// (done, failed, cancelled) plus the "completed" bd-compat alias for done.
func validateResolution(resolution string) (string, error) {
	switch resolution {
	case state.StatusDone, "completed":
		return state.StatusDone, nil
	case state.StatusFailed:
		return state.StatusFailed, nil
	case state.StatusCancelled:
		return state.StatusCancelled, nil
	default:
		return "", fmt.Errorf("invalid --resolution %q: must be one of done, failed, cancelled", resolution)
	}
}

// printUnknownLabelHints writes a hint to stderr for each requested label atom
// that is not present in the label registry. This is called only when the
// label-filtered result is empty, to help users distinguish "valid label, no
// matching items" from "atom not in registry / possible typo".
//
// nostr-native (ready-cb6): the label registry is no longer a campfire store
// construct. The nostr projection has no per-project registry — card "l" tags are
// freeform — so the hint checks against the built-in seed atoms only (the same set
// `rd label list` shows). Best-effort: an atom outside the seed set is flagged.
func printUnknownLabelHints(atoms []string) {
	registry := state.DeriveAll("", nil).LabelRegistry()
	if registry == nil {
		return
	}
	for _, atom := range atoms {
		if _, known := registry[atom]; !known {
			fmt.Fprintf(os.Stderr, "hint: label %q is not in the registry — run `rd label list` to see valid atoms\n", atom)
		}
	}
}

func init() {
	listCmd.Flags().StringArray("status", nil, "filter by status (repeatable, OR semantics)")
	listCmd.Flags().String("resolution", "", "filter by outcome of closed items: done, failed, or cancelled")
	listCmd.Flags().String("for", "", "filter by 'for' party")
	listCmd.Flags().String("by", "", "filter by 'by' party")
	listCmd.Flags().String("project", "", "filter by project")
	listCmd.Flags().String("priority", "", "filter by priority")
	listCmd.Flags().String("type", "", "filter by type")
	listCmd.Flags().StringArray("label", nil, "filter by label atom (repeatable, AND semantics)")
	listCmd.Flags().Bool("all", false, "include terminal items (done, cancelled, failed)")
	listCmd.Flags().Bool("offline", false, "read local only — skip the automatic relay reconcile")
	rootCmd.AddCommand(listCmd)
}
