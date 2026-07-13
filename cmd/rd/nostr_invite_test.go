package main

// Deterministic tests for the nostr mint-and-ship invite (ready-a49).
//
// The two-actor test uses a SHARED LOCAL LOG as the sync medium — NO live relay,
// no network egress — so it does not join the ready-6d5 flaky family (the cmd/rd
// join tests that block on real relay dials). Owner mints; a SECOND $RD_HOME
// redeems and `rd ready` (projection) shows the items; re-joining the same token
// FAILS (nonce consumed); an expired token FAILS. Every event is real
// (schnorr-signed + re-verified through the projection trust gate).

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	"github.com/campfire-net/ready/pkg/state"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// projectTrust mirrors nostrTrustSet: self ∪ grant-derived read-trust for the
// pinned board. Used to project a joiner's log exactly as `rd ready` would.
func projectTrust(events []*nostr.Event, selfPub, boardAuthor, boardD string) map[string]bool {
	trust := map[string]bool{selfPub: true}
	for pk := range rdSync.DeriveReadTrust(events, boardAuthor, boardD) {
		trust[pk] = true
	}
	return trust
}

func TestNostrInvite_TwoActor_MintJoinRejoinExpiry(t *testing.T) {
	ctx := context.Background()

	// The shared medium models a relay as a shared append-only log (no network).
	sharedLog := rdSync.NewNostrLog(filepath.Join(t.TempDir(), "relay-log.jsonl"))
	medium := &logInviteMedium{log: sharedLog}

	// --- OWNER: pin a board, publish two items + the invite grant to the medium ---
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

	// Mint the token + owner-signed grant; publish the grant to the medium.
	minted, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("minted GenerateKey: %v", err)
	}
	const nonce = "det-nonce-01"
	tokenRelays := []string{"ws://127.0.0.1:1"} // unreachable by design
	token, grant, err := buildNostrInviteToken(ownerKey, board, minted, tokenRelays, nonce, now, now+7200, now+2)
	if err != nil {
		t.Fatalf("buildNostrInviteToken: %v", err)
	}
	if err := medium.Publish([]*nostr.Event{grant}); err != nil {
		t.Fatalf("publishing grant to medium: %v", err)
	}

	// The token must never expose being anything but an opaque rd1_ string, and it
	// must NOT contain the campfire prefix.
	if !strings.HasPrefix(token, nostrInviteTokenPrefix) {
		t.Fatalf("token missing rd1_ prefix: %q", token)
	}

	// --- ACTOR B: redeem (first use) against a FRESH $RD_HOME + project dir ---
	bHome := t.TempDir()
	bDir := t.TempDir()
	pB, err := decodeNostrInviteToken(token)
	if err != nil {
		t.Fatalf("decode token (B): %v", err)
	}
	if err := redeemNostrInviteToken(pB, bHome, bDir, medium, false); err != nil {
		t.Fatalf("Actor B redeem (first use): %v", err)
	}

	// Identity imported == the minted key.
	loaded, err := nostr.LoadKeyFile(nostr.DefaultKeyPath(bHome))
	if err != nil {
		t.Fatalf("loading B identity: %v", err)
	}
	if loaded.PubKeyHex() != minted.PubKeyHex() {
		t.Errorf("B identity pubkey = %s, want minted %s", loaded.PubKeyHex(), minted.PubKeyHex())
	}

	// Board pinned in B's project.
	bSync, err := rdconfig.LoadSyncConfig(bDir)
	if err != nil {
		t.Fatalf("loading B sync config: %v", err)
	}
	if bSync.Board != board {
		t.Errorf("B pinned board = %q, want %q", bSync.Board, board)
	}

	// Relays adopted into B's rd.json.
	bCfg, err := rdconfig.Load(bHome)
	if err != nil {
		t.Fatalf("loading B rd.json: %v", err)
	}
	if len(bCfg.RelayEndpoints) != 1 || bCfg.RelayEndpoints[0].URL != tokenRelays[0] {
		t.Errorf("B relay endpoints = %+v, want single %q", bCfg.RelayEndpoints, tokenRelays[0])
	}

	// `rd ready` (projection) shows the owner's two items, trust-admitted via the
	// grant the token shipped — the load-bearing outcome.
	bEvents, err := rdSync.NewNostrLog(rdSync.NostrLogPath(bDir)).ReadAll()
	if err != nil {
		t.Fatalf("reading B log: %v", err)
	}
	trust := projectTrust(bEvents, minted.PubKeyHex(), owner, boardD)
	items := rdSync.ProjectItems(bEvents, rdSync.ProjectOptions{Trusted: trust, PinnedBoard: board})
	for _, want := range []string{"ready-001", "ready-002"} {
		if _, ok := items[want]; !ok {
			t.Errorf("B projection missing item %s (rd ready would not show it)", want)
		}
	}

	// --- RE-JOIN the same token: must FAIL (nonce consumed) and leave no state ---
	cHome := t.TempDir()
	cDir := t.TempDir()
	pC, err := decodeNostrInviteToken(token)
	if err != nil {
		t.Fatalf("decode token (C): %v", err)
	}
	err = redeemNostrInviteToken(pC, cHome, cDir, medium, false)
	if err == nil {
		t.Fatal("re-join with a consumed token must FAIL, got nil")
	}
	if !strings.Contains(err.Error(), "already redeemed") {
		t.Errorf("re-join error = %q, want 'already redeemed'", err.Error())
	}
	if fileExists(nostr.DefaultKeyPath(cHome)) {
		t.Error("a rejected re-join must not write an identity")
	}

	// --- EXPIRED token: must FAIL at decode AND at redeem (defense in depth) ---
	expMinted, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("expired-minted GenerateKey: %v", err)
	}
	expToken, expGrant, err := buildNostrInviteToken(ownerKey, board, expMinted, tokenRelays, "exp-nonce", now-7200, now-3600, now-7000)
	if err != nil {
		t.Fatalf("build expired token: %v", err)
	}
	if _, decErr := decodeNostrInviteToken(expToken); decErr == nil || !strings.Contains(decErr.Error(), "expired") {
		t.Errorf("decode of expired token = %v, want 'expired' error", decErr)
	}
	// Even if an attacker crafts the payload directly (bypassing decode), redeem
	// re-checks TTL.
	_ = medium.Publish([]*nostr.Event{expGrant})
	expPayload := &nostrInvitePayload{
		Version: nostrInviteVersion, Board: board, SecretHex: expMinted.SecretHex(),
		Relays: tokenRelays, Nonce: "exp-nonce", IssuedAt: now - 7200, ExpiresAt: now - 3600, Issuer: owner,
	}
	dHome := t.TempDir()
	dDir := t.TempDir()
	if err := redeemNostrInviteToken(expPayload, dHome, dDir, medium, false); err == nil || !strings.Contains(err.Error(), "expired") {
		t.Errorf("redeem of expired payload = %v, want 'expired' error", err)
	}
	if fileExists(nostr.DefaultKeyPath(dHome)) {
		t.Error("an expired redemption must not write an identity")
	}
}

// TestNostrInvite_Decode_Rejections covers the token-format fail-closed paths.
func TestNostrInvite_Decode_Rejections(t *testing.T) {
	if _, err := decodeNostrInviteToken("rd1_"); err == nil {
		t.Error("empty-body token should be rejected")
	}
	if _, err := decodeNostrInviteToken("rd1_!!!not-base64!!!"); err == nil {
		t.Error("non-base64 token should be rejected")
	}
	// Wrong version.
	owner, _ := nostr.GenerateKey()
	minted, _ := nostr.GenerateKey()
	board := rdSync.BoardCoord(owner.PubKeyHex(), "ready")
	now := time.Now().Unix()
	good, _, err := buildNostrInviteToken(owner, board, minted, nil, "n", now, now+3600, now)
	if err != nil {
		t.Fatalf("buildNostrInviteToken: %v", err)
	}
	if _, err := decodeNostrInviteToken(good); err != nil {
		t.Fatalf("valid token should decode: %v", err)
	}
}

// TestNostrInvite_Redeem_UngrantedTokenRefused proves the grant-presence gate: a
// well-formed, unexpired token whose grant never landed on the medium is refused
// fail-closed, and writes no identity.
func TestNostrInvite_Redeem_UngrantedTokenRefused(t *testing.T) {
	sharedLog := rdSync.NewNostrLog(filepath.Join(t.TempDir(), "relay-log.jsonl"))
	medium := &logInviteMedium{log: sharedLog}

	owner, _ := nostr.GenerateKey()
	minted, _ := nostr.GenerateKey()
	board := rdSync.BoardCoord(owner.PubKeyHex(), "ready")
	now := time.Now().Unix()
	// Build a token but DO NOT publish the grant to the medium.
	token, _, err := buildNostrInviteToken(owner, board, minted, nil, "n2", now, now+3600, now)
	if err != nil {
		t.Fatalf("buildNostrInviteToken: %v", err)
	}
	p, err := decodeNostrInviteToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	home := t.TempDir()
	dir := t.TempDir()
	if err := redeemNostrInviteToken(p, home, dir, medium, false); err == nil || !strings.Contains(err.Error(), "not authorized") {
		t.Errorf("ungranted token redeem = %v, want 'not authorized' error", err)
	}
	if fileExists(nostr.DefaultKeyPath(home)) {
		t.Error("an unauthorized redemption must not write an identity")
	}
}

// --- ready-cc5 security hardening: HIGH-3, MED-5, MED-7 ---

// TestRelayInviteMedium_Publish_FailClosed is the HIGH-3 / ready-e03 proof: the
// production relay medium's Publish must FAIL (return an error) whenever the
// kind-39303 consumed marker cannot be confirmed landed+observable on any relay —
// so redeemNostrInviteToken rolls back instead of finalizing a join with single-use
// silently bypassed. The old body discarded nostr.Publish's (accepted, err) and
// always returned nil. The publish/fetch seams are injected so no live relay is
// needed and the test is deterministic.
func TestRelayInviteMedium_Publish_FailClosed(t *testing.T) {
	minted, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	board := rdSync.BoardCoord(minted.PubKeyHex(), "ready")
	marker, err := rdSync.BuildInviteConsumedEvent(minted, "nonce-h3", board, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildInviteConsumedEvent: %v", err)
	}

	// Case 1: the relay REJECTS the publish (accepted=false) -> Publish errors.
	rejecting := &relayInviteMedium{
		relays: []string{"ws://relay"}, board: board, timeout: time.Second,
		publishFn: func(context.Context, string, *nostr.Event) (bool, error) { return false, nil },
		fetchFn:   func(context.Context, string, map[string]any) ([]*nostr.Event, error) { return nil, nil },
	}
	if err := rejecting.Publish([]*nostr.Event{marker}); err == nil {
		t.Error("Publish must FAIL when no relay accepts the marker (fail-closed)")
	}

	// Case 2: the relay reports an ERROR -> Publish errors (surfaces it).
	erroring := &relayInviteMedium{
		relays: []string{"ws://relay"}, board: board, timeout: time.Second,
		publishFn: func(context.Context, string, *nostr.Event) (bool, error) {
			return false, fmt.Errorf("dial refused")
		},
		fetchFn: func(context.Context, string, map[string]any) ([]*nostr.Event, error) { return nil, nil },
	}
	if err := erroring.Publish([]*nostr.Event{marker}); err == nil || !strings.Contains(err.Error(), "dial refused") {
		t.Errorf("Publish must surface the publish error, got %v", err)
	}

	// Case 3: the relay ACCEPTS but the marker is NOT observable on read-back ->
	// Publish still errors (a claimed-but-absent write must not consume the token).
	lying := &relayInviteMedium{
		relays: []string{"ws://relay"}, board: board, timeout: time.Second,
		publishFn: func(context.Context, string, *nostr.Event) (bool, error) { return true, nil },
		fetchFn:   func(context.Context, string, map[string]any) ([]*nostr.Event, error) { return nil, nil },
	}
	if err := lying.Publish([]*nostr.Event{marker}); err == nil {
		t.Error("Publish must FAIL when the marker is accepted but not observable on read-back")
	}

	// Case 4: accepted AND observable on read-back -> Publish SUCCEEDS.
	good := &relayInviteMedium{
		relays: []string{"ws://relay"}, board: board, timeout: time.Second,
		publishFn: func(context.Context, string, *nostr.Event) (bool, error) { return true, nil },
		fetchFn: func(context.Context, string, map[string]any) ([]*nostr.Event, error) {
			return []*nostr.Event{marker}, nil
		},
	}
	if err := good.Publish([]*nostr.Event{marker}); err != nil {
		t.Errorf("Publish must succeed when the marker lands and reads back: %v", err)
	}
}

// TestNostrInvite_ImportGatedByTrust is the MED-5 proof: a FOREIGN-key event served
// by the medium is NOT admitted to the joiner's local authoritative log on join. Only
// events from the board owner's derived trust set (owner + cap-valid grantees, incl.
// the token's minted key) enter the log; a validly-signed foreign event a hostile
// relay injects is dropped at ingestion (single-admission-choke preserved).
func TestNostrInvite_ImportGatedByTrust(t *testing.T) {
	ctx := context.Background()
	sharedLog := rdSync.NewNostrLog(filepath.Join(t.TempDir(), "relay-log.jsonl"))
	medium := &logInviteMedium{log: sharedLog}

	ownerKey, _ := nostr.GenerateKey()
	owner := ownerKey.PubKeyHex()
	const boardD = "ready"
	board := rdSync.BoardCoord(owner, boardD)

	ownerPub := &rdSync.Publisher{Key: ownerKey, Log: sharedLog}
	boardSpec := rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{owner}}
	now := time.Now().Unix()
	if _, err := ownerPub.PublishItem(ctx, &boardSpec, rdSync.CardSpec{
		ItemID: "ready-001", Title: "owner item", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: boardD, BoardAuthor: owner,
	}, now); err != nil {
		t.Fatalf("owner PublishItem: %v", err)
	}

	// A HOSTILE, ungranted foreign key injects a validly-signed card onto the medium.
	foreignKey, _ := nostr.GenerateKey()
	foreignPub := &rdSync.Publisher{Key: foreignKey, Log: sharedLog}
	if _, err := foreignPub.PublishItem(ctx, nil, rdSync.CardSpec{
		ItemID: "ready-666", Title: "poison", Status: state.StatusActive,
		Priority: "p0", Type: "task", BoardD: boardD, BoardAuthor: owner,
	}, now+1); err != nil {
		t.Fatalf("foreign PublishItem: %v", err)
	}

	// Mint + publish the owner-signed contributor grant.
	minted, _ := nostr.GenerateKey()
	token, grant, err := buildNostrInviteToken(ownerKey, board, minted, nil, "nonce-med5", now, now+7200, now+2)
	if err != nil {
		t.Fatalf("buildNostrInviteToken: %v", err)
	}
	if err := medium.Publish([]*nostr.Event{grant}); err != nil {
		t.Fatalf("publish grant: %v", err)
	}

	p, err := decodeNostrInviteToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	home := t.TempDir()
	dir := t.TempDir()
	if err := redeemNostrInviteToken(p, home, dir, medium, false); err != nil {
		t.Fatalf("redeem: %v", err)
	}

	// The joiner's local log must NOT contain any event signed by the foreign key.
	localEvents, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read local log: %v", err)
	}
	for _, e := range localEvents {
		if e.PubKey == foreignKey.PubKeyHex() {
			t.Fatalf("foreign-key event admitted to the local log on join (log poisoning): %s", e.ID)
		}
	}
	// Sanity: the owner's own item DID import (the gate is not over-broad).
	foundOwner := false
	for _, e := range localEvents {
		if e.PubKey == owner {
			foundOwner = true
			break
		}
	}
	if !foundOwner {
		t.Fatal("owner-signed events must still import (trust gate must not drop the owner)")
	}
}

// failPublishMedium models a medium whose Events() serves the owner's snapshot but
// whose Publish (the LATE consumed-marker step) fails — the MED-7 forced-failure
// injection point.
type failPublishMedium struct{ src *rdSync.NostrLog }

func (m *failPublishMedium) Events() ([]*nostr.Event, error) { return m.src.ReadAll() }
func (m *failPublishMedium) Publish([]*nostr.Event) error {
	return fmt.Errorf("forced marker-publish failure")
}

// TestNostrInvite_LateFailure_FullRollback is the MED-7 proof: when a post-write
// step fails (here the consumed-marker publish, the last step), redeem must leave NO
// partial project state — not just the identity, but the board pin, the adopted relay
// config, and the imported log entries are ALL reverted.
func TestNostrInvite_LateFailure_FullRollback(t *testing.T) {
	ctx := context.Background()
	sharedLog := rdSync.NewNostrLog(filepath.Join(t.TempDir(), "relay-log.jsonl"))

	ownerKey, _ := nostr.GenerateKey()
	owner := ownerKey.PubKeyHex()
	const boardD = "ready"
	board := rdSync.BoardCoord(owner, boardD)

	ownerPub := &rdSync.Publisher{Key: ownerKey, Log: sharedLog}
	boardSpec := rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{owner}}
	now := time.Now().Unix()
	if _, err := ownerPub.PublishItem(ctx, &boardSpec, rdSync.CardSpec{
		ItemID: "ready-001", Title: "owner item", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: boardD, BoardAuthor: owner,
	}, now); err != nil {
		t.Fatalf("owner PublishItem: %v", err)
	}

	minted, _ := nostr.GenerateKey()
	token, grant, err := buildNostrInviteToken(ownerKey, board, minted, []string{"ws://127.0.0.1:1"}, "nonce-med7", now, now+7200, now+2)
	if err != nil {
		t.Fatalf("buildNostrInviteToken: %v", err)
	}
	if _, err := sharedLog.AppendUnique([]*nostr.Event{grant}); err != nil {
		t.Fatalf("append grant: %v", err)
	}

	p, err := decodeNostrInviteToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	home := t.TempDir()
	dir := t.TempDir()
	medium := &failPublishMedium{src: sharedLog}

	err = redeemNostrInviteToken(p, home, dir, medium, false)
	if err == nil || !strings.Contains(err.Error(), "forced marker-publish failure") {
		t.Fatalf("redeem = %v, want the forced late failure surfaced", err)
	}

	// FULL rollback: identity, board pin, relay config, and imported log all reverted.
	if fileExists(nostr.DefaultKeyPath(home)) {
		t.Error("late failure left an identity (identity not rolled back)")
	}
	syncCfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if syncCfg.Board != "" {
		t.Errorf("late failure left a pinned board %q (board pin not rolled back)", syncCfg.Board)
	}
	relayCfg, err := rdconfig.Load(home)
	if err != nil {
		t.Fatalf("rdconfig.Load: %v", err)
	}
	if len(relayCfg.RelayEndpoints) != 0 {
		t.Errorf("late failure left adopted relays %+v (relay config not rolled back)", relayCfg.RelayEndpoints)
	}
	localEvents, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read local log: %v", err)
	}
	if len(localEvents) != 0 {
		t.Errorf("late failure left %d imported log event(s) (log import not rolled back)", len(localEvents))
	}
}
