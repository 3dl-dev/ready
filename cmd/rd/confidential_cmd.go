package main

// `rd confidential enable|status|rotate` — flip an EXISTING board to confidential
// mode (new boards are confidential by default via `rd init`), report status, and
// retire a compromised board read key.

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/3dl-dev/ready/pkg/nostr"
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
			// Report EVERY epoch held, not just the current one: after a rotation the
			// older epochs are exactly what keeps pre-rotation cards readable, so
			// "epoch 2; holding epochs 1,2" is the observable form of that guarantee.
			fmt.Printf("board %s is CONFIDENTIAL (epoch %d; you hold the read key for epoch(s) %s)\n",
				coord, ep, joinEpochs(kr.Epochs(coord)))
		} else if _, confidential := kr.Cutover(coord); confidential {
			fmt.Printf("board %s is CONFIDENTIAL (you hold NO read key — ask the owner to `rd grant` %s)\n", coord, k.PubKeyHex())
		} else {
			fmt.Printf("board %s is marked confidential but not yet bootstrapped (the owner has not written yet)\n", coord)
		}
		return nil
	},
}

// joinEpochs renders an epoch list as "1,2,3" for status output.
func joinEpochs(epochs []int) string {
	parts := make([]string, 0, len(epochs))
	for _, e := range epochs {
		parts = append(parts, strconv.Itoa(e))
	}
	return strings.Join(parts, ",")
}

// confidentialRotateCmd retires a compromised board read key by minting a new CEK
// epoch and re-wrapping it to the current membership.
//
// WHY IT EXISTS (ready-2b25). `rd board --with-key` deliberately mints a bearer
// credential: the board's CEK travels in the URL fragment so a browser can read a
// confidential board with no extension. That tradeoff is only defensible if a key
// that leaks can be RETIRED — and until this command there was no path that
// minted a new epoch, for any board, for any reason short of revoking a member.
// A leaked CEK was permanent.
var confidentialRotateCmd = &cobra.Command{
	Use:   "rotate",
	Short: "Mint a NEW board read key (CEK epoch N+1) and re-wrap it to every current member",
	// A failed relay read-back is a RESULT the operator must act on, not a misuse
	// of the command: exit non-zero without burying the report under flag usage.
	SilenceUsage: true,
	Long: `Retire this board's current read key and mint the next epoch.

Use it when a board key has been exposed — pasted into a transcript, shared in a
` + "`rd board --with-key`" + ` link that reached the wrong person, or held by a
laptop that left the building.

WHAT ROTATING DOES: mints a fresh random CEK at epoch N+1 and publishes it,
NIP-44-wrapped, to every key holding a live grant on this board. From that moment
every card, edit and close reason this board writes is sealed under the new epoch,
so a holder of only the old key reads nothing new.

WHAT ROTATING DOES NOT DO: it does not re-seal, re-sign or invalidate a single
existing card. Everything already written stays sealed under the OLD epoch and
stays readable by anyone holding it — including the leaked key and any
` + "`rd board --with-key`" + ` link minted before the rotation. Those links keep
opening pre-rotation cards, permanently. Rotation closes the future, not the past;
it cannot recall what the leaked key could already read.

Preview it first with --dry-run: it prints the epoch step and exactly which keys
would and would not receive the new key, and publishes nothing.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		verifyRelays, _ := cmd.Flags().GetStringArray("verify")
		noVerify, _ := cmd.Flags().GetBool("no-verify")

		dir, ok := readyProjectDir()
		if !ok {
			return errNotNostrProject()
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
		plan, err := planBoardRotation(dir, pub, boardAuthor, boardD, "")
		if err != nil {
			return err
		}

		fmt.Printf("board %s\n", plan.Coord)
		fmt.Printf("  CEK epoch %d -> %d\n", plan.OldEpoch, plan.NewEpoch)
		fmt.Printf("  new key goes to: the owner (self-grant) + %d member(s)\n", len(plan.Members))
		for _, m := range plan.Members {
			fmt.Printf("    %s  %s  %s\n", shortKey(m.Pubkey), m.Role, m.Label)
		}
		if len(plan.Withheld) > 0 {
			fmt.Printf("  WITHHELD from %d revoked key(s):\n", len(plan.Withheld))
			for _, w := range plan.Withheld {
				fmt.Printf("    %s  %s  %s\n", shortKey(w.Pubkey), w.Role, w.Label)
			}
		}
		printRotationEffect(plan)
		if dryRun {
			fmt.Println("\nDRY RUN — nothing published.")
			return nil
		}

		published, rotErr := rotateBoardEpoch(pub, boardAuthor, boardD, plan)
		if rotErr != nil {
			// Partial rotation: say exactly how far it got. Re-running is safe —
			// it mints epoch N+2 and re-wraps to everyone again.
			fmt.Fprintf(os.Stderr, "\nrotation INCOMPLETE after publishing %d of %d grant(s): %v\n"+
				"re-run `rd confidential rotate` — a re-run mints the next epoch and re-wraps to every member, so no one is left stranded.\n",
				len(published), len(plan.Members)+1, rotErr)
			return rotErr
		}
		fmt.Printf("\nrotated: %d grant(s) published (1 owner self-grant + %d member wrap(s)); this board now seals new writes under epoch %d.\n",
			len(published), len(plan.Members), plan.NewEpoch)

		if noVerify {
			return nil
		}
		if len(verifyRelays) == 0 {
			verifyRelays = nostrReadRelays()
		}
		if len(verifyRelays) == 0 {
			fmt.Println("\nno read relays configured — the new grants exist only in the local log. " +
				"Pass --verify <relay> once relays are configured, or run `rd relay repair --relay <url>`.")
			return nil
		}
		return verifyRotationOnRelays(verifyRelays, published)
	},
}

// printRotationEffect states, in the command's own output, what a rotation does
// and does not change. The "does not" half is the operationally surprising part
// and the reason this is printed on every rotation rather than left to --help:
// an operator rotating because a `rd board --with-key` link leaked will assume
// the link is now dead, and it is not — it still opens every card written before
// this moment.
func printRotationEffect(plan *epochRotationPlan) {
	fmt.Printf("\nwhat this changes:\n")
	fmt.Printf("  - every card, edit and close reason written from now on is sealed under epoch %d.\n", plan.NewEpoch)
	fmt.Printf("  - a key holding only epoch %d (the leaked one) can read NONE of it.\n", plan.OldEpoch)
	fmt.Printf("what this does NOT change:\n")
	fmt.Printf("  - existing cards are NOT re-sealed, re-signed or invalidated. Everything written\n")
	fmt.Printf("    before now stays sealed under epoch <=%d and stays readable by every holder of\n", plan.OldEpoch)
	fmt.Printf("    those epochs — including the leaked key.\n")
	fmt.Printf("  - `rd board --with-key` links minted BEFORE this rotation keep working on\n")
	fmt.Printf("    pre-rotation cards, permanently. They will not show anything written after it.\n")
	fmt.Printf("    Mint a fresh link with `rd board --with-key` for a reader who needs both.\n")
	fmt.Printf("  - it cannot recall what the leaked key could already read.\n")
}

// verifyRotationOnRelays reads the just-published grants back off each relay with
// a FRESH subscription and independently verifies them.
//
// This is not belt-and-braces. reduceEventOutcome (pkg/sync/relayclass.go) reports
// an event as accepted the moment ANY write relay accepts it, and the LAN relays
// accept everything — so rd's own success report cannot tell you whether the
// public relay a remote member reads from took the new grants. If it did not, that
// member never learns the new key and silently stops being able to read anything
// written from this point on, while every local signal says the rotation
// succeeded.
func verifyRotationOnRelays(relays []string, published []*nostr.Event) error {
	fmt.Printf("\nread-back (fresh REQ per relay, each event schnorr-verified independently):\n")
	var failed []string
	for _, r := range relays {
		ctx, cancel := context.WithTimeout(context.Background(), rdSync.DefaultAuditTimeout)
		rb, err := rdSync.VerifyEventsOnRelay(ctx, r, published)
		cancel()
		if err != nil {
			fmt.Printf("  %s  READ FAILED: %v\n", r, err)
			failed = append(failed, r)
			continue
		}
		if rb.Match {
			fmt.Printf("  %s  %d/%d new grant(s) present\n", r, len(rb.Present), rb.Want)
			continue
		}
		fmt.Printf("  %s  %d/%d new grant(s) present — MISSING %d", r, len(rb.Present), rb.Want, len(rb.Missing))
		if len(rb.Unverified) > 0 {
			fmt.Printf(" (%d served but FAILED VERIFICATION)", len(rb.Unverified))
		}
		fmt.Println()
		for _, id := range rb.Missing {
			fmt.Printf("      missing %s\n", id)
		}
		failed = append(failed, r)
	}
	if len(failed) > 0 {
		return fmt.Errorf("the new grants are NOT readable on %v — members reading from those relays will not receive the new key; "+
			"re-run `rd relay repair --relay <url>` and then `rd confidential rotate --dry-run` to confirm", failed)
	}
	return nil
}

func init() {
	confidentialCmd.AddCommand(confidentialEnableCmd)
	confidentialCmd.AddCommand(confidentialStatusCmd)

	confidentialRotateCmd.Flags().Bool("dry-run", false, "print the epoch step and exactly which keys would and would not receive the new key; publish nothing")
	confidentialRotateCmd.Flags().StringArray("verify", nil, "relay URL to read the new grants back from (repeatable; defaults to the configured read relays)")
	confidentialRotateCmd.Flags().Bool("no-verify", false, "skip the relay read-back (the local log is then the only evidence the rotation reached anyone)")
	confidentialCmd.AddCommand(confidentialRotateCmd)

	rootCmd.AddCommand(confidentialCmd)
}
