package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
  rd label list                                   List the registry`,
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

// labelProposeCmd creates a p3 decision item requesting promotion of a new label
// into the registry. The context includes the demand count from label-demand.jsonl.
var labelProposeCmd = &cobra.Command{
	Use:   "propose <name>",
	Short: "Propose a new label for the registry",
	Long: `Propose a new label for promotion into the campfire label registry.

Creates a p3 decision item titled "Label proposal: <name>" with context
that includes the demand count from .ready/label-demand.jsonl (how many
times this label was attempted). A grant-holder can then approve the
proposal via rd label define.

Example:
  rd label propose incident
  rd label propose hotfix --reason "Used frequently in ops incidents"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		labelName := args[0]
		reason, _ := cmd.Flags().GetString("reason")

		// Count demand for this label from the local demand log.
		demandCount := countLabelDemand(labelName)

		// Build context string.
		contextParts := fmt.Sprintf("Label %q requested for promotion into the campfire registry.", labelName)
		if demandCount > 0 {
			contextParts += fmt.Sprintf(" Demand count: %d attempt(s) recorded in .ready/label-demand.jsonl.", demandCount)
		} else {
			contextParts += " No demand recorded yet (label-demand.jsonl empty or absent)."
		}
		if reason != "" {
			contextParts += " Reason: " + reason
		}
		contextParts += " To approve: rd label define " + labelName

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}
			decl, err := loadDeclaration("create")
			if err != nil {
				return err
			}

			// Generate an ID.
			existingItems, _ := allItemsFromJSONLOrStore(s)
			existingIDs := map[string]struct{}{}
			for _, it := range existingItems {
				existingIDs[it.ID] = struct{}{}
			}
			_, projectDir, hasCampfire := projectRoot()
			prefix := ""
			if hasCampfire {
				prefix = projectPrefix(projectDir)
			} else if dir, ok := readyProjectDir(); ok {
				prefix = projectPrefix(dir)
			}
			id, err := generateID(prefix, existingIDs)
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"id":       id,
				"title":    "Label proposal: " + labelName,
				"type":     "decision",
				"for":      agentID.PublicKeyHex(),
				"priority": "p3",
				"context":  contextParts,
				"labels":   "label-proposal",
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				// If label-proposal label isn't in registry, retry without it.
				delete(argsMap, "labels")
				var err2 error
				msg, campfireID, err2 = executeConventionOp(agentID, s, exec, decl, argsMap)
				if err2 != nil {
					return err // return original error
				}
			}

			if jsonOutput {
				out := map[string]interface{}{
					"id":           id,
					"msg_id":       msg.ID,
					"campfire_id":  campfireID,
					"label":        labelName,
					"demand_count": demandCount,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			_ = msg
			_ = campfireID
			fmt.Printf("proposed label %q → item %s (p3 decision)\n", labelName, id)
			if demandCount > 0 {
				fmt.Printf("  demand count: %d recorded attempt(s)\n", demandCount)
			}
			return nil
		})
	},
}

// countLabelDemand returns the number of demand records for a specific label
// in .ready/label-demand.jsonl. Returns 0 if the file doesn't exist or is unreadable.
func countLabelDemand(label string) int {
	projectDir, ok := readyProjectDir()
	if !ok {
		return 0
	}
	demandFile := filepath.Join(projectDir, ".ready", "label-demand.jsonl")
	f, err := os.Open(demandFile)
	if err != nil {
		return 0
	}
	defer f.Close()

	count := 0
	dec := json.NewDecoder(f)
	for dec.More() {
		var rec struct {
			Label string `json:"label"`
		}
		if err := dec.Decode(&rec); err != nil {
			continue
		}
		if rec.Label == label {
			count++
		}
	}
	return count
}

func init() {
	labelDefineCmd.Flags().String("description", "", "human-readable description of the label")
	labelCmd.AddCommand(labelDefineCmd)
	labelCmd.AddCommand(labelListCmd)
	labelProposeCmd.Flags().String("reason", "", "reason for proposing this label")
	labelCmd.AddCommand(labelProposeCmd)
	rootCmd.AddCommand(labelCmd)
}
