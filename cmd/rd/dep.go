package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/spf13/cobra"
)

// depCmd is the parent command for dep subcommands.
var depCmd = &cobra.Command{
	Use:   "dep",
	Short: "Manage item dependencies",
	Long: `Manage dependencies between work items.

  rd dep add <blocked-id> <blocker-id>    wire a dependency
  rd dep remove <blocked-id> <blocker-id> remove a dependency
  rd dep tree <id>                        show downstream dependency tree
  rd dep tree --up <id>                   show upstream blocker chain (see also: rd why)`,
}

// depAddCmd implements rd dep add <blocked-id> <blocker-id>.
var depAddCmd = &cobra.Command{
	Use:   "add <blocked-id> <blocker-id>",
	Short: "Wire a dependency: blocker-id blocks blocked-id",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		blockedArg := args[0]
		blockerArg := args[1]

		// Reject explicitly dotted cross-campfire refs for the blocked item.
		// The blocked item must always be local to the current project.
		if state.IsCrossCampfireRef(blockedArg) {
			return fmt.Errorf("cross-project deps not supported for blocked item: %q looks like a cross-campfire reference", blockedArg)
		}

		// nostr-native default write path (ready-6ef): no .cf, secp256k1 signer.
		// Closes the dep publisher gap (deps are card "i" tags; a card-only edit).
		if _, native := nostrNativeProject(); native {
			return runDepAddNostr(blockedArg, blockerArg)
		}

		return errNotNostrProject()
	},
}

// depRemoveCmd implements rd dep remove <blocked-id> <blocker-id>.
var depRemoveCmd = &cobra.Command{
	Use:   "remove <blocked-id> <blocker-id>",
	Short: "Remove a dependency between two items",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		blockedArg := args[0]
		blockerArg := args[1]
		reason, _ := cmd.Flags().GetString("reason")

		// nostr-native default write path (ready-6ef): deps are card "i" tags —
		// removal is a card-only edit; no work:block message lookup needed.
		if _, native := nostrNativeProject(); native {
			_ = reason
			return runDepRemoveNostr(blockedArg, blockerArg)
		}

		return errNotNostrProject()
	},
}

// depTreeCmd implements rd dep tree <id>.
var depTreeCmd = &cobra.Command{
	Use:   "tree <id>",
	Short: "Show the dependency tree rooted at an item",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]

		// --up walks UPSTREAM (blocked_by[]) — the chain of blockers that led to
		// this item — instead of the default downstream (blocks + children) walk.
		if up, _ := cmd.Flags().GetBool("up"); up {
			return runUpTree(itemID)
		}

		// Resolve root item.
		root, err := itemByID(itemID)
		if err != nil {
			return err
		}

		// Load all items from the nostr projection / local JSONL log for tree walking.
		items, err := allProjectItems()
		if err != nil {
			return fmt.Errorf("loading items: %w", err)
		}
		allItems := make(map[string]*state.Item, len(items))
		for _, item := range items {
			allItems[item.ID] = item
		}

		// ready-0103: distinguish PLANNED closure members from ones ADMITTED
		// mid-dispatch (created while some agent held an active claim on
		// another item already inside this same tree). Read-only report over
		// the same closure the tree above walks; never affects the walk itself.
		closure := closureIDs(root.ID, allItems)
		summary := summarizeProvenance(closure, allItems)

		if jsonOutput {
			tree := buildDepTree(root.ID, allItems, map[string]bool{})
			tree.Provenance = &summary
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(tree)
		}

		printDepTree(root, allItems, "", map[string]bool{})
		fmt.Printf("\n%d items in this tree: %d planned, %d admitted mid-dispatch (created while some agent held a claim inside the tree)\n",
			summary.Total, summary.Planned, summary.Admitted)
		return nil
	},
}

// treeNode is used for JSON output of dep tree.
type treeNode struct {
	ID       string      `json:"id"`
	Title    string      `json:"title"`
	Status   string      `json:"status"`
	Children []*treeNode `json:"children,omitempty"`
	// Provenance is set ONLY on the tree's ROOT node (ready-0103) — an existing
	// JSON consumer walking Children sees no new field on any non-root node,
	// so this is purely additive to the wire contract.
	Provenance *provenanceSummary `json:"provenance,omitempty"`
}

// provenanceSummary is ready-0103's DONE CONDITION made countable: of the items
// in a `rd dep tree` closure, how many were PLANNED (wired into the tree from
// the start) versus ADMITTED (created while some agent held an active claim on
// an item already inside this SAME closure — i.e. mid-dispatch churn, not the
// original plan). This is the one query that used to require parsing 3.5MB of
// session JSONL by hand (ready-db0's measured 16% on-objective figure).
type provenanceSummary struct {
	Total    int `json:"total"`
	Planned  int `json:"planned"`
	Admitted int `json:"admitted"`
}

// closureIDs returns every item id in the SAME downstream closure `rd dep
// tree` walks and prints — root plus every item reachable via item.Blocks and
// via child.ParentID == <some id already in the closure> — matching
// buildDepTree/printDepTree's own edge definition exactly, so the provenance
// count is scoped to precisely what the tree above it shows, no more and no
// less. Cycle-safe via the `seen` set (a cycle simply stops re-expanding).
func closureIDs(rootID string, items map[string]*state.Item) map[string]bool {
	seen := map[string]bool{}
	var walk func(id string)
	walk = func(id string) {
		if seen[id] {
			return
		}
		seen[id] = true
		item, ok := items[id]
		if !ok {
			return
		}
		for _, childID := range item.Blocks {
			walk(childID)
		}
		for _, child := range items {
			if child.ParentID == id {
				walk(child.ID)
			}
		}
	}
	walk(rootID)
	return seen
}

// summarizeProvenance classifies every member of closure as PLANNED or
// ADMITTED (ready-0103). A member is ADMITTED when its ClaimedDuring names
// ANOTHER member of this SAME closure — i.e. its creator held an active claim
// on something already inside this objective's own tree at create time. Empty
// ClaimedDuring (no claim, or an ambiguous/zero-claim create), or a
// ClaimedDuring naming an item OUTSIDE this closure (the creator was working
// on something unrelated), both count as PLANNED: this function only ever
// counts an admission it can attribute to THIS closure specifically, never a
// guess. An item id present in the closure walk but absent from `items`
// (a dangling reference) counts as PLANNED — there is no ClaimedDuring to read.
func summarizeProvenance(closure map[string]bool, items map[string]*state.Item) provenanceSummary {
	s := provenanceSummary{Total: len(closure)}
	for id := range closure {
		item, ok := items[id]
		if !ok {
			s.Planned++
			continue
		}
		if item.ClaimedDuring != "" && closure[item.ClaimedDuring] {
			s.Admitted++
		} else {
			s.Planned++
		}
	}
	return s
}

// buildDepTree builds a recursive tree for JSON output.
func buildDepTree(id string, items map[string]*state.Item, visited map[string]bool) *treeNode {
	return buildDepTreeHelper(id, items, visited, make(map[string]bool))
}

func buildDepTreeHelper(id string, items map[string]*state.Item, visited map[string]bool, inPath map[string]bool) *treeNode {
	item, ok := items[id]
	if !ok {
		return &treeNode{ID: id, Title: "(not found)", Status: "unknown"}
	}
	// Check if this node is in the current recursion path (indicates a cycle).
	if inPath[id] {
		return &treeNode{ID: id, Title: item.Title, Status: item.Status + " (cycle)"}
	}
	// Mark as visited to avoid processing the same node twice across different branches.
	visited[id] = true
	// Mark as in the current path for cycle detection.
	inPath[id] = true

	node := &treeNode{ID: id, Title: item.Title, Status: item.Status}
	// Children are: items this one blocks (blocks list) + children by parent_id.
	seen := map[string]bool{}
	for _, childID := range item.Blocks {
		if !seen[childID] {
			seen[childID] = true
			node.Children = append(node.Children, buildDepTreeHelper(childID, items, visited, inPath))
		}
	}
	for _, child := range items {
		if child.ParentID == id && !seen[child.ID] {
			seen[child.ID] = true
			node.Children = append(node.Children, buildDepTreeHelper(child.ID, items, visited, inPath))
		}
	}
	// Remove from the current path when backtracking, but keep in visited
	// to avoid re-processing the same node in different branches.
	delete(inPath, id)
	return node
}

// printDepTree prints an indented dependency tree.
func printDepTree(item *state.Item, items map[string]*state.Item, prefix string, visited map[string]bool) {
	if visited[item.ID] {
		fmt.Printf("%s%s  [%s] (cycle detected)\n", prefix, item.ID, item.Status)
		return
	}
	visited[item.ID] = true

	// Format: <id>  [<status>]  <title>  (blocked by: X, Y)
	// Cross-campfire blockers are annotated with [cross].
	line := fmt.Sprintf("%s  [%s]  %s", item.ID, item.Status, item.Title)
	if len(item.BlockedBy) > 0 {
		blockerStrs := make([]string, len(item.BlockedBy))
		for i, b := range item.BlockedBy {
			if state.IsCrossCampfireRef(b) {
				blockerStrs[i] = b + " [cross]"
			} else {
				blockerStrs[i] = b
			}
		}
		line += fmt.Sprintf("  (blocked by: %s)", strings.Join(blockerStrs, ", "))
	}
	fmt.Println(prefix + line)

	// Child indentation.
	childPrefix := prefix + "  "

	// Show items this one blocks (dependency children).
	seen := map[string]bool{}
	for _, childID := range item.Blocks {
		if seen[childID] {
			continue
		}
		seen[childID] = true
		if child, ok := items[childID]; ok {
			fmt.Printf("%s└─ blocks: ", childPrefix)
			printDepTree(child, items, childPrefix+"   ", visited)
		} else {
			fmt.Printf("%s└─ blocks: %s  (not found)\n", childPrefix, childID)
		}
	}

	// Show child items by parent_id hierarchy.
	for _, child := range items {
		if child.ParentID == item.ID && !seen[child.ID] {
			seen[child.ID] = true
			fmt.Printf("%s└─ child:  ", childPrefix)
			printDepTree(child, items, childPrefix+"   ", visited)
		}
	}

	delete(visited, item.ID)
}

func init() {
	depRemoveCmd.Flags().String("reason", "", "reason for removing the dependency")
	depTreeCmd.Flags().Bool("up", false, "walk upstream (blocked_by) — show the chain of blockers that led to the item")
	depCmd.AddCommand(depAddCmd)
	depCmd.AddCommand(depRemoveCmd)
	depCmd.AddCommand(depTreeCmd)
	rootCmd.AddCommand(depCmd)
}
