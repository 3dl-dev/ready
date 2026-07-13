package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

// engageCmd implements rd engage <playbook-id> [--project <p>] [--for <id>] [--var key=value].
var engageCmd = &cobra.Command{
	Use:   "engage <playbook-id>",
	Short: "Instantiate a playbook into work items",
	Long: `Instantiate a playbook template into concrete work items.

The template is read store-free from .ready/playbooks.jsonl and each item is
published store-free through the secp256k1 nostr-native write path, with
dependency edges preserved between the created items.

  1. Finds the playbook by ID in .ready/playbooks.jsonl
  2. Generates unique item IDs (<project>-<random-hex>)
  3. Applies {{variable}} substitutions to titles and contexts
  4. Publishes each item as a nostr card, preserving dep edges

--project defaults to the project prefix; --for defaults to the secp256k1 signer.

Example:
  rd engage sre-incident --var env=prod
  rd engage sre-incident --project myapp --for baron@3dl.dev --var project=myapp`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		playbookID := args[0]
		project, _ := cmd.Flags().GetString("project")
		forParty, _ := cmd.Flags().GetString("for")
		varFlags, _ := cmd.Flags().GetStringArray("var")

		// Parse --var key=value flags.
		variables := make(map[string]string, len(varFlags))
		for _, v := range varFlags {
			parts := strings.SplitN(v, "=", 2)
			if len(parts) != 2 {
				return fmt.Errorf("invalid --var %q: must be key=value", v)
			}
			variables[parts[0]] = parts[1]
		}

		// nostr-native only: engage instantiates a playbook template into concrete
		// nostr items via the secp256k1 write path. No campfire, no .cf, no store.
		dir, native := nostrNativeProject()
		if !native {
			return errNotNostrProject()
		}

		// --project defaults to the project prefix (the ID-generation namespace the
		// nostr create path uses); --for defaults to the secp256k1 signer.
		if project == "" {
			project = projectPrefix(dir)
			if project == "" {
				return fmt.Errorf("--project is required (could not derive a prefix from the project directory)")
			}
		}
		if forParty == "" {
			self, err := nostrSelfPubkey()
			if err != nil {
				return err
			}
			forParty = self
		}

		return runEngageNostr(dir, playbookID, project, forParty, variables)
	},
}

func init() {
	engageCmd.Flags().String("project", "", "project prefix for generated item IDs (default: project directory prefix)")
	engageCmd.Flags().String("for", "", "who needs these outcomes (default: secp256k1 signer)")
	engageCmd.Flags().StringArray("var", nil, "variable substitution: key=value (may be repeated)")
	rootCmd.AddCommand(engageCmd)
}
