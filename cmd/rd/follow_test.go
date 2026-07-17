package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/identity"
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// TestFollow_BindsAllOwnerBoardsKeepingKey is the ready-636 DONE condition, driven
// end-to-end against a SEEDED snapshot (no live relay — followFetch is the injected
// seam, the ready-6d5 pattern). Setup: a scratch RD_HOME holding an EXISTING owner
// key that published 3 boards (each with an item) plus a person-alias mapping the
// owner email to that key. `rd follow baron@3dl.dev` must:
//
//   - bind ALL 3 boards, writing a committed .ready/board.json for each;
//   - publish exactly ONE person-alias;
//   - leave the pre-existing key UNCHANGED (never re-mint / overwrite — edge #1);
//   - import each board's full history so `rd ready` (ProjectItems) returns its item;
//   - never require or print a raw 64-hex coordinate the operator had to type.
func TestFollow_BindsAllOwnerBoardsKeepingKey(t *testing.T) {
	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	// PRE-EXISTING owner key: whatever nostrKey() first-run creates in this RD_HOME
	// IS the owner identity we then seed boards under. Snapshot its bytes so we can
	// prove `rd follow` never rewrites it.
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	keyPath, err := nostr.ActorKeyPath(rdHome, rdActor())
	if err != nil {
		t.Fatalf("ActorKeyPath: %v", err)
	}
	keyBefore, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading pre-existing key: %v", err)
	}

	const email = "baron@3dl.dev"
	boardNames := []string{"proj1", "proj2", "proj3"}

	// SEED the relay snapshot: one person-alias (email->owner, signed by owner, so
	// the local resolver trusts it since owner IS this machine's key), plus for each
	// board a signed 30301 board event, an owner-signed card, and a board-scoped
	// status event — the full history a fresh box would pull.
	var seeded []*nostr.Event
	alias, err := identity.BuildAliasEvent(k, identity.AliasSpec{
		Handle:  email,
		Pubkeys: []string{owner},
		Emails:  []string{email},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}
	seeded = append(seeded, alias)

	itemForBoard := map[string]string{}
	for i, name := range boardNames {
		ts := int64(1000 + i)
		be, err := rdSync.BuildBoardEvent(k, rdSync.BoardSpec{BoardD: name, Title: name, Maintainers: []string{owner}}, ts)
		if err != nil {
			t.Fatalf("BuildBoardEvent %s: %v", name, err)
		}
		itemID := "ready-" + name
		itemForBoard[name] = itemID
		coord := rdSync.BoardCoord(owner, name)
		card, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{
			ItemID:      itemID,
			Title:       itemID,
			Status:      state.StatusActive,
			Type:        "task",
			BoardD:      name,
			BoardAuthor: owner,
		}, ts)
		if err != nil {
			t.Fatalf("BuildCardEvent %s: %v", name, err)
		}
		st, err := rdSync.BuildStatusEventWithIssueRoot(k, itemID, state.StatusActive, card.ID, "", coord, "", ts, nil)
		if err != nil {
			t.Fatalf("BuildStatusEvent %s: %v", name, err)
		}
		seeded = append(seeded, be, card, st)
	}

	// Inject the seeded snapshot as the relay medium (no network).
	origFetch := followFetch
	followFetch = func(_ context.Context, _ []string, _ map[string]any) ([]*nostr.Event, error) {
		return seeded, nil
	}
	t.Cleanup(func() { followFetch = origFetch })

	root := filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	rep, err := runFollow(followOpts{
		who:    email,
		email:  email,
		root:   root,
		relays: []string{"wss://seed.example.test"},
	})
	if err != nil {
		t.Fatalf("runFollow: %v", err)
	}

	// (a) KEY UNCHANGED: not minted, bytes byte-identical.
	if rep.MintedKey {
		t.Error("rd follow re-minted the machine key; it must KEEP an existing owner key (edge #1)")
	}
	keyAfter, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("reading key after follow: %v", err)
	}
	if string(keyBefore) != string(keyAfter) {
		t.Error("the pre-existing owner key file changed after rd follow; it must be left UNTOUCHED")
	}
	if rep.Pubkey != owner {
		t.Errorf("reported pubkey %s != pre-existing owner %s", rep.Pubkey, owner)
	}

	// (b) ALL 3 BOARDS BOUND, each with a committed board.json holding the coord.
	if len(rep.BoardDirs) != 3 {
		t.Fatalf("bound %d boards, want 3: %+v", len(rep.BoardDirs), rep.BoardDirs)
	}
	aliasCount := 0
	for _, name := range boardNames {
		dir := filepath.Join(root, name)
		wantCoord := rdSync.BoardCoord(owner, name)

		b, err := rdconfig.LoadBoardBinding(dir)
		if err != nil {
			t.Fatalf("LoadBoardBinding %s: %v", name, err)
		}
		if b.Board != wantCoord {
			t.Errorf("board.json[%s].Board = %q, want %q", name, b.Board, wantCoord)
		}
		if _, err := os.Stat(rdconfig.BoardBindingPath(dir)); err != nil {
			t.Errorf("no committed board.json for %s: %v", name, err)
		}

		// (c) FULL HISTORY IMPORTED: ProjectItems over the bound log returns the item
		// (this is what `rd ready` reads).
		events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
		if err != nil {
			t.Fatalf("ReadAll %s: %v", name, err)
		}
		for _, e := range events {
			if e.Kind == identity.KindPersonAlias {
				aliasCount++
			}
		}
		items := rdSync.ProjectItems(events, rdSync.ProjectOptions{
			Trusted:     map[string]bool{owner: true},
			PinnedBoard: wantCoord,
		})
		it := items[itemForBoard[name]]
		if it == nil {
			t.Errorf("board %s: item %q did not project — rd ready would show nothing", name, itemForBoard[name])
			continue
		}
		if it.Status != state.StatusActive {
			t.Errorf("board %s item status = %q, want %q", name, it.Status, state.StatusActive)
		}
	}

	// (d) EXACTLY ONE alias published across all bound logs.
	if aliasCount != 1 {
		t.Errorf("found %d person-alias events across bound board logs, want exactly 1", aliasCount)
	}
	if rep.AliasEventID == "" {
		t.Error("runFollow reported no alias event id; it must publish one person-alias")
	}
}

// TestFollow_BoardFlagScopesToOneBoard is ready-e50 CLI/runFollow-level coverage
// for `rd follow <owner> --board <name>` (followOpts.boardD, wired through
// runFollow -> rdSync.DiscoverOwnerBoards(snapshot, ownerPubkeys, opts.boardD)):
// given an owner who published MULTIPLE boards, passing --board must bind EXACTLY
// the named board — no sibling board gets a bound dir or a committed board.json.
// Mirrors TestFollow_BindsAllOwnerBoardsKeepingKey's seeded-snapshot / no-network
// setup (followFetch injection) but asserts the single-board scope instead of the
// bind-everything default.
func TestFollow_BoardFlagScopesToOneBoard(t *testing.T) {
	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()

	const email = "baron@3dl.dev"
	boardNames := []string{"proj1", "proj2", "proj3"}

	var seeded []*nostr.Event
	alias, err := identity.BuildAliasEvent(k, identity.AliasSpec{
		Handle:  email,
		Pubkeys: []string{owner},
		Emails:  []string{email},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}
	seeded = append(seeded, alias)

	for i, name := range boardNames {
		ts := int64(1000 + i)
		be, err := rdSync.BuildBoardEvent(k, rdSync.BoardSpec{BoardD: name, Title: name, Maintainers: []string{owner}}, ts)
		if err != nil {
			t.Fatalf("BuildBoardEvent %s: %v", name, err)
		}
		seeded = append(seeded, be)
	}

	origFetch := followFetch
	followFetch = func(_ context.Context, _ []string, _ map[string]any) ([]*nostr.Event, error) {
		return seeded, nil
	}
	t.Cleanup(func() { followFetch = origFetch })

	root := filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	const wantBoard = "proj2"
	rep, err := runFollow(followOpts{
		who:    email,
		boardD: wantBoard,
		email:  email,
		root:   root,
		relays: []string{"wss://seed.example.test"},
	})
	if err != nil {
		t.Fatalf("runFollow: %v", err)
	}

	if len(rep.BoardDirs) != 1 {
		t.Fatalf("bound %d boards with --board %s, want exactly 1: %+v", len(rep.BoardDirs), wantBoard, rep.BoardDirs)
	}
	if _, ok := rep.BoardDirs[wantBoard]; !ok {
		t.Fatalf("BoardDirs = %+v, want the named board %q bound", rep.BoardDirs, wantBoard)
	}
	for _, name := range boardNames {
		if name == wantBoard {
			continue
		}
		if _, ok := rep.BoardDirs[name]; ok {
			t.Errorf("sibling board %q was bound; --board %s must scope to ONLY the named board", name, wantBoard)
		}
		if _, err := os.Stat(rdconfig.BoardBindingPath(filepath.Join(root, name))); err == nil {
			t.Errorf("sibling board %q got a committed board.json on disk despite --board %s scoping", name, wantBoard)
		}
	}

	dir := filepath.Join(root, wantBoard)
	b, err := rdconfig.LoadBoardBinding(dir)
	if err != nil {
		t.Fatalf("LoadBoardBinding: %v", err)
	}
	wantCoord := rdSync.BoardCoord(owner, wantBoard)
	if b.Board != wantCoord {
		t.Errorf("board.json.Board = %q, want %q", b.Board, wantCoord)
	}
}

// seedFollowFixture builds an owner key plus a signed person-alias + one
// signed 30301 board event per name in boardNames, mirroring the setup in
// TestFollow_BindsAllOwnerBoardsKeepingKey but factored out so the
// ready-4c9c confirmation-gate tests can seed an arbitrary board count.
func seedFollowFixture(t *testing.T, base string, boardNames []string) (owner string, seeded []*nostr.Event) {
	t.Helper()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner = k.PubKeyHex()

	const email = "baron@3dl.dev"
	alias, err := identity.BuildAliasEvent(k, identity.AliasSpec{
		Handle:  email,
		Pubkeys: []string{owner},
		Emails:  []string{email},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}
	seeded = append(seeded, alias)

	for i, name := range boardNames {
		ts := int64(1000 + i)
		be, err := rdSync.BuildBoardEvent(k, rdSync.BoardSpec{BoardD: name, Title: name, Maintainers: []string{owner}}, ts)
		if err != nil {
			t.Fatalf("BuildBoardEvent %s: %v", name, err)
		}
		seeded = append(seeded, be)
	}
	return owner, seeded
}

// TestFollow_ManyBoardsRequireConfirmation is the ready-4c9c done condition: a
// bare `rd follow <owner>` (no --board) that discovers MORE than
// followConfirmThreshold boards must NOT bind anything without confirmation.
// followConfirm is overridden (same package-level-var seam as followFetch) to
// deterministically report "declined" — exactly what a real non-interactive
// script sees (followConfirm's own isInteractive() gate) — WITHOUT this test
// touching real stdin/tty, so it cannot block if `go test` is ever run from an
// actual terminal. This proves the fix's core promise: no --all/--yes and no
// confirmation means NOTHING gets bound, no matter how many boards were
// discovered. Reproduces the reported footgun (88 boards silently bound over
// ~6 minutes with no preview/confirmation) at a small, deterministic N.
func TestFollow_ManyBoardsRequireConfirmation(t *testing.T) {
	base := t.TempDir()
	boardNames := []string{"proj1", "proj2", "proj3", "proj4", "proj5", "proj6"}
	if len(boardNames) <= followConfirmThreshold {
		t.Fatalf("fixture has %d boards, must exceed followConfirmThreshold=%d to exercise the gate", len(boardNames), followConfirmThreshold)
	}
	_, seeded := seedFollowFixture(t, base, boardNames)

	origFetch := followFetch
	followFetch = func(_ context.Context, _ []string, _ map[string]any) ([]*nostr.Event, error) {
		return seeded, nil
	}
	t.Cleanup(func() { followFetch = origFetch })

	origConfirm := followConfirm
	confirmCalledWith := []string(nil)
	followConfirm = func(names []string) bool {
		confirmCalledWith = names
		return false // declined, deterministically — no real stdin involved
	}
	t.Cleanup(func() { followConfirm = origConfirm })

	root := filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	const email = "baron@3dl.dev"
	rep, err := runFollow(followOpts{
		who:    email,
		email:  email,
		root:   root,
		relays: []string{"wss://seed.example.test"},
		// all: false — no --all/--yes, and no tty, so the >threshold gate must
		// refuse rather than silently binding all 6 boards.
	})
	if err == nil {
		t.Fatalf("runFollow with %d boards (> threshold %d) and no --all succeeded; want a confirmation error. rep=%+v", len(boardNames), followConfirmThreshold, rep)
	}
	if !strings.Contains(err.Error(), "--all") {
		t.Errorf("error %q does not mention --all as the way to skip confirmation", err.Error())
	}
	if !strings.Contains(err.Error(), "6") {
		t.Errorf("error %q does not preview the discovered board count", err.Error())
	}
	for _, name := range boardNames {
		if _, err := os.Stat(rdconfig.BoardBindingPath(filepath.Join(root, name))); err == nil {
			t.Errorf("board %q got a committed board.json despite the confirmation gate refusing — nothing must bind", name)
		}
	}
	if len(confirmCalledWith) != len(boardNames) {
		t.Errorf("followConfirm called with %d names, want all %d discovered board names previewed: %v", len(confirmCalledWith), len(boardNames), confirmCalledWith)
	}
}

// TestFollowConfirm_NonInteractiveStdinDeclinesWithoutBlocking exercises the
// REAL followConfirm (not overridden) with os.Stdin swapped for a closed pipe
// — guaranteed non-tty (a pipe's Stat().Mode() never sets ModeCharDevice) — so
// isInteractive() deterministically reports false regardless of whatever tty
// the test runner itself happens to have. Proves followConfirm's default
// implementation never prompts/blocks for a script/CI caller with no --all.
func TestFollowConfirm_NonInteractiveStdinDeclinesWithoutBlocking(t *testing.T) {
	origStdin := os.Stdin
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	w.Close() // EOF immediately if anything ever tried to read it
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = origStdin; r.Close() })

	if got := followConfirm([]string{"proj1", "proj2"}); got != false {
		t.Errorf("followConfirm on non-interactive stdin = %v, want false (must never auto-approve)", got)
	}
}

// TestFollow_AllFlagBindsAllAndReportsProgress is the --all/--yes escape hatch
// for the ready-4c9c confirmation gate: with opts.all set, a >threshold
// discovery binds every board non-interactively (no prompt, no tty needed) and
// prints a "[i/N] binding <name>..." progress line per board to stderr so a
// multi-minute follow no longer looks hung.
func TestFollow_AllFlagBindsAllAndReportsProgress(t *testing.T) {
	base := t.TempDir()
	boardNames := []string{"proj1", "proj2", "proj3", "proj4", "proj5", "proj6"}
	_, seeded := seedFollowFixture(t, base, boardNames)

	origFetch := followFetch
	followFetch = func(_ context.Context, _ []string, _ map[string]any) ([]*nostr.Event, error) {
		return seeded, nil
	}
	t.Cleanup(func() { followFetch = origFetch })

	root := filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	const email = "baron@3dl.dev"
	var rep *followReport
	var runErr error
	out := captureStderrPipe(t, func() {
		rep, runErr = runFollow(followOpts{
			who:    email,
			email:  email,
			root:   root,
			relays: []string{"wss://seed.example.test"},
			all:    true,
		})
	})
	if runErr != nil {
		t.Fatalf("runFollow with --all: %v", runErr)
	}
	if len(rep.BoardDirs) != len(boardNames) {
		t.Fatalf("bound %d boards with --all, want all %d: %+v", len(rep.BoardDirs), len(boardNames), rep.BoardDirs)
	}
	for _, name := range boardNames {
		if _, err := os.Stat(rdconfig.BoardBindingPath(filepath.Join(root, name))); err != nil {
			t.Errorf("board %q missing committed board.json despite --all: %v", name, err)
		}
		wantProgress := "] binding " + name + "..."
		if !strings.Contains(out, wantProgress) {
			t.Errorf("stderr missing per-board progress line containing %q; got:\n%s", wantProgress, out)
		}
	}
	if !strings.Contains(out, "[1/6]") || !strings.Contains(out, "[6/6]") {
		t.Errorf("stderr missing bracketed [i/N] progress counters; got:\n%s", out)
	}
}

// TestFollow_BoardFlagBypassesConfirmationGate is the ready-4c9c pin for
// `rd follow <owner> --board <name>`: even when the owner has published MORE
// boards than followConfirmThreshold, scoping to one named board must bind
// immediately with NO confirmation prompt and no --all required — the gate
// only applies to the discover-everything path.
func TestFollow_BoardFlagBypassesConfirmationGate(t *testing.T) {
	base := t.TempDir()
	boardNames := []string{"proj1", "proj2", "proj3", "proj4", "proj5", "proj6"}
	owner, seeded := seedFollowFixture(t, base, boardNames)

	origFetch := followFetch
	followFetch = func(_ context.Context, _ []string, _ map[string]any) ([]*nostr.Event, error) {
		return seeded, nil
	}
	t.Cleanup(func() { followFetch = origFetch })

	root := filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	const email = "baron@3dl.dev"
	const wantBoard = "proj4"
	rep, err := runFollow(followOpts{
		who:    email,
		boardD: wantBoard,
		email:  email,
		root:   root,
		relays: []string{"wss://seed.example.test"},
		// all: false, and the owner has 6 boards (> threshold) — must still
		// bind with no prompt because --board scopes to exactly one.
	})
	if err != nil {
		t.Fatalf("runFollow --board %s (owner has %d boards > threshold): %v", wantBoard, len(boardNames), err)
	}
	if len(rep.BoardDirs) != 1 {
		t.Fatalf("bound %d boards with --board despite owner having %d, want exactly 1: %+v", len(rep.BoardDirs), len(boardNames), rep.BoardDirs)
	}
	wantCoord := rdSync.BoardCoord(owner, wantBoard)
	dir := filepath.Join(root, wantBoard)
	b, err := rdconfig.LoadBoardBinding(dir)
	if err != nil {
		t.Fatalf("LoadBoardBinding: %v", err)
	}
	if b.Board != wantCoord {
		t.Errorf("board.json.Board = %q, want %q", b.Board, wantCoord)
	}
}

// TestFollow_NeverPrintsARawCoordinate asserts the human output uses the
// `rd grant --all-boards <pubkey>` line and never emits a 30301:<hex>:<d> board
// coordinate the operator would have to copy — the whole point of `rd follow`.
func TestFollow_NeverPrintsARawCoordinate(t *testing.T) {
	owner := randPubkey(t)
	rep := &followReport{
		Pubkey:    owner,
		MintedKey: false,
		BoardDirs: map[string]string{"proj1": "/tmp/projects/proj1", "proj2": "/tmp/projects/proj2"},
		Boards: []string{
			rdSync.BoardCoord(owner, "proj1"),
			rdSync.BoardCoord(owner, "proj2"),
		},
		AliasEventID: "deadbeef",
	}

	out := captureStdoutPipe(t, func() { printFollowReport(rep) })
	assertNoCoord(t, out, owner)
}

func assertNoCoord(t *testing.T, out, owner string) {
	t.Helper()
	if strings.Contains(out, "30301:") {
		t.Errorf("rd follow printed a raw board coordinate the operator must retype:\n%s", out)
	}
	wantGrant := "rd grant --all-boards " + owner
	if !strings.Contains(out, wantGrant) {
		t.Errorf("output missing the exact grant line %q:\n%s", wantGrant, out)
	}
}

// TestDecodeNpub_CanonicalVector decodes the canonical NIP-19 npub test vector to
// its 32-byte hex pubkey — proving the standalone bech32 decoder (no external dep)
// is correct, and that a corrupted npub is rejected on checksum.
func TestDecodeNpub_CanonicalVector(t *testing.T) {
	const npub = "npub1sn0wdenkukak0d9dfczzeacvhkrgz92ak56egt7vdgzn8pv2wfqqhrjdv9"
	const wantHex = "84dee6e676e5bb67b4ad4e042cf70cbd8681155db535942fcc6a0533858a7240"

	got, err := decodeNpub(npub)
	if err != nil {
		t.Fatalf("decodeNpub(canonical): %v", err)
	}
	if got != wantHex {
		t.Errorf("decodeNpub = %q, want %q", got, wantHex)
	}

	// A single-character corruption must fail the checksum, not decode to a
	// different-but-plausible owner.
	corrupt := npub[:len(npub)-1] + "0"
	if _, err := decodeNpub(corrupt); err == nil {
		t.Error("decodeNpub accepted a corrupted npub — checksum not enforced")
	}
}

// TestImportFollowedBoard_DropsForeignAndForgedEvents is the ready-50c fail-closed
// pin on importFollowedBoard's admit loop (~line 337-353): a hostile relay can
// serve (a) a validly-signed event authored by a FOREIGN key that is neither the
// board owner nor granted any role, and (b) a FORGED event whose pubkey field
// claims the owner's identity but whose signature does not verify against it.
// Both must be dropped before AppendUnique — Verify() catches the forgery,
// trusted[e.PubKey] (derived from DeriveReadTrust) catches the foreign key — so
// neither ever lands in the bound board's authoritative log. A legit owner-signed
// card in the same snapshot IS admitted, proving the loop is selective rather than
// accidentally dropping everything.
func TestImportFollowedBoard_DropsForeignAndForgedEvents(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RD_HOME", filepath.Join(base, "rdhome"))
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey owner: %v", err)
	}
	attacker, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey attacker: %v", err)
	}
	ownerHex := owner.PubKeyHex()
	const boardD = "proj1"
	coord := rdSync.BoardCoord(ownerHex, boardD)

	// (a) LEGIT: owner-signed, board-scoped card. Must be admitted — the control
	// that proves the admit loop isn't just dropping everything.
	legit, err := rdSync.BuildCardEvent(owner, rdSync.CardSpec{
		ItemID: "ready-legit", Title: "legit", Status: state.StatusActive, Type: "task",
		BoardD: boardD, BoardAuthor: ownerHex,
	}, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent legit: %v", err)
	}

	// (b) FOREIGN KEY: validly signed by the ATTACKER (Verify() passes — nothing
	// wrong with the signature itself), board-scoped via BoardAuthor=owner so it
	// clears eventBelongsToFollowedBoard — but the attacker holds no board-owner
	// status and no role grant, so trusted[e.PubKey] must reject it.
	foreign, err := rdSync.BuildCardEvent(attacker, rdSync.CardSpec{
		ItemID: "ready-foreign", Title: "foreign", Status: state.StatusActive, Type: "task",
		BoardD: boardD, BoardAuthor: ownerHex,
	}, 1001)
	if err != nil {
		t.Fatalf("BuildCardEvent foreign: %v", err)
	}
	if err := foreign.Verify(); err != nil {
		t.Fatalf("foreign event must verify (it IS validly signed, just untrusted): %v", err)
	}

	// (c) FORGED: signed by the attacker, then the pubkey field is overwritten to
	// claim the OWNER's identity and the id recomputed to match the new canonical
	// form — but the signature is still the attacker's, over the ORIGINAL
	// (attacker-pubkey) id. An id-only check would be fooled; Verify() must reject
	// it because the schnorr signature does not verify against the claimed owner
	// pubkey.
	forged, err := rdSync.BuildCardEvent(attacker, rdSync.CardSpec{
		ItemID: "ready-forged", Title: "forged", Status: state.StatusActive, Type: "task",
		BoardD: boardD, BoardAuthor: ownerHex,
	}, 1002)
	if err != nil {
		t.Fatalf("BuildCardEvent forged: %v", err)
	}
	forged.PubKey = ownerHex
	forged.ID = forged.ComputeID()
	if err := forged.Verify(); err == nil {
		t.Fatal("forged event unexpectedly verifies — test fixture is not actually forged")
	}

	snapshot := []*nostr.Event{legit, foreign, forged}

	dir := filepath.Join(base, "board")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir board dir: %v", err)
	}

	// relays=nil: followFetch's board-scoped re-fetch and the best-effort
	// PublishBoard backfill both no-op on an empty relay set (no network, no
	// extra events reach the log) — see followFetch's empty-loop return and the
	// offline-Publisher pattern used elsewhere in this package.
	if err := importFollowedBoard(context.Background(), dir, coord, ownerHex, boardD, snapshot, nil); err != nil {
		t.Fatalf("importFollowedBoard: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	ids := map[string]bool{}
	for _, e := range events {
		ids[e.ID] = true
	}
	if !ids[legit.ID] {
		t.Error("legit owner-signed card was NOT admitted — admit loop is over-dropping")
	}
	if ids[foreign.ID] {
		t.Error("FOREIGN-key event was admitted into the bound board log — a hostile relay's untrusted-signer events must be dropped fail-closed")
	}
	if ids[forged.ID] {
		t.Error("FORGED event was admitted into the bound board log — a hostile relay's tampered-signature events must be dropped fail-closed")
	}
	if len(events) != 1 {
		t.Errorf("bound board log holds %d events, want exactly 1 (only the legit card): %+v", len(events), ids)
	}
}

// TestResolveFollowTarget_HostileAliasNeverResolvesEmail is the ready-50c email-case
// fail-closed pin (~line 263-272): a hostile relay serves a person-alias event
// mapping the owner's email to an ATTACKER pubkey, signed by the ATTACKER, not by
// self. Because identity.Resolve's trust root is self ALONE (v1 single-operator
// trust — package doc, pkg/identity/alias.go), an alias signed by any other key
// contributes nothing to the party graph, so resolveFollowTarget must refuse to
// resolve the email at all rather than silently binding to the attacker's boards
// under the owner's own email handle.
func TestResolveFollowTarget_HostileAliasNeverResolvesEmail(t *testing.T) {
	self, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey self: %v", err)
	}
	attacker, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey attacker: %v", err)
	}
	const email = "baron@3dl.dev"

	hostile, err := identity.BuildAliasEvent(attacker, identity.AliasSpec{
		Handle:  email,
		Pubkeys: []string{attacker.PubKeyHex()},
		Emails:  []string{email},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent hostile: %v", err)
	}
	if err := hostile.Verify(); err != nil {
		t.Fatalf("hostile alias must verify (it IS validly signed by the attacker): %v", err)
	}

	keys, _, err := resolveFollowTarget(email, email, []*nostr.Event{hostile}, self.PubKeyHex())
	if err == nil {
		t.Fatalf("resolveFollowTarget resolved %q to %v via a HOSTILE relay-served alias signed by an untrusted attacker key — trust root must be self only", email, keys)
	}
	if len(keys) != 0 {
		t.Errorf("resolveFollowTarget returned owner keys %v on the error path; want none", keys)
	}

	// Control: the SAME email, with a LEGIT alias signed by self ALSO present (as
	// well as the hostile one), resolves to self's key only — proving the rejection
	// above is genuinely about trust (attacker excluded), not a broken email lookup.
	legit, err := identity.BuildAliasEvent(self, identity.AliasSpec{
		Handle:  email,
		Pubkeys: []string{self.PubKeyHex()},
		Emails:  []string{email},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent legit: %v", err)
	}
	keys, _, err = resolveFollowTarget(email, email, []*nostr.Event{hostile, legit}, self.PubKeyHex())
	if err != nil {
		t.Fatalf("resolveFollowTarget with a legit self-signed alias present: %v", err)
	}
	if len(keys) != 1 || keys[0] != self.PubKeyHex() {
		t.Errorf("resolveFollowTarget = %v, want [%s] (self only, attacker excluded)", keys, self.PubKeyHex())
	}
}

// TestResolveFollowTarget_TokenAndHex covers the non-email identity forms: an rd1_
// token resolves to its board owner, and a bare 64-hex pubkey is taken verbatim.
func TestResolveFollowTarget_TokenAndHex(t *testing.T) {
	owner := randPubkey(t)
	self := randPubkey(t)

	// Bare hex.
	keys, _, err := resolveFollowTarget(owner, "e@x", nil, self)
	if err != nil || len(keys) != 1 || keys[0] != owner {
		t.Fatalf("hex resolve = %v, %v, want [%s]", keys, err, owner)
	}

	// rd1_ token: owner comes from the board coordinate, relays ride along.
	coord := rdSync.BoardCoord(owner, "proj1")
	tok, err := buildNostrClaimToken(coord, []string{"wss://r.example"}, "nonce123", 1, 1<<62, owner)
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	keys, relays, err := resolveFollowTarget(tok, "e@x", nil, self)
	if err != nil || len(keys) != 1 || keys[0] != owner {
		t.Fatalf("token resolve = %v, %v, want [%s]", keys, err, owner)
	}
	if len(relays) != 1 || relays[0] != "wss://r.example" {
		t.Errorf("token relays = %v, want [wss://r.example]", relays)
	}
}

// aliasesBySigner returns every self-signed (PubKey==signer) kind-39302 alias in
// dir's bound log — the write-side of `rd follow`'s person-alias refresh.
func aliasesBySigner(t *testing.T, dir, signer string) []*nostr.Event {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll %s: %v", dir, err)
	}
	var out []*nostr.Event
	for _, e := range events {
		if e.Kind == identity.KindPersonAlias && e.PubKey == signer {
			out = append(out, e)
		}
	}
	return out
}

func aliasEmails(e *nostr.Event) []string {
	var out []string
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == "email" {
			out = append(out, t[1])
		}
	}
	return out
}

// seedFollowerAndOwner mints a fresh follower key in a scratch RD_HOME and seeds a
// SEPARATE owner key that published one board plus an owner-signed alias binding
// baron@3dl.dev to the OWNER key. It injects the seeded snapshot as followFetch and
// returns (self follower pubkey, owner pubkey, projects root). This is the cold
// non-owner setup: the follower has never run `rd identify`, and the only
// baron@3dl.dev alias in the snapshot is signed by a key the follower does NOT
// trust.
func seedFollowerAndOwner(t *testing.T) (self, owner, root string) {
	t.Helper()
	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	fk, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey (follower): %v", err)
	}
	self = fk.PubKeyHex()

	ownerKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey owner: %v", err)
	}
	owner = ownerKey.PubKeyHex()

	var seeded []*nostr.Event
	// Owner-signed alias binding baron@3dl.dev -> OWNER key. The follower must never
	// reuse this handle: it is signed by a key outside the follower's trust closure.
	ownerAlias, err := identity.BuildAliasEvent(ownerKey, identity.AliasSpec{
		Handle:  "baron@3dl.dev",
		Pubkeys: []string{owner},
		Emails:  []string{"baron@3dl.dev"},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent owner: %v", err)
	}
	be, err := rdSync.BuildBoardEvent(ownerKey, rdSync.BoardSpec{BoardD: "proj1", Title: "proj1", Maintainers: []string{owner}}, 1000)
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	seeded = append(seeded, ownerAlias, be)

	origFetch := followFetch
	followFetch = func(_ context.Context, _ []string, _ map[string]any) ([]*nostr.Event, error) {
		return seeded, nil
	}
	t.Cleanup(func() { followFetch = origFetch })

	root = filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}
	return self, owner, root
}

// TestFollow_NoEmailNeverClaimsOwnerHandle is the ready-57d SECURITY done
// condition. A FRESH (never-identified) key running `rd follow <owner-pubkey>` with
// NO --email must publish a KEY-ONLY person-alias: it binds THIS machine's key to
// NO email at all, and specifically never to baron@3dl.dev. Before the fix, follow
// defaulted --email to a hardcoded owner handle baked into the binary, so any
// non-owner auto-claimed baron@3dl.dev and polluted the email->key trust map.
func TestFollow_NoEmailNeverClaimsOwnerHandle(t *testing.T) {
	self, owner, root := seedFollowerAndOwner(t)

	rep, err := runFollow(followOpts{
		who:    owner, // 64-hex owner pubkey
		email:  "",    // the vulnerable path: no --email given
		root:   root,
		relays: []string{"wss://seed.example.test"},
	})
	if err != nil {
		t.Fatalf("runFollow: %v", err)
	}
	if rep.MintedKey {
		// follower key was minted at seedFollowerAndOwner via nostrKey(); follow keeps it.
		t.Error("rd follow re-minted the follower key; it must keep the existing key")
	}

	dir := rep.BoardDirs["proj1"]
	if dir == "" {
		t.Fatalf("proj1 not bound: %+v", rep.BoardDirs)
	}
	selfAliases := aliasesBySigner(t, dir, self)
	if len(selfAliases) != 1 {
		t.Fatalf("follower published %d self-aliases, want exactly 1", len(selfAliases))
	}
	emails := aliasEmails(selfAliases[0])
	if len(emails) != 0 {
		t.Errorf("KEY-ONLY alias must claim NO email, got %v — a cold non-owner must never invent an email handle", emails)
	}
	for _, e := range emails {
		if e == "baron@3dl.dev" {
			t.Errorf("SECURITY: follower's alias claims the owner handle baron@3dl.dev without --email")
		}
	}
	// The follower key must NOT resolve to the owner's baron@3dl.dev party.
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	r := identity.Resolve(events, []string{self})
	if keys, ok := r.KeysForParty("baron@3dl.dev"); ok {
		for _, k := range keys {
			if k == self {
				t.Errorf("SECURITY: follower key %s resolved into the baron@3dl.dev party", self)
			}
		}
	}
}

// TestFollow_NoEmailPreservesSiblingKeys is the ready-104 SECURITY/correctness
// done condition. Setup: the follower already co-asserted a SECOND machine key
// via `rd identify --add-key <sibling>` — a self-signed (signer=self, d=me@x)
// person-alias whose p-tags are BOTH self AND sibling. Running a no-`--email`
// `rd follow <owner>` reuses the me@x handle (ready-57d reuse policy) and
// republishes that same (self, me@x) addressable slot. Before the ready-104
// fix, that republish asserted ONLY self, so latest-wins supersede (ready-998)
// EVICTED sibling from the party — silently dropping that machine from
// KeysForParty / PartyForPubkey and from `rd grant --all-boards` discovery. The
// fix republishes the UNION (existing asserted keys + self), so sibling STILL
// resolves after the follow. This test FAILS on the narrowing behavior (sibling
// evicted) and passes once the union is preserved.
func TestFollow_NoEmailPreservesSiblingKeys(t *testing.T) {
	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	fk, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey (follower): %v", err)
	}
	self := fk.PubKeyHex()

	// A SECOND machine key the operator co-asserted via `rd identify --add-key`.
	// It has no key material here — it only ever appears as a p-tag the follower's
	// own self-signed alias asserts.
	siblingKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey sibling: %v", err)
	}
	sibling := siblingKey.PubKeyHex()

	ownerKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey owner: %v", err)
	}
	owner := ownerKey.PubKeyHex()

	const mine = "me@example.com"
	// Prior `rd identify --add-key <sibling>`: ONE self-signed alias for the
	// (self, me@x) slot that asserts BOTH self and sibling.
	priorAlias, err := identity.BuildAliasEvent(fk, identity.AliasSpec{
		Handle:  mine,
		Pubkeys: []string{self, sibling},
		Emails:  []string{mine},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent prior: %v", err)
	}
	be, err := rdSync.BuildBoardEvent(ownerKey, rdSync.BoardSpec{BoardD: "proj1", Title: "proj1", Maintainers: []string{owner}}, 1000)
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	seeded := []*nostr.Event{priorAlias, be}

	// Sanity: BEFORE follow, the seeded snapshot already resolves both keys into
	// the me@x party — so any post-follow eviction is caused by follow, not setup.
	if keys, ok := identity.Resolve(seeded, []string{self}).KeysForParty(mine); !ok || !containsKey(keys, sibling) {
		t.Fatalf("fixture invalid: sibling %s not in me@x party before follow (keys=%v, ok=%v)", sibling, keys, ok)
	}

	origFetch := followFetch
	followFetch = func(_ context.Context, _ []string, _ map[string]any) ([]*nostr.Event, error) {
		return seeded, nil
	}
	t.Cleanup(func() { followFetch = origFetch })

	root := filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	rep, err := runFollow(followOpts{
		who:    owner,
		email:  "", // no --email: reuses me@x, MUST preserve the sibling key
		root:   root,
		relays: []string{"wss://seed.example.test"},
	})
	if err != nil {
		t.Fatalf("runFollow: %v", err)
	}

	dir := rep.BoardDirs["proj1"]
	if dir == "" {
		t.Fatalf("proj1 not bound: %+v", rep.BoardDirs)
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	r := identity.Resolve(events, []string{self})

	// CORE ASSERTION: sibling must STILL be in the me@x party after follow — the
	// no-email republish must not narrow (self, me@x) down to self alone.
	keys, ok := r.KeysForParty(mine)
	if !ok {
		t.Fatalf("me@x resolved to no party after follow — the reuse republish broke resolution")
	}
	if !containsKey(keys, sibling) {
		t.Errorf("SECURITY/correctness (ready-104): sibling key %s was EVICTED from the me@x party by a no-email follow; want it preserved. keys=%v", sibling, keys)
	}
	if !containsKey(keys, self) {
		t.Errorf("self key %s missing from me@x party after follow; keys=%v", self, keys)
	}

	// And PartyForPubkey must still map the sibling machine back to this operator
	// (what `rd grant --all-boards` discovery relies on).
	if party, ok := r.PartyForPubkey(sibling); !ok || !containsKey(party.Pubkeys, self) {
		t.Errorf("PartyForPubkey(sibling) lost the operator binding after follow: party=%+v ok=%v", party, ok)
	}
}

func containsKey(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestFollow_ExplicitEmailBindsThatEmail proves the legitimate path still works:
// `rd follow <owner> --email you@x` binds this machine's key to you@x (and only
// you@x).
func TestFollow_ExplicitEmailBindsThatEmail(t *testing.T) {
	self, owner, root := seedFollowerAndOwner(t)

	const mine = "you@example.com"
	rep, err := runFollow(followOpts{
		who:    owner,
		email:  mine,
		root:   root,
		relays: []string{"wss://seed.example.test"},
	})
	if err != nil {
		t.Fatalf("runFollow: %v", err)
	}
	dir := rep.BoardDirs["proj1"]
	selfAliases := aliasesBySigner(t, dir, self)
	if len(selfAliases) != 1 {
		t.Fatalf("follower published %d self-aliases, want exactly 1", len(selfAliases))
	}
	emails := aliasEmails(selfAliases[0])
	if len(emails) != 1 || emails[0] != mine {
		t.Errorf("alias emails = %v, want [%s]", emails, mine)
	}
	events, _ := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	r := identity.Resolve(events, []string{self})
	keys, ok := r.KeysForParty(mine)
	if !ok {
		t.Fatalf("email %s did not resolve to any party", mine)
	}
	found := false
	for _, k := range keys {
		if k == self {
			found = true
		}
	}
	if !found {
		t.Errorf("follower key %s not in party for %s (keys=%v)", self, mine, keys)
	}
}

// TestFollow_NoEmailReusesPriorIdentify proves the reuse policy: a follower that
// already ran `rd identify --add-email me@x` (a self-signed alias present in the
// snapshot) and then runs `rd follow <owner>` with NO --email REUSES me@x rather
// than going key-only or inventing a handle.
func TestFollow_NoEmailReusesPriorIdentify(t *testing.T) {
	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	fk, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey (follower): %v", err)
	}
	self := fk.PubKeyHex()

	ownerKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey owner: %v", err)
	}
	owner := ownerKey.PubKeyHex()

	const mine = "me@example.com"
	// Prior `rd identify`: a self-signed alias binding the follower key to me@x.
	priorAlias, err := identity.BuildAliasEvent(fk, identity.AliasSpec{
		Handle:  mine,
		Pubkeys: []string{self},
		Emails:  []string{mine},
	}, 1000)
	if err != nil {
		t.Fatalf("BuildAliasEvent prior: %v", err)
	}
	be, err := rdSync.BuildBoardEvent(ownerKey, rdSync.BoardSpec{BoardD: "proj1", Title: "proj1", Maintainers: []string{owner}}, 1000)
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	seeded := []*nostr.Event{priorAlias, be}
	origFetch := followFetch
	followFetch = func(_ context.Context, _ []string, _ map[string]any) ([]*nostr.Event, error) {
		return seeded, nil
	}
	t.Cleanup(func() { followFetch = origFetch })

	root := filepath.Join(base, "projects")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatalf("mkdir root: %v", err)
	}

	rep, err := runFollow(followOpts{
		who:    owner,
		email:  "", // no --email: must REUSE the prior identify handle
		root:   root,
		relays: []string{"wss://seed.example.test"},
	})
	if err != nil {
		t.Fatalf("runFollow: %v", err)
	}
	dir := rep.BoardDirs["proj1"]
	selfAliases := aliasesBySigner(t, dir, self)
	// There may be more than one (the prior alias imported + the refreshed one);
	// assert EVERY self-alias with an email carries me@x and none invents another.
	sawEmail := false
	for _, a := range selfAliases {
		for _, e := range aliasEmails(a) {
			sawEmail = true
			if e != mine {
				t.Errorf("follow published/kept an alias with email %q, want reuse of %q", e, mine)
			}
		}
	}
	if !sawEmail {
		t.Errorf("no self-alias carried an email; the prior identify handle %q was not reused", mine)
	}
	events, _ := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	r := identity.Resolve(events, []string{self})
	if _, ok := r.KeysForParty(mine); !ok {
		t.Errorf("email %s did not resolve after follow — reuse failed", mine)
	}
}
