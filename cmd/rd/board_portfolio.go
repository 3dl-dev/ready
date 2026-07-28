package main

// ready-4d9: ONE link that opens the WHOLE portfolio decrypted.
//
// `rd board --with-key` (ready-df0) mints a link for the pinned board of the
// current directory. The owner has ~24 boards, so answering "give me a link to
// the whole portfolio" with that command means 24 links. This file is the other
// half: gather every board THIS key can read (pkg/sync.DerivePortfolioKeyring),
// and serialize all of their keys into one fragment.
//
// WHY THE KEY MATERIAL IS A BINARY BLOB AND NOT A LONGER cek= LIST.
//
// The obvious extension of ready-df0's grammar is board-scoped text:
// cek=<boardD>:<epoch>:<64-hex>[,...]. It has a failure mode this link cannot
// afford. Twenty-four 32-byte keys are 1.5 KB of hex before coordinates and
// relays; terminals wrap that and chat clients truncate it. A comma-delimited
// list TRUNCATED mid-flight still parses — as a SHORTER, perfectly well-formed
// list. The reader would open a board that looks complete and is not, with no
// signal anywhere that anything was lost. That is ready-62d1's lesson pointed at
// a new target: a damaged key-bearing link must fail VISIBLY.
//
// So the fragment carries keys= : base64url of a length-prefixed binary record
// whose counts are declared UP FRONT. The format and its single encoder live in
// pkg/sync/portfolioblob.go. Two properties fall out of that shape:
//
//   - Truncation is ALWAYS an error. Every count is read before the entries it
//     governs, and the decoder requires the buffer to be exactly consumed, so no
//     proper prefix of a valid blob is itself a valid blob. The decoder lives in
//     the browser (web/board/src/lib/portfoliokeys.ts), which is where that
//     property is proven — exhaustively, over EVERY prefix length of a
//     Go-emitted blob, in portfoliokeys.test.ts. The bytes those two files agree
//     on are pinned from both sides by web/board/testdata/portfolio-key-vectors.json.
//   - It is smaller: 1552 characters for the real 24-board portfolio against
//     ~1849 for the board-scoped hex grammar. A real saving, and a secondary
//     one — the truncation property is what decided the format.
//
// SECURITY POSTURE — inherited from ready-df0, with a bigger blast radius.
// A single-board key link is a bearer credential for one board's content. THIS
// link is a bearer credential for EVERY board's content, and the stderr warning
// says exactly that, in different words from the single-board one
// (portfolioKeyWarning vs boardKeyWarning). Everything else is unchanged and
// still load-bearing: opt-in flag only, never a default; the fragment never
// reaches a server and the page strips it in a `finally`; a CEK cannot sign and
// conveys no write authority; the nsec never enters the page. And `rd board
// share` STILL emits no key material under any flag combination — it has neither
// --with-key nor --portfolio, and its RunE passes a hardcoded nil to
// ownBoardURL. That nil is the structural guard; the flags' absence is only the
// first fence.

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// portfolioGatherTimeout bounds the whole cross-relay grant read. It is generous
// because the public relay is scale-to-zero (a cold start alone has been observed
// past nostr.DefaultTimeout) and because a portfolio read that gives up early
// mints a link that is missing boards — the silent-partial outcome this item is
// specifically about. followFetch applies nostr.DefaultTimeout per relay inside
// this budget.
const portfolioGatherTimeout = 90 * time.Second

// portfolioGrantEvents collects the kind-39301 role grants addressed to `self`
// from every read relay, merged with this project's LOCAL signed log.
//
// BOTH sources, not either: the relays are the only place a board from ANOTHER
// project directory can come from (this machine's log for repo A knows nothing
// about repo B's board), and the local log is the only place anything comes from
// when the project is local-only or the relays are down. A gather that used only
// the relay would make `rd board --portfolio --with-key` fail offline on a board
// the CLI can demonstrably read.
//
// The "#p" filter is a routing hint to the relay and NOTHING MORE. Everything it
// returns is untrusted: DerivePortfolioKeyring re-verifies every signature,
// re-checks that the grant was signed by the board owner, re-checks that the
// signed p tag names this reader, and re-checks that the wrap actually opens.
func portfolioGrantEvents(ctx context.Context, dir, self string) ([]*nostr.Event, error) {
	seen := map[string]*nostr.Event{}
	add := func(evs []*nostr.Event) {
		for _, e := range evs {
			if e != nil {
				seen[e.ID] = e
			}
		}
	}

	local, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		return nil, fmt.Errorf("rd board --portfolio: read local log: %w", err)
	}
	add(local)

	var relayErr error
	if relays := nostrReadRelays(); len(relays) > 0 {
		evs, ferr := followFetch(ctx, relays, map[string]any{
			"kinds": []int{rdSync.KindRoleGrant},
			"#p":    []string{self},
		})
		add(evs)
		relayErr = ferr
	}

	out := make([]*nostr.Event, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	// Deterministic order so the same relay snapshot always yields the same link.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	// A relay error is only fatal when it left us with nothing to derive from;
	// otherwise the local log still produces a usable (if possibly narrower) link
	// and the caller reports the board count it actually got.
	if relayErr != nil && len(out) == 0 {
		return nil, fmt.Errorf("rd board --portfolio: no grants could be read (relays unreachable): %w", relayErr)
	}
	return out, nil
}

// portfolioKeys gathers EVERY board this machine's key can read — across all
// projects, not just the pinned board of the current directory — and returns the
// key material for them.
//
// It reuses rdSync.DerivePortfolioKeyring, which reuses DeriveBoardKeyring: the
// SAME authorization computation the read path runs. A key this machine cannot
// legitimately derive can therefore never reach a link, however many boards the
// relay claims exist.
func portfolioKeys(ctx context.Context, dir string) (*portfolioKeyFragment, error) {
	k, err := nostrKey()
	if err != nil {
		return nil, err
	}
	self := k.PubKeyHex()
	events, err := portfolioGrantEvents(ctx, dir, self)
	if err != nil {
		return nil, err
	}

	kr, coords := rdSync.DerivePortfolioKeyring(events, k)
	f := &portfolioKeyFragment{viewer: self, boards: map[string]map[int][32]byte{}}
	for _, coord := range coords {
		byEpoch := map[int][32]byte{}
		for _, ep := range kr.Epochs(coord) {
			if cek, held := kr.CEK(coord, ep); held {
				byEpoch[ep] = cek
			}
		}
		if len(byEpoch) > 0 {
			f.boards[coord] = byEpoch
		}
	}
	// kr.LTK is deliberately NOT read — see this file's header and cmd/rd/board.go's
	// LEAST PRIVILEGE note. The label-token key has no consumer in the browser.
	return f, nil
}

// portfolioKeyFragment is the OPT-IN key material `rd board --portfolio
// --with-key` embeds. Like ready-df0's boardKeyFragment, `viewer` is a PUBLIC
// pubkey (so nothing has to be pasted into the page) and `boards` is SECRET.
//
// There is deliberately no LTK here either: the label-token key still has no
// reader in the browser (see cmd/rd/board.go's LEAST PRIVILEGE note), and
// multiplying a secret-with-no-consumer by 24 boards does not improve it.
type portfolioKeyFragment struct {
	viewer string                      // 64-hex pubkey of the key that minted this link
	boards map[string]map[int][32]byte // board coordinate -> epoch -> CEK
}

// carriesSecret reports whether this fragment actually ships key material, as
// opposed to only the public viewer pubkey. It decides whether the bearer-
// credential warning prints — the user must be told when, and only when, the
// link is one.
func (f *portfolioKeyFragment) carriesSecret() bool {
	return f != nil && len(f.boards) > 0
}

// boardCount is how many boards' keys the link carries. It goes into the warning
// text, because "the read keys for 17 boards" tells the user what they are
// holding in a way "the read keys" does not.
func (f *portfolioKeyFragment) boardCount() int {
	if f == nil {
		return 0
	}
	return len(f.boards)
}

// blob renders the held keys as the base64url payload of the keys= parameter.
// The format itself, and the one encoder for it, live in
// pkg/sync/portfolioblob.go — read that file's header for why it is binary and
// why every truncation of it is an error.
func (f *portfolioKeyFragment) blob() (string, error) {
	return rdSync.EncodePortfolioKeyBlob(f.boards)
}

// portfolioURL builds the whole-portfolio link:
//
//	https://<host>#pk=<64-hex>&relays=<comma-list>[&keys=<base64url>]
//
// NO board= parameter, and that absence is the format's discriminator: a
// fragment carrying pk= with no board= is a request to open EVERY board the
// viewer can see, which is precisely what the page's own-boards discovery
// already does (web/board/src/main.ts). The view was portfolio-wide before this
// item; only the keys were not.
//
// PARAMETER ORDER IS DELIBERATE and is not url.Values.Encode's (which sorts
// alphabetically and would put the big keys= blob first). The blob goes LAST so
// that a link truncated in transit loses blob bytes — which the decoder rejects
// loudly — instead of losing pk=/relays= and degrading into an unrecognizable
// fragment the page can only answer with a bare login form.
func portfolioURL(host, viewer string, relays []string, blob string) string {
	parts := []string{"pk=" + url.QueryEscape(viewer)}
	if len(relays) > 0 {
		parts = append(parts, "relays="+url.QueryEscape(strings.Join(relays, ",")))
	}
	if blob != "" {
		parts = append(parts, "keys="+blob)
	}
	return host + "#" + strings.Join(parts, "&")
}

// runBoardPortfolio is `rd board --portfolio [--with-key]`: ONE URL covering
// every board this key can read.
//
// WITHOUT --with-key it prints only the public shape (pk= + relays=), which is
// still useful — the page opens the viewer's whole board list with no npub to
// paste — and carries no secret whatsoever. WITH --with-key it also carries the
// keys= blob and warns, on stderr, that the link is now a portfolio-wide bearer
// credential.
//
// The URL goes to stdout and everything else to stderr, so
// `rd board --portfolio --with-key | pbcopy` copies exactly the URL while the
// human still reads the warning.
func runBoardPortfolio(cmd *cobra.Command, dir string, withKey bool) error {
	host := boardHost(cmd)
	relays := inviteRelaySet()
	errOut := cmd.ErrOrStderr()

	if !withKey {
		// No key gather at all on this path: without --with-key there is nothing
		// to put in the link, so there is no reason to read a single grant, and
		// no code path from here to a secret.
		k, err := nostrKey()
		if err != nil {
			return err
		}
		fmt.Println(portfolioURL(host, k.PubKeyHex(), relays, ""))
		fmt.Fprintln(errOut, "NOTE: no keys embedded — this link opens every board you can see, but a CONFIDENTIAL board's titles will show as placeholders unless the browser has a NIP-07 extension holding your key. Add --with-key to embed them (the link then becomes a bearer credential for your whole portfolio).")
		return nil
	}

	// cmd.Context() is nil unless the command was driven through ExecuteContext,
	// which includes every direct RunE call a test makes.
	base := cmd.Context()
	if base == nil {
		base = context.Background()
	}
	ctx, cancel := context.WithTimeout(base, portfolioGatherTimeout)
	defer cancel()
	keys, err := portfolioKeys(ctx, dir)
	if err != nil {
		return err
	}
	blob := ""
	if keys.carriesSecret() {
		if blob, err = keys.blob(); err != nil {
			return err
		}
	}
	fmt.Println(portfolioURL(host, keys.viewer, relays, blob))

	if keys.carriesSecret() {
		fmt.Fprintln(errOut, portfolioKeyWarning(keys.boardCount()))
	} else {
		fmt.Fprintln(errOut, "NOTE: no keys embedded — this key holds no read key for any confidential board, so there is nothing to decrypt. Ask each board's owner to run: rd grant "+keys.viewer)
	}
	return nil
}

// portfolioKeyWarning is the stderr line for a whole-portfolio key link. It is
// deliberately NOT boardKeyWarning: the two links differ in blast radius, so
// they must differ in what they tell the user. boardKeyWarning says "every title
// on this board"; this one has to say every title on every board, and name the
// count, because a user who has internalized the single-board warning would
// otherwise apply single-board judgement to a portfolio-wide credential.
func portfolioKeyWarning(n int) string {
	boards := "boards"
	if n == 1 {
		boards = "board"
	}
	return fmt.Sprintf("WARNING: this link CARRIES THE READ KEYS FOR ALL %d OF YOUR CONFIDENTIAL %s in its fragment — anyone who opens it can read every title in your ENTIRE PORTFOLIO, not just one board. It is a far wider credential than a single-board `rd board --with-key` link. Treat it like a password: do not paste it into chat, a ticket, or any shared channel.", n, strings.ToUpper(boards))
}
