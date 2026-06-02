package main

import (
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"fmt"
	"os"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention-extension/delegation"
	"github.com/spf13/cobra"
)

var killCmd = &cobra.Command{
	Use:   "kill <pubkey>",
	Short: "Revoke a grant-holder's delegation grant",
	Long: `Revoke the cf-authority delegation grant held by <pubkey> by posting an
identity:revoked message at the project campfire. The in-process convention
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

		campfireID, _, ok := projectRoot()
		if !ok {
			return fmt.Errorf("no campfire project found — run 'rd init' first")
		}

		client, err := requireClient()
		if err != nil {
			return err
		}

		childKey, err := hex.DecodeString(pubKeyHex)
		if err != nil || len(childKey) != ed25519.PublicKeySize {
			return fmt.Errorf("pubkey is not a valid 32-byte hex key")
		}
		campfireIDBytes, err := hex.DecodeString(campfireID)
		if err != nil {
			return fmt.Errorf("decoding campfire id: %w", err)
		}

		msg, err := delegation.PostRevoke(context.Background(), client, campfireIDBytes, ed25519.PublicKey(childKey))
		if err != nil {
			return fmt.Errorf("revoking grant: %w", err)
		}

		displayKey := pubKeyHex
		if len(displayKey) > 12 {
			displayKey = displayKey[:12] + "..."
		}
		fmt.Fprintf(os.Stdout, "revoked %s (identity:revoked %s)\n", displayKey, truncateID(msg.ID, 12))
		fmt.Fprintln(os.Stdout, "  the revoked identity's operations are denied within one sync cycle")
		return nil
	},
}

func init() {
	rootCmd.AddCommand(killCmd)
}
