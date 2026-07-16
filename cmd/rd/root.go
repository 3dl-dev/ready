package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/3dl-dev/ready/pkg/state"
	"github.com/spf13/cobra"
)

// Version is set at build time via -ldflags.
var Version = "dev"

var (
	jsonOutput  bool
	debugOutput bool
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
	rootCmd.PersistentFlags().StringVar(&rdHomeFlag, "rd-home", "", "rd home directory for the nostr identity + config (default: ~/.config/rd)")
}

// RDHome returns the resolved rd home directory — where the nostr signing
// identity (nostr-identity.json) and rd config (rd.json) live. Resolution order:
//
//	(1) --rd-home flag set        → use it
//	(2) RD_HOME env set           → use it
//	(3) walk up from cwd for a repo-local ".rd/" marker dir → use it
//	    (per-worktree identity isolation, keyed on a marker rd owns)
//	(4) default: $XDG_CONFIG_HOME/rd, else ~/.config/rd (XDG)
func RDHome() string {
	dir := resolveRDHome()
	// ready-bf8: a test that resolves RDHome() outside its temp sandbox is about
	// to read/write the REAL nostr identity + config under ~/.config/rd (the
	// case-4 default is independent of cwd, so isolateTempDir's chdir alone does
	// NOT cover it). The guard is nil in production, so this is a no-op there.
	guardResolvedRDHome(dir)
	return dir
}

// resolveRDHome is the pure resolution cascade for RDHome (see RDHome's doc).
func resolveRDHome() string {
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

// rdHomeGuard is a test-only hook, the RDHome() analogue of projectDirGuard.
// TestMain installs it so a test that resolves the rd home outside its temp
// sandbox fails loudly instead of silently touching the real ~/.config/rd
// identity/config (the ready-bf8 leak). Always nil in production.
var rdHomeGuard func(dir string)

// guardResolvedRDHome hands a resolved rd home to the test guard, if installed.
// No-op in production.
func guardResolvedRDHome(dir string) {
	if rdHomeGuard != nil {
		rdHomeGuard(dir)
	}
}

// rdHomeWalkUp walks up from the current working directory looking for a ".rd/"
// marker directory. Returns its path if found, or "" otherwise. Stops at the
// filesystem root. Skips ~/.rd (there is no such default) — the marker is a
// per-project opt-in for worktree-local identity isolation, keyed on a directory
// rd controls.
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

// allProjectItems returns all items, projected from the local nostr log (the
// authoritative source). Empty on a project with no board.
func allProjectItems() ([]*state.Item, error) {
	items, _, err := nostrDualReadAll()
	return items, err
}

// itemByID resolves a single item by ID from the nostr projection.
func itemByID(itemID string) (*state.Item, error) {
	it, ok, err := nostrDualReadByID(itemID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, fmt.Errorf("item %q not found", itemID)
	}
	return it, nil
}
