package main

import (
	"bufio"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/campfire-net/campfire/pkg/beacon"
	discovery "github.com/campfire-net/campfire/cf-conventions/cf-discovery"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/spf13/cobra"

	"github.com/campfire-net/ready/pkg/rdconfig"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// defaultBeaconRoot is the compiled-in default beacon root ID.
// Empty string means no default is compiled in — any first use is a TOFU event.
const defaultBeaconRoot = ""

var joinCmd = &cobra.Command{
	Use:   "join <name-or-campfire-id>",
	Short: "Join a project campfire by name or ID",
	Long: `Join a campfire by name (cf:// URI or short name) or by campfire ID.

For open campfires, joins immediately.

For invite-only campfires, exits with an error and prints your public key.
Ask an existing member to run 'cf admit <your-pubkey>' or 'rd admit', then
run 'rd join' again.

TOFU PINNING
  The first time you join using a non-default beacon root (--root), rd warns
  you and pins the beacon root in the config. On subsequent joins, any
  deviation from the pinned root requires --confirm.

  To reset the pinned root:
    rd join --reset-beacon-root

EXAMPLES
  rd join myorg.ready/myproject
  rd join cf://myorg.ready/myproject
  rd join abcdef1234...               # join by campfire ID directly
  rd join <id> --root <beacon-root>   # join with explicit beacon root (TOFU)
  rd join --reset-beacon-root         # clear the pinned beacon root`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resetRoot, _ := cmd.Flags().GetBool("reset-beacon-root")
		beaconRootFlag, _ := cmd.Flags().GetString("root")
		confirm, _ := cmd.Flags().GetBool("confirm")
		// --timeout and --role are kept as flags for forward compatibility but are
		// not used in the current open-join path (invite-only join-request path removed).
		_, _ = cmd.Flags().GetDuration("timeout")
		_, _ = cmd.Flags().GetString("role")

		cfg, err := rdconfig.Load(CFHome())
		if err != nil {
			return fmt.Errorf("loading config: %w", err)
		}

		// Handle --reset-beacon-root.
		if resetRoot {
			prev, saveErr := resetBeaconRoot(CFHome())
			if saveErr != nil {
				return saveErr
			}
			if prev == "" {
				fmt.Println("no beacon root pinned")
				return nil
			}
			fmt.Printf("beacon root pin cleared (was: %s...)\n", prev[:minInt(12, len(prev))])
			return nil
		}

		if len(args) == 0 {
			return fmt.Errorf("name-or-campfire-id required (use --reset-beacon-root to clear the pinned beacon root)")
		}

		nameOrID := args[0]
		force, _ := cmd.Flags().GetBool("force")

		// Detect a nostr mint-and-ship token (rd1_ prefix) — imports the minted
		// secp256k1 key, pins the board, adopts relays, and syncs (ready-a49).
		if strings.HasPrefix(nameOrID, nostrInviteTokenPrefix) {
			return joinViaNostrInviteToken(nameOrID, force)
		}

		// Detect a campfire invite token (rdx1_ prefix) — takes a completely
		// different path (campfire admit + ed25519 seed).
		if strings.HasPrefix(nameOrID, inviteTokenPrefix) {
			return joinViaInviteToken(nameOrID, force)
		}

		// TOFU pinning: validate beacon root before resolving, but do not save yet.
		// If the join fails, we must roll back to avoid pinning an untrusted root.
		if err := validateBeaconRootTOFU(CFHome(), cfg, beaconRootFlag, confirm); err != nil {
			return err
		}

		client, err := requireClient()
		if err != nil {
			return err
		}

		// Resolve the name to a campfire ID.
		campfireID, err := resolveName(client, nameOrID)
		if err != nil {
			return fmt.Errorf("resolving name %q: %w", nameOrID, err)
		}

		// Attempt to join.
		result, err := client.Join(protocol.JoinRequest{
			CampfireID: campfireID,
			Transport:  protocol.FilesystemTransport{Dir: resolveTransportDir(campfireID)},
		})
		if err == nil {
			// Join succeeded. Now pin the beacon root if needed (TOFU).
			if beaconRootFlag != "" {
				// Atomically pin the beacon root (with file locking to prevent
				// concurrent TOCTOU race — ready-2dc).
				pinned, err := rdconfig.PinBeaconRoot(CFHome(), beaconRootFlag)
				if err != nil {
					fmt.Fprintf(os.Stderr, "warning: could not pin beacon root after successful join: %v\n", err)
				} else if pinned {
					fmt.Fprintf(os.Stderr, "pinned beacon root: %s...\n", beaconRootFlag[:minInt(12, len(beaconRootFlag))])
				}
				// If pinned=false, another process set the root first. Either way,
				// join succeeded and we're done.
			}

			// Bootstrap local project state so rd commands work immediately
			// after join, and pull existing items (ready-5cd).
			cwd, cwdErr := os.Getwd()
			if cwdErr == nil {
				if bootstrapErr := bootstrapJoinedProject(cwd, campfireID, client); bootstrapErr != nil {
					fmt.Fprintf(os.Stderr, "warning: could not bootstrap project state: %v\n", bootstrapErr)
				}
			}

			displayID := campfireID
			if len(displayID) > 12 {
				displayID = displayID[:12] + "..."
			}
			fmt.Fprintf(os.Stdout, "joined campfire %s (%s)\n", displayID, result.JoinProtocol)
			fmt.Println("  cross-campfire deps referencing this campfire will now resolve")
			return nil
		}

		// Join failed. Distinguish invite-only from transport/network errors.
		//
		// Invite-only campfires: non-members cannot post work:join-request because
		// sending requires membership (chicken-and-egg). Tell the user clearly: they
		// must be admitted by an existing member first, then run 'rd join' again.
		//
		// Other errors (transport state not found, corrupted state, network errors):
		// report the raw error so the user can diagnose.
		if strings.Contains(err.Error(), "invite-only") {
			agentID, s, storeErr := requireAgentAndStore()
			pubkeyStr := "(could not load identity)"
			if storeErr == nil {
				pubkeyStr = agentID.PublicKeyHex()
				s.Close()
			}
			return fmt.Errorf("campfire is invite-only — ask a member to admit your public key, then run 'rd join' again\n  your public key: %s", pubkeyStr)
		}
		return fmt.Errorf("joining campfire: %w", err)
	},
}

// resolveTransportDir returns the campfire-specific filesystem transport directory.
//
// The campfire library's joinFilesystem uses path-rooted mode (fs.ForDir), which
// expects the campfire-specific directory (baseDir/campfireID/) rather than the
// base directory (baseDir/). This function returns the correct campfire-specific dir.
//
// Resolution order:
//  1. Scan beacon dirs (global default + project-local .campfire/beacons/) for a
//     beacon matching campfireID. If found, the beacon carries the authoritative
//     transport dir (set by the campfire creator). This handles the two-sided
//     handshake: when the campfire was created in a different CF_HOME, the beacon
//     advertises the creator's transport dir so joiners can locate the state.
//  2. Fall back to localCampfireBaseDir()/campfireID — the default campfire-specific
//     subdirectory under the local CF_HOME.
func resolveTransportDir(campfireID string) string {
	scanDirs := []string{beacon.DefaultBeaconDir()}

	// Also check project-local .campfire/beacons/ dir if we're in a project.
	if _, projectDir, ok := projectRoot(); ok {
		scanDirs = append(scanDirs, filepath.Join(projectDir, ".campfire", "beacons"))
	}

	// Use the cf-discovery Tier-1 surface (ScanFresh) rather than a raw
	// beacon.Scan: it filters expired beacons and applies the discovery layer's
	// signature/tamper checks, so we no longer reimplement beacon discovery here.
	now := time.Now()
	for _, dir := range scanDirs {
		if dir == "" {
			continue
		}
		beacons, err := discovery.ScanFresh(dir, now)
		if err != nil {
			continue
		}
		for _, b := range beacons {
			if b.CampfireIDHex() == campfireID {
				if d, ok := b.Transport.Config["dir"]; ok && d != "" {
					return d
				}
			}
		}
	}

	// Default: campfire-specific subdirectory under the local CF_HOME campfires base.
	// This is the correct path-rooted dir expected by joinFilesystem (fs.ForDir mode).
	return filepath.Join(localCampfireBaseDir(), campfireID)
}

// validateBeaconRootTOFU validates the TOFU beacon root pinning logic without saving.
// cfg is read but NOT modified; validation only.
// Returns an error if the user aborts or the root mismatches without --confirm.
//
// Paths:
//   - beaconRoot empty: no-op
//   - cfg.BeaconRoot empty (first use): warn, prompt if interactive and !confirm
//   - cfg.BeaconRoot matches beaconRoot: no-op
//   - cfg.BeaconRoot mismatches beaconRoot + !confirm: error
//   - cfg.BeaconRoot mismatches beaconRoot + confirm: proceed (no error)
//
// Note: The actual pin save is deferred to AFTER a successful join (see joinCmd.RunE).
// This prevents pinning an untrusted beacon root if the join fails (ready-f43).
func validateBeaconRootTOFU(cfHome string, cfg *rdconfig.Config, beaconRoot string, confirm bool) error {
	if beaconRoot == "" {
		return nil
	}

	if cfg.BeaconRoot == "" {
		// First use of a non-default beacon root — warn and prompt.
		fmt.Fprintf(os.Stderr, "warning: first use of non-default beacon root %s...\n", beaconRoot[:minInt(12, len(beaconRoot))])
		fmt.Fprintf(os.Stderr, "  this root will be pinned (TOFU) in the config after a successful join\n")
		fmt.Fprintf(os.Stderr, "  future joins using a different root will require --confirm\n")

		if !confirm {
			if !isInteractive() {
				return fmt.Errorf("TOFU first use: non-interactive mode requires --confirm flag to pin beacon root")
			}
			fmt.Fprint(os.Stderr, "proceed? [Y/n] ")
			scanner := bufio.NewScanner(os.Stdin)
			if scanner.Scan() {
				answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
				if answer == "n" || answer == "no" {
					return fmt.Errorf("aborted: beacon root not pinned")
				}
			}
		}
		return nil
	}

	if cfg.BeaconRoot != beaconRoot {
		// Deviation from pinned root — warn, require confirmation.
		fmt.Fprintf(os.Stderr, "warning: beacon root mismatch\n")
		fmt.Fprintf(os.Stderr, "  pinned:    %s...\n", cfg.BeaconRoot[:minInt(12, len(cfg.BeaconRoot))])
		fmt.Fprintf(os.Stderr, "  requested: %s...\n", beaconRoot[:minInt(12, len(beaconRoot))])
		if !confirm {
			return fmt.Errorf("beacon root does not match pinned root — pass --confirm to proceed or use 'rd join --reset-beacon-root' to re-pin")
		}
	}
	return nil
}

// applyBeaconRootTOFU applies the TOFU beacon root pinning logic.
// cfg is read and updated in-place; if a pin is saved, cfHome is used for rdconfig.Save.
// Returns an error if the user aborts or the root mismatches without --confirm.
//
// DEPRECATED: Use validateBeaconRootTOFU + manual save instead to avoid pinning on failed join.
// This function is kept for backwards compatibility with tests.
//
// Paths:
//   - beaconRoot empty: no-op
//   - cfg.BeaconRoot empty (first use): warn, prompt if interactive and !confirm, pin
//   - cfg.BeaconRoot matches beaconRoot: no-op
//   - cfg.BeaconRoot mismatches beaconRoot + !confirm: error
//   - cfg.BeaconRoot mismatches beaconRoot + confirm: proceed (no error)
func applyBeaconRootTOFU(cfHome string, cfg *rdconfig.Config, beaconRoot string, confirm bool) error {
	if beaconRoot == "" {
		return nil
	}

	if cfg.BeaconRoot == "" {
		// First use of a non-default beacon root — warn and pin (TOFU).
		fmt.Fprintf(os.Stderr, "warning: first use of non-default beacon root %s...\n", beaconRoot[:minInt(12, len(beaconRoot))])
		fmt.Fprintf(os.Stderr, "  this root will be pinned (TOFU) in the config\n")
		fmt.Fprintf(os.Stderr, "  future joins using a different root will require --confirm\n")

		if !confirm {
			if !isInteractive() {
				fmt.Fprintf(os.Stderr, "  non-interactive: pinning beacon root automatically\n")
			} else {
				fmt.Fprint(os.Stderr, "pin this beacon root? [Y/n] ")
				scanner := bufio.NewScanner(os.Stdin)
				if scanner.Scan() {
					answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
					if answer == "n" || answer == "no" {
						return fmt.Errorf("aborted: beacon root not pinned")
					}
				}
			}
		}
		cfg.BeaconRoot = beaconRoot
		if err := rdconfig.Save(cfHome, cfg); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not pin beacon root: %v\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "  pinned beacon root: %s...\n", beaconRoot[:minInt(12, len(beaconRoot))])
		}
		return nil
	}

	if cfg.BeaconRoot != beaconRoot {
		// Deviation from pinned root — warn, require confirmation.
		fmt.Fprintf(os.Stderr, "warning: beacon root mismatch\n")
		fmt.Fprintf(os.Stderr, "  pinned:    %s...\n", cfg.BeaconRoot[:minInt(12, len(cfg.BeaconRoot))])
		fmt.Fprintf(os.Stderr, "  requested: %s...\n", beaconRoot[:minInt(12, len(beaconRoot))])
		if !confirm {
			return fmt.Errorf("beacon root does not match pinned root — pass --confirm to proceed or use 'rd join --reset-beacon-root' to re-pin")
		}
	}
	return nil
}

// resetBeaconRoot clears the pinned beacon root from the config at cfHome.
// Returns the previous value (empty string if nothing was pinned).
// Uses file locking to prevent TOCTOU race (ready-2dc).
func resetBeaconRoot(cfHome string) (prev string, err error) {
	configPath := rdconfig.Path(cfHome)
	lockPath := configPath + ".lock"
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return "", fmt.Errorf("opening lock file: %w", err)
	}
	defer lockFile.Close()

	// Acquire exclusive lock.
	fd := int(lockFile.Fd())
	if err := rdconfig.FlockExclusive(fd); err != nil {
		return "", fmt.Errorf("acquiring lock: %w", err)
	}
	defer rdconfig.FlockUnlock(fd)

	// Load under lock.
	cfg, err := rdconfig.Load(cfHome)
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}
	if cfg.BeaconRoot == "" {
		return "", nil
	}
	prev = cfg.BeaconRoot
	cfg.BeaconRoot = ""
	if err := rdconfig.Save(cfHome, cfg); err != nil {
		return "", fmt.Errorf("saving config: %w", err)
	}
	return prev, nil
}

// validateNameFormat rejects malformed names before any network or resolution
// call. Valid names are either:
//   - 64-char hex campfire IDs, or
//   - cf:// URIs / short names using alphanumeric, dot, hyphen, slash, colon
//     characters (the characters legal in campfire naming URIs).
//
// Rejected inputs (ready-bf5):
//   - Path traversal sequences (../ or ..\)
//   - Null bytes
//   - Names longer than 256 characters
//   - Characters outside the allowed set (for non-hex-ID inputs)
func validateNameFormat(input string) error {
	if len(input) == 0 {
		return fmt.Errorf("name must not be empty")
	}
	if len(input) > 256 {
		return fmt.Errorf("name too long: %d chars (max 256)", len(input))
	}
	// Reject null bytes.
	if strings.ContainsRune(input, '\x00') {
		return fmt.Errorf("name contains null byte")
	}
	// Reject path traversal sequences (both Unix and Windows separators).
	if strings.Contains(input, "../") || strings.Contains(input, `..\`) || input == ".." || strings.HasSuffix(input, "/..") {
		return fmt.Errorf("name contains path traversal sequence")
	}
	// If this is a 64-char hex campfire ID, no further character validation needed.
	if len(input) == 64 && isHex(input) {
		return nil
	}
	// For cf:// URIs and short names, allow: alphanumeric, dot, hyphen, slash, colon.
	for _, c := range input {
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			c == '.' || c == '-' || c == '/' || c == ':') {
			return fmt.Errorf("name contains invalid character %q (allowed: alphanumeric, '.', '-', '/', ':')", c)
		}
	}
	return nil
}

// resolveName resolves a name (cf:// URI, short name, or raw campfire ID) to a
// campfire ID hex string.
func resolveName(client *protocol.Client, input string) (string, error) {
	// Validate name format before any network call (ready-bf5).
	if err := validateNameFormat(input); err != nil {
		return "", fmt.Errorf("invalid name %q: %w", input, err)
	}

	// If it's already a campfire ID (64 hex chars), return as-is.
	if len(input) == 64 && isHex(input) {
		return input, nil
	}

	// Use the naming resolver with the client.
	root, err := naming.LoadOperatorRoot(CFHome())
	rootID := ""
	if err == nil && root != nil {
		rootID = root.CampfireID
	}

	resolver := naming.NewResolverFromClient(client, rootID)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resolved, err := resolver.ResolveOrPassthrough(ctx, input)
	if err != nil {
		return "", err
	}
	// Validate that resolution produced a 64-char hex campfire ID.
	if len(resolved) != 64 || !isHex(resolved) {
		return "", fmt.Errorf("name %q resolved to %q which is not a valid campfire ID (64 hex chars)", input, resolved)
	}
	return resolved, nil
}

// campfireReader is the subset of protocol.Client used by pollForRoleGrant
// and findMembersAdmittedBy. Defined here so tests can inject a fake.
type campfireReader interface {
	Read(req protocol.ReadRequest) (*protocol.ReadResult, error)
}

// pollForRoleGrant polls the campfire for a work:role-grant message targeting
// myPubKey, returning the message ID when found, or an error on timeout.
//
// authorizedSenders is the set of pubkeys (hex) whose role-grant messages are
// trusted. Only messages whose Sender field is in this set are accepted.
// Messages from unauthorized senders are silently ignored, preventing a
// malicious campfire member from posting a fake role-grant (ready-9ce).
func pollForRoleGrant(client campfireReader, campfireID, myPubKey string, authorizedSenders map[string]bool, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	interval := 3 * time.Second

	for time.Now().Before(deadline) {
		result, err := client.Read(protocol.ReadRequest{
			CampfireID: campfireID,
			Tags:       []string{"work:role-grant"},
		})
		if err == nil {
			for _, msg := range result.Messages {
				// Security: only accept role-grants from authorized senders
				// (campfire creator or maintainer). Ignore messages from
				// unauthorized senders to prevent fake role-grant injection.
				if !authorizedSenders[msg.Sender] {
					continue
				}
				if containsTag(msg.Tags, "work:for:"+myPubKey) {
					return msg.ID, nil
				}
				if grantTargets(msg, myPubKey) {
					return msg.ID, nil
				}
			}
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			break
		}
		if interval > remaining {
			interval = remaining
		}
		time.Sleep(interval)
	}

	return "", fmt.Errorf("timed out waiting for role-grant after %s", timeout)
}

// containsTag returns true if the tag slice contains the given tag.
func containsTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// grantTargets returns true if the message payload's pubkey field matches myPubKey
// AND the role is an admission role (not a revocation).
func grantTargets(msg protocol.Message, myPubKey string) bool {
	var payload struct {
		Pubkey string `json:"pubkey"`
		Role   string `json:"role"`
	}
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return false
	}
	if payload.Pubkey != myPubKey {
		return false
	}
	// Reject revocation grants — we're waiting for an admission, not a ban.
	return payload.Role != "revoked" && payload.Role != ""
}

// isHex returns true if s consists entirely of hex characters.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

// isInteractive reports whether stdin is a terminal.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// bootstrapJoinedProject creates .campfire/root, .ready/config.json, and
// .ready/mutations.jsonl so that rd commands work immediately after joining
// a campfire (ready-5cd). This mirrors the essential project state that rd init
// creates, without declarations or summary campfire setup (those belong to the
// project owner).
//
// After writing the local scaffolding, it performs an automatic sync pull using
// client so that rd list shows existing items without requiring a manual
// rd sync pull (ready-5cd). Pull failures are non-fatal — a warning is printed
// to stderr and join succeeds regardless.
func bootstrapJoinedProject(projectDir, campfireID string, client *protocol.Client) error {
	// Create .campfire/ dir and write root.
	campfireDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		return fmt.Errorf("creating .campfire dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(campfireID), 0600); err != nil {
		return fmt.Errorf("writing .campfire/root: %w", err)
	}

	// Create .ready/ dir and write config.json with the campfire ID.
	syncCfg := &rdconfig.SyncConfig{
		CampfireID: campfireID,
	}
	if err := rdconfig.SaveSyncConfig(projectDir, syncCfg); err != nil {
		return fmt.Errorf("saving sync config: %w", err)
	}

	// Create empty mutations.jsonl so rd ready/rd list work immediately.
	mutationsPath := filepath.Join(projectDir, ".ready", "mutations.jsonl")
	if _, err := os.Stat(mutationsPath); os.IsNotExist(err) {
		if err := os.WriteFile(mutationsPath, nil, 0600); err != nil {
			return fmt.Errorf("creating mutations.jsonl: %w", err)
		}
	}

	// Auto-sync: pull existing items from the campfire so rd list shows them
	// immediately without requiring a manual rd sync pull (ready-5cd).
	// Pull failure is non-fatal — join succeeds regardless.
	if client != nil {
		lister := &clientLister{client: client}
		result, pullErr := rdSync.Pull(lister, campfireID, mutationsPath, projectDir, 0)
		if pullErr != nil {
			fmt.Fprintf(os.Stderr, "warning: auto-sync pull failed (run 'rd sync pull' manually): %v\n", pullErr)
		} else if result.Pulled > 0 {
			fmt.Fprintf(os.Stderr, "synced %d item(s)\n", result.Pulled)
		}
	}

	return nil
}

// joinViaInviteToken handles the invite-token join path. It decodes the token,
// writes the pre-provisioned identity to CF_HOME/identity.json, then performs
// the normal join + bootstrap flow using that identity.
func joinViaInviteToken(token string, force bool) error {
	payload, err := decodeInviteToken(token)
	if err != nil {
		return fmt.Errorf("invalid invite token: %w", err)
	}

	// Reconstruct the ed25519 private key from the seed.
	seed, err := hex.DecodeString(payload.PrivateKey)
	if err != nil {
		return fmt.Errorf("decoding private key from token: %w", err)
	}
	privKey := ed25519.NewKeyFromSeed(seed)
	pubKey := privKey.Public().(ed25519.PublicKey)
	pubKeyHex := hex.EncodeToString(pubKey)

	// Write the pre-provisioned identity to CF_HOME.
	cfHome := CFHome()
	idPath := filepath.Join(cfHome, "identity.json")

	if identity.Exists(idPath) && !force {
		return fmt.Errorf("identity already exists at %s — use --force to overwrite", idPath)
	}

	// Snapshot the pre-token identity so we can restore it if the join or
	// redemption check fails.
	oldIdentity, readErr := os.ReadFile(idPath)
	hasOldIdentity := readErr == nil

	id := &identity.Identity{
		PublicKey:  pubKey,
		PrivateKey: privKey,
		CreatedAt:  time.Now().UnixNano(),
	}
	if err := id.Save(idPath); err != nil {
		return fmt.Errorf("writing invite identity: %w", err)
	}

	// Reset the cached protocol client so it picks up the new identity.
	protocolClient = nil

	// restoreIdentity rolls back to the pre-join identity on failure.
	restoreIdentity := func() {
		if hasOldIdentity {
			_ = os.WriteFile(idPath, oldIdentity, 0600)
		} else {
			_ = os.Remove(idPath)
		}
		protocolClient = nil
	}

	client, err := requireClient()
	if err != nil {
		restoreIdentity()
		return fmt.Errorf("join failed, identity restored: %w", err)
	}

	campfireID := payload.CampfireID

	// Server-side TTL check: verify the campfire role-grant for this pubkey has
	// not expired. An attacker who extracts the seed from an expired token and
	// bypasses the client-side CLI check is rejected here because the role-grant
	// message records expires_at server-side (posted by rd invite via postInviteGrant).
	if ttlErr := checkServerSideInviteTTL(client, campfireID, pubKeyHex, payload.ExpiresAt); ttlErr != nil {
		restoreIdentity()
		return ttlErr
	}

	// Attempt to join the campfire.
	_, err = client.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport:  protocol.FilesystemTransport{Dir: resolveTransportDir(campfireID)},
	})
	if err != nil {
		restoreIdentity()
		return fmt.Errorf("join failed, identity restored: %w", err)
	}

	// Bootstrap local project state and auto-sync items (syncs messages so we
	// can check for prior redemption records).
	cwd, cwdErr := os.Getwd()
	if cwdErr == nil {
		if bootstrapErr := bootstrapJoinedProject(cwd, campfireID, client); bootstrapErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not bootstrap project state: %v\n", bootstrapErr)
		}
	}

	// Single-use check: now that we've joined and synced, check for an existing
	// redemption record posted by a prior joiner with the same token.
	if redeemed, checkErr := isInviteTokenRedeemed(client, campfireID, pubKeyHex); checkErr == nil && redeemed {
		restoreIdentity()
		return fmt.Errorf("invite token already redeemed — each token may only be used once")
	}

	// Post our redemption record so subsequent join attempts are detected.
	if postErr := postInviteRedemption(client, campfireID, pubKeyHex); postErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not record invite token redemption: %v\n", postErr)
	}

	// Calculate remaining TTL for display.
	remaining := time.Until(time.Unix(payload.ExpiresAt, 0))
	remainingStr := remaining.Truncate(time.Minute).String()

	displayID := campfireID
	if len(displayID) > 12 {
		displayID = displayID[:12] + "..."
	}
	fmt.Fprintf(os.Stdout, "joined %s via invite token (expires in %s)\n", displayID, remainingStr)
	return nil
}

// inviteRedeemedTag is the campfire message tag used to record invite token redemptions.
// Format: "work:invite-redeemed:<pubkey-hex>"
const inviteRedeemedTag = "work:invite-redeemed"

// checkServerSideInviteTTL reads the campfire for role-grant messages targeting
// pubKeyHex and enforces server-side TTL. Returns an error if a revocation
// exists or if a role-grant with expires_at in the past is found (server-side
// enforcement for attackers who bypass the client-side CLI check).
//
// Best-effort: campfire read errors allow the join to proceed (the client-side
// TTL check in decodeInviteToken already ran).
func checkServerSideInviteTTL(client campfireReader, campfireID, pubKeyHex string, _ int64) error {
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{"work:role-grant"},
	})
	if err != nil {
		return nil // best-effort: allow join if campfire is unreadable
	}

	now := time.Now().Unix()

	for _, msg := range result.Messages {
		var grantPayload struct {
			Pubkey    string `json:"pubkey"`
			Role      string `json:"role"`
			ExpiresAt string `json:"expires_at"`
		}
		if err := json.Unmarshal(msg.Payload, &grantPayload); err != nil {
			continue
		}
		if grantPayload.Pubkey != pubKeyHex {
			continue
		}
		if grantPayload.Role == "revoked" {
			return fmt.Errorf("invite token has been revoked")
		}
		if grantPayload.ExpiresAt != "" {
			t, parseErr := time.Parse(time.RFC3339, grantPayload.ExpiresAt)
			if parseErr == nil && now > t.Unix() {
				return fmt.Errorf("invite token expired at %s (server-side TTL)", grantPayload.ExpiresAt)
			}
		}
	}

	return nil
}

// isInviteTokenRedeemed checks whether the campfire contains a work:invite-redeemed
// message for the given pubkey. Returns (true, nil) if redeemed, (false, nil) if not,
// or (false, err) if the check could not be performed (caller treats errors as not
// redeemed to avoid blocking legitimate first-use joins when the transport is
// temporarily unreadable).
func isInviteTokenRedeemed(client campfireReader, campfireID, pubKeyHex string) (bool, error) {
	redemptionTag := inviteRedeemedTag + ":" + pubKeyHex
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{redemptionTag},
	})
	if err != nil {
		return false, err
	}
	return len(result.Messages) > 0, nil
}

// postInviteRedemption posts a work:invite-redeemed message to the campfire,
// recording that this token (identified by pubKeyHex) has been consumed.
// Subsequent join attempts that call isInviteTokenRedeemed will detect this record.
func postInviteRedemption(client *protocol.Client, campfireID, pubKeyHex string) error {
	redemptionPayload := map[string]string{
		"pubkey":      pubKeyHex,
		"redeemed_at": time.Now().UTC().Format(time.RFC3339),
	}
	payloadJSON, err := json.Marshal(redemptionPayload)
	if err != nil {
		return fmt.Errorf("encoding redemption payload: %w", err)
	}

	redemptionTag := inviteRedeemedTag + ":" + pubKeyHex
	_, err = client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    payloadJSON,
		Tags:       []string{inviteRedeemedTag, redemptionTag},
	})
	return err
}

func init() {
	joinCmd.Flags().Duration("timeout", 5*time.Minute, "how long to wait for admission grant")
	joinCmd.Flags().String("role", "member", "role to request: member or agent")
	joinCmd.Flags().String("root", "", "beacon root campfire ID to use for TOFU pinning")
	joinCmd.Flags().Bool("reset-beacon-root", false, "clear the pinned beacon root")
	joinCmd.Flags().Bool("confirm", false, "confirm beacon root deviation without prompting")
	joinCmd.Flags().Bool("force", false, "overwrite existing identity when joining via invite token")
	rootCmd.AddCommand(joinCmd)
}
