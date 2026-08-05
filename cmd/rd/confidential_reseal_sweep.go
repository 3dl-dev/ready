package main

// `rd confidential reseal` — the per-board execution pass for ready-5e7, the
// operation the whole ready-336 epic exists to make safe.
//
// WHAT IT DOES. For the current project's pinned board it re-seals every
// coordinate the relay is still serving in PLAINTEXT, one addressable
// replacement per coordinate, then re-reads the board off the relay and reports
// what an outsider now sees.
//
// WHY THE PLAN COMES OFF THE RELAY AND NOT THE LOCAL LOG. The question this pass
// answers is "what can a stranger fetch", and only the relay can answer it. The
// local append-only log retains every superseded event FOREVER, so a sweep that
// picked its work list from the log would re-seal coordinates that are already
// sealed on the relay, skip coordinates another machine plaintext-wrote since the
// last sync, and — worst — report success by reading back its own plaintext copy.
// An entire confidential suite once stayed green while a rotation was deleting
// keys from the relay, because every test read the log. So: BuildResealPlan walks
// the relay, and the work list, the ordering floor, and the final verification all
// come from that walk.
//
// WHY IT IS RESUMABLE BY RE-DERIVATION, NOT BY CHECKPOINT. Eight projects write to
// this board set continuously. A checkpoint file records what was true when the
// run started; re-running the plan records what is true now. Those differ, and the
// difference is exactly the set a checkpoint would get wrong — so there is no
// checkpoint. Re-run the command; it re-derives and continues.
//
// WHY IT HALTS RATHER THAN SKIPS. A refusal that is not in the disposition list
// (already-sealed, foreign-author) means the inventory drifted between the dry run
// and now. Skipping past it would leave a plaintext card on a board this pass is
// about to report clean, which is the one outcome that makes the whole epic a lie.
// So an unexpected refusal stops that board with its coordinate named.

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/spf13/cobra"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// resealSweepResult is one board's pass, reported as before/after counts taken
// from the relay on both sides — never from the local log.
type resealSweepResult struct {
	BoardCoord string
	// PlaintextBefore / PlaintextAfter are what the RELAY served, counted the way
	// an outsider would count: winning card per addressable coordinate, no enc tag.
	PlaintextBefore int
	PlaintextAfter  int
	Sealed          int
	Skipped         map[string]int
	// NeverLanded names coordinates whose local winner is sealed while the relay
	// still serves plaintext — a replacement that was signed and never arrived.
	NeverLanded []string
	// Unprojectable names coordinates whose card IS in the local log but which the
	// fold does not project, so no item exists to re-seal. They stay readable.
	Unprojectable []string
}

var confidentialResealCmd = &cobra.Command{
	Use:   "reseal",
	Short: "Seal the plaintext cards this board's relay is still serving (ready-336)",
	Long: `Re-seal every grandfathered plaintext card on this board, in place.

A board that became confidential AFTER it had items carries a plaintext tail
forever: 'rd confidential enable' grandfathers every pre-cutover card, so it stays
readable to ANYONE with the relay URL, permanently, while 'rd confidential status'
reports the board CONFIDENTIAL. Sealing future writes never closes that.

The mechanism is ADDRESSABLE REPLACEMENT, not deletion. Publishing a sealed card at
the same coordinate with a later created_at makes the relay evict the plaintext copy
by the ordinary NIP-01 replaceable rule. Nothing is destroyed: the local append-only
log keeps BOTH events. What changes is which one a stranger can fetch.

WHAT THIS COSTS, so it is chosen rather than discovered:
  - every re-sealed coordinate gets a NEW event id, so history forks for it and
    'rd relay audit' reports it as superseded (ready-fcd), not missing;
  - a reader holding only an OLDER CEK epoch loses access to history they can read
    today. Check who that is before running: go run ./scripts/resealplan.

The work list, the ordering floor and the verification all come off the RELAY, never
the local log — the log retains superseded events forever and would report success
unconditionally.

Preview with --dry-run. Re-run to resume: the plan is re-derived every time, so a
board that other projects kept writing to converges rather than drifting.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		dryRun, _ := cmd.Flags().GetBool("dry-run")
		limit, _ := cmd.Flags().GetInt("limit")
		relayFlag, _ := cmd.Flags().GetString("relay")

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
		signer := pub.Key.PubKeyHex()
		if signer != boardAuthor {
			return fmt.Errorf("only the board owner can re-seal this board: it is authored by %s, you are %s", boardAuthor, signer)
		}

		relay := relayFlag
		if relay == "" {
			rs := nostrReadRelays()
			if len(rs) == 0 {
				return errors.New("no read relay configured, and the plan must come off a relay — pass --relay")
			}
			relay = rs[0]
		}

		coord := rdSync.BoardCoord(boardAuthor, boardD)
		ctx := context.Background()

		plan, err := rdSync.BuildResealPlan(ctx, relay, boardAuthor, boardD, coord)
		if err != nil {
			return fmt.Errorf("planning re-seal for %s: %w", coord, err)
		}
		if !plan.Confidential {
			return fmt.Errorf("refusing to re-seal %s: it carries no CEK-bearing grant, so it was never confidential — its plaintext is intended, and sealing it would make it unreadable to its own audience", coord)
		}

		res := resealSweepResult{
			BoardCoord:      coord,
			PlaintextBefore: countRelayPlaintext(plan),
			Skipped:         map[string]int{},
		}
		fmt.Printf("board %s (epoch %d)\n", coord, plan.CurrentEpoch)
		fmt.Printf("  relay serves %d card(s): %d plaintext, %d sealed\n", plan.Cards, res.PlaintextBefore, plan.Cards-res.PlaintextBefore)
		if len(plan.ReadersLosingHistory) > 0 {
			fmt.Printf("  READERS LOSING HISTORY (they can read this board's plaintext tail today and will not be able to after this pass):\n")
			for _, pk := range plan.ReadersLosingHistory {
				fmt.Printf("    %s\n", pk)
			}
		}

		todo := make([]rdSync.CoordPlan, 0, plan.WouldReseal)
		for _, cp := range plan.Coords {
			if cp.Reseal {
				todo = append(todo, cp)
			} else if cp.SkipReason != "" {
				res.Skipped[cp.SkipReason]++
			}
		}
		if limit > 0 && limit < len(todo) {
			todo = todo[:limit]
			fmt.Printf("  --limit %d: re-sealing only the first %d of %d\n", limit, len(todo), plan.WouldReseal)
		}
		if len(todo) == 0 {
			fmt.Printf("  nothing to do: the relay serves no owner-authored plaintext card for this board.\n")
			return nil
		}
		fmt.Printf("  %d coordinate(s) to re-seal\n", len(todo))
		if dryRun {
			for _, cp := range todo {
				fmt.Printf("    %s  relay event %s (created_at %d) -> sealed, ~%d bytes\n", cp.ItemID, shortID(cp.EventID), cp.RelayCreatedAt, cp.SealedBytes)
			}
			fmt.Printf("\n--dry-run: nothing published.\n")
			return nil
		}

		// The projection is read ONCE, before any publish. resealCard requires the
		// real projected item (a hand-assembled one would seal whatever the fold
		// would have overlaid), and re-projecting mid-pass would fold this pass's
		// own replacements back in for no gain.
		_, byID, err := nostrProjectAllItems()
		if err != nil {
			return fmt.Errorf("projecting items: %w", err)
		}

		// Cards the local log holds, by coordinate. Used ONLY to tell two very
		// different "not in the projection" cases apart — see below.
		localEvents, err := pub.Log.ReadAll()
		if err != nil {
			return fmt.Errorf("reading log: %w", err)
		}

		for _, cp := range todo {
			item := byID[cp.ItemID]
			if item == nil {
				// The relay serves a plaintext card whose item this machine cannot
				// project. Two causes, opposite answers, and conflating them either
				// stalls the portfolio on a permanent condition or silently walks
				// past a genuine sync gap:
				//
				//   the log has NO card here  -> this machine is simply behind the
				//     relay. `rd sync` and re-run; the coordinate will re-seal.
				//   the log HAS the card      -> the fold refuses to project it (a
				//     malformed or fixture card written straight onto the board,
				//     ready-3e3). No amount of syncing changes that, and resealCard
				//     cannot run without a projected item. It is a disposition, not
				//     a transient — so it is NAMED and the board is reported dirty,
				//     never skipped quietly.
				if _, held := rdSync.WinningCardEvent(localEvents, coord, cp.ItemID); !held {
					return fmt.Errorf("HALTED at %s: the relay serves a plaintext card for it and this machine's log holds none — the log is behind the relay; `rd sync` and re-run", cp.ItemID)
				}
				res.Unprojectable = append(res.Unprojectable, cp.ItemID)
				fmt.Printf("    %s  UNPROJECTABLE — the log holds this card but the fold does not project it, so it cannot be re-sealed and stays readable (ready-3e3)\n", cp.ItemID)
				continue
			}
			out, rerr := resealCard(dir, pub, boardAuthor, boardD, item, resealOptions{RelayCardCreatedAt: cp.RelayCreatedAt})
			switch {
			case errors.Is(rerr, errCardAlreadySealed):
				// The relay serves PLAINTEXT for this coordinate but the local log's
				// winner is already SEALED. That is not convergence — it is the
				// ready-fcd "re-seal never landed" state: a sealed replacement was
				// signed here and never reached the relay, so a stranger still reads
				// the plaintext while every local signal says sealed. resealCard
				// cannot fix it (it refuses to mint a second replacement), and it
				// must never be counted as done. Named here, and the read-back below
				// fails the board because the coordinate is still readable.
				res.NeverLanded = append(res.NeverLanded, cp.ItemID)
				fmt.Printf("    %s  LOCAL SEALED BUT RELAY SERVES PLAINTEXT — an earlier replacement never reached %s; `rd relay repair` this coordinate\n", cp.ItemID, relay)
				continue
			case errors.Is(rerr, errCardForeignAuthor):
				res.Skipped[rdSync.SkipForeignAuthor]++
				fmt.Printf("    %s  authored by another key — cannot be re-sealed by this signer, left plaintext\n", cp.ItemID)
				continue
			case rerr != nil:
				return fmt.Errorf("HALTED at %s (%d of %d re-sealed so far; re-run to resume): %w", cp.ItemID, res.Sealed, len(todo), rerr)
			}
			if out.RelayRejected {
				return fmt.Errorf("HALTED at %s: the sealed replacement exceeded the relay's size limit and was dead-lettered, so the relay is STILL SERVING THE PLAINTEXT for it. The dry run said no coordinate would halt, so the inventory drifted — re-measure with `go run ./scripts/resealplan --board %s` before continuing", cp.ItemID, boardD)
			}
			res.Sealed++
		}

		fmt.Printf("\n  re-sealed %d coordinate(s)\n", res.Sealed)

		// VERIFICATION, off the relay. This re-walk is the whole point: it is the
		// outsider's view, and it is the only evidence that does not come from the
		// machine that just did the writing.
		after, err := rdSync.BuildResealPlan(ctx, relay, boardAuthor, boardD, coord)
		if err != nil {
			return fmt.Errorf("re-reading %s off %s to verify: %w", coord, relay, err)
		}
		res.PlaintextAfter = countRelayPlaintext(after)
		fmt.Printf("\nread-back off %s (independent of this machine's log):\n", relay)
		fmt.Printf("  plaintext cards: %d before -> %d after\n", res.PlaintextBefore, res.PlaintextAfter)
		if len(res.Skipped) > 0 {
			fmt.Printf("  skipped: %s\n", formatSkipped(res.Skipped))
		}
		if len(res.NeverLanded) > 0 {
			fmt.Printf("  NEVER LANDED (sealed locally, plaintext on the relay): %v\n", res.NeverLanded)
		}
		if len(res.Unprojectable) > 0 {
			fmt.Printf("  UNPROJECTABLE (in the log, not in the fold — cannot be re-sealed): %v\n", res.Unprojectable)
		}
		if res.PlaintextAfter > 0 {
			remaining := map[string]int{}
			for _, cp := range after.Coords {
				switch {
				case cp.SkipReason == rdSync.SkipAlreadySealed:
					// sealed; not readable
				case cp.Reseal:
					remaining["still-plaintext"]++
				default:
					remaining[cp.SkipReason]++
				}
			}
			return fmt.Errorf("%s still serves %d readable card(s) after the pass (%s) — this board is NOT done", coord, res.PlaintextAfter, formatSkipped(remaining))
		}
		fmt.Printf("  %s serves ZERO readable cards.\n", coord)
		return nil
	},
}

// countRelayPlaintext counts the cards an OUTSIDER can read: the winning card per
// addressable coordinate with no envelope. Foreign-authored and over-limit
// coordinates count too — they are readable, and a count that excused them would
// report a board clean while a stranger could still read it.
func countRelayPlaintext(plan rdSync.BoardResealPlan) int {
	n := 0
	for _, cp := range plan.Coords {
		if cp.SkipReason != rdSync.SkipAlreadySealed {
			n++
		}
	}
	return n
}

func formatSkipped(m map[string]int) string {
	if len(m) == 0 {
		return "none"
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	s := ""
	for i, k := range keys {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%s=%d", k, m[k])
	}
	return s
}

func init() {
	confidentialResealCmd.Flags().Bool("dry-run", false, "print exactly which coordinates would be re-sealed; publish nothing")
	confidentialResealCmd.Flags().Int("limit", 0, "re-seal at most N coordinates this run (0 = all); the pass is resumable, so a limited run is a partial one, not a different one")
	confidentialResealCmd.Flags().String("relay", "", "relay to plan and verify against (default: the first configured read relay)")
	confidentialCmd.AddCommand(confidentialResealCmd)
}
