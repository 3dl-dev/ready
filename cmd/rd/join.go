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
// The dead grant is one instance of a general rule, NOT the scope of it: every
// as-typed pubkey ends up byte-compared against, or byte-indexed into, a value
// that came from a signed event and is therefore lowercase. The failures are all
// the same shape — a comparison that silently cannot match, with success
// reported.
//
// THE ENUMERATION BELOW IS DERIVED, NOT ASSERTED. It was produced by grepping
// the two ways an as-typed pubkey can enter the CLI and classifying every hit,
// so it is checkable rather than a claim of completeness:
//
//	grep -rn 'isHex(' cmd/ pkg/ --include='*.go' | grep -v _test.go
//	grep -rn 'GetString("for"\|GetString("by"\|GetString("to")' cmd/rd/*.go
//
// GROUP A — PUBKEY-ONLY tokens. isHex has already established the value can be
// nothing but a pubkey, so these lowercase unconditionally via
// normalizeHexPubkey:
//
//	cmd/rd/board.go resolveGranteePubkey      → grant p/d tags (dead grant)
//	cmd/rd/nostr_grant.go publishRoleGrant    → grant p/d tags (dead grant; also
//	                                            the confidential forward-secrecy
//	                                            guard, see its own comment)
//	cmd/rd/authz_nostr.go runNostrGrantRevoke → rekey exclude + summary line
//	cmd/rd/follow.go resolveFollowTarget      → DiscoverOwnerBoards owner set
//	                                            (finds NO board; blames relays
//	                                            and the trust graph instead)
//	cmd/rd/nostr_grant.go runLinkOrPinBoard   → board coordinate in
//	                                            .ready/config.json AND committed
//	                                            .ready/board.json (dead pin, and
//	                                            it travels to every clone)
//	cmd/rd/ready.go --scope                   → nostrScopeForKey owner/levels
//	                                            lookup (granted key DENIED)
//	cmd/rd/identify.go --add-key              → alias p tags (key locked out of
//	                                            the trust closure it joins)
//
// GROUP B — PARTY tokens (--for / --by / --to). These may be a pubkey, an email,
// an agent id, or a coordinate, so they must NOT be lowercased unconditionally;
// they route through normalizePartyToken, which lowercases ONLY a 64-char hex
// value and returns every other token byte-identical:
//
//	cmd/rd/create.go --for/--by   → the kind-30302 card's "for" tag (--for) and
//	                                its "p" assignee tag (--by, via
//	                                CardSpec.Assignee — the SAME tag class as the
//	                                dead grant above). WORST SHAPE OF ALL: unlike
//	                                the read gates below,
//	                                this value is SIGNED INTO A DISTRIBUTED
//	                                EVENT and travels to every reader forever.
//	                                Every party match is a BYTE comparison —
//	                                idset[item.For] in pkg/views, tagValue
//	                                equality in pkg/sync; `grep -rn EqualFold
//	                                pkg/` is empty, and the three
//	                                strings.EqualFold uses in cmd/ are a URL
//	                                scheme check, the parent-id "none" sentinel
//	                                and an `rd init` prompt answer, none of them
//	                                an identity. So the real holder of the key
//	                                gets "nothing ready" on every machine and rd
//	                                blames the queue.
//	cmd/rd/engage.go --for        → the "for" tag of EVERY card the playbook
//	                                instantiates (same, once per item)
//	cmd/rd/delegate.go --to       → item.By, republished as the card's "p"
//	                                assignee tag (same; nostrwrite.go
//	                                runDelegateNostr sets item.By = to)
//	cmd/rd/ready.go --for         → nostrPartyIdentitySet, then
//	                                idset[item.For] || idset[item.By]
//	cmd/rd/list.go --for          → nostrPartyIdentitySet, then idset[item.For]
//	cmd/rd/list.go --by           → applyListFilters' exact item.By != byFilter
//	cmd/rd/work.go --for          → views.MyWorkFilter → idset[item.By]
//
// DELIBERATELY NOT NORMALIZED. Each has a reason that must be re-checked if it
// changes:
//
//	cmd/rd/kill.go, cmd/rd/revoke.go — VALIDATE only, then hand the string to
//	  runNostrGrantRevoke, which normalizes (covered end-to-end by
//	  TestGrantRevokeKillCmd_UppercaseGrantee_NormalizesAtEachEntryPoint).
//	cmd/rd/sessions.go nostrAuthorityResolver.label — the actor pubkey is read
//	  out of a signed event, never human input, so it is canonical already.
//	pkg/identity/alias.go BuildAliasEvent — a library validator, not an entry
//	  point: its only production caller (cmd/rd/identify.go --add-key) normalizes
//	  first, and a builder must not silently rewrite the spec it was handed.
//	cmd/rd/join.go --force-replace-owner-key — byte-compared against the
//	  coordinate rd itself printed in the refusal message. A mismatch FAILS
//	  CLOSED and re-prints the exact string to paste, so there is no silent wrong
//	  answer; normalizing would widen an identity guard instead of fixing one.
//
// One helper, so there is a single definition of "canonical form" rather than N
// independent strings.ToLower calls that could drift apart — and a single
// symbol to grep for when auditing whether a new entry point normalized.
func normalizeHexPubkey(s string) string {
	return strings.ToLower(s)
}

// normalizePartyToken canonicalizes an as-typed PARTY token — the value of
// --for / --by / --to — and is the group-B half of normalizeHexPubkey's rule
// above.
//
// A party token is polymorphic by design: `rd create --for baron@3dl.dev`,
// `--for atlas/worker-3`, `--for cf://agents/implementer` and `--for <64-hex>`
// are all legal, and item.For defaults to the signer's own pubkey hex. So the
// normalization is GUARDED: lowercase only when the token is exactly a 64-char
// hex string (the sole shape that can be a pubkey), and return everything else
// byte-identical. Lowercasing unconditionally would silently rewrite an email's
// or an agent id's case and make `--for Baron@3DL.dev` no longer match the
// party it was stored under — trading this defect for its mirror image.
func normalizePartyToken(s string) string {
	if len(s) == 64 && isHex(s) {
		return normalizeHexPubkey(s)
	}
	return s
}

func init() {
	joinCmd.Flags().Bool("force", false, "overwrite existing identity when joining via invite token (REFUSED if that key owns a board — see --force-replace-owner-key)")
	joinCmd.Flags().String("force-replace-owner-key", "", "board coordinate (30301:owner:d) of the board this machine's key owns — REQUIRED to replace an owner key; plain --force will not. The old key is backed up first.")
	rootCmd.AddCommand(joinCmd)
}
