// rd board confidential-since (ready-475).
//
// THE GAP THIS CLOSES. §11.13a's cutover is DERIVED — a minimum over the owner
// CEK grants a reader received — and its three witnesses can only ever refute
// that minimum, never establish the truth. A board whose log contains a sealed
// event that is not evidence about its cutover therefore fails closed forever: on
// the live `ready` board, three permanently-unsupersedable kind-1630 events left
// by a test fixture withheld 167 of 536 cards from the board's OWNER. This
// command is how an owner states the instant instead, by republishing the board's
// own kind-30301 definition with an additive `confidential_since` tag.
//
// IT CHANGES NOTHING ELSE. No card, status event or grant is touched, re-sealed
// or re-signed, exactly as `rd board archive` does not; and the READ side can
// only ever use the assertion to move the cutover EARLIER (§11.13a's
// min(asserted, derived)), so publishing one can never reveal a card the served
// grants already quarantine.
//
// IT FOLLOWS `rd board archive`'s PRECEDENT for everything else — run from any
// nostr-native project, name the target board explicitly, owner-only — because
// the two commands do the same thing to the same event: read the CURRENT
// definition (local log plus configured read relays), change one tag, republish
// through rd's own writer. See runBoardArchiveToggle's header for why that shape,
// rather than requiring a local directory for the target board.
package main

import (
	"context"
	"fmt"
	"strconv"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

var boardConfidentialSinceCmd = &cobra.Command{
	Use:   "confidential-since <board-d-or-coord> <unix-seconds>",
	Short: "State when a board went confidential, so readers stop deriving it from the grants they happen to receive",
	Long: `Republish a board's kind-30301 definition with an owner-signed
"confidential_since" tag: the instant the board went confidential, in unix
seconds (board-fold-spec.md §11.13a).

WHY. Without it, every reader DERIVES the cutover as the earliest owner
CEK-bearing grant it received — a minimum, so it is only ever a lower bound on
the truth — and fails closed when anything in the board's own log contradicts
that minimum. A sealed event that is not really evidence about this board (a
test fixture, an import, a status event under a key that was never a board CEK)
therefore withholds the board's entire plaintext history from everyone,
including its owner, with no relay misbehaving.

WHAT IT CHANGES. Nothing but the definition event: no card, status, or grant is
touched, re-sealed, or re-signed. The assertion can only move the cutover
EARLIER — readers take min(asserted, derived) — so it can never reveal a card
the board's own grants already quarantine.

Only a board's OWNER may assert this: the tag rides on the board's own
addressable coordinate, so a definition signed by any other key is a different
board's and is ignored by every reader.

Passing 0 REMOVES the assertion, restoring the derived behaviour.

The board argument may be a bare board "d" tag (its owner is resolved to THIS
signing key) or a full "30301:<owner>:<d>" coordinate.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		since, err := strconv.ParseInt(args[1], 10, 64)
		if err != nil || since < 0 {
			return fmt.Errorf("rd board confidential-since: %q is not a unix-second instant (decimal, >= 0; 0 removes the assertion)", args[1])
		}
		return runBoardConfidentialSince(cmd, args[0], since)
	},
}

func runBoardConfidentialSince(cmd *cobra.Command, arg string, since int64) error {
	dir, ok := readyProjectDir()
	if !ok {
		return fmt.Errorf("rd board confidential-since: run inside a nostr-native rd project — it supplies the signing key, local durability log, and configured relays; the TARGET board named on the command line may be any board this key owns")
	}
	k, err := nostrKey()
	if err != nil {
		return err
	}
	self := k.PubKeyHex()

	boardD := arg
	coord := rdSync.BoardCoord(self, arg)
	if owner, d, isCoord := rdSync.ParseBoardCoord(arg); isCoord {
		if owner != self {
			return fmt.Errorf("rd board confidential-since: %s is authored by %s, not this key's own pubkey (%s) — only a board's OWNER may assert its cutover (board-fold-spec.md §11.13a); nothing was published", arg, owner, self)
		}
		boardD, coord = d, arg
	}

	base := cmd.Context()
	if base == nil {
		base = context.Background()
	}
	errOut := cmd.ErrOrStderr()

	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	local, err := log.ReadAll()
	if err != nil {
		return err
	}
	events := append([]*nostr.Event{}, local...)
	relays := nostrReadRelays()
	for _, r := range relays {
		rctx, cancel := context.WithTimeout(base, rdSync.DefaultAuditTimeout)
		// NO `authors` FILTER. Measured against wss://relay.3dl.network, an
		// authors-filtered REQ silently UNDER-returns (ready-27b: a paged
		// {kinds:[30301],authors:[owner]} served 42 of an owner's 56 boards where
		// the same REQ without it served all 56). Under-returning here would mean
		// republishing on top of a STALE definition and silently reverting the
		// board's title, maintainers or archived marker. Filtering by "d" alone
		// may return other owners' boards with the same d tag; WinningBoardEvent
		// discards them, because it matches the full coordinate.
		fetched, ferr := nostr.FetchMany(rctx, r, map[string]any{
			"kinds": []int{rdSync.KindBoard},
			"#d":    []string{boardD},
		})
		cancel()
		if ferr != nil {
			fmt.Fprintf(errOut, "warning: rd board confidential-since: could not read the current definition of %s from %s: %v\n", coord, r, ferr)
			continue
		}
		events = append(events, fetched...)
	}

	// READ-MODIFY-WRITE (§16.3): the republished definition must carry the
	// CURRENT title, maintainers and archived marker forward, or asserting a
	// cutover would silently rename the board or un-archive it.
	winner, found := rdSync.WinningBoardEvent(events, coord)
	if !found {
		return fmt.Errorf("rd board confidential-since: no existing kind-30301 definition found for %s (checked the local log and %d relay(s)) — nothing to assert on", coord, len(relays))
	}
	spec := rdSync.BoardSpecFromEvent(winner)

	createdAt := nostrNextCreatedAt(log, rdSync.BoardDriftScope(boardD))
	ev, err := rdSync.BuildBoardEventWithConfidentialSince(k, spec, since, createdAt)
	if err != nil {
		return err
	}

	pub := &rdSync.Publisher{
		Key:         k,
		Log:         log,
		WriteRelays: nostrWriteRelays(),
		PendingPath: nostrPendingPath(dir),
		// Production: a sanctioned CLI write path (mirrors `rd board archive`).
		// Unlike that command this one legitimately targets THIS repo's own
		// reserved "ready" coordinate — that board is the reason the assertion
		// exists — so the ready-fce guard has to be satisfied rather than dodged.
		Production: true,
	}
	res, err := pub.PublishEvents(base, []*nostr.Event{ev})
	if err != nil {
		return err
	}

	if since == 0 {
		fmt.Printf("removed the cutover assertion on %s: event %s (created_at %d)\n", coord, ev.ID, ev.CreatedAt)
	} else {
		fmt.Printf("%s is confidential since %d: event %s (created_at %d)\n", coord, since, ev.ID, ev.CreatedAt)
	}
	if res.Buffered {
		fmt.Fprintln(errOut, "NOTE: no write relay accepted this yet — buffered for retry (nostr-pending.jsonl). The assertion is already durable in the local log.")
	}
	if res.Rejected {
		fmt.Fprintln(errOut, "WARNING: at least one relay PERMANENTLY rejected this event — see nostr-rejected.jsonl.")
	}
	fmt.Fprintf(errOut, "NOTE: this changes NOTHING about the board's cards, statuses, or grants. Reverse with: rd board confidential-since %s 0\n", arg)
	return nil
}

func init() {
	boardCmd.AddCommand(boardConfidentialSinceCmd)
}
