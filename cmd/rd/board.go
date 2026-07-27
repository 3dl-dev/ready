package main

// ready-384: `rd board` / `rd board share` print a WORKING URL instead of a bare
// rd1_ token, so sharing a board is one command — not "mint a token, then explain
// to the human how to turn it into a link."
//
//   rd board                 -> URL for YOUR OWN board. No grant is issued — your
//                                key already holds owner access, so nothing needs
//                                to be conveyed to anyone. The emitted URL still
//                                wraps an rd1_ v3 token (board+relays) so a hosted
//                                board page knows what to open; decodeNostrClaimToken
//                                still validates it (thin wrapper over `rd invite`).
//   rd board share <who>     -> resolves <who> (npub1... or 64-hex pubkey), issues
//                                an owner-signed grant via the EXISTING grant path
//                                (same body as `rd grant`), THEN prints the URL —
//                                zero-wait, the grant is already durable on the
//                                relay before the recipient clicks.
//   rd board share           -> mints a one-use claim-nonce token (same as
//                                `rd invite`) for someone whose pubkey isn't known
//                                yet, wrapped as a URL. Completed later with:
//                                  rd grant --claim <nonce> <pubkey>
//
// URL SHAPE: https://<board-host>/#rd1_<base64url> — a FRAGMENT (never sent to a
// server, never in an access log). The payload is the UNCHANGED rd1_ v3
// nostrClaimPayload (cmd/rd/nostr_invite.go buildNostrClaimToken /
// decodeNostrClaimToken) — no new token version, no new event kind (constraint).
//
// SECURITY: the link conveys NO secret and NO read access on its own for a
// confidential board. Read access comes ONLY from the owner-signed kind-39301
// role-grant, which wraps the board CEK to a specific grantee (ready-216). A
// stranger holding a board URL can at most self-mint a read-only sync that
// imports ciphertext it cannot decrypt — authorization is the grant, not the link.

import (
	"fmt"
	"os"
	"strings"
	"time"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// defaultBoardHost is the placeholder hosted-board origin used when neither
// --host nor $RD_BOARD_HOST names one. The board host does not serve TLS yet
// (ready-1ab: wss:// + a browser-served page still need to ship) — this is a
// NAME, not a live endpoint today. rd board still prints a well-formed,
// decodable URL now so this command and its tests don't block on that
// infrastructure landing.
const defaultBoardHost = "https://board.ready.3dl.dev"

// boardHost resolves the hosted-board ORIGIN a token URL is minted against:
// --host flag > $RD_BOARD_HOST env > defaultBoardHost. This is the ONLY place
// the host is resolved (constraint: "board host URL must be configurable, not
// hardcoded"). A trailing slash is trimmed so boardURL never doubles one up.
func boardHost(cmd *cobra.Command) string {
	host := defaultBoardHost
	if h := os.Getenv("RD_BOARD_HOST"); h != "" {
		host = h
	}
	if h, _ := cmd.Flags().GetString("host"); h != "" {
		host = h
	}
	return strings.TrimSuffix(host, "/")
}

// boardURL wraps an rd1_ token as the fragment URL a hosted board page opens.
// A fragment (not a query string) is never sent to a server and never lands in
// an access log.
func boardURL(host, token string) string {
	return host + "/#" + token
}

// resolveGranteePubkey accepts either an npub1... (NIP-19 bech32) or a bare
// 64-hex pubkey — the same two forms `rd grant`/`rd follow` accept — and
// returns the 64-hex pubkey. decodeNpub is shared with cmd/rd/follow.go.
func resolveGranteePubkey(who string) (string, error) {
	if strings.HasPrefix(who, "npub1") {
		pub, err := decodeNpub(who)
		if err != nil {
			return "", err
		}
		return pub, nil
	}
	if len(who) == 64 && isHex(who) {
		return who, nil
	}
	return "", fmt.Errorf("rd board share: %q is not an npub1... or a 64-hex pubkey", who)
}

const boardSecurityNote = `SECURITY: this link conveys NO read access on its own for a CONFIDENTIAL
board. The board content is encrypted; only an owner-signed role-grant
(kind-39301) wraps the board key to a specific grantee. A stranger holding
this URL can at most self-mint a read-only sync that imports ciphertext it
cannot decrypt — authorization is the grant, not the link.`

var boardCmd = &cobra.Command{
	Use:   "board",
	Short: "Print a working URL for this project's board",
	Long: `Print a shareable URL for this repo's pinned board:

  https://<board-host>/#rd1_<token>

With NO arguments this is YOUR OWN board: no grant is issued — your key
already holds owner access, so nothing needs to be conveyed to anyone. The
URL just tells the hosted board page which board and relays to open.

  rd board share <npub-or-pubkey>   issue a grant to a KNOWN key, then print
                                     the URL (zero-wait: the grant is durable
                                     on the relay before they click).
  rd board share                    mint a one-use claim-nonce link for
                                     someone whose key you don't know yet.

` + boardSecurityNote + `

--host / $RD_BOARD_HOST overrides the hosted-board origin (default: a
placeholder — the board host does not serve TLS yet, see ready-1ab).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, _ := cmd.Flags().GetDuration("ttl")
		token, err := runNostrInvite(ttl)
		if err != nil {
			return err
		}
		fmt.Println(boardURL(boardHost(cmd), token))
		return nil
	},
}

var boardShareCmd = &cobra.Command{
	Use:   "share [npub-or-pubkey]",
	Short: "Grant + print a board URL (known key), or mint a claim-nonce link (unknown key)",
	Long: `With a pubkey (npub1... or 64-hex), issue an owner-signed grant to that key
via the same path as 'rd grant' (default role contributor), THEN print the
board URL. Zero-wait: the grant is durable on the relay before the recipient
clicks the link.

With NO argument, mint a one-use claim-nonce token (same as 'rd invite') and
print it as a URL for someone whose pubkey you don't know yet. They open the
link (or run 'rd join <token>' with the raw token) and self-mint a read-only
identity, then send you back a pubkey; complete the invite with:

  rd grant --claim <nonce-from-token> <pubkey>

` + boardSecurityNote,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, _ := cmd.Flags().GetDuration("ttl")
		host := boardHost(cmd)

		if len(args) == 0 {
			token, err := runNostrInvite(ttl)
			if err != nil {
				return err
			}
			fmt.Println(boardURL(host, token))
			fmt.Fprintln(cmd.ErrOrStderr(), "\nShare this link. When the recipient's pubkey comes back to you, grant write with:")
			fmt.Fprintln(cmd.ErrOrStderr(), "  rd grant --claim <nonce-from-token> <pubkey>")
			return nil
		}

		grantee, err := resolveGranteePubkey(args[0])
		if err != nil {
			return err
		}
		role, _ := cmd.Flags().GetString("role")
		switch role {
		case rdSync.RoleOwner, rdSync.RoleMaintainer, rdSync.RoleContributor:
		default:
			return fmt.Errorf("invalid --role %q: choose owner|maintainer|contributor", role)
		}
		label, _ := cmd.Flags().GetString("label")
		dir, native := nostrNativeProject()
		if !native {
			return fmt.Errorf("rd board share operates on a nostr-native project (kind-39301 role-grants) — run: rd link <coord> first")
		}

		// Issue the grant via the EXISTING grant path (same body as `rd grant`) —
		// this is the durable authorization act. No --claim: the grantee's
		// pubkey is already known, so there is no self-mint claim to bind.
		if err := runNostrGrantRevoke(dir, grantee, role, label, 0, ""); err != nil {
			return err
		}

		// Mint the URL's token AFTER the grant is durable (zero-wait: the grant
		// is on the relay before the recipient can click the link). The token
		// carries the same board+relays as any other board link — no secret,
		// and the claim-nonce it carries is informational (this grant did not
		// consume it; the grant itself, not the link, conferred access).
		board := nostrPinnedBoard(dir)
		owner, _, ok := rdSync.ParseBoardCoord(board)
		if !ok {
			return fmt.Errorf("pinned board %q is malformed", board)
		}
		claim, err := randomNonce()
		if err != nil {
			return err
		}
		now := time.Now()
		token, err := buildNostrClaimToken(board, inviteRelaySet(), claim, now.Unix(), now.Add(ttl).Unix(), owner)
		if err != nil {
			return err
		}
		fmt.Println(boardURL(host, token))
		return nil
	},
}

func init() {
	boardCmd.Flags().String("host", "", "hosted-board origin override (default: $RD_BOARD_HOST, else a placeholder — ready-1ab has not shipped TLS yet)")
	boardCmd.Flags().Duration("ttl", 2*time.Hour, "token time-to-live for the emitted link")

	boardShareCmd.Flags().String("host", "", "hosted-board origin override (default: $RD_BOARD_HOST, else a placeholder — ready-1ab has not shipped TLS yet)")
	boardShareCmd.Flags().Duration("ttl", 2*time.Hour, "token time-to-live for the emitted link")
	boardShareCmd.Flags().String("role", rdSync.RoleContributor, "role to grant (owner|maintainer|contributor) — only used with a pubkey argument")
	boardShareCmd.Flags().String("label", "", "human label carried in the grant content — only used with a pubkey argument")

	boardCmd.AddCommand(boardShareCmd)
	rootCmd.AddCommand(boardCmd)
}
