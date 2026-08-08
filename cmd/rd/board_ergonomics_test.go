package main

// ready-1df + ready-634: the two things that were wrong with the link `rd board`
// prints — how much you had to type to get one, and what was inside it.
//
// ready-1df — FOUR FLAGS TO OPEN YOUR OWN BOARD. The command was
// `rd board --portfolio --with-key --allow-partial`. Each flag was added by a
// different agent solving a different, individually defensible security concern;
// nobody read the composed surface. The outcome asserted here is the one the
// owner asked for: he types `rd board` and gets a link that opens his whole
// portfolio, decrypted. The tests below drive the REAL cobra parser from the root
// command with real argv, because "RunE does the right thing" says nothing about
// what a user can actually type.
//
// ready-634 — A LINK FULL OF RELAYS THE BROWSER REFUSES. Every link carried
// ws://192.168.2.40:7777 and ws://192.168.2.41:7777 alongside the one wss:// relay
// that worked. The page is served over https, and a browser BLOCKS an insecure
// WebSocket from a secure origin as mixed content — no click-through, no user
// override. Two thirds of every printed link was unopenable by the only client
// the link is for. The tests below assert on what the REAL commands print, and
// they are mutation-proof in the way ready-634 asked for: the fixture CONFIGURES
// ws:// relays, so a build that stops filtering puts them straight back into the
// asserted string, and every one of these tests goes red.
//
// AND THE OTHER HALF OF READY-634, WHICH IS EASY TO BREAK BY "FIXING" THE FIRST:
// the CLI's own sync must still use those ws:// LAN relays. They are fast and
// local and that is why they exist.
// TestBoardShare_PublishesOverWsWhileOmittingItFromTheLink proves both facts from
// ONE invocation — the grant really lands on a ws:// relay while that same
// command's link does not name it.

import (
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/pflag"
)

// stored returns a snapshot of everything a storingRelay has had PUBLISHED to it.
// It is the witness that the CLI's own write path really reached a ws:// relay.
func (r *storingRelay) stored() []*nostr.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*nostr.Event(nil), r.events...)
}

// runRootBoardArgv drives the REAL root command with real argv — `rd board ...`
// exactly as a user types it — and returns stdout plus the error.
//
// Driving from the ROOT is not a detail. `rd board` having the right default in
// RunE says nothing about whether cobra accepts the argv a user types, and an
// earlier test in this package was caught asserting against `rd --help` because
// it drove a subcommand's Execute(). Every flag boardCmd defines is restored
// afterwards, since cobra remembers what it parsed onto a package-level command.
func runRootBoardArgv(t *testing.T, argv ...string) (stdout string, runErr error) {
	t.Helper()
	var sink strings.Builder
	rootCmd.SetErr(&sink)
	rootCmd.SetOut(&sink)
	defaults := map[string]string{}
	boardCmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) { defaults[f.Name] = f.DefValue })
	t.Cleanup(func() {
		rootCmd.SetErr(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
		for name, def := range defaults {
			_ = boardCmd.Flags().Set(name, def)
		}
	})
	out := captureStdoutPipe(t, func() {
		rootCmd.SetArgs(append([]string{"board"}, argv...))
		runErr = rootCmd.Execute()
	})
	return out, runErr
}

// TestBoardCmd_BareCommand_IsTheWholePortfolioDecrypted IS READY-1DF. Everything
// else in this package can pass while the owner still has to type four flags to
// look at his own work.
//
// It is driven through the REAL cobra parser with the literal argv `board` — no
// flag set by a helper, nothing reached through RunE directly — so what it proves
// is what a user typing `rd board` actually gets.
func TestBoardCmd_BareCommand_IsTheWholePortfolioDecrypted(t *testing.T) {
	owner, pinnedCoord, siblingCoord, _, _, pinned1, pinned2, sibling, _ := portfolioEnv(t)

	out, err := runRootBoardArgv(t)
	if err != nil {
		t.Fatalf("`rd board` with no flags failed: %v", err)
	}

	// STDOUT IS EXACTLY THE URL. `rd board | pbcopy` has to copy a link and
	// nothing else; the warning belongs on stderr.
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("`rd board` printed %d line(s) to stdout, want exactly 1 URL:\n%s", len(lines), out)
	}
	line := lines[0]
	if !strings.HasPrefix(line, "https://") {
		t.Fatalf("`rd board` stdout %q is not a URL", line)
	}

	v := portfolioFragment(t, out)
	// PORTFOLIO, not one board: sk= with no board= is the shape that means "open
	// everything this viewer can see AND write to it" (ready-f947: `rd board` is
	// the owner's own write-capable link).
	if got := v.Get("sk"); got != owner.SecretHex() {
		t.Errorf("fragment sk=%q, want the minting key's secret %q", got, owner.SecretHex())
	}
	if got := v.Get("board"); got != "" {
		t.Errorf("`rd board` carries board=%q — the default must be the whole portfolio, not this directory's board", got)
	}

	// DECRYPTED: the real CEKs, for a board OUTSIDE this directory as well as the
	// pinned one. Asserting the exact minted bytes is what proves the command ran
	// the real unwrap rather than echoing something off a tag.
	blob := parseKeysBlob(t, v.Get("keys"))
	if got, ok := blob[siblingCoord][1]; !ok || got != sibling {
		t.Errorf("`rd board` does not carry the sibling board's real CEK — the default did not leave this directory")
	}
	if got, ok := blob[pinnedCoord][1]; !ok || got != pinned1 {
		t.Errorf("`rd board` is missing the pinned board's epoch-1 CEK")
	}
	if got, ok := blob[pinnedCoord][2]; !ok || got != pinned2 {
		t.Errorf("`rd board` is missing the pinned board's epoch-2 CEK")
	}

	// AND IT IS NOT SILENT ABOUT IT. With minting as the default, this warning is
	// the whole of what keeps a bearer credential from being handed over without
	// the user noticing.
	if !strings.Contains(warnOf(t), "WARNING") {
		t.Errorf("`rd board` minted a key-bearing link and said nothing on stderr; stderr = %q", warnOf(t))
	}
}

// warnOf re-runs the bare command with stderr captured, so the assertion above
// can be about stderr without giving up the real-argv drive on stdout (cobra
// writes RunE's stderr through cmd.ErrOrStderr, which the root-level SetErr does
// not redirect for a Println to a captured buffer).
func warnOf(t *testing.T) string {
	t.Helper()
	_, errOut, err := tryBoardCmd(t, boardFlags{})
	if err != nil {
		t.Fatalf("rd board: %v", err)
	}
	return errOut
}

// TestBoardCmd_OldFourFlagInvocationIsGone is the other half of ready-1df, and it
// is a REJECTION test rather than a convenience one.
//
// Leaving --portfolio and --with-key in place as silent aliases would have made
// this item cosmetic: the four-token command would still work, so nothing would
// force the composed surface to stay small, and the next agent adding a flag
// would find three defaults to compose with instead of a decision to read. The
// old spelling must FAIL, loudly, at the parser.
func TestBoardCmd_OldFourFlagInvocationIsGone(t *testing.T) {
	for _, argv := range [][]string{
		{"--portfolio"},
		{"--with-key"},
		{"--portfolio", "--with-key"},
		{"--portfolio", "--with-key", "--allow-partial"},
	} {
		argv := argv
		t.Run(strings.Join(argv, " "), func(t *testing.T) {
			portfolioEnv(t)
			out, err := runRootBoardArgv(t, argv...)
			if err == nil {
				t.Fatalf("`rd board %s` was accepted — the removed opt-ins must fail at the parser, not linger as aliases:\n%s", strings.Join(argv, " "), out)
			}
			if !strings.Contains(err.Error(), "unknown flag") {
				t.Errorf("`rd board %s` failed for some reason other than the flag not existing; error = %v", strings.Join(argv, " "), err)
			}
		})
	}

	// ANTI-TAUTOLOGY: the parser is not simply rejecting everything. The bare
	// command and each surviving escape hatch are accepted.
	for _, argv := range [][]string{{}, {"--no-key"}, {"--this-board"}, {"--this-board", "--no-key"}, {"--strict"}, {"--allow-partial"}} {
		argv := argv
		t.Run("accepted: "+strings.Join(append([]string{"rd board"}, argv...), " "), func(t *testing.T) {
			portfolioEnv(t)
			out, err := runRootBoardArgv(t, argv...)
			if err != nil {
				t.Fatalf("`rd board %s` was rejected: %v", strings.Join(argv, " "), err)
			}
			if !strings.Contains(out, "https://") {
				t.Errorf("`rd board %s` printed no link:\n%s", strings.Join(argv, " "), out)
			}
		})
	}
}

// --- ready-634 ---------------------------------------------------------------

// mixedRelayEnv configures the project with the shape that produced the defect:
// two ws:// LAN relays and one wss:// public one, in that order, so the usable
// relay is last exactly as it was in the reported repro.
//
// The ws:// URLs are RFC1918 literals and the wss:// one is a name that does not
// resolve. NOTHING IN THESE TESTS DIALS THEM: every command driven against this
// fixture is a local-log path (--this-board, or --no-key, or the bare
// `rd board share` mint), so the assertions are about the emitted STRING and the
// test is hermetic.
func mixedRelayEnv(t *testing.T) (lan []string, public string) {
	t.Helper()
	lan = []string{"ws://192.168.2.40:7777", "ws://192.168.2.41:7777"}
	public = "wss://relay.example.invalid"
	return lan, public
}

// linkRelays pulls the relays= list out of an emitted board link.
//
// It finds the URL line itself rather than reusing findURLLine, which requires
// an https:// prefix — TestBoardLink_HttpHostKeepsWsRelays deliberately drives an
// http:// board host, and relaxing the shared helper to accommodate it would
// weaken every other test that leans on it.
func linkRelays(t *testing.T, out string) []string {
	t.Helper()
	var line string
	for _, l := range strings.Split(out, "\n") {
		if l = strings.TrimSpace(l); strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "https://") {
			line = l
			break
		}
	}
	if line == "" {
		t.Fatalf("no board link found in output:\n%s", out)
	}
	i := strings.Index(line, "#")
	if i < 0 {
		t.Fatalf("board URL %q has no '#' fragment", line)
	}
	frag := line[i+1:]
	v, err := url.ParseQuery(frag)
	if err != nil {
		t.Fatalf("board fragment %q did not parse as a query string: %v", frag, err)
	}
	raw := v.Get("relays")
	if raw == "" {
		// Distinguish "no relays=" from "relays=" with an empty value, since an
		// empty list is a legitimate outcome of the filter.
		if strings.Contains(frag, "relays=") {
			t.Fatalf("relays= is present but empty in %q", frag)
		}
		return nil
	}
	return strings.Split(raw, ",")
}

// assertBrowserOpenable is the ready-634 predicate, stated once: on an https
// page a browser may open wss:// and nothing else.
func assertBrowserOpenable(t *testing.T, what string, relays []string) {
	t.Helper()
	for _, r := range relays {
		if !strings.HasPrefix(r, "wss://") {
			t.Errorf("%s puts %q in the link — a browser on an https page BLOCKS that as mixed content, with no click-through, so it can never be opened by the client this link is for", what, r)
		}
	}
}

// TestBoardLink_CarriesNoRelayABrowserCannotOpen IS READY-634, across every form
// that emits a browser link.
//
// The fixture configures the ws:// relays deliberately, which is what makes this
// mutation-proof rather than merely green: delete the filter and the asserted
// string contains ws://192.168.2.40:7777 again. The wss:// relay is asserted
// PRESENT in the same breath, so a "fix" that simply emptied relays= — which
// would fall back to the page's own relays.json and look fine in a browser —
// fails too.
func TestBoardLink_CarriesNoRelayABrowserCannotOpen(t *testing.T) {
	lan, public := mixedRelayEnv(t)

	t.Run("rd board (portfolio)", func(t *testing.T) {
		_, _, _, dir := boardTestEnv(t)
		setProjectRelays(t, dir, append(append([]string{}, lan...), public)...)

		// --no-key: the portfolio SHAPE with no relay gather, so the assertion is
		// about the emitted string and nothing dials anything.
		out, _, err := tryBoardCmd(t, boardFlags{noKey: true})
		if err != nil {
			t.Fatalf("rd board --no-key: %v", err)
		}
		got := linkRelays(t, out)
		assertBrowserOpenable(t, "`rd board`", got)
		if len(got) != 1 || got[0] != public {
			t.Fatalf("relays=%v, want exactly the one relay a browser can open (%s)", got, public)
		}
	})

	t.Run("rd board --this-board", func(t *testing.T) {
		_, _, _, dir := boardTestEnv(t)
		setProjectRelays(t, dir, append(append([]string{}, lan...), public)...)

		out, _, err := tryBoardCmd(t, boardFlags{thisBoard: true})
		if err != nil {
			t.Fatalf("rd board --this-board: %v", err)
		}
		got := linkRelays(t, out)
		assertBrowserOpenable(t, "`rd board --this-board`", got)
		if len(got) != 1 || got[0] != public {
			t.Fatalf("relays=%v, want exactly %s", got, public)
		}
	})

	t.Run("rd board share <pubkey>", func(t *testing.T) {
		_, _, _, dir := boardTestEnv(t)
		// A live in-process relay stands in for the LAN pair here, because this
		// form PUBLISHES a grant and a publish to an unroutable address would
		// block. Its URL is still ws://, which is the only property under test.
		relay := newStoringRelay(t)
		t.Cleanup(relay.close)
		setProjectRelays(t, dir, relay.url(), public)

		grantee, err := nostr.GenerateKey()
		if err != nil {
			t.Fatalf("GenerateKey: %v", err)
		}
		out := captureStdoutPipe(t, func() {
			if err := boardShareCmd.RunE(boardShareCmd, []string{grantee.PubKeyHex()}); err != nil {
				t.Fatalf("rd board share <pubkey>: %v", err)
			}
		})
		got := linkRelays(t, out)
		assertBrowserOpenable(t, "`rd board share <pubkey>`", got)
		if len(got) != 1 || got[0] != public {
			t.Fatalf("relays=%v, want exactly %s", got, public)
		}
	})

	// The bare share form is NOT one of these shapes and is deliberately absent
	// from this test — see TestBoardShareToken_IsACliTokenAndKeepsEveryRelay for
	// why applying this predicate to it was a bug rather than extra safety.
}

// TestBoardShareToken_IsACliTokenAndKeepsEveryRelay IS THE REGRESSION.
//
// ready-634's filter was applied to the rd1_ token minted by the bare
// `rd board share`, on the reasoning that the link is "opened by someone else,
// in a browser". The URL is; the token's relay list is not read by any browser.
// web/board's afterLogin hits `fragment.kind === "claim"`, renders
// renderAwaitingAuthorization and RETURNS before any fetchEvents, and
// `payload.relays` has no reader anywhere in web/board/src. The token's only
// consumer is `rd join` — a CLI — which is the route boardShareCmd's own help
// text advertises ("or run 'rd join <token>' with the raw token").
//
// The fixture is ws://-ONLY, which is the shape that made the regression fatal
// rather than cosmetic: under the filter the token carried relays:null and the
// join below had nothing to dial. A build that reintroduces the filter fails
// this test on the first assertion, and TestBoardShareToken_RoundTripsThroughRdJoinOverWs
// fails on what that costs a user.
//
// ANTI-TAUTOLOGY, and the thing this test must not be allowed to "fix": the
// board=/pk= link shapes in TestBoardLink_CarriesNoRelayABrowserCannotOpen are
// still filtered, from this same configured relay set. The two facts hold at
// once because they are about two different readers.
func TestBoardShareToken_IsACliTokenAndKeepsEveryRelay(t *testing.T) {
	lan, public := mixedRelayEnv(t)
	all := append(append([]string{}, lan...), public)
	_, _, _, dir := boardTestEnv(t)
	setProjectRelays(t, dir, all...)

	out, errOut := captureBoardShareArgv(t)
	p, err := decodeNostrClaimToken(extractToken(t, findURLLine(t, out)))
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	if len(p.Relays) != len(all) {
		t.Fatalf("the rd1_ token carries relays=%v, want all %d configured relays — its consumer is `rd join`, a CLI, and a ws:// LAN relay may be the only way a teammate can reach this board", p.Relays, len(all))
	}
	for _, want := range all {
		if !slices.Contains(p.Relays, want) {
			t.Errorf("relay %q was filtered out of a token redeemed by `rd join`; token relays = %v", want, p.Relays)
		}
	}

	// The URL this token rides in is `<host>#rd1_...`: there is no relays=
	// parameter for a browser to read, which is the structural reason the browser
	// filter has nothing to do here.
	frag := findURLLine(t, out)
	if i := strings.Index(frag, "#"); i < 0 || !strings.HasPrefix(frag[i+1:], "rd1_") {
		t.Fatalf("`rd board share` printed %q, want a <host>#rd1_<token> claim URL", frag)
	}
	if got := linkRelays(t, out); got != nil {
		t.Errorf("the bare share URL grew a relays= parameter (%v) — that shape is read by afterLogin's board=/pk= branches, and this link is a claim token that never reaches them", got)
	}

	// AND THE CONTRADICTION IS GONE. This path used to print, in one breath, a
	// NOTE naming relays it had dropped ("The CLI itself still syncs through
	// them.") immediately followed by "this project has no relays configured".
	// Both cannot be true; the second was false. Nothing is dropped now, so
	// neither line has anything to say.
	if strings.Contains(errOut, "no relays configured") {
		t.Errorf("`rd board share` told the owner his project has no relays configured while %d are; stderr = %q", len(all), errOut)
	}
	if strings.Contains(errOut, "not in this link") {
		t.Errorf("`rd board share` reported omitting relays from a token that carries all of them; stderr = %q", errOut)
	}
}

// TestBoardShareToken_RoundTripsThroughRdJoinOverWs is the outcome the item is
// actually about, proved end to end against a LIVE ws:// relay: mint with
// `rd board share`, redeem with `rd join` in a clean $RD_HOME, and read the
// owner's item back.
//
// A unit assertion on the token's relay list would not have caught the shape of
// this regression, because the damage was DOWNSTREAM and silent:
// redeemNostrClaimToken's relay adoption is `if len(p.Relays) > 0`, so an empty
// list skipped it, wrote no rd.json, and the join still printed
// "Joined board … READ-ONLY". The user saw a successful join of a project with
// zero relays and an empty `rd ready`. This test therefore asserts on the JOINED
// MACHINE'S STATE — its rd.json names the relay, and the owner's item is in its
// log — not on the token.
//
// The relay is ws:// only, in-process, and really speaks NIP-01: the join dials
// a real websocket through the production relayInviteMedium.
func TestBoardShareToken_RoundTripsThroughRdJoinOverWs(t *testing.T) {
	ownerKey, boardD, coord, dir := boardTestEnv(t)
	relay := newStoringRelay(t)
	t.Cleanup(relay.close)
	setProjectRelays(t, dir, relay.url()) // ws:// ONLY — the LAN case, exactly

	// The owner publishes one item, so "the join worked" can be read off content
	// rather than off an exit code.
	const wantTitle = "the item a joiner must be able to read"
	ownerLog := rdSync.NewNostrLog(filepath.Join(t.TempDir(), "owner-log.jsonl"))
	ownerPub := &rdSync.Publisher{Key: ownerKey, Log: ownerLog}
	boardSpec := rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{ownerKey.PubKeyHex()}}
	if _, err := ownerPub.PublishItem(nil, &boardSpec, rdSync.CardSpec{
		ItemID: "ready-rt1", Title: wantTitle, Status: "active",
		Priority: "p1", Type: "task", BoardD: boardD, BoardAuthor: ownerKey.PubKeyHex(),
	}, time.Now().Unix()); err != nil {
		t.Fatalf("owner PublishItem: %v", err)
	}
	published, err := ownerLog.ReadAll()
	if err != nil {
		t.Fatalf("reading the owner's log: %v", err)
	}
	relay.seed(published...)

	// MINT — through the real cobra parser, as a user types it.
	out, _ := captureBoardShareArgv(t)
	token := extractToken(t, findURLLine(t, out))

	// REDEEM — clean $RD_HOME, clean project dir, production join path.
	joinBase := t.TempDir()
	joinHome := filepath.Join(joinBase, "rdhome")
	joinDir := filepath.Join(joinBase, "project")
	for _, d := range []string{joinHome, joinDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}
	t.Setenv("RD_HOME", joinHome)
	if err := os.Chdir(joinDir); err != nil {
		t.Fatalf("chdir to the joiner's project: %v", err)
	}
	joinOut := captureStdoutPipe(t, func() {
		if err := joinViaNostrInviteToken(token, false, ""); err != nil {
			t.Fatalf("rd join <token>: %v", err)
		}
	})
	if !strings.Contains(joinOut, "Joined board") {
		t.Fatalf("`rd join` did not report a join:\n%s", joinOut)
	}

	// (1) rd.json EXISTS AND NAMES THE RELAY.
	//
	// The os.Stat is not belt-and-braces: rdconfig.Load returns a ZERO Config and
	// a NIL error when the file is absent, so a Load-only check would read the
	// regression — no rd.json at all — as an empty relay list and could be
	// "satisfied" by a weaker assertion. The file must be there.
	cfgPath := rdconfig.Path(joinHome)
	if _, err := os.Stat(cfgPath); err != nil {
		t.Fatalf("`rd join` reported success but wrote no rd.json at %s (%v) — this is the regression exactly: relay adoption is skipped when the token carries no relays, and the joiner is left with a project it can never sync", cfgPath, err)
	}
	cfg, err := rdconfig.Load(joinHome)
	if err != nil {
		t.Fatalf("the joiner's rd.json is unreadable: %v", err)
	}
	var endpoints []string
	for _, e := range cfg.RelayEndpoints {
		endpoints = append(endpoints, e.URL)
	}
	if !slices.Contains(endpoints, relay.url()) {
		t.Fatalf("the joiner's rd.json names relays %v, not the ws:// relay %q the token was minted against — `rd ready` on this machine can reach nothing", endpoints, relay.url())
	}

	// (2) THE BOARD IS PINNED.
	syncCfg, err := rdconfig.LoadSyncConfig(joinDir)
	if err != nil {
		t.Fatalf("LoadSyncConfig(joiner): %v", err)
	}
	if syncCfg.Board != coord {
		t.Errorf("the joiner pinned board %q, want %q", syncCfg.Board, coord)
	}

	// (3) `rd ready` WORKS. allProjectItems is the exact accessor the ready/list
	// commands read from, driven here against the joiner's $RD_HOME and project
	// dir — so this is the user-visible outcome ("run 'rd ready' to see the
	// project's items now", which the join itself just promised), not a proxy for
	// it. Under the regression this returned nothing.
	items, err := allProjectItems()
	if err != nil {
		t.Fatalf("allProjectItems on the joined machine (what `rd ready` reads): %v", err)
	}
	found := false
	for _, it := range items {
		if it != nil && it.Title == wantTitle {
			found = true
		}
	}
	if !found {
		var titles []string
		for _, it := range items {
			if it != nil {
				titles = append(titles, it.Title)
			}
		}
		t.Fatalf("`rd ready` on the joined machine shows %d item(s) %v and not the owner's %q — nothing came back over the ws:// relay, which is exactly what a join with an empty relay set looks like from the user's side", len(items), titles, wantTitle)
	}
	if relay.reqs() == 0 {
		t.Error("the ws:// relay was never queried — the join did not actually dial the relay in the token")
	}
}

// captureBoardShareArgv runs the bare `rd board share` through the REAL root
// command with real argv, returning stdout and stderr separately.
//
// Driving from the root rather than calling boardShareCmd.RunE is the same
// discipline TestBoardShareCmd_NoArgvEmitsKeyMaterial applies: what a RunE does
// says nothing about what cobra accepts, and stderr is only routed where the
// command's ErrOrStderr() points once the root has wired it.
func captureBoardShareArgv(t *testing.T, argv ...string) (stdout, stderr string) {
	t.Helper()
	var sink strings.Builder
	rootCmd.SetErr(&sink)
	rootCmd.SetOut(&sink)
	defaults := map[string]string{}
	boardShareCmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) { defaults[f.Name] = f.DefValue })
	t.Cleanup(func() {
		rootCmd.SetErr(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
		for name, def := range defaults {
			_ = boardShareCmd.Flags().Set(name, def)
		}
	})
	out := captureStdoutPipe(t, func() {
		rootCmd.SetArgs(append([]string{"board", "share"}, argv...))
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("`rd board share %s`: %v", strings.Join(argv, " "), err)
		}
	})
	return out, sink.String()
}

// TestBoardLink_DroppedRelaysAreNamedOnStderr: a link whose relay list silently
// differs from the configured one is how ready-634 stayed invisible for as long
// as it did — the owner could read the URL and not know which of his relays had
// been left out or why. The note says which, and says it on stderr so the URL
// stays pipeable.
func TestBoardLink_DroppedRelaysAreNamedOnStderr(t *testing.T) {
	lan, public := mixedRelayEnv(t)
	_, _, _, dir := boardTestEnv(t)
	setProjectRelays(t, dir, append(append([]string{}, lan...), public)...)

	out, errOut, err := tryBoardCmd(t, boardFlags{thisBoard: true, noKey: true})
	if err != nil {
		t.Fatalf("rd board --this-board --no-key: %v", err)
	}
	// stdout is still exactly the URL.
	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 1 {
		t.Fatalf("stdout is %d lines, want exactly the URL:\n%s", len(lines), out)
	}
	for _, r := range lan {
		if !strings.Contains(errOut, r) {
			t.Errorf("stderr does not name the omitted relay %q; stderr = %q", r, errOut)
		}
	}
	lower := strings.ToLower(errOut)
	if !strings.Contains(lower, "mixed content") {
		t.Errorf("stderr does not say WHY the relays were omitted; stderr = %q", errOut)
	}
	if !strings.Contains(lower, "cli") {
		t.Errorf("stderr does not say the CLI still uses them, which is the thing a user would otherwise assume was broken; stderr = %q", errOut)
	}
}

// TestBoardLink_HttpHostKeepsWsRelays pins the DERIVATION, which is the whole
// reason ready-634 chose option (a) over a `browser` flag in rd.json.
//
// Mixed-content blocking is a property of the PAGE'S ORIGIN, not of the relay. An
// http:// page may open ws:// freely, which is exactly what web/board's own dev
// loop (`RD_BOARD_HOST=http://localhost:5173`) is. A blanket wss-only rule would
// pass every other test in this file and quietly break local development against
// a LAN relay; this is the test that says the filter is exactly as strict as the
// browser and no stricter.
func TestBoardLink_HttpHostKeepsWsRelays(t *testing.T) {
	lan, public := mixedRelayEnv(t)
	_, _, _, dir := boardTestEnv(t)
	setProjectRelays(t, dir, append(append([]string{}, lan...), public)...)
	t.Setenv("RD_BOARD_HOST", "http://localhost:5173")

	out, errOut, err := tryBoardCmd(t, boardFlags{thisBoard: true, noKey: true})
	if err != nil {
		t.Fatalf("rd board --this-board --no-key: %v", err)
	}
	got := linkRelays(t, out)
	if len(got) != 3 {
		t.Fatalf("an http:// board host dropped relays it did not have to: relays=%v", got)
	}
	for _, want := range append(append([]string{}, lan...), public) {
		found := false
		for _, g := range got {
			if g == want {
				found = true
			}
		}
		if !found {
			t.Errorf("relay %q is missing from a link built for an http:// origin, where no mixed-content rule applies; relays=%v", want, got)
		}
	}
	if errOut != "" {
		t.Errorf("nothing was omitted, so there was nothing to say; stderr = %q", errOut)
	}

	// ANTI-TAUTOLOGY: the SAME fixture under the https default drops them. Without
	// this, a filter that had simply stopped working would pass the block above.
	t.Setenv("RD_BOARD_HOST", "https://board.example.test")
	secure, _, err := tryBoardCmd(t, boardFlags{thisBoard: true, noKey: true})
	if err != nil {
		t.Fatalf("rd board --this-board --no-key (https host): %v", err)
	}
	if got := linkRelays(t, secure); len(got) != 1 || got[0] != public {
		t.Fatalf("the same relay set under an https origin yielded relays=%v, want exactly %s", got, public)
	}
}

// TestBoardShare_PublishesOverWsWhileOmittingItFromTheLink IS READY-634'S
// CONSTRAINT, and it is the one a careless fix breaks: "do not 'fix' this by
// removing the LAN relays from rd.json — the CLI's own sync uses them and they
// are fast and local, that is why they exist."
//
// ONE invocation of `rd board share <pubkey>` establishes both halves at once:
// the kind-39301 grant really lands on the ws:// relay (a real websocket, a real
// EVENT frame, the real publish path), and the link that same command prints does
// not name it. A change that filtered the relay SET rather than the LINK would
// fail the first half; a change that dropped the filter would fail the second.
func TestBoardShare_PublishesOverWsWhileOmittingItFromTheLink(t *testing.T) {
	ownerKey, boardD, _, dir := boardTestEnv(t)
	relay := newStoringRelay(t)
	t.Cleanup(relay.close)
	setProjectRelays(t, dir, relay.url()) // ws:// only — the LAN case, exactly

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	out := captureStdoutPipe(t, func() {
		if err := boardShareCmd.RunE(boardShareCmd, []string{grantee.PubKeyHex()}); err != nil {
			t.Fatalf("rd board share <pubkey>: %v", err)
		}
	})

	// HALF ONE: the CLI really wrote to the ws:// relay.
	var grants []*nostr.Event
	for _, e := range relay.stored() {
		if e != nil && e.Kind == rdSync.KindRoleGrant {
			grants = append(grants, e)
		}
	}
	if len(grants) == 0 {
		t.Fatalf("the ws:// relay received no kind-%d grant — the CLI's own sync stopped using it, which is the thing ready-634 must not break (it received %d event(s))",
			rdSync.KindRoleGrant, len(relay.stored()))
	}
	if !rdSync.InviteGrantValid(grants, ownerKey.PubKeyHex(), boardD, grantee.PubKeyHex()) {
		t.Error("the grant that reached the ws:// relay does not verify for the grantee — the publish path is not the real one")
	}

	// HALF TWO: and the link that same command printed does not name it.
	if got := linkRelays(t, out); len(got) != 0 {
		t.Errorf("the link carries relays=%v, but the only configured relay is ws:// and a browser on an https page cannot open it", got)
	}
	if strings.Contains(out, relay.url()) {
		t.Errorf("the emitted link names the ws:// relay %q:\n%s", relay.url(), out)
	}
}

// TestCLIRelaySet_StillCarriesEveryConfiguredRelay is the unit-level companion to
// the test above: the accessors the CLI's own sync reads from are untouched by
// the browser filter. inviteRelaySet in particular is what `rd invite` ships to a
// teammate who will redeem it with `rd join` over a plain websocket — narrowing
// it would strand a LAN teammate with no way in.
func TestCLIRelaySet_StillCarriesEveryConfiguredRelay(t *testing.T) {
	lan, public := mixedRelayEnv(t)
	all := append(append([]string{}, lan...), public)
	_, _, _, dir := boardTestEnv(t)
	setProjectRelays(t, dir, all...)

	for name, got := range map[string][]string{
		"nostrReadRelays":  nostrReadRelays(),
		"nostrWriteRelays": nostrWriteRelays(),
		"inviteRelaySet":   inviteRelaySet(),
	} {
		if len(got) != len(all) {
			t.Errorf("%s() = %v, want all %d configured relays — the ws:// LAN relays are the CLI's own fast path and must not be filtered", name, got, len(all))
			continue
		}
		for _, want := range all {
			found := false
			for _, g := range got {
				if g == want {
					found = true
				}
			}
			if !found {
				t.Errorf("%s() dropped %q", name, want)
			}
		}
	}

	// And `rd invite`'s token — the CLI-to-CLI path — still ships them all, unlike
	// `rd board share`'s (asserted above).
	token, err := runNostrInvite(time.Hour)
	if err != nil {
		t.Fatalf("rd invite: %v", err)
	}
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("decode invite token: %v", err)
	}
	if len(p.Relays) != len(all) {
		t.Errorf("`rd invite` token relays = %v, want all %d — its token is redeemed by `rd join` in a CLI, which dials ws:// perfectly well and may have no other route to a LAN board", p.Relays, len(all))
	}
}
