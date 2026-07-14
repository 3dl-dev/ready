package main

import (
	"fmt"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

var revokeCmd = &cobra.Command{
	Use:   "revoke <pubkey>",
	Short: "Revoke a member's role in the project",
	Long: `Revoke a member's role by posting a work:role-grant with role="revoked".

The target must be identified by their 64-character hex public key.
Name-based revocation is not supported because name resolution produces
project IDs, not member pubkeys — using one in place of the other is a
semantic type error (ready-34d).

By default the revocation is PROSPECTIVE (effective now): the key's PAST
authoritative events stay honored (completed items do not reopen). Pass --from <unix>
for a retroactive repudiation from T (the compromise case). --label carries a human
label into the grant content.

EXAMPLES
  rd revoke abcdef1234...
  rd revoke abcdef1234... --from 1699999999`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		target := args[0]
		retroactive, _ := cmd.Flags().GetBool("retroactive")
		from, _ := cmd.Flags().GetInt64("from")
		label, _ := cmd.Flags().GetString("label")

		// Validate target is a 64-char hex pubkey.
		// Name-based resolution is intentionally not supported here because
		// resolveName() resolves names to campfire IDs — a different 64-char hex
		// type — not to member pubkeys. Using a campfire ID as a pubkey in a
		// role-grant message is a semantic type confusion (ready-34d).
		// Use the member's full 64-character hex public key instead.
		if len(target) != 64 || !isHex(target) {
			return fmt.Errorf("revoke target %q is not a valid pubkey: must be a 64-character hex string\n  hint: use the member's public key, not a name or campfire ID", target)
		}
		pubKeyHex := target

		// NOSTR-NATIVE default path (ready-477): revocation = publish an owner-signed
		// kind-39301 role=revoked grant + regenerate the relay write-allowlist. This
		// runs BEFORE any projectRoot()/requireClient() so the cf-authority delegation
		// path is never invoked and no .cf is provisioned. Prospective by default;
		// retroactive (compromise) repudiation is 'rd revoke --from <unix>' (ready-f58:
		// the --from/--label capability migrated here from the deleted `rd nostr revoke`).
		if dir, native := nostrNativeProject(); native {
			if retroactive {
				return fmt.Errorf("--retroactive is campfire-only; on a nostr-native project use 'rd revoke %s --from <unix>' for retroactive (compromise) repudiation", pubKeyHex)
			}
			return runNostrGrantRevoke(dir, pubKeyHex, rdSync.RoleRevoked, label, from, "")
		}

		// nostr-native only (ready-cb6): the campfire-backed revocation path has been
		// removed. A directory with no pinned nostr board is not a valid rd project.
		return errNotNostrProject()
	},
}

func init() {
	revokeCmd.Flags().Bool("retroactive", false, "legacy transitive revocation (not supported on the nostr-native path; use --from)")
	revokeCmd.Flags().Int64("from", 0, "retroactive repudiation from this unix time (0 = prospective / effective now)")
	revokeCmd.Flags().String("label", "", "human label carried in the grant content")
	rootCmd.AddCommand(revokeCmd)
}
