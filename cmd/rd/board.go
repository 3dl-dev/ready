package main

// ready-384: `rd board` / `rd board share` print a WORKING URL instead of a bare
// rd1_ token, so sharing a board is one command — not "mint a token, then explain
// to the human how to turn it into a link."
//
// ready-1df — WHAT `rd board` DOES BY DEFAULT, AND WHY THAT CHANGED.
//
// Opening your own board used to be `rd board --portfolio --with-key
// --allow-partial`. Each of those three flags was added by a DIFFERENT agent
// solving a DIFFERENT security concern, and every one was individually
// defensible: ready-df0 (a link carrying key material is a bearer credential, so
// do not mint one silently), ready-4d9 (a portfolio link's blast radius is every
// board, so make the wider scope explicit), ready-4d9 follow-up (do not mint a
// link that claims a completeness the gather could not establish). Nobody looked
// at the COMPOSED surface, and three defensible defaults stacked into a
// four-token command for the single most common action a user takes.
//
// THE BOUNDARY THAT ACTUALLY MATTERS IS NOT FLAG-VS-NO-FLAG. It is FOR ME vs FOR
// SOMEONE ELSE:
//
//	`rd board`        FOR ME. My key, my boards, a link to my own clipboard. The
//	                  key is already on this disk and the link goes to this
//	                  terminal, so the flags were guarding a door that was
//	                  already open. It just works: whole portfolio, decrypted,
//	                  one command, zero flags.
//	`rd board share`  FOR SOMEONE ELSE. It must NEVER carry key material, under
//	                  any flag combination or argv ordering. A third party's read
//	                  access is an owner-signed kind-39301 grant wrapped to THAT
//	                  key — revocable by rotation, scoped to one grantee, which
//	                  is strictly stronger than a bearer link. THAT BOUNDARY DOES
//	                  NOT MOVE, and it is enforced STRUCTURALLY by the hardcoded
//	                  nil `keys` argument in boardShareCmd.RunE, not by the
//	                  absence of a flag.
//
// WHAT KEEPS MINTING-BY-DEFAULT SAFE IS NOT A FLAG, IT IS THE WARNING. Every
// key-bearing link still prints a bearer-credential warning on stderr, and stdout
// stays exactly the URL so `rd board | pbcopy` still copies a clean link. Removing
// the warning is the change that would make this default unsafe; removing the
// flags was not.
//
// The three opt-ins became escape hatches, all of them on the FOR-ME side:
//
//	rd board                 -> whole portfolio, decrypted AND WRITABLE:
//	                            #sk=<secret>&relays=<list>&keys=<base64url blob>
//	rd board --no-key        -> the same portfolio link carrying NO key material.
//	rd board --this-board    -> only THIS directory's pinned board:
//	                            #board=<coord>&relays=<list>[&pk=&cek=]
//	rd board --strict        -> refuse to mint unless completeness was fully
//	                            confirmed (see the gate in board_portfolio.go).
//	rd board --allow-partial -> mint even though a read relay never ANSWERED.
//	                            Named in the refusal that suggests it; not a
//	                            decision anyone has to make up front.
//	rd board share <who>     -> resolves <who> (npub1... or 64-hex pubkey), issues
//	                            an owner-signed grant via the EXISTING grant path
//	                            (same body as `rd grant`), THEN prints the URL —
//	                            zero-wait, the grant is already durable on the
//	                            relay before the recipient clicks. NO claim-nonce
//	                            is minted (ready-5c1): the grantee's key is
//	                            already known, and the grant just published IS
//	                            the authorization. A live, unbound claim-nonce
//	                            in this URL would be a bearer credential anyone
//	                            who saw the link could later bind to THEIR OWN
//	                            key via `rd grant --claim` — a credential leak.
//	rd board share           -> mints a one-use claim-nonce token (same as
//	                            `rd invite`) for someone whose pubkey isn't known
//	                            yet, wrapped as a URL. Completed later with:
//	                              rd grant --claim <nonce> <pubkey>
//
// URL SHAPE (bare share form, unknown key): https://<board-host>#rd1_<base64url>
// — a FRAGMENT (never sent to a server, never in an access log). The payload is
// the UNCHANGED rd1_ v3 nostrClaimPayload (cmd/rd/nostr_invite.go
// buildNostrClaimToken / decodeNostrClaimToken) — no new token version, no new
// event kind (constraint).
//
// URL SHAPE (single board: `rd board --this-board`, AND `rd board share <who>`
// for a known key): https://<board-host>#board=<coord>&relays=<comma-list> — NO
// rd1_ token, NO claim-nonce. Byte-shape deliberately distinct from the bare
// share link: a claim-nonce is a bearer credential `rd grant --claim` can bind
// to whoever presents it, so a link for an already-authorized recipient must
// never carry one, even an unconsumed/informational one.
//
// SECURITY: a link with no key material conveys NO secret and NO read access on
// its own for a confidential board. Read access comes ONLY from the owner-signed
// kind-39301 role-grant, which wraps the board CEK to a specific grantee
// (ready-216). A stranger holding a key-free board URL can at most self-mint a
// read-only sync that imports ciphertext it cannot decrypt — authorization is the
// grant, not the link.
//
// ready-df0 — THE KEY MATERIAL, AND WHY IT IS NOW THE DEFAULT FOR YOUR OWN LINK.
//
//	rd board --this-board    -> the own-board URL above, PLUS this key's already-
//	                            unwrapped board read key(s) in the fragment:
//	                            pk=<viewer pubkey>&cek=<epoch>:<hex>[,...].
//	rd board                 -> the same, for every board this key can read, as
//	                            the keys= blob (cmd/rd/board_portfolio.go).
//	--no-key                 -> neither.
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
// THE PROBLEM THE EMBEDDED KEY SOLVES: a confidential board's CEK is
// NIP-44-wrapped TO a pubkey, so unwrapping it needs that key's SECRET. A
// read-only npub pasted into the hosted board page is a PUBLIC key — it can never
// unwrap anything, so the owner of the work saw a wall of "[encrypted]" and the
// only sanctioned fix was a NIP-07 browser extension. The owner rejected that
// outright. ready-9f5 already settled that the board is an independent static
// page with no rd binary and no localhost daemon, so the key can only arrive via
// the URL, via browser storage (which still needs the URL to fill it), or via an
// extension. The URL is what is left.
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
//     tokens already live. That is why emitting one prints a warning line to
//     stderr on EVERY key-bearing link (ready-1df keeps that; it is what the
//     removed flags were really buying).
//
// `rd board share` NEVER embeds a key, with or without any flag. Third-party
// access stays grant-based — an owner-signed kind-39301 wrapping the CEK to that
// SPECIFIC grantee, revocable by rotation — which is a strictly stronger
// property than a bearer link, and it is the single boundary this file must not
// blur. There is no key-bearing flag on the share subcommand,
// TestBoardShareCmd_NeverEmbedsKeyMaterial is the rejection test for that, and
// TestBoardShareCmd_NoArgvEmitsKeyMaterial re-attacks it through the REAL cobra
// parser with a whole argv battery.

import (
	"encoding/hex"
	"fmt"
	"io"
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

// browserOpenableRelays narrows a relay set to the URLs a BROWSER on `host`'s
// origin is actually permitted to open, and reports the ones it left out.
//
// ready-634 — THE DEFECT. `rd board` emitted
//
//	relays=ws://192.168.2.40:7777,ws://192.168.2.41:7777,wss://relay.3dl.network
//
// while the page it links to is served over https. A browser on a SECURE origin
// BLOCKS an insecure WebSocket as mixed content: no click-through, no user
// override, no console opt-in. Two of the three relays in every printed link were
// therefore unopenable by the only client the link exists for, and the board
// worked at all only because the third entry happened to be wss://. The owner
// watched his browser fail on the first two.
//
// THE DECISION, AND WHY IT IS (a) AND NOT (b) OR (c). ready-634 put three options
// on the table: (a) filter to wss:// when building a browser URL, (b) drop RFC1918
// addresses regardless of scheme, (c) add a per-relay "browser-reachable" flag in
// rd.json next to the existing NIP-65 Read/Write flags. This is (a), in the
// sharpened form below — browser-openability is DERIVED from the board host's
// scheme, never declared:
//
//   - IT IS THE ACTUAL RULE. Mixed-content blocking is a property of the BROWSER
//     and of THE PAGE'S ORIGIN. It is not a property of the relay, of anyone's
//     network, or of any deployment. A derived predicate is therefore correct by
//     construction and can never go stale.
//   - (c) WOULD MAKE A BROWSER INVARIANT INTO A HUMAN-MAINTAINED FIELD, which is
//     a new way to be wrong in both directions: an operator who forgets to set it
//     ships exactly the link this item is about, and one who sets it on a ws://
//     relay ships a link the browser still blocks. rd.json would be asserting
//     something rd.json cannot know. Read/Write are genuine policy (which relays
//     do I choose to use, and how); "can a browser open this" is not policy.
//   - (b) IS NEITHER NECESSARY NOR SUFFICIENT. A PUBLIC ws:// relay is blocked
//     just the same, so (b) misses it. A wss:// relay on a LAN address opens
//     perfectly well from the owner's own browser on that LAN, so (b) would drop
//     a relay that works. RFC1918 is a reachability heuristic; mixed content is a
//     hard rule, and only the hard rule belongs in a filter whose claim is "a
//     browser can open this".
//
// WHY THE HOST'S SCHEME AND NOT A BARE wss:// TEST. `RD_BOARD_HOST=http://localhost:5173
// rd board` is web/board's own dev loop, and an http:// page may open ws:// freely
// — there is no mixed content to block. Keying on the host makes this filter
// exactly as strict as the browser is and no stricter, so local development
// against a LAN relay keeps working while the shipped https link stays clean.
// Anything that is not explicitly http:// is treated as secure, so an
// unparseable or scheme-less host fails CLOSED (filtered), never open.
//
// WHAT THIS IS NOT: it is not a change to the relay set. rd.json still carries the
// ws:// LAN relays, and the CLI's own sync still reads and writes through every
// one of them — they are fast and local, which is why they exist (ready-634
// constraint: do not "fix" this by deleting them from rd.json). This narrowing is
// applied at the browser LINK and nowhere else.
func browserOpenableRelays(host string, relays []string) (kept, dropped []string) {
	secure := true
	if u, err := url.Parse(host); err == nil && strings.EqualFold(u.Scheme, "http") {
		secure = false
	}
	for _, r := range relays {
		if !secure || strings.HasPrefix(strings.ToLower(r), "wss://") {
			kept = append(kept, r)
			continue
		}
		dropped = append(dropped, r)
	}
	return kept, dropped
}

// boardLinkRelays is the relay list the `relays=` PARAMETER of a board=/pk=
// fragment is built from: the CLI's configured relay set narrowed by
// browserOpenableRelays, with a note on stderr naming anything left out.
//
// SCOPE — READ THIS BEFORE ADDING A CALLER. This function's output is only ever
// correct for the two fragment shapes web/board actually reads a relay list out
// of: `#board=<coord>&relays=…` and `#pk=<hex>&relays=…`. Those are the two
// branches in main.ts's afterLogin that evaluate `fragment.relays`. Every other
// thing rd prints is NOT one of them, and narrowing it is a live defect rather
// than a harmless extra safety margin:
//
//   - The rd1_ CLAIM TOKEN printed by `rd board share` (bare) and by `rd invite`
//     is redeemed by `rd join` — A CLI. main.ts's afterLogin returns at
//     `fragment.kind === "claim"` (renderAwaitingAuthorization) BEFORE any
//     fetchEvents, and `payload.relays` has no reader anywhere in web/board/src.
//     Filtering it does not protect a browser from anything; it strands the CLI
//     that does read it. See runNostrInvite's header for what that cost.
//
// The stderr note exists so the emitted link never quietly differs from the
// configured set — a user reading `relays=` should not have to work out which of
// his relays silently did not make it. It goes to stderr, never stdout, so
// `rd board | pbcopy` still copies exactly the URL (ready-1df).
//
// When the filter empties the list, a board=/pk= link simply carries no relays=
// at all, and for THOSE TWO SHAPES that is not a dead end: web/board falls back
// to its own same-origin relays.json (main.ts:
// `fragment.relays.length > 0 ? fragment.relays : await deps.loadRelays()`).
// That fallback is a property of the board=/pk= branches and of nothing else —
// it does NOT rescue an rd1_ token, whose consumer is `rd join` and which has no
// same-origin anything to fall back to. An empty relay list in a token is a dead
// token, which is precisely why no token is built from this function.
func boardLinkRelays(errOut io.Writer, host string) []string {
	kept, dropped := browserOpenableRelays(host, inviteRelaySet())
	if len(dropped) > 0 {
		fmt.Fprintf(errOut, "NOTE: %d relay(s) are not in this link because a browser on %s cannot open them — an https page may not open a ws:// socket (mixed content), and there is no click-through: %s. The CLI itself still syncs through them.\n",
			len(dropped), host, strings.Join(dropped, ", "))
	}
	return kept
}

// boardURL wraps an rd1_ token as the fragment URL a hosted board page opens.
// A fragment (not a query string) is never sent to a server and never lands in
// an access log. Used ONLY by the share forms (`rd board share ...`), which
// carry a real claim-nonce or a live grant reference.
func boardURL(host, token string) string {
	return host + "#" + token
}

// boardKeyFragment is the key material a single-board link (`rd board
// --this-board`) embeds (ready-df0). A nil *boardKeyFragment is what `--no-key`
// produces and makes ownBoardURL emit exactly the bytes it emitted before
// ready-df0 — that identity is what keeps a key-free link key-free, and it is
// asserted by TestBoardCmd_NoKey_NoKeyMaterial.
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

// ownBoardURL builds the URL for ONE board of your own: no rd1_ token, no
// claim-nonce — your key already holds owner access, so nothing needs to be
// conveyed. The fragment carries the board coordinate and relay set a hosted
// board page needs to know what to open. Deliberately a DIFFERENT shape than
// boardURL's rd1_ fragment so an own-board link can never be mistaken for (or
// later confused with) a claim/share link.
//
// `keys` is nil for `rd board share` (always, structurally) and for `rd board
// --this-board --no-key`, and nil produces byte-for-byte the pre-ready-df0
// fragment. When non-nil it appends pk= (public) and, for a confidential board
// this key can read, cek= (secret). Those FOUR parameters are the whole emitted
// vocabulary — see TestBoardCmd_ThisBoard_FragmentParamAllowlist.
// web/board/src/lib/fragment.ts parses one more, ltk=, which this no longer
// emits (header: LEAST PRIVILEGE); it stays parseable so links from older builds
// keep opening.
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
		return nil, false, fmt.Errorf("rd board --this-board: read local log: %w", err)
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

// boardKeyWarning is the ONE line printed to stderr whenever an emitted
// single-board link actually carries key material (ready-df0 done condition 4).
// It exists so a user can never paste a key-bearing link into a shared channel
// believing it is inert, which is precisely what a --no-key link IS.
//
// ready-1df made key-bearing the DEFAULT for a link to your own board, which
// makes this line load-bearing rather than decorative: it is now the only thing
// standing between "the command just works" and "the command silently hands you
// a credential". It is not optional and has no flag.
//
// stderr, not stdout, so `rd board --this-board | pbcopy` still copies exactly
// the URL and the human still sees the warning.
const boardKeyWarning = "WARNING: this link CARRIES THIS BOARD'S READ KEY in its fragment — anyone who opens it can read every title on this board. Treat it like a password: do not paste it into chat, a ticket, or any shared channel."

// resolveGranteePubkey accepts either an npub1... (NIP-19 bech32) or a bare
// 64-hex pubkey — the same two forms `rd grant`/`rd follow` accept — and
// returns the 64-hex pubkey. decodeNpub is shared with cmd/rd/follow.go.
//
// ready-3e1: the bare-hex branch normalizes to lowercase. isHex accepts A-F as
// well as a-f (case-insensitive format check), but the grantee's REAL identity
// is nostr.Key.PubKeyHex(), which is always lowercase — that lowercase string
// is what lands in the grant's signed p/d tags and what DeriveLevels/
// InviteGrantValid index on. decodeNpub already returns lowercase
// (hex.EncodeToString), so only the bare-hex branch needs normalizing.
//
// This site's normalization is currently REDUNDANT with publishRoleGrant's
// (cmd/rd/nostr_grant.go), the true funnel every grant-issuing path publishes
// through: this function's only caller (boardShareCmd) passes its return value
// into runNostrGrantRevoke, which normalizes again before publishRoleGrant
// normalizes a third time — so an unnormalized return here does NOT by itself
// produce a dead grant today (verified: reverting only this line leaves the
// existing uppercase regression test green). It stays here as defense in
// depth — a single, direct, unit-testable guarantee at this API's boundary
// that does not depend on which caller happens to be downstream — not as the
// sole thing standing between an uppercase grantee and a dead grant.
func resolveGranteePubkey(who string) (string, error) {
	if strings.HasPrefix(who, "npub1") {
		pub, err := decodeNpub(who)
		if err != nil {
			return "", err
		}
		return pub, nil
	}
	if len(who) == 64 && isHex(who) {
		return normalizeHexPubkey(who), nil
	}
	return "", fmt.Errorf("rd board share: %q is not an npub1... or a 64-hex pubkey", who)
}

const boardSecurityNote = `SECURITY: a link with no key material conveys NO read access on its own for a
CONFIDENTIAL board. The board content is encrypted; only an owner-signed
role-grant (kind-39301) wraps the board key to a specific grantee. A stranger
holding such a URL can at most self-mint a read-only sync that imports
ciphertext it cannot decrypt — authorization is the grant, not the link.`

var boardCmd = &cobra.Command{
	Use:   "board",
	Short: "Print a link that opens your boards in a browser",
	Long: `Print a link to your own work, ready to paste into a browser.

  rd board

That is the whole command. It covers EVERY board your key can read — not just
this directory's — and it is WRITE-CAPABLE: it carries your signing key so the
browser signs your changes with no extension, and the read keys so confidential
boards open decrypted, nothing to paste:

  https://<board-host>#sk=<your-secret>&relays=<relay-list>&keys=<your-read-keys>

Because it carries your signing key the link is a WRITE BEARER CREDENTIAL for
your entire portfolio — anyone who opens it can make changes as you — so the
command says so on stderr every single time. The URL itself is the only thing on
stdout, so ` + "`rd board | pbcopy`" + ` still copies a clean link.

If you want something narrower:

  rd board --no-key        a READ-ONLY portfolio link with NO keys in it — no
                            signing key, so it cannot write, and safe to share.
                            A confidential board's titles then read as
                            [encrypted] unless the browser has a NIP-07 extension
                            holding your key.
  rd board --this-board    just this directory's board, not the portfolio:
                            https://<board-host>#board=<coord>&relays=<relay-list>
                            (plus this board's read key unless --no-key).
  rd board --strict        refuse to print anything unless the board set could
                            be confirmed complete. The default already refuses
                            when a read relay never ANSWERED — that is real data
                            loss. --strict additionally refuses when a relay
                            answered but had not been given some of your boards,
                            which is a publishing gap, not a permission problem:
                            the keys are here, the boards are not on that relay.
                            The default mints and says which relay is behind.
  rd board --allow-partial print a link even though a read relay never answered,
                            so the board set could not be confirmed complete.
                            The refusal that needs it names it; you should not
                            have to decide this before you have hit it.

To share with SOMEONE ELSE, never send them one of the links above:

  rd board share <npub-or-pubkey>   issue an owner-signed grant to a KNOWN key,
                                     then print the URL (zero-wait: the grant is
                                     durable on the relay before they click).
  rd board share                    mint a one-use claim-nonce link for someone
                                     whose key you don't know yet.

` + "`rd board share`" + ` NEVER puts key material in a link, under any flag or argv
ordering. Their read access is the kind-39301 grant, wrapped to their key alone
and revocable by rotating the board — strictly stronger than a bearer link.

` + boardSecurityNote + `

WITH keys (the default for your own link), that changes for THAT link and only
that link: the fragment also carries the content-encryption key(s) your key
already holds — those and nothing else — so the hosted page can decrypt titles
in your browser. A fragment is never sent to a server and the page strips it
from the address bar immediately, but the link itself is a bearer credential
while it sits in your scrollback or clipboard — anyone you send it to can read
those boards. It carries NO signing key and NO write authority: a content key
cannot sign, and writes still require an owner-signed grant.

Only relays a browser can actually open go into the link. An https page may not
open a ws:// socket (mixed content), so ws:// relays are left out and named on
stderr; the CLI keeps syncing through them.

--host / $RD_BOARD_HOST overrides the hosted-board origin (default:
` + defaultBoardHost + ` — a real, TLS-serving, DNS-resolving host that
serves the browser board UI: open the printed URL and it renders your boards).`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, native := nostrNativeProject()
		if !native {
			return fmt.Errorf("rd board requires a nostr-native project (a pinned board) — run: rd link <coord> first")
		}
		coord := nostrPinnedBoard(dir)

		noKey, _ := cmd.Flags().GetBool("no-key")
		thisBoard, _ := cmd.Flags().GetBool("this-board")
		strict, _ := cmd.Flags().GetBool("strict")
		allowPartial, _ := cmd.Flags().GetBool("allow-partial")

		// ready-4d9 (follow-up), kept through ready-1df. A flag that moves a
		// completeness GUARANTEE is rejected outright anywhere that guarantee is
		// not being made, rather than silently ignored: a user who believes they
		// passed a meaningful flag and did not is exactly how the silent-partial
		// link got shipped in the first place.
		if strict && allowPartial {
			return fmt.Errorf("--strict and --allow-partial are opposites: one refuses on any shortfall the gather could detect, the other mints over one. Pass at most one")
		}
		for _, g := range []struct {
			set  bool
			name string
		}{{strict, "--strict"}, {allowPartial, "--allow-partial"}} {
			if !g.set {
				continue
			}
			switch {
			case noKey:
				return fmt.Errorf("%s applies to the key-bearing portfolio link `rd board` prints by default; --no-key gathers no keys and makes no completeness claim to change", g.name)
			case thisBoard:
				return fmt.Errorf("%s applies to the whole-portfolio gather; --this-board links one board this directory already names, so there is no board SET whose completeness could be in question", g.name)
			}
		}

		// ready-1df. The DEFAULT is the whole portfolio: the epic's own premise is
		// "every work item across all of his rd boards on ONE board", so portfolio
		// is what the bare command means. --this-board is the narrowing.
		if !thisBoard {
			return runBoardPortfolio(cmd, dir, !noKey, strict, allowPartial)
		}

		var keys *boardKeyFragment
		var confidential bool
		if !noKey {
			k, conf, err := ownBoardKeys(dir, coord)
			if err != nil {
				return err
			}
			keys, confidential = k, conf
		}

		host := boardHost(cmd)
		errOut := cmd.ErrOrStderr()
		fmt.Println(ownBoardURL(host, coord, boardLinkRelays(errOut, host), keys))

		// Said AFTER the URL, on stderr, so piping the URL stays clean while
		// the human still reads what they just minted.
		if !noKey {
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

This link NEVER carries key material — not with any flag, not in any argv
order. That is what makes it the command for someone else: their read access is
the owner-signed grant, wrapped to their key alone and revocable by rotating the
board.

` + boardSecurityNote,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		ttl, _ := cmd.Flags().GetDuration("ttl")
		host := boardHost(cmd)
		errOut := cmd.ErrOrStderr()

		if len(args) == 0 {
			// NO BROWSER FILTER HERE, and that is the fix for a regression this
			// call site shipped. ready-634's narrowing was applied to this token
			// on the theory that "a share link is opened by someone else, in a
			// browser". Half of that is true and the wrong half was acted on: the
			// URL is opened in a browser, but the browser reads NOTHING out of the
			// token's relay list. main.ts's afterLogin hits
			// `fragment.kind === "claim"`, renders renderAwaitingAuthorization and
			// RETURNS before any fetchEvents; `payload.relays` has no reader in
			// web/board/src at all. The token's only consumer is `rd join` — a
			// CLI, which dials ws:// perfectly well and for a teammate on the same
			// LAN may have no other way in. boardShareCmd's own Long text still
			// advertises exactly that route ("or run 'rd join <token>' with the
			// raw token").
			//
			// What the filter actually did on a ws://-only project: the token
			// carried relays:null, `rd join` printed "Joined board … READ-ONLY"
			// and skipped relay adoption entirely (redeemNostrClaimToken step 4 is
			// `if len(p.Relays) > 0`), so no rd.json was written and `rd ready`
			// returned nothing — a silent, successful-looking join of a project
			// with zero relays. This token therefore carries the FULL configured
			// set, identically to `rd invite`'s, which is the same token redeemed
			// by the same command. See
			// TestBoardShareToken_RoundTripsThroughRdJoinOverWs.
			token, err := runNostrInvite(ttl)
			if err != nil {
				return err
			}
			fmt.Println(boardURL(host, token))
			fmt.Fprintln(errOut, "\nShare this link. When the recipient's pubkey comes back to you, grant write with:")
			fmt.Fprintln(errOut, "  rd grant --claim <nonce-from-token> <pubkey>")
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
		//
		// ready-f7b: runNostrGrantRevoke now reports the grant's ACTUAL relay
		// delivery outcome (published-and-accepted vs. durably-buffered-no-relay-
		// yet) instead of an unqualified "granted"/"they can read and write", and
		// FAILS this command (returns a non-nil error, no URL printed below) if
		// the grant was permanently rejected by a relay — a genuine failure, not
		// the deliberate offline-durability buffering. Before this fix, `rd board
		// share` printed the same "granted ... they can read and write" and then
		// the board URL even when the grant reached NO relay: the sharer handed
		// out a link believing access was live while the grantee's client (which
		// reads from a relay, never the sharer's local log) saw nothing.
		if err := runNostrGrantRevoke(dir, grantee, role, label, 0, ""); err != nil {
			return err
		}

		// Print the URL AFTER the grant publish attempt completes — zero-wait on
		// the common path (the grant is already on a relay before the recipient
		// can click the link), or, on the buffered path, immediately after the
		// honest "reached NO relay yet" line above so the sharer is not misled
		// about what the link's recipient can do right now. NO claim-nonce is
		// minted here (ready-5c1): the grantee's pubkey is already known and the
		// grant just published is the authorization — a live, unbound
		// claim-nonce would be a bearer credential anyone who saw this URL could
		// later bind to THEIR OWN key via `rd grant --claim`, which is exactly
		// the credential-leak this path must not create. Same plain
		// board+relays fragment shape as the own-board URL — no rd1_ token.
		//
		// ready-df0, unchanged by ready-1df: the nil `keys` argument is THE
		// BOUNDARY. `rd board share` has no key-bearing flag and must never grow
		// one, but the flags' absence is only the outer fence — this hardcoded nil
		// is the wall, and it makes the share path incapable of rendering key
		// material regardless of what flags exist or how argv is ordered. The
		// recipient's read access is the kind-39301 grant published just above,
		// which wraps the CEK to THAT pubkey and can be revoked by rotating the
		// epoch. Putting a CEK in this URL would replace a revocable, per-grantee
		// capability with an unrevocable bearer credential — see
		// TestBoardShareCmd_NeverEmbedsKeyMaterial and
		// TestBoardShareCmd_NoArgvEmitsKeyMaterial.
		board := nostrPinnedBoard(dir)
		if _, _, ok := rdSync.ParseBoardCoord(board); !ok {
			return fmt.Errorf("pinned board %q is malformed", board)
		}
		fmt.Println(ownBoardURL(host, board, boardLinkRelays(errOut, host), nil))
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
	// ready-1df. These are ESCAPE HATCHES, not opt-ins: the bare command already
	// does the thing the owner wants, and each flag below narrows or hardens it.
	// None of them exists on boardShareCmd, and none of them ever will — a link
	// for someone else is always a grant, never a key.
	boardCmd.Flags().Bool("no-key", false, "leave every read key out of the link, so it carries no secret and is safe to paste anywhere (a confidential board's titles then read as [encrypted] in the browser)")
	boardCmd.Flags().Bool("this-board", false, "link only THIS directory's pinned board instead of every board your key can read")
	boardCmd.Flags().Bool("strict", false, "refuse to print a link unless the board set was confirmed complete — also refusing on a publishing gap (a relay that answered but had not been given some of your boards), which the default mints over and names")
	// ready-4d9 (follow-up), re-scoped by ready-1df. The default REFUSES when a
	// read relay never answered, because nothing is known about what that relay
	// held and boards may genuinely be missing from the link. This is the informed
	// way through — an offline owner still gets a link, and the warning on it says
	// it is partial and names what fell short. It is named in the refusal that
	// suggests it, so it is a recovery affordance rather than something a user has
	// to know up front.
	boardCmd.Flags().Bool("allow-partial", false, "mint the link even though a read relay never answered, so the board set could not be confirmed complete (the link's warning then says so and names the relay)")

	boardShareCmd.Flags().String("host", "", hostFlagUsage)
	boardShareCmd.Flags().Duration("ttl", 2*time.Hour, "token time-to-live for the emitted link")
	boardShareCmd.Flags().String("role", rdSync.RoleContributor, "role to grant (owner|maintainer|contributor) — only used with a pubkey argument")
	boardShareCmd.Flags().String("label", "", "human label carried in the grant content — only used with a pubkey argument")

	boardCmd.AddCommand(boardShareCmd)
	rootCmd.AddCommand(boardCmd)
}
