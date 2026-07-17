package main

// `rd status` (ready-e31) — the ONE command an operator runs when a board is not
// behaving: it prints, in a handful of plain lines, the state of THIS machine +
// board and the single next command to run when something is wrong.
//
// It answers three questions and, when any is broken, names the exact remedy:
//   - WHO am I here?      pubkey + the party/email its person-alias binds it to.
//   - Can I READ it?      a linked board, and (if confidential) a read key.
//   - Can I WRITE it?     the owner, or an owner-signed grant admitting this key.
//
// Every classification is derived from the SAME signed sources the read/write
// paths use (nostrPinnedBoard, DeriveBoardKeyring, DeriveReadTrust, the nostr
// projection) — there is no separate health probe to drift out of step. Raw hex
// (pubkeys, 30301 coordinates) is withheld unless --debug, EXCEPT the pubkey the
// owner must copy into the `rd grant --all-boards <pubkey>` remedy.

import (
	"fmt"
	"strings"

	"github.com/3dl-dev/ready/pkg/identity"
	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/spf13/cobra"
)

// statusState is the single dominant condition `rd status` classifies the current
// machine+board into. Exactly one is reported — the most actionable one.
type statusState int

const (
	// statusHealthy: a linked, readable board — "all good" + item count.
	statusHealthy statusState = iota
	// statusNoBoard: no board is linked in this directory.
	statusNoBoard
	// statusOwnsUnlinked: no board here, but this key already OWNS one elsewhere —
	// the "about to join/init a competing board" trap the join guard warns about.
	statusOwnsUnlinked
	// statusNoReadKey: a bootstrapped confidential board this key cannot decrypt.
	statusNoReadKey
	// statusNoAlias: linked + readable, but the personal queue is empty because this
	// key carries no person-alias, so `--for <email>` work does not resolve to it.
	statusNoAlias
)

// statusReport is the computed, print-ready view of the current machine+board.
type statusReport struct {
	Pubkey       string // this machine's signing pubkey (hex)
	Party        string // aliased email handle for this key, "" if unaliased
	HasAlias     bool
	HasProject   bool
	Board        string // pinned board coordinate, "" if none
	BoardName    string // the board's human d identifier
	IsOwner      bool
	Confidential bool
	Bootstrapped bool // confidential cutover present (board is actually sealed)
	CanRead      bool
	CanWrite     bool
	Granted      bool   // an owner-signed grant admits this key
	OwnedCoord   string // a board this key owns when the current dir is unlinked
	ItemCount    int
	MyCount      int
	State        statusState
}

// computeStatus resolves the current machine+board into a statusReport. It reads
// only the local authoritative log + config (no network) — the local signed log
// is authoritative, and a status read must never block on a relay.
func computeStatus() (*statusReport, error) {
	k, err := nostrKey()
	if err != nil {
		return nil, err
	}
	self := k.PubKeyHex()
	rep := &statusReport{Pubkey: self}

	dir, hasProject := readyProjectDir()
	rep.HasProject = hasProject

	var events []*nostr.Event
	if hasProject {
		rep.Board = nostrPinnedBoard(dir)
		if evs, rerr := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll(); rerr == nil {
			events = evs
		}
	}

	// Identity: resolve this key's party from its own trust closure (kind 39302).
	r := identity.Resolve(events, []string{self})
	partySet := map[string]bool{self: true}
	if p, ok := r.PartyForPubkey(self); ok {
		for _, pk := range p.Pubkeys {
			partySet[pk] = true
		}
		for _, em := range p.Emails {
			partySet[em] = true
		}
		if len(p.Emails) > 0 {
			rep.Party = p.Emails[0]
			rep.HasAlias = true
		}
	}

	// No board linked here — but distinguish "you already own one" (don't fork a
	// competing board) from "you have none" (adopt a teammate's).
	if rep.Board == "" {
		if dir != "" {
			if coord := keyOwnedBoard(self, dir); coord != "" {
				rep.OwnedCoord = coord
				rep.State = statusOwnsUnlinked
				return rep, nil
			}
		}
		rep.State = statusNoBoard
		return rep, nil
	}

	owner, boardD, ok := rdSync.ParseBoardCoord(rep.Board)
	if !ok {
		// A present-but-malformed pin is, from the operator's seat, an unusable board.
		rep.State = statusNoBoard
		return rep, nil
	}
	rep.BoardName = boardD
	rep.IsOwner = self == owner
	rep.Confidential = boardIsConfidential(dir)

	coord := rdSync.BoardCoord(owner, boardD)
	kr := rdSync.DeriveBoardKeyring(events, k, owner, boardD)
	_, rep.Bootstrapped = kr.Cutover(coord)
	_, _, haveCEK := kr.CurrentEpoch(coord)
	rep.Granted = rdSync.DeriveReadTrust(events, owner, boardD)[self]

	rep.CanRead = rep.IsOwner || !rep.Confidential || !rep.Bootstrapped || haveCEK
	rep.CanWrite = rep.IsOwner || rep.Granted

	// Confidential + actually sealed + I hold no key + not the owner: I cannot read.
	// (An UNBOOTSTRAPPED confidential board is still plaintext, so it must NOT land
	// here — reads work and the owner's first write self-heals it.)
	if rep.Confidential && rep.Bootstrapped && !haveCEK && !rep.IsOwner {
		rep.State = statusNoReadKey
		return rep, nil
	}

	items, _, err := nostrProjectAllItems()
	if err != nil {
		return nil, err
	}
	rep.ItemCount = len(items)
	emailScoped := false
	for _, it := range items {
		if partySet[it.For] {
			rep.MyCount++
		}
		if strings.Contains(it.For, "@") {
			emailScoped = true
		}
	}

	// Linked + readable, yet the board's email-scoped work does not resolve to this
	// key: it carries no person-alias, so the my-work queue is silently empty.
	if rep.ItemCount > 0 && rep.MyCount == 0 && !rep.HasAlias && emailScoped {
		rep.State = statusNoAlias
		return rep, nil
	}

	rep.State = statusHealthy
	return rep, nil
}

// printStatusReport writes the human view. debug adds the withheld hex (pubkey +
// board coordinate). Kept to a handful of lines — this is a glance, not a dump.
func printStatusReport(rep *statusReport, debug bool) {
	fmt.Println("rd status")

	// WHO.
	if rep.HasAlias {
		fmt.Printf("  you:    %s\n", rep.Party)
	} else {
		fmt.Println("  you:    (no person-alias for this key)")
	}
	if debug {
		fmt.Printf("  pubkey: %s\n", rep.Pubkey)
	}

	// BOARD + read/write, only once a board is linked.
	if rep.Board != "" {
		fmt.Printf("  board:  %s (linked)\n", rep.BoardName)
		if debug {
			fmt.Printf("  coord:  %s\n", rep.Board)
		}
		fmt.Printf("  read:   %s\n", readLine(rep))
		fmt.Printf("  write:  %s\n", writeLine(rep))
	} else {
		fmt.Println("  board:  none linked here")
	}

	fmt.Println()

	// The single next action (or the all-clear).
	switch rep.State {
	case statusHealthy:
		fmt.Printf("all good — %s (%d in your queue)\n", itemCount(rep.ItemCount), rep.MyCount)
	case statusNoBoard:
		fmt.Println("No board is linked in this directory.")
		fmt.Println("Run 'rd follow <owner>' to adopt one (or 'rd init' to start a new board here).")
	case statusOwnsUnlinked:
		fmt.Println("No board is linked here, but your key already owns a board.")
		fmt.Println("Run 'rd follow <you>' to adopt your own board(s) — do NOT 'rd init' or 'rd join'")
		fmt.Println("here, which would fork a competing board under a fresh key.")
	case statusNoReadKey:
		fmt.Println("This board is confidential and your key holds no read key.")
		fmt.Printf("Ask the owner to run:  rd grant --all-boards %s\n", rep.Pubkey)
		fmt.Println("(Your writes self-heal automatically once that grant lands.)")
	case statusNoAlias:
		fmt.Printf("%s on this board, but none are in your queue.\n", itemCount(rep.ItemCount))
		fmt.Println("Your key has no person-alias, so '--for <email>' work doesn't resolve to you.")
		fmt.Println("Run:  rd identify --add-email <you@email>")
	}
}

// readLine describes read access in plain words (no hex).
func readLine(rep *statusReport) string {
	if rep.CanRead {
		return "yes"
	}
	return "NO — confidential board, you hold no read key"
}

// writeLine describes write authority in plain words (no hex).
func writeLine(rep *statusReport) string {
	switch {
	case rep.IsOwner:
		return "yes (you own this board)"
	case rep.Granted:
		return "yes (granted)"
	default:
		return "no grant yet"
	}
}

// itemCount renders a pluralized item count ("1 item" / "N items").
func itemCount(n int) string {
	if n == 1 {
		return "1 item"
	}
	return fmt.Sprintf("%d items", n)
}

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Show this machine's identity, board, and the one next command when something's wrong",
	Long: `Show the state of THIS machine and board at a glance, and the SINGLE next
command to run when something is wrong.

Reports your identity (the party/email its key is aliased to), whether this
directory has a linked board and whether you can READ and WRITE it, and — when a
board is misbehaving — the exact remedy:

  no board here              -> rd follow <owner>
  confidential, no read key  -> ask the owner: rd grant --all-boards <your-pubkey>
  empty queue, no alias      -> rd identify --add-email <you@email>

Raw hex (pubkeys, board coordinates) is hidden unless you pass --debug.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		rep, err := computeStatus()
		if err != nil {
			return err
		}
		printStatusReport(rep, debugOutput)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}
