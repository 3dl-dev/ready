// Command boardinventory is ready-207's re-runnable measurement: an exact,
// CURRENT, per-board list of every addressable kind-30302 card coordinate
// across every LIVE board this machine's owner key controls, read directly
// off a relay (default: wss://relay.3dl.network, the public, unrestricted-read
// relay — the one that matters for "who can read this today").
//
// It is measure-only: it opens zero write connections, signs nothing, and
// changes nothing on any relay. It needs no decryption key either — telling
// plaintext from sealed only requires reading the clear ["enc","1"] marker
// tag a sealed card carries (pkg/sync/envelope.go, tagEnc).
//
// WHY THIS IS CODE, NOT A ONE-OFF QUERY: the 2026-07-29T04:19Z figure
// (5,446 cards, 4,043 plaintext / 1,403 sealed across 24 live boards, on
// ready-336) is stale by construction — eight projects are actively writing.
// This tool reproduces that measurement's exact method (pkg/sync.
// DiscoverLiveBoards / InventoryBoardCards) so ready-336's re-seal pass can
// re-derive the current set on every resume instead of trusting a snapshot.
//
// MEASUREMENT DISCIPLINE, inherited from pkg/sync/boardinventory.go and
// non-negotiable: this tool issues no "authors"-filtered REQ anywhere. Board
// discovery walks every kind-30301 event on the relay and narrows to the
// owner client-side; card discovery walks each board's own "#a" coordinate.
// Both page through fetchPaged's until-walk. See ready-d84/ready-5c5/
// ready-0ab for why an authors filter is unsafe on this relay.
//
// USAGE
//
//	go run ./scripts/boardinventory \
//	  [--relay wss://relay.3dl.network] \
//	  [--rd-home ~/.config/rd] \
//	  [--csv-out docs/ops/board-inventory.csv] \
//	  [--json-out docs/ops/board-inventory.json]
//
// With no --csv-out/--json-out, the per-coordinate rows are not written
// anywhere (only the per-board summary prints to stdout) — pass at least one
// to persist a snapshot.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

func main() {
	relay := flag.String("relay", "wss://relay.3dl.network", "relay to measure (the public, unrestricted-read relay is the one 'who can read this' is about)")
	rdHome := flag.String("rd-home", "", "rd home directory holding nostr-identity.json (default: $RD_HOME, else $XDG_CONFIG_HOME/rd, else ~/.config/rd)")
	csvOut := flag.String("csv-out", "", "write the per-coordinate inventory as CSV to this path (optional)")
	jsonOut := flag.String("json-out", "", "write the per-coordinate inventory as JSON to this path (optional)")
	timeout := flag.Duration("timeout", 5*time.Minute, "overall deadline for the whole measurement pass")
	flag.Parse()

	owner, err := ownerPubkey(*rdHome)
	if err != nil {
		fatalf("resolve owner pubkey: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	boards, err := rdsync.DiscoverLiveBoards(ctx, *relay, owner)
	if err != nil {
		fatalf("discover live boards: %v", err)
	}
	if len(boards) == 0 {
		fatalf("discovered zero live boards for owner %s off %s — that almost certainly means the query is wrong, not that the portfolio is empty", owner, *relay)
	}

	var allRows []rdsync.CardCoordRow
	var allTotals []rdsync.BoardCardTotals
	for _, b := range boards {
		rows, totals, err := rdsync.InventoryBoardCards(ctx, *relay, b.D, b.Coord)
		if err != nil {
			fatalf("inventory board %s (%s): %v", b.D, b.Coord, err)
		}
		allRows = append(allRows, rows...)
		allTotals = append(allTotals, totals)
	}
	sort.Slice(allTotals, func(i, j int) bool { return allTotals[i].Board < allTotals[j].Board })
	sort.Slice(allRows, func(i, j int) bool {
		if allRows[i].Board != allRows[j].Board {
			return allRows[i].Board < allRows[j].Board
		}
		if allRows[i].ItemID != allRows[j].ItemID {
			return allRows[i].ItemID < allRows[j].ItemID
		}
		return allRows[i].Coord < allRows[j].Coord
	})

	if *csvOut != "" {
		if err := writeCSV(*csvOut, allRows); err != nil {
			fatalf("write CSV: %v", err)
		}
	}
	if *jsonOut != "" {
		if err := writeJSON(*jsonOut, allRows); err != nil {
			fatalf("write JSON: %v", err)
		}
	}

	printSummary(*relay, allTotals)
}

// ownerPubkey resolves the owner pubkey from the rd home's stored key file,
// reading ONLY the pubkey_hex field (nostr.StoredPubKeyHex) — this tool never
// loads the secret, since it never signs anything.
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

func writeCSV(path string, rows []rdsync.CardCoordRow) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	w := csv.NewWriter(f)
	defer w.Flush()
	// Column order matches ready-207's DONE CONDITION exactly (board, item id,
	// kind, wire size in bytes, enc flag, created_at), plus board_coord/coord/
	// event_id — needed by ready-c53 (per-coordinate sealed-size projection)
	// and ready-c9d (every place an event id is cited must be enumerated).
	if err := w.Write([]string{"board", "item_id", "kind", "wire_bytes", "enc", "created_at", "board_coord", "coord", "event_id"}); err != nil {
		return err
	}
	for _, r := range rows {
		if err := w.Write([]string{
			r.Board,
			r.ItemID,
			strconv.Itoa(r.Kind),
			strconv.Itoa(r.WireBytes),
			strconv.FormatBool(r.Sealed),
			strconv.FormatInt(r.CreatedAt, 10),
			r.BoardCoord,
			r.Coord,
			r.EventID,
		}); err != nil {
			return err
		}
	}
	return w.Error()
}

func writeJSON(path string, rows []rdsync.CardCoordRow) error {
	if dir := filepath.Dir(path); dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(rows, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

func printSummary(relay string, totals []rdsync.BoardCardTotals) {
	fmt.Printf("board inventory off %s at %s\n\n", relay, time.Now().UTC().Format(time.RFC3339))
	fmt.Printf("%-22s %8s %10s %8s\n", "board", "cards", "plaintext", "sealed")
	var cards, plain, sealed int
	for _, t := range totals {
		fmt.Printf("%-22s %8d %10d %8d\n", t.Board, t.Cards, t.Plaintext, t.Sealed)
		cards += t.Cards
		plain += t.Plaintext
		sealed += t.Sealed
	}
	fmt.Printf("%-22s %8d %10d %8d\n", fmt.Sprintf("TOTAL (%d boards)", len(totals)), cards, plain, sealed)
}

func fatalf(format string, a ...any) {
	fmt.Fprintf(os.Stderr, "boardinventory: "+format+"\n", a...)
	os.Exit(1)
}
