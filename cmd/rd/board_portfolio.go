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
// (portfolioKeyWarning vs boardKeyWarning).
//
// AND THE LINK MUST NOT LIE ABOUT ITS OWN SCOPE. A warning that says "your
// ENTIRE PORTFOLIO" over a board set the command could not confirm is complete is
// a security defect, not a wording one: it invites the owner to reason about a
// 1-board link as though it were the 47-board one. So the gather now reports
// which relays answered (relayFetchMany / portfolioGather), an incomplete gather
// REFUSES to mint by default, and --allow-partial mints a link whose warning says
// partial and names the relay that was missed. Everything else is unchanged and
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

// portfolioRelayTimeout is the deadline for ONE attempt against ONE relay in the
// grant gather, and portfolioRelayAttempts is how many attempts each relay gets.
//
// These are the numbers that actually govern the read, so they are the ones this
// comment describes. nostr.DefaultTimeout (10s) is NOT enough here: the public
// relay is minReplicas=0 scale-to-zero and a cold start has been observed past
// 12s, so a 10s cap turns the owner's FIRST use of the day into a relay miss. The
// retry is not superstition either — the attempt that times out is the attempt
// that WAKES the relay, so attempt 2 is the one that lands.
//
// They are vars, not consts, ONLY so the cold-start test can shorten the clock
// while still driving the real relay, the real nostr.FetchMany and the real retry
// loop. Nothing in the product writes to them.
var (
	portfolioRelayTimeout  = 30 * time.Second
	portfolioRelayAttempts = 2
)

// portfolioGatherBudget bounds the whole cross-relay grant read for n relays.
//
// It is DERIVED from the per-relay numbers instead of being a flat constant,
// because a flat constant is exactly what made this file's previous comment
// false: it advertised a 90s budget "because the relay is scale-to-zero" while
// every individual relay was separately capped at nostr.DefaultTimeout, so the
// 90s could never help a cold start. Any overall budget must be at least the sum
// of the per-relay budgets it contains or it silently truncates them.
func portfolioGatherBudget(n int) time.Duration {
	if n < 1 {
		n = 1
	}
	return time.Duration(n) * time.Duration(portfolioRelayAttempts) * portfolioRelayTimeout
}

// portfolioGather records HOW COMPLETE the grant read was — which relays were
// asked, and which never answered.
//
// It exists because "we got some grants" and "we got all the grants" are
// different facts and the link's honesty depends on the second one. A
// whole-portfolio key link is a claim about a SET ("all your boards"); minting it
// from a read that lost a relay makes the claim false, and the user cannot see
// the loss from the URL.
type portfolioGather struct {
	asked  int            // read relays consulted (0 = local-only project)
	missed []relayFailure // relays that never answered
}

// complete reports that nothing was lost: either no relay was asked (a local-only
// project, where the local log IS the whole world) or every relay asked answered.
func (g portfolioGather) complete() bool { return len(g.missed) == 0 }

// reason renders the unanswered relays for the operator-facing refusal.
func (g portfolioGather) reason() string {
	parts := make([]string, 0, len(g.missed))
	for _, f := range g.missed {
		parts = append(parts, fmt.Sprintf("%s (%v)", f.Relay, f.Err))
	}
	return strings.Join(parts, "; ")
}

// missedRelays lists just the URLs, for the partial-link warning.
func (g portfolioGather) missedRelays() string {
	parts := make([]string, 0, len(g.missed))
	for _, f := range g.missed {
		parts = append(parts, f.Relay)
	}
	return strings.Join(parts, ", ")
}

// portfolioGrantEvents collects the kind-39301 role grants addressed to `self`
// from every read relay, merged with this project's LOCAL signed log. It returns
// the events AND a portfolioGather describing whether that read was complete.
//
// BOTH sources, not either: the relays are the only place a board from ANOTHER
// project directory can come from (this machine's log for repo A knows nothing
// about repo B's board), and the local log is the only place anything comes from
// when the project is local-only or the relays are down. A gather that used only
// the relay would make `rd board --portfolio --with-key` fail offline on a board
// the CLI can demonstrably read.
//
// THE COMPLETENESS SIGNAL IS THE POINT. Merging a rich local log with a dead
// relay yields a perfectly plausible, perfectly WRONG board set, and the previous
// version of this function could not tell the difference: it treated a relay
// error as fatal only when len(out) == 0, so any populated local log silently
// absorbed the loss. Worse, in the default multi-relay configuration followFetch
// returned a nil error whenever ANY relay answered, so there was usually no error
// to notice. Hence relayFetchMany, which reports per-relay outcomes, and hence
// this second return value — the caller decides what an incomplete read is worth,
// and for a bearer credential the answer is "refuse unless told otherwise".
//
// The "#p" filter is a routing hint to the relay and NOTHING MORE. Everything it
// returns is untrusted: DerivePortfolioKeyring re-verifies every signature,
// re-checks that the grant was signed by the board owner, re-checks that the
// signed p tag names this reader, and re-checks that the wrap actually opens.
func portfolioGrantEvents(ctx context.Context, dir, self string, onRetry func(string, int, error)) ([]*nostr.Event, portfolioGather, error) {
	var gather portfolioGather
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
		return nil, gather, fmt.Errorf("rd board --portfolio: read local log: %w", err)
	}
	add(local)

	if relays := nostrReadRelays(); len(relays) > 0 {
		gather.asked = len(relays)
		res := relayFetchMany(ctx, relays, map[string]any{
			"kinds": []int{rdSync.KindRoleGrant},
			"#p":    []string{self},
		}, relayFetchOpts{
			PerAttempt: portfolioRelayTimeout,
			Attempts:   portfolioRelayAttempts,
			OnRetry:    onRetry,
		})
		add(res.Events)
		gather.missed = res.Failed
	}

	out := make([]*nostr.Event, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	// Deterministic order so the same relay snapshot always yields the same link.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })

	// Nothing at all to derive from is fatal regardless of what the caller is
	// willing to tolerate: there is no link to mint, partial or otherwise.
	if !gather.complete() && len(out) == 0 {
		return nil, gather, fmt.Errorf("rd board --portfolio: no grants could be read (relays unreachable): %s", gather.reason())
	}
	return out, gather, nil
}

// portfolioKeys gathers EVERY board this machine's key can read — across all
// projects, not just the pinned board of the current directory — and returns the
// key material for them.
//
// It reuses rdSync.DerivePortfolioKeyring, which reuses DeriveBoardKeyring: the
// SAME authorization computation the read path runs. A key this machine cannot
// legitimately derive can therefore never reach a link, however many boards the
// relay claims exist.
func portfolioKeys(ctx context.Context, dir string, onRetry func(string, int, error)) (*portfolioKeyFragment, portfolioGather, error) {
	var gather portfolioGather
	k, err := nostrKey()
	if err != nil {
		return nil, gather, err
	}
	self := k.PubKeyHex()
	events, gather, err := portfolioGrantEvents(ctx, dir, self, onRetry)
	if err != nil {
		return nil, gather, err
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
	return f, gather, nil
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
func runBoardPortfolio(cmd *cobra.Command, dir string, withKey, allowPartial bool) error {
	host := boardHost(cmd)
	relays := inviteRelaySet()
	errOut := cmd.ErrOrStderr()

	// Past argv validation: every failure below is a RUNTIME condition (an
	// unreachable relay), not a mistyped command, so dumping the flag table on
	// top of it only buries the sentence the operator has to act on. Same idiom
	// as list.go / show.go.
	cmd.SilenceUsage = true

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
	ctx, cancel := context.WithTimeout(base, portfolioGatherBudget(len(nostrReadRelays())))
	defer cancel()

	// Say WHY the command is still running before the retry, so a cold
	// scale-to-zero relay reads as "waking the relay" rather than "hung".
	onRetry := func(relay string, attempt int, err error) {
		fmt.Fprintf(errOut, "NOTE: %s did not answer within %s (%v) — retrying (attempt %d of %d); a scale-to-zero relay is usually cold on first use.\n",
			relay, portfolioRelayTimeout, err, attempt, portfolioRelayAttempts)
	}

	keys, gather, err := portfolioKeys(ctx, dir, onRetry)
	if err != nil {
		return err
	}

	// THE GATE. An incomplete gather cannot produce this link by default. The
	// boards behind a whole-portfolio credential are a SET the link asserts is
	// whole; if a relay went unanswered, boards known only to that relay are
	// missing and NOTHING in the printed URL shows it. Refusing here is what
	// makes the warning below trustworthy — it can say "your entire portfolio"
	// only because this branch already ruled out the case where it would be a
	// lie. --allow-partial is the explicit, informed way through (the offline
	// owner still gets a link, and gets told what it is).
	if !gather.complete() && !allowPartial {
		return fmt.Errorf(`rd board --portfolio --with-key: INCOMPLETE BOARD GATHER — refusing to mint a link that would look whole and is not.
  Unreachable: %s
  %d of %d read relays answered. This key holds read keys for %d board(s) from what DID answer, but any board known only to the unreachable relay(s) is missing, and the printed URL would not show that.
  Fix the relay and re-run, or pass --allow-partial to mint the narrower link on purpose (its warning then says it is partial and names what was missed)`,
			gather.reason(), gather.asked-len(gather.missed), gather.asked, keys.boardCount())
	}

	blob := ""
	if keys.carriesSecret() {
		if blob, err = keys.blob(); err != nil {
			return err
		}
	}
	fmt.Println(portfolioURL(host, keys.viewer, relays, blob))

	switch {
	case keys.carriesSecret():
		fmt.Fprintln(errOut, portfolioKeyWarning(keys.boardCount(), gather))
	case !gather.complete():
		fmt.Fprintln(errOut, "WARNING: PARTIAL — no keys embedded, but "+gather.missedRelays()+" never answered, so this is not a statement that you hold no read keys. Boards known only to that relay were never seen. Re-run when the relay is reachable.")
	default:
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
//
// THE COUNT AND THE CLAIM MUST AGREE, which is why there are two sentences here
// and not one. The single-sentence version said "ALL %d OF YOUR CONFIDENTIAL
// BOARDS ... your ENTIRE PORTFOLIO" whatever the gather did — text that reads as
// true for 1 board and for 47, and was in fact printed over a 1-board link minted
// from a dead relay by an owner who has 47. "Entire portfolio" is a claim about
// COMPLETENESS, so it is only ever said on the branch where completeness was
// established; the partial branch says the opposite, in the same breath as the
// count, and names the relay whose boards are missing.
func portfolioKeyWarning(n int, g portfolioGather) string {
	boards := "boards"
	if n == 1 {
		boards = "board"
	}
	if !g.complete() {
		return fmt.Sprintf("WARNING: this link is PARTIAL and CARRIES THE READ KEYS FOR %d CONFIDENTIAL %s in its fragment — anyone who opens it can read every title on %s. It is NOT your whole portfolio: %s never answered, so boards known only to there are MISSING from this link and from the count. Re-run when that relay is reachable to get the complete link. It is still a bearer credential for %s: treat it like a password — do not paste it into chat, a ticket, or any shared channel.",
			n, strings.ToUpper(boards), pluralBoards(n), g.missedRelays(), pluralBoards(n))
	}
	return fmt.Sprintf("WARNING: this link CARRIES THE READ KEYS FOR ALL %d OF YOUR CONFIDENTIAL %s in its fragment — anyone who opens it can read every title in your ENTIRE PORTFOLIO, not just one board. It is a far wider credential than a single-board `rd board --with-key` link. Treat it like a password: do not paste it into chat, a ticket, or any shared channel.", n, strings.ToUpper(boards))
}

// pluralBoards renders "that 1 board" / "those 7 boards" for the partial warning,
// which cannot fall back on "your entire portfolio" to describe its scope.
func pluralBoards(n int) string {
	if n == 1 {
		return "that 1 board"
	}
	return fmt.Sprintf("those %d boards", n)
}
