package main

// ready-4d9: ONE link that opens the WHOLE portfolio decrypted.
//
// `rd board --this-board` (ready-df0) mints a link for the pinned board of the
// current directory. The owner has ~24 boards, so answering "give me a link to
// the whole portfolio" with that command means 24 links. This file is the other
// half: gather every board THIS key can read (pkg/sync.DerivePortfolioKeyring),
// and serialize all of their keys into one fragment.
//
// ready-1df made this file's path the DEFAULT — bare `rd board` runs
// runBoardPortfolio — because the epic's premise was always "every work item
// across all of his rd boards on ONE board". See cmd/rd/board.go's header for
// the FOR-ME / FOR-SOMEONE-ELSE boundary that decides what may be a default and
// what may never be.
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
// 1-board link as though it were the 47-board one. So the gather reports which
// relays answered (relayFetchMany / portfolioGather), and — because "answered" is
// a much weaker fact than "served everything" — which relays answered SHORT of
// the grants the read can prove exist (portfolioGather.short, detectShortAnswers).
//
// THE TWO SHORTFALLS CONCLUDE DIFFERENTLY (ready-1df). A relay that NEVER
// ANSWERED refuses to mint — nothing is known about what it held, so a board may
// genuinely be missing from the link — and --allow-partial is the named way
// through, minting a link whose warning says PARTIAL and names what fell short.
// A relay that ANSWERED SHORT mints and says so: every board it failed to serve
// is one this read already has from the local log or another relay, so nothing is
// missing from the link; what is missing is that relay's COPY. --strict restores
// the older, stricter reading. portfolioGather.lostRelay has the full argument,
// and it matters in practice: 32 of the owner's boards live only on his LAN
// relays, so the conflated gate refused on every single invocation.
//
// AND THE CLEAN PATH IS SCOPED, TOO, because the gate is a floor and not a proof.
// A relay that serves a subset of what it holds and then sends EOSE is
// indistinguishable, from one REQ, from a relay that holds only that subset —
// NIP-01 carries no count to check against. So the minted link's warning says
// "all N boards THIS GATHER COULD FIND" and carries a scope clause stating what
// was established ("no shortfall was detectable") rather than what was not
// ("nothing was missed"). portfolioGather's doc has the full epistemics.
// Everything else is unchanged and
// still load-bearing: the bearer-credential warning on EVERY key-bearing link
// (which, since ready-1df made minting the default, is the whole of what keeps
// that default safe); the fragment never reaches a server and the page strips it
// in a `finally`; a CEK cannot sign and conveys no write authority; the nsec
// never enters the page. And `rd board share` STILL emits no key material under
// any flag combination — it has no key-bearing flag, and its RunE passes a
// hardcoded nil to ownBoardURL. That nil is the structural guard; the flags'
// absence is only the first fence.

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

// shortAnswer names one relay that ANSWERED and answered SHORT: it accepted the
// REQ and sent EOSE, but did not serve grants for boards this same read proved
// exist. Missing holds those board coordinates, so the operator is told which
// boards the relay owes rather than "a relay was short".
type shortAnswer struct {
	Relay   string
	Served  int      // readable board coordinates this relay did supply
	Missing []string // readable board coordinates it did not, and which exist
}

// portfolioGather records HOW COMPLETE the grant read was — which relays were
// asked, which never answered, and which ANSWERED SHORT.
//
// It exists because "we got some grants" and "we got all the grants" are
// different facts and the link's honesty depends on the second one. A
// whole-portfolio key link is a claim about a SET ("all your boards"); minting it
// from a read that lost a relay makes the claim false, and the user cannot see
// the loss from the URL.
//
// WHY "EVERY RELAY ANSWERED" WAS NOT ENOUGH, and what replaced it. The first cut
// of this gate keyed on len(missed) == 0. A relay that accepts the REQ, serves 3
// of the 15 grants it holds, and sends EOSE walks straight through that: it is in
// Answered, it is not in Failed, and the link is minted claiming the entire
// portfolio. There is no count, digest or continuation marker in NIP-01 to check
// a single answer against, so a short answer is INDISTINGUISHABLE, from that one
// REQ alone, from a relay that legitimately holds only 3.
//
// What IS available is a FLOOR — grants the reader can prove exist because it
// already has them from somewhere else:
//
//   - THE LOCAL LOG. rd holds signed grants locally. A relay that did not serve
//     one of those did not serve everything matching the filter that demonstrably
//     exists. (It may never have received it; that is not a contradiction — the
//     conclusion is only that this relay is not a complete mirror, which is
//     exactly what disqualifies it as evidence about boards nobody else knows.)
//   - THE OTHER RELAYS. If relay A serves grants for 15 boards and relay B serves
//     3, B is demonstrably short, and the comparison costs nothing extra: both
//     were queried anyway.
//
// Both are the same computation — union versus per-relay — and both are SOUND:
// every coordinate in Missing is backed by a grant that verified, so the shortfall
// is proven, not inferred.
//
// WHAT REMAINS UNDETECTABLE, stated plainly because a gate that hid this would be
// worse than no gate. The CEILING cannot be established. If a relay withholds
// grants for boards that appear in NO local log and on NO other relay, the floor
// is silent and the link is minted without them. Nothing in the protocol closes
// that, which is why the minted link's warning is scoped to what the gather could
// FIND (portfolioKeyWarning) instead of asserting that the set is exhaustive, and
// why the honest reading of a clean gather is "no shortfall was detectable", not
// "the portfolio is complete".
type portfolioGather struct {
	asked  int            // read relays consulted (0 = local-only project)
	missed []relayFailure // relays that never answered
	short  []shortAnswer  // relays that answered, but served less than the floor
	found  int            // readable board coordinates the whole read established
}

// complete reports that no shortfall was DETECTED: every relay asked answered,
// and no relay's answer was short of what the rest of the read proved exists.
// Asking no relay at all is complete too — there is no relay whose contents were
// missed — though that is a NARROWER fact than it sounds and the warning says so
// separately (scopeClause's asked == 0 branch).
//
// It is NOT a statement that the portfolio is whole — see the type's doc for the
// ceiling this cannot reach. It is also NOT the refusal condition any more —
// that is lostRelay; see it for why the two kinds of shortfall got different
// conclusions (ready-1df).
func (g portfolioGather) complete() bool { return len(g.missed) == 0 && len(g.short) == 0 }

// lostRelay reports the ONE kind of shortfall that can actually cost this link a
// board: a relay that never answered.
//
// ready-1df — WHY THE TWO KINDS OF SHORTFALL NOW CONCLUDE DIFFERENTLY. The gate
// used to refuse on complete() — unanswered relays and short answers alike — and
// that conflation is what made `--allow-partial` a flag the owner had to type to
// look at his own work. Thirty-two of his boards (rdtestagentproj4uw,
// rdtestbackendc0, e2eproj, veriproj, existing2 ...) live only on his LAN relays
// and were never pushed to the public one, so the public relay answered SHORT on
// every single invocation and the command refused every single time. The board
// PAGE already handles that exact situation gracefully — "That is a publishing
// gap, not a permission problem — the keys are here; the boards are not on the
// relay" — so the CLI was refusing for something the product already explains.
//
// The two facts are not the same fact:
//
//   - A RELAY THAT NEVER ANSWERED is an EMPTY set of observations. Nothing is
//     known about what it held. Boards that exist only there are missing from
//     the link and there is no way to see that from the URL. That is real data
//     loss, and it still REFUSES (--allow-partial is the named way through).
//   - A RELAY THAT ANSWERED SHORT served everything it was going to serve, and
//     the difference between that and the floor is, by construction, grants this
//     read ALREADY HAS from somewhere else — the local log or another relay.
//     They are in the union, so they are in the link. Nothing was lost from the
//     link; what the shortfall proves is that the relay has not been given
//     everything, which is a replication fact about the relay, not a gap in the
//     credential. (The unprovable residue — a relay quietly withholding boards
//     nothing else knows — is the CEILING, and it is exactly as unreachable for
//     a relay that served in full. It is not a reason to treat these two
//     differently; scopeClause states it for both.)
//
// So the detection stays (it is ready-4d9's, and it is mutation-proven); what
// changed is what it CONCLUDES. A short answer now mints and says which relay is
// behind and which boards it owes. --strict restores the old, stricter reading
// for anyone who wants a link only when nothing at all fell short.
func (g portfolioGather) lostRelay() bool { return len(g.missed) > 0 }

// publishingGap reports that a relay answered but had not been given boards this
// read can prove exist. The keys are in the link; the boards are not on that
// relay — the same distinction the board page draws in its own banner.
func (g portfolioGather) publishingGap() bool { return len(g.short) > 0 }

// reason renders BOTH kinds of shortfall for the operator-facing refusal. The two
// are kept apart in the text because the fix differs: an unanswered relay has to
// come up, a short one has to be backfilled (or replaced).
func (g portfolioGather) reason() string {
	parts := make([]string, 0, len(g.missed)+len(g.short))
	for _, f := range g.missed {
		parts = append(parts, fmt.Sprintf("%s never answered (%v)", f.Relay, f.Err))
	}
	for _, s := range g.short {
		parts = append(parts, fmt.Sprintf("%s answered SHORT — it served %d of the %d board(s) this read proved exist, and did not serve: %s",
			s.Relay, s.Served, g.found, strings.Join(s.Missing, ", ")))
	}
	return strings.Join(parts, "; ")
}

// answered is how many of the asked relays replied at all — the numerator the
// refusal quotes. A relay that answered short is counted here as having answered,
// because it did; the refusal names its shortfall separately.
func (g portfolioGather) answered() int { return g.asked - len(g.missed) }

// shortfallSummary is reason() without the board-coordinate list: the one-line
// form for a warning, where 64-hex coordinates would bury the sentence. It still
// says WHICH relay and WHICH KIND of shortfall, because "partial" without either
// is a shrug.
func (g portfolioGather) shortfallSummary() string {
	parts := make([]string, 0, len(g.missed)+len(g.short))
	for _, f := range g.missed {
		parts = append(parts, f.Relay+" never answered")
	}
	for _, s := range g.short {
		parts = append(parts, fmt.Sprintf("%s answered SHORT (it served %d of the %d board(s) this read proved exist)", s.Relay, s.Served, g.found))
	}
	return strings.Join(parts, "; ")
}

// scopeClause states, in the minted link's warning, EXACTLY what was established
// about the board set — never more.
//
// This is the sentence that stops "every relay answered" from being printed as
// "your entire portfolio". It covers the three ways a link is minted without a
// LOST relay, which are not the same fact and must not share wording:
//
//	asked == 0        nobody was consulted at all.
//	publishingGap()   everyone answered, but a relay had not been given some of
//	                  the boards this read proved exist. Nothing is missing from
//	                  the LINK — those keys came from the local log or another
//	                  relay and travel in it — but their CARDS will be empty in a
//	                  browser pointed at that relay. ready-1df: this is the case
//	                  the CLI used to refuse over, while the page already
//	                  explained it correctly.
//	default           everyone answered and nobody was detectably short.
//
// The fourth way a link gets minted — --allow-partial over an unanswered relay —
// never reaches here; that branch says PARTIAL and names what fell short
// (shortfallSummary).
func (g portfolioGather) scopeClause() string {
	switch {
	case g.asked == 0:
		return "SCOPE: no read relays are configured, so this covers what THIS MACHINE's local log holds and nothing was asked of the network — a board that exists only on a relay was never looked for."
	case g.publishingGap():
		return fmt.Sprintf("SCOPE: %d of %d read relay(s) answered, and %s. That is a PUBLISHING GAP, not a permission problem — the keys are here and they are in this link; those boards are just not on that relay, so their cards will be empty in a browser until it is back-filled. Beyond that, a relay that quietly serves a SUBSET of what it holds cannot be told apart from one that holds only that much, so this is 'no further shortfall was detectable', not 'nothing was missed'.",
			g.answered(), g.asked, g.shortfallSummary())
	default:
		return fmt.Sprintf("SCOPE: %d of %d read relay(s) answered and none served short of what this read proved exists — but a relay that quietly serves a SUBSET of what it holds cannot be told apart from one that holds only that much, so this is 'no shortfall was detectable', not 'nothing was missed'.", g.answered(), g.asked)
	}
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
func portfolioGrantEvents(ctx context.Context, dir, self string, onRetry func(string, int, error)) (grantSources, portfolioGather, error) {
	var src grantSources
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
		return src, gather, fmt.Errorf("rd board --portfolio: read local log: %w", err)
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
		src.perRelay = res.PerRelay
		gather.missed = res.Failed
	}

	out := make([]*nostr.Event, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	// Deterministic order so the same relay snapshot always yields the same link.
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	src.merged = out

	// Nothing at all to derive from is fatal regardless of what the caller is
	// willing to tolerate: there is no link to mint, partial or otherwise.
	if gather.lostRelay() && len(out) == 0 {
		return src, gather, fmt.Errorf("rd board --portfolio: no grants could be read (relays unreachable): %s", gather.reason())
	}
	return src, gather, nil
}

// grantSources keeps each relay's answer SEPARATE from the merged union the
// keyring is derived from.
//
// The split is the whole basis of short-answer detection: once the local log,
// relay A and relay B are one slice, "relay B served less than the rest" is
// unanswerable. The local log needs no field of its own — it is folded into
// merged, and merged is the floor every relay is measured against, which is
// exactly how the local log and the other relays end up contributing to the same
// comparison.
type grantSources struct {
	perRelay []relayAnswer  // what each relay that ANSWERED individually served
	merged   []*nostr.Event // de-duplicated union of local + every relay, ordered
}

// detectShortAnswers finds every relay that answered with LESS than the read as a
// whole proved exists.
//
// It runs the SAME derivation the link is built from (rdSync.DerivePortfolioKeyring)
// once per relay, over that relay's events alone, and compares the resulting
// readable-board coordinates against the union's. Re-running the real derivation
// rather than reimplementing a cheaper predicate is deliberate: a second,
// approximate notion of "which boards these events yield" would be a second
// authorization computation to keep in sync, and this file's whole point is that
// the link may only ever contain what the real one produced.
//
// The comparison is over READABLE BOARD COORDINATES, not raw event counts,
// because coverage is what the link claims. A relay missing a duplicate grant for
// a board already covered is not short in any sense the user cares about; a relay
// missing the only grant for a board is.
//
// SOUNDNESS. Derivation is monotone in the event set — a coordinate is admitted
// iff SOME grant event passes per-event checks (owner-signed, addressed to this
// reader, epoch >= 1, wrap opens) — so coords(subset) is always a subset of
// coords(union), and every coordinate reported Missing is backed by a grant that
// verified somewhere else. No false positive is possible from partial context.
//
// LIMIT. This can only ever prove a FLOOR violation. A relay that withholds
// boards nothing else knows about produces an empty Missing set and is reported
// as having answered in full. See portfolioGather's doc.
func detectShortAnswers(src grantSources, reader *nostr.Key, union []string) []shortAnswer {
	if len(src.perRelay) == 0 || len(union) == 0 {
		return nil
	}
	want := make(map[string]bool, len(union))
	for _, c := range union {
		want[c] = true
	}
	var out []shortAnswer
	for _, a := range src.perRelay {
		_, served := rdSync.DerivePortfolioKeyring(a.Events, reader)
		has := make(map[string]bool, len(served))
		for _, c := range served {
			has[c] = true
		}
		var missing []string
		for c := range want {
			if !has[c] {
				missing = append(missing, c)
			}
		}
		if len(missing) == 0 {
			continue
		}
		sort.Strings(missing)
		out = append(out, shortAnswer{Relay: a.Relay, Served: len(served), Missing: missing})
	}
	return out
}

// filterArchivedBoards drops every coordinate in coords whose CURRENT
// (latest-wins, board-fold-spec.md §4.5) kind-30301 definition carries the
// archived marker (ready-a9b, `rd board archive`). It reads the SAME
// local-log-union-read-relays shape portfolioGrantEvents already builds for
// role grants, applied to board (30301) events instead: local log first (so
// an offline read still honours a marker this machine already knows about),
// then every configured read relay, merged, then WinningBoardEvent picks the
// latest-wins definition per coordinate — exactly the read side of what `rd
// board archive`'s write publishes.
//
// A relay that fails to answer here degrades to "whatever the local log and
// the OTHER relays already established" — the same posture portfolioGrantEvents
// takes for a single relay miss — because an archived board is a
// portfolio-discovery nicety, not a security or durability boundary: a read
// failure here must never turn into a hard refusal of the whole portfolio
// link the way a lost grant relay does.
func filterArchivedBoards(ctx context.Context, dir string, coords []string, onRetry func(string, int, error)) []string {
	if len(coords) == 0 {
		return coords
	}
	owners := map[string]bool{}
	for _, c := range coords {
		if o, _, ok := rdSync.ParseBoardCoord(c); ok {
			owners[o] = true
		}
	}
	authors := make([]string, 0, len(owners))
	for o := range owners {
		authors = append(authors, o)
	}

	var events []*nostr.Event
	if local, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll(); err == nil {
		events = append(events, local...)
	}
	if relays := nostrReadRelays(); len(relays) > 0 {
		res := relayFetchMany(ctx, relays, map[string]any{
			"kinds":   []int{rdSync.KindBoard},
			"authors": authors,
		}, relayFetchOpts{
			PerAttempt: portfolioRelayTimeout,
			Attempts:   portfolioRelayAttempts,
			OnRetry:    onRetry,
		})
		events = append(events, res.Events...)
	}

	out := make([]string, 0, len(coords))
	for _, c := range coords {
		if e, found := rdSync.WinningBoardEvent(events, c); found && rdSync.IsBoardArchived(e) {
			continue
		}
		out = append(out, c)
	}
	return out
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
	src, gather, err := portfolioGrantEvents(ctx, dir, self, onRetry)
	if err != nil {
		return nil, gather, err
	}

	kr, coords := rdSync.DerivePortfolioKeyring(src.merged, k)
	// ready-a9b: drop every coordinate whose CURRENT board definition carries
	// the archived marker BEFORE anything downstream counts it. An archived
	// board's grant may still be perfectly readable — archiving touches no
	// grant — but it must not occupy a slot in "found", in the short-answer
	// comparison, or in the minted key blob. See filterArchivedBoards's doc.
	coords = filterArchivedBoards(ctx, dir, coords, onRetry)
	// The second half of the completeness question, and the one "did the relay
	// answer" cannot reach: did any relay that DID answer serve less than the
	// rest of this read proves exists?
	gather.found = len(coords)
	gather.short = detectShortAnswers(src, k, coords)

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

// portfolioURLWritable is the OWNER's own, WRITE-CAPABLE portfolio link
// (ready-f947). `rd board` mints a link for the owner to use, and the owner
// wants to write from the browser with no extension, so this shape carries the
// signing secret in sk= (not just the public pk=). keys= still carries the read
// keys for decryption. sk= goes FIRST for the same reason pk= did: a fragment
// truncated in transit loses its tail (the big keys= blob) and fails loudly
// rather than silently dropping the identity. This link SIGNS as well as reads —
// a write bearer credential — so its warning (portfolioKeyWarning) says so.
func portfolioURLWritable(host, secret string, relays []string, blob string) string {
	parts := []string{"sk=" + url.QueryEscape(secret)}
	if len(relays) > 0 {
		parts = append(parts, "relays="+url.QueryEscape(strings.Join(relays, ",")))
	}
	if blob != "" {
		parts = append(parts, "keys="+blob)
	}
	return host + "#" + strings.Join(parts, "&")
}

// runBoardPortfolio is what bare `rd board` runs (ready-1df): ONE URL covering
// every board this key can read, carrying their read keys.
//
// With --no-key (withKey == false) it prints only the public shape (pk= +
// relays=), which is still useful — the page opens the viewer's whole board list
// with no npub to paste — and carries no secret whatsoever. Otherwise it also
// carries the keys= blob and warns, on stderr, that the link is a
// portfolio-wide bearer credential.
//
// The URL goes to stdout and everything else to stderr, so `rd board | pbcopy`
// copies exactly the URL while the human still reads the warning.
func runBoardPortfolio(cmd *cobra.Command, dir string, withKey, strict, allowPartial bool) error {
	host := boardHost(cmd)
	errOut := cmd.ErrOrStderr()
	// ready-634: only relays a browser on this host's origin can actually open.
	relays := boardLinkRelays(errOut, host)

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
		fmt.Fprintln(errOut, "NOTE: no keys embedded (--no-key) — this link opens every board you can see, but a CONFIDENTIAL board's titles will show as placeholders unless the browser has a NIP-07 extension holding your key. Drop --no-key to embed them (the link then becomes a bearer credential for your whole portfolio).")
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

	// THE GATE (ready-4d9, re-scoped by ready-1df). A relay that NEVER ANSWERED
	// cannot produce this link by default. The boards behind a whole-portfolio
	// credential are a SET the link implies it covers; nothing is known about what
	// an unanswered relay held, so boards known only to it are missing and NOTHING
	// in the printed URL shows it. --allow-partial is the explicit, informed way
	// through (the offline owner still gets a link, and gets told what it is), and
	// it is NAMED HERE so nobody has to know it exists in advance.
	//
	// A relay that answered SHORT does not reach this branch by default — see
	// lostRelay's doc for why the two are different facts, and --strict below for
	// the stricter reading.
	//
	// This gate is a FLOOR, not a proof of completeness, and the wording below is
	// bounded accordingly — see portfolioGather's doc for the ceiling no gate here
	// can reach.
	if gather.lostRelay() && !allowPartial {
		return fmt.Errorf(`rd board: INCOMPLETE BOARD GATHER — refusing to mint a link that would look whole and is not.
  Shortfall: %s
  %d of %d read relays answered. This key holds read keys for %d board(s) from what this read could see, but any board known only to the relay(s) above is missing, and the printed URL would not show that.
  Fix the relay and re-run, or pass --allow-partial to mint the narrower link on purpose (its warning then says it is partial and names what was missed)`,
			gather.reason(), gather.answered(), gather.asked, keys.boardCount())
	}
	// --strict is the opt-in to the OLD, stricter reading: refuse on anything the
	// gather could detect, including a publishing gap. It is for someone who wants
	// a link only when nothing at all fell short; it is not the default, because
	// for the owner it fires on every invocation over boards that are simply not
	// on that relay and never will be.
	if strict && !gather.complete() {
		return fmt.Errorf(`rd board --strict: PUBLISHING GAP — refusing to mint because a relay had not been given every board this read proved exists.
  Shortfall: %s
  The keys for those boards ARE in this gather (%d board(s)) — they came from the local log or another relay — so the link would carry them; what fell short is the relay's copy, and their cards would be empty in the browser. Back-fill that relay and re-run, or drop --strict to mint anyway (the warning then names the gap)`,
			gather.reason(), keys.boardCount())
	}

	blob := ""
	if keys.carriesSecret() {
		if blob, err = keys.blob(); err != nil {
			return err
		}
	}
	// ready-f947: `rd board` is the OWNER's own link, and the owner writes from
	// the browser with no extension, so the default carries the SIGNING SECRET in
	// sk= — a write bearer credential. `rd board --no-key` remains the read-only
	// public shape (pk=, no keys) for sharing.
	k, kerr := nostrKey()
	if kerr != nil {
		return kerr
	}
	fmt.Println(portfolioURLWritable(host, k.SecretHex(), relays, blob))

	// ready-f947: the default link ALWAYS carries sk= now, so it is ALWAYS a write
	// bearer credential — the warning fires even when no read keys are embedded.
	switch {
	case keys.carriesSecret():
		fmt.Fprintln(errOut, portfolioKeyWarning(keys.boardCount(), gather))
	case gather.lostRelay():
		fmt.Fprintln(errOut, "WARNING: this link is PARTIAL and CARRIES YOUR SIGNING KEY (sk=) — anyone who opens it can MAKE CHANGES AS YOU (a WRITE bearer credential). No read keys are embedded, but "+gather.shortfallSummary()+", so this is not a statement that you hold no read keys; boards known only to that relay were never seen. Re-run when the relay is reachable and serving in full. Treat it like a password. (For a read-only link to share, use `rd board --no-key`.)")
	default:
		fmt.Fprintln(errOut, "WARNING: this link CARRIES YOUR SIGNING KEY (sk=) — anyone who opens it can MAKE CHANGES AS YOU on every board (a WRITE bearer credential). No read keys are embedded (this key holds none for any confidential board, so there is nothing to decrypt). Treat it like a password. (For a read-only link to share, use `rd board --no-key`.)")
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
// from a dead relay by an owner who has 47. So the partial branch says the
// opposite, in the same breath as the count, and names the relay whose boards are
// missing.
//
// THE PUBLISHING-GAP CASE SHARES THE CLEAN WORDING, DELIBERATELY (ready-1df). A
// relay that answered short cost the LINK nothing — every board it failed to
// serve is one this read already holds from the local log or another relay, so
// it travels in the blob either way — and the blast radius really is the whole
// portfolio. What differs is the SCOPE CLAUSE, which names the relay that is
// behind and says the cards will be empty in a browser until it is back-filled.
// It must NOT say PARTIAL: the link is not narrower, and calling it partial would
// train the owner to ignore the word on the one link where it means a board is
// genuinely missing.
//
// AND THE CLEAN BRANCH IS SCOPED TOO. "ENTIRE PORTFOLIO" stays, because as a
// statement of BLAST RADIUS — what the bearer of this link can read — it is the
// conservative direction and the user must not underestimate it. What is NOT
// allowed is for the same sentence to double as a claim that the SET is
// exhaustive: the gather can prove a floor (no relay served short of what the
// read proved exists) and can never prove the ceiling. Hence "ALL %d ... THIS
// GATHER COULD FIND" rather than "ALL %d OF YOUR CONFIDENTIAL BOARDS", and hence
// the scope clause, which says in the link's own warning what was actually
// established and what remains undetectable.
func portfolioKeyWarning(n int, g portfolioGather) string {
	boards := "boards"
	if n == 1 {
		boards = "board"
	}
	if g.lostRelay() {
		return fmt.Sprintf("WARNING: this link is PARTIAL and CARRIES YOUR SIGNING KEY plus the read keys for %d CONFIDENTIAL %s in its fragment — anyone who opens it can read every title on %s AND MAKE CHANGES AS YOU (a WRITE bearer credential, not just read). It is NOT your whole portfolio: %s, so boards known only to there are MISSING from this link and from the count. Re-run when that relay is reachable and serving in full to get the complete link. Treat it like a password — do not paste it into chat, a ticket, or any shared channel. (For a read-only link to share, use `rd board --no-key`.)",
			n, strings.ToUpper(boards), pluralBoards(n), g.shortfallSummary())
	}
	return fmt.Sprintf("WARNING: this link CARRIES YOUR SIGNING KEY plus the read keys for ALL %d CONFIDENTIAL %s THIS GATHER COULD FIND in its fragment — anyone who opens it can read every title in your ENTIRE PORTFOLIO AND MAKE CHANGES AS YOU on every board, not just one board (a WRITE bearer credential). %s Treat it like a password: do not paste it into chat, a ticket, or any shared channel. (For a read-only link to share, use `rd board --no-key`.)",
		n, strings.ToUpper(boards), g.scopeClause())
}

// pluralBoards renders "that 1 board" / "those 7 boards" for the partial warning,
// which cannot fall back on "your entire portfolio" to describe its scope.
func pluralBoards(n int) string {
	if n == 1 {
		return "that 1 board"
	}
	return fmt.Sprintf("those %d boards", n)
}
