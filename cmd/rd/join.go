package main

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

var joinCmd = &cobra.Command{
	Use:   "join <rd1_token>",
	Short: "Join a project via an invite token",
	Long: `Join a project via a one-use invite token.

'rd join rd1_...' imports the minted secp256k1 identity, pins the board,
adopts the project's relays, and syncs the project's items. An invite token is
the only join path.

EXAMPLES
  rd join rd1_...                     # join via a one-use invite token`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return fmt.Errorf("invite token required (rd join rd1_...)")
		}

		nameOrID := args[0]
		force, _ := cmd.Flags().GetBool("force")

		// A nostr mint-and-ship token (rd1_ prefix) is the SOLE join path: it
		// imports the minted secp256k1 key, pins the board, adopts relays, and
		// syncs (ready-a49).
		if strings.HasPrefix(nameOrID, nostrInviteTokenPrefix) {
			return joinViaNostrInviteToken(nameOrID, force)
		}

		return fmt.Errorf("only invite tokens (rd1_...) are supported — campfire open-join (by name or ID) was retired with the campfire backend")
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
	joinCmd.Flags().Bool("force", false, "overwrite existing identity when joining via invite token")
	rootCmd.AddCommand(joinCmd)
}
