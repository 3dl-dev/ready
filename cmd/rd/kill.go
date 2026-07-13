package main

import (
	"fmt"

	rdSync "github.com/campfire-net/ready/pkg/sync"
	"github.com/spf13/cobra"
)

var killCmd = &cobra.Command{
	Use:   "kill <pubkey>",
	Short: "Revoke a grant-holder's role (nostr-native alias for 'rd revoke')",
	Long: `Revoke <pubkey>'s trust by publishing an owner-signed kind-39301 grant with
role="revoked" on the pinned board, then regenerating the relay write-allowlist.
The revocation is prospective (effective now): the key's past authoritative
events stay honored (completed items do not reopen).

This is a shorthand for 'rd revoke <pubkey>'. Use 'rd nostr revoke <pubkey> --from
<unix>' for retroactive (compromise) repudiation.

EXAMPLE
  rd kill abcdef0123...   # revoke this identity's grant`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		pubKeyHex := args[0]
		if len(pubKeyHex) != 64 || !isHex(pubKeyHex) {
			return fmt.Errorf("invalid pubkey %q: must be a 64-character hex string", pubKeyHex)
		}

		// NOSTR-NATIVE default path (ready-477): actor-key revocation = publish an
		// owner-signed kind-39301 role=revoked grant + regenerate the relay
		// write-allowlist (the same primitive as 'rd revoke' under the unified model:
		// there is no un-grant, only revoke).
		if dir, native := nostrNativeProject(); native {
			return runNostrGrantRevoke(dir, pubKeyHex, rdSync.RoleRevoked, "", 0)
		}

		// nostr-native only (ready-cb6): a directory with no pinned nostr board is
		// not a valid rd project.
		return errNotNostrProject()
	},
}

func init() {
	rootCmd.AddCommand(killCmd)
}
