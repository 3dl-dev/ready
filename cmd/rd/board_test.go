package main

// ready-384: `rd board` / `rd board share` CLI-entry-point tests.
//
// Deterministic tests (default `go test ./...`, no relay reachable) drive the
// commands the SAME way a real invocation does — via cmd.RunE with a real
// pinned .ready/ project on disk and a real signed-event log — and cover:
//
//   done #1 — each of the three URL forms decodes via decodeNostrClaimToken.
//   done #3 — the emitted URL/token carries NO secret material: a REJECTION
//             test that must fail if a key/secret field is ever added to the
//             payload.
//   done #4 — an expired token and a v2 secret-bearing token are both
//             rejected with the EXISTING actionable messages (already proven
//             by decodeNostrClaimToken's own tests in nostr_invite_test.go;
//             re-asserted here scoped to what `rd board`/`rd board share`
//             actually emit and consume).
//   done #5 — `rd board --help` (Long) states the link conveys no read access
//             on its own for a confidential board.
//
// TestLiveRelay_BoardShare_GrantReadableOnRelay is gated behind
// RD_NOSTR_LIVE_RELAY=1 and proves done #2 against a REAL relay: after
// `rd board share <pubkey>`, InviteGrantValid returns true for events FETCHED
// FROM THE RELAY (not the local log) — the literal done condition ("verified
// against the live relays — --offline is not sufficient").

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// boardTestEnv pins RD_HOME to a fresh sandbox, chdirs into a fresh project
// dir pinned to a board owned by a freshly generated key, and returns the
// owner key + board coordinates. NO relay is configured (no
// RD_NOSTR_RELAY_URL, no rd.json relay_endpoints), so any grant/invite
// publish touches no network — the local authoritative log is the only
// durable write these deterministic tests assert against. The live-relay
// proof of done #2 lives in TestLiveRelay_BoardShare_GrantReadableOnRelay.
func boardTestEnv(t *testing.T) (ownerKey *nostr.Key, boardD, coord, dir string) {
	t.Helper()
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := nostr.SaveKeyFile(nostr.DefaultKeyPath(rdHome), k, rdHome); err != nil {
		t.Fatalf("SaveKeyFile: %v", err)
	}

	dir = filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	boardD = fmt.Sprintf("board384-%d", time.Now().UnixNano())
	coord = rdSync.BoardCoord(k.PubKeyHex(), boardD)
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{Board: coord, ProjectName: boardD}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	return k, boardD, coord, dir
}

// findURLLine scans multi-line command output for the one line that is the
// board URL (some forms — e.g. `rd board share <pubkey>` — also print a
// "published role-grant: ..." confirmation line before the URL).
func findURLLine(t *testing.T, out string) string {
	t.Helper()
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "https://") {
			return line
		}
	}
	t.Fatalf("no https:// URL line found in output:\n%s", out)
	return ""
}

// extractToken pulls the rd1_... token out of a "<host>/#rd1_..." URL, failing
// the test if the URL shape doesn't match (done #1's precondition).
func extractToken(t *testing.T, url string) string {
	t.Helper()
	url = strings.TrimSpace(url)
	i := strings.Index(url, "#"+nostrInviteTokenPrefix)
	if i < 0 {
		t.Fatalf("URL %q does not carry a %q token after a '#' fragment", url, nostrInviteTokenPrefix)
	}
	if !strings.HasPrefix(url, "https://") {
		t.Fatalf("URL %q does not start with https://", url)
	}
	return url[i+1:]
}

// TestBoardCmd_OwnBoard_PrintsDecodableURL covers done #1 for `rd board` (no
// args): it prints exactly one line shaped https://<host>/#rd1_<token>, and
// the token round-trips through decodeNostrClaimToken to the project's own
// pinned board coordinate.
func TestBoardCmd_OwnBoard_PrintsDecodableURL(t *testing.T) {
	_, _, coord, _ := boardTestEnv(t)

	out := captureStdoutPipe(t, func() {
		if err := boardCmd.RunE(boardCmd, nil); err != nil {
			t.Fatalf("rd board: %v", err)
		}
	})
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("rd board printed %d line(s), want exactly 1 URL:\n%s", len(lines), out)
	}
	token := extractToken(t, lines[0])
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("rd board token failed to decode: %v", err)
	}
	if p.Board != coord {
		t.Errorf("rd board token board = %q, want %q", p.Board, coord)
	}
}

// TestBoardShareCmd_Bare_PrintsDecodableClaimURL covers done #1 for
// `rd board share` (no argument): the claim-nonce link for an unknown key.
func TestBoardShareCmd_Bare_PrintsDecodableClaimURL(t *testing.T) {
	_, _, coord, _ := boardTestEnv(t)

	out := captureStdoutPipe(t, func() {
		if err := boardShareCmd.RunE(boardShareCmd, nil); err != nil {
			t.Fatalf("rd board share: %v", err)
		}
	})
	urlLine := findURLLine(t, out)
	token := extractToken(t, urlLine)
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("rd board share (bare) token failed to decode: %v", err)
	}
	if p.Board != coord {
		t.Errorf("rd board share (bare) token board = %q, want %q", p.Board, coord)
	}
	if p.Claim == "" {
		t.Error("rd board share (bare) token has an empty claim-nonce — nothing for `rd grant --claim` to bind")
	}
}

// TestBoardShareCmd_WithPubkey_GrantsAndPrintsURL covers done #1 + the
// mechanism behind done #2 for `rd board share <64-hex-pubkey>`: the grant
// lands in the local authoritative log (the durable write PublishEvents
// always performs before any relay attempt) such that InviteGrantValid — the
// SAME derivation the trust gate uses — returns true for the grantee, and the
// printed URL still decodes.
func TestBoardShareCmd_WithPubkey_GrantsAndPrintsURL(t *testing.T) {
	ownerKey, boardD, coord, dir := boardTestEnv(t)
	granteeKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}
	grantee := granteeKey.PubKeyHex()

	out := captureStdoutPipe(t, func() {
		if err := boardShareCmd.RunE(boardShareCmd, []string{grantee}); err != nil {
			t.Fatalf("rd board share <pubkey>: %v", err)
		}
	})
	urlLine := findURLLine(t, out)
	token := extractToken(t, urlLine)
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("rd board share <pubkey> token failed to decode: %v", err)
	}
	if p.Board != coord {
		t.Errorf("rd board share <pubkey> token board = %q, want %q", p.Board, coord)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("reading local log: %v", err)
	}
	if !rdSync.InviteGrantValid(events, ownerKey.PubKeyHex(), boardD, grantee) {
		t.Error("InviteGrantValid = false after `rd board share <pubkey>` — the grant did not land where the trust gate reads it")
	}
}

// TestBoardShareCmd_WithNpub_ResolvesAndGrants proves `rd board share` accepts
// an npub1... argument (not just bare hex) — resolved via the SAME bech32
// decoder `rd follow` uses (decodeNpub), against the canonical NIP-19 test
// vector also used by TestDecodeNpub_CanonicalVector in follow_test.go.
func TestBoardShareCmd_WithNpub_ResolvesAndGrants(t *testing.T) {
	const npub = "npub1sn0wdenkukak0d9dfczzeacvhkrgz92ak56egt7vdgzn8pv2wfqqhrjdv9"
	const wantHex = "84dee6e676e5bb67b4ad4e042cf70cbd8681155db535942fcc6a0533858a7240"

	ownerKey, boardD, _, dir := boardTestEnv(t)

	captureStdoutPipe(t, func() {
		if err := boardShareCmd.RunE(boardShareCmd, []string{npub}); err != nil {
			t.Fatalf("rd board share <npub>: %v", err)
		}
	})

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("reading local log: %v", err)
	}
	if !rdSync.InviteGrantValid(events, ownerKey.PubKeyHex(), boardD, wantHex) {
		t.Error("InviteGrantValid = false for the npub-resolved pubkey after `rd board share <npub>`")
	}
}

// TestBoardShareCmd_RejectsGarbageArg proves a value that is neither an
// npub1... nor a 64-hex pubkey is refused with an actionable error instead of
// silently minting a claim link or granting a malformed identity.
func TestBoardShareCmd_RejectsGarbageArg(t *testing.T) {
	boardTestEnv(t)
	err := boardShareCmd.RunE(boardShareCmd, []string{"not-a-key"})
	if err == nil {
		t.Fatal("rd board share <garbage> succeeded, want a rejection")
	}
	if !strings.Contains(err.Error(), "not an npub1... or a 64-hex pubkey") {
		t.Errorf("rd board share <garbage> error = %v, want the npub/hex rejection message", err)
	}
}

// TestBoardURL_NoSecretMaterial is the REJECTION test for done #3: it asserts
// the emitted URL — for all three forms — contains no secret material. This
// test is DESIGNED to fail if a future change adds a key/secret field to
// nostrClaimPayload (e.g. reintroducing v2's "sk", or a CEK/LTK/wrapped-key
// blob) — it inspects the raw decoded JSON bytes, not just known field names,
// so a NEW field carrying secret-shaped content also trips it.
func TestBoardURL_NoSecretMaterial(t *testing.T) {
	ownerKey, _, _, _ := boardTestEnv(t)
	granteeKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}

	forbidden := []string{
		"nsec1",
		strings.ToLower(ownerKey.SecretHex()),   // the owner's 64-hex private key
		strings.ToLower(granteeKey.SecretHex()), // a grantee's 64-hex private key
		"\"sk\"",                                // the retired v2 secret field name
		"\"cek\"",                               // confidential-board content-encryption key
		"\"ltk\"",                               // confidential-board long-term key
		"wrapped",                               // any wrapped-key blob naming
	}

	check := func(label, url string) {
		t.Helper()
		token := extractToken(t, url)
		raw, err := base64.RawURLEncoding.DecodeString(token[len(nostrInviteTokenPrefix):])
		if err != nil {
			t.Fatalf("%s: decode token base64: %v", label, err)
		}
		lower := strings.ToLower(string(raw))
		for _, bad := range forbidden {
			if strings.Contains(lower, strings.ToLower(bad)) {
				t.Errorf("%s: URL/token contains forbidden secret-shaped material %q — raw payload: %s", label, bad, raw)
			}
		}
	}

	out := captureStdoutPipe(t, func() {
		if err := boardCmd.RunE(boardCmd, nil); err != nil {
			t.Fatalf("rd board: %v", err)
		}
	})
	check("rd board", findURLLine(t, out))

	out = captureStdoutPipe(t, func() {
		if err := boardShareCmd.RunE(boardShareCmd, nil); err != nil {
			t.Fatalf("rd board share: %v", err)
		}
	})
	check("rd board share (bare)", findURLLine(t, out))

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	out = captureStdoutPipe(t, func() {
		if err := boardShareCmd.RunE(boardShareCmd, []string{grantee.PubKeyHex()}); err != nil {
			t.Fatalf("rd board share <pubkey>: %v", err)
		}
	})
	check("rd board share <pubkey>", findURLLine(t, out))
}

// TestBoardURL_RejectsExpiredToken and TestBoardURL_RejectsV2SecretToken cover
// done #4: a board URL's token is decoded with the SAME decodeNostrClaimToken
// used everywhere else, so an expired token and a retired v2 secret-bearing
// token are both refused with the existing actionable messages. This
// exercises decodeNostrClaimToken directly against board-URL-shaped tokens
// (the URL wrapping in boardURL/extractToken adds nothing decodeNostrClaimToken
// doesn't already see) — the fuller rejection matrix (malformed board, empty
// claim, bad base64) is already proven in TestNostrClaim_Decode_Rejections
// (nostr_invite_test.go); this scopes the SAME guarantees to what a board URL
// carries.
func TestBoardURL_RejectsExpiredToken(t *testing.T) {
	owner, _ := nostr.GenerateKey()
	coord := rdSync.BoardCoord(owner.PubKeyHex(), "board384-expired")
	now := time.Now()
	expired, err := buildNostrClaimToken(coord, nil, "claim-expired", now.Add(-2*time.Hour).Unix(), now.Add(-1*time.Hour).Unix(), owner.PubKeyHex())
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	url := boardURL(defaultBoardHost, expired)
	token := extractToken(t, url)
	_, err = decodeNostrClaimToken(token)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired board-URL token decode = %v, want the 'expired' message", err)
	}
}

func TestBoardURL_RejectsV2SecretToken(t *testing.T) {
	owner, _ := nostr.GenerateKey()
	coord := rdSync.BoardCoord(owner.PubKeyHex(), "board384-v2")
	now := time.Now()
	v2Raw := `{"v":2,"board":"` + coord + `","sk":"` + strings.Repeat("a", 64) + `","nonce":"x","exp":` + fmt.Sprintf("%d", now.Add(time.Hour).Unix()) + `}`
	v2Token := nostrInviteTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(v2Raw))
	url := boardURL(defaultBoardHost, v2Token)
	token := extractToken(t, url)
	_, err := decodeNostrClaimToken(token)
	if err == nil || !strings.Contains(err.Error(), "insecure") {
		t.Errorf("v2 secret-bearing board-URL token decode = %v, want the 'insecure' rejection", err)
	}
}

// TestBoardCmd_Help_StatesLinkConveysNoReadAccess covers done #5: `rd board
// --help` (the Long description cobra prints for --help) states plainly that
// the link conveys no read access on its own for a confidential board, and
// that authorization is the grant.
func TestBoardCmd_Help_StatesLinkConveysNoReadAccess(t *testing.T) {
	help := boardCmd.Long
	lower := strings.ToLower(help)
	if !strings.Contains(lower, "no read access") {
		t.Errorf("rd board --help does not state the link conveys no read access; Long =\n%s", help)
	}
	if !strings.Contains(lower, "confidential") {
		t.Errorf("rd board --help does not mention confidential boards; Long =\n%s", help)
	}
	if !strings.Contains(lower, "authorization is the grant") {
		t.Errorf("rd board --help does not state authorization is the grant; Long =\n%s", help)
	}
}

// TestBoardHost_Resolution proves the --host flag and $RD_BOARD_HOST override
// the placeholder default, and that a trailing slash on either is trimmed so
// boardURL never doubles it (constraint: "board host URL must be
// configurable, not hardcoded").
func TestBoardHost_Resolution(t *testing.T) {
	cmd := boardCmd
	t.Cleanup(func() { _ = cmd.Flags().Set("host", "") })

	if got := boardHost(cmd); got != defaultBoardHost {
		t.Errorf("boardHost() with nothing set = %q, want default %q", got, defaultBoardHost)
	}

	t.Setenv("RD_BOARD_HOST", "https://env-board.example/")
	if got := boardHost(cmd); got != "https://env-board.example" {
		t.Errorf("boardHost() with RD_BOARD_HOST = %q, want trimmed %q", got, "https://env-board.example")
	}

	if err := cmd.Flags().Set("host", "https://flag-board.example/"); err != nil {
		t.Fatalf("set --host: %v", err)
	}
	if got := boardHost(cmd); got != "https://flag-board.example" {
		t.Errorf("boardHost() with --host = %q, want trimmed %q (flag beats env)", got, "https://flag-board.example")
	}
}

// --- live-relay proof (done #2) ------------------------------------------

// resolveLiveBoardOwnerKey resolves an ALLOWLISTED portfolio key for the
// live-relay proof, mirroring pkg/sync/live_relay_key_test.go's liveRelayKey
// resolution order (RD_NOSTR_TEST_SECRET_HEX, then RD_NOSTR_TEST_KEY_PATH,
// then the machine's own persistent identity) plus this machine's actual
// default rd home key path, since $HOME/.cf/nostr-identity.json is not always
// where the admitted key lives (this workshop machine's is
// $HOME/.config/rd/nostr-identity.json — RDHome()'s own default). The locked
// relays reject a non-admitted author (ready-266), so a write-proof test
// needs an admitted key or it cannot prove anything and must skip.
func resolveLiveBoardOwnerKey(t *testing.T) *nostr.Key {
	t.Helper()
	if h := os.Getenv("RD_NOSTR_TEST_SECRET_HEX"); h != "" {
		k, err := nostr.KeyFromHex(h)
		if err != nil {
			t.Fatalf("RD_NOSTR_TEST_SECRET_HEX: %v", err)
		}
		return k
	}
	var candidates []string
	if p := os.Getenv("RD_NOSTR_TEST_KEY_PATH"); p != "" {
		candidates = append(candidates, p)
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		candidates = append(candidates,
			filepath.Join(home, ".cf", "nostr-identity.json"),
			filepath.Join(home, ".config", "rd", "nostr-identity.json"),
		)
	}
	for _, path := range candidates {
		if k, err := nostr.LoadKeyFile(path); err == nil {
			return k
		}
	}
	t.Skip("no allowlisted portfolio key available: set RD_NOSTR_TEST_SECRET_HEX or RD_NOSTR_TEST_KEY_PATH (the write-allowlisted relays reject non-admitted keys; ready-266)")
	return nil
}

// TestLiveRelay_BoardShare_GrantReadableOnRelay is the ground-source proof
// for done #2: `rd board share <pubkey>` results in a grant ACTUALLY readable
// on the relay for that pubkey — fetched fresh FROM THE RELAY (not the local
// log a process could have faked), then checked with InviteGrantValid, the
// SAME derivation the trust gate uses. Gated behind RD_NOSTR_LIVE_RELAY=1 so
// the default `go test ./...` stays green with no relay reachable.
func TestLiveRelay_BoardShare_GrantReadableOnRelay(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live board-share proof")
	}
	relay := os.Getenv("RD_NOSTR_RELAY_URL")
	if relay == "" {
		var cfg rdconfig.Config
		urls := cfg.WriteRelayURLs()
		if len(urls) == 0 {
			t.Fatal("no write relays configured (set RD_NOSTR_RELAY_URL)")
		}
		relay = urls[0]
	}
	t.Logf("live relay: %s", relay)

	ownerKey := resolveLiveBoardOwnerKey(t)
	owner := ownerKey.PubKeyHex()

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	if err := nostr.SaveKeyFile(nostr.DefaultKeyPath(rdHome), ownerKey, rdHome); err != nil {
		t.Fatalf("SaveKeyFile: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", relay)
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	dir := filepath.Join(base, "project")
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	boardD := fmt.Sprintf("ready-384-live-%d", time.Now().UnixNano())
	coord := rdSync.BoardCoord(owner, boardD)
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{Board: coord, ProjectName: boardD}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	granteeKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}
	grantee := granteeKey.PubKeyHex()

	out := captureStdoutPipe(t, func() {
		if err := boardShareCmd.RunE(boardShareCmd, []string{grantee}); err != nil {
			t.Fatalf("rd board share <pubkey>: %v", err)
		}
	})
	urlLine := findURLLine(t, out)
	token := extractToken(t, urlLine)
	if _, err := decodeNostrClaimToken(token); err != nil {
		t.Fatalf("rd board share <pubkey> token failed to decode: %v", err)
	}

	// Give the relay a beat to index before querying it back — matches
	// pkg/sync's live-relay convention (TestLiveRelay_ItemRoundTrip).
	time.Sleep(2 * time.Second)

	// FETCH FROM THE RELAY into a clean log — no local knowledge — the actual
	// done condition ("--offline is not sufficient").
	cleanLog := rdSync.NewNostrLog(filepath.Join(t.TempDir(), "clean-log.jsonl"))
	trust := map[string]bool{owner: true}
	rres, err := rdSync.ReconcileBoard(context.Background(), []string{relay}, cleanLog, coord, trust, 15*time.Second)
	if err != nil {
		t.Fatalf("ReconcileBoard: %v", err)
	}
	t.Logf("reconcile: fetched=%d added=%d relay_errors=%v", rres.Fetched, rres.Added, rres.RelayErrors)

	relayEvents, err := cleanLog.ReadAll()
	if err != nil {
		t.Fatalf("read clean log: %v", err)
	}
	if !rdSync.InviteGrantValid(relayEvents, owner, boardD, grantee) {
		t.Fatalf("InviteGrantValid = false for events fetched FROM THE RELAY — the grant from `rd board share %s` is not actually readable on the relay", grantee)
	}
	t.Logf("PROVEN: grant from `rd board share %s` is readable on the live relay %s", grantee, relay)
}
