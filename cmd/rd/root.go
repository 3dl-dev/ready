package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/3dl-dev/ready/pkg/resolve"
	"github.com/3dl-dev/ready/pkg/state"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var (
	jsonOutput  bool
	debugOutput bool
	rdHome      string
	rdHomeFlag  string
)

var rootCmd = &cobra.Command{
	Use:   "rd",
	Short: "Ready — nostr-native work management",
	Long: `Ready — nostr-native work management for humans and agents.

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
  rd init --name myproject        create a project (one-time)
  rd invite                       mint a one-use token for a teammate to join

Work items live in your project's local signed-event log, synced over nostr
relays. No database, no server.
https://ready.3dl.dev`,
	Version: Version,
}

func init() {
	rootCmd.PersistentFlags().BoolVar(&jsonOutput, "json", false, "output as JSON")
	rootCmd.PersistentFlags().BoolVar(&debugOutput, "debug", false, "show hex IDs for diagnostics")
	rootCmd.PersistentFlags().StringVar(&rdHome, "cf-home", "", "legacy .cf home directory (default: ~/.cf)")
	rootCmd.PersistentFlags().StringVar(&rdHomeFlag, "rd-home", "", "rd home directory for the nostr identity + config (default: ~/.config/rd)")

	// CUTOVER (ready-cb6 I7): the default path is nostr-native and provisions NO
	// campfire client, NO .cf identity, and NO in-process convention server. The
	// former campfire solo-mode server has been removed entirely — work operations
	// are self-signed secp256k1 nostr events, not convention-server-authorized
	// campfire messages. PersistentPreRunE therefore only guards the nostr join
	// token path (so protocol.Init never auto-generates a throwaway .cf identity)
	// and otherwise does nothing.
	rootCmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		_ = args
		return nil
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

// RDHome returns the resolved rd home directory — where the nostr signing
// identity (nostr-identity.json) and rd config (rd.json) live, post-flip. It is
// deliberately INDEPENDENT of CFHome(): the ".cf" dependency is being retired
// (docs/design/nostr-identity-model.md §5), so rd's own identity no longer keys
// off campfire's home. Resolution order (mirrors CFHome's cascade shape but
// rd-native):
//
//	(1) --rd-home flag set        → use it
//	(2) RD_HOME env set           → use it
//	(3) walk up from cwd for a repo-local ".rd/" marker dir → use it
//	    (preserves the per-worktree identity isolation cfHomeWalkUp gave, but
//	    keyed on a marker rd owns instead of campfire's identity.json — see
//	    adversary A7)
//	(4) default: $XDG_CONFIG_HOME/rd, else ~/.config/rd (XDG)
func RDHome() string {
	if rdHomeFlag != "" {
		return rdHomeFlag
	}
	if env := os.Getenv("RD_HOME"); env != "" {
		return env
	}
	if found := rdHomeWalkUp(); found != "" {
		return found
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, "rd")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: cannot determine home directory: %v\n", err)
		os.Exit(1)
	}
	return filepath.Join(home, ".config", "rd")
}

// rdHomeWalkUp walks up from the current working directory looking for a ".rd/"
// marker directory. Returns its path if found, or "" otherwise. Stops at the
// filesystem root. Skips ~/.rd (there is no such default) — the marker is a
// per-project opt-in for worktree-local identity isolation. Unlike cfHomeWalkUp
// (which keys on campfire's identity.json, a file that stops being written
// post-flip), this keys on a directory rd controls, so worktree isolation
// survives the flip (adversary A7).
func rdHomeWalkUp() string {
	dir, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		candidate := filepath.Join(dir, ".rd")
		if fi, err := os.Stat(candidate); err == nil && fi.IsDir() {
			return candidate
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return ""
}

// projectDirGuard is a test-only hook. When non-nil it is called with every real
// project directory the walk-up resolvers (projectRoot, readyProjectDir) are
// about to return. TestMain installs it so a test that resolves project state
// outside its temp sandbox fails loudly instead of silently reading/writing the
// real .ready/ + .campfire/ tree — the ready-b3b leak, where unisolated tests
// running under .claude/worktrees/<agent>/ walked up into the production project
// and minted junk items. It is always nil in production builds (TestMain exists
// only in the test binary), so the walk-up path is unchanged outside `go test`.
var projectDirGuard func(dir string)

// guardResolvedProjectDir hands a resolved project directory to the test guard,
// if one is installed. No-op in production.
func guardResolvedProjectDir(dir string) {
	if projectDirGuard != nil {
		projectDirGuard(dir)
	}
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
			guardResolvedProjectDir(dir)
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

// allItemsFromJSONLOrStore returns all items from the nostr projection (default
// on a nostr-native project, or when RD_NOSTR_READ=1) or the local JSONL log.
//
// CUTOVER (ready-cb6 I7): the campfire store read fallback and cross-campfire
// blocking (a campfire-topology-only feature) have been deleted — a nostr board
// is a single project, so there is no cross-store topology to resolve. The store
// parameter is retained only so the write-path call sites (which still open a
// store for the executor) need not change ahead of the write-path cutover; it is
// unused for resolution.
func allItemsFromJSONLOrStore() ([]*state.Item, error) {
	// DUAL-READ (ready-d65): when RD_NOSTR_READ=1, or on a nostr-native project,
	// resolve the item set from the nostr projection.
	if items, ok, err := nostrDualReadAll(); ok {
		return items, err
	}
	if path := jsonlPath(); path != "" {
		// campfireID may be empty for JSONL-only projects; DeriveFromJSONL handles that.
		campfireID, _, _ := projectRoot()
		return resolve.AllItemsFromJSONL(path, campfireID)
	}
	return nil, nil
}

// byIDFromJSONLOrStore resolves an item by ID from the nostr projection or JSONL.
func byIDFromJSONLOrStore(itemID string) (*state.Item, error) {
	// DUAL-READ (ready-d65): resolve from the nostr projection when active.
	if it, ok, err := nostrDualReadByID(itemID); ok {
		return it, err
	}
	if path := jsonlPath(); path != "" {
		// campfireID may be empty for JSONL-only projects.
		campfireID, _, _ := projectRoot()
		return resolve.ByIDFromJSONL(path, campfireID, itemID)
	}
	return nil, resolve.ErrNotFound{ID: itemID}
}
