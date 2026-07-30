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

// isTTYStdout reports whether stdout is a terminal. Indirected through a var
// (rather than calling isatty directly at each call site) for exactly one
// reason: testability. go test's own stdout is never a real terminal --
// piped through a test runner or a CI log, it always reports non-TTY -- so
// without this indirection, readyCmd.RunE's TTY-only branches (the
// printReadyTree/printItemTable choice, and the pipe-friendly bare-ID
// fallback) could never be driven through RunE in-process at all; a test
// could only ever exercise the non-TTY path. Tests override this var to
// force either branch deterministically (see ready_tree_test.go).
var isTTYStdout = func() bool {
	return isatty.IsTerminal(os.Stdout.Fd()) || isatty.IsCygwinTerminal(os.Stdout.Fd())
}

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
			//
			// scopeGateDenied (ready-497 rework) records whether THIS gate is what
			// zeroed `items`. printIdentityScopeHint's recompute re-derives every
			// other filter that ran above it (view, project, label) over fullItems,
			// but it cannot re-derive this one -- nostrScopeForKey depends on the
			// grant-holder key, which the identity-blind recompute has no notion
			// of. Left unguarded, a denied scope key would make the hint claim
			// identity scope hid the board when the true, already-reported cause
			// was the scope gate (`note`, printed just below).
			scopeGateDenied := false
			if scopeKey != "" {
				if len(scopeKey) != 64 || !isHex(scopeKey) {
					return fmt.Errorf("invalid --scope pubkey %q: must be a 64-character hex string", scopeKey)
				}
				// ready-3e1: normalize before the gate. nostrScopeForKey
				// (cmd/rd/sessions.go) byte-compares its argument against the
				// board owner pubkey and indexes the DeriveLevels map with it;
				// both keys are canonical lowercase because they come from
				// signed events. isHex above accepts A-F as a FORMAT check, so
				// an uppercase --scope for a genuinely granted key misses the
				// map and the gate DENIES it with "no active grant ... (not a
				// granted identity)" — the same silent-wrong-answer class as
				// the dead grant, in the read direction.
				scopeKey = normalizeHexPubkey(scopeKey)
				allowed, note := nostrScopeForKey(scopeKey)
				if !allowed {
					// Stderr is a separate stream from stdout, so this note is
					// safe to print unconditionally -- including in --json mode,
					// where stdout must stay a clean JSON document but the
					// diagnostic still needs to reach the user (ready-497
					// rework #1: the old `if !jsonOutput` guard here made the
					// --json path fully silent on a denied scope, since
					// printIdentityScopeHint also defers to this note via
					// scopeGateDenied and never fires itself).
					fmt.Fprintln(os.Stderr, note)
					items = nil
					scopeGateDenied = true
				}
			}

			sortByPriorityETA(items)

			// ready-497: an identity-scoped view returning bare empty output is
			// indistinguishable from a genuinely empty board. The repro that
			// filed this item was `rd ready` (0 bytes, exit 0) vs `rd ready
			// --for ""` (10 items) vs `rd list` (17 open items) -- silence read
			// as "all caught up" when 17 items existed, just not for the caller.
			// Emit the hint to STDERR, and do it BEFORE the jsonOutput branch
			// returns, so every downstream shape (tree, --flat, piped bare-ID,
			// --json) gets it identically -- stderr is a separate stream from
			// whichever of those stdout contracts is in play, so this can never
			// corrupt --json or piped machine consumption on either path.
			if len(items) == 0 {
				printIdentityScopeHint(viewName, forFilter, fullItems, filter, projectFilter, labelFilters, scopeGateDenied)
			}

			if jsonOutput {
				return outputItemsJSON(items)
			}

			if len(items) == 0 {
				if isTTYStdout() {
					fmt.Println("nothing ready")
				}
				return nil
			}

			// Pipe-friendly output: print bare IDs when stdout is not a TTY so
			// scripts can do: for id in $(rd ready); do ...; done
			if isTTYStdout() {
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

// identityBlindViewFilter returns viewName's Filter with identity scoping
// removed -- same status/shape semantics, but not restricted to any one
// party. For every named view EXCEPT my-work/delegated, the identity match
// is layered on afterward in runReady (see the switch at the top of runReady
// that scopes non-my-work/delegated views to forFilter's party), so the
// Filter object itself (views.Named's return value) never restricted by
// identity in the first place and is reused as-is. my-work/delegated are the
// two views where identity is baked directly into the Filter (via
// MyWorkFilterSet/DelegatedFilterSet's idset), so an identity-blind
// equivalent has to be built separately here, matching each one's
// non-identity shape (MyWorkFilterSet: idset[By] && !terminal;
// DelegatedFilterSet: idset[For] && By set && By outside idset && active).
func identityBlindViewFilter(viewName string, filter views.Filter) views.Filter {
	switch viewName {
	case views.ViewMyWork:
		return func(item *state.Item) bool {
			return item.By != "" && !state.IsTerminal(item)
		}
	case views.ViewDelegated:
		return func(item *state.Item) bool {
			return item.For != "" && item.By != "" && item.For != item.By && item.Status == state.StatusActive
		}
	default:
		return filter
	}
}

// printIdentityScopeHint writes a one-line stderr hint distinguishing "the
// identity scope hid real work" (ready-497) from "the board is genuinely
// empty". Only called when the final, fully-filtered item set is already
// empty. It recomputes the SAME view, shaped the same way but with identity
// scoping removed (identityBlindViewFilter), over fullItems -- the complete,
// unfiltered project snapshot -- and re-applies the same project/label
// filters actually in effect, so the only variable that differs from the
// real computation is identity. If that recomputation is ALSO empty, the
// board is genuinely empty for this view and nothing is printed (the
// existing "nothing ready" / empty stdout behavior is unchanged). If it
// finds items, the caller was about to report silence over a non-empty
// board, so the hint names the identity in effect and how many items exist
// outside its scope.
//
// forFilter == "" means no identity is actively narrowing the view (the
// caller already asked for everything with --for ""), so there is nothing
// to attribute the emptiness to and the hint does not fire.
//
// scopeDenied reports whether the --scope gate (nostrScopeForKey, applied by
// the caller AFTER every filter this function reproduces) is what zeroed the
// real item set. That gate has no identity-blind equivalent here -- the
// recompute below has no notion of a grant-holder key -- so if the scope
// gate is the reason items is empty, this function cannot tell whether
// identity ALSO would have hidden something and must not guess. The caller
// already printed the scope denial's own note, which names the real cause;
// emitting the identity hint on top of it would misattribute an unrelated
// gate to identity scope (ready-497 rework, live-run false positive: one
// item For==By==self, --for self, --scope an ungranted key -- the hint fired
// blaming identity when the scope gate was the actual and only cause).
//
// The project reapply's real-world reach is narrower than it looks: Item.Project
// has no nostr wire carrier today (ready-762), so every item in fullItems has
// Project == "" regardless of what was set before publish, and a non-empty
// projectFilter drops all of them uniformly. Re-check this function once
// ready-762 lands a real carrier -- a wire-carried Project reopens the
// original false-positive concern (a --project filter hiding the caller's
// OWN item, misattributed here to identity) as a live case again.
func printIdentityScopeHint(viewName, forFilter string, fullItems []*state.Item, filter views.Filter, projectFilter string, labelFilters []string, scopeDenied bool) {
	if forFilter == "" {
		return
	}
	if scopeDenied {
		return
	}
	blind := identityBlindViewFilter(viewName, filter)
	hidden := views.Apply(fullItems, blind)
	hidden = filterByProject(hidden, projectFilter)
	for _, atom := range labelFilters {
		hidden = views.Apply(hidden, views.LabelFilter(atom))
	}
	if len(hidden) == 0 {
		return
	}
	suggestion := `rd ready --for ""`
	if viewName != views.ViewReady {
		suggestion = fmt.Sprintf("rd ready --view %s --for \"\"", viewName)
	}
	fmt.Fprintf(os.Stderr, "0 items %s for %s. %d item(s) exist for other parties. Try: %s\n",
		viewName, shortKey(forFilter), len(hidden), suggestion)
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

// sortByPriorityETA sorts items by priority (ascending), then ETA (ascending),
// then ID (ascending) as a final tiebreak. Used by ready, work, pending,
// focus, and gates views.
//
// The ID tiebreak (ready-e88 rework, challenge 5) makes this a strict total
// order over the input, and therefore deterministic regardless of the
// slice's incoming order -- which matters because callers build that slice
// from a nostr projection whose map iteration is itself nondeterministic
// (board-fold-spec.md §15.7). Before this fix, two items sharing a priority
// and ETA (or both empty, the common case for un-triaged items) sorted in
// whatever order they happened to arrive in, so `rd ready`'s piped/--json
// output could reorder between two runs of the SAME binary over the SAME
// board with no state change in between -- observed directly: two live-board
// runs differed in bare-ID order and in 6 --json fields (nested `blocks`
// array order, itself sorted by iteration of a different unordered
// collection upstream). sort.Slice's comparator was already correct given a
// total order; it only lacked one (nothing broke the tie past ETA).
// Switching to SliceStable is defense-in-depth on top of the ID tiebreak --
// once the tiebreak makes every comparison unambiguous, input order can no
// longer influence output order at all.
func sortByPriorityETA(items []*state.Item) {
	sort.SliceStable(items, func(i, j int) bool {
		pi := priorityOrder(items[i].Priority)
		pj := priorityOrder(items[j].Priority)
		if pi != pj {
			return pi < pj
		}
		if items[i].ETA != items[j].ETA {
			return items[i].ETA < items[j].ETA
		}
		return items[i].ID < items[j].ID
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

// headerThreshold is the smallest ready-children-under-an-epic count for
// which printing an epic header is not itself a regression against the flat
// list (ready-e88 rework, challenge 4 -- "small epics render longer than
// flat"). Printing a header costs 1 line; the cap+"more" line costs another
// 1 line once the group exceeds maxChildrenPerEpic. So for a group of N
// ready children the header form costs:
//
//	N <= maxChildrenPerEpic:  1 (header) + N                      = N+1
//	N >  maxChildrenPerEpic:  1 (header) + maxChildrenPerEpic + 1 = maxChildrenPerEpic+2
//
// The header form only breaks even with (or beats) the flat N-line cost
// once maxChildrenPerEpic+2 <= N, i.e. N >= maxChildrenPerEpic+2 -- one MORE
// than "exceeds the cap" would naively suggest, because collapsing exactly
// one item into a "+1 more" line still costs a line to say so. Any group at
// or under this threshold is inlined instead (see printReadyTree): its rows
// print with no header at all, so its contribution to total line count is
// exactly what the flat list would have used for the same items -- never
// more. This is what closes the "many small epics" gap: a board of many
// epics each with 2-3 ready children renders identically to the flat list
// instead of costing +1 line per epic.
const headerThreshold = maxChildrenPerEpic + 2

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
// or the indentation) is what actually shortens the output, and
// headerThreshold's doc for why small groups skip the header entirely.
//
// A ready item's own root can itself be ready (an unblocked epic that is
// also directly workable -- ready-e88 rework, challenge 3), not just a
// closed/blocked aggregator. displayChildren excludes the root from the
// rows nested under its own header so it is never printed twice (once as
// the header, again as a child row identical to it), and the "(N ready)"
// header count reflects only the rows shown/collapsed below the header --
// consistent with the meaning that count has always had for the ordinary
// case where the root itself is never ready.
func printReadyTree(items []*state.Item, allItems []*state.Item) {
	byID := make(map[string]*state.Item, len(allItems))
	for _, it := range allItems {
		byID[it.ID] = it
	}
	for _, g := range buildReadyGroups(items, byID) {
		displayChildren := make([]*state.Item, 0, len(g.children))
		for _, c := range g.children {
			if c.ID == g.root.ID {
				continue
			}
			displayChildren = append(displayChildren, c)
		}

		// Inline (no epic header) whenever a header can't beat the flat list
		// for this group's items: a true orphan / a ready root with no other
		// ready children (displayChildren empty -- the group is just the root
		// itself), or any group at or under headerThreshold (challenge 4).
		// Printing every item in g.children with no header/cap/more bookkeeping
		// reproduces exactly what the flat list would have printed for this
		// same item set -- so this branch can never cost more lines than flat.
		if len(displayChildren) < headerThreshold {
			for _, c := range g.children {
				fmt.Println(formatItemRow(c, "  "))
			}
			continue
		}

		fmt.Printf("%s  (%d ready)  %s\n", g.root.ID, len(displayChildren), g.root.Title)
		shown := displayChildren
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
