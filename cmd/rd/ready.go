package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/3dl-dev/ready/pkg/views"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
)

var readyCmd = &cobra.Command{
	Use:   "ready",
	Short: "Show items needing attention now",
	Long: `Show work items that need attention now.

Items appear in the ready view when:
  - not in a terminal status (done, cancelled, failed)
  - not blocked
  - ETA is within the next 4 hours

Named views:
  ready      what needs attention now (default)
  work       items actively being worked on
  pending    waiting, scheduled, or blocked
  overdue    past-due items
  delegated  work I delegated, in progress
  my-work    work assigned to me

Example:
  rd ready
  rd ready --view overdue
  rd ready --view my-work --json
  rd ready --for ""                show all items, not just mine
  rd ready --label bug             ready items tagged 'bug'
  rd ready --label bug --label p0  ready items tagged both 'bug' AND 'p0'`,
	RunE: func(cmd *cobra.Command, args []string) error {
		viewName, _ := cmd.Flags().GetString("view")
		forFilter, _ := cmd.Flags().GetString("for")
		projectFilter, _ := cmd.Flags().GetString("project")
		scopeKey, _ := cmd.Flags().GetString("scope")
		labelFilters, _ := cmd.Flags().GetStringArray("label")
		offlineFlag, _ := cmd.Flags().GetBool("offline")

		// nostr-native default READ path (ready-6ef S-read): on an `rd init` project
		// the session identity is the secp256k1 signer. The read spine resolves items
		// from the nostr projection (nostrReadActive() is true on this path) — no
		// campfire store is opened. Default --for to the secp256k1 self and run the
		// shared body.
		runReady := func(selfHex string) error {
			// Default --for to the current session identity when not explicitly set.
			if !cmd.Flags().Changed("for") {
				forFilter = selfHex
			}

			items, err := allItemsFromJSONLOrStore()
			if err != nil {
				return fmt.Errorf("loading items: %w", err)
			}

			// Apply view filter.
			if viewName == "" {
				viewName = views.ViewReady
			}
			filter := views.Named(viewName, forFilter)
			if filter == nil {
				return fmt.Errorf("unknown view %q: choose from %v", viewName, views.AllNames())
			}
			items = views.Apply(items, filter)

			// For views that don't filter by identity internally, scope to
			// items where the current identity is involved -- either as the
			// outcome owner (for) or the performer (by). This covers items
			// you created, items delegated to you, and items you own.
			switch viewName {
			case views.ViewDelegated, views.ViewMyWork:
				// Already filtered by identity in the view function.
			default:
				if forFilter != "" {
					items = views.Apply(items, func(item *state.Item) bool {
						return item.For == forFilter || item.By == forFilter
					})
				}
			}

			items = filterByProject(items, projectFilter)

			// Apply label filters (AND semantics: item must carry all requested atoms).
			if len(labelFilters) > 0 {
				for _, atom := range labelFilters {
					items = views.Apply(items, views.LabelFilter(atom))
				}
				// Emit a stderr hint for any atom not in the registry when result is empty.
				if len(items) == 0 {
					printUnknownLabelHints(labelFilters)
				}
			}

			// Scope gate (ready-a55): restrict the list to what the given
			// grant-holder is authorized to claim, derived from the signed
			// kind-39301 role-grants (ready-cb6 I7). The board owner is always
			// allowed; otherwise the key needs a live contributor/maintainer grant.
			if scopeKey != "" {
				if len(scopeKey) != 64 || !isHex(scopeKey) {
					return fmt.Errorf("invalid --scope pubkey %q: must be a 64-character hex string", scopeKey)
				}
				allowed, note := nostrScopeForKey(scopeKey)
				if !allowed {
					if !jsonOutput {
						fmt.Fprintln(os.Stderr, note)
					}
					items = nil
				}
			}

			sortByPriorityETA(items)

			if jsonOutput {
				return outputItemsJSON(items)
			}

			if len(items) == 0 {
				if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
					fmt.Println("nothing ready")
				}
				return nil
			}

			// Pipe-friendly output: print bare IDs when stdout is not a TTY so
			// scripts can do: for id in $(rd ready); do ...; done
			if isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd()) {
				printItemTable(items)
			} else {
				for _, item := range items {
					fmt.Println(item.ID)
				}
			}
			return nil
		}

		if _, native := nostrNativeProject(); native {
			self, err := nostrSelfPubkey()
			if err != nil {
				return err
			}
			// Reads auto-reconcile the pinned board from the read relays into the
			// local authoritative log before computing readiness, so the attention
			// engine reflects other machines' updates with no manual `rd sync`.
			// No-op when local-only; best-effort; --offline skips it.
			autoReconcileBoardBestEffort(offlineFlag)
			return runReady(self)
		}

		// nostr-native only (ready-cb6): no campfire/JSONL agent-and-store read path.
		return errNotNostrProject()
	},
}

func init() {
	readyCmd.Flags().String("view", "ready", "named view: ready, work, pending, overdue, delegated, my-work")
	readyCmd.Flags().String("for", "", "filter by 'for' party (default: current identity; pass \"\" to show all)")
	readyCmd.Flags().String("project", "", "filter by project")
	readyCmd.Flags().String("scope", "", "show only items the given grant-holder pubkey is authorized to claim")
	readyCmd.Flags().StringArray("label", nil, "filter by label atom (repeatable, AND semantics)")
	readyCmd.Flags().Bool("offline", false, "read local only — skip the automatic relay reconcile")
	readyCmd.Flags().Bool("reconcile", false, "deprecated: reads auto-reconcile by default (flag kept as a no-op)")
	_ = readyCmd.Flags().MarkHidden("reconcile")
	rootCmd.AddCommand(readyCmd)
}

// filterByProject returns only items matching the given project, or all items if project is empty.
func filterByProject(items []*state.Item, project string) []*state.Item {
	if project == "" {
		return items
	}
	var out []*state.Item
	for _, item := range items {
		if item.Project == project {
			out = append(out, item)
		}
	}
	return out
}

// sortByPriorityETA sorts items by priority (ascending) then ETA (ascending).
// Used by ready, work, pending, focus, and gates views.
func sortByPriorityETA(items []*state.Item) {
	sort.Slice(items, func(i, j int) bool {
		pi := priorityOrder(items[i].Priority)
		pj := priorityOrder(items[j].Priority)
		if pi != pj {
			return pi < pj
		}
		return items[i].ETA < items[j].ETA
	})
}

// priorityOrder maps priority strings to sort order integers.
func priorityOrder(p string) int {
	switch p {
	case "p0":
		return 0
	case "p1":
		return 1
	case "p2":
		return 2
	case "p3":
		return 3
	default:
		return 9
	}
}

// printItemTable prints items in a compact table format.
// Labels, when present, are appended as a compact suffix on the title cell:
// e.g. "Fix auth bug  [bug,security]". The fixed-width columns are never widened.
func printItemTable(items []*state.Item) {
	for _, item := range items {
		eta := formatETA(item.ETA)
		status := item.Status
		title := item.Title
		if len(item.Labels) > 0 {
			title = title + "  [" + strings.Join(item.Labels, ",") + "]"
		}
		fmt.Printf("  %-16s  %-8s  %-10s  %-10s  %s\n",
			item.ID, item.Priority, status, eta, title)
	}
}

// formatETA formats an ETA string for display.
func formatETA(eta string) string {
	if eta == "" {
		return ""
	}
	t, err := time.Parse(time.RFC3339, eta)
	if err != nil {
		return eta
	}
	now := time.Now()
	diff := t.Sub(now)
	switch {
	case diff < 0:
		return "overdue"
	case diff < time.Hour:
		return fmt.Sprintf("%dm", int(diff.Minutes()))
	case diff < 24*time.Hour:
		return fmt.Sprintf("%dh", int(diff.Hours()))
	default:
		return fmt.Sprintf("%dd", int(diff.Hours()/24))
	}
}

// outputItemsJSON outputs items as JSON.
func outputItemsJSON(items []*state.Item) error {
	if items == nil {
		items = []*state.Item{}
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(items)
}
