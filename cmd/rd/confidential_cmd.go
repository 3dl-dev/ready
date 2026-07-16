package main

// `rd confidential enable|status` — flip an EXISTING board to confidential mode
// (new boards are confidential by default via `rd init`) and report status.

import (
	"fmt"

	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

var confidentialCmd = &cobra.Command{
	Use:    "confidential",
	Hidden: true, // boards are confidential by default at init; this is a rare retrofit/status op
	Short:  "Manage confidential mode for the current board (new boards are confidential by default)",
}

var confidentialEnableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable confidential mode on this board: mint the board key and seal future free text",
	Long: "Marks this board confidential and, when run by the OWNER, mints the per-board\n" +
		"CEK+LTK now (published as an owner self-grant so it is recoverable from the log).\n" +
		"Existing plaintext cards stay readable (grandfathered); future writes seal their\n" +
		"free text. Grant members with `rd grant` so they receive the read key.",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, ok := readyProjectDir()
		if !ok {
			return errNotNostrProject()
		}
		cfg, err := rdconfig.LoadSyncConfig(dir)
		if err != nil {
			return err
		}
		if cfg.Board == "" {
			return fmt.Errorf("no pinned board in this project; run `rd init` first")
		}
		cfg.Public = false // confidential is the default; clear any opt-out
		if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
			return err
		}
		pub, ok, err := nostrPublisher()
		if err != nil {
			return err
		}
		if !ok {
			return errNotNostrProject()
		}
		boardAuthor, boardD, err := resolveBoardAuthorD(dir, pub.Key.PubKeyHex())
		if err != nil {
			return err
		}
		coord := rdSync.BoardCoord(boardAuthor, boardD)
		if pub.Key.PubKeyHex() != boardAuthor {
			fmt.Printf("board %s marked confidential — only the OWNER can mint the CEK; ask the owner to run `rd confidential enable`\n", coord)
			return nil
		}
		// Bootstrap the CEK now (idempotent: no-op if already bootstrapped).
		env, err := boardConfidentialEnvelope(dir, pub, boardAuthor, boardD)
		if err != nil {
			return err
		}
		if env != nil {
			fmt.Printf("confidential mode ENABLED for board %s (epoch %d)\n", coord, env.Epoch)
			fmt.Println("  future writes seal title/description/reason; existing plaintext cards stay readable (grandfathered).")
		} else {
			fmt.Printf("board %s marked confidential\n", coord)
		}
		return nil
	},
}

var confidentialStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show whether this board is confidential and which CEK epoch you hold",
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, ok := readyProjectDir()
		if !ok {
			return errNotNostrProject()
		}
		cfg, err := rdconfig.LoadSyncConfig(dir)
		if err != nil {
			return err
		}
		if cfg.Board == "" {
			return fmt.Errorf("no pinned board in this project")
		}
		if cfg.Public {
			fmt.Printf("board %s is PUBLIC (free text is plaintext; --public opt-out)\n", cfg.Board)
			return nil
		}
		k, err := nostrKey()
		if err != nil {
			return err
		}
		events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
		if err != nil {
			return err
		}
		kr := boardReadKeyring(dir, k, events)
		owner, boardD, okc := rdSync.ParseBoardCoord(cfg.Board)
		if !okc {
			return fmt.Errorf("malformed pinned board coordinate %q", cfg.Board)
		}
		coord := rdSync.BoardCoord(owner, boardD)
		if ep, _, ok := kr.CurrentEpoch(coord); ok {
			fmt.Printf("board %s is CONFIDENTIAL (epoch %d; you hold the read key)\n", coord, ep)
		} else if _, confidential := kr.Cutover(coord); confidential {
			fmt.Printf("board %s is CONFIDENTIAL (you hold NO read key — ask the owner to `rd grant` %s)\n", coord, k.PubKeyHex())
		} else {
			fmt.Printf("board %s is marked confidential but not yet bootstrapped (the owner has not written yet)\n", coord)
		}
		return nil
	},
}

func init() {
	confidentialCmd.AddCommand(confidentialEnableCmd)
	confidentialCmd.AddCommand(confidentialStatusCmd)
	rootCmd.AddCommand(confidentialCmd)
}
