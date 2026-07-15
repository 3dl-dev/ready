package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a ready work project",
	Long: `Initialize a nostr-native ready work project in the current directory.

Work items are stored in a local append-only signed-event log
(.ready/nostr-log.jsonl) and synced over nostr relays. No server and no separate
identity ceremony — the portfolio signing key is created on first use.

  1. Creates the .ready/ directory and config.json
  2. Establishes the local nostr signing identity (if not already present)
  3. Leaves the project ready for 'rd create' immediately

The local signed-event log is the source of truth; relays are a replaceable
cache, so a project works standalone with no reachable relay.

To let a teammate join, run 'rd invite' to mint a one-use token, then they run
'rd join <token>'.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		name, _ := cmd.Flags().GetString("name")
		description, _ := cmd.Flags().GetString("description")
		public, _ := cmd.Flags().GetBool("public")

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
		return initNostr(cwd, name, description, public)
	},
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
func initNostr(cwd, name, description string, public bool) error {
	// Reject double-init.
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
	// Confidential by DEFAULT (ready-216): a new board seals its free text unless
	// --public opts out. The owner's first write mints + self-grants the CEK/LTK.
	syncCfg := &rdconfig.SyncConfig{
		ProjectName: name,
		Board:       coord,
		Public:      public,
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

func init() {
	initCmd.Flags().String("name", "", "project name (default: current directory name)")
	initCmd.Flags().String("description", "", "project description")
	initCmd.Flags().Bool("public", false, "create a PUBLIC board (free text stays plaintext); confidential is the default")
	rootCmd.AddCommand(initCmd)
}
