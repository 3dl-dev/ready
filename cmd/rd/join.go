package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var joinCmd = &cobra.Command{
	Use:   "join <rd1_token>",
	Short: "Join a project via an invite token",
	Long: `Join a project via a one-use invite (claim) token from the project owner.

'rd join rd1_...' mints a fresh identity for THIS machine, pins the board, adopts
the project's relays, and syncs the project's items READ-ONLY — 'rd ready' works
immediately. It writes nothing to the relays. It then prints your pubkey and the
claim-nonce; send those to the owner, who grants write access with
'rd grant <pubkey> contributor --claim <claim-nonce>'. Re-joining the same token on
this machine needs --force.

Joining one of YOUR OWN other machines, not a teammate's project? Skip the token —
run 'rd follow <you@email>' instead: it keeps this machine's existing identity and
pulls in every board you own, with no coordinate to copy.

EXAMPLES
  rd join rd1_...                     # join a teammate's project via invite token`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("invite token required (rd join rd1_...)")
		}

		nameOrID := args[0]
		force, _ := cmd.Flags().GetBool("force")
		ownerKeyForce, _ := cmd.Flags().GetString("force-replace-owner-key")

		// A nostr mint-and-ship token (rd1_ prefix) is the SOLE join path: it
		// imports the minted secp256k1 key, pins the board, adopts relays, and
		// syncs (ready-a49).
		if strings.HasPrefix(nameOrID, nostrInviteTokenPrefix) {
			return joinViaNostrInviteToken(nameOrID, force, ownerKeyForce)
		}

		return fmt.Errorf("only invite tokens (rd1_...) are supported — get one from the project owner (they run 'rd invite'), or if you're adding one of your own machines, run 'rd follow <you@email>' instead")
	},
}

// isHex returns true if s consists entirely of hex characters. Shared by the
// nostr grant/revoke/sessions/audit paths.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func init() {
	joinCmd.Flags().Bool("force", false, "overwrite existing identity when joining via invite token (REFUSED if that key owns a board — see --force-replace-owner-key)")
	joinCmd.Flags().String("force-replace-owner-key", "", "board coordinate (30301:owner:d) of the board this machine's key owns — REQUIRED to replace an owner key; plain --force will not. The old key is backed up first.")
	rootCmd.AddCommand(joinCmd)
}
