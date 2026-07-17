package main

// Nostr-native SELF-MINT invite/claim (ready-ce0, re-architected from ready-a49).
//
// The old model MINTED a secret key and SHIPPED it in the token — a leaked token was
// a full identity compromise, and the kind-39303 single-use marker (signed by that
// same shipped key) guarded nothing. This is the design §2 "generate-then-authorize"
// replacement:
//
//  1. `rd invite --ttl` (OWNER) mints ONLY a one-use CLAIM-NONCE. It publishes NO
//     key and NO grant. The token carries {board coord, relay set, TTL, claim-nonce}
//     and NO secret. The owner records the nonce locally as UNCLAIMED.
//
//  2. `rd join <token>` (JOINER) SELF-MINTS a fresh secp256k1 key into $RD_HOME
//     (INERT per design §9 — nothing it signs is honored until the owner grants it),
//     pins the board, adopts the relay set, and syncs READ-ONLY (so `rd ready` works
//     immediately). The joiner WRITES NOTHING to the relays pre-admission. It prints
//     its pubkey + the claim-nonce for the owner.
//
//  3. `rd grant <joiner-pubkey> contributor --claim <nonce>` (OWNER) binds the grant
//     to the joiner's self-minted pubkey AND consumes the claim-nonce. Single-use is
//     REAL and owner-enforced: derivation binds one claim-nonce to exactly one
//     pubkey, so a leaked claim admitted to a SECOND self-minted key is REFUSED.
//
// SECURITY. The token is a TTL-bounded CLAIM, not a bearer secret: a leak yields
// only the right to self-mint and ask for a grant the owner may deny; there is no
// importable key and no live grant in it. Single-use is a projection property, not a
// relay write, so `rd join` no longer needs to publish anything (the ready-e03
// fail-closed-relay-write breakage on locked relays is gone with the marker).

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// nostrInviteTokenPrefix is the prefix for nostr claim tokens. Distinct from the
// retired campfire rdx1_ prefix so `rd join` can dispatch on it.
const nostrInviteTokenPrefix = "rd1_"

// nostrClaimVersion is the token schema version. v3 is the self-mint CLAIM token (NO
// secret). v2 was the insecure mint-and-ship token that shipped the secret key; it is
// rejected at decode with a clear message.
const nostrClaimVersion = 3

// nostrClaimPayload is the JSON bundled into an rd1_ v3 token. It carries NO secret
// key and NO grant — only a TTL-bounded, one-use claim-nonce and the coordinates the
// joiner needs to sync read-only.
type nostrClaimPayload struct {
	Version   int      `json:"v"`
	Board     string   `json:"board"`  // "30301:<ownerPubkey>:<boardD>"
	Relays    []string `json:"relays"` // read relay URLs the joiner adopts
	Claim     string   `json:"claim"`  // one-use claim-nonce (hex)
	IssuedAt  int64    `json:"iat"`
	ExpiresAt int64    `json:"exp"`
	Issuer    string   `json:"iss"` // owner pubkey (== board author); informational
}

// buildNostrClaimToken builds an rd1_ v3 claim token. PURE (no I/O, no clock, no
// key): the caller supplies the claim-nonce and timestamps so the result is
// deterministic and testable. The token carries NO secret material.
func buildNostrClaimToken(board string, relays []string, claim string, issuedAt, expiresAt int64, issuer string) (string, error) {
	if _, _, ok := rdSync.ParseBoardCoord(board); !ok {
		return "", fmt.Errorf("invite: malformed board coordinate %q (want 30301:<owner>:<boardD>)", board)
	}
	if claim == "" {
		return "", fmt.Errorf("invite: empty claim-nonce")
	}
	payload := nostrClaimPayload{
		Version:   nostrClaimVersion,
		Board:     board,
		Relays:    relays,
		Claim:     claim,
		IssuedAt:  issuedAt,
		ExpiresAt: expiresAt,
		Issuer:    issuer,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("invite: marshal token: %w", err)
	}
	return nostrInviteTokenPrefix + base64.RawURLEncoding.EncodeToString(data), nil
}

// decodeNostrClaimToken decodes and validates an rd1_ token's FORMAT and TTL. A v2
// (secret-bearing) token is rejected with an explicit "insecure/unsupported" message
// so a stale mint-and-ship token cannot be redeemed. A past ExpiresAt is rejected
// here so an expired token never reaches the redemption path.
func decodeNostrClaimToken(token string) (*nostrClaimPayload, error) {
	if len(token) <= len(nostrInviteTokenPrefix) {
		return nil, fmt.Errorf("token too short")
	}
	decoded, err := base64.RawURLEncoding.DecodeString(token[len(nostrInviteTokenPrefix):])
	if err != nil {
		return nil, fmt.Errorf("invalid token encoding: %w", err)
	}
	// Detect the retired v2 secret-bearing token by its version before generic
	// validation, so the operator gets an actionable error instead of "unsupported
	// version N". The v2 payload carried an "sk" field and version 2.
	var probe struct {
		Version int    `json:"v"`
		Secret  string `json:"sk"`
	}
	if err := json.Unmarshal(decoded, &probe); err == nil {
		if probe.Version < nostrClaimVersion || probe.Secret != "" {
			return nil, fmt.Errorf("unsupported/insecure invite token: this token ships a secret key (v%d). "+
				"Regenerate it with `rd invite` on an up-to-date rd — the current model self-mints on join and ships NO secret", probe.Version)
		}
	}
	var p nostrClaimPayload
	if err := json.Unmarshal(decoded, &p); err != nil {
		return nil, fmt.Errorf("invalid token payload: %w", err)
	}
	if p.Version != nostrClaimVersion {
		return nil, fmt.Errorf("unsupported token version %d", p.Version)
	}
	if _, _, ok := rdSync.ParseBoardCoord(p.Board); !ok {
		return nil, fmt.Errorf("invalid board coordinate in token")
	}
	if p.Claim == "" {
		return nil, fmt.Errorf("token missing claim-nonce")
	}
	if time.Now().Unix() > p.ExpiresAt {
		return nil, fmt.Errorf("token expired at %s", time.Unix(p.ExpiresAt, 0).UTC().Format(time.RFC3339))
	}
	return &p, nil
}

// inviteMedium is the READ-ONLY event source a claim token syncs through. In
// PRODUCTION it is backed by the token's relays (relayInviteMedium); the
// deterministic test backs it with a shared local NostrLog (logInviteMedium) so
// verification needs NO live relay and no network egress (ready-6d5 flaky family).
// The joiner reads the board's events to sync read-only; it PUBLISHES NOTHING —
// pre-admission the self-minted key writes to no relay (design §2, security prop b).
type inviteMedium interface {
	Events() ([]*nostr.Event, error)
}

// logInviteMedium models a relay as a shared local append-only log — the "local
// transport dir" the deterministic test drives. Events() returns the whole log.
type logInviteMedium struct{ log *rdSync.NostrLog }

func (m *logInviteMedium) Events() ([]*nostr.Event, error) { return m.log.ReadAll() }

// relayInviteMedium is the production READ-ONLY medium: Events() fetches the board's
// events from the token's relays. A relay being unreachable degrades to an empty
// snapshot — never a panic — so the read-only join stays robust (an empty snapshot
// just means `rd ready` shows nothing until the next sync). It NEVER publishes.
type relayInviteMedium struct {
	relays  []string
	board   string
	timeout time.Duration
	// fetchFn is the seam the deterministic test injects. Nil means "use the real
	// nostr client" (the production path).
	fetchFn func(ctx context.Context, relay string, filter map[string]any) ([]*nostr.Event, error)
}

func (m *relayInviteMedium) fetch1(ctx context.Context, relay string, filter map[string]any) ([]*nostr.Event, error) {
	if m.fetchFn != nil {
		return m.fetchFn(ctx, relay, filter)
	}
	return nostr.FetchMany(ctx, relay, filter)
}

func (m *relayInviteMedium) Events() ([]*nostr.Event, error) {
	seen := map[string]*nostr.Event{}
	filter := rdSync.BoardSyncFilter(m.board, nil)
	for _, relay := range m.relays {
		ctx, cancel := context.WithTimeout(context.Background(), m.timeout)
		evs, err := m.fetch1(ctx, relay, filter)
		cancel()
		if err != nil {
			continue // best-effort: a down relay must not fail the read-only join
		}
		for _, e := range evs {
			if e != nil {
				seen[e.ID] = e
			}
		}
	}
	out := make([]*nostr.Event, 0, len(seen))
	for _, e := range seen {
		out = append(out, e)
	}
	return out, nil
}

// redeemNostrClaimToken is the JOIN core (design §2): local idempotency check, then
// SELF-MINT a fresh key into $RD_HOME, pin the board, adopt relays, and import the
// board's events READ-ONLY. It PUBLISHES NOTHING to any relay — the self-minted key
// is inert until the owner grants it. Returns the self-minted pubkey (hex) the joiner
// sends to the owner. medium abstracts the relay/log source so the deterministic test
// drives it locally. force overrides both the local re-join guard and an existing
// $RD_HOME identity.
//
// ready-b32 (edge #1, the moot-board near-miss): overwriting the machine key is
// SECURITY-CRITICAL. Two guards apply before any replace:
//   - If the existing key OWNS a board (it is the owner in the pinned board, or it
//     authored a 30301 board / 39301 grant in the local log), a plain --force is
//     NOT enough — self-minting a new key would orphan that board's only owner key.
//     The join HARD-STOPS and points at `rd follow` (keep identity) unless the
//     operator explicitly names that board via ownerKeyForce.
//   - ANY key that is legitimately replaced is first COPIED to backupDir
//     (~/.config/rd-backups/<pubkey>-<n>) so a key is never lost silently. backupDir
//     is passed in (the production caller resolves keyBackupDir(); a test passes a
//     temp dir) so this never touches the real ~/.config.
func redeemNostrClaimToken(p *nostrClaimPayload, rdHome, projectDir, backupDir string, medium inviteMedium, force bool, ownerKeyForce string) (mintedPub string, err error) {
	if time.Now().Unix() > p.ExpiresAt {
		return "", fmt.Errorf("invite token expired at %s", time.Unix(p.ExpiresAt, 0).UTC().Format(time.RFC3339))
	}
	owner, boardD, ok := rdSync.ParseBoardCoord(p.Board)
	if !ok {
		return "", fmt.Errorf("invite token has a malformed board coordinate")
	}

	// (1) LOCAL IDEMPOTENCY (honest, not a security claim): a second `rd join` of the
	// SAME token on THIS machine is refused without --force. The real single-use is
	// owner-enforced at grant derivation; this only guards accidental re-redemption.
	consumedPath := consumedInvitesPath(rdHome)
	if !force {
		already, cerr := localClaimPresent(consumedPath, p.Claim)
		if cerr != nil {
			return "", fmt.Errorf("checking local invite record: %w", cerr)
		}
		if already {
			return "", fmt.Errorf("this invite token was already joined on this machine — pass --force to re-join (self-mint a new key)")
		}
	}

	// (2) Identity guard: join SELF-MINTS a fresh key. Before overwriting an
	// existing $RD_HOME identity we (a) REFUSE if that key OWNS a board — plain
	// --force is deliberately NOT enough, since self-minting would orphan that
	// board's only owner key (edge #1) — unless the operator explicitly NAMES that
	// board via ownerKeyForce; and (b) BACK UP any key we are about to replace so a
	// key is never lost (SECURITY-CRITICAL).
	keyPath := nostr.DefaultKeyPath(rdHome)
	if fileExists(keyPath) {
		existingPub, perr := existingKeyPubkey(keyPath)
		if perr != nil {
			return "", fmt.Errorf("inspecting existing identity at %s: %w", keyPath, perr)
		}
		if owned := keyOwnedBoard(existingPub, projectDir); owned != "" {
			if ownerKeyForce != owned {
				return "", fmt.Errorf(
					"refusing to overwrite this machine's identity: the key %s OWNS board %s. "+
						"Joining would self-mint a NEW key and orphan that board's owner. "+
						"To sync this board WITHOUT giving up your identity, use `rd follow`. "+
						"If you really mean to replace the owner key, re-run with "+
						"--force-replace-owner-key %s (the old key is backed up first).",
					shortHex(existingPub), owned, owned)
			}
			// Explicit board-naming force: fall through to overwrite (with backup).
		} else if !force {
			return "", fmt.Errorf("identity already exists at %s — use --force to overwrite (self-mint a new key for this board)", keyPath)
		}
		// Back up the key we are about to replace. Never lose a key silently.
		if backupDir == "" {
			backupDir = keyBackupDir()
		}
		if _, berr := backupKeyFile(keyPath, backupDir, existingPub); berr != nil {
			return "", fmt.Errorf("backing up the identity before replacing it: %w", berr)
		}
	}
	minted, err := nostr.GenerateKey()
	if err != nil {
		return "", fmt.Errorf("self-minting join identity: %w", err)
	}
	mintedPub = minted.PubKeyHex()

	// Snapshot every file a step below mutates so restore() reverts a half-adopted
	// project on ANY late failure (MED-7 discipline, preserved).
	type fileSnap struct {
		path    string
		data    []byte
		existed bool
	}
	snap := func(path string) fileSnap {
		b, statErr := os.ReadFile(path)
		return fileSnap{path: path, data: b, existed: statErr == nil}
	}
	snaps := []fileSnap{
		snap(keyPath),
		snap(rdconfig.SyncConfigPath(projectDir)),
		snap(rdconfig.Path(rdHome)),
		snap(rdSync.NostrLogPath(projectDir)),
		snap(consumedPath),
	}
	restore := func() {
		for _, s := range snaps {
			if s.existed {
				_ = os.WriteFile(s.path, s.data, 0o600)
			} else {
				_ = os.Remove(s.path)
			}
		}
	}

	if err := nostr.SaveKeyFile(keyPath, minted, rdHome); err != nil {
		return "", fmt.Errorf("writing self-minted identity: %w", err)
	}

	// (3) Pin the board (load-modify-save so no sibling field is clobbered).
	syncCfg, err := rdconfig.LoadSyncConfig(projectDir)
	if err != nil {
		restore()
		return "", fmt.Errorf("loading sync config: %w", err)
	}
	syncCfg.Board = p.Board
	if err := rdconfig.SaveSyncConfig(projectDir, syncCfg); err != nil {
		restore()
		return "", fmt.Errorf("pinning board: %w", err)
	}

	// (4) Adopt the token's relay set into the local rd.json (read+write; the joiner
	// reads now and writes once granted).
	if len(p.Relays) > 0 {
		if err := adoptInviteRelays(rdHome, p.Relays); err != nil {
			restore()
			return "", fmt.Errorf("updating relay config: %w", err)
		}
	}

	// (5) Import the medium's events into the joiner's authoritative log READ-ONLY,
	// gated through the board owner's derived read-trust set (DeriveReadTrust): {board
	// owner} ∪ {cap-valid grantees}. The self-minted key is NOT yet in that set (no
	// grant), so nothing it might have signed would import — but it has signed nothing.
	// A hostile relay's foreign-key events are dropped at ingestion, fail-closed.
	localLog := rdSync.NewNostrLog(rdSync.NostrLogPath(projectDir))
	events, err := medium.Events()
	if err != nil {
		restore()
		return "", fmt.Errorf("reading invite medium: %w", err)
	}
	importTrust := rdSync.DeriveReadTrust(events, owner, boardD)
	verified := make([]*nostr.Event, 0, len(events))
	for _, e := range events {
		if e == nil || e.Verify() != nil {
			continue
		}
		if !importTrust[e.PubKey] {
			continue // foreign-key event served by the relay — not owner-trusted, dropped
		}
		verified = append(verified, e)
	}
	if _, err := localLog.AppendUnique(verified); err != nil {
		restore()
		return "", fmt.Errorf("importing project items: %w", err)
	}

	// (6) Record the claim-nonce consumed LOCALLY (idempotency only; no relay write).
	rec := localClaim{Claim: p.Claim, Board: p.Board, ExpiresAt: p.ExpiresAt, Pubkey: mintedPub}
	if err := appendLocalClaim(consumedPath, rec); err != nil {
		restore()
		return "", fmt.Errorf("recording local invite claim: %w", err)
	}
	return mintedPub, nil
}

// adoptInviteRelays rewrites the local rd.json relay endpoints to the token's relay
// set (each read+write), so the joiner's subsequent `rd sync` reaches the same relays
// the owner published to. Load-modify-save preserves other config.
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

// runNostrInvite mints an rd1_ v3 CLAIM token for the current nostr-native project. It
// generates ONLY a one-use claim-nonce — NO key, NO grant is published at mint — and
// records the nonce locally as UNCLAIMED. Returns the token the invite command prints.
func runNostrInvite(ttl time.Duration) (string, error) {
	dir, native := nostrNativeProject()
	if !native {
		return "", fmt.Errorf("rd invite requires a nostr-native project (a pinned board) — run: rd link <coord> first")
	}
	board := nostrPinnedBoard(dir)
	owner, _, okBoard := rdSync.ParseBoardCoord(board)
	if !okBoard {
		return "", fmt.Errorf("pinned board %q is malformed", board)
	}

	claim, err := randomNonce()
	if err != nil {
		return "", err
	}
	now := time.Now()
	relays := inviteRelaySet()
	if len(relays) == 0 {
		// A local-only project has no relay for a teammate to sync through, so the
		// token would be unusable. Warn loudly rather than mint a dead invite.
		fmt.Fprintln(os.Stderr, "warning: this project has no relays configured, so the invitee has no way to reach your board.")
		fmt.Fprintln(os.Stderr, "         Add a relay (edit .ready/config.json's relay_endpoints, or re-init with --relay) before inviting.")
	}

	token, err := buildNostrClaimToken(board, relays, claim, now.Unix(), now.Add(ttl).Unix(), owner)
	if err != nil {
		return "", err
	}
	// Record the nonce locally as UNCLAIMED (best-effort UX; never fails the mint —
	// the token itself carries the nonce, and single-use is owner-enforced at grant).
	_ = appendLocalClaim(unclaimedInvitesPath(RDHome()), localClaim{Claim: claim, Board: board, ExpiresAt: now.Add(ttl).Unix()})
	return token, nil
}

// joinViaNostrInviteToken redeems an rd1_ claim token against the token's relays for
// the current project directory. It self-mints a read-only identity and prints the
// pubkey + claim-nonce the joiner sends to the owner for a write grant. It creates a
// .ready/ directory in the cwd if none exists, so a truly fresh joiner works without a
// separate `rd init`.
func joinViaNostrInviteToken(token string, force bool, ownerKeyForce string) error {
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		return fmt.Errorf("invalid invite token: %w", err)
	}
	dir, ok := readyProjectDir()
	if !ok {
		// A fresh joiner has no project yet — create .ready/ in the cwd so the join
		// can pin the board and import items without a separate `rd init`.
		cwd, werr := os.Getwd()
		if werr != nil {
			return fmt.Errorf("resolving working directory: %w", werr)
		}
		if err := os.MkdirAll(filepath.Join(cwd, ".ready"), 0o755); err != nil {
			return fmt.Errorf("creating .ready project directory: %w", err)
		}
		dir = cwd
	}
	medium := &relayInviteMedium{relays: p.Relays, board: p.Board, timeout: nostr.DefaultTimeout}
	mintedPub, err := redeemNostrClaimToken(p, RDHome(), dir, keyBackupDir(), medium, force, ownerKeyForce)
	if err != nil {
		return err
	}
	owner, _, _ := rdSync.ParseBoardCoord(p.Board)
	remaining := time.Until(time.Unix(p.ExpiresAt, 0)).Truncate(time.Minute)
	fmt.Fprintf(os.Stdout, "Joined board %s READ-ONLY (invite expires in %s).\n", shortHex(owner), remaining)
	fmt.Println("  run 'rd ready' to see the project's items now.")
	fmt.Println()
	fmt.Println("To get WRITE access, send the owner this — they run `rd grant <pubkey> contributor --claim <claim>`:")
	fmt.Printf("  pubkey=%s\n", mintedPub)
	fmt.Printf("  claim=%s\n", p.Claim)
	fmt.Println("  (on locked relays the owner then runs `rd relay sync-allowlist --apply`)")
	return nil
}

// inviteRelaySet returns the relay URLs to ship in a token: the configured read+write
// relays (deduped), falling back to the defaults.
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

// existingKeyPubkey resolves the pubkey of the identity currently on disk at
// keyPath. It prefers the recorded pubkey_hex field (cheap, no secp math) and
// falls back to re-deriving from the secret when the field is absent. Used by the
// ready-b32 owner-key guard to decide whether the key we are about to replace owns
// a board and to name the backup file.
func existingKeyPubkey(keyPath string) (string, error) {
	if pub, err := nostr.StoredPubKeyHex(keyPath); err == nil && pub != "" {
		return pub, nil
	}
	k, err := nostr.LoadKeyFile(keyPath)
	if err != nil {
		return "", err
	}
	return k.PubKeyHex(), nil
}

// keyOwnedBoard reports a board coordinate (30301:<owner>:<boardD>) that pubkey OWNS
// on THIS MACHINE, or "" if it owns none. The identity key at DefaultKeyPath is
// machine-GLOBAL, so ownership must be detected no matter WHERE `rd join` is run —
// otherwise a plain `rd join --force` from a fresh cwd or an unrelated project dodges
// the guard and silently orphans the global owner key (ready-23d, edge #1). Detection
// therefore spans every board the machine knows:
//   - the project at projectDir itself (its pinned board + local log), and
//   - every SIBLING project under the projects root (filepath.Dir(projectDir)) — the
//     same enumeration source discoverLocalOwnedBoards uses; there is no separate
//     board registry, each repo's pin + local log IS the source of truth.
//
// Per project dir, ownership is any of: pubkey is the owner in the PINNED board
// coordinate; pubkey authored a 30301 board event in the local log; or pubkey authored
// a 39301 role-grant in the local log (only an owner/maintainer signs grants — the
// grant's "a" tag names the board). A concrete coordinate is returned so the operator
// can echo it back via --force-replace-owner-key.
func keyOwnedBoard(pubkey, projectDir string) string {
	if pubkey == "" {
		return ""
	}
	// (1) The project dir itself — cheapest, and the only reachable source for a fresh
	// joiner whose cwd is not under a scannable projects root.
	if coord := keyOwnsBoardInDir(pubkey, projectDir); coord != "" {
		return coord
	}
	// (2) Machine-global scan across sibling projects, so the guard cannot be dodged by
	// where the operator stands. A dir already covered in (1) is skipped; an unreadable
	// root (e.g. projectDir has no parent we can list) degrades to (1)-only.
	root := filepath.Dir(projectDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		sib := filepath.Join(root, e.Name())
		if sib == projectDir {
			continue
		}
		if coord := keyOwnsBoardInDir(pubkey, sib); coord != "" {
			return coord
		}
	}
	return ""
}

// keyOwnsBoardInDir reports the board coordinate pubkey OWNS for the SINGLE project at
// dir (its pinned board, or a 30301 board / 39301 grant it authored in dir's local
// log), or "" if none. keyOwnedBoard calls this per-dir across the whole machine.
func keyOwnsBoardInDir(pubkey, dir string) string {
	if pin := nostrPinnedBoard(dir); pin != "" {
		if owner, _, ok := rdSync.ParseBoardCoord(pin); ok && owner == pubkey {
			return pin
		}
	}
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	events, err := log.ReadAll()
	if err != nil {
		return ""
	}
	for _, e := range events {
		if e == nil || e.PubKey != pubkey {
			continue
		}
		switch e.Kind {
		case rdSync.KindBoard:
			if d := eventTagValue(e, "d"); d != "" {
				return rdSync.BoardCoord(pubkey, d)
			}
		case rdSync.KindRoleGrant:
			if a := eventTagValue(e, "a"); a != "" {
				if _, _, ok := rdSync.ParseBoardCoord(a); ok {
					return a
				}
			}
		}
	}
	return ""
}

// eventTagValue returns the first value of the first tag named name, or "".
func eventTagValue(e *nostr.Event, name string) string {
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

// keyBackupDir resolves the directory replaced machine keys are copied into,
// ~/.config/rd-backups (honoring $XDG_CONFIG_HOME), a SIBLING of the rd home so a
// backup survives even a full ~/.config/rd wipe. Tests pass an explicit backupDir
// into redeemNostrClaimToken instead, so this never touches the real ~/.config
// under test.
func keyBackupDir() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "rd-backups")
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return filepath.Join(home, ".config", "rd-backups")
	}
	return filepath.Join(RDHome(), "rd-backups")
}

// backupKeyFile copies the key file at keyPath into backupDir as <pubkey>-<n>,
// choosing the lowest n≥1 whose file does not yet exist so repeated replacements
// never clobber an earlier backup. Returns the backup path written. This is the
// SECURITY-CRITICAL "never lose a key without a backup" invariant (ready-b32).
func backupKeyFile(keyPath, backupDir, pubkey string) (string, error) {
	data, err := os.ReadFile(keyPath)
	if err != nil {
		return "", fmt.Errorf("reading key to back up: %w", err)
	}
	if err := os.MkdirAll(backupDir, 0o700); err != nil {
		return "", fmt.Errorf("creating backup dir %s: %w", backupDir, err)
	}
	if pubkey == "" {
		pubkey = "unknown-pubkey"
	}
	for n := 1; ; n++ {
		dest := filepath.Join(backupDir, fmt.Sprintf("%s-%d", pubkey, n))
		if _, statErr := os.Stat(dest); os.IsNotExist(statErr) {
			if err := os.WriteFile(dest, data, 0o600); err != nil {
				return "", fmt.Errorf("writing key backup %s: %w", dest, err)
			}
			return dest, nil
		}
	}
}

// shortHex abbreviates a hex id for human output.
func shortHex(s string) string {
	if len(s) > 12 {
		return s[:12] + "..."
	}
	return s
}
