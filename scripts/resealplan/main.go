// Command resealplan is ready-43d's DRY RUN: the gate an operator passes before
// ready-336's re-seal touches anything.
//
// It reports, per board, exactly what a re-seal WOULD do — how many coordinates it
// would seal, how many it would skip AND WHY, the projected sealed sizes, how many
// references would break, and which readers would lose access to history they can
// read today — and it writes nothing.
//
// "WRITES NOTHING" IS PROVEN HERE, NOT ASSERTED. Two independent ways, because this
// project has a documented case of a republish believed to be a no-op that was not
// (ready-500), and a dry run inherits that credibility problem:
//
//   - STRUCTURALLY, in the library: BuildResealPlan is handed a relay URL and calls
//     only the paginated read path. It constructs no Publisher and holds no key.
//     TestBuildResealPlan_IsProvablyReadOnly runs it against a fixture relay that
//     counts EVENT frames at arrival and fails if the count is not zero.
//   - OBSERVATIONALLY, here: this command hashes the local append-only log before and
//     after the whole pass and refuses to exit 0 if the digest moved. That covers the
//     local side, which no amount of reasoning about relay calls can.
//
// USAGE
//
//	go run ./scripts/resealplan \
//	  [--relay wss://relay.3dl.network] \
//	  [--rd-home ~/.config/rd] \
//	  [--log <path to nostr-log.jsonl>] \
//	  [--board <d-tag>]        only this board, repeatable; default every live board
//	  [--json-out plan.json]   the full per-coordinate plan
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

type boardFlag []string

func (b *boardFlag) String() string     { return fmt.Sprint(*b) }
func (b *boardFlag) Set(v string) error { *b = append(*b, v); return nil }

func main() {
	relay := flag.String("relay", "wss://relay.3dl.network", "relay to plan against (the public, unrestricted-read relay is the one 'who can read this' is about)")
	rdHome := flag.String("rd-home", "", "rd home holding nostr-identity.json (default: $RD_HOME, else $XDG_CONFIG_HOME/rd, else ~/.config/rd)")
	logPath := flag.String("log", "", "local append-only log to hash before/after as the read-only proof (default: ./.ready/nostr-log.jsonl if present)")
	jsonOut := flag.String("json-out", "", "write the full per-coordinate plan as JSON to this path")
	timeout := flag.Duration("timeout", 30*time.Minute, "overall deadline")
	var boards boardFlag
	flag.Var(&boards, "board", "plan only this board d-tag (repeatable); default is every live board")
	flag.Parse()

	owner, err := ownerPubkey(*rdHome)
	if err != nil {
		fatalf("resolve owner pubkey: %v", err)
	}

	// READ-ONLY PROOF, part 1: the local log's digest before anything runs.
	logFile := resolveLogPath(*logPath)
	beforeDigest, beforeErr := digest(logFile)
	if beforeErr != nil && logFile != "" {
		fatalf("hash local log %s: %v", logFile, beforeErr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	live, err := rdsync.DiscoverLiveBoards(ctx, *relay, owner)
	if err != nil {
		fatalf("discover live boards: %v", err)
	}
	if len(live) == 0 {
		fatalf("discovered zero live boards for owner %s off %s — that almost certainly means the query is wrong, not that the portfolio is empty", owner, *relay)
	}
	if len(boards) > 0 {
		want := map[string]bool{}
		for _, b := range boards {
			want[b] = true
		}
		var narrowed []rdsync.LiveBoardDef
		for _, b := range live {
			if want[b.D] {
				narrowed = append(narrowed, b)
			}
		}
		if len(narrowed) == 0 {
			fatalf("none of the requested boards %v are live for this owner", []string(boards))
		}
		live = narrowed
	}

	plans := make([]rdsync.BoardResealPlan, 0, len(live))
	for _, b := range live {
		p, perr := rdsync.BuildResealPlan(ctx, *relay, owner, b.D, b.Coord)
		if perr != nil {
			fatalf("plan board %s (%s): %v", b.D, b.Coord, perr)
		}
		plans = append(plans, p)
	}
	sort.Slice(plans, func(i, j int) bool { return plans[i].Board < plans[j].Board })

	if *jsonOut != "" {
		if err := writeJSON(*jsonOut, plans); err != nil {
			fatalf("write JSON: %v", err)
		}
	}
	printReport(*relay, plans)

	// READ-ONLY PROOF, part 2: the local log must be byte-identical afterwards.
	afterDigest, afterErr := digest(logFile)
	if afterErr != nil && logFile != "" {
		fatalf("re-hash local log %s: %v", logFile, afterErr)
	}
	switch {
	case logFile == "":
		fmt.Printf("\nREAD-ONLY PROOF: no local log found to hash (pass --log to include the local-side check).\n")
		fmt.Printf("                 the relay side is proven structurally by TestBuildResealPlan_IsProvablyReadOnly.\n")
	case beforeDigest != afterDigest:
		fatalf("THE DRY RUN MUTATED THE LOCAL LOG: %s went from %s to %s. Do not proceed; this is the ready-500 class of defect.", logFile, beforeDigest[:16], afterDigest[:16])
	default:
		fmt.Printf("\nREAD-ONLY PROOF: %s unchanged across the pass (sha256 %s).\n", logFile, beforeDigest[:16])
	}
}

func printReport(relay string, plans []rdsync.BoardResealPlan) {
	fmt.Printf("re-seal DRY RUN against %s at %s — NOTHING IS WRITTEN\n\n", relay, time.Now().UTC().Format(time.RFC3339))
	fmt.Printf("%-24s %6s %8s %9s %9s %9s %7s %8s\n", "board", "epoch", "cards", "reseal", "sealed", "foreign", "refs", "readers")
	var totCards, totReseal, totRefs, totReaders int
	var inScope int
	for _, p := range plans {
		if !p.Confidential {
			continue // reported in the out-of-scope roll-up below
		}
		inScope++
		fmt.Printf("%-24s %6d %8d %9d %9d %9d %7d %8d\n",
			p.Board, p.CurrentEpoch, p.Cards, p.WouldReseal,
			p.Skipped[rdsync.SkipAlreadySealed], p.Skipped[rdsync.SkipForeignAuthor],
			p.BrokenRefs, len(p.ReadersLosingHistory))
		totCards += p.Cards
		totReseal += p.WouldReseal
		totRefs += p.BrokenRefs
		totReaders += len(p.ReadersLosingHistory)
	}
	fmt.Printf("%-24s %6s %8d %9d %9s %9s %7d %8d\n", fmt.Sprintf("TOTAL (%d confidential)", inScope), "", totCards, totReseal, "", "", totRefs, totReaders)

	var outCards, outBoards int
	for _, p := range plans {
		if p.Confidential {
			continue
		}
		outBoards++
		outCards += p.Cards
	}
	fmt.Printf("\nOUT OF SCOPE: %d boards / %d cards have no CEK-bearing grant — never confidential,\n", outBoards, outCards)
	fmt.Printf("              their plaintext is intended, and sealing them would make them\n")
	fmt.Printf("              unreadable to their own audience.\n")

	// The halt-the-pass set and the human cost, named rather than buried in a column.
	var halting []string
	readers := map[string][]string{}
	for _, p := range plans {
		if n := p.Skipped[rdsync.SkipOverLimit]; n > 0 {
			halting = append(halting, fmt.Sprintf("%s (%d)", p.Board, n))
		}
		for _, pk := range p.ReadersLosingHistory {
			readers[pk] = append(readers[pk], p.Board)
		}
	}
	if len(halting) > 0 {
		fmt.Printf("\nWOULD HALT THE PASS — coordinates whose sealed form exceeds the relay limit: %v\n", halting)
	} else {
		fmt.Printf("\nWOULD HALT THE PASS: none. No coordinate's sealed form exceeds the relay limit.\n")
	}
	if len(readers) == 0 {
		fmt.Printf("READERS LOSING HISTORY: none. Every non-revoked grantee holds the current epoch.\n")
	} else {
		fmt.Printf("READERS LOSING HISTORY — hold an older CEK epoch and can read the plaintext tail TODAY:\n")
		keys := make([]string, 0, len(readers))
		for pk := range readers {
			keys = append(keys, pk)
		}
		sort.Strings(keys)
		for _, pk := range keys {
			fmt.Printf("  %s on %v\n", pk, readers[pk])
		}
		fmt.Printf("  These are the people ready-402 has to tell BEFORE the pass runs.\n")
	}
}

// resolveLogPath returns the local log to hash: the explicit flag, else this
// project's ./.ready/nostr-log.jsonl when it exists, else "" (no local-side check).
func resolveLogPath(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	def := filepath.Join(".ready", "nostr-log.jsonl")
	if _, err := os.Stat(def); err == nil {
		return def
	}
	return ""
}

// digest returns the sha256 of a file, or "" when path is empty.
func digest(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func ownerPubkey(rdHome string) (string, error) {
	home := rdHome
	if home == "" {
		home = os.Getenv("RD_HOME")
	}
	if home == "" {
		if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
			home = filepath.Join(xdg, "rd")
		}
	}
	if home == "" {
		hd, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve $HOME: %w", err)
		}
		home = filepath.Join(hd, ".config", "rd")
	}
	path := nostr.DefaultKeyPath(home)
	pk, err := nostr.StoredPubKeyHex(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	if pk == "" {
		return "", fmt.Errorf("%s carries no pubkey_hex", path)
	}
	return pk, nil
}

func writeJSON(path string, plans []rdsync.BoardResealPlan) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(plans, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "resealplan: "+format+"\n", a...)
	os.Exit(1)
}
