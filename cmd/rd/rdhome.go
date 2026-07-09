package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/rdconfig"
	"github.com/spf13/cobra"
)

// rdHomeMigrationPlan describes what a first-run home migration would do: copy an
// existing legacy ".cf" nostr identity (and rd.json) FORWARD into the new rd home
// ($RD_HOME), identity-preservingly. It is computed as a pure function of the
// filesystem so `rd migrate-home --dry-run` can print it without side effects.
type rdHomeMigrationPlan struct {
	RDHome        string `json:"rd_home"`
	LegacyHome    string `json:"legacy_home"`
	NewKeyPath    string `json:"new_key_path"`
	LegacyKeyPath string `json:"legacy_key_path"`
	NewCfgPath    string `json:"new_config_path"`
	LegacyCfgPath string `json:"legacy_config_path"`
	// KeyNeedsCopy is true when the legacy key exists and the new one does not —
	// the only case a copy is performed. If the new key already exists the plan is
	// a no-op (the identity has already migrated, or was freshly generated here).
	KeyNeedsCopy bool `json:"key_needs_copy"`
	// KeyAlreadyPresent is true when $RD_HOME already holds a key (migration is a
	// no-op regardless of any legacy key — we NEVER overwrite an existing identity).
	KeyAlreadyPresent bool `json:"key_already_present"`
	CfgNeedsCopy      bool `json:"config_needs_copy"`
}

// planRDHomeMigration computes the migration plan for the given rd home. The
// legacy source is CFHome() — the resolved OLD ".cf" (or ~/.campfire) location —
// so the migration reuses the existing, well-tested campfire-home cascade to find
// the pre-flip key instead of hardcoding ~/.cf (which would ignore CF_HOME / the
// per-worktree override and, in tests, read the real home).
func planRDHomeMigration(rdHome string) rdHomeMigrationPlan {
	legacyHome := CFHome()
	p := rdHomeMigrationPlan{
		RDHome:        rdHome,
		LegacyHome:    legacyHome,
		NewKeyPath:    nostr.DefaultKeyPath(rdHome),
		LegacyKeyPath: nostr.DefaultKeyPath(legacyHome),
		NewCfgPath:    rdconfig.Path(rdHome),
		LegacyCfgPath: rdconfig.Path(legacyHome),
	}
	p.KeyAlreadyPresent = fileExists(p.NewKeyPath)
	// Only copy when the legacy key exists, the new one is absent, AND the legacy
	// home is genuinely a different directory (else there is nothing to migrate).
	p.KeyNeedsCopy = !p.KeyAlreadyPresent && p.LegacyKeyPath != p.NewKeyPath && fileExists(p.LegacyKeyPath)
	p.CfgNeedsCopy = !fileExists(p.NewCfgPath) && p.LegacyCfgPath != p.NewCfgPath && fileExists(p.LegacyCfgPath)
	return p
}

// migrateRDHomeIfNeeded performs the identity-preserving forward COPY on first
// run. It NEVER calls GenerateKey and NEVER overwrites an existing $RD_HOME key —
// a regenerated pubkey would silently break the relay allowlist, every peer's
// trust set, and self-authorship of already-published events (adversary A2). The
// legacy originals are left in place for one deprecation window (rollback). Safe
// to call on every nostrKey(): a no-op once the key is present.
func migrateRDHomeIfNeeded(rdHome string) error {
	p := planRDHomeMigration(rdHome)
	if p.KeyNeedsCopy {
		src, err := nostr.LoadKeyFile(p.LegacyKeyPath)
		if err != nil {
			return fmt.Errorf("reading legacy nostr identity %q: %w", p.LegacyKeyPath, err)
		}
		// O_EXCL copy so concurrent first-runs converge on one file; never clobbers.
		if err := nostr.WriteKeyFileExclusive(p.NewKeyPath, src, rdHome); err != nil {
			return fmt.Errorf("copying nostr identity into rd home: %w", err)
		}
		// Integrity assertion: the copied identity MUST have the identical pubkey.
		// This is the never-regenerate tripwire — a mismatch means the copy did not
		// preserve identity (it should be impossible, and proves we never regen).
		dst, err := nostr.LoadKeyFile(p.NewKeyPath)
		if err != nil {
			return fmt.Errorf("re-reading migrated nostr identity: %w", err)
		}
		if dst.PubKeyHex() != src.PubKeyHex() {
			return fmt.Errorf("MIGRATION INTEGRITY FAILURE: migrated nostr pubkey %s != source %s — identity was NOT preserved; refusing to proceed", dst.PubKeyHex(), src.PubKeyHex())
		}
	}
	if p.CfgNeedsCopy {
		if err := copyFile0600(p.LegacyCfgPath, p.NewCfgPath); err != nil {
			// A missing/partial rd.json copy is non-fatal: rd.json is a derived
			// bootstrap cache, not the identity. Warn, do not abort the command.
			fmt.Fprintf(os.Stderr, "warning: could not copy legacy rd.json into rd home: %v\n", err)
		}
	}
	return nil
}

// warnIfIdentityInconsistent is the startup tripwire: it compares the pubkey
// recorded in the key file against the pubkey derived from the loaded secret. A
// mismatch means the file was tampered with or its secret was swapped without
// rewriting pubkey_hex (a botched / regenerated identity). Non-fatal warning —
// the derived key is authoritative — but loud, so a botched migration is visible.
func warnIfIdentityInconsistent(keyPath string, k *nostr.Key) {
	stored, err := nostr.StoredPubKeyHex(keyPath)
	if err != nil || stored == "" {
		return
	}
	if stored != k.PubKeyHex() {
		fmt.Fprintf(os.Stderr,
			"warning: nostr identity at %q is INCONSISTENT — file records pubkey %s but the secret derives %s; the key file may be corrupt or regenerated\n",
			keyPath, stored, k.PubKeyHex())
	}
}

// loadRDConfig loads rd.json preferring $RD_HOME, falling back to the legacy
// ".cf" home during the deprecation window (backward compat: an old rd.json under
// .cf must still load). Never errors — an unreadable/absent config degrades to an
// empty Config (self-only trust), the safe default.
func loadRDConfig() *rdconfig.Config {
	rdHome := RDHome()
	if fileExists(rdconfig.Path(rdHome)) {
		if cfg, err := rdconfig.Load(rdHome); err == nil {
			return cfg
		}
	}
	if cfg, err := rdconfig.Load(CFHome()); err == nil {
		return cfg
	}
	return &rdconfig.Config{}
}

func fileExists(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

// copyFile0600 copies src to dst with 0600 perms, creating parent dirs (0700). It
// does not overwrite an existing dst (O_EXCL) so a concurrent first-run cannot
// race a half-written config over a good one.
func copyFile0600(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			return nil
		}
		return err
	}
	defer f.Close()
	_, err = f.Write(data)
	return err
}

// migrateHomeCmd prints (or performs) the identity-preserving migration of the
// legacy ".cf" nostr identity + rd.json into $RD_HOME. --dry-run prints the plan
// without touching disk. Keys are per-machine, so each host migrates independently.
var migrateHomeCmd = &cobra.Command{
	Use:   "migrate-home",
	Short: "Migrate the nostr identity + rd.json from the legacy .cf home into $RD_HOME (identity-preserving)",
	Long: `Copy this machine's nostr signing identity (nostr-identity.json) and rd.json
FORWARD from the legacy campfire home (.cf / ~/.campfire) into the rd home
($RD_HOME, default ~/.config/rd). The copy is identity-preserving: the pubkey is
NEVER regenerated (a regen would break the relay allowlist, peer trust, and
self-authorship). The legacy originals are left in place for rollback.

Keys are per-machine — run this once on each host. --dry-run prints the plan
without modifying anything.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		rdHome := RDHome()
		p := planRDHomeMigration(rdHome)

		if jsonOutput {
			p2 := p
			p2.RDHome = rdHome
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(struct {
				rdHomeMigrationPlan
				DryRun bool `json:"dry_run"`
			}{p, dryRun}); err != nil {
				return err
			}
		} else {
			fmt.Printf("rd home:      %s\n", p.RDHome)
			fmt.Printf("legacy home:  %s\n", p.LegacyHome)
			switch {
			case p.KeyAlreadyPresent:
				fmt.Printf("identity:     already present at %s (no copy)\n", p.NewKeyPath)
			case p.KeyNeedsCopy:
				fmt.Printf("identity:     COPY %s -> %s (pubkey preserved)\n", p.LegacyKeyPath, p.NewKeyPath)
			default:
				fmt.Printf("identity:     no legacy key at %s; a fresh key will be generated on first use\n", p.LegacyKeyPath)
			}
			if p.CfgNeedsCopy {
				fmt.Printf("rd.json:      COPY %s -> %s\n", p.LegacyCfgPath, p.NewCfgPath)
			} else {
				fmt.Printf("rd.json:      no copy (already present or no legacy config)\n")
			}
		}

		if dryRun {
			if !jsonOutput {
				fmt.Println("(dry-run: nothing written)")
			}
			return nil
		}
		if err := migrateRDHomeIfNeeded(rdHome); err != nil {
			return err
		}
		if !jsonOutput {
			fmt.Println("migration complete (legacy .cf originals left in place for rollback)")
		}
		return nil
	},
}

func init() {
	migrateHomeCmd.Flags().Bool("dry-run", false, "print the migration plan without writing anything")
	rootCmd.AddCommand(migrateHomeCmd)
}
