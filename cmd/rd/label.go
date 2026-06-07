package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"text/tabwriter"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/spf13/cobra"

	"github.com/campfire-net/ready/pkg/state"
)

var labelCmd = &cobra.Command{
	Use:   "label",
	Short: "Manage labels in the campfire label registry",
	Long: `Manage labels in the per-campfire label registry.

Labels are named atoms used to categorize and filter work items.
The registry is seeded with built-in labels (bug, feature, question,
security, sweep-finding, blog-candidate). Grant-holders (level >= 2)
can define additional labels.

Commands:
  rd label define <name> [--description "..."]   Define a new label
  rd label list                                   List the registry
  rd label add <item-id> <label>                  Add a label to an existing item
  rd label remove <item-id> <label>               Remove a label from an existing item`,
}

var labelDefineCmd = &cobra.Command{
	Use:   "define <name>",
	Short: "Define a label in the campfire registry",
	Long: `Define a label in the per-campfire label registry.

Only grant-holders (operator level >= 2) can define labels.
The label name must match: ^[a-z0-9][a-z0-9-]{0,31}$

Example:
  rd label define my-label --description "A custom label for tracking X"
  rd label define hotfix`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		label := args[0]
		description, _ := cmd.Flags().GetString("description")

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}
			decl, err := loadDeclaration("label-define")
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"label": label,
			}
			if description != "" {
				argsMap["description"] = description
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}

			if jsonOutput {
				out := map[string]interface{}{
					"label":       label,
					"msg_id":      msg.ID,
					"campfire_id": campfireID,
				}
				if description != "" {
					out["description"] = description
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Printf("defined label %q\n", label)
			return nil
		})
	},
}

var labelListCmd = &cobra.Command{
	Use:   "list",
	Short: "List labels in the campfire registry",
	Long: `List all labels in the per-campfire label registry.

Includes seed atoms (always present) and user-defined atoms.
The defined-by column shows "seed" for built-in labels or a truncated
pubkey for user-defined labels.

Example:
  rd label list
  rd label list --json`,
	RunE: func(cmd *cobra.Command, args []string) error {
		agentID, s, err := requireAgentAndStore()
		if err != nil {
			return err
		}
		defer s.Close()
		_ = agentID

		campfireID, _, hasCampfire := projectRoot()
		var result *state.DeriveResult
		if hasCampfire && campfireID != "" {
			result, err = state.DeriveAllFromStore(s, campfireID)
			if err != nil {
				return fmt.Errorf("deriving label registry: %w", err)
			}
		} else {
			// JSONL-only mode: derive from JSONL file.
			items, jsonlErr := allItemsFromJSONLOrStore(s)
			_ = items
			if jsonlErr != nil {
				return fmt.Errorf("loading items: %w", jsonlErr)
			}
			// Fall through with empty registry (seed atoms only).
			result = state.DeriveAll("", nil)
		}

		registry := result.LabelRegistry()

		// Sort labels for stable output: seeds first (alphabetical), then user-defined (by name).
		type labelEntry struct {
			name      string
			def       state.LabelDef
		}
		var seeds, userDefined []labelEntry
		for name, def := range registry {
			if def.DefinedBy == "seed" {
				seeds = append(seeds, labelEntry{name, def})
			} else {
				userDefined = append(userDefined, labelEntry{name, def})
			}
		}
		sort.Slice(seeds, func(i, j int) bool { return seeds[i].name < seeds[j].name })
		sort.Slice(userDefined, func(i, j int) bool { return userDefined[i].name < userDefined[j].name })
		entries := append(seeds, userDefined...)

		if jsonOutput {
			type jsonLabel struct {
				Name        string `json:"name"`
				Description string `json:"description,omitempty"`
				DefinedBy   string `json:"defined_by"`
				DefinedAt   string `json:"defined_at,omitempty"`
			}
			var out []jsonLabel
			for _, e := range entries {
				jl := jsonLabel{
					Name:        e.def.Name,
					Description: e.def.Description,
					DefinedBy:   e.def.DefinedBy,
				}
				if e.def.DefinedAt != 0 {
					jl.DefinedAt = time.Unix(0, e.def.DefinedAt).UTC().Format(time.RFC3339)
				}
				out = append(out, jl)
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tDESCRIPTION\tDEFINED-BY\tDEFINED-AT")
		for _, e := range entries {
			definedBy := e.def.DefinedBy
			if definedBy != "seed" {
				definedBy = truncateID(definedBy, 12)
			}
			definedAt := ""
			if e.def.DefinedAt != 0 {
				definedAt = time.Unix(0, e.def.DefinedAt).UTC().Format(time.RFC3339)
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.def.Name, e.def.Description, definedBy, definedAt)
		}
		return w.Flush()
	},
}

var labelAddCmd = &cobra.Command{
	Use:   "add <item-id> <label>",
	Short: "Add a label to an existing item",
	Long: `Add a label to an existing work item.

The label must be registered in the campfire label registry (see rd label list).
Any member can add labels to items — only DEFINING new label atoms is grant-gated.

Example:
  rd label add ready-94a bug
  rd label add ready-94a blog-candidate`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		label := args[1]

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}
			decl, err := loadDeclaration("label-add")
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"id":    itemID,
				"label": label,
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}

			if jsonOutput {
				out := map[string]interface{}{
					"item_id":     itemID,
					"label":       label,
					"msg_id":      msg.ID,
					"campfire_id": campfireID,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Printf("label %q added to %s\n", label, itemID)
			return nil
		})
	},
}

var labelRemoveCmd = &cobra.Command{
	Use:   "remove <item-id> <label>",
	Short: "Remove a label from an existing item",
	Long: `Remove a label from an existing work item.

Removing a label that is not present is idempotent — no error is returned.

Example:
  rd label remove ready-94a bug
  rd label remove ready-94a blog-candidate`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		label := args[1]

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}
			decl, err := loadDeclaration("label-remove")
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"id":    itemID,
				"label": label,
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}

			if jsonOutput {
				out := map[string]interface{}{
					"item_id":     itemID,
					"label":       label,
					"msg_id":      msg.ID,
					"campfire_id": campfireID,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Printf("label %q removed from %s\n", label, itemID)
			return nil
		})
	},
}

func init() {
	labelDefineCmd.Flags().String("description", "", "human-readable description of the label")
	labelCmd.AddCommand(labelDefineCmd)
	labelCmd.AddCommand(labelListCmd)
	labelCmd.AddCommand(labelAddCmd)
	labelCmd.AddCommand(labelRemoveCmd)
	rootCmd.AddCommand(labelCmd)
}
