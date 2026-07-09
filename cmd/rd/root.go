package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	cfauthprov "github.com/campfire-net/campfire/cf-conventions/cf-authority/provenance"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/pkg/naming"
	cfprov "github.com/campfire-net/campfire/pkg/provenance"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"
	"github.com/campfire-net/ready/pkg/conventionserver"
	"github.com/campfire-net/ready/pkg/crossdep"
	"github.com/campfire-net/ready/pkg/declarations"
	"github.com/campfire-net/ready/pkg/provenance"
	"github.com/campfire-net/ready/pkg/rdconfig"
	"github.com/campfire-net/ready/pkg/resolve"
	"github.com/campfire-net/ready/pkg/state"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var (
	jsonOutput     bool
	debugOutput    bool
	rdHome         string
	protocolClient *protocol.Client
)

var rootCmd = &cobra.Command{
	Use:   "rd",
	Short: "Ready — work management on campfire",
	Long: `Ready — work management as a campfire convention.

LIFECYCLE
  The work item lifecycle is: create → claim → close.

  rd create "Fix auth bug" --type task --priority p0
  rd claim <id>
  rd close <id> --reason "Was checking issuer, not audience"

QUERY
  rd ready                        what needs attention now
  rd list                         all open items
  rd list --status active --json  filtered, machine-readable
  rd show <id>                    full item details

DELEGATION
  rd delegate <id> --to <identity>
  rd ready --view delegated       see what you've delegated
  rd ready --view my-work         see what's assigned to you

RESUMING WORK (for agents and humans returning to a project)
  rd ready                        what needs attention — start here
  rd ready --view work            what's actively being worked on
  rd show <id>                    full item spec, done condition, audit trail
  rd ready --view my-work --json  items assigned to your identity

  If you're an agent resuming after context loss: run rd ready --view work
  to find your in-progress item. Run rd show <id> to read the full spec.
  The item description is self-contained — it has everything you need.

SETUP
  rd init --name myproject        create a work campfire (one-time)
  rd register --org <name>        add to an org for multi-project (optional)

Work items live in your project's campfire. No database, no server.
https://ready.getcampfire.dev`,
	Version: Version,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	rootCmd.PersistentFlags().BoolVar(&debugOutput, "debug", false, "show hex IDs for diagnostics")
	rootCmd.PersistentFlags().StringVar(&rdHome, "cf-home", "", "campfire home directory (default: ~/.cf)")

	// Wire in the in-process convention server for solo mode.
	// PersistentPreRunE runs before every subcommand; if the client initializes
	// successfully and we're in solo mode, the server starts as a background goroutine
	// tied to the command's context (cancelled when the command exits).
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		// Skip protocol.Init (and its auto-identity generation) when the user is
		// joining via an invite token. The token carries a pre-provisioned identity;
		// joinViaInviteToken writes it to disk and then calls requireClient() itself.
		// If we let protocol.Init run here first, it auto-generates a throwaway
		// identity, causing joinViaInviteToken to see an "existing" identity and
		// refuse without --force. (ready-167)
		if cmd.Name() == "join" && len(args) > 0 && strings.HasPrefix(args[0], inviteTokenPrefix) {
			return nil
		}

		client, err := requireClient()
		if err != nil {
			// Client init failure is non-fatal here — individual commands report it.
			return nil
		}
		requireConventionServer(cmd.Context(), client)
		return nil
	}
}

// requireConventionServer starts the in-process convention server in solo mode.
// Solo mode is detected via conventionserver.IsSoloMode: if no convention:server-binding
// exists other than our own, we start the in-process server so work operations are
// self-authorized. Loads InboxCampfireID from sync config to activate the inbox watcher.
func requireConventionServer(ctx context.Context, client *protocol.Client) {
	campfireID, projectDir, hasCampfire := projectRoot()
	if !hasCampfire || campfireID == "" {
		// JSONL-only mode — no campfire, no server needed.
		return
	}

	var opts []conventionserver.ServerOption
	if syncCfg, err := rdconfig.LoadSyncConfig(projectDir); err == nil && syncCfg != nil {
		if syncCfg.InboxCampfireID != "" {
			// Only enable inbox watcher if we're a member of the inbox campfire.
			// Non-owner joiners are not members of the owner's inbox campfire, and
			// attempting to validate membership would produce a confusing warning.
			if m, err := client.GetMembership(syncCfg.InboxCampfireID); err == nil && m != nil {
				opts = append(opts, conventionserver.WithInboxCampfireID(syncCfg.InboxCampfireID))
			}
		}
		if syncCfg.SummaryCampfireID != "" {
			opts = append(opts, conventionserver.WithSummaryCampfireID(syncCfg.SummaryCampfireID))
		}
	}

	srv, err := conventionserver.New(client, campfireID, opts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not start in-process convention server: %v\n", err)
		return
	}

	if !conventionserver.IsSoloMode(client, campfireID, srv.PubKeyHex()) {
		// A remote convention server is present — don't start a local one.
		return
	}

	if err := srv.Start(ctx); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not start in-process convention server: %v\n", err)
		return
	}
}

// CFHome returns the resolved campfire home directory.
// Detection order:
// (1) rdHome flag set → use it
// (2) CF_HOME env set → use it
// (3) walk up from cwd looking for .cf/identity.json → use that .cf/
// (4) ~/.cf exists → use it (new install path)
// (5) ~/.campfire exists → use it (legacy user migration path)
// (6) neither → default to ~/.cf
func CFHome() string {
	if rdHome != "" {
		return rdHome
	}
	if env := os.Getenv("CF_HOME"); env != "" {
		return env
	}

	// Walk up from cwd looking for a .cf/ directory containing identity.json.
	// This enables per-worktree identity isolation without CF_HOME env vars.
	if found := cfHomeWalkUp(); found != "" {
		return found
	}

	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	newPath := filepath.Join(home, ".cf")
	legacyPath := filepath.Join(home, ".campfire")

	if _, err := os.Stat(newPath); err == nil {
		return newPath
	}
	if _, err := os.Stat(legacyPath); err == nil {
		return legacyPath
	}
	return newPath
}

// cfHomeWalkUp walks up from the current working directory looking for a .cf/
// directory that contains identity.json. Returns the .cf/ path if found, or
// empty string if not. Stops at the filesystem root. Skips ~/.cf to avoid
// short-circuiting the global fallback logic.
func cfHomeWalkUp() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	home, _ := os.UserHomeDir()
	for {
		candidate := filepath.Join(dir, ".cf")
		// Skip ~/.cf — that's handled by the global fallback path.
		if home == "" || candidate != filepath.Join(home, ".cf") {
			if _, err := os.Stat(filepath.Join(candidate, "identity.json")); err == nil {
				return candidate
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// IdentityPath returns the path to the identity file.
func IdentityPath() string {
	return filepath.Join(CFHome(), "identity.json")
}

// openStore opens the campfire store at the default path.
func openStore() (store.Store, error) {
	s, err := store.Open(store.StorePath(CFHome()))
	if err != nil {
		return nil, fmt.Errorf("opening store: %w", err)
	}
	return s, nil
}

// requireAgentAndStore loads the agent identity and opens the store.
func requireAgentAndStore() (*identity.Identity, store.Store, error) {
	agentID, err := identity.Load(IdentityPath())
	if err != nil {
		return nil, nil, fmt.Errorf("loading identity (run 'cf init' first): %w", err)
	}
	s, err := store.Open(store.StorePath(CFHome()))
	if err != nil {
		return nil, nil, fmt.Errorf("opening store: %w", err)
	}
	return agentID, s, nil
}

// withAgentAndStore loads the agent identity and opens the store, then calls fn
// with both. The store is automatically closed when fn returns, even if fn returns
// an error. This helper reduces boilerplate for the common pattern:
//
//	agentID, s, err := requireAgentAndStore()
//	if err != nil {
//		return err
//	}
//	defer s.Close()
func withAgentAndStore(fn func(*identity.Identity, store.Store) error) error {
	agentID, s, err := requireAgentAndStore()
	if err != nil {
		return err
	}
	defer s.Close()
	return fn(agentID, s)
}

// requireExecutor returns a convention.Executor backed by the protocol client,
// with a ProvenanceChecker wired in so that min_operator_level gates are
// enforced correctly.
//
// The checker reads work:role-grant messages from the local store. The campfire
// creator (from Membership.CreatorPubkey) is bootstrapped as maintainer (level 2).
// All others default to contributor (level 1) until an explicit role-grant
// message says otherwise.
func requireExecutor() (*convention.Executor, *protocol.Client, error) {
	client, err := requireClient()
	if err != nil {
		return nil, nil, err
	}
	exec := convention.NewExecutor(client)

	// Wire in provenance checking so that min_operator_level gates work.
	// Best-effort: if we can't open the store or find the campfire, fall back to
	// no provenance checker rather than blocking all operations.
	if campfireID, _, ok := projectRoot(); ok && campfireID != "" {
		s, storeErr := openStore()
		if storeErr == nil {
			defer s.Close()
			var creatorKey string
			if m, memErr := s.GetMembership(campfireID); memErr == nil && m != nil {
				creatorKey = m.CreatorPubkey
			}
			checker, checkerErr := provenance.NewStoreChecker(s, campfireID, creatorKey)
			if checkerErr == nil {
				// Wire cf-authority's production ProvenanceCheckerV2 (ready-cd9),
				// backed by rd's role-based level source. Numeric levels are
				// preserved, so min_operator_level gating is unchanged.
				exec = exec.WithProvenanceV2(cfauthprov.NewChecker(rdLevelSource{checker}))
			}
		}
	}

	return exec, client, nil
}

// rdLevelSource adapts rd's role-based StoreChecker (Level → int) to the
// cf-authority provenance.LevelSource interface (Level → cfprov.Level). The
// numeric level is preserved, so min_operator_level comparisons are unchanged.
type rdLevelSource struct {
	inner *provenance.StoreChecker
}

func (s rdLevelSource) Level(keyHex string) cfprov.Level {
	return cfprov.Level(s.inner.Level(keyHex))
}

// loadDeclaration loads a convention declaration by operation name from the
// embedded declarations package and parses it for use with convention.Executor.
// The name corresponds to the operation name (e.g. "create", "claim", "gate-resolve").
func loadDeclaration(name string) (*convention.Declaration, error) {
	data, err := declarations.Load(name)
	if err != nil {
		return nil, err
	}
	decl, _, err := convention.Parse([]string{"convention:operation"}, data, "", "", convention.DefaultDeniedTagPrefixes)
	if err != nil {
		return nil, fmt.Errorf("parsing declaration %q: %w", name, err)
	}
	return decl, nil
}

// requireClient returns a *protocol.Client backed by the campfire home directory.
// The client is cached after first initialization (CLI is single-threaded).
// Walk-up is enabled by default; WithAuthorizeFunc wires the center-campfire
// authorization flow.
func requireClient() (*protocol.Client, error) {
	if protocolClient != nil {
		return protocolClient, nil
	}
	c, _, err := protocol.Init(CFHome(), protocol.WithAuthorizeFunc(centerAuthorize))
	if err != nil {
		return nil, fmt.Errorf("initializing campfire client: %w", err)
	}
	protocolClient = c
	return c, nil
}

// centerAuthorize is the recentering authorize hook. It prompts the user once
// when Init() detects an unlinked center campfire. In non-interactive contexts
// (pipes, agents) it returns false to skip silently.
func centerAuthorize(description string) (bool, error) {
	if !isatty.IsTerminal(os.Stdin.Fd()) {
		return false, nil
	}
	fmt.Fprintf(os.Stderr, "rd: %s [y/N] ", description)
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return false, nil
	}
	return strings.EqualFold(strings.TrimSpace(scanner.Text()), "y"), nil
}

// readyProjectDir walks up from cwd looking for a .ready/ directory.
// Returns (projectDir, true) if found. This covers both campfire-backed
// projects (which have .campfire/root AND .ready/) and JSONL-only projects
// (which have only .ready/).
func readyProjectDir() (string, bool) {
	// First try via campfire root (campfire-backed projects).
	if _, dir, ok := projectRoot(); ok {
		if _, err := os.Stat(filepath.Join(dir, ".ready")); err == nil {
			return dir, true
		}
		// Campfire exists but .ready/ not yet created — still return the dir so
		// it can be created on first write.
		return dir, true
	}
	// Walk up looking for a .ready/ directory (JSONL-only projects).
	dir, err := os.Getwd()
	if err != nil {
		return "", false
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".ready")); err == nil {
			return dir, true
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", false
}

// jsonlPath returns the path to .ready/mutations.jsonl for the current project.
// Returns an empty string if no project root is found (not initialized).
func jsonlPath() string {
	dir, ok := readyProjectDir()
	if !ok {
		return ""
	}
	return filepath.Join(dir, ".ready", "mutations.jsonl")
}

// pendingPath returns the path to .ready/pending.jsonl for the current project.
// Returns an empty string if no project root is found.
func pendingPath() string {
	dir, ok := readyProjectDir()
	if !ok {
		return ""
	}
	return filepath.Join(dir, ".ready", "pending.jsonl")
}

// allItemsFromJSONLOrStore returns all items, preferring JSONL when a project
// root exists, falling back to the campfire store when it does not.
func allItemsFromJSONLOrStore(s store.Store) ([]*state.Item, error) {
	// DUAL-READ (ready-d65): when RD_NOSTR_READ=1 this SINGLE process resolves the
	// item set from the nostr projection instead of campfire/JSONL. Additive and
	// off by default — campfire stays the authoritative default backend, so the
	// live campfire-backed rd is never disturbed. This is the controlled nostr-only
	// verification context: rd's whole read surface (list/ready/show) runs against
	// nostr without flipping the live default.
	if items, ok, err := nostrDualReadAll(); ok {
		if err != nil {
			return items, err
		}
		// Apply cross-campfire blocking on the nostr dual-read set too (ready-187):
		// the JSONL and campfire paths both run crossdep.ApplyBlocking, so without
		// this the nostr-backed read surface computed a DIFFERENT blocked set for any
		// cross-project dep — an item blocked by an out-of-project blocker would show
		// actionable on nostr but blocked on campfire. ApplyBlocking short-circuits
		// when no item carries a cross-campfire warning, so this is a no-op for
		// single-project reads.
		aliases := naming.NewAliasStore(CFHome())
		crossdep.ApplyBlocking(items, s, aliases)
		return items, nil
	}
	if path := jsonlPath(); path != "" {
		// campfireID may be empty for JSONL-only projects; DeriveFromJSONL handles that.
		campfireID, _, _ := projectRoot()
		items, err := resolve.AllItemsFromJSONL(path, campfireID)
		if err != nil {
			return nil, err
		}
		// Apply cross-campfire blocking even in JSONL mode: the store holds
		// memberships and messages for all campfires, so cross-project deps
		// can be resolved and applied as blocking status.
		aliases := naming.NewAliasStore(CFHome())
		crossdep.ApplyBlocking(items, s, aliases)
		return items, nil
	}
	items, err := resolve.AllItems(s)
	if err != nil {
		return nil, err
	}

	// Apply cross-campfire blocking: resolve cross-campfire deps and
	// mark items as blocked when the blocker is non-terminal.
	aliases := naming.NewAliasStore(CFHome())
	crossdep.ApplyBlocking(items, s, aliases)

	return items, nil
}

// byIDFromJSONLOrStore resolves an item by ID, preferring JSONL when available.
func byIDFromJSONLOrStore(s store.Store, itemID string) (*state.Item, error) {
	// DUAL-READ (ready-d65): resolve from the nostr projection when RD_NOSTR_READ=1.
	if it, ok, err := nostrDualReadByID(itemID); ok {
		return it, err
	}
	if path := jsonlPath(); path != "" {
		// campfireID may be empty for JSONL-only projects.
		campfireID, _, _ := projectRoot()
		return resolve.ByIDFromJSONL(path, campfireID, itemID)
	}
	return resolve.ByID(s, itemID)
}

// byIDFromJSONLOrStoreExact resolves an item by exact ID only — no prefix
// expansion. Use for security-sensitive operations (e.g. admit) where a prefix
// collision could allow an attacker to substitute a crafted item.
func byIDFromJSONLOrStoreExact(s store.Store, itemID string) (*state.Item, error) {
	if path := jsonlPath(); path != "" {
		campfireID, _, _ := projectRoot()
		return resolve.ByIDFromJSONLExact(path, campfireID, itemID)
	}
	return resolve.ByIDExact(s, itemID)
}
