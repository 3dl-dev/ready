package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/spf13/cobra"
)

// upNode is the JSON shape for an upstream (provenance) tree. Unlike treeNode —
// which walks DOWNSTREAM (what an item unlocks) — upNode walks UPSTREAM along
// blocked_by[]: the chain of blockers that LED TO an item.
type upNode struct {
	ID        string    `json:"id"`
	Title     string    `json:"title"`
	Status    string    `json:"status"`
	BlockedBy []*upNode `json:"blocked_by,omitempty"`
}

// buildUpTree builds a recursive upstream provenance tree rooted at id, walking
// each item's BlockedBy[] edges. Cycle-safe: a node already on the current
// recursion path is emitted once with a "(cycle)" status suffix and not re-walked.
func buildUpTree(id string, items map[string]*state.Item, inPath map[string]bool) *upNode {
	item, ok := items[id]
	if !ok {
		return &upNode{ID: id, Title: "(not found)", Status: "unknown"}
	}
	if inPath[id] {
		return &upNode{ID: id, Title: item.Title, Status: item.Status + " (cycle)"}
	}
	inPath[id] = true

	node := &upNode{ID: id, Title: item.Title, Status: item.Status}
	seen := map[string]bool{}
	for _, blockerID := range item.BlockedBy {
		if seen[blockerID] {
			continue
		}
		seen[blockerID] = true
		node.BlockedBy = append(node.BlockedBy, buildUpTree(blockerID, items, inPath))
	}

	delete(inPath, id)
	return node
}

// printUpTree prints an indented upstream provenance tree. Each level lists the
// blockers that led to the item above it. Cross-project blockers are annotated
// [cross]; cycles are annotated inline.
func printUpTree(item *state.Item, items map[string]*state.Item, prefix string, inPath map[string]bool) {
	if inPath[item.ID] {
		fmt.Printf("%s%s  [%s]  %s  (cycle detected)\n", prefix, item.ID, item.Status, item.Title)
		return
	}
	inPath[item.ID] = true

	fmt.Printf("%s%s  [%s]  %s\n", prefix, item.ID, item.Status, item.Title)

	childPrefix := prefix + "  "
	seen := map[string]bool{}
	for _, blockerID := range item.BlockedBy {
		if seen[blockerID] {
			continue
		}
		seen[blockerID] = true
		if state.IsCrossCampfireRef(blockerID) {
			fmt.Printf("%s└─ blocked by: %s [cross]\n", childPrefix, blockerID)
			continue
		}
		if blocker, ok := items[blockerID]; ok {
			fmt.Printf("%s└─ blocked by: ", childPrefix)
			printUpTree(blocker, items, childPrefix+"   ", inPath)
		} else {
			fmt.Printf("%s└─ blocked by: %s  (not found)\n", childPrefix, blockerID)
		}
	}

	delete(inPath, item.ID)
}

// runUpTree renders the upstream provenance tree for itemID. Shared by
// `rd why <id>` and `rd dep tree --up <id>`.
func runUpTree(itemID string) error {
	root, err := itemByID(itemID)
	if err != nil {
		return err
	}
	items, err := allProjectItems()
	if err != nil {
		return fmt.Errorf("loading items: %w", err)
	}
	allItems := make(map[string]*state.Item, len(items))
	for _, item := range items {
		allItems[item.ID] = item
	}

	if jsonOutput {
		tree := buildUpTree(root.ID, allItems, map[string]bool{})
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(tree)
	}

	printUpTree(root, allItems, "", map[string]bool{})
	return nil
}

// whyCmd implements `rd why <id>` — the upstream counterpart to `rd dep tree`.
var whyCmd = &cobra.Command{
	Use:   "why <id>",
	Short: "Show the upstream chain of blockers that led to an item",
	Long: `Walk blocked_by[] recursively to show WHY an item exists — the chain of
blockers that had to be created before it. This is the upstream/provenance view;
'rd dep tree' walks the opposite (downstream) direction.

Cycle-safe. Supports --json.

Example:
  rd why ready-a1b
  rd why ready-a1b --json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runUpTree(args[0])
	},
}

func init() {
	rootCmd.AddCommand(whyCmd)
}
