package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var inviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Mint a one-use claim token that lets a worker join this project",
	Long: `Mint a one-use CLAIM token for this project (the self-mint invite model).

The token carries ONLY the board coordinate, the relay set, a TTL, and a one-use
claim-nonce. It ships NO secret key and NO grant. The joiner runs 'rd join <token>',
which SELF-MINTS its own key, syncs the board READ-ONLY, and prints its pubkey plus
the claim-nonce. You then grant write access:

  rd grant <joiner-pubkey> contributor --claim <claim-nonce>

Single-use is REAL and owner-enforced: a claim-nonce binds to exactly one pubkey, so
a leaked token yields only a TTL-bounded request the owner may deny — never a key.

SECURITY
  The token contains NO private key. A leak is a TTL-bounded claim, not a compromise.
  Use --ttl to limit the window (default 2h).

EXAMPLES
  rd invite                  # default: 2h TTL
  rd invite --ttl 30m        # shorter TTL`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, _ := cmd.Flags().GetDuration("ttl")

		// The rd1_ v3 claim token is the SOLE invite path (ready-ce0): a one-use
		// claim-nonce + board coord + relays + TTL. No key, no grant at mint time.
		token, err := runNostrInvite(ttl)
		if err != nil {
			return err
		}
		fmt.Println(token)
		fmt.Fprintln(cmd.ErrOrStderr(), "\nShare this token with the joiner. They run `rd join <token>` (self-mints, read-only)")
		fmt.Fprintln(cmd.ErrOrStderr(), "and send back a pubkey + claim-nonce; grant write with:")
		fmt.Fprintln(cmd.ErrOrStderr(), "  rd grant <pubkey> contributor --claim <claim-nonce>")
		fmt.Fprintln(cmd.ErrOrStderr(), "On locked relays, then run `rd relay sync-allowlist --apply`. Token is TTL-bounded.")
		return nil
	},
}

func init() {
	inviteCmd.Flags().Duration("ttl", 2*time.Hour, "token time-to-live")
	rootCmd.AddCommand(inviteCmd)
}
