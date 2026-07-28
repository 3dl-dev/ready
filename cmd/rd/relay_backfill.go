// Board backfill + read-back audit CLI (ready-260).
//
// THE PROBLEM THIS SOLVES: relay endpoints are configured GLOBALLY
// (~/.config/rd/rd.json), so every write relay — including the public one a
// browser board reads from — is already a write target for every project. But an
// event only reaches a relay as a SIDE EFFECT of a write. A project that has not
// been touched since a relay was added has therefore never sent that relay
// anything, and there is no in-product signal distinguishing "this board is
// empty" from "this board was never published here". Nothing is misconfigured;
// the history simply predates the relay.
//
// The fix is a re-send of events ALREADY SIGNED AND ALREADY DURABLE in the local
// authoritative log. It re-signs nothing, rebuilds nothing, and alters no
// created_at: each event goes out with its original id and signature, so a relay
// that already holds it answers "duplicate:" and the whole operation is
// idempotent and safe to re-run. Any path that re-signed would mint new event
// ids and fork the history — that is the one thing this must never do.
//
// Two commands, deliberately separate:
//
//	rd log publish --board --dry-run   what WOULD be sent, no network at all
//	rd relay audit                     what the relay ACTUALLY serves, read fresh
//
// The audit is not a nicety. rd's own publish report cannot answer "did the
// public relay take it": reduceEventOutcome (pkg/sync/relayclass.go) reports an
// event as accepted when ANY write relay accepts, and the LAN relays accept
// everything. Only an independent read-back against the named relay is evidence.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"time"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// relayAuditCmd reads a board (or the owner's whole board list) back off ONE
// named relay with a fresh subscription and compares it to the local
// authoritative log.
var relayAuditCmd = &cobra.Command{
	Use:   "audit",
	Short: "Read a board back off a named relay and compare it to the local log",
	// A failed read-back is a RESULT, not a misuse of the command: this exits
	// non-zero so a backfill script can gate on it, and dumping the flag usage
	// on every mismatch would bury the audit line the operator actually needs.
	SilenceUsage: true,
	Long: `Read back from a relay and compare against the local authoritative log.

  rd relay audit --relay wss://example            Audit the PINNED board: is its
                                                  30301 definition there, is every
                                                  addressable coordinate present and
                                                  current, is any status event missing?
  rd relay audit --relay wss://example --boards   Portfolio mode: list every kind-30301
                                                  board definition that relay serves for
                                                  the board owner.

Why this exists: a publish reports success when ANY write relay accepts an event.
With several relays configured, a total rejection by ONE of them is invisible in
that report. This command asks the relay itself, with a fresh REQ, and verifies
every event it serves independently.

Counts are compared as a PROJECTION, not as raw totals. Kinds 30301/30302/39301
are addressable: a relay keeps one event per coordinate while the local log keeps
every historical re-materialization, so a correctly-backfilled board legitimately
shows fewer events on the relay. What must match is the set of coordinates, the
winning event for each, and every non-addressable status event by id.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		relays, _ := cmd.Flags().GetStringArray("relay")
		boardsMode, _ := cmd.Flags().GetBool("boards")
		boardFlag, _ := cmd.Flags().GetString("board")
		if len(relays) == 0 {
			relays = nostrReadRelays()
		}
		if len(relays) == 0 {
			return fmt.Errorf("no relay to audit: pass --relay <url> (repeatable) or configure read relays")
		}
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		boardCoord := boardFlag
		if boardCoord == "" {
			boardCoord = nostrPinnedBoard(dir)
		}
		if boardCoord == "" {
			return fmt.Errorf("this project has no pinned board; pass --board <30301:owner:d> to name one explicitly")
		}
		owner, _, ok := rdSync.ParseBoardCoord(boardCoord)
		if !ok {
			return fmt.Errorf("%q is not a board coordinate of the form 30301:<owner-pubkey>:<board-d>", boardCoord)
		}

		ctx := context.Background()
		if boardsMode {
			out := map[string]any{}
			failed := false
			for _, r := range relays {
				rctx, cancel := context.WithTimeout(ctx, rdSync.DefaultAuditTimeout)
				boards, err := rdSync.FetchPortfolioBoards(rctx, r, owner)
				cancel()
				if err != nil {
					failed = true
					out[r] = map[string]any{"error": err.Error()}
					continue
				}
				out[r] = map[string]any{"owner": owner, "count": len(boards), "boards": boards}
			}
			if err := emitAuditJSON(out); err != nil {
				return err
			}
			if failed {
				return fmt.Errorf("at least one relay could not be read")
			}
			return nil
		}

		localEvents, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
		if err != nil {
			return err
		}
		results := make([]rdSync.BoardRelayAudit, 0, len(relays))
		var firstErr error
		mismatch := false
		for _, r := range relays {
			rctx, cancel := context.WithTimeout(ctx, rdSync.DefaultAuditTimeout)
			a, err := rdSync.AuditBoardOnRelay(rctx, r, localEvents, boardCoord)
			cancel()
			if err != nil && firstErr == nil {
				firstErr = err
			}
			if err != nil {
				fmt.Fprintf(os.Stderr, "audit %s: %v\n", r, err)
			}
			if !a.Match {
				mismatch = true
			}
			results = append(results, a)
		}
		if jsonOutput {
			if err := emitAuditJSON(results); err != nil {
				return err
			}
		} else {
			for _, a := range results {
				fmt.Printf("%s  board=%s match=%v  items local=%d relay=%d  events local=%d relay=%d  definition=%v  missing_coords=%d stale_coords=%d missing_status=%d verify_failures=%d\n",
					a.Relay, a.BoardCoord, a.Match, a.LocalItems, a.RelayItems, a.LocalEvents, a.RelayEvents,
					a.BoardDefinitionOnRelay, len(a.MissingCoords), len(a.StaleCoords), a.MissingRegular, a.RelayVerifyFailures)
			}
		}
		if firstErr != nil {
			return firstErr
		}
		if mismatch {
			// A non-zero exit is the point: this command is meant to be the gate
			// in a backfill script, so "did not match" must be machine-detectable
			// without parsing output.
			return fmt.Errorf("read-back does NOT match the local log on at least one relay")
		}
		return nil
	},
}

// relayRepairCmd drives the board to convergence on ONE relay by repeatedly
// measuring the gap and re-sending only what is missing.
//
// WHY REPEATEDLY, AND WHY ONLY THE GAP. The production relay is backed by a
// throttled store: under load a large share of writes come back as transient
// "error: save …" and the board lands only partially, so one pass never
// finishes a large board. Re-sending the WHOLE board each round converges too
// (every event is verbatim, so a re-send is a no-op for what already landed) but
// costs as much as the first pass every time. Publishing the measured gap makes
// each round cheaper than the last and makes convergence observable: the delta
// must shrink round over round, and zero is exactly the audit's Match.
var relayRepairCmd = &cobra.Command{
	Use:          "repair",
	Short:        "Re-send only what a relay is missing for this board, until the read-back matches",
	SilenceUsage: true,
	Long: `Converge one board onto one relay.

Each round: read the board back off the relay, compute which events it is missing
or holds a stale version of, and publish exactly those — verbatim, with their
original ids, created_at and signatures. Repeat until the read-back matches or
--rounds is exhausted.

This is the repair counterpart to 'rd log publish --board', which always sends the
whole board. Use it when a relay accepts writes unreliably: the first pass gets
most of the board there, and repair closes the remainder without re-sending
everything that already landed.

Exits non-zero if the board has not converged when the rounds run out.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		relay, _ := cmd.Flags().GetString("relay")
		rounds, _ := cmd.Flags().GetInt("rounds")
		boardFlag, _ := cmd.Flags().GetString("board")
		if relay == "" {
			return fmt.Errorf("--relay <url> is required: a repair is defined against ONE relay's measured gap")
		}
		dir, ok := readyProjectDir()
		if !ok {
			return fmt.Errorf("no .ready project directory found")
		}
		pub, ok, err := nostrPublisher()
		if err != nil {
			return err
		}
		if !ok {
			return fmt.Errorf("no project dir for publisher")
		}
		boardCoord := boardFlag
		if boardCoord == "" {
			boardCoord = nostrPinnedBoard(dir)
		}
		if boardCoord == "" {
			return fmt.Errorf("this project has no pinned board; pass --board <30301:owner:d> to name one explicitly")
		}

		var last rdSync.BoardRelayAudit
		for round := 1; round <= rounds; round++ {
			ctx, cancel := context.WithTimeout(context.Background(), repairRoundTimeout)
			audit, sent, err := pub.PublishBoardDelta(ctx, relay, boardCoord)
			cancel()
			if err != nil {
				return err
			}
			last = audit
			fmt.Printf("round %d: relay items=%d/%d events=%d/%d definition=%v missing_coords=%d stale=%d missing_status=%d -> resent %d\n",
				round, audit.RelayItems, audit.LocalItems, audit.RelayEvents, audit.LocalEvents,
				audit.BoardDefinitionOnRelay, len(audit.MissingCoords), len(audit.StaleCoords), audit.MissingRegular, sent)
			if audit.Match {
				return nil
			}
			if sent == 0 {
				// Nothing to send yet the audit does not match: the gap is not
				// something a re-send can close (e.g. the relay serves an event
				// that fails verification). Stop rather than spin.
				return fmt.Errorf("board does not match but there is nothing to re-send (verify_failures=%d) — re-sending cannot fix this", audit.RelayVerifyFailures)
			}
		}
		// One last measurement so the caller learns whether the FINAL round
		// closed the gap; the loop above only ever sees the state BEFORE its own
		// publish.
		local, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
		if err != nil {
			return err
		}
		ctx, cancel := context.WithTimeout(context.Background(), repairRoundTimeout)
		final, ferr := rdSync.AuditBoardOnRelay(ctx, relay, local, boardCoord)
		cancel()
		if ferr != nil {
			return ferr
		}
		fmt.Printf("final:   relay items=%d/%d events=%d/%d definition=%v missing_coords=%d stale=%d missing_status=%d\n",
			final.RelayItems, final.LocalItems, final.RelayEvents, final.LocalEvents,
			final.BoardDefinitionOnRelay, len(final.MissingCoords), len(final.StaleCoords), final.MissingRegular)
		if final.Match {
			return nil
		}
		_ = last
		return fmt.Errorf("board %s has NOT converged on %s after %d round(s)", boardCoord, relay, rounds)
	},
}

// repairRoundTimeout bounds ONE measure+resend round. It is generous because a
// first round on a large board sends thousands of events to a scale-to-zero,
// throttled relay; a round that legitimately takes minutes must not be cut off
// mid-batch, which would leave the next round more work rather than less.
const repairRoundTimeout = 30 * time.Minute

// emitAuditJSON writes v as indented JSON to stdout. The audit is evidence, so
// it always prints its full structured form regardless of --json when in
// --boards mode; the per-board form honours --json for a human-readable default.
func emitAuditJSON(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

// runPublishBoardPlan is `rd log publish --board --dry-run`: it prints exactly
// what a board publish would send, computed from the local log with no relay
// contact whatsoever. A bulk republish writes to production data across a whole
// portfolio, so the shape of the write is reviewable before any of it happens.
func runPublishBoardPlan(relayOverride []string) error {
	dir, ok := readyProjectDir()
	if !ok {
		return fmt.Errorf("no .ready project directory found")
	}
	pub, ok, err := nostrPublisher()
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("no project dir for publisher")
	}
	if len(relayOverride) > 0 {
		pub.WriteRelays = relayOverride
	}
	boardCoord := nostrPinnedBoard(dir)
	plan, err := pub.PlanBoardPublish(boardCoord)
	if err != nil {
		return err
	}
	if jsonOutput {
		return emitAuditJSON(plan)
	}
	kinds := make([]string, 0, len(plan.ByKind))
	for k := range plan.ByKind {
		kinds = append(kinds, k)
	}
	sort.Strings(kinds)
	fmt.Printf("DRY RUN — nothing published.\n")
	fmt.Printf("  board:       %s\n", plan.BoardCoord)
	fmt.Printf("  relays:      %v\n", plan.Relays)
	fmt.Printf("  events:      %d\n", plan.Events)
	fmt.Printf("  items:       %d\n", plan.Items)
	fmt.Printf("  definition:  %v\n", plan.HasBoardDefinition)
	fmt.Printf("  created_at:  %d..%d\n", plan.OldestCreatedAt, plan.NewestCreatedAt)
	for _, k := range kinds {
		fmt.Printf("    kind %-6s %d\n", k, plan.ByKind[k])
	}
	return nil
}

func init() {
	nostrPublishCmd.Flags().Bool("dry-run", false, "with --board: report what WOULD be published (per-kind counts, item count, time range) and exit without contacting any relay")
	nostrPublishCmd.Flags().StringArray("relay", nil, "with --board: publish to these relay URLs INSTEAD of the configured write relays (repeatable) — use to target one relay so its accept/reject is unambiguous")

	relayAuditCmd.Flags().StringArray("relay", nil, "relay URL to read back from (repeatable; defaults to the configured read relays)")
	relayAuditCmd.Flags().Bool("boards", false, "portfolio mode: list every kind-30301 board definition the relay serves for the board owner")
	relayAuditCmd.Flags().String("board", "", "board coordinate to audit (default: this project's pinned board)")
	relayCmd.AddCommand(relayAuditCmd)

	relayRepairCmd.Flags().String("relay", "", "relay URL to converge this board onto (required)")
	relayRepairCmd.Flags().Int("rounds", 5, "maximum measure-and-resend rounds")
	relayRepairCmd.Flags().String("board", "", "board coordinate to repair (default: this project's pinned board)")
	relayCmd.AddCommand(relayRepairCmd)
}
