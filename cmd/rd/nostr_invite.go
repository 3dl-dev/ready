package main

// Nostr-native mint-and-ship invite (ready-a49).
//
// `rd invite` on a nostr-native project (a pinned board in .ready/config.json)
// MINTS a fresh secp256k1 contributor key, publishes an owner-signed kind-39301
// CONTRIBUTOR grant for its pubkey, and SHIPS everything the joiner needs in a
// single `rd1_` token: the board coordinate 30301:<owner>:<boardD>, the relay set,
// a TTL, a one-use nonce, and the minted SECRET key. `rd join rd1_...` imports the
// key to $RD_HOME, pins the board, updates the local relay config, and syncs so
// `rd ready` returns the project's items immediately.
//
// SECURITY. The secret key travels in the token BY DESIGN (closed team tier,
// design §9): the joiner needs a signing identity and the owner mints an inert one
// that is honored ONLY because of the owner-signed grant that rides the same
// medium. The token is therefore a bearer secret — never logged, written only to
// $RD_HOME on join, TTL-bounded, and ONE-USE. Single-use is enforced
// RELAY/LOG-OBSERVABLY: the first redeemer publishes a signed kind-39303
// invite-consumed marker carrying the nonce; any later redemption sees it and is
// refused (pkg/sync.InviteNonceConsumed).
//
// This is the nostr replacement for the campfire admit + ed25519-seed rdx1_ token
// (invite.go / join.go). The rdx1_ path remains for campfire-backed projects; a
// nostr-native project (the default `rd init`) uses THIS path.

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// nostrInviteTokenPrefix is the prefix for nostr mint-and-ship tokens. Distinct
// from the campfire rdx1_ prefix so `rd join` can dispatch on it.
const nostrInviteTokenPrefix = "rd1_"

// nostrInviteVersion is the token schema version (2 — v1 is the ed25519/campfire
// rdx1_ payload).
const nostrInviteVersion = 2

// nostrInvitePayload is the JSON bundled into an rd1_ token. The secret key is a
// bearer credential — see the file header.
type nostrInvitePayload struct {
	Version   int      `json:"v"`
	Board     string   `json:"board"`  // "30301:<ownerPubkey>:<boardD>"
	SecretHex string   `json:"sk"`     // 64-char hex secp256k1 secret of the minted key
	Relays    []string `json:"relays"` // read+write relay URLs the joiner adopts
	Nonce     string   `json:"nonce"`  // one-use nonce (hex)
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
	Issuer    string   `json:"iss"` // owner pubkey (== board author); informational
}

// buildNostrInviteToken builds an rd1_ token AND the owner-signed kind-39301
// contributor grant for the minted key. PURE (no I/O, no clock): the caller
// supplies the minted key, nonce, and timestamps so the result is deterministic
// and testable. ownerKey signs the grant (owner or a maintainer within the
// escalation cap — contributor grants are cap-allowed for both). board is the
// full "30301:<owner>:<boardD>" coordinate; the grant binds to it.
func buildNostrInviteToken(ownerKey *nostr.Key, board string, minted *nostr.Key, relays []string, nonce string, issuedAt, expiresAt, grantCreatedAt int64) (token string, grant *nostr.Event, err error) {
	owner, boardD, ok := rdSync.ParseBoardCoord(board)
	if !ok {
		return "", nil, fmt.Errorf("invite: malformed board coordinate %q (want 30301:<owner>:<boardD>)", board)
	}
	grant, err = rdSync.BuildRoleGrantEvent(ownerKey, rdSync.RoleGrantSpec{
		BoardD:      boardD,
		BoardAuthor: owner,
		Grantee:     minted.PubKeyHex(),
		Role:        rdSync.RoleContributor,
		Label:       "rd invite",
	}, grantCreatedAt)
	if err != nil {
		return "", nil, fmt.Errorf("invite: build grant: %w", err)
	}
	payload := nostrInvitePayload{
		Version:   nostrInviteVersion,
		Board:     board,
		SecretHex: minted.SecretHex(),
		Relays:    relays,
		Nonce:     nonce,
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
		Issuer:    owner,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", nil, fmt.Errorf("invite: marshal token: %w", err)
	}
	return nostrInviteTokenPrefix + base64.RawURLEncoding.EncodeToString(data), grant, nil
}

// decodeNostrInviteToken decodes and validates an rd1_ token's FORMAT and TTL.
// Structural validation (version, board coordinate, 32-byte secret, non-empty
// nonce) is fail-closed; a past ExpiresAt is rejected here so an expired token
// never reaches the redemption path.
func decodeNostrInviteToken(token string) (*nostrInvitePayload, error) {
	if len(token) <= len(nostrInviteTokenPrefix) {
		return nil, fmt.Errorf("token too short")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token[len(nostrInviteTokenPrefix):])
	if err != nil {
		return nil, fmt.Errorf("invalid token encoding: %w", err)
	}
	var p nostrInvitePayload
	if err := json.Unmarshal(decoded, &p); err != nil {
		return nil, fmt.Errorf("invalid token payload: %w", err)
	}
	if p.Version != nostrInviteVersion {
		return nil, fmt.Errorf("unsupported token version %d", p.Version)
	}
	if _, _, ok := rdSync.ParseBoardCoord(p.Board); !ok {
		return nil, fmt.Errorf("invalid board coordinate in token")
	}
	if len(p.SecretHex) != 64 || !isHex(p.SecretHex) {
		return nil, fmt.Errorf("invalid secret key in token")
	}
	if p.Nonce == "" {
		return nil, fmt.Errorf("token missing nonce")
	}
	if time.Now().Unix() > p.ExpiresAt {
		return nil, fmt.Errorf("token expired at %s", time.Unix(p.ExpiresAt, 0).UTC().Format(time.RFC3339))
	}
	return &p, nil
}

// inviteMedium is the shared event medium a mint-and-ship token syncs through. In
// PRODUCTION it is backed by the token's relays (relayInviteMedium); the
// two-actor DETERMINISTIC test backs it with a shared local NostrLog
// (logInviteMedium) so verification needs NO live relay and no network egress
// (ready-6d5 flaky family). Events() returns every signature-relevant event
// visible on the medium; Publish ships the consumed marker back onto it.
type inviteMedium interface {
	Events() ([]*nostr.Event, error)
	Publish(events []*nostr.Event) error
}

// logInviteMedium models a relay as a shared local append-only log — the "local
// transport dir" the deterministic test drives. Events() returns the whole log;
// Publish appends (a relay-observable write both actors and any re-joiner see).
type logInviteMedium struct{ log *rdSync.NostrLog }

func (m *logInviteMedium) Events() ([]*nostr.Event, error) { return m.log.ReadAll() }
func (m *logInviteMedium) Publish(events []*nostr.Event) error {
	_, err := m.log.AppendUnique(events)
	return err
}

// relayInviteMedium is the production medium: Events() fetches the board's events
// plus any invite-consumed markers from the token's relays; Publish best-effort
// posts the consumed marker to them. A relay being unreachable degrades to an
// empty snapshot / a buffered publish — never a panic — so the redeem path stays
// fail-closed (an empty snapshot means "no grant present" ⇒ refused, and "no
// marker" only lets a FIRST use through, which is correct).
type relayInviteMedium struct {
	relays  []string
	board   string
	timeout time.Duration
}

func (m *relayInviteMedium) Events() ([]*nostr.Event, error) {
	seen := map[string]*nostr.Event{}
	filters := []map[string]any{
		rdSync.BoardSyncFilter(m.board, nil),
		{"kinds": []int{rdSync.KindInviteConsumed}, "#a": []string{m.board}},
	}
	for _, relay := range m.relays {
		for _, f := range filters {
			ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
			evs, err := nostr.FetchMany(ctx, relay, f)
			cancel()
			if err != nil {
				continue // best-effort: a down relay must not fail the join
			}
			for _, e := range evs {
				if e != nil {
					seen[e.ID] = e
				}
			}
		}
	}
	out := make([]*nostr.Event, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	return out, nil
}

func (m *relayInviteMedium) Publish(events []*nostr.Event) error {
	for _, e := range events {
		for _, relay := range m.relays {
			ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
			_, _, _ = nostr.Publish(ctx, relay, e) // best-effort
			cancel()
		}
	}
	return nil
}

// redeemNostrInviteToken is the JOIN core: single-use + grant checks first (a
// rejected redemption leaves NO state), then key import, board pin, relay config,
// item sync, and the consumed-marker publish. medium abstracts the relay/log
// source so the deterministic test drives it locally. force overwrites an existing
// $RD_HOME identity (mirroring the rdx1_ path's --force).
func redeemNostrInviteToken(p *nostrInvitePayload, rdHome, projectDir string, medium inviteMedium, force bool) error {
	if time.Now().Unix() > p.ExpiresAt {
		return fmt.Errorf("invite token expired at %s", time.Unix(p.ExpiresAt, 0).UTC().Format(time.RFC3339))
	}
	owner, boardD, ok := rdSync.ParseBoardCoord(p.Board)
	if !ok {
		return fmt.Errorf("invite token has a malformed board coordinate")
	}
	minted, err := nostr.KeyFromHex(p.SecretHex)
	if err != nil {
		return fmt.Errorf("invite token secret key is invalid: %w", err)
	}
	mintedPub := minted.PubKeyHex()

	// Snapshot the shared medium (relay download / shared-log read).
	events, err := medium.Events()
	if err != nil {
		return fmt.Errorf("reading invite medium: %w", err)
	}

	// (1) SINGLE-USE — refuse if the nonce is already consumed. Before any write,
	// so a rejected re-join leaves nothing behind.
	if rdSync.InviteNonceConsumed(events, p.Nonce) {
		return fmt.Errorf("invite token already redeemed — each token may only be used once")
	}
	// (2) GRANT PRESENCE — fail-closed: the minted key must actually hold an
	// owner-rooted, cap-valid contributor grant on the pinned board. A token whose
	// grant never landed (or was forged / cross-board) is refused before we import
	// its identity.
	if !rdSync.InviteGrantValid(events, owner, boardD, mintedPub) {
		return fmt.Errorf("invite token is not authorized: no owner-signed contributor grant for its key on board %s", p.Board)
	}

	// (3) Identity import guard.
	keyPath := nostr.DefaultKeyPath(rdHome)
	hadKey := fileExists(keyPath)
	if hadKey && !force {
		return fmt.Errorf("identity already exists at %s — use --force to overwrite", keyPath)
	}
	oldKey, _ := os.ReadFile(keyPath)

	// restore rolls back the identity on any post-write failure, so a failed join
	// never leaves a half-adopted identity (parity with the rdx1_ path).
	restore := func() {
		if hadKey {
			_ = os.WriteFile(keyPath, oldKey, 0o600)
		} else {
			_ = os.Remove(keyPath)
		}
	}

	if err := nostr.SaveKeyFile(keyPath, minted, rdHome); err != nil {
		return fmt.Errorf("writing invite identity: %w", err)
	}

	// (4) Pin the board (load-modify-save so no sibling field is clobbered).
	syncCfg, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		restore()
		return fmt.Errorf("loading sync config: %w", err)
	}
	syncCfg.Board = p.Board
	if err := rdconfig.SaveSyncConfig(projectDir, syncCfg); err != nil {
		restore()
		return fmt.Errorf("pinning board: %w", err)
	}

	// (5) Adopt the token's relay set into the local rd.json (read+write).
	if len(p.Relays) > 0 {
		if err := adoptInviteRelays(rdHome, p.Relays); err != nil {
			restore()
			return fmt.Errorf("updating relay config: %w", err)
		}
	}

	// (6) Import the medium's events into the joiner's authoritative log
	// (signature-gated at ingestion; the projection re-applies the trust gate). Now
	// `rd ready` projects the owner's items immediately.
	localLog := rdSync.NewNostrLog(rdSync.NostrLogPath(projectDir))
	verified := make([]*nostr.Event, 0, len(events))
	for _, e := range events {
		if e != nil && e.Verify() == nil {
			verified = append(verified, e)
		}
	}
	if _, err := localLog.AppendUnique(verified); err != nil {
		restore()
		return fmt.Errorf("importing project items: %w", err)
	}

	// (7) Consume the nonce RELAY/LOG-OBSERVABLY: publish a signed marker to the
	// medium AND the local log so a later re-join (which reads the medium) is
	// refused. Signed by the minted key — trusted because of its grant.
	// The consumed marker is a leaf event (kind 39303, DriftScope==""): it is matched
	// by nonce (InviteNonceConsumed), never ordered by created_at in latest-wins, so it
	// takes the empty drift scope — consistent with DriftScope of a 39303 event.
	marker, err := rdSync.BuildInviteConsumedEvent(minted, p.Nonce, p.Board, nostrNextCreatedAt(localLog, ""))
	if err != nil {
		restore()
		return fmt.Errorf("building consumed marker: %w", err)
	}
	if err := medium.Publish([]*nostr.Event{marker}); err != nil {
		restore()
		return fmt.Errorf("publishing consumed marker: %w", err)
	}
	if err := localLog.Append(marker); err != nil {
		restore()
		return fmt.Errorf("recording consumed marker locally: %w", err)
	}
	return nil
}

// adoptInviteRelays rewrites the local rd.json relay endpoints to the token's
// relay set (each read+write), so the joiner's subsequent `rd nostr sync` reaches
// the same relays the owner published to. Load-modify-save preserves other config.
func adoptInviteRelays(rdHome string, relays []string) error {
	cfg, err := rdconfig.Load(rdHome)
	if err != nil {
		cfg = &rdconfig.Config{}
	}
	eps := make([]rdconfig.RelayEndpoint, 0, len(relays))
	for _, u := range relays {
		if u == "" {
			continue
		}
		eps = append(eps, rdconfig.RelayEndpoint{URL: u, Read: true, Write: true})
	}
	cfg.RelayEndpoints = eps
	return rdconfig.Save(rdHome, cfg)
}

// runNostrInvite mints an rd1_ token for the current nostr-native project: it
// generates a fresh contributor key, publishes the owner-signed grant to the local
// log + relays, and prints the token. Returns an error the invite command
// surfaces; the caller falls back to the campfire path when this project is not
// nostr-native.
func runNostrInvite(ttl time.Duration) (string, error) {
	dir, native := nostrNativeProject()
	if !native {
		return "", fmt.Errorf("rd invite --nostr requires a nostr-native project (a pinned board); run 'rd nostr pin-board' first")
	}
	board := nostrPinnedBoard(dir)
	_, boardD, okBoard := rdSync.ParseBoardCoord(board)
	if !okBoard {
		return "", fmt.Errorf("pinned board %q is malformed", board)
	}

	pub, ok, err := nostrPublisher()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("no .ready project directory found")
	}

	minted, err := nostr.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("minting invite key: %w", err)
	}
	nonce, err := randomNonce()
	if err != nil {
		return "", err
	}
	now := time.Now()
	relays := inviteRelaySet()

	token, grant, err := buildNostrInviteToken(pub.Key, board, minted, relays, nonce, now.Unix(), now.Add(ttl).Unix(), nostrNextCreatedAt(pub.Log, rdSync.GrantDriftScope(boardD, minted.PubKeyHex())))
	if err != nil {
		return "", err
	}

	// Publish the owner-signed grant: durable in the local log, best-effort to the
	// relays. Never log the token's secret; the grant carries only the PUBLIC key.
	if _, err := pub.PublishEvents(context.Background(), []*nostr.Event{grant}); err != nil {
		return "", fmt.Errorf("publishing invite grant: %w", err)
	}
	return token, nil
}

// joinViaNostrInviteToken redeems an rd1_ token against the token's relays for the
// current project directory. It is the production wrapper around
// redeemNostrInviteToken (which the deterministic test drives with a local
// medium).
func joinViaNostrInviteToken(token string, force bool) error {
	p, err := decodeNostrInviteToken(token)
	if err != nil {
		return fmt.Errorf("invalid invite token: %w", err)
	}
	dir, ok := readyProjectDir()
	if !ok {
		return fmt.Errorf("run 'rd join' from inside a project directory (no .ready found in cwd or parents)")
	}
	medium := &relayInviteMedium{relays: p.Relays, board: p.Board, timeout: nostr.DefaultTimeout}
	if err := redeemNostrInviteToken(p, RDHome(), dir, medium, force); err != nil {
		return err
	}
	owner, _, _ := rdSync.ParseBoardCoord(p.Board)
	remaining := time.Until(time.Unix(p.ExpiresAt, 0)).Truncate(time.Minute)
	fmt.Fprintf(os.Stdout, "joined board %s via invite token (expires in %s)\n", shortHex(owner), remaining)
	fmt.Println("  run 'rd ready' to see the project's items")
	return nil
}

// inviteRelaySet returns the relay URLs to ship in a token: the configured
// read+write relays (deduped), falling back to the defaults.
func inviteRelaySet() []string {
	seen := map[string]bool{}
	var out []string
	add := func(us []string) {
		for _, u := range us {
			if u != "" && !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	}
	add(nostrWriteRelays())
	add(nostrReadRelays())
	return out
}

// randomNonce returns a 16-byte crypto-random hex nonce.
func randomNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// shortHex abbreviates a hex id for human output.
func shortHex(s string) string {
	if len(s) > 12 {
		return s[:12] + "..."
	}
	return s
}
