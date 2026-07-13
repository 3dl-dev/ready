package main

// Deterministic tests for the SELF-MINT nostr claim invite (ready-ce0).
//
// The two-actor test uses a SHARED LOCAL LOG as the read medium — NO live relay, no
// network egress — so it does not join the ready-6d5 flaky family. It proves the
// re-architecture's security properties:
//   (a) the token carries NO secret key (two joins of one token self-mint DIFFERENT
//       keys — nothing importable rides the token);
//   (b) join WRITES NOTHING pre-admission (the shared medium is unchanged after a
//       successful join — the self-minted key is inert, publishes to no relay);
//   (e) inertness at projection (the self-minted key is absent from the board owner's
//       derived read-trust until the owner grants it).
// Every event is real (schnorr-signed + re-verified through the projection trust gate).

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	"github.com/campfire-net/ready/pkg/state"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// projectTrust mirrors nostrTrustSet: self ∪ grant-derived read-trust for the pinned
// board. Used to project a joiner's log exactly as `rd ready` would.
func projectTrust(events []*nostr.Event, selfPub, boardAuthor, boardD string) map[string]bool {
	trust := map[string]bool{selfPub: true}
	for pk := range rdSync.DeriveReadTrust(events, boardAuthor, boardD) {
		trust[pk] = true
	}
	return trust
}

func TestNostrClaim_TwoActor_SelfMintReadOnlyThenGrant(t *testing.T) {
	ctx := context.Background()

	// The shared medium models a relay as a shared append-only log (no network).
	sharedLog := rdSync.NewNostrLog(t.TempDir() + "/relay-log.jsonl")
	medium := &logInviteMedium{log: sharedLog}

	// --- OWNER: pin a board, publish two items to the medium ---
	ownerKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("owner GenerateKey: %v", err)
	}
	owner := ownerKey.PubKeyHex()
	const boardD = "ready"
	board := rdSync.BoardCoord(owner, boardD)

	ownerPub := &rdSync.Publisher{Key: ownerKey, Log: sharedLog} // WriteRelays nil → no dial
	boardSpec := rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{owner}}
	now := time.Now().Unix()
	if _, err := ownerPub.PublishItem(ctx, &boardSpec, rdSync.CardSpec{
		ItemID: "ready-001", Title: "first item", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: boardD, BoardAuthor: owner,
	}, now); err != nil {
		t.Fatalf("owner PublishItem 1: %v", err)
	}
	if _, err := ownerPub.PublishItem(ctx, nil, rdSync.CardSpec{
		ItemID: "ready-002", Title: "second item", Status: state.StatusInbox,
		Priority: "p2", Type: "task", BoardD: boardD, BoardAuthor: owner,
	}, now+1); err != nil {
		t.Fatalf("owner PublishItem 2: %v", err)
	}

	// Mint a CLAIM token — NO key, NO grant published.
	const claim = "det-claim-01"
	tokenRelays := []string{"ws://127.0.0.1:1"} // unreachable by design
	token, err := buildNostrClaimToken(board, tokenRelays, claim, now, now+7200, owner)
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	if !strings.HasPrefix(token, nostrInviteTokenPrefix) {
		t.Fatalf("token missing rd1_ prefix: %q", token)
	}

	// (a) NO SECRET IN THE TOKEN: the decoded payload has no "sk" field and no secret
	// material of any kind.
	rawJSON, err := base64.RawURLEncoding.DecodeString(token[len(nostrInviteTokenPrefix):])
	if err != nil {
		t.Fatalf("decode token base64: %v", err)
	}
	if strings.Contains(string(rawJSON), "\"sk\"") || strings.Contains(strings.ToLower(string(rawJSON)), "secret") {
		t.Fatalf("token payload must carry NO secret, got %s", rawJSON)
	}

	// Snapshot the medium size — join must not grow it (prop b).
	before, err := sharedLog.ReadAll()
	if err != nil {
		t.Fatalf("read medium before join: %v", err)
	}

	// --- JOINER: redeem (self-mint) against a FRESH $RD_HOME + project dir ---
	bHome := t.TempDir()
	bDir := t.TempDir()
	pB, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("decode token (B): %v", err)
	}
	mintedPubB, err := redeemNostrClaimToken(pB, bHome, bDir, medium, false)
	if err != nil {
		t.Fatalf("Joiner B redeem: %v", err)
	}

	// A self-minted identity was written; it matches the returned pubkey.
	loaded, err := nostr.LoadKeyFile(nostr.DefaultKeyPath(bHome))
	if err != nil {
		t.Fatalf("loading B identity: %v", err)
	}
	if loaded.PubKeyHex() != mintedPubB {
		t.Errorf("B identity pubkey = %s, want returned minted %s", loaded.PubKeyHex(), mintedPubB)
	}
	if loaded.PubKeyHex() == owner {
		t.Error("self-minted key must NOT be the owner key")
	}

	// (b) JOIN WROTE NOTHING to the medium (no relay write pre-admission).
	after, err := sharedLog.ReadAll()
	if err != nil {
		t.Fatalf("read medium after join: %v", err)
	}
	if len(after) != len(before) {
		t.Errorf("join wrote %d event(s) to the medium; must write NOTHING pre-admission", len(after)-len(before))
	}

	// Board pinned + relays adopted in B's project/config.
	bSync, err := rdconfig.LoadSyncConfig(bDir)
	if err != nil {
		t.Fatalf("loading B sync config: %v", err)
	}
	if bSync.Board != board {
		t.Errorf("B pinned board = %q, want %q", bSync.Board, board)
	}
	bCfg, err := rdconfig.Load(bHome)
	if err != nil {
		t.Fatalf("loading B rd.json: %v", err)
	}
	if len(bCfg.RelayEndpoints) != 1 || bCfg.RelayEndpoints[0].URL != tokenRelays[0] {
		t.Errorf("B relay endpoints = %+v, want single %q", bCfg.RelayEndpoints, tokenRelays[0])
	}

	// `rd ready` (projection) shows the owner's two items READ-ONLY (owner trust root).
	bEvents, err := rdSync.NewNostrLog(rdSync.NostrLogPath(bDir)).ReadAll()
	if err != nil {
		t.Fatalf("reading B log: %v", err)
	}
	trust := projectTrust(bEvents, mintedPubB, owner, boardD)
	items := rdSync.ProjectItems(bEvents, rdSync.ProjectOptions{Trusted: trust, PinnedBoard: board})
	for _, want := range []string{"ready-001", "ready-002"} {
		if _, ok := items[want]; !ok {
			t.Errorf("B projection missing item %s (rd ready would not show it)", want)
		}
	}

	// (e) INERTNESS: pre-grant, the self-minted key is ABSENT from the board owner's
	// derived read-trust set.
	if rdSync.DeriveReadTrust(bEvents, owner, boardD)[mintedPubB] {
		t.Error("self-minted key must be inert (absent from read-trust) before the owner grants it")
	}

	// --- OWNER grants the self-minted key with --claim; single-use binds the nonce ---
	grantEv, err := rdSync.BuildRoleGrantEvent(ownerKey, rdSync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner, Grantee: mintedPubB,
		Role: rdSync.RoleContributor, Claim: claim,
	}, now+10)
	if err != nil {
		t.Fatalf("owner grant: %v", err)
	}
	withGrant := append(bEvents, grantEv)
	if !rdSync.DeriveReadTrust(withGrant, owner, boardD)[mintedPubB] {
		t.Error("after the owner's --claim grant the self-minted key must be admitted to read-trust")
	}
	if bound, ok := rdSync.ClaimGrantee(withGrant, owner, boardD, claim); !ok || bound != mintedPubB {
		t.Errorf("claim %s should bind to the joiner pubkey, got (%s,%v)", claim, bound, ok)
	}

	// (a again) A SECOND joiner of the SAME token self-mints a DIFFERENT key — nothing
	// importable rode the token.
	cHome := t.TempDir()
	cDir := t.TempDir()
	pC, _ := decodeNostrClaimToken(token)
	mintedPubC, err := redeemNostrClaimToken(pC, cHome, cDir, medium, false)
	if err != nil {
		t.Fatalf("Joiner C redeem: %v", err)
	}
	if mintedPubC == mintedPubB {
		t.Error("two joins of one token must self-mint DIFFERENT keys (no key in the token)")
	}
}

// TestNostrClaim_ReJoinLocalIdempotency: a second `rd join` of the SAME token on the
// SAME machine is refused without --force (honest local idempotency), and succeeds
// with --force (self-minting a fresh key).
func TestNostrClaim_ReJoinLocalIdempotency(t *testing.T) {
	sharedLog := rdSync.NewNostrLog(t.TempDir() + "/relay-log.jsonl")
	medium := &logInviteMedium{log: sharedLog}
	ownerKey, _ := nostr.GenerateKey()
	owner := ownerKey.PubKeyHex()
	board := rdSync.BoardCoord(owner, "ready")
	now := time.Now().Unix()
	token, err := buildNostrClaimToken(board, nil, "claim-rejoin", now, now+7200, owner)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	home := t.TempDir()
	dir := t.TempDir()

	p, _ := decodeNostrClaimToken(token)
	if _, err := redeemNostrClaimToken(p, home, dir, medium, false); err != nil {
		t.Fatalf("first join: %v", err)
	}
	// Second join, same home, no force → refused.
	p2, _ := decodeNostrClaimToken(token)
	if _, err := redeemNostrClaimToken(p2, home, dir, medium, false); err == nil ||
		!strings.Contains(err.Error(), "already joined on this machine") {
		t.Fatalf("re-join without --force = %v, want 'already joined on this machine'", err)
	}
	// With --force → allowed (self-mints again).
	p3, _ := decodeNostrClaimToken(token)
	if _, err := redeemNostrClaimToken(p3, home, dir, medium, true); err != nil {
		t.Fatalf("re-join with --force: %v", err)
	}
}

// TestNostrClaim_Decode_Rejections covers the token-format fail-closed paths, incl.
// the retired v2 secret-bearing token.
func TestNostrClaim_Decode_Rejections(t *testing.T) {
	if _, err := decodeNostrClaimToken("rd1_"); err == nil {
		t.Error("empty-body token should be rejected")
	}
	if _, err := decodeNostrClaimToken("rd1_!!!not-base64!!!"); err == nil {
		t.Error("non-base64 token should be rejected")
	}
	owner, _ := nostr.GenerateKey()
	board := rdSync.BoardCoord(owner.PubKeyHex(), "ready")
	now := time.Now().Unix()
	good, err := buildNostrClaimToken(board, nil, "n", now, now+3600, owner.PubKeyHex())
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	if _, err := decodeNostrClaimToken(good); err != nil {
		t.Fatalf("valid token should decode: %v", err)
	}
	// Expired token is rejected at decode.
	expired, _ := buildNostrClaimToken(board, nil, "n", now-7200, now-3600, owner.PubKeyHex())
	if _, err := decodeNostrClaimToken(expired); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("expired token decode = %v, want 'expired'", err)
	}
	// A retired v2 secret-bearing token must be rejected with an explicit message.
	v2 := nostrInviteTokenPrefix + base64.RawURLEncoding.EncodeToString([]byte(
		`{"v":2,"board":"`+board+`","sk":"`+strings.Repeat("a", 64)+`","nonce":"x","exp":`+itoa(now+3600)+`}`))
	if _, err := decodeNostrClaimToken(v2); err == nil || !strings.Contains(err.Error(), "insecure") {
		t.Errorf("v2 secret-bearing token decode = %v, want 'insecure' rejection", err)
	}
}

// failEventsMedium's Events() fails — the LATE-failure injection point (the read step
// runs after the key/board/relay writes, so a failure here must roll them all back).
type failEventsMedium struct{}

func (failEventsMedium) Events() ([]*nostr.Event, error) {
	return nil, errForcedEvents
}

var errForcedEvents = &forcedErr{"forced medium read failure"}

type forcedErr struct{ s string }

func (e *forcedErr) Error() string { return e.s }

// TestNostrClaim_LateFailure_FullRollback: when the medium read (a post-write step)
// fails, redeem must leave NO partial state — not the self-minted identity, the board
// pin, the adopted relay config, the imported log, nor the local claim record.
func TestNostrClaim_LateFailure_FullRollback(t *testing.T) {
	ownerKey, _ := nostr.GenerateKey()
	owner := ownerKey.PubKeyHex()
	board := rdSync.BoardCoord(owner, "ready")
	now := time.Now().Unix()
	token, err := buildNostrClaimToken(board, []string{"ws://127.0.0.1:1"}, "claim-med7", now, now+7200, owner)
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	p, _ := decodeNostrClaimToken(token)
	home := t.TempDir()
	dir := t.TempDir()

	_, err = redeemNostrClaimToken(p, home, dir, failEventsMedium{}, false)
	if err == nil || !strings.Contains(err.Error(), "forced medium read failure") {
		t.Fatalf("redeem = %v, want the forced late failure surfaced", err)
	}

	if fileExists(nostr.DefaultKeyPath(home)) {
		t.Error("late failure left an identity (not rolled back)")
	}
	syncCfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if syncCfg.Board != "" {
		t.Errorf("late failure left a pinned board %q (not rolled back)", syncCfg.Board)
	}
	relayCfg, err := rdconfig.Load(home)
	if err != nil {
		t.Fatalf("rdconfig.Load: %v", err)
	}
	if len(relayCfg.RelayEndpoints) != 0 {
		t.Errorf("late failure left adopted relays %+v (not rolled back)", relayCfg.RelayEndpoints)
	}
	localEvents, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read local log: %v", err)
	}
	if len(localEvents) != 0 {
		t.Errorf("late failure left %d imported log event(s) (not rolled back)", len(localEvents))
	}
	if present, _ := localClaimPresent(consumedInvitesPath(home), "claim-med7"); present {
		t.Error("late failure left a local consumed-claim record (not rolled back)")
	}
}

// itoa is a tiny int64→string helper for building the raw v2 test token.
func itoa(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
