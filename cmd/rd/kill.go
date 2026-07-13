package main

import (
	"fmt"

	rdSync "github.com/campfire-net/ready/pkg/sync"
	"github.com/spf13/cobra"
)

var killCmd = &cobra.Command{
	Use:   "kill <pubkey>",
	Short: "Revoke a grant-holder's delegation grant",
	Long: `Revoke the cf-authority delegation grant held by <pubkey> by posting an
identity:revoked message at the project. The in-process convention
server's gate denies the revoked key's operations within one sync cycle.

Use 'rd sessions' to list active grant-holders. This is the cf-authority
counterpart to the legacy 'rd revoke' (which posts work:role-grant role=revoked);
both are honored during the authority-model migration.

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
		// there is no un-grant, only revoke). Runs BEFORE any projectRoot()/
		// requireClient(), so the cf-authority delegation path is never invoked and no
		// .cf is provisioned.
		if dir, native := nostrNativeProject(); native {
			return runNostrGrantRevoke(dir, pubKeyHex, rdSync.RoleRevoked, "", 0)
		}

		// nostr-native only (ready-cb6): the campfire cf-authority delegation-revoke
		// path has been removed. A directory with no pinned nostr board is not a valid
		// rd project.
		return errNotNostrProject()
	},
}

func init() {
	rootCmd.AddCommand(killCmd)
}
