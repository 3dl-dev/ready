package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var delegateCmd = &cobra.Command{
	Use:   "delegate <item-id>",
	Short: "Delegate a work item to another party",
	Long: `Delegate a work item — assign or reassign the performer.

The --to flag is required and specifies the delegatee identity.
Identity types:
  - Person:           baron@3dl.dev
  - Claude agent:     claude-session-xyz
  - Open agent:       cf://agents/implementer
  - Rudi automaton:   atlas/worker-3

Example:
  rd delegate ready-a1b --to baron@3dl.dev
  rd delegate ready-a1b --to atlas/worker-3 --reason "Routing to automaton"`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		itemID := args[0]
		// ready-3e1: --to is a PARTY token that runDelegateNostr assigns to item.By
		// and signs into the card's by tag — signed and distributed, same shape as
		// `rd create --by`. Normalized at the entry point via the guarded
		// normalizePartyToken (the flag's own help text lists emails, agent ids and
		// cf:// URIs alongside pubkeys, which is exactly why the guard is needed).
		toRaw, _ := cmd.Flags().GetString("to")
		to := normalizePartyToken(toRaw)
		reason, _ := cmd.Flags().GetString("reason")

		if to == "" {
			return fmt.Errorf("--to is required")
		}

		// nostr-native write path (ready-cb6): no .cf, secp256k1 signer. Only path.
		if _, native := nostrNativeProject(); native {
			return runDelegateNostr(itemID, to, reason)
		}
		return errNotNostrProject()
	},
}

func init() {
	delegateCmd.Flags().String("to", "", "identity to delegate to (required)")
	delegateCmd.Flags().String("reason", "", "reason for delegation")
	rootCmd.AddCommand(delegateCmd)
}
