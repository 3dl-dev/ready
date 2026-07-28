// rd board archive / rd board unarchive (ready-a9b).
//
// THE GAP THIS CLOSES: rd had no archive, delete, retire, or close-board path.
// `rd revoke` targets a MEMBER's role (posts a role-grant with role="revoked"),
// never a board, and self-revoking the owner from their own board is a
// different, incoherent operation. The owner's `rd board` link carried keys
// for boards that were test junk, a cancelled project, and a stale duplicate
// coordinate — 32 boards the portfolio gather had no way to stop naming.
//
// ARCHIVE, NOT DELETE, AND DELIBERATELY SO. Reversible, keeps every signed
// event readable (a NIP-09 delete is advisory — a relay may simply keep
// serving the events, and the local log holds them regardless, so a delete
// would give a false impression of removal), and touches nothing but the
// board's OWN kind-30301 definition event. See BoardSpec.Archived's doc
// (pkg/sync/nostrwire.go) for the marker itself.
//
// WHY THIS COMMAND DOES NOT REQUIRE A LOCAL DIRECTORY FOR THE TARGET BOARD.
// Every one of the 32 boards this shipped to archive is either test-generated
// (no directory ever existed) or a directory that pins a DIFFERENT board
// coordinate today (dap's ~/projects/dap/.ready/config.json pins "resonant",
// not "dap"; ceo's directory has no .ready/ at all). `rd grant --all-boards`
// solves a different problem (fan a grant across every board THIS MACHINE
// still has pinned locally) and chdirs into each one's own repo; that pattern
// cannot reach an orphaned board. So this command follows `rd relay audit
// --board <coord>` / `rd relay repair --board <coord>`'s existing precedent
// instead: run from ANY nostr-native project (it supplies the signing key,
// the local durability log, and the configured relays) and name the TARGET
// board explicitly. The archive event for a foreign boardD lands in the
// CURRENT project's local log — harmless, because EventBelongsToBoard (the
// membership test every board-scoped read/publish uses) matches on the
// event's OWN "d" tag, so a foreign-board event never surfaces in a scoped
// read of the current project's board.
package main

import (
	"context"
	"fmt"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

var boardArchiveCmd = &cobra.Command{
	Use:   "archive <board-d-or-coord>",
	Short: "Mark a board archived: dropped from `rd board` and the browser's board list, nothing else changes",
	Long: `Mark a board archived by republishing its kind-30301 definition with an
additive "archived" tag. This changes NOTHING else about the board: every
card, status event, and role grant already published stays exactly as
signed — nothing is re-sealed, nothing is deleted, nothing loses readability.

Archiving is a PORTFOLIO-DISCOVERY marker, not a project mutation. It is
honoured by two readers: the CLI's whole-portfolio gather (` + "`rd board`" + `,
` + "`rd board --portfolio`" + `) drops the archived coordinate's keys from the
link, and the hosted browser board drops it from the rendered board list. A
project that still pins an archived board's coordinate keeps working exactly
as before: ` + "`rd list`" + `/` + "`rd ready`" + `/` + "`rd show`" + ` never consult this
marker, because archiving is not a statement about the project's own items.

Reversible: ` + "`rd board unarchive <same-target>`" + ` republishes the identical
title and maintainers with the marker removed.

The argument may be a bare board "d" tag (its owner is resolved to THIS
signing key) or a full "30301:<owner>:<d>" coordinate. Only a board's OWNER
may archive it — a coordinate whose owner is a different pubkey is refused.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardArchiveToggle(cmd, args[0], true)
	},
}

var boardUnarchiveCmd = &cobra.Command{
	Use:   "unarchive <board-d-or-coord>",
	Short: "Reverse `rd board archive`: the board reappears in `rd board` and the browser",
	Long: `The exact inverse of ` + "`rd board archive`" + `: republishes the board's
kind-30301 definition with the SAME title and maintainers and the "archived"
tag removed. Nothing about any card, status, or grant is touched either way.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return runBoardArchiveToggle(cmd, args[0], false)
	},
}

func archiveStateWord(archived bool) string {
	if archived {
		return "archived"
	}
	return "not archived"
}

func archiveVerbPast(archived bool) string {
	if archived {
		return "archived"
	}
	return "unarchived"
}

// inverseCommand names the command that would UNDO an action leaving the board
// in state `archived` (e.g. inverseCommand(true) == "unarchive": if this call
// just archived the board, "unarchive" reverses it).
func inverseCommand(archived bool) string {
	if archived {
		return "unarchive"
	}
	return "archive"
}

// currentCommand names the command PERFORMING an action that leaves the board
// in state `archived` — the mirror of inverseCommand, spelled out separately so
// call sites read as what they mean instead of as a double negative.
func currentCommand(archived bool) string {
	return inverseCommand(!archived)
}

// runBoardArchiveToggle is shared by `rd board archive` and `rd board
// unarchive`: resolve the board's CURRENT definition (title, maintainers) from
// the local log and the configured read relays, then republish it — identical
// except for the "archived" marker — through rd's own writer
// (pkg/sync.Publisher.PublishEvents), so the shape stays conformant with
// board-fold-spec.md Part II exactly like every other rd mutation.
func runBoardArchiveToggle(cmd *cobra.Command, arg string, archived bool) error {
	dir, ok := readyProjectDir()
	if !ok {
		return fmt.Errorf("rd board %s: run inside a nostr-native rd project — it supplies the signing key, local durability log, and configured relays; the TARGET board named on the command line may be any board this key owns, not necessarily this project's own", currentCommand(archived))
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
			return fmt.Errorf("rd board %s: %s is authored by %s, not this key's own pubkey (%s) — only a board's OWNER may archive/unarchive it (board-fold-spec.md §16.6); nothing was published", currentCommand(archived), arg, owner, self)
		}
		boardD, coord = d, arg
	}

	relays := nostrReadRelays()

	base := cmd.Context()
	if base == nil {
		base = context.Background()
	}

	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	local, err := log.ReadAll()
	if err != nil {
		return err
	}
	events := append([]*nostr.Event{}, local...)
	errOut := cmd.ErrOrStderr()
	for _, r := range relays {
		rctx, cancel := context.WithTimeout(base, rdSync.DefaultAuditTimeout)
		fetched, ferr := nostr.FetchMany(rctx, r, map[string]any{
			"kinds":   []int{rdSync.KindBoard},
			"authors": []string{self},
			"#d":      []string{boardD},
		})
		cancel()
		if ferr != nil {
			fmt.Fprintf(errOut, "warning: rd board %s: could not read the current definition of %s from %s: %v\n", currentCommand(archived), coord, r, ferr)
			continue
		}
		events = append(events, fetched...)
	}

	winner, found := rdSync.WinningBoardEvent(events, coord)
	if !found {
		return fmt.Errorf("rd board %s: no existing kind-30301 definition found for %s (checked the local log and %d relay(s)) — nothing to %s", currentCommand(archived), coord, len(relays), currentCommand(archived))
	}
	if rdSync.IsBoardArchived(winner) == archived {
		fmt.Fprintf(errOut, "NOTE: %s is already %s (as of event %s, created_at %d) — republishing anyway so every relay converges on the same definition.\n",
			coord, archiveStateWord(archived), winner.ID, winner.CreatedAt)
	}

	spec := rdSync.BoardSpecFromEvent(winner)
	spec.Archived = archived

	createdAt := nostrNextCreatedAt(log, rdSync.BoardDriftScope(boardD))
	ev, err := rdSync.BuildBoardEvent(k, spec, createdAt)
	if err != nil {
		return err
	}

	pub := &rdSync.Publisher{
		Key:         k,
		Log:         log,
		WriteRelays: nostrWriteRelays(),
		PendingPath: nostrPendingPath(dir),
		// Production: this command IS a sanctioned rd CLI write path (mirrors
		// nostrPublisher()'s justification) — the only thing it refuses is
		// addressing this repo's own reserved "ready" board coordinate, which
		// none of the 32 archive targets do.
		Production: true,
	}
	res, err := pub.PublishEvents(base, []*nostr.Event{ev})
	if err != nil {
		return err
	}

	fmt.Printf("%s %s: event %s (created_at %d)\n", archiveVerbPast(archived), coord, ev.ID, ev.CreatedAt)
	if res.Buffered {
		fmt.Fprintln(errOut, "NOTE: no write relay accepted this yet — buffered for retry (nostr-pending.jsonl). The marker is already durable in the local log.")
	}
	if res.Rejected {
		fmt.Fprintln(errOut, "WARNING: at least one relay PERMANENTLY rejected this event — see nostr-rejected.jsonl.")
	}
	fmt.Fprintf(errOut, "NOTE: this changes NOTHING about the board's cards, statuses, or grants — every existing event stays exactly as signed. Reverse with: rd board %s %s\n",
		inverseCommand(archived), arg)
	return nil
}

func init() {
	boardCmd.AddCommand(boardArchiveCmd)
	boardCmd.AddCommand(boardUnarchiveCmd)
}
