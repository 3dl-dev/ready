package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
		relays, _ := cmd.Flags().GetStringArray("relay")
		local, _ := cmd.Flags().GetBool("local")

		if len(relays) > 0 && local {
			return fmt.Errorf("--local and --relay are mutually exclusive")
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
		return initNostr(cwd, name, description, public, relays, local)
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
func initNostr(cwd, name, description string, public bool, relays []string, local bool) error {
	// Reject double-init.
	if _, _, ok := projectRoot(); ok {
		return fmt.Errorf(".campfire/root already exists — this project is already initialized")
	}
	readyDir := filepath.Join(cwd, ".ready")
	if _, err := os.Stat(readyDir); err == nil {
		return fmt.Errorf(".ready/ already exists — this project is already initialized")
	}

	// ready-b3b: init writes real project state (.ready/, config.json,
	// nostr-log.jsonl) directly at cwd without going through the walk-up
	// resolvers, so funnel cwd through the same sandbox guard. No-op in
	// production; in tests it fails loudly if an unisolated init would mint a
	// project tree outside the temp sandbox (the guard's protection would
	// otherwise be bypassed on a nostr-native tree that carries no .campfire/root).
	guardResolvedProjectDir(cwd)

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

	// Resolve the relay choice (local-only vs BYOR) — per-project, so a --local
	// project never inherits another project's relays.
	eps := resolveRelayEndpoints(relays, local)

	// Pin the authoritative board coordinate + project name + relays in
	// .ready/config.json. Confidential by DEFAULT (ready-216): a new board seals its
	// free text unless --public opts out. The owner's first write mints the CEK/LTK.
	syncCfg := &rdconfig.SyncConfig{
		ProjectName:    name,
		Board:          coord,
		Public:         public,
		RelayEndpoints: eps,
	}
	if err := rdconfig.SaveSyncConfig(cwd, syncCfg); err != nil {
		return fmt.Errorf("writing .ready/config.json: %w", err)
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

	mode := "confidential"
	if public {
		mode = "public"
	}
	fmt.Printf("initialized %s (%s)\n", name, mode)
	fmt.Printf("  board: %s\n", coord)
	fmt.Printf("  owner: %s\n", owner)
	fmt.Printf("  log:   %s\n", rdSync.NostrLogPath(cwd))
	fmt.Println()
	if public {
		fmt.Println("  free text is PLAINTEXT on this board (--public). work items are signed events")
		fmt.Println("  in .ready/nostr-log.jsonl (the source of truth); relays are a replaceable cache.")
	} else {
		fmt.Println("  free text is ENCRYPTED — only granted members can read it. work items are signed")
		fmt.Println("  events in .ready/nostr-log.jsonl (the source of truth); relays cache ciphertext.")
	}
	fmt.Println("  create your first item with:")
	fmt.Println("    rd create \"...\" --type task --priority p1")
	return nil
}

// resolveRelayEndpoints resolves the relay choice for a fresh project into the
// endpoints to store in THIS project's .ready/config.json. The ship default is
// LOCAL-ONLY — the binary bakes in no relay topology. Resolution order:
//   - --relay <url> (repeatable): use exactly those (BYOR), skip the prompt.
//   - --local: explicit local-only, skip the prompt.
//   - interactive terminal, neither flag: prompt for relay URL(s), Enter = local.
//   - non-interactive, neither flag: local-only (never blocks a scripted init).
func resolveRelayEndpoints(relays []string, local bool) []rdconfig.RelayEndpoint {
	// Prompt only when interactive, not in --json mode, and no explicit choice was
	// given. JSON mode is non-interactive by contract — prompting there would also
	// corrupt the JSON on stdout.
	if len(relays) == 0 && !local && !jsonOutput && isInteractive() {
		relays = promptRelays()
	}

	eps := make([]rdconfig.RelayEndpoint, 0, len(relays))
	for _, u := range relays {
		if u = strings.TrimSpace(u); u != "" {
			eps = append(eps, rdconfig.RelayEndpoint{URL: u, Read: true, Write: true})
		}
	}

	if !jsonOutput {
		if len(eps) == 0 {
			fmt.Println("  relays: none (local-only). the signed log is the source of truth;")
			fmt.Println("          add relays anytime by editing .ready/config.json.")
		} else {
			fmt.Printf("  relays: %d configured (read+write)\n", len(eps))
			for _, e := range eps {
				fmt.Printf("          %s\n", e.URL)
			}
		}
	}
	return eps
}

// isInteractive reports whether stdin is a terminal, so a scripted/agent 'rd
// init' never blocks on a prompt.
func isInteractive() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice != 0
}

// promptRelays asks the user for comma/space-separated relay URL(s). An empty
// line means local-only.
func promptRelays() []string {
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Relays sync this project across machines and teammates (optional).")
	fmt.Fprintln(os.Stderr, "The local signed log works standalone, so this is safe to skip.")
	fmt.Fprint(os.Stderr, "Enter relay URL(s) [comma-separated], or press Enter for local-only: ")
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadString('\n')
	if err != nil && line == "" {
		return nil
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return nil
	}
	return splitRelayList(line)
}

// splitRelayList splits a relay list on commas and whitespace, dropping blanks.
func splitRelayList(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		if f = strings.TrimSpace(f); f != "" {
			out = append(out, f)
		}
	}
	return out
}

func init() {
	initCmd.Flags().String("name", "", "project name (default: current directory name)")
	initCmd.Flags().String("description", "", "project description")
	initCmd.Flags().Bool("public", false, "create a PUBLIC board (free text stays plaintext); confidential is the default")
	initCmd.Flags().StringArray("relay", nil, "relay URL to sync through (repeatable); omit for local-only. BYOR — no relay is baked in")
	initCmd.Flags().Bool("local", false, "local-only: configure no relays (skips the interactive prompt)")
	rootCmd.AddCommand(initCmd)
}
