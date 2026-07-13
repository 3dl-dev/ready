package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/campfire-net/ready/pkg/rdconfig"
)

var joinCmd = &cobra.Command{
	Use:   "join <rd1_token>",
	Short: "Join a project via an invite token",
	Long: `Join a project via a one-use invite token.

'rd join rd1_...' imports the minted secp256k1 identity, pins the board,
adopts the project's relays, and syncs the project's items. An invite token is
the only join path.

EXAMPLES
  rd join rd1_...                     # join via a one-use invite token
  rd join --reset-beacon-root         # clear the pinned beacon root`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		resetRoot, _ := cmd.Flags().GetBool("reset-beacon-root")

		// Handle --reset-beacon-root (config-only; no backend).
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
			return fmt.Errorf("invite token required (use --reset-beacon-root to clear the pinned beacon root)")
		}

		nameOrID := args[0]
		force, _ := cmd.Flags().GetBool("force")

		// A nostr mint-and-ship token (rd1_ prefix) is the SOLE join path: it
		// imports the minted secp256k1 key, pins the board, adopts relays, and
		// syncs (ready-a49).
		if strings.HasPrefix(nameOrID, nostrInviteTokenPrefix) {
			return joinViaNostrInviteToken(nameOrID, force)
		}

		return fmt.Errorf("only invite tokens (rd1_...) are supported — campfire open-join (by name or ID) was retired with the campfire backend")
	},
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

// isHex returns true if s consists entirely of hex characters. Shared by the
// nostr grant/revoke/sessions/audit paths.
func isHex(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}

func init() {
	joinCmd.Flags().Bool("reset-beacon-root", false, "clear the pinned beacon root")
	joinCmd.Flags().Bool("force", false, "overwrite existing identity when joining via invite token")
	rootCmd.AddCommand(joinCmd)
}
