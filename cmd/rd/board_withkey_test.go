package main

// ready-df0: `rd board --with-key` — the CLI half of "the owner clicks the link
// and the titles are readable".
//
// WHAT THESE TESTS ARE ABOUT. Embedding a content key in a URL is a deliberate
// widening of what a board link conveys, so the tests here are shaped around the
// BOUNDARIES of that widening rather than around the happy path (which the
// browser suite proves at the DOM: web/board/src/main.fragmentkey.test.ts):
//
//	*  bare `rd board` still emits ZERO key material, on a board that is really
//	   confidential and where the key really is available to embed
//	   (TestBoardCmd_Default_NoKeyMaterial — the gate).
//	*  a key-bearing link carries EXACTLY the four parameters something reads,
//	   and no more (TestBoardCmd_WithKey_FragmentParamAllowlist — least
//	   privilege; read its comment for why the LTK is gone).
//	*  `rd board share`, in BOTH its forms, never embeds key material and has no
//	   flag that could make it (TestBoardShareCmd_NeverEmbedsKeyMaterial — the
//	   boundary the item calls "the single most important" one: third-party read
//	   access stays an owner-signed, per-grantee, revocable kind-39301 grant).
//	*  a key-bearing link SAYS SO on stderr, in one line
//	   (TestBoardCmd_WithKey_AnnouncesThatTheLinkCarriesAKey).
//	*  a board with nothing to decrypt gets no key
//	   (TestBoardCmd_WithKey_NonConfidentialBoard_EmbedsNoKey).
//
// Every fixture below is REAL: real minted CEKs, really NIP-44-wrapped by
// pkg/sync.WrapKey into a really-signed kind-39301 owner grant appended to the
// real local log. So the assertions that the printed hex equals the minted key
// prove the command ran the actual DeriveBoardKeyring unwrap — it cannot pass by
// echoing something it read off a tag.

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// confidentialBoardEnv builds on boardTestEnv and bootstraps the project's
// pinned board as CONFIDENTIAL, exactly the way the owner's own bootstrap does:
// two CEK epochs and one LTK, each NIP-44-wrapped to the owner's own key and
// carried inside owner-signed kind-39301 grants in the local log.
//
// The LTK is still bootstrapped, and the CLI's keyring still derives it, even
// though no link carries it any more — that is the point: the returned `ltk` is
// what TestBoardCmd_WithKey_FragmentParamAllowlist proves is WITHHELD. A fixture
// that stopped minting one would turn that test vacuous.
//
// TWO epochs, not one, because a rotated board is the case a naive
// implementation gets wrong: shipping only the current epoch leaves every
// pre-rotation card sealed in the browser.
func confidentialBoardEnv(t *testing.T) (owner *nostr.Key, coord, dir string, cek1, cek2, ltk [32]byte) {
	t.Helper()
	owner, boardD, coord, dir := boardTestEnv(t)

	var err error
	if cek1, err = rdSync.MintKey(); err != nil {
		t.Fatalf("MintKey cek1: %v", err)
	}
	if cek2, err = rdSync.MintKey(); err != nil {
		t.Fatalf("MintKey cek2: %v", err)
	}
	if ltk, err = rdSync.MintKey(); err != nil {
		t.Fatalf("MintKey ltk: %v", err)
	}

	self := owner.PubKeyHex()
	now := time.Now().Unix()
	board, err := rdSync.BuildBoardEvent(owner, rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{self}}, now)
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}

	grantFor := func(cek [32]byte, epoch int, withLTK bool, at int64) *nostr.Event {
		t.Helper()
		wrappedCEK, werr := rdSync.WrapKey(owner, self, cek)
		if werr != nil {
			t.Fatalf("WrapKey cek epoch %d: %v", epoch, werr)
		}
		spec := rdSync.RoleGrantSpec{
			BoardD:      boardD,
			BoardAuthor: self,
			Grantee:     self,
			Role:        rdSync.RoleOwner,
			WrappedCEK:  wrappedCEK,
			CEKEpoch:    epoch,
		}
		if withLTK {
			wrappedLTK, lerr := rdSync.WrapKey(owner, self, ltk)
			if lerr != nil {
				t.Fatalf("WrapKey ltk: %v", lerr)
			}
			spec.WrappedLTK = wrappedLTK
		}
		e, gerr := rdSync.BuildRoleGrantEvent(owner, spec, at)
		if gerr != nil {
			t.Fatalf("BuildRoleGrantEvent epoch %d: %v", epoch, gerr)
		}
		return e
	}

	events := []*nostr.Event{board, grantFor(cek1, 1, true, now+1), grantFor(cek2, 2, false, now+2)}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique(events); err != nil {
		t.Fatalf("AppendUnique: %v", err)
	}
	return owner, coord, dir, cek1, cek2, ltk
}

// runBoardCmd drives the REAL cobra RunE — the same entry point a real
// invocation takes — and returns stdout and stderr separately. The split matters,
// because the URL goes
// to stdout so it stays pipeable while the key warning goes to stderr so a human
// still reads it.
func runBoardCmd(t *testing.T, withKey bool) (stdout, stderr string) {
	t.Helper()
	if err := boardCmd.Flags().Set("with-key", fmt.Sprint(withKey)); err != nil {
		t.Fatalf("set --with-key: %v", err)
	}
	// boardCmd is a package-level var shared by every test in this package.
	t.Cleanup(func() { _ = boardCmd.Flags().Set("with-key", "false") })

	var errBuf bytes.Buffer
	boardCmd.SetErr(&errBuf)
	t.Cleanup(func() { boardCmd.SetErr(nil) })

	out := captureStdoutPipe(t, func() {
		if err := boardCmd.RunE(boardCmd, nil); err != nil {
			t.Fatalf("rd board (--with-key=%v): %v", withKey, err)
		}
	})
	return out, errBuf.String()
}

// fragmentOf splits the emitted URL and parses its fragment as a query string.
func fragmentOf(t *testing.T, out string) url.Values {
	t.Helper()
	line := findURLLine(t, out)
	i := strings.Index(line, "#")
	if i < 0 {
		t.Fatalf("board URL %q has no '#' fragment", line)
	}
	v, err := url.ParseQuery(line[i+1:])
	if err != nil {
		t.Fatalf("board fragment %q did not parse as a query string: %v", line[i+1:], err)
	}
	return v
}

// TestBoardCmd_WithKey_EmbedsEveryHeldEpoch is done condition 1's CLI half: the
// link carries the key material this key actually holds, unwrapped.
func TestBoardCmd_WithKey_EmbedsEveryHeldEpoch(t *testing.T) {
	owner, coord, _, cek1, cek2, _ := confidentialBoardEnv(t)

	out, _ := runBoardCmd(t, true)
	v := fragmentOf(t, out)

	if got := v.Get("board"); got != coord {
		t.Errorf("fragment board=%q, want %q", got, coord)
	}
	if got := v.Get("pk"); got != owner.PubKeyHex() {
		t.Errorf("fragment pk=%q, want the minting key's pubkey %q", got, owner.PubKeyHex())
	}
	// BOTH epochs, ascending: a board that rotated has cards under epoch 1 that
	// only epoch 1's key opens. Shipping only the current epoch is the bug this
	// asserts against.
	want := fmt.Sprintf("1:%s,2:%s", hex.EncodeToString(cek1[:]), hex.EncodeToString(cek2[:]))
	if got := v.Get("cek"); got != want {
		t.Errorf("fragment cek=%q, want %q (every held epoch, ascending)", got, want)
	}
}

// TestBoardCmd_WithKey_FragmentParamAllowlist is the least-privilege gate on the
// emitted link: a key-bearing URL carries EXACTLY four parameters and every one
// of them has to earn its place.
//
// The LTK is the reason this test is its own function rather than a tail of the
// one above. This command used to embed ltk= — the board's label-token key — and
// it passed review because the CLI legitimately holds it and the browser
// legitimately parses it. What nobody checked was whether anything in the browser
// ever READ it: keyring.ltk() and envelope.labelToken() are called only from
// their own tests. A secret with no consumer is exposure bought for nothing, and
// it rode along in every key-bearing link for exactly as long as the assertion
// said "ltk is present" instead of "only what is used is present".
//
// So the allowlist below is the standing rule, and a would-be sixth parameter
// (or a returning ltk=) fails here until someone edits this list on purpose.
func TestBoardCmd_WithKey_FragmentParamAllowlist(t *testing.T) {
	_, _, _, _, _, ltk := confidentialBoardEnv(t)

	out, _ := runBoardCmd(t, true)
	v := fragmentOf(t, out)

	allowed := map[string]bool{"board": true, "relays": true, "pk": true, "cek": true}
	for k := range v {
		if !allowed[k] {
			t.Errorf("fragment carries unexpected parameter %q — every parameter in a key-bearing link must be a deliberate choice, and must have a consumer", k)
		}
	}
	// Named explicitly, not just excluded by the allowlist, so the reason is
	// legible at the failure site.
	if got := v.Get("ltk"); got != "" {
		t.Errorf("fragment carries ltk=%q — the label-token key has NO reader in web/board (keyring.ltk() and labelToken() are test-only). Re-add emission when a consumer lands, not before.", got)
	}
	// Byte level: not smuggled under another name either. This key is genuinely
	// held by the minting key (the fixture wraps it into the epoch-1 grant), so
	// this assertion is about restraint, not about absence.
	assertNoKeyHex(t, out, ltk)
}

// TestBoardCmd_Default_NoKeyMaterial IS THE GATE (done condition 3). It runs on
// a board that is genuinely confidential and where the CLI genuinely holds the
// keys — so a regression that made embedding the default would be caught here
// and nowhere else. TestBoardURL_NoSecretMaterial's own-board case runs on a
// board with no key material at all and therefore cannot detect it.
func TestBoardCmd_Default_NoKeyMaterial(t *testing.T) {
	_, coord, _, cek1, cek2, ltk := confidentialBoardEnv(t)

	// Not vacuous: --with-key on this same project really does emit the keys.
	withOut, _ := runBoardCmd(t, true)
	if fragmentOf(t, withOut).Get("cek") == "" {
		t.Fatal("precondition failed: --with-key emitted no cek on a confidential board this key can read")
	}

	out, errOut := runBoardCmd(t, false)
	if errOut != "" {
		t.Errorf("bare `rd board` wrote to stderr: %q — the default form has nothing to warn about", errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("bare `rd board` printed %d line(s), want exactly 1 URL:\n%s", len(lines), out)
	}

	v := fragmentOf(t, out)
	for _, param := range []string{"pk", "cek", "ltk"} {
		if got := v.Get(param); got != "" {
			t.Errorf("bare `rd board` fragment carries %s=%q — key material must be opt-in only", param, got)
		}
	}
	if got := v.Get("board"); got != coord {
		t.Errorf("bare `rd board` fragment board=%q, want %q", got, coord)
	}
	// Byte-level: none of the real key hex appears anywhere in the output.
	assertNoKeyHex(t, out, cek1, cek2, ltk)
}

// TestBoardShareCmd_NeverEmbedsKeyMaterial is the boundary ready-df0 calls the
// most important one: sharing with a THIRD PARTY stays grant-based. A CEK in a
// share link would replace a per-grantee, owner-signed, rotation-revocable
// capability with an unrevocable bearer credential, so both share forms are
// checked, and the flag's ABSENCE on the subcommand is checked too — a link
// cannot carry a key if there is no way to ask for one.
func TestBoardShareCmd_NeverEmbedsKeyMaterial(t *testing.T) {
	_, _, _, cek1, cek2, ltk := confidentialBoardEnv(t)

	if f := boardShareCmd.Flags().Lookup("with-key"); f != nil {
		t.Fatal("`rd board share` has a --with-key flag — third-party read access must stay the owner-signed kind-39301 grant, never a key in a URL")
	}

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	knownKey := captureStdoutPipe(t, func() {
		if err := boardShareCmd.RunE(boardShareCmd, []string{grantee.PubKeyHex()}); err != nil {
			t.Fatalf("rd board share <pubkey>: %v", err)
		}
	})
	bare := captureStdoutPipe(t, func() {
		if err := boardShareCmd.RunE(boardShareCmd, nil); err != nil {
			t.Fatalf("rd board share: %v", err)
		}
	})

	for label, out := range map[string]string{
		"rd board share <pubkey>": knownKey,
		"rd board share (bare)":   bare,
	} {
		assertNoKeyHex(t, out, cek1, cek2, ltk)
		line := findURLLine(t, out)
		for _, param := range []string{"cek=", "ltk=", "pk="} {
			if strings.Contains(line, param) {
				t.Errorf("%s: URL %q carries %q — a share link must never convey a key", label, line, param)
			}
		}
	}
}

// TestBoardCmd_WithKey_AnnouncesThatTheLinkCarriesAKey covers done condition 4:
// a user must not be able to paste a key-bearing link somewhere public believing
// it is inert. The announcement is on stderr so `rd board --with-key | pbcopy`
// still copies exactly the URL.
func TestBoardCmd_WithKey_AnnouncesThatTheLinkCarriesAKey(t *testing.T) {
	_, _, _, _, _, _ = confidentialBoardEnv(t)

	out, errOut := runBoardCmd(t, true)

	if lines := strings.Split(strings.TrimSpace(out), "\n"); len(lines) != 1 {
		t.Fatalf("`rd board --with-key` printed %d line(s) to stdout, want exactly 1 URL:\n%s", len(lines), out)
	}
	notice := strings.TrimSpace(errOut)
	if notice == "" {
		t.Fatal("`rd board --with-key` said NOTHING about the link carrying a key — done condition 4")
	}
	if n := len(strings.Split(notice, "\n")); n != 1 {
		t.Errorf("the key notice is %d lines, want exactly 1:\n%s", n, notice)
	}
	lower := strings.ToLower(notice)
	for _, want := range []string{"key", "read"} {
		if !strings.Contains(lower, want) {
			t.Errorf("the key notice does not mention %q; notice = %q", want, notice)
		}
	}
	if !strings.Contains(notice, "WARNING") {
		t.Errorf("the key notice is not marked as a warning; notice = %q", notice)
	}
}

// TestBoardCmd_WithKey_NonConfidentialBoard_EmbedsNoKey covers done condition 6:
// there is nothing to decrypt on a plaintext board, so nothing is embedded — and
// the user is told why rather than being left to wonder where the key went.
func TestBoardCmd_WithKey_NonConfidentialBoard_EmbedsNoKey(t *testing.T) {
	owner, _, coord, _ := boardTestEnv(t) // no confidential bootstrap: no CEK-bearing grants

	out, errOut := runBoardCmd(t, true)
	v := fragmentOf(t, out)

	for _, param := range []string{"cek", "ltk"} {
		if got := v.Get(param); got != "" {
			t.Errorf("non-confidential board emitted %s=%q — there is nothing to decrypt", param, got)
		}
	}
	if got := v.Get("board"); got != coord {
		t.Errorf("fragment board=%q, want %q", got, coord)
	}
	// pk is PUBLIC and still carried: it is what lets the page open without a
	// paste, and it conveys nothing a reader could not read off the coordinate.
	if got := v.Get("pk"); got != owner.PubKeyHex() {
		t.Errorf("fragment pk=%q, want %q", got, owner.PubKeyHex())
	}
	if !strings.Contains(errOut, "no key embedded") {
		t.Errorf("stderr does not explain that no key was embedded; stderr = %q", errOut)
	}
	if strings.Contains(errOut, "WARNING") {
		t.Errorf("a key-free link must not print the bearer-credential warning; stderr = %q", errOut)
	}
}

// TestBoardCmd_WithKey_FragmentShapeMatchesBrowserParser pins the wire format
// the browser parses (web/board/src/lib/fragment.ts decodeKeyParams). The two
// implementations live in different languages and different test suites, so this
// asserts the exact grammar the TypeScript side accepts against bytes the REAL
// command printed — the same drift-proofing ready-df6 applied to the host.
func TestBoardCmd_WithKey_FragmentShapeMatchesBrowserParser(t *testing.T) {
	_, _, _, _, _, _ = confidentialBoardEnv(t)

	out, _ := runBoardCmd(t, true)
	v := fragmentOf(t, out)

	hex64 := regexp.MustCompile(`^[0-9a-f]{64}$`)
	cekList := regexp.MustCompile(`^[1-9][0-9]*:[0-9a-f]{64}(,[1-9][0-9]*:[0-9a-f]{64})*$`)

	if got := v.Get("cek"); !cekList.MatchString(got) {
		t.Errorf("cek=%q does not match the <epoch>:<64-hex>[,...] grammar fragment.ts parses", got)
	}
	if got := v.Get("pk"); !hex64.MatchString(got) {
		t.Errorf("pk=%q is not 64 lowercase hex characters", got)
	}
	// The fragment marker itself: host directly followed by '#', no slash — the
	// shape ready-df6 pinned and the browser's location.hash handling assumes.
	line := findURLLine(t, out)
	if i := strings.Index(line, "#"); i < 0 || strings.HasSuffix(line[:i], "/") {
		t.Errorf("board URL %q is not <host>#<fragment>", line)
	}
}

// assertNoKeyHex fails if any real key's hex (in either case) appears anywhere in
// the output — the byte-level backstop behind the parameter-name checks, since a
// key smuggled under an innocuous parameter name would pass those.
func assertNoKeyHex(t *testing.T, out string, keys ...[32]byte) {
	t.Helper()
	lower := strings.ToLower(out)
	for i, k := range keys {
		h := hex.EncodeToString(k[:])
		if strings.Contains(lower, h) {
			t.Errorf("output contains key #%d's hex (%s…) — no key material may appear here:\n%s", i, h[:8], out)
		}
	}
}

// TestBoardCmd_Help_DoesNotClaimTheHostedPageIsAPlaceholder is the ALSO-FIX of
// ready-df0, made rot-proof rather than merely corrected.
//
// The Long text used to say the hosted page "is currently a build placeholder;
// the browser-served board UI has not shipped yet, so the URL is not yet useful
// to open — see ready-1ab". That stopped being true when ready-56b shipped the
// board UI, and it is exactly the class of stale user-facing claim ready-df6
// spent five rounds killing: help text that describes a state of the world which
// has moved on, printed with full confidence to every user who runs --help.
//
// The check is a DENYLIST over the rendered help surfaces, because the failure
// mode is a sentence being TRUE-AT-WRITING and FALSE-LATER, and no positive
// assertion catches that. Each needle below is a way of saying "this page does
// not work yet"; a future edit that reintroduces any of them fails here.
func TestBoardCmd_Help_DoesNotClaimTheHostedPageIsAPlaceholder(t *testing.T) {
	surfaces := map[string]string{
		"boardCmd.Long":                   boardCmd.Long,
		"boardCmd.Short":                  boardCmd.Short,
		"boardShareCmd.Long":              boardShareCmd.Long,
		"boardShareCmd.Short":             boardShareCmd.Short,
		"boardCmd --host flag usage":      boardCmd.Flags().Lookup("host").Usage,
		"boardCmd --with-key flag usage":  boardCmd.Flags().Lookup("with-key").Usage,
		"boardShareCmd --host flag usage": boardShareCmd.Flags().Lookup("host").Usage,
	}
	stale := []string{
		"placeholder",
		"has not shipped",
		"not yet useful",
		"not yet shipped",
		"ready-1ab",
		"coming soon",
	}
	for label, text := range surfaces {
		lower := strings.ToLower(text)
		for _, needle := range stale {
			if strings.Contains(lower, needle) {
				t.Errorf("%s still claims the hosted board page is unavailable (%q) — the browser board UI shipped in ready-56b; text =\n%s", label, needle, text)
			}
		}
	}

	// And the flag this item added is documented where a user looks for it.
	if !strings.Contains(boardCmd.Long, "--with-key") {
		t.Errorf("rd board --help does not document --with-key; Long =\n%s", boardCmd.Long)
	}
}

// TestBoardCmd_Help_StatesTheKeyLinkIsABearerCredential: --help must not sell
// --with-key without saying what it costs. The default link's "conveys no read
// access" note is still there for the default link (proven by
// TestBoardCmd_Help_StatesLinkConveysNoReadAccess), so the help text has to hold
// both statements at once without either one silently covering for the other.
func TestBoardCmd_Help_StatesTheKeyLinkIsABearerCredential(t *testing.T) {
	lower := strings.ToLower(boardCmd.Long)
	for _, want := range []string{"bearer credential", "with-key"} {
		if !strings.Contains(lower, want) {
			t.Errorf("rd board --help does not say %q about the key-bearing link; Long =\n%s", want, boardCmd.Long)
		}
	}
	if !strings.Contains(lower, "rd board share") {
		t.Errorf("rd board --help does not point at `rd board share` as the way to share with someone else; Long =\n%s", boardCmd.Long)
	}
}
