package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/3dl-dev/ready/pkg/playbook"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// playbooksStore returns the store-free playbook template store for a project
// directory: <dir>/.ready/playbooks.jsonl. No campfire store, no .cf identity.
func playbooksStore(dir string) *playbook.Store {
	return playbook.NewStore(filepath.Join(dir, rdSync.ReadyDir, playbook.PlaybooksFile))
}

// requireNostrPlaybookStore resolves the current project's playbook store, erroring
// unless the project is nostr-native (the only supported path after the cutover).
func requireNostrPlaybookStore() (*playbook.Store, error) {
	dir, native := nostrNativeProject()
	if !native {
		return nil, errNotNostrProject()
	}
	return playbooksStore(dir), nil
}

// playbookCmd is the parent command for playbook subcommands.
var playbookCmd = &cobra.Command{
	Use:   "playbook",
	Short: "Manage playbook templates",
	Long: `Manage reusable playbook templates stored store-free in .ready/playbooks.jsonl.

  rd playbook create <title> --id <id> --items-file <path>  register a new playbook
  rd playbook list                                           list registered playbooks
  rd playbook show <id>                                      show playbook details`,
}

// playbookCreateCmd implements rd playbook create <title> --id <id> --items-file <path>.
var playbookCreateCmd = &cobra.Command{
	Use:   "create <title>",
	Short: "Register a new playbook template",
	Long: `Register a playbook template by reading item definitions from a JSON file.

The template is appended store-free to .ready/playbooks.jsonl.

The items file must be a JSON array of template items, each with:
  title     - item title (may contain {{variable}} placeholders)
  type      - one of task, decision, review, reminder, deadline, prep, message, directive
  priority  - one of p0, p1, p2, p3
  level     - (optional) epic, task, subtask
  context   - (optional) description text (may contain {{variable}} placeholders)
  labels    - (optional) label atoms to attach at engage time
  deps      - (optional) 0-based indices of items that must complete first

Example:
  rd playbook create "SRE Incident" --id sre-incident --items-file items.json`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		title := args[0]
		id, _ := cmd.Flags().GetString("id")
		description, _ := cmd.Flags().GetString("description")
		itemsFile, _ := cmd.Flags().GetString("items-file")

		if id == "" {
			return fmt.Errorf("--id is required")
		}
		if itemsFile == "" {
			return fmt.Errorf("--items-file is required")
		}

		itemsJSON, err := os.ReadFile(itemsFile)
		if err != nil {
			return fmt.Errorf("reading items file: %w", err)
		}

		tmpl, err := playbook.Parse(id, title, description, itemsJSON)
		if err != nil {
			return fmt.Errorf("invalid playbook: %w", err)
		}

		store, err := requireNostrPlaybookStore()
		if err != nil {
			return err
		}
		if err := store.Add(tmpl); err != nil {
			return fmt.Errorf("registering playbook: %w", err)
		}

		if jsonOutput {
			out := map[string]interface{}{
				"id":         tmpl.ID,
				"title":      tmpl.Title,
				"item_count": len(tmpl.Items),
			}
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}
		fmt.Printf("playbook %s registered (%d items)\n", tmpl.ID, len(tmpl.Items))
		return nil
	},
}

// playbookListCmd implements rd playbook list.
var playbookListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered playbooks",
	RunE: func(cmd *cobra.Command, args []string) error {
		store, err := requireNostrPlaybookStore()
		if err != nil {
			return err
		}
		playbooks, err := store.List()
		if err != nil {
			return err
		}

		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(playbooks)
		}

		if len(playbooks) == 0 {
			fmt.Println("no playbooks registered")
			return nil
		}

		for _, pb := range playbooks {
			desc := pb.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Printf("  %-24s  %-5d items  %s\n", pb.ID, len(pb.Items), truncate(desc, 48))
		}
		return nil
	},
}

// playbookShowCmd implements rd playbook show <id>.
var playbookShowCmd = &cobra.Command{
	Use:   "show <id>",
	Short: "Show a playbook template with item tree preview",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		playbookID := args[0]

		store, err := requireNostrPlaybookStore()
		if err != nil {
			return err
		}
		pb, err := store.Find(playbookID)
		if err != nil {
			return err
		}

		if jsonOutput {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			return enc.Encode(pb)
		}

		fmt.Printf("ID:          %s\n", pb.ID)
		fmt.Printf("Title:       %s\n", pb.Title)
		if pb.Description != "" {
			fmt.Printf("Description: %s\n", pb.Description)
		}
		fmt.Printf("Items:       %d\n", len(pb.Items))
		fmt.Println()
		fmt.Println("Item tree:")
		printPlaybookTree(pb.Items)
		return nil
	},
}

// printPlaybookTree prints the template items with their dep edges for playbook show.
func printPlaybookTree(items []playbook.TemplateItem) {
	for i, item := range items {
		depStr := ""
		if len(item.Deps) > 0 {
			depIDs := make([]string, len(item.Deps))
			for j, d := range item.Deps {
				depIDs[j] = fmt.Sprintf("[%d]", d)
			}
			depStr = fmt.Sprintf("  (after: %s)", strings.Join(depIDs, ", "))
		}
		typeStr := item.Type
		if item.Level != "" {
			typeStr = item.Level + "/" + item.Type
		}
		fmt.Printf("  [%d] %-8s  %-6s  %s%s\n", i, item.Priority, typeStr, item.Title, depStr)
	}
}

func init() {
	playbookCreateCmd.Flags().String("id", "", "playbook ID (required, e.g. sre-incident)")
	playbookCreateCmd.Flags().String("description", "", "playbook description")
	playbookCreateCmd.Flags().String("items-file", "", "path to JSON file containing template items (required)")

	playbookCmd.AddCommand(playbookCreateCmd)
	playbookCmd.AddCommand(playbookListCmd)
	playbookCmd.AddCommand(playbookShowCmd)
	rootCmd.AddCommand(playbookCmd)
}
