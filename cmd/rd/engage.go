package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/ready/pkg/playbook"
	"github.com/campfire-net/ready/pkg/state"
	"github.com/spf13/cobra"
)

// engageCmd implements rd engage <playbook-id> --project <project> --for <identity> --var key=value.
var engageCmd = &cobra.Command{
	Use:   "engage <playbook-id>",
	Short: "Instantiate a playbook into work items",
	Long: `Instantiate a playbook template into concrete work items.

The engage command:
  1. Finds the playbook by ID
  2. Generates unique item IDs (<project>-<random-3-chars>)
  3. Applies variable substitutions to titles and contexts
  4. Creates work items (work:create for each)
  5. Wires dependencies (work:block for each dep edge)
  6. Records the engagement (work:engage)

Example:
  rd engage sre-incident --project myapp --for baron@3dl.dev --var project=myapp --var env=prod`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		playbookID := args[0]
		project, _ := cmd.Flags().GetString("project")
		forParty, _ := cmd.Flags().GetString("for")
		varFlags, _ := cmd.Flags().GetStringArray("var")

		if forParty == "" {
			return fmt.Errorf("--for is required")
		}
		if project == "" {
			return fmt.Errorf("--project is required")
		}

		// Parse --var key=value flags.
		variables := make(map[string]string, len(varFlags))
		for _, v := range varFlags {
			parts := strings.SplitN(v, "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid --var %q: must be key=value", v)
			}
			variables[parts[0]] = parts[1]
		}

		// nostr-native default write path (ready-6ef): engage instantiates a
		// PLAYBOOK template into concrete work items. The TEMPLATE is campfire-vestigial
		// storage (scanPlaybooks reads the campfire store; templates are NOT projected
		// to nostr — I5/ready-9ac deletes the playbook surface), but the ITEMS it
		// creates are first-class nostr items. Per the spec's NOSTR-PUBLISHER GAP
		// resolution: source the template from a lazily-opened campfire store — via
		// openStore(), which reads the store WITHOUT identity.Load and therefore never
		// provisions .cf/identity.json — then publish each expanded item + its dep
		// edges through the secp256k1 nostr-native path (publishItemFullCreateNostr).
		// No requireExecutor / withAgentAndStore, so the no-.cf-on-the-default-path
		// invariant holds (asserted by assertNoDotCf in the engage native test).
		if dir, native := nostrNativeProject(); native {
			return runEngageNostr(dir, playbookID, project, forParty, variables)
		}

		return withAgentAndStore(func(agentID *identity.Identity, s store.Store) error {
			exec, _, err := requireExecutor()
			if err != nil {
				return err
			}

			// Find the playbook.
			pb, err := findPlaybook(s, playbookID)
			if err != nil {
				return err
			}

			// Expand the template.
			items, err := playbook.Expand(pb.PlaybookTemplate, project, variables)
			if err != nil {
				return fmt.Errorf("expanding playbook: %w", err)
			}

			// Load the label registry for UX-level warning on absent labels.
			// Derive is the enforcement gate; this is best-effort UX only.
			var labelRegistry map[string]state.LabelDef
			if cfID, _, ok := projectRoot(); ok && cfID != "" {
				if dr, drErr := state.DeriveAllFromStore(s, cfID); drErr == nil {
					labelRegistry = dr.LabelRegistry()
				}
			}

			createDecl, err := loadDeclaration("create")
			if err != nil {
				return err
			}

			// Maps template index → msg ID of the work:create message sent.
			createMsgIDs := make(map[int]string, len(items))

			// Send work:create for each item using the executor.
			for _, item := range items {
				argsMap := map[string]any{
					"id":       item.ID,
					"title":    item.Title,
					"type":     item.Type,
					"for":      forParty,
					"priority": item.Priority,
					"project":  project,
				}
				if item.Context != "" {
					argsMap["context"] = item.Context
				}
				if item.Level != "" {
					argsMap["level"] = item.Level
				}
				if len(item.Labels) > 0 {
					// Warn about labels not present in the target campfire registry.
					// Derive will drop them — this surface makes it visible at engage time.
					if labelRegistry != nil {
						for _, atom := range item.Labels {
							if _, inReg := labelRegistry[atom]; !inReg {
								fmt.Fprintf(os.Stderr, "warning: label %q on item %s is not in the target campfire registry; it will be dropped at derive time\n", atom, item.ID)
							}
						}
					}
					argsMap["labels"] = strings.Join(item.Labels, ",")
				}

				msg, _, err := executeConventionOp(agentID, s, exec, createDecl, argsMap)
				if err != nil {
					return fmt.Errorf("sending work:create for %s: %w", item.ID, err)
				}
				createMsgIDs[item.TemplateIndex] = msg.ID
			}

			blockDecl, err := loadDeclaration("block")
			if err != nil {
				return err
			}

			// Send work:block for each dependency edge using the executor.
			for _, item := range items {
				for _, depID := range item.Deps {
					// Find the dep item to get its msg ID.
					var depItem *playbook.ExpandedItem
					for _, other := range items {
						if other.ID == depID {
							depItem = other
							break
						}
					}
					if depItem == nil {
						return fmt.Errorf("internal: dep item %q not found", depID)
					}

					// item is blocked by depItem (depItem must complete first).
					argsMap := map[string]any{
						"blocker_id":  depItem.ID,
						"blocked_id":  item.ID,
						"blocker_msg": createMsgIDs[depItem.TemplateIndex],
						"blocked_msg": createMsgIDs[item.TemplateIndex],
					}
					_, _, err = executeConventionOp(agentID, s, exec, blockDecl, argsMap)
					if err != nil {
						return fmt.Errorf("sending work:block for %s→%s: %w", depItem.ID, item.ID, err)
					}
				}
			}

			// Collect all created item IDs.
			createdIDs := make([]string, len(items))
			for i, item := range items {
				createdIDs[i] = item.ID
			}

			engageDecl, err := loadDeclaration("engage")
			if err != nil {
				return err
			}

			// Send the work:engage message using the executor.
			engageArgs := map[string]any{
				"playbook_id": playbookID,
				"project":     project,
				"for":         forParty,
			}
			if len(variables) > 0 {
				engageArgs["variables"] = variables
			}

			engageMsg, campfireID, err := executeConventionOp(agentID, s, exec, engageDecl, engageArgs)
			if err != nil {
				return fmt.Errorf("sending work:engage: %w", err)
			}

			if jsonOutput {
				out := map[string]interface{}{
					"playbook_id": playbookID,
					"project":     project,
					"for":         forParty,
					"created_ids": createdIDs,
					"engage_msg":  engageMsg.ID,
					"campfire_id": campfireID,
				}
				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			// Human-readable output: print the created item tree.
			fmt.Printf("engaged playbook %s → %d items\n\n", playbookID, len(items))
			for _, item := range items {
				depStr := ""
				if len(item.Deps) > 0 {
					depStr = fmt.Sprintf("  (blocked by: %s)", strings.Join(item.Deps, ", "))
				}
				fmt.Printf("  %-16s  %-6s  %s%s\n", item.ID, item.Priority, item.Title, depStr)
			}
			return nil
		})
	},
}

func init() {
	engageCmd.Flags().String("project", "", "project prefix for generated item IDs (required)")
	engageCmd.Flags().String("for", "", "who needs these outcomes (required)")
	engageCmd.Flags().StringArray("var", nil, "variable substitution: key=value (may be repeated)")
	rootCmd.AddCommand(engageCmd)
}
