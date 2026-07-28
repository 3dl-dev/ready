package main

// ready-4d9: `rd board --portfolio` — the CLI half of "one link opens the WHOLE
// portfolio decrypted".
//
// WHAT THESE TESTS ARE ABOUT. ready-df0 widened a board link to carry ONE
// board's read key; this widens it to carry EVERY board's. The blast radius grew
// and the boundaries did not move, so the tests are shaped around the boundaries
// rather than the happy path (which the browser suite proves at the DOM,
// web/board/src/main.portfolio.test.ts, and which the live-relay proof in this
// item's audit trail proves against the real relay):
//
//	*  the link reaches boards OUTSIDE this directory — otherwise the whole item
//	   is unbuilt (TestBoardPortfolio_ReachesBoardsBeyondThePinnedOne).
//	*  it never carries a key this machine cannot legitimately derive
//	   (TestBoardPortfolio_NeverEmbedsAKeyThisKeyCannotRead).
//	*  bare `rd board --portfolio` emits ZERO key material on a project where
//	   --with-key demonstrably emits some (TestBoardPortfolio_Default_NoKeyMaterial).
//	*  the warning says PORTFOLIO, not board, and differs from the single-board
//	   one (TestBoardPortfolio_WarningStatesTheWiderBlastRadius).
//	*  `rd board share` emits no key material under ANY argv the real cobra
//	   parser accepts (TestBoardShareCmd_NoArgvEmitsKeyMaterial) — the ready-df0
//	   boundary, re-attacked now that a second key-bearing flag exists.
//
// Every fixture is REAL: real minted CEKs, really NIP-44-wrapped by
// pkg/sync.WrapKey into really-signed kind-39301 grants in the real local log.
// So an assertion that the emitted blob contains a specific key's bytes proves
// the command ran the actual DeriveBoardKeyring unwrap.

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// portfolioEnv builds on boardTestEnv and puts THREE confidential boards in the
// local log:
//
//	pinned    — this directory's pinned board, two epochs (a rotated board).
//	sibling   — another board owned by the SAME key. It is NOT the pinned board
//	            and nothing in this project directory references it, so it is the
//	            board a per-directory command structurally cannot reach. If the
//	            link carries its key, the gather really is portfolio-wide.
//	foreign   — a board owned by a STRANGER, whose CEK-bearing grant is addressed
//	            to the stranger, not to us. Its key must never appear.
//
// The returned keys are the raw CEKs, so the tests assert on the exact bytes
// rather than on "something key-shaped is present".
func portfolioEnv(t *testing.T) (owner *nostr.Key, pinnedCoord, siblingCoord, foreignCoord, dir string, pinned1, pinned2, sibling, foreign [32]byte) {
	t.Helper()
	owner, pinnedD, pinnedCoord, dir := boardTestEnv(t)
	self := owner.PubKeyHex()
	now := time.Now().Unix()

	mint := func(what string) [32]byte {
		t.Helper()
		k, err := rdSync.MintKey()
		if err != nil {
			t.Fatalf("MintKey %s: %v", what, err)
		}
		return k
	}
	pinned1, pinned2, sibling, foreign = mint("pinned1"), mint("pinned2"), mint("sibling"), mint("foreign")

	grant := func(signer *nostr.Key, boardAuthor, boardD, grantee string, cek [32]byte, epoch int, at int64) *nostr.Event {
		t.Helper()
		wrapped, werr := rdSync.WrapKey(signer, grantee, cek)
		if werr != nil {
			t.Fatalf("WrapKey %s: %v", boardD, werr)
		}
		e, gerr := rdSync.BuildRoleGrantEvent(signer, rdSync.RoleGrantSpec{
			BoardD: boardD, BoardAuthor: boardAuthor, Grantee: grantee,
			Role: rdSync.RoleOwner, WrappedCEK: wrapped, CEKEpoch: epoch,
		}, at)
		if gerr != nil {
			t.Fatalf("BuildRoleGrantEvent %s: %v", boardD, gerr)
		}
		return e
	}
	board := func(signer *nostr.Key, boardD string, at int64) *nostr.Event {
		t.Helper()
		e, err := rdSync.BuildBoardEvent(signer, rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{signer.PubKeyHex()}}, at)
		if err != nil {
			t.Fatalf("BuildBoardEvent %s: %v", boardD, err)
		}
		return e
	}

	const siblingD = "sibling-board"
	siblingCoord = rdSync.BoardCoord(self, siblingD)

	stranger, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey stranger: %v", err)
	}
	const foreignD = "foreign-board"
	foreignCoord = rdSync.BoardCoord(stranger.PubKeyHex(), foreignD)

	events := []*nostr.Event{
		board(owner, pinnedD, now),
		board(owner, siblingD, now),
		board(stranger, foreignD, now),
		grant(owner, self, pinnedD, self, pinned1, 1, now+1),
		grant(owner, self, pinnedD, self, pinned2, 2, now+2),
		grant(owner, self, siblingD, self, sibling, 1, now+3),
		// Addressed to the stranger, not to us — a real grant we simply are not
		// the grantee of.
		grant(stranger, stranger.PubKeyHex(), foreignD, stranger.PubKeyHex(), foreign, 1, now+4),
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique(events); err != nil {
		t.Fatalf("AppendUnique: %v", err)
	}
	return owner, pinnedCoord, siblingCoord, foreignCoord, dir, pinned1, pinned2, sibling, foreign
}

// runBoardPortfolioCmd drives the REAL cobra RunE with the real flags, and
// returns stdout and stderr separately (the URL is pipeable; the warning is not
// in the pipe).
func runBoardPortfolioCmd(t *testing.T, withKey bool) (stdout, stderr string) {
	t.Helper()
	out, errOut, err := tryBoardPortfolioCmd(t, withKey, false)
	if err != nil {
		t.Fatalf("rd board --portfolio (--with-key=%v): %v", withKey, err)
	}
	return out, errOut
}

// tryBoardPortfolioCmd is runBoardPortfolioCmd for the paths that are SUPPOSED to
// fail: it returns the error instead of failing the test, and it can set
// --allow-partial. Both are needed by the incomplete-gather tests, where "the
// command returned an error and printed no URL" is the assertion.
func tryBoardPortfolioCmd(t *testing.T, withKey, allowPartial bool) (stdout, stderr string, runErr error) {
	t.Helper()
	setFlag := func(name, value string) {
		if err := boardCmd.Flags().Set(name, value); err != nil {
			t.Fatalf("set --%s: %v", name, err)
		}
	}
	setFlag("portfolio", "true")
	setFlag("with-key", fmt.Sprint(withKey))
	setFlag("allow-partial", fmt.Sprint(allowPartial))
	t.Cleanup(func() {
		_ = boardCmd.Flags().Set("portfolio", "false")
		_ = boardCmd.Flags().Set("with-key", "false")
		_ = boardCmd.Flags().Set("allow-partial", "false")
	})

	var errBuf bytes.Buffer
	boardCmd.SetErr(&errBuf)
	t.Cleanup(func() { boardCmd.SetErr(nil) })

	out := captureStdoutPipe(t, func() {
		runErr = boardCmd.RunE(boardCmd, nil)
	})
	return out, errBuf.String(), runErr
}

// decodedBlob is a board coordinate -> epoch -> CEK view of an emitted keys=
// parameter. Parsing it here — with an INDEPENDENT reader, not by calling the
// encoder — is what lets a test say "the sibling board's real CEK is at epoch 1"
// instead of "the blob is non-empty".
type decodedBlob map[string]map[int][32]byte

// parseKeysBlob is a deliberately separate, minimal implementation of the format
// in pkg/sync/portfolioblob.go. It exists so the Go assertions do not verify the
// encoder against itself; the browser decoder is pinned separately, against the
// committed conformance vectors.
func parseKeysBlob(t *testing.T, b64 string) decodedBlob {
	t.Helper()
	raw, err := base64.RawURLEncoding.DecodeString(b64)
	if err != nil {
		t.Fatalf("keys= is not base64url: %v", err)
	}
	i := 0
	take := func(n int, what string) []byte {
		t.Helper()
		if i+n > len(raw) {
			t.Fatalf("keys= truncated: needed %d more bytes for %s at offset %d of %d", n, what, i, len(raw))
		}
		out := raw[i : i+n]
		i += n
		return out
	}
	if v := take(1, "version")[0]; v != rdSync.PortfolioBlobVersion {
		t.Fatalf("keys= version = %d, want %d", v, rdSync.PortfolioBlobVersion)
	}
	out := decodedBlob{}
	owners := int(take(1, "owner count")[0])
	for o := 0; o < owners; o++ {
		owner := hex.EncodeToString(take(32, "owner pubkey"))
		boards := int(take(1, "board count")[0])
		for b := 0; b < boards; b++ {
			dLen := int(take(1, "d-tag length")[0])
			boardD := string(take(dLen, "d-tag"))
			coord := rdSync.BoardCoord(owner, boardD)
			epochs := int(take(1, "epoch count")[0])
			out[coord] = map[int][32]byte{}
			for e := 0; e < epochs; e++ {
				ep := take(4, "epoch")
				epoch := int(ep[0])<<24 | int(ep[1])<<16 | int(ep[2])<<8 | int(ep[3])
				var cek [32]byte
				copy(cek[:], take(32, "cek"))
				out[coord][epoch] = cek
			}
		}
	}
	if i != len(raw) {
		t.Fatalf("keys= has %d trailing bytes", len(raw)-i)
	}
	return out
}

func portfolioFragment(t *testing.T, out string) url.Values {
	t.Helper()
	line := findURLLine(t, out)
	i := strings.Index(line, "#")
	if i < 0 {
		t.Fatalf("portfolio URL %q has no '#' fragment", line)
	}
	v, err := url.ParseQuery(line[i+1:])
	if err != nil {
		t.Fatalf("portfolio fragment %q did not parse as a query string: %v", line[i+1:], err)
	}
	return v
}

// TestBoardPortfolio_ReachesBoardsBeyondThePinnedOne IS THE ITEM. A link that
// only ever covered this directory's pinned board would satisfy every other test
// in this file and deliver nothing the owner asked for.
func TestBoardPortfolio_ReachesBoardsBeyondThePinnedOne(t *testing.T) {
	owner, pinnedCoord, siblingCoord, _, _, pinned1, pinned2, sibling, _ := portfolioEnv(t)

	out, _ := runBoardPortfolioCmd(t, true)
	v := portfolioFragment(t, out)

	if got := v.Get("pk"); got != owner.PubKeyHex() {
		t.Errorf("fragment pk=%q, want the minting key's pubkey %q", got, owner.PubKeyHex())
	}
	if got := v.Get("board"); got != "" {
		t.Errorf("a portfolio link carries board=%q — the whole point is that it names no single board", got)
	}

	blob := parseKeysBlob(t, v.Get("keys"))
	if len(blob) != 2 {
		t.Fatalf("keys= covers %d board(s) %v, want 2 (the pinned board AND the sibling this directory does not reference)", len(blob), blobCoords(blob))
	}

	// The SIBLING board — the one nothing in this directory points at. Its
	// presence is what distinguishes a portfolio gather from a pinned-board one.
	if got, ok := blob[siblingCoord][1]; !ok || got != sibling {
		t.Errorf("keys= does not carry the sibling board's real CEK at epoch 1 — the gather did not leave this directory's pinned board")
	}
	// And the pinned board, BOTH epochs: a rotated board's older cards need the
	// older key, exactly as in the single-board link.
	if got, ok := blob[pinnedCoord][1]; !ok || got != pinned1 {
		t.Errorf("keys= is missing the pinned board's epoch-1 CEK")
	}
	if got, ok := blob[pinnedCoord][2]; !ok || got != pinned2 {
		t.Errorf("keys= is missing the pinned board's epoch-2 CEK")
	}
}

func blobCoords(b decodedBlob) []string {
	out := make([]string, 0, len(b))
	for c := range b {
		out = append(out, c)
	}
	return out
}

// TestBoardPortfolio_NeverEmbedsAKeyThisKeyCannotRead: "every board" means every
// board THIS KEY CAN READ, and the difference is the whole security boundary.
// The fixture's foreign board is genuinely confidential, its grant is genuinely
// owner-signed and genuinely in this machine's local log — it is simply
// addressed to someone else.
func TestBoardPortfolio_NeverEmbedsAKeyThisKeyCannotRead(t *testing.T) {
	_, _, _, foreignCoord, _, _, _, _, foreign := portfolioEnv(t)

	out, _ := runBoardPortfolioCmd(t, true)
	v := portfolioFragment(t, out)
	blob := parseKeysBlob(t, v.Get("keys"))

	if _, present := blob[foreignCoord]; present {
		t.Errorf("keys= claims board %s, which this key holds no read key for", foreignCoord)
	}
	// Byte level: not smuggled under another coordinate either.
	for coord, epochs := range blob {
		for epoch, cek := range epochs {
			if cek == foreign {
				t.Errorf("the foreign board's CEK appears at %s epoch %d", coord, epoch)
			}
		}
	}
	assertNoKeyHex(t, out, foreign)
	// Raw bytes, not just hex: this blob is base64, so a hex scan alone would
	// miss a key that really is in there.
	if raw, err := base64.RawURLEncoding.DecodeString(v.Get("keys")); err == nil {
		if bytes.Contains(raw, foreign[:]) {
			t.Error("the foreign board's raw CEK bytes are inside the keys= blob")
		}
	}
}

// TestBoardPortfolio_Default_NoKeyMaterial IS THE GATE. It runs on a project
// where --with-key demonstrably DOES emit keys, so a regression that made key
// embedding the default would be caught here.
func TestBoardPortfolio_Default_NoKeyMaterial(t *testing.T) {
	_, _, _, _, _, pinned1, pinned2, sibling, _ := portfolioEnv(t)

	// Not vacuous: the same project with --with-key really does emit keys.
	withOut, _ := runBoardPortfolioCmd(t, true)
	if portfolioFragment(t, withOut).Get("keys") == "" {
		t.Fatal("precondition failed: --portfolio --with-key emitted no keys on a project with readable confidential boards")
	}

	out, errOut := runBoardPortfolioCmd(t, false)
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("`rd board --portfolio` printed %d line(s) to stdout, want exactly 1 URL:\n%s", len(lines), out)
	}
	v := portfolioFragment(t, out)
	for _, param := range []string{"keys", "cek", "ltk"} {
		if got := v.Get(param); got != "" {
			t.Errorf("`rd board --portfolio` fragment carries %s=%q — key material must be opt-in only", param, got)
		}
	}
	assertNoKeyHex(t, out, pinned1, pinned2, sibling)
	// The keyless portfolio link is not a bearer credential, so it must not carry
	// the bearer-credential warning; it should still explain why titles may be
	// sealed.
	if strings.Contains(errOut, "WARNING") {
		t.Errorf("a key-free portfolio link printed the bearer-credential warning; stderr = %q", errOut)
	}
	if !strings.Contains(errOut, "no keys embedded") {
		t.Errorf("stderr does not explain that no keys were embedded; stderr = %q", errOut)
	}
}

// TestBoardPortfolio_FragmentParamAllowlist is the least-privilege gate: the
// whole-portfolio link carries EXACTLY three parameters. A would-be fourth
// (a returning ltk=, a board=, anything) fails here until someone edits this list
// on purpose.
func TestBoardPortfolio_FragmentParamAllowlist(t *testing.T) {
	portfolioEnv(t)

	out, _ := runBoardPortfolioCmd(t, true)
	v := portfolioFragment(t, out)

	allowed := map[string]bool{"pk": true, "relays": true, "keys": true}
	for k := range v {
		if !allowed[k] {
			t.Errorf("portfolio fragment carries unexpected parameter %q — every parameter in a key-bearing link must be a deliberate choice with a consumer", k)
		}
	}
	if got := v.Get("ltk"); got != "" {
		t.Errorf("portfolio fragment carries ltk=%q — the label-token key has NO reader in web/board", got)
	}
	// The blob is LAST in the fragment. Truncation then eats blob bytes (which
	// the decoder rejects loudly) rather than pk=/relays= (which would leave an
	// unrecognizable fragment and only a bare login form). See portfolioURL.
	frag := findURLLine(t, out)
	frag = frag[strings.Index(frag, "#")+1:]
	if !strings.Contains(frag, "&keys=") || strings.Index(frag, "keys=") < strings.Index(frag, "pk=") {
		t.Errorf("keys= is not the LAST fragment parameter: %q", frag)
	}
}

// TestBoardPortfolio_WarningStatesTheWiderBlastRadius. A user who has internalized
// the single-board warning must not apply single-board judgement to a
// portfolio-wide credential, so the two lines have to differ — and the difference
// has to be about SCOPE, not phrasing.
func TestBoardPortfolio_WarningStatesTheWiderBlastRadius(t *testing.T) {
	portfolioEnv(t)

	_, errOut := runBoardPortfolioCmd(t, true)
	notice := strings.TrimSpace(errOut)

	if notice == "" {
		t.Fatal("`rd board --portfolio --with-key` said NOTHING about the link carrying keys")
	}
	if n := len(strings.Split(notice, "\n")); n != 1 {
		t.Errorf("the key notice is %d lines, want exactly 1:\n%s", n, notice)
	}
	if notice == boardKeyWarning {
		t.Fatal("the portfolio warning is byte-identical to the single-board warning — the blast radius differs, so the words must")
	}
	if !strings.Contains(notice, "WARNING") {
		t.Errorf("the portfolio notice is not marked as a warning; notice = %q", notice)
	}
	lower := strings.ToLower(notice)
	for _, want := range []string{"portfolio", "every title", "not just one board"} {
		if !strings.Contains(lower, want) {
			t.Errorf("the portfolio warning does not say %q — it must state that this covers every board, not one; notice = %q", want, notice)
		}
	}
	// The COUNT: "the read keys for 2 boards" tells the user what they hold in a
	// way "the read keys" does not. The fixture has exactly 2 readable boards.
	if !strings.Contains(notice, " 2 ") {
		t.Errorf("the portfolio warning does not name how many boards' keys it carries (2 in this fixture); notice = %q", notice)
	}
	// And the SINGLE-BOARD warning is still the one the single-board link uses —
	// the two did not collapse into each other.
	if !strings.Contains(boardKeyWarning, "this board") {
		t.Errorf("boardKeyWarning no longer scopes itself to one board: %q", boardKeyWarning)
	}
}

// TestBoardPortfolio_NoReadableBoards_EmbedsNothing: a key with no confidential
// boards it can read gets a link with no keys= at all, and is told why rather
// than left to wonder.
func TestBoardPortfolio_NoReadableBoards_EmbedsNothing(t *testing.T) {
	boardTestEnv(t) // no confidential bootstrap: no CEK-bearing grants anywhere

	out, errOut := runBoardPortfolioCmd(t, true)
	v := portfolioFragment(t, out)

	if got := v.Get("keys"); got != "" {
		t.Errorf("keys=%q emitted for a key that can read no confidential board", got)
	}
	if got := v.Get("pk"); got == "" {
		t.Error("pk= is missing — it is public, and it is what opens the page with nothing to paste")
	}
	if !strings.Contains(errOut, "no keys embedded") {
		t.Errorf("stderr does not explain that no keys were embedded; stderr = %q", errOut)
	}
	if strings.Contains(errOut, "WARNING") {
		t.Errorf("a key-free link must not print the bearer-credential warning; stderr = %q", errOut)
	}
}

// TestBoardShareCmd_NoArgvEmitsKeyMaterial re-attacks ready-df0's boundary now
// that a SECOND key-bearing flag exists. Third-party read access stays the
// owner-signed, per-grantee, rotation-revocable kind-39301 grant; a bearer link
// is not an acceptable substitute at one board and is a worse one at 24.
//
// This drives the REAL cobra parser from the ROOT command with real argv —
// `rd board share ...` exactly as a user types it — because that is the layer a
// user actually reaches: `boardShareCmd.RunE` being safe says nothing about
// whether cobra would accept `--with-key` and route it somewhere. Each argv is
// asserted to EITHER fail parsing (the flag does not exist on this subcommand)
// OR produce a REAL share link (positive control, below) that contains no key
// material and no key-bearing parameter.
//
// Driving it from the root is not a detail. An earlier cut of this test called
// boardShareCmd.Execute(), which cobra redirects to Root().ExecuteC() with the
// root's own args — so every case silently ran `rd --help` and asserted that the
// help text contains no CEK. It passed, and proved nothing. The positive control
// below (every accepted argv must print an actual board URL) is what makes that
// class of vacuity impossible rather than merely unlikely.
//
// The structural guard is NOT the flags' absence — a future flag could be added
// in one line. It is the hardcoded nil `keys` argument in boardShareCmd.RunE's
// call to ownBoardURL, which makes the share path incapable of rendering key
// material regardless of what flags exist. The flag checks below are the outer
// fence; the nil is the wall.
func TestBoardShareCmd_NoArgvEmitsKeyMaterial(t *testing.T) {
	_, _, _, _, _, pinned1, pinned2, sibling, foreign := portfolioEnv(t)

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pub := grantee.PubKeyHex()

	// Flags that could plausibly be typed at a share command by someone who read
	// `rd board --help`, in every combination and both spellings cobra accepts.
	argvs := [][]string{
		{},
		{pub},
		{"--with-key"},
		{"--with-key=true"},
		{"--with-key", pub},
		{"--with-key=true", pub},
		{"--portfolio"},
		{"--portfolio=true"},
		{"--portfolio", pub},
		{"--portfolio=true", pub},
		{"--portfolio", "--with-key"},
		{"--portfolio=true", "--with-key=true"},
		{"--portfolio", "--with-key", pub},
		{"--with-key", "--portfolio", pub},
		{"--role", "owner", "--with-key", pub},
		{"--role", "owner", "--portfolio", pub},
		{"--label", "x", "--with-key=true", pub},
		{"--host", "https://example.test", "--with-key", pub},
		{"--host", "https://example.test", "--portfolio", "--with-key", pub},
		{"--ttl", "1h", "--with-key"},
		// ready-4d9 follow-up: --allow-partial is the THIRD flag on `rd board`
		// that touches the key-bearing path. It relaxes a completeness guard, so
		// the attack surface it adds gets the same treatment as the other two.
		{"--allow-partial"},
		{"--allow-partial=true"},
		{"--allow-partial", pub},
		{"--portfolio", "--with-key", "--allow-partial"},
		{"--portfolio", "--with-key", "--allow-partial", pub},
		{"--allow-partial", "--with-key", "--portfolio", pub},
		{"--host", "https://example.test", "--portfolio", "--with-key", "--allow-partial", pub},
	}

	accepted := 0
	for _, argv := range argvs {
		argv := argv
		t.Run(strings.Join(append([]string{"share"}, argv...), " "), func(t *testing.T) {
			var errBuf bytes.Buffer
			rootCmd.SetErr(&errBuf)
			rootCmd.SetOut(&errBuf)
			t.Cleanup(func() {
				rootCmd.SetErr(nil)
				rootCmd.SetOut(nil)
				rootCmd.SetArgs(nil)
				// Cobra remembers flags it parsed; reset the ones this loop sets so
				// a later case cannot inherit a previous one's --host/--role.
				_ = boardShareCmd.Flags().Set("host", "")
				_ = boardShareCmd.Flags().Set("role", rdSync.RoleContributor)
				_ = boardShareCmd.Flags().Set("label", "")
			})

			var runErr error
			out := captureStdoutPipe(t, func() {
				rootCmd.SetArgs(append([]string{"board", "share"}, argv...))
				runErr = rootCmd.Execute()
			})
			if runErr != nil {
				// Rejected by the parser (or by the command). Nothing was emitted;
				// that is the strongest possible outcome for this test.
				return
			}
			accepted++

			// POSITIVE CONTROL. An accepted argv must have printed a real share
			// link — either the known-key board URL or the bare claim-nonce one.
			// Without this, a command that printed help (or nothing) would sail
			// through every "contains no key" assertion below.
			line := findURLLine(t, out)
			if !strings.Contains(line, "#board=") && !strings.Contains(line, "#rd1_") {
				t.Fatalf("`rd board share %s` was accepted but printed no share link: %q", strings.Join(argv, " "), line)
			}

			// Accepted: then whatever it printed must contain no key material at all.
			assertNoKeyHex(t, out+errBuf.String(), pinned1, pinned2, sibling, foreign)
			for _, param := range []string{"cek=", "ltk=", "keys=", "pk="} {
				if strings.Contains(out, param) {
					t.Errorf("`rd board share %s` emitted %q — a share link must never convey a key or an identity to open as:\n%s", strings.Join(argv, " "), param, out)
				}
			}
		})
	}

	// A parser that rejected EVERY argv would make the loop above vacuous.
	if accepted == 0 {
		t.Fatal("every argv was rejected — this test proved nothing about what `rd board share` emits")
	}

	// And the flags really are absent from this subcommand, named explicitly so
	// the reason is legible at the failure site.
	for _, flag := range []string{"with-key", "portfolio", "allow-partial"} {
		if f := boardShareCmd.Flags().Lookup(flag); f != nil {
			t.Errorf("`rd board share` has a --%s flag — third-party read access must stay the owner-signed kind-39301 grant, never a key in a URL", flag)
		}
	}
}

// TestBoardPortfolio_Help_DocumentsTheWiderCredential: --help must not sell
// --portfolio without saying what it costs, in the same way ready-df0 required
// of --with-key.
func TestBoardPortfolio_Help_DocumentsTheWiderCredential(t *testing.T) {
	lower := strings.ToLower(boardCmd.Long)
	for _, want := range []string{"--portfolio", "entire portfolio", "bearer\ncredential"} {
		if !strings.Contains(lower, want) && !strings.Contains(strings.ReplaceAll(lower, "\n", " "), strings.ReplaceAll(want, "\n", " ")) {
			t.Errorf("rd board --help does not say %q about the portfolio link; Long =\n%s", want, boardCmd.Long)
		}
	}
	usage := boardCmd.Flags().Lookup("portfolio").Usage
	if !strings.Contains(strings.ToLower(usage), "every board") {
		t.Errorf("--portfolio flag usage does not say it covers every board: %q", usage)
	}
}
