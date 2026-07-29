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
  rd ready --label bug --label p0  ready items tagged both 'bug' AND 'p0'
  rd ready --flat                  the old flat list, no epic grouping

The default (TTY) view groups ready items under their epic -- the topmost
parent_id ancestor -- indented beneath it, each epic capped to a few children
with a "N more" line past the cap. --flat prints the pre-ready-e88 flat list
instead. --json and piped (non-TTY) output are unaffected either way.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		viewName, _ := cmd.Flags().GetString("view")
		forFilter, _ := cmd.Flags().GetString("for")
		projectFilter, _ := cmd.Flags().GetString("project")
		scopeKey, _ := cmd.Flags().GetString("scope")
		labelFilters, _ := cmd.Flags().GetStringArray("label")
		offlineFlag, _ := cmd.Flags().GetBool("offline")
		flatFlag, _ := cmd.Flags().GetBool("flat")

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

			items, err := allProjectItems()
			if err != nil {
				return fmt.Errorf("loading items: %w", err)
			}

			// fullItems is the complete, unfiltered project snapshot (every status,
			// every project) captured before any view/identity/label/scope filter
			// narrows `items` below. The indented-tree render (printReadyTree) needs
			// it to walk parent_id chains: an epic's ready descendant can sit behind
			// a closed/terminal intermediate item, or belong to an epic this filter
			// pass would otherwise exclude, and the tree must still resolve to the
			// right ancestor. Read-only: never republished (ready-500 guard).
			fullItems := items

			// Apply view filter.
			if viewName == "" {
				viewName = views.ViewReady
			}
			var filter views.Filter
			switch viewName {
			case views.ViewMyWork, views.ViewDelegated:
				// Party-aware identity scoping (ready-f0b, extends ready-99d edge #6
				// to my-work/delegated): resolve forFilter's party ONCE here, then
				// match items whose By/For is ANY pubkey or email in that party —
				// not raw string equality. On a box with no person-alias the set
				// collapses to {forFilter}, preserving the pre-ready-f0b single-
				// identity behaviour. An empty forFilter yields an empty set, which
				// matches nothing (same as before).
				idset := map[string]bool{}
				if forFilter != "" {
					idset = nostrPartyIdentitySet(forFilter)
				}
				if viewName == views.ViewMyWork {
					filter = views.MyWorkFilterSet(idset)
				} else {
					filter = views.DelegatedFilterSet(idset)
				}
			default:
				filter = views.Named(viewName, forFilter)
				if filter == nil {
					return fmt.Errorf("unknown view %q: choose from %v", viewName, views.AllNames())
				}
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
					// Party-aware identity scoping (ready-99d, edge #6): an item is
					// "mine" if its For/By is ANY pubkey or email in forFilter's party,
					// not just forFilter verbatim. On a box with no person-alias the set
					// collapses to {forFilter}, preserving the raw-pubkey behaviour.
					idset := nostrPartyIdentitySet(forFilter)
					items = views.Apply(items, func(item *state.Item) bool {
						return idset[item.For] || idset[item.By]
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
				// Owner ruling (ready-e88, 2026-07-29): the default human-facing
				// `rd ready` view groups items under their epic (topmost parent_id
				// ancestor) and indents children, with the pre-existing flat table
				// kept behind --flat. Only the default "ready" view groups -- other
				// named views (--view work/pending/my-work/etc.) are unaffected, and
				// so is every non-TTY / --json path above and below this branch.
				if viewName == views.ViewReady && !flatFlag {
					printReadyTree(items, fullItems)
				} else {
					printItemTable(items)
				}
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
	readyCmd.Flags().Bool("flat", false, "show the flat list without epic grouping (pre-ready-e88 behavior)")
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

// maxChildrenPerEpic caps how many of an epic's ready children are printed
// before the tree view collapses the remainder into a single "N more" line.
// This is the actual line-count reduction the ready-e88 owner ruling
// requires: an indented tree that prints every item is still one line per
// item and fails the "one screen" outcome exactly like the flat list does.
// Capping is what makes an epic with 20+ ready children collapse to a
// handful of lines instead of 20+.
const maxChildrenPerEpic = 5

// readyGroup is one epic's worth of ready items in the indented tree: the
// root item (the topmost parent_id ancestor reachable from any member) and
// the ready items that belong to it.
type readyGroup struct {
	root     *state.Item
	children []*state.Item
}

// findReadyRoot walks item's parent_id chain upward through byID -- which
// must index items of EVERY status, not just ready ones, because an
// ancestor partway up the chain can be closed/terminal and is still a valid
// link (parent_id records structure, not liveness; ready-500's derived-
// status guard is about writes, not this read-only walk).
//
// The walk stops, returning the last resolvable item, when:
//   - item.ParentID == "" (a genuine root / an orphan with no parent at all,
//     both present on the live board today), or
//   - item.ParentID does not resolve in byID (a dangling pointer -- none
//     exist on the current board, per ready-e88's own measurement, but the
//     walk must not panic if one appears), or
//   - the chain revisits an item already seen (a cycle -- the board is
//     verified acyclic today, but this must never hang).
func findReadyRoot(item *state.Item, byID map[string]*state.Item) *state.Item {
	cur := item
	seen := map[string]bool{cur.ID: true}
	for cur.ParentID != "" {
		parent, ok := byID[cur.ParentID]
		if !ok || seen[parent.ID] {
			break
		}
		seen[parent.ID] = true
		cur = parent
	}
	return cur
}

// buildReadyGroups groups ready items by their epic (findReadyRoot's
// result), preserving first-appearance order in items -- which is already
// priority/ETA sorted by the time this runs, so the highest-priority epic's
// group leads. byID must cover every item on the board, all statuses (see
// findReadyRoot).
func buildReadyGroups(items []*state.Item, byID map[string]*state.Item) []*readyGroup {
	order := make([]string, 0, len(items))
	groups := make(map[string]*readyGroup, len(items))
	for _, item := range items {
		root := findReadyRoot(item, byID)
		g, ok := groups[root.ID]
		if !ok {
			g = &readyGroup{root: root}
			groups[root.ID] = g
			order = append(order, root.ID)
		}
		g.children = append(g.children, item)
	}
	out := make([]*readyGroup, 0, len(order))
	for _, id := range order {
		out = append(out, groups[id])
	}
	return out
}

// formatItemRow formats one item as a single table row -- the same columns
// printItemTable uses (ID, priority, status, ETA, title with a labels
// suffix) -- prefixed with indent, so the tree view can nest child rows
// under an epic header while the flat table keeps its original 2-space lead.
func formatItemRow(item *state.Item, indent string) string {
	eta := formatETA(item.ETA)
	title := item.Title
	if len(item.Labels) > 0 {
		title = title + "  [" + strings.Join(item.Labels, ",") + "]"
	}
	return fmt.Sprintf("%s%-16s  %-8s  %-10s  %-10s  %s",
		indent, item.ID, item.Priority, item.Status, eta, title)
}

// printReadyTree renders ready items grouped under their epic, indented,
// each epic capped at maxChildrenPerEpic children with a "N more" line past
// the cap -- see maxChildrenPerEpic's doc for why the cap (not the grouping
// or the indentation) is what actually shortens the output.
//
// A group whose only member IS its own root -- a true orphan (no parent_id
// at all) or a dangling-pointer fallback, both handled identically by
// findReadyRoot -- has nothing to nest it under, so it renders as one plain
// row with no epic header (a header would just repeat the same line).
func printReadyTree(items []*state.Item, allItems []*state.Item) {
	byID := make(map[string]*state.Item, len(allItems))
	for _, it := range allItems {
		byID[it.ID] = it
	}
	for _, g := range buildReadyGroups(items, byID) {
		if len(g.children) == 1 && g.children[0].ID == g.root.ID {
			fmt.Println(formatItemRow(g.children[0], "  "))
			continue
		}
		fmt.Printf("%s  (%d ready)  %s\n", g.root.ID, len(g.children), g.root.Title)
		shown := g.children
		more := 0
		if len(shown) > maxChildrenPerEpic {
			more = len(shown) - maxChildrenPerEpic
			shown = shown[:maxChildrenPerEpic]
		}
		for _, item := range shown {
			fmt.Println(formatItemRow(item, "    "))
		}
		if more > 0 {
			fmt.Printf("    … +%d more (rd dep tree %s / rd ready --flat for the full list)\n", more, g.root.ID)
		}
	}
}
