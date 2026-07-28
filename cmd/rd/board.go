package main

// ready-384: `rd board` / `rd board share` print a WORKING URL instead of a bare
// rd1_ token, so sharing a board is one command — not "mint a token, then explain
// to the human how to turn it into a link."
//
//   rd board                 -> URL for YOUR OWN board. NO grant is issued and NO
//                                rd1_ token is minted: your key already holds
//                                owner access, so nothing needs to be conveyed to
//                                anyone. The emitted URL is a PLAIN fragment
//                                carrying only the board coordinate + relay set
//                                (board=...&relays=...) so a hosted board page
//                                knows what to open. Nothing here is a claim —
//                                there is no claim-nonce for `rd grant --claim`
//                                to ever bind a stranger's key to.
//   rd board share <who>     -> resolves <who> (npub1... or 64-hex pubkey), issues
//                                an owner-signed grant via the EXISTING grant path
//                                (same body as `rd grant`), THEN prints the URL —
//                                zero-wait, the grant is already durable on the
//                                relay before the recipient clicks. NO claim-nonce
//                                is minted (ready-5c1): the grantee's key is
//                                already known, and the grant just published IS
//                                the authorization. A live, unbound claim-nonce
//                                in this URL would be a bearer credential anyone
//                                who saw the link could later bind to THEIR OWN
//                                key via `rd grant --claim` — a credential leak.
//   rd board share           -> mints a one-use claim-nonce token (same as
//                                `rd invite`) for someone whose pubkey isn't known
//                                yet, wrapped as a URL. Completed later with:
//                                  rd grant --claim <nonce> <pubkey>
//
// URL SHAPE (bare share form, unknown key): https://<board-host>#rd1_<base64url>
// — a FRAGMENT (never sent to a server, never in an access log). The payload is
// the UNCHANGED rd1_ v3 nostrClaimPayload (cmd/rd/nostr_invite.go
// buildNostrClaimToken / decodeNostrClaimToken) — no new token version, no new
// event kind (constraint).
//
// URL SHAPE (own board, `rd board` with no args, AND `rd board share <who>` for
// a known key): https://<board-host>#board=<coord>&relays=<comma-list> — NO
// rd1_ token, NO claim-nonce. Byte-shape deliberately distinct from the bare
// share link: a claim-nonce is a bearer credential `rd grant --claim` can bind
// to whoever presents it, so a link for an already-authorized recipient must
// never carry one, even an unconsumed/informational one.
//
// SECURITY: by default the link conveys NO secret and NO read access on its own
// for a confidential board. Read access comes ONLY from the owner-signed
// kind-39301 role-grant, which wraps the board CEK to a specific grantee
// (ready-216). A stranger holding a board URL can at most self-mint a read-only
// sync that imports ciphertext it cannot decrypt — authorization is the grant,
// not the link.
//
// ready-df0 — THE ONE EXCEPTION, AND WHY IT IS OPT-IN ONLY.
//
//	rd board --with-key      -> the own-board URL above, PLUS this key's already-
//	                            unwrapped board read key(s) in the fragment:
//	                            pk=<viewer pubkey>&cek=<epoch>:<hex>[,...].
//
// LEAST PRIVILEGE — WHY THE LTK IS NOT IN THAT LIST. An earlier cut of this also
// embedded ltk=<hex>, the board's label-token key. Nothing in the browser reads
// it: web/board's keyring.ltk() and envelope.labelToken() have no caller outside
// their own tests, because labels are filtered client-side on decrypted
// plaintext and the `#l`-filter path that would need a token has not been built.
// So the link was shipping a second secret with no consumer — pure exposure for
// no capability. It is gone from the EMITTED fragment; fragment.ts still PARSES
// ltk= so links minted by older builds keep working. Re-add emission here (and
// re-add it to the fragment allowlist test) at the same time a consumer lands,
// not before.
//
// THE PROBLEM IT SOLVES: a confidential board's CEK is NIP-44-wrapped TO a
// pubkey, so unwrapping it needs that key's SECRET. A read-only npub pasted into
// the hosted board page is a PUBLIC key — it can never unwrap anything, so the
// owner of the work saw a wall of "[encrypted]" and the only sanctioned fix was
// a NIP-07 browser extension. The owner rejected that outright. ready-9f5
// already settled that the board is an independent static page with no rd
// binary and no localhost daemon, so the key can only arrive via the URL, via
// browser storage (which still needs the URL to fill it), or via an extension.
// The URL is what is left.
//
// WHY THE EXPOSURE IS ACCEPTABLE, as a decision and not an accident:
//   - A fragment is NEVER sent to a server: no access log, no Referer header, no
//     CDN cache. It is already the trust model for the rd1_ claim token above,
//     and web/board/src/lib/fragment.ts strips it via history.replaceState in a
//     `finally` immediately after parsing (ready-dbf #6, ready-62d1), so it does
//     not linger in the address bar or in browser history.
//   - A CEK decrypts ONE BOARD'S CONTENT. It is not an identity, it CANNOT sign,
//     and it conveys no write authority — that still comes only from an
//     owner-signed kind-39301 grant. This is a materially weaker exposure than
//     the nsec-in-an-extension alternative it replaces. The nsec never enters
//     the page, and nothing here needs it.
//   - Residual risk, accepted: the link is a bearer credential at rest in
//     terminal scrollback and the clipboard — the same place `rd board share`
//     tokens already live. That is why it is BEHIND A FLAG and why emitting one
//     prints a warning line to stderr: a link carrying a key must always be a
//     deliberate act, never a silent default.
//
// `rd board share` NEVER embeds a key, with or without any flag. Third-party
// access stays grant-based — an owner-signed kind-39301 wrapping the CEK to that
// SPECIFIC grantee, revocable by rotation — which is a strictly stronger
// property than a bearer link, and it is the single boundary this file must not
// blur. There is no --with-key on the share subcommand and
// TestBoardShareCmd_NeverEmbedsKeyMaterial is the rejection test for that.

import (
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// defaultBoardHost is the hosted-board origin used when neither --host nor
// $RD_BOARD_HOST names one. This MUST be a host that actually resolves
// (ready-df6: an earlier placeholder, board.ready.3dl.dev, never resolved and
// shipped as if it were real) — verified by
// TestBoardCmd_DefaultHost_EmitsConfiguredHost (cmd/rd/board_test.go), which
// drives the real cobra RunE and asserts on the literal printed bytes.
const defaultBoardHost = "https://ready.3dl.dev/board"

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
// an access log. Used ONLY by the share forms (`rd board share ...`), which
// carry a real claim-nonce or a live grant reference.
func boardURL(host, token string) string {
	return host + "#" + token
}

// boardKeyFragment is the OPT-IN key material `rd board --with-key` embeds in
// an own-board link (ready-df0). A nil *boardKeyFragment is the default and
// makes ownBoardURL emit exactly the bytes it emitted before ready-df0 — that
// identity is what keeps bare `rd board` key-free, and it is asserted by
// TestBoardCmd_Default_NoKeyMaterial.
//
// `viewer` is a PUBLIC pubkey (the identity the page should open as, so nobody
// has to paste an npub); `ceks` are SECRET.
//
// There is deliberately NO ltk field: the label-token key has no reader in the
// browser, so embedding it would ship a secret nothing can spend (see the
// LEAST PRIVILEGE note in this file's header). Adding one back means adding a
// consumer first.
type boardKeyFragment struct {
	viewer string           // 64-hex pubkey of the key that minted this link
	ceks   map[int][32]byte // epoch -> content-encryption key
}

// carriesSecret reports whether this fragment actually ships key material, as
// opposed to only the public viewer pubkey. It is what decides whether the
// warning line is printed — the user must be told when, and only when, the link
// is a bearer credential.
func (f *boardKeyFragment) carriesSecret() bool {
	return f != nil && len(f.ceks) > 0
}

// cekParam renders the held CEKs as "<epoch>:<64-hex>[,<epoch>:<64-hex>...]",
// ascending by epoch. EVERY held epoch travels, not just the current one: a
// board that has rotated has cards sealed under older epochs, and shipping only
// the newest key would leave those cards showing the placeholder in the browser
// even though this key can read them in the CLI (see BoardKeyring.Epochs).
func (f *boardKeyFragment) cekParam() string {
	epochs := make([]int, 0, len(f.ceks))
	for ep := range f.ceks {
		epochs = append(epochs, ep)
	}
	sort.Ints(epochs)
	parts := make([]string, 0, len(epochs))
	for _, ep := range epochs {
		cek := f.ceks[ep]
		parts = append(parts, strconv.Itoa(ep)+":"+hex.EncodeToString(cek[:]))
	}
	return strings.Join(parts, ",")
}

// ownBoardURL builds the URL for YOUR OWN board: no rd1_ token, no claim-nonce —
// your key already holds owner access, so nothing needs to be conveyed. The
// fragment carries the board coordinate and relay set a hosted board page needs
// to know what to open. Deliberately a DIFFERENT shape than boardURL's rd1_
// fragment so an own-board link can never be mistaken for (or later confused
// with) a claim/share link.
//
// `keys` is nil for every caller except `rd board --with-key` (ready-df0), and
// nil produces byte-for-byte the pre-ready-df0 fragment. When non-nil it appends
// pk= (public) and, for a confidential board this key can read, cek= (secret).
// Those FOUR parameters are the whole emitted vocabulary — see
// TestBoardCmd_WithKey_FragmentParamAllowlist. web/board/src/lib/fragment.ts
// parses one more, ltk=, which this no longer emits (header: LEAST PRIVILEGE);
// it stays parseable so links from older builds keep opening.
func ownBoardURL(host, coord string, relays []string, keys *boardKeyFragment) string {
	v := url.Values{}
	v.Set("board", coord)
	if len(relays) > 0 {
		v.Set("relays", strings.Join(relays, ","))
	}
	if keys != nil {
		if keys.viewer != "" {
			v.Set("pk", keys.viewer)
		}
		if len(keys.ceks) > 0 {
			v.Set("cek", keys.cekParam())
		}
	}
	return host + "#" + v.Encode()
}

// ownBoardKeys derives, from the LOCAL signed log only (no relay round-trip),
// the key material this machine's key holds for `coord`, plus whether the board
// is confidential at all.
//
// It reuses rdSync.DeriveBoardKeyring — the SAME authorization computation the
// read path uses — rather than reaching into any local key cache, so the four
// checks that decide whether a wrapped key becomes a usable CEK (signature,
// owner-signed, p-tag names this reader, the wrap actually opens) all still run.
// A key this machine cannot legitimately derive can therefore never be embedded
// in a link.
//
// `confidential` comes from the keyring's board-global cutover, which is set by
// ANY owner CEK-bearing grant regardless of who it is addressed to. So "the
// board is confidential but I hold no key" is distinguishable from "the board is
// not confidential", and the two get different advice on stderr.
func ownBoardKeys(dir, coord string) (keys *boardKeyFragment, confidential bool, err error) {
	k, err := nostrKey()
	if err != nil {
		return nil, false, err
	}
	owner, boardD, ok := rdSync.ParseBoardCoord(coord)
	if !ok {
		return nil, false, fmt.Errorf("pinned board %q is malformed", coord)
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		return nil, false, fmt.Errorf("rd board --with-key: read local log: %w", err)
	}
	kr := rdSync.DeriveBoardKeyring(events, k, owner, boardD)
	f := &boardKeyFragment{viewer: k.PubKeyHex(), ceks: map[int][32]byte{}}
	for _, ep := range kr.Epochs(coord) {
		if cek, held := kr.CEK(coord, ep); held {
			f.ceks[ep] = cek
		}
	}
	// kr.LTK(coord) is deliberately NOT read. The keyring holds the label-token
	// key — this key is entitled to it — but entitlement is not a reason to put
	// it in a URL. Nothing on the receiving end can spend it (header: LEAST
	// PRIVILEGE), so gathering it here would only widen what the link leaks.
	_, confidential = kr.Cutover(coord)
	return f, confidential, nil
}

// boardKeyWarning is the ONE line printed to stderr whenever an emitted link
// actually carries key material (ready-df0 done condition 4). It exists so a
// user can never paste a key-bearing link into a shared channel believing it is
// inert, which is precisely what the default key-free link IS.
//
// stderr, not stdout, so `rd board --with-key | pbcopy` still copies exactly the
// URL and the human still sees the warning.
const boardKeyWarning = "WARNING: this link CARRIES THIS BOARD'S READ KEY in its fragment — anyone who opens it can read every title on this board. Treat it like a password: do not paste it into chat, a ticket, or any shared channel."

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

  https://<board-host>#board=<coord>&relays=<relay-list>

With NO arguments this is YOUR OWN board: no grant is issued and NO rd1_
token is minted — your key already holds owner access, so nothing needs to
be conveyed to anyone. The URL just tells the hosted board page which board
and relays to open; it carries no claim-nonce for ` + "`rd grant --claim`" + ` to
ever bind a stranger's key to.

  rd board --with-key               ALSO embed this key's board read key in
                                     the fragment, so the page decrypts a
                                     CONFIDENTIAL board with no browser
                                     extension and nothing to paste. The
                                     resulting link is a BEARER CREDENTIAL
                                     for this board's content.
  rd board --portfolio              ONE link for EVERY board this key can
                                     read, not just this directory's board.
  rd board --portfolio --with-key   the same whole-portfolio link, carrying
                                     every one of those boards' read keys.
                                     The resulting link is a BEARER
                                     CREDENTIAL for your ENTIRE PORTFOLIO —
                                     strictly wider than --with-key alone,
                                     which covers one board.
  rd board share <npub-or-pubkey>   issue a grant to a KNOWN key, then print
                                     the URL (zero-wait: the grant is durable
                                     on the relay before they click).
  rd board share                    mint a one-use claim-nonce link for
                                     someone whose key you don't know yet.

WITHOUT --with-key (the default), and for every ` + "`rd board share`" + ` link:

` + boardSecurityNote + `

WITH --with-key, that changes for THIS link and only this link: the fragment
also carries the content-encryption key(s) your key already holds for this
board — those and nothing else — so the hosted page can decrypt titles in
your browser. A fragment is never sent to a server and the page
strips it from the address bar immediately, but the link itself is a bearer
credential while it sits in your scrollback or clipboard — anyone you send it
to can read this board. It carries NO signing key and NO write authority: a
content key cannot sign, and writes still require an owner-signed grant.
Sharing with someone else should still be ` + "`rd board share <npub>`" + `, which
wraps the key to THAT key alone and never puts one in a URL.

--host / $RD_BOARD_HOST overrides the hosted-board origin (default:
` + defaultBoardHost + ` — a real, TLS-serving, DNS-resolving host that
serves the browser board UI: open the printed URL and it renders this board).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, native := nostrNativeProject()
		if !native {
			return fmt.Errorf("rd board requires a nostr-native project (a pinned board) — run: rd link <coord> first")
		}
		coord := nostrPinnedBoard(dir)

		withKey, _ := cmd.Flags().GetBool("with-key")
		// ready-4d9. --portfolio switches the SCOPE of the link from this
		// directory's pinned board to every board this key can read; --with-key
		// still independently decides whether any key material travels. Keeping
		// them orthogonal is what keeps --with-key the ONE flag that can put a
		// secret in a URL, on either scope.
		if portfolio, _ := cmd.Flags().GetBool("portfolio"); portfolio {
			return runBoardPortfolio(cmd, dir, withKey)
		}
		var keys *boardKeyFragment
		var confidential bool
		if withKey {
			k, conf, err := ownBoardKeys(dir, coord)
			if err != nil {
				return err
			}
			keys, confidential = k, conf
		}

		fmt.Println(ownBoardURL(boardHost(cmd), coord, inviteRelaySet(), keys))

		// Said AFTER the URL, on stderr, so piping the URL stays clean while
		// the human still reads what they just minted.
		if withKey {
			errOut := cmd.ErrOrStderr()
			switch {
			case keys.carriesSecret():
				fmt.Fprintln(errOut, boardKeyWarning)
			case confidential:
				fmt.Fprintln(errOut, "NOTE: no key embedded — this board is confidential but your key holds no read key for it; ask the owner to run: rd grant "+keys.viewer)
			default:
				fmt.Fprintln(errOut, "NOTE: no key embedded — this board is not confidential, so there is nothing to decrypt.")
			}
		}
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

		// Print the URL AFTER the grant is durable (zero-wait: the grant is on
		// the relay before the recipient can click the link). NO claim-nonce is
		// minted here (ready-5c1): the grantee's pubkey is already known and the
		// grant just published is the authorization — a live, unbound
		// claim-nonce would be a bearer credential anyone who saw this URL could
		// later bind to THEIR OWN key via `rd grant --claim`, which is exactly
		// the credential-leak this path must not create. Same plain
		// board+relays fragment shape as the own-board URL — no rd1_ token.
		//
		// ready-df0: the nil `keys` argument is the boundary. `rd board share`
		// has no --with-key and must never grow one: the recipient's read access
		// is the kind-39301 grant published just above, which wraps the CEK to
		// THAT pubkey and can be revoked by rotating the epoch. Putting a CEK in
		// this URL would replace a revocable, per-grantee capability with an
		// unrevocable bearer credential — see TestBoardShareCmd_NeverEmbedsKeyMaterial.
		board := nostrPinnedBoard(dir)
		if _, _, ok := rdSync.ParseBoardCoord(board); !ok {
			return fmt.Errorf("pinned board %q is malformed", board)
		}
		fmt.Println(ownBoardURL(host, board, inviteRelaySet(), nil))
		return nil
	},
}

// hostFlagUsage is the --host flag's help text on both boardCmd and
// boardShareCmd, DERIVED from defaultBoardHost so the two copies can never
// drift from the constant (or each other) — the exact bug class ready-df6
// exists to remove structurally rather than police after the fact.
var hostFlagUsage = fmt.Sprintf("hosted-board origin override (default: $RD_BOARD_HOST, else %s)", defaultBoardHost)

func init() {
	boardCmd.Flags().String("host", "", hostFlagUsage)
	// ready-df0. Deliberately OFF by default and deliberately absent from
	// boardShareCmd: a link that carries a key is always an explicit act, and a
	// link for someone else is always a grant.
	boardCmd.Flags().Bool("with-key", false, "embed this key's board read key in the link fragment so a browser can decrypt a confidential board with no extension — the link becomes a bearer credential for this board's content")
	// ready-4d9. Also OFF by default and also absent from boardShareCmd: a
	// whole-portfolio link is a wider act than a single-board one, so it can
	// never be what a bare command prints, and a link for someone else is still
	// always a grant.
	boardCmd.Flags().Bool("portfolio", false, "print ONE link covering EVERY board this key can read, not just this directory's board (with --with-key the link carries every one of those boards' read keys)")

	boardShareCmd.Flags().String("host", "", hostFlagUsage)
	boardShareCmd.Flags().Duration("ttl", 2*time.Hour, "token time-to-live for the emitted link")
	boardShareCmd.Flags().String("role", rdSync.RoleContributor, "role to grant (owner|maintainer|contributor) — only used with a pubkey argument")
	boardShareCmd.Flags().String("label", "", "human label carried in the grant content — only used with a pubkey argument")

	boardCmd.AddCommand(boardShareCmd)
	rootCmd.AddCommand(boardCmd)
}
