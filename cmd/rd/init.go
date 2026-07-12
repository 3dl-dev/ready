package main

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/admission"
	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	cfhttp "github.com/campfire-net/campfire/cf-protocol/transport/http"
	"github.com/campfire-net/campfire/pkg/beacon"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/spf13/cobra"

	"github.com/campfire-net/ready/pkg/declarations"
	"github.com/campfire-net/ready/pkg/durability"
	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a ready work project",
	Long: `Initialize a ready work project in the current directory.

By default, creates a campfire and links it to the current directory.
Pass --offline to initialize in JSONL-only mode with no campfire required.
Pass --join <beacon> to join an existing project campfire from another machine.

CAMPFIRE MODE (default):
  1. Creates a campfire with reception_requirements: ["work:create"]
  2. Writes .campfire/root (linking this directory to the campfire)
  3. Posts all convention:operation declarations (making the campfire self-describing)
  4. Publishes a beacon for local discovery
  5. Evaluates campfire durability and stores sync config in .ready/config.json
  6. Checks for a home campfire and reports what it finds

  If transport.relay is set in .cf/config.toml (global or project), campfires
  are created on the hosted relay instead of the local filesystem. This enables
  multi-machine portability — the same project campfire is reachable from any
  machine that can reach the relay.

JOIN MODE (--join <beacon>):
  Joins an existing project campfire on this machine. Use the beacon string
  from the machine that ran rd init originally.

OFFLINE MODE (--offline):
  1. Creates .ready/ directory for local JSONL storage
  2. Writes .ready/project.json with project metadata
  No campfire, no network, no identity required.
  Use 'rd sync' later to connect to a campfire.

The project campfire works standalone — no home campfire or naming required.
Use 'rd register' later to add naming when you're ready.

DURABILITY
  rd init evaluates durability tags before configuring sync. If the campfire
  does not meet the minimum requirements (max-ttl:0 + lifecycle:persistent +
  provenance:basic), a warning is printed and you are prompted to confirm.

  Configure via environment:
    RD_CAMPFIRE_TAGS   comma-separated beacon tags (e.g. "durability:max-ttl:0,durability:lifecycle:persistent")
    RD_PROVENANCE      operator provenance level ("getcampfire.dev", "operator-verified", "basic", "unverified")

  Pass --confirm to skip the interactive prompt and proceed regardless.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		description, _ := cmd.Flags().GetString("description")
		offline, _ := cmd.Flags().GetBool("offline")

		// CUTOVER (ready-6ef): the DEFAULT path is nostr-native (no .cf, no campfire).
		// The legacy campfire-creating init is kept compiled but is reachable ONLY via
		// the hidden, explicit `--campfire` flag — never the default path. It exists
		// solely to keep the still-present campfire-org code (summary/inbox campfires,
		// beacons, summary-bind, admit/join topology) test-covered until I7/ready-cb6
		// deletes the campfire code and its tests together. It is NOT a public surface.
		if campfireMode, _ := cmd.Flags().GetBool("campfire"); campfireMode {
			return initCampfire(cmd, args)
		}

		positionalName := ""
		if len(args) > 0 {
			positionalName = args[0]
		}
		cwd, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting cwd: %w", err)
		}
		if positionalName != "" {
			name = positionalName
		}
		if name == "" {
			name = filepath.Base(cwd)
		}
		// JSONL-only escape hatch (no campfire, no nostr log).
		if offline {
			return initOffline(cwd, name, description)
		}
		// CUTOVER (ready-6ef): the default path is nostr-native.
		return initNostr(cwd, name, description)
	},
}

// initCampfire is the LEGACY campfire-creating init, retained but UNINVOKED on the
// default path post-cutover (ready-6ef). It is kept compiled so the campfire
// SDK/executor call sites stay present until I7/ready-cb6 deletes them; the default
// `rd init` no longer calls it. Do not wire it back into the default path.
func initCampfire(cmd *cobra.Command, args []string) error {
	name, _ := cmd.Flags().GetString("name")
	description, _ := cmd.Flags().GetString("description")
	confirm, _ := cmd.Flags().GetBool("confirm")
	offline, _ := cmd.Flags().GetBool("offline")
	beaconRoot, _ := cmd.Flags().GetString("beacon-root")
	joinBeacon, _ := cmd.Flags().GetString("join")

	// Accept optional positional argument as project name.
	positionalName := ""
	if len(args) > 0 {
		positionalName = args[0]
	}

	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("getting cwd: %w", err)
	}

	// Positional argument takes precedence over --name flag.
	if positionalName != "" {
		name = positionalName
	}

	// Default name from current directory.
	if name == "" {
		name = filepath.Base(cwd)
	}

	// --- JSONL-only offline mode ---
	if offline {
		return initOffline(cwd, name, description)
	}

	// --- Join existing project campfire ---
	if joinBeacon != "" {
		return initJoin(cwd, name, joinBeacon)
	}

	// Check we're not already initialized.
	if _, _, ok := projectRoot(); ok {
		return fmt.Errorf(".campfire/root already exists — this project is already initialized")
	}
	// Also check for .ready/ dir (JSONL-only already initialized).
	if _, err := os.Stat(filepath.Join(cwd, ".ready")); err == nil {
		return fmt.Errorf(".ready/ already exists — this project is already initialized (offline mode). Use 'rd sync' to connect to a campfire")
	}

	// --- Auto-join from [rd].beacon in .cf/config.toml ---
	// If the project's .cf/config.toml has [rd].beacon set (typically because
	// machine-1 already ran rd init and committed it), join that campfire
	// instead of creating a new one. Zero-ceremony machine-2 onboarding.
	if autoBeacon, err := rdconfig.LoadProjectBeacon(cwd); err != nil {
		fmt.Fprintf(os.Stderr, "warning: reading .cf/config.toml [rd].beacon: %v\n", err)
	} else if autoBeacon != "" {
		return initJoin(cwd, name, autoBeacon)
	}

	// Load client.
	client, err := requireClient()
	if err != nil {
		return err
	}

	// Resolve relay URL from config cascade.
	relayURL := resolveRelayURL()

	// Default description.
	if description == "" {
		description = name + " work campfire"
	}

	// Choose creation function based on relay config.
	createFn := func(projectDir, joinProtocol string, receptionReqs []string, desc string) (campfireID string, beaconStr string, relayEndpoint string, err error) {
		if relayURL != "" {
			return createRelayCampfire(projectDir, joinProtocol, receptionReqs, desc, relayURL)
		}
		id, localErr := createLocalCampfire(client, projectDir, joinProtocol, receptionReqs, desc)
		return id, "", "", localErr
	}

	// --- Create the campfire (state in ~/.campfire/campfires/<id>/) ---

	campfireDir := filepath.Join(cwd, ".campfire")
	campfireID, mainBeacon, relayEndpoint, err := createFn(campfireDir, "invite-only", []string{"work:create"}, description)
	if err != nil {
		return err
	}

	// --- Create the shadow summary campfire for org observers ---
	summaryCampfireID, _, _, err := createFn("", "invite-only", []string{}, description+" (summary)")
	if err != nil {
		return fmt.Errorf("creating summary campfire: %w", err)
	}

	// --- Write .campfire/root (pointer in the project) ---

	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		return fmt.Errorf("creating .campfire dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(campfireID), 0600); err != nil {
		return fmt.Errorf("writing .campfire/root: %w", err)
	}

	// --- Post convention:operation declarations via transport ---

	payloads, err := declarations.All()
	if err != nil {
		return fmt.Errorf("loading declarations: %w", err)
	}
	declClient, err := requireClient()
	if err != nil {
		return fmt.Errorf("initializing campfire client for declarations: %w", err)
	}
	nDecls := 0
	for _, payload := range payloads {
		_, err := declClient.Send(protocol.SendRequest{
			CampfireID: campfireID,
			Payload:    payload,
			Tags:       []string{"convention:operation"},
		})
		if err != nil {
			return fmt.Errorf("posting declaration %d: %w", nDecls, err)
		}
		nDecls++
	}

	// --- Create the maintainer inbox campfire ---

	inboxCampfireID, _, _, err := createFn("", "invite-only", []string{"work:join-request"}, name+" maintainer inbox")
	if err != nil {
		return fmt.Errorf("creating inbox campfire: %w", err)
	}

	// --- Write join-policy.json to campfire home ---

	jp := &naming.JoinPolicy{
		Policy:          "consult",
		ConsultCampfire: inboxCampfireID,
		JoinRoot:        campfireID,
	}
	if saveErr := naming.SaveJoinPolicy(CFHome(), jp); saveErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not write join-policy.json: %v\n", saveErr)
	}

	// --- Evaluate durability and store sync config ---
	var (
		syncCfg            *rdconfig.SyncConfig
		durabilityWarnings []string
	)
	if relayURL != "" {
		// Relay campfires go through durability evaluation.
		syncCfg, durabilityWarnings, err = evaluateCampfireDurability(campfireID, confirm)
		if err != nil {
			return err
		}
	} else if !isRemoteTransport() {
		// Local filesystem campfires don't expire — skip durability evaluation.
		syncCfg = &rdconfig.SyncConfig{
			CampfireID: campfireID,
			Durability: &rdconfig.DurabilityAssessment{MeetsMinimum: true},
		}
	} else {
		// Legacy remote transport.
		syncCfg, durabilityWarnings, err = evaluateCampfireDurability(campfireID, confirm)
		if err != nil {
			return err
		}
	}

	// --- Register project name in beacon root if configured ---

	if name != filepath.Base(cwd) || positionalName != "" {
		if err := registerProjectName(client, name, campfireID, beaconRoot); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not register project name: %v\n", err)
		}
	}

	// Store project name, summary campfire ID, encryption intent, and relay metadata.
	syncCfg.ProjectName = name
	syncCfg.SummaryCampfireID = summaryCampfireID
	syncCfg.Encrypted = true
	syncCfg.InboxCampfireID = inboxCampfireID
	if relayURL != "" {
		effectiveRelay := relayEndpoint
		if effectiveRelay == "" {
			effectiveRelay = relayURL
		}
		syncCfg.RelayURL = effectiveRelay
		syncCfg.Beacon = mainBeacon
	}
	if saveErr := rdconfig.SaveSyncConfig(cwd, syncCfg); saveErr != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save sync config: %v\n", saveErr)
	}

	// Persist beacon to .cf/config.toml [rd].beacon so machine-2 can clone
	// the repo and run `rd init` with no flags or arguments.
	if mainBeacon != "" {
		if saveErr := rdconfig.SaveProjectBeacon(cwd, mainBeacon); saveErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not write [rd].beacon to .cf/config.toml: %v\n", saveErr)
		}
	}

	// --- Check for home campfire ---

	aliases := naming.NewAliasStore(CFHome())
	homeID, homeErr := aliases.Get("home")
	hasHome := homeErr == nil && homeID != ""

	// --- Output ---

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id":         campfireID,
			"summary_campfire_id": summaryCampfireID,
			"inbox_campfire_id":   inboxCampfireID,
			"encrypted":           true,
			"name":                name,
			"declarations":        nDecls,
			"description":         description,
			"has_home":            hasHome,
			"durability":          syncCfg.Durability,
		}
		if hasHome {
			out["home_campfire_id"] = homeID
		}
		if relayURL != "" {
			out["transport"] = "relay"
			out["relay"] = syncCfg.RelayURL
			if mainBeacon != "" {
				out["beacon"] = mainBeacon
			}
		} else {
			out["transport"] = "filesystem"
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("initialized %s\n", name)
	fmt.Printf("  campfire: %s\n", campfireID[:12]+"...")
	fmt.Printf("  summary campfire: %s\n", summaryCampfireID[:12]+"...")
	fmt.Printf("  inbox campfire: %s\n", inboxCampfireID[:12]+"...")
	if relayURL != "" {
		fmt.Printf("  transport: relay (%s)\n", syncCfg.RelayURL)
		if mainBeacon != "" {
			fmt.Printf("  beacon: %s\n", mainBeacon)
		}
	} else {
		fmt.Printf("  transport: filesystem\n")
	}
	fmt.Printf("  encrypted: true (E2E intent set; SDK encryption enabled when available)\n")
	fmt.Printf("  declarations: %d operations published\n", nDecls)
	if len(durabilityWarnings) > 0 {
		fmt.Println()
		fmt.Println("  sync config stored with warnings (see .ready/config.json)")
	}
	fmt.Println()
	if hasHome {
		fmt.Printf("  home campfire: found (%s)\n", homeID[:12]+"...")
		fmt.Println("  run 'rd register --org <name>' to add naming")
	} else {
		fmt.Println("  home campfire: not found")
		fmt.Println("  your project works standalone. to add naming later:")
		fmt.Println("    rd register --org <name>        create a home and register")
		fmt.Println("    rd register --home <id>         join an existing home")
	}
	if relayURL != "" && mainBeacon != "" {
		fmt.Println()
		fmt.Println("  to join this project on another machine:")
		fmt.Printf("    rd init --join %s\n", mainBeacon)
	}

	return nil
}

// registerProjectName attempts to register the project name through the beacon root.
// If beaconRoot is empty, checks CF_BEACON_ROOT environment variable and returns
// a warning if not configured (not an error — local-only binding is acceptable).
//
// If a beacon root is configured, walks the naming hierarchy to find or create the
// beacon and registers the project campfire under the given name.
func registerProjectName(client *protocol.Client, projectName string, campfireID string, beaconRoot string) error {
	// Determine the beacon root to use.
	if beaconRoot == "" {
		beaconRoot = os.Getenv("CF_BEACON_ROOT")
	}

	if beaconRoot == "" {
		// No beacon root configured — warning only, proceed with local binding.
		fmt.Fprintf(os.Stderr, "warning: no beacon root configured (set CF_BEACON_ROOT or pass --beacon-root) — project name bound locally only\n")
		return nil
	}

	// Beacon root is configured. Attempt to resolve and register under it.
	// This uses pkg/naming to walk the naming hierarchy and post a registration.
	ctx := context.Background()

	// For Wave 1.5, we do local registration only as a placeholder.
	// Full beacon traversal and naming resolution will come in Wave 2 with the
	// convention server. For now, just warn if the root is not local.
	if err := validateLocalBeaconRoot(beaconRoot); err != nil {
		fmt.Fprintf(os.Stderr, "warning: beacon root validation: %v\n", err)
		return nil
	}

	// Register the project name under the beacon root.
	_, err := naming.Register(ctx, client, beaconRoot, projectName, campfireID, nil)
	if err != nil {
		return fmt.Errorf("registering name in beacon: %w", err)
	}

	return nil
}

// validateLocalBeaconRoot checks if the beacon root exists and is resolvable
// as a local campfire ID (64-char hex). For Wave 1.5, this is a validation-only
// placeholder; full naming resolution comes in Wave 2.
func validateLocalBeaconRoot(beaconRoot string) error {
	// Check if it's a valid hex campfire ID (64 chars).
	if len(beaconRoot) == 64 {
		if _, err := hex.DecodeString(beaconRoot); err == nil {
			return nil // Valid local campfire ID.
		}
	}
	// For Wave 1.5, if it's not a valid local ID, return a warning message.
	// Full DNS-like resolution happens in Wave 2.
	return fmt.Errorf("beacon root %q is not a valid local campfire ID — Wave 2 naming resolution required", beaconRoot)
}

// evaluateCampfireDurability reads campfire beacon tags (from RD_CAMPFIRE_TAGS
// environment variable), parses durability metadata, evaluates trust against
// the operator provenance level (from RD_PROVENANCE env), and warns if the
// campfire does not meet minimum sync requirements.
//
// If the campfire does not meet minimum requirements and confirm is false,
// the user is prompted interactively. Returns the SyncConfig to persist and
// any warnings that were displayed.
//
// This function is advisory — it never blocks init if the user confirms
// (or passes --confirm).
func evaluateCampfireDurability(campfireID string, confirm bool) (*rdconfig.SyncConfig, []string, error) {
	// Read campfire tags from environment.
	tags := campfireTagsFromEnv()

	// Read provenance level from environment.
	provenanceLevel := os.Getenv("RD_PROVENANCE")

	// Parse durability tags.
	result, err := durability.ParseTags(tags)
	if err != nil {
		result = &durability.DurabilityResult{Valid: false, Error: err.Error()}
	}

	// Evaluate trust.
	assessment := durability.EvaluateTrust(result, provenanceLevel)

	// Collect all warnings.
	var allWarnings []string
	allWarnings = append(allWarnings, result.Warnings...)
	allWarnings = append(allWarnings, assessment.Warnings...)

	// Build sync config.
	syncCfg := &rdconfig.SyncConfig{
		CampfireID: campfireID,
		Durability: &rdconfig.DurabilityAssessment{
			MeetsMinimum:    assessment.MeetsMinimum,
			Weight:          assessment.Weight,
			MaxTTL:          result.MaxTTL,
			LifecycleType:   result.LifecycleType,
			Warnings:        allWarnings,
			ProvenanceLevel: provenanceLevel,
		},
	}

	if assessment.MeetsMinimum {
		// All criteria met — proceed silently.
		return syncCfg, nil, nil
	}

	// Below minimum: print warnings.
	fmt.Fprintln(os.Stderr, "warning: campfire durability check — sync may be unreliable:")
	if result.MaxTTL != "" && result.MaxTTL != "0" {
		fmt.Fprintf(os.Stderr, "  max-ttl:%s — messages may expire after this period\n", result.MaxTTL)
	}
	if result.LifecycleType == "ephemeral" {
		fmt.Fprintln(os.Stderr, "  lifecycle:ephemeral — this campfire is short-lived")
	}
	for _, w := range assessment.Warnings {
		fmt.Fprintf(os.Stderr, "  %s\n", w)
	}
	fmt.Fprintln(os.Stderr, "  minimum requirements: max-ttl:0 + lifecycle:persistent + provenance:basic or higher")
	fmt.Fprintln(os.Stderr, "  set RD_CAMPFIRE_TAGS and RD_PROVENANCE to configure, or pass --confirm to proceed anyway")

	if confirm {
		return syncCfg, allWarnings, nil
	}

	// Prompt the user interactively.
	fmt.Fprint(os.Stderr, "proceed with sync configuration anyway? [y/N] ")
	scanner := bufio.NewScanner(os.Stdin)
	if scanner.Scan() {
		answer := strings.TrimSpace(strings.ToLower(scanner.Text()))
		if answer != "y" && answer != "yes" {
			return nil, nil, fmt.Errorf("aborted: campfire does not meet minimum durability requirements")
		}
	}

	return syncCfg, allWarnings, nil
}

// campfireTagsFromEnv reads campfire beacon tags from the RD_CAMPFIRE_TAGS
// environment variable. Tags are comma-separated.
// Example: RD_CAMPFIRE_TAGS="durability:max-ttl:0,durability:lifecycle:persistent"
func campfireTagsFromEnv() []string {
	raw := os.Getenv("RD_CAMPFIRE_TAGS")
	if raw == "" {
		return nil
	}
	var tags []string
	for _, t := range strings.Split(raw, ",") {
		t = strings.TrimSpace(t)
		if t != "" {
			tags = append(tags, t)
		}
	}
	return tags
}

// isRemoteTransport reports whether the campfire home directory is configured
// for a remote (hosted) transport. A remote transport is detected by the
// presence of a remote.json file in the CF_HOME directory, which is written
// by cf init when connecting to a hosted campfire server (e.g. getcampfire.dev).
//
// When no remote.json exists, the transport is local filesystem — messages are
// stored as files and never expire, so durability evaluation is unnecessary.
func isRemoteTransport() bool {
	remotePath := filepath.Join(CFHome(), "remote.json")
	_, err := os.Stat(remotePath)
	return err == nil
}

// localCampfireBaseDir returns a persistent base directory for
// campfires (home, ready namespace). Uses CF_TRANSPORT_DIR if set, otherwise
// {CFHome()}/campfires/ (e.g., ~/.cf/campfires/ or ~/.campfire/campfires/ for legacy users).
func localCampfireBaseDir() string {
	if env := os.Getenv("CF_TRANSPORT_DIR"); env != "" {
		return env
	}
	return filepath.Join(CFHome(), "campfires")
}

// createLocalCampfire creates a campfire with the filesystem transport at the
// persistent base directory (~/.campfire/campfires/ by default). Used for both
// project campfires and non-project campfires (home, ready namespace).
//
// projectDir is the project .campfire/ directory. When non-empty, the beacon is
// also published to projectDir/beacons/ for project-local discovery. Pass empty
// string to skip the project-local beacon publish (e.g. for home/namespace campfires).
func createLocalCampfire(client *protocol.Client, projectDir string, joinProtocol string, receptionReqs []string, description string) (string, error) {
	result, err := client.Create(protocol.CreateRequest{
		JoinProtocol:          joinProtocol,
		ReceptionRequirements: receptionReqs,
		Description:           description,
		Transport:             protocol.FilesystemTransport{Dir: localCampfireBaseDir()},
	})
	if err != nil {
		return "", err
	}

	// Project-local beacon publish (client.Create only publishes to ~/.campfire/beacons).
	if projectDir != "" && result.Beacon != nil {
		projectBeaconsDir := filepath.Join(projectDir, "beacons")
		_ = beacon.Publish(projectBeaconsDir, result.Beacon)
	}

	return result.CampfireID, nil
}

// initNostr initializes a nostr-native ready project (ready-6ef cutover). It:
//   - creates .ready/,
//   - loads/creates the secp256k1 OWNER signing key under $RD_HOME (never .cf),
//   - pins the authoritative board coordinate 30301:<owner>:<boardD> in
//     .ready/config.json,
//   - ensures a relay config exists (rd.json under $RD_HOME; relays are a
//     replaceable cache, the local log is authoritative), and
//   - builds and appends the signed 30301 board event to .ready/nostr-log.jsonl.
//
// It writes NO .campfire/ and NO .cf/ — the default post-cutover path provisions
// no campfire identity. boardD equals the project prefix so item ids (create.go)
// and published cards bind to the same pinned board.
func initNostr(cwd, name, description string) error {
	// Reject double-init (campfire-backed or nostr/JSONL-backed).
	if _, _, ok := projectRoot(); ok {
		return fmt.Errorf(".campfire/root already exists — this project is already initialized")
	}
	readyDir := filepath.Join(cwd, ".ready")
	if _, err := os.Stat(readyDir); err == nil {
		return fmt.Errorf(".ready/ already exists — this project is already initialized")
	}

	boardD := projectPrefix(cwd)
	if boardD == "" {
		return fmt.Errorf("cannot derive a board identifier from directory %q (need a name of at least 2 alphanumeric characters)", filepath.Base(cwd))
	}

	if err := os.MkdirAll(readyDir, 0o700); err != nil {
		return fmt.Errorf("creating .ready dir: %w", err)
	}

	// OWNER signing key under $RD_HOME (secp256k1) — no .cf dependency.
	k, err := nostrKey()
	if err != nil {
		return fmt.Errorf("provisioning nostr owner identity: %w", err)
	}
	owner := k.PubKeyHex()
	coord := rdSync.BoardCoord(owner, boardD)

	// Pin the authoritative board coordinate + project name in .ready/config.json.
	syncCfg := &rdconfig.SyncConfig{
		ProjectName: name,
		Board:       coord,
	}
	if err := rdconfig.SaveSyncConfig(cwd, syncCfg); err != nil {
		return fmt.Errorf("writing .ready/config.json: %w", err)
	}

	// Ensure a relay config exists under $RD_HOME (defaults when unset).
	if err := ensureRelayConfig(); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not persist relay config: %v\n", err)
	}

	// Build + append the signed 30301 board event to the authoritative log.
	board := rdSync.BoardSpec{BoardD: boardD, Title: name, Maintainers: []string{owner}}
	be, err := rdSync.BuildBoardEvent(k, board, time.Now().Unix())
	if err != nil {
		return fmt.Errorf("building board event: %w", err)
	}
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(cwd))
	if _, err := log.AppendUnique([]*nostr.Event{be}); err != nil {
		return fmt.Errorf("appending board event to nostr log: %w", err)
	}

	if jsonOutput {
		out := map[string]interface{}{
			"name":        name,
			"description": description,
			"board":       coord,
			"owner":       owner,
			"board_d":     boardD,
			"transport":   "nostr",
			"ready_dir":   readyDir,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("initialized %s (nostr-native)\n", name)
	fmt.Printf("  board: %s\n", coord)
	fmt.Printf("  owner: %s\n", owner)
	fmt.Printf("  log:   %s\n", rdSync.NostrLogPath(cwd))
	fmt.Println()
	fmt.Println("  work items are signed events in .ready/nostr-log.jsonl (the source of truth);")
	fmt.Println("  relays are a replaceable cache. create your first item with:")
	fmt.Println("    rd create \"...\" --type task --priority p1")
	return nil
}

// ensureRelayConfig persists the default nostr relay topology into rd.json under
// $RD_HOME when no relay endpoints are configured yet, so a freshly initialized
// project has an on-disk relay config. It never clobbers an existing config.
func ensureRelayConfig() error {
	home := RDHome()
	cfg, err := rdconfig.Load(home)
	if err != nil {
		cfg = &rdconfig.Config{}
	}
	if len(cfg.RelayEndpoints) > 0 {
		return nil // already configured — do not clobber
	}
	cfg.RelayEndpoints = rdconfig.DefaultRelays()
	return rdconfig.Save(home, cfg)
}

// initOffline initializes a project in JSONL-only mode — no campfire required.
// Creates .ready/ directory and writes .ready/project.json with project metadata.
func initOffline(cwd, name, description string) error {
	readyDir := filepath.Join(cwd, ".ready")

	// Check not already initialized.
	if _, err := os.Stat(readyDir); err == nil {
		return fmt.Errorf(".ready/ already exists — this project is already initialized")
	}
	if _, _, ok := projectRoot(); ok {
		return fmt.Errorf(".campfire/root already exists — this project is already initialized with a campfire")
	}

	if err := os.MkdirAll(readyDir, 0700); err != nil {
		return fmt.Errorf("creating .ready dir: %w", err)
	}

	if description == "" {
		description = name + " work project (offline)"
	}

	projectMeta := map[string]interface{}{
		"name":        name,
		"description": description,
		"mode":        "offline",
		"created_at":  time.Now().UTC().Format(time.RFC3339),
	}
	metaBytes, err := json.MarshalIndent(projectMeta, "", "  ")
	if err != nil {
		return fmt.Errorf("encoding project.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(readyDir, "project.json"), append(metaBytes, '\n'), 0600); err != nil {
		return fmt.Errorf("writing .ready/project.json: %w", err)
	}

	if jsonOutput {
		out := map[string]interface{}{
			"name":        name,
			"description": description,
			"mode":        "offline",
			"ready_dir":   readyDir,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("initialized %s (offline mode)\n", name)
	fmt.Printf("  storage: %s\n", readyDir)
	fmt.Println()
	fmt.Println("  all rd commands work without a campfire.")
	fmt.Println("  to connect to a campfire later, run 'rd init' in this directory.")
	return nil
}

// resolveRelayURL reads transport.relay from the cf config cascade.
// Returns empty string if no relay is configured.
func resolveRelayURL() string {
	cfHome := CFHome()
	cwd, err := os.Getwd()
	if err != nil {
		cwd = cfHome
	}
	cfg, _, _, err := protocol.LoadConfig(cfHome, cwd)
	if err != nil || cfg == nil {
		return ""
	}
	return cfg.Transport.Relay
}

// createRelayCampfire creates a campfire on a hosted relay and stores state locally.
// Returns the campfire ID, beacon string, and effective relay endpoint.
func createRelayCampfire(projectDir, joinProtocol string, receptionReqs []string, description, relayURL string) (campfireID string, beaconStr string, relayEndpoint string, err error) {
	agentID, err := identity.Load(IdentityPath())
	if err != nil {
		return "", "", "", fmt.Errorf("loading identity: %w", err)
	}

	s, err := openStore()
	if err != nil {
		return "", "", "", fmt.Errorf("opening store: %w", err)
	}
	defer s.Close()

	// Create campfire keypair locally.
	cf, err := campfire.New(joinProtocol, receptionReqs, 1)
	if err != nil {
		return "", "", "", fmt.Errorf("creating campfire keypair: %w", err)
	}
	campfireID = cf.PublicKeyHex()

	// Register on relay.
	cfDesc := &cfhttp.CampfireDescriptor{
		CampfireID:            campfireID,
		PrivateKey:            cf.PrivateKey,
		JoinProtocol:          joinProtocol,
		ReceptionRequirements: receptionReqs,
		Threshold:             1,
		Description:           description,
	}
	agentDesc := &cfhttp.AgentDescriptor{
		PublicKeyHex: agentID.PublicKeyHex(),
		PrivateKey:   agentID.PrivateKey,
	}

	resp, err := cfhttp.RegisterOnRelay(relayURL, cfDesc, agentDesc)
	if err != nil {
		return "", "", "", fmt.Errorf("registering on relay %s: %w", relayURL, err)
	}

	effectiveEndpoint := resp.Endpoint
	if effectiveEndpoint == "" {
		effectiveEndpoint = relayURL
	}

	// Store campfire state locally (for message signing).
	baseDir := localCampfireBaseDir()
	transport := fs.New(baseDir)
	if err := transport.Init(cf); err != nil {
		return "", "", "", fmt.Errorf("storing campfire state locally: %w", err)
	}

	// Record p2p-http membership in local store.
	if _, err := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		FSTransport: transport,
		Store:       s,
	}, admission.AdmissionRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            store.PeerRoleCreator,
		JoinProtocol:    joinProtocol,
		TransportDir:    transport.CampfireDir(campfireID),
		TransportType:   "p2p-http",
		Description:     description,
	}); err != nil {
		return "", "", "", fmt.Errorf("recording membership: %w", err)
	}

	// Store relay as peer endpoint.
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: campfireID,
		Endpoint:     effectiveEndpoint,
	}); err != nil {
		return "", "", "", fmt.Errorf("storing relay peer endpoint: %w", err)
	}

	// Publish beacon locally for discovery.
	if projectDir != "" && resp.Beacon != "" {
		b, bErr := parseBeaconString(resp.Beacon)
		if bErr == nil {
			projectBeaconsDir := filepath.Join(projectDir, "beacons")
			_ = beacon.Publish(projectBeaconsDir, b)
		}
	}

	return campfireID, resp.Beacon, effectiveEndpoint, nil
}

// parseBeaconString decodes a beacon:BASE64 string into a Beacon struct.
func parseBeaconString(beaconStr string) (*beacon.Beacon, error) {
	data := beaconStr
	if strings.HasPrefix(data, "beacon:") {
		data = data[len("beacon:"):]
	}
	// Try multiple base64 encodings.
	var raw []byte
	var decErr error
	for _, enc := range []*base64.Encoding{
		base64.RawURLEncoding,
		base64.URLEncoding,
		base64.RawStdEncoding,
		base64.StdEncoding,
	} {
		raw, decErr = enc.DecodeString(data)
		if decErr == nil {
			break
		}
	}
	if raw == nil {
		return nil, fmt.Errorf("decoding beacon base64: %w", decErr)
	}
	var b beacon.Beacon
	if err := cfencoding.Unmarshal(raw, &b); err != nil {
		return nil, fmt.Errorf("unmarshalling beacon: %w", err)
	}
	return &b, nil
}

// initJoin joins an existing project campfire using a beacon string.
// This is the machine-2 path for multi-machine portability.
func initJoin(cwd, name, beaconStr string) error {
	// Check we're not already initialized.
	if _, _, ok := projectRoot(); ok {
		return fmt.Errorf(".campfire/root already exists — this project is already initialized")
	}
	if _, err := os.Stat(filepath.Join(cwd, ".ready")); err == nil {
		return fmt.Errorf(".ready/ already exists — this project is already initialized")
	}

	// Parse the beacon to get campfire ID and transport.
	parsed, err := naming.ParseURI(beaconStr)
	if err != nil {
		return fmt.Errorf("parsing beacon: %w", err)
	}
	campfireID := parsed.CampfireID

	// Decode beacon to read transport hint.
	b, err := parseBeaconString(beaconStr)
	if err != nil {
		return fmt.Errorf("decoding beacon: %w", err)
	}
	if !b.Verify() {
		return fmt.Errorf("beacon signature invalid")
	}

	// Currently only p2p-http (relay) beacons are supported for join.
	if b.Transport.Protocol != "p2p-http" {
		return fmt.Errorf("unsupported beacon transport %q — only relay (p2p-http) beacons are supported for rd init --join", b.Transport.Protocol)
	}
	relayEndpoint, ok := b.Transport.Config["endpoint"]
	if !ok || relayEndpoint == "" {
		return fmt.Errorf("beacon p2p-http transport missing 'endpoint' config key")
	}

	// Open store first — we may not need identity at all if this machine is
	// already a member of the campfire (idempotent re-init below).
	s, err := openStore()
	if err != nil {
		return fmt.Errorf("opening store: %w", err)
	}
	defer s.Close()

	// Idempotent re-init: if this identity is already a member of the campfire
	// on this machine (e.g. .ready/ was wiped but ~/.cf/store.db still has the
	// row), skip the SDK join and the AdmitMember insert — both would fail with
	// UNIQUE constraint violations on campfire_memberships.campfire_id. Refresh
	// the relay peer endpoint (idempotent upsert) and re-link the project.
	existing, err := s.GetMembership(campfireID)
	if err != nil {
		return fmt.Errorf("checking existing membership: %w", err)
	}
	if existing != nil {
		if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
			CampfireID:   campfireID,
			MemberPubkey: campfireID,
			Endpoint:     relayEndpoint,
		}); err != nil {
			return fmt.Errorf("refreshing relay peer endpoint: %w", err)
		}
		return writeJoinedProjectFiles(cwd, name, campfireID, relayEndpoint, beaconStr, true)
	}

	// New member path — needs identity to call cfhttp.Join.
	agentID, err := identity.Load(IdentityPath())
	if err != nil {
		return fmt.Errorf("loading identity: %w", err)
	}

	// Join via HTTP relay.
	result, err := cfhttp.Join(relayEndpoint, campfireID, agentID, "")
	if err != nil {
		return fmt.Errorf("joining campfire via %s: %w", relayEndpoint, err)
	}

	// Store campfire state locally.
	baseDir := localCampfireBaseDir()
	transport := fs.New(baseDir)
	cf := &campfire.Campfire{
		PublicKey:             result.CampfirePubKey,
		PrivateKey:            result.CampfirePrivKey,
		JoinProtocol:          result.JoinProtocol,
		ReceptionRequirements: result.ReceptionRequirements,
		Threshold:             result.Threshold,
	}
	if err := transport.Init(cf); err != nil {
		return fmt.Errorf("storing campfire state: %w", err)
	}

	// Record membership.
	if _, err := admission.AdmitMember(context.Background(), admission.AdmitterDeps{
		FSTransport: transport,
		Store:       s,
	}, admission.AdmissionRequest{
		CampfireID:      campfireID,
		MemberPubKeyHex: agentID.PublicKeyHex(),
		Role:            campfire.RoleFull,
		JoinProtocol:    result.JoinProtocol,
		TransportDir:    transport.CampfireDir(campfireID),
		TransportType:   "p2p-http",
		Description:     name + " (joined via beacon)",
	}); err != nil {
		return fmt.Errorf("recording membership: %w", err)
	}

	// Store relay peer endpoint.
	if err := s.UpsertPeerEndpoint(store.PeerEndpoint{
		CampfireID:   campfireID,
		MemberPubkey: campfireID,
		Endpoint:     relayEndpoint,
	}); err != nil {
		return fmt.Errorf("storing relay peer endpoint: %w", err)
	}

	return writeJoinedProjectFiles(cwd, name, campfireID, relayEndpoint, beaconStr, false)
}

// writeJoinedProjectFiles writes .campfire/root and .ready/config.json for a
// project that is now linked to an existing campfire (whether freshly joined
// or already a member). When relinked is true, the human-readable output says
// "linked" instead of "joined" to clarify that no network join occurred.
func writeJoinedProjectFiles(cwd, name, campfireID, relayEndpoint, beaconStr string, relinked bool) error {
	// Write .campfire/root.
	campfireDir := filepath.Join(cwd, ".campfire")
	if err := os.MkdirAll(campfireDir, 0700); err != nil {
		return fmt.Errorf("creating .campfire dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(campfireID), 0600); err != nil {
		return fmt.Errorf("writing .campfire/root: %w", err)
	}

	// Write .ready/config.json.
	syncCfg := &rdconfig.SyncConfig{
		CampfireID:  campfireID,
		ProjectName: name,
		RelayURL:    relayEndpoint,
		Beacon:      beaconStr,
		Durability:  &rdconfig.DurabilityAssessment{MeetsMinimum: true},
	}
	if saveErr := rdconfig.SaveSyncConfig(cwd, syncCfg); saveErr != nil {
		return fmt.Errorf("saving sync config: %w", saveErr)
	}

	verb := "joined"
	if relinked {
		verb = "linked"
	}

	if jsonOutput {
		out := map[string]interface{}{
			"campfire_id": campfireID,
			"name":        name,
			"transport":   "relay",
			"relay":       relayEndpoint,
			"joined":      !relinked,
			"linked":      relinked,
		}
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(out)
	}

	fmt.Printf("%s %s\n", verb, name)
	fmt.Printf("  campfire: %s\n", campfireID[:12]+"...")
	fmt.Printf("  relay: %s\n", relayEndpoint)
	fmt.Println()
	if relinked {
		fmt.Println("  already a member on this machine — re-linked existing membership")
	}
	fmt.Println("  run 'rd sync pull' to fetch existing items")

	return nil
}

func init() {
	initCmd.Flags().String("name", "", "project name (default: current directory name)")
	initCmd.Flags().String("description", "", "project description")
	initCmd.Flags().Bool("offline", false, "initialize in JSONL-only mode (no nostr log, no campfire)")
	// Hidden, explicit-only escape hatch to the LEGACY campfire-creating init
	// (ready-6ef). The default path is nostr-native; this exists only to keep the
	// vestigial campfire-org code test-covered until I7/ready-cb6 removes it. Not a
	// public surface — do NOT document or wire into the default path.
	initCmd.Flags().Bool("campfire", false, "")
	_ = initCmd.Flags().MarkHidden("campfire")
	rootCmd.AddCommand(initCmd)
}
