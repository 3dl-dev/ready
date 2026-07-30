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
// nostr grant/revoke/sessions/audit paths. Deliberately case-insensitive (A-F
// as well as a-f) — it is a FORMAT check, not an identity check.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// normalizeHexPubkey lowercases a hex pubkey already validated by isHex.
//
// ready-3e1: isHex's case-insensitivity above is correct for FORMAT
// validation, but a pubkey's real identity is nostr.Key.PubKeyHex(), which is
// ALWAYS lowercase — that lowercase string is what a signed grant's p/d tags
// carry, and what DeriveLevels/InviteGrantValid/MayGrant index and compare
// against. A grantee accepted as uppercase or mixed-case hex and carried
// forward unnormalized publishes a grant whose p tag can never equal the
// grantee's actual event pubkey: InviteGrantValid returns false for the real
// key while the command reports success — a silently dead grant.
//
// The dead grant is one instance of a general rule, NOT the scope of it: EVERY
// entry point that accepts an as-typed hex pubkey normalizes through this
// helper, because every one of them ends up comparing that string against a
// pubkey that came from a signed event and is therefore lowercase. The failures
// are all the same shape — a byte comparison that silently cannot match, with
// success reported:
//
//	cmd/rd/board.go resolveGranteePubkey     → grant p/d tags (dead grant)
//	cmd/rd/nostr_grant.go publishRoleGrant   → grant p/d tags (dead grant; also
//	                                           the confidential forward-secrecy
//	                                           guard, see its own comment)
//	cmd/rd/authz_nostr.go runNostrGrantRevoke → rekey exclude + summary line
//	cmd/rd/follow.go resolveFollowTarget     → DiscoverOwnerBoards owner set
//	                                           (finds NO board; blames relays
//	                                           and the trust graph instead)
//	cmd/rd/nostr_grant.go runLinkOrPinBoard  → board coordinate in
//	                                           .ready/config.json AND committed
//	                                           .ready/board.json (dead pin, and
//	                                           it travels to every clone)
//	cmd/rd/ready.go --scope                  → nostrScopeForKey owner/levels
//	                                           lookup (granted key DENIED)
//	cmd/rd/identify.go --add-key             → alias p tags (key locked out of
//	                                           the trust closure it joins)
//
// The remaining isHex call sites deliberately do NOT normalize, and each has a
// reason that must be re-checked if it changes: cmd/rd/kill.go and
// cmd/rd/revoke.go only VALIDATE before handing the string to
// runNostrGrantRevoke, which normalizes (covered end-to-end by
// TestGrantRevokeKillCmd_UppercaseGrantee_NormalizesAtEachEntryPoint);
// cmd/rd/sessions.go's nostrAuthorityResolver.label takes an actor pubkey read
// out of a signed event, never human input, so it is canonical already.
//
// One helper, so there is a single definition of "canonical form" rather than N
// independent strings.ToLower calls that could drift apart — and a single
// symbol to grep for when auditing whether a new entry point normalized.
func normalizeHexPubkey(s string) string {
	return strings.ToLower(s)
}

func init() {
	joinCmd.Flags().Bool("force", false, "overwrite existing identity when joining via invite token (REFUSED if that key owns a board — see --force-replace-owner-key)")
	joinCmd.Flags().String("force-replace-owner-key", "", "board coordinate (30301:owner:d) of the board this machine's key owns — REQUIRED to replace an owner key; plain --force will not. The old key is backed up first.")
	rootCmd.AddCommand(joinCmd)
}
