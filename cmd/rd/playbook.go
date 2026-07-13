package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/ready/pkg/playbook"
	"github.com/spf13/cobra"
)

// playbookCreatePayload is the JSON payload for a work:playbook-create message.
type playbookCreatePayload struct {
	ID          string          `json:"id"`
	Title       string          `json:"title"`
	Description string          `json:"description,omitempty"`
	Items       json.RawMessage `json:"items"`
}

// playbookCmd is the parent command for playbook subcommands.
var playbookCmd = &cobra.Command{
	Use:   "playbook",
	Short: "Manage playbook templates",
	Long: `Manage reusable playbook templates.

  rd playbook create <title> --items-file <path>  register a new playbook
  rd playbook list                                 list registered playbooks
  rd playbook show <id>                            show playbook details`,
}

// playbookCreateCmd implements rd playbook create <title> --items-file <path>.
var playbookCreateCmd = &cobra.Command{
	Use:   "create <title>",
	Short: "Register a new playbook template",
	Long: `Register a playbook template by reading item definitions from a JSON file.

The items file must be a JSON array of template items, each with:
  title     - item title (may contain {{variable}} placeholders)
  type      - one of task, decision, review, reminder, deadline, prep, message, directive
  priority  - one of p0, p1, p2, p3
  level     - (optional) epic, task, subtask
  context   - (optional) description text (may contain {{variable}} placeholders)
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

		// Parse and validate the template.
		tmpl, err := playbook.Parse(id, title, description, itemsJSON)
		if err != nil {
			return fmt.Errorf("invalid playbook: %w", err)
		}

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			// Re-marshal the items as raw JSON for the payload.
			itemsRaw, err := tmpl.ItemsJSON()
			if err != nil {
				return fmt.Errorf("encoding items: %w", err)
			}

			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}
			decl, err := loadDeclaration("playbook-create")
			if err != nil {
				return err
			}

			argsMap := map[string]any{
				"id":    tmpl.ID,
				"title": tmpl.Title,
				"items": json.RawMessage(itemsRaw),
			}
			if tmpl.Description != "" {
				argsMap["description"] = tmpl.Description
			}

			msg, campfireID, err := executeConventionOp(agentID, s, exec, decl, argsMap)
			if err != nil {
				return err
			}

			if jsonOutput {
				out := map[string]interface{}{
					"id":          tmpl.ID,
					"title":       tmpl.Title,
					"item_count":  len(tmpl.Items),
					"msg_id":      msg.ID,
					"campfire_id": campfireID,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			fmt.Printf("playbook %s registered (%d items, msg: %s)\n", tmpl.ID, len(tmpl.Items), msg.ID)
			return nil
		})
	},
}

// playbookListCmd implements rd playbook list.
var playbookListCmd = &cobra.Command{
	Use:   "list",
	Short: "List registered playbooks",
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		playbooks, err := scanPlaybooks(s)
		if err != nil {
			return err
		}

		// Sort by ID for stable output.
		sort.Slice(playbooks, func(i, j int) bool {
			return playbooks[i].ID < playbooks[j].ID
		})

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
			itemCount := len(pb.Items)
			desc := pb.Description
			if desc == "" {
				desc = "(no description)"
			}
			fmt.Printf("  %-24s  %-5d items  %s\n", pb.ID, itemCount, truncate(desc, 48))
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

		s, err := openStore()
		if err != nil {
			return err
		}
		defer s.Close()

		pb, err := findPlaybook(s, playbookID)
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
		printPlaybookTree(pb.Items, "", map[int]bool{})
		return nil
	},
}

// playbookRecord is used for scanning playbook-create messages.
type playbookRecord struct {
	*playbook.PlaybookTemplate
	MsgID      string `json:"msg_id"`
	CampfireID string `json:"campfire_id"`
}

// scanPlaybooks scans all campfires for work:playbook-create messages.
// If a playbook ID appears multiple times, the most recent registration wins.
func scanPlaybooks(s store.Store) ([]*playbookRecord, error) {
	memberships, err := s.ListMemberships()
	if err != nil {
		return nil, fmt.Errorf("listing memberships: %w", err)
	}

	// Track most recent registration per playbook ID.
	type entry struct {
		record *playbookRecord
		ts     int64
	}
	byID := map[string]entry{}

	for _, m := range memberships {
		msgs, err := s.ListMessages(m.CampfireID, 0, store.MessageFilter{})
		if err != nil {
			continue
		}
		for _, msg := range msgs {
			if !hasTagStr(msg.Tags, "work:playbook-create") {
				continue
			}
			var p playbookCreatePayload
			if err := json.Unmarshal(msg.Payload, &p); err != nil {
				continue
			}
			// Parse the full template.
			tmpl, err := playbook.Parse(p.ID, p.Title, p.Description, []byte(p.Items))
			if err != nil {
				continue
			}
			rec := &playbookRecord{
				PlaybookTemplate: tmpl,
				MsgID:            msg.ID,
				CampfireID:       m.CampfireID,
			}
			if prev, ok := byID[p.ID]; !ok || msg.Timestamp > prev.ts {
				byID[p.ID] = entry{rec, msg.Timestamp}
			}
		}
	}

	result := make([]*playbookRecord, 0, len(byID))
	for _, e := range byID {
		result = append(result, e.record)
	}
	return result, nil
}

// findPlaybook finds a registered playbook by ID.
func findPlaybook(s store.Store, id string) (*playbookRecord, error) {
	playbooks, err := scanPlaybooks(s)
	if err != nil {
		return nil, err
	}
	for _, pb := range playbooks {
		if pb.ID == id {
			return pb, nil
		}
	}
	return nil, fmt.Errorf("playbook %q not found", id)
}

// printPlaybookTree prints an item tree for playbook show.
func printPlaybookTree(items []playbook.TemplateItem, prefix string, visited map[int]bool) {
	// Find root items (no deps or no one depends on them from above).
	// Show all items with dep-child relationships indicated.
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
