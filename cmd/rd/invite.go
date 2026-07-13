package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var inviteCmd = &cobra.Command{
	Use:   "invite",
	Short: "Generate a one-use invite token for this project",
	Long: `Generate a one-use invite token that lets a worker join this project
with a single 'rd join <token>' command.

The token MINTS a fresh secp256k1 contributor identity, publishes an owner-signed
contributor grant for it, and bundles the board coordinate, relay set, TTL, a
one-use nonce, and the minted secret into the rd1_ token. The joiner imports the
key, pins the board, adopts the relays, and syncs — no separate key exchange.

SECURITY
  The token contains a private key — treat it as a secret.
  Use --ttl to limit the exposure window (default 2h).
  The token is single-use: the first redeemer publishes a signed consumed marker.

EXAMPLES
  rd invite                  # default: 2h TTL, contributor role
  rd invite --ttl 30m        # shorter TTL`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, _ := cmd.Flags().GetDuration("ttl")

		// The rd1_ nostr mint-and-ship token is the SOLE invite path (ready-a49):
		// a fresh secp256k1 key, an owner-signed contributor grant, and the board
		// coord + relays + TTL + one-use nonce + secret bundled in the token.
		token, err := runNostrInvite(ttl)
		if err != nil {
			return err
		}
		fmt.Println(token)
		return nil
	},
}

func init() {
	inviteCmd.Flags().Duration("ttl", 2*time.Hour, "token time-to-live")
	rootCmd.AddCommand(inviteCmd)
}
