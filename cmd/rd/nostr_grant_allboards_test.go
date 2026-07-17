package main

// nostr_grant_allboards_test.go — `rd grant --all-boards <pubkey>` fans the SAME
// owner-signed kind-39301 role-grant across EVERY board this machine owns/has pinned
// locally (ready-58f). Onboarding a key to N repos used to mean N separate grants.
//
// The done condition (ready-58f): two scratch repos, each with a pinned board owned
// by the SAME key; one `rd grant --all-boards` produces a valid signed grant on BOTH
// boards. Verification RESOLVES the grant (DeriveLevels over each board's real signed
// log) — nothing about the thing under test is mocked. Every event is schnorr-signed
// via the wire builders and re-verified inside the derivation.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// setupOwnedBoardsUnderRoot builds a projects root whose immediate subdirectories are
// each a nostr-native rd project pinned to a board OWNED by one shared owner key
// (loaded from an isolated RD_HOME). Returns (projectsRoot, ownerPubkeyHex, dirByName).
// This is the multi-repo, single-owner topology `rd grant --all-boards` fans across.
func setupOwnedBoardsUnderRoot(t *testing.T, boardNames ...string) (string, string, map[string]string) {
	t.Helper()

	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", unreachableRelayURL)
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	projectsRoot := filepath.Join(base, "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		t.Fatalf("mkdir projects root: %v", err)
	}

	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()

	dirByName := map[string]string{}
	for _, name := range boardNames {
		dir := filepath.Join(projectsRoot, name)
		if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o700); err != nil {
			t.Fatalf("mkdir %s/.ready: %v", name, err)
		}
		coord := rdSync.BoardCoord(owner, name)
		if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: name, Board: coord, Public: true}); err != nil {
			t.Fatalf("SaveSyncConfig %s: %v", name, err)
		}
		board := rdSync.BoardSpec{BoardD: name, Title: name, Maintainers: []string{owner}}
		be, err := rdSync.BuildBoardEvent(k, board, time.Now().Unix())
		if err != nil {
			t.Fatalf("BuildBoardEvent %s: %v", name, err)
		}
		if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{be}); err != nil {
			t.Fatalf("append board event %s: %v", name, err)
		}
		dirByName[name] = dir
	}
	return projectsRoot, owner, dirByName
}

// TestGrantAllBoards_GrantsSignedGrantOnEveryOwnedBoard is the ready-58f done
// condition: one `rd grant --all-boards <pk>` invocation lands a valid, signed,
// RESOLVABLE contributor grant on BOTH locally-owned boards.
func TestGrantAllBoards_GrantsSignedGrantOnEveryOwnedBoard(t *testing.T) {
	projectsRoot, owner, dirByName := setupOwnedBoardsUnderRoot(t, "repoalpha", "repobeta")

	// Typical invocation site: inside one of the repos.
	if err := os.Chdir(dirByName["repoalpha"]); err != nil {
		t.Fatalf("chdir repoalpha: %v", err)
	}

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}
	grantee := gk.PubKeyHex()

	// Exercise the real `rd grant --all-boards --projects-root <root>` command wiring.
	cmd := grantCmd
	if err := cmd.Flags().Set("all-boards", "true"); err != nil {
		t.Fatalf("set all-boards flag: %v", err)
	}
	if err := cmd.Flags().Set("projects-root", projectsRoot); err != nil {
		t.Fatalf("set projects-root flag: %v", err)
	}
	if err := cmd.Flags().Set("label", "onboard-agent"); err != nil {
		t.Fatalf("set label flag: %v", err)
	}
	t.Cleanup(func() {
		_ = cmd.Flags().Set("all-boards", "false")
		_ = cmd.Flags().Set("projects-root", "")
		_ = cmd.Flags().Set("label", "")
	})

	if err := cmd.RunE(cmd, []string{grantee, rdSync.RoleContributor}); err != nil {
		t.Fatalf("rd grant --all-boards: %v", err)
	}

	// Resolve the grant on EACH board — no mocks: read the board's own signed log,
	// verify the schnorr signature, and derive the operator level.
	for name, dir := range dirByName {
		grants := readGrantEventsForTest(t, dir, grantee)
		if len(grants) != 1 {
			t.Fatalf("board %s: expected exactly 1 signed kind-39301 grant for grantee, got %d", name, len(grants))
		}
		if err := grants[0].Verify(); err != nil {
			t.Fatalf("board %s: published grant does not verify: %v", name, err)
		}
		if role, _ := tagVal(grants[0].Tags, "role"); role != rdSync.RoleContributor {
			t.Fatalf("board %s: grant role = %q, want contributor", name, role)
		}

		events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
		if err != nil {
			t.Fatalf("board %s: ReadAll: %v", name, err)
		}
		levels, _ := rdSync.DeriveLevels(events, owner, name)
		if lvl, ok := levels[grantee]; !ok || lvl != rdSync.LevelContributor {
			t.Fatalf("board %s: resolved grantee level = (%d, present=%v), want contributor (%d)",
				name, levels[grantee], ok, rdSync.LevelContributor)
		}
	}

	// The whole flow stays on the nostr-native path — no campfire .cf provisioned.
	assertNoDotCf(t)
}

// TestDiscoverLocalOwnedBoards_FiltersToOwnedPinnedOnly proves the enumeration that
// backs --all-boards: it returns exactly the immediate-subdir projects whose pinned
// board is OWNED by the signer, skipping unpinned dirs and boards owned by a foreign
// key (which the escalation cap would reject anyway).
func TestDiscoverLocalOwnedBoards_FiltersToOwnedPinnedOnly(t *testing.T) {
	projectsRoot, owner, dirByName := setupOwnedBoardsUnderRoot(t, "ownedone", "ownedtwo")

	// A dir with NO pinned board must be skipped.
	unpinned := filepath.Join(projectsRoot, "unpinned")
	if err := os.MkdirAll(filepath.Join(unpinned, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir unpinned: %v", err)
	}
	if err := rdconfig.SaveSyncConfig(unpinned, &rdconfig.SyncConfig{ProjectName: "unpinned"}); err != nil {
		t.Fatalf("SaveSyncConfig unpinned: %v", err)
	}

	// A dir pinned to a board owned by a FOREIGN key must be skipped (the signer is
	// not the owner, so it cannot mint a grant there).
	fk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey foreign: %v", err)
	}
	foreign := filepath.Join(projectsRoot, "foreign")
	if err := os.MkdirAll(filepath.Join(foreign, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir foreign: %v", err)
	}
	if err := rdconfig.SaveSyncConfig(foreign, &rdconfig.SyncConfig{ProjectName: "foreign", Board: rdSync.BoardCoord(fk.PubKeyHex(), "foreign")}); err != nil {
		t.Fatalf("SaveSyncConfig foreign: %v", err)
	}

	boards, err := discoverLocalOwnedBoards(projectsRoot, owner)
	if err != nil {
		t.Fatalf("discoverLocalOwnedBoards: %v", err)
	}
	got := map[string]bool{}
	for _, b := range boards {
		got[b.dir] = true
	}
	if len(boards) != 2 {
		t.Fatalf("discovered %d owned boards, want 2 (%v)", len(boards), boards)
	}
	for _, name := range []string{"ownedone", "ownedtwo"} {
		if !got[dirByName[name]] {
			t.Fatalf("owned board %s missing from discovery %v", name, boards)
		}
	}
	if got[unpinned] {
		t.Fatalf("unpinned dir must not be discovered as a board")
	}
	if got[foreign] {
		t.Fatalf("foreign-owned board must not be discovered (signer is not its owner)")
	}
}

// setupOwnerEnvUnderRoot builds the isolated owner-key env + an empty projects root,
// WITHOUT provisioning any boards, so a test can construct exactly the sibling topology
// it needs (a board.json-only clone, a broken board, an empty root). Returns
// (projectsRoot, ownerPubkeyHex, ownerKey).
func setupOwnerEnvUnderRoot(t *testing.T) (string, string, *nostr.Key) {
	t.Helper()
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", unreachableRelayURL)
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	projectsRoot := filepath.Join(base, "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		t.Fatalf("mkdir projects root: %v", err)
	}
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	return projectsRoot, k.PubKeyHex(), k
}

// TestGrantAllBoards_IncludesBoardJSONOnlySibling is the ready-8f3 done condition: a
// sibling repo that carries ONLY the committed .ready/board.json binding — no
// gitignored .ready/config.json, the exact state of a FRESH GIT CLONE before any
// command writes a config — MUST be included in the `rd grant --all-boards` fan-out and
// receive a valid, signed, RESOLVABLE grant. Before the fix discoverLocalOwnedBoards
// read only config.json, so this board was silently omitted (under-grant, no error).
func TestGrantAllBoards_IncludesBoardJSONOnlySibling(t *testing.T) {
	projectsRoot, owner, _ := setupOwnerEnvUnderRoot(t)

	// A board.json-ONLY sibling: committed binding present, config.json ABSENT.
	name := "clonedrepo"
	dir := filepath.Join(projectsRoot, name)
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir %s/.ready: %v", name, err)
	}
	coord := rdSync.BoardCoord(owner, name)
	if err := rdconfig.SaveBoardBinding(dir, &rdconfig.BoardBinding{Board: coord, ProjectName: name}); err != nil {
		t.Fatalf("SaveBoardBinding %s: %v", name, err)
	}
	// Assert the fresh-clone precondition: NO machine-local config.json exists yet.
	if _, err := os.Stat(rdconfig.SyncConfigPath(dir)); !os.IsNotExist(err) {
		t.Fatalf("precondition: %s should have NO config.json (fresh-clone case), stat err = %v",
			name, err)
	}

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}
	grantee := gk.PubKeyHex()

	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", name, err)
	}
	if err := runGrantAllBoards(projectsRoot, grantee, rdSync.RoleContributor, "onboard-clone", 0, ""); err != nil {
		t.Fatalf("runGrantAllBoards: %v", err)
	}

	// The board.json-only board must carry exactly one valid, resolvable grant.
	grants := readGrantEventsForTest(t, dir, grantee)
	if len(grants) != 1 {
		t.Fatalf("board.json-only board %s: expected exactly 1 signed grant, got %d", name, len(grants))
	}
	if err := grants[0].Verify(); err != nil {
		t.Fatalf("published grant does not verify: %v", err)
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	levels, _ := rdSync.DeriveLevels(events, owner, name)
	if lvl, ok := levels[grantee]; !ok || lvl != rdSync.LevelContributor {
		t.Fatalf("resolved grantee level = (%d, present=%v), want contributor (%d)",
			levels[grantee], ok, rdSync.LevelContributor)
	}
	assertNoDotCf(t)
}

// TestDiscoverLocalOwnedBoards_ResolvesBoardJSONOnly proves the enumeration itself
// includes a board.json-only sibling — the unit-level companion to the command-level
// done condition above.
func TestDiscoverLocalOwnedBoards_ResolvesBoardJSONOnly(t *testing.T) {
	projectsRoot, owner, _ := setupOwnerEnvUnderRoot(t)

	name := "boardjsononly"
	dir := filepath.Join(projectsRoot, name)
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir %s/.ready: %v", name, err)
	}
	coord := rdSync.BoardCoord(owner, name)
	if err := rdconfig.SaveBoardBinding(dir, &rdconfig.BoardBinding{Board: coord, ProjectName: name}); err != nil {
		t.Fatalf("SaveBoardBinding: %v", err)
	}

	boards, err := discoverLocalOwnedBoards(projectsRoot, owner)
	if err != nil {
		t.Fatalf("discoverLocalOwnedBoards: %v", err)
	}
	if len(boards) != 1 || boards[0].dir != dir || boards[0].coord != coord {
		t.Fatalf("board.json-only board not discovered: got %v, want [{%s %s}]", boards, dir, coord)
	}
}

// TestGrantAllBoards_AggregatesPartialFailure locks the partial-failure aggregation
// path: with N owned boards where ONE fails to publish, runGrantAllBoards grants the
// healthy boards durably, collects the failure, and returns an error that names the
// failed board's coordinate and the N-succeeded/N-total tally — it does NOT abort on
// the first failure. The failure is injected deterministically and root-independently
// by making the broken board's nostr log PATH a DIRECTORY: publishRoleGrant's
// ReadAll of that log (escalation-cap check) then returns a structural read error
// (EISDIR) for that board only. No behaviour of the grant itself is mocked.
func TestGrantAllBoards_AggregatesPartialFailure(t *testing.T) {
	projectsRoot, owner, k := setupOwnerEnvUnderRoot(t)

	// Healthy board: normal config pin + a real signed board event in its log.
	good := "goodboard"
	goodDir := filepath.Join(projectsRoot, good)
	if err := os.MkdirAll(filepath.Join(goodDir, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir good/.ready: %v", err)
	}
	goodCoord := rdSync.BoardCoord(owner, good)
	if err := rdconfig.SaveSyncConfig(goodDir, &rdconfig.SyncConfig{ProjectName: good, Board: goodCoord, Public: true}); err != nil {
		t.Fatalf("SaveSyncConfig good: %v", err)
	}
	be, err := rdSync.BuildBoardEvent(k, rdSync.BoardSpec{BoardD: good, Title: good, Maintainers: []string{owner}}, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildBoardEvent good: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(goodDir)).AppendUnique([]*nostr.Event{be}); err != nil {
		t.Fatalf("append good board event: %v", err)
	}

	// Broken board: valid pin (so discovery includes it) but its nostr-log path is a
	// DIRECTORY, so publishRoleGrant's ReadAll of the log fails structurally.
	broken := "brokenboard"
	brokenDir := filepath.Join(projectsRoot, broken)
	if err := os.MkdirAll(filepath.Join(brokenDir, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir broken/.ready: %v", err)
	}
	brokenCoord := rdSync.BoardCoord(owner, broken)
	if err := rdconfig.SaveSyncConfig(brokenDir, &rdconfig.SyncConfig{ProjectName: broken, Board: brokenCoord, Public: true}); err != nil {
		t.Fatalf("SaveSyncConfig broken: %v", err)
	}
	if err := os.MkdirAll(rdSync.NostrLogPath(brokenDir), 0o700); err != nil {
		t.Fatalf("make broken log a directory: %v", err)
	}

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}
	grantee := gk.PubKeyHex()

	if err := os.Chdir(goodDir); err != nil {
		t.Fatalf("chdir good: %v", err)
	}
	err = runGrantAllBoards(projectsRoot, grantee, rdSync.RoleContributor, "onboard", 0, "")
	if err == nil {
		t.Fatalf("expected an aggregated error when one board fails, got nil")
	}
	// The error must name the failed board and the tally — not swallow it.
	if !strings.Contains(err.Error(), "1 of 2") {
		t.Errorf("aggregated error missing the N-of-N tally: %v", err)
	}
	if !strings.Contains(err.Error(), brokenCoord) {
		t.Errorf("aggregated error does not name the failed board %s: %v", brokenCoord, err)
	}

	// The healthy board's grant is durable regardless of the sibling failure.
	grants := readGrantEventsForTest(t, goodDir, grantee)
	if len(grants) != 1 {
		t.Fatalf("healthy board: expected 1 durable grant despite sibling failure, got %d", len(grants))
	}
	if err := grants[0].Verify(); err != nil {
		t.Fatalf("healthy board grant does not verify: %v", err)
	}
	// The broken board's log stayed a directory (publish never reached the append) —
	// so it recorded no grant. Its log is unreadable as a file by construction, which
	// is exactly why its grant failed; assert the injected fault is still in place.
	if fi, serr := os.Stat(rdSync.NostrLogPath(brokenDir)); serr != nil || !fi.IsDir() {
		t.Fatalf("broken board log should still be a directory (no grant appended): fi=%v err=%v", fi, serr)
	}
}

// TestGrantAllBoards_NoLocalBoardsError locks the empty-discovery path: when no sibling
// under the projects root pins a board this key owns, runGrantAllBoards returns the
// actionable "no locally-pinned boards" error (pointing at `rd link`) rather than
// silently succeeding with a zero fan-out. An unpinned sibling and a foreign-owned
// sibling are both present to prove neither is mistaken for an owned board.
func TestGrantAllBoards_NoLocalBoardsError(t *testing.T) {
	projectsRoot, owner, _ := setupOwnerEnvUnderRoot(t)

	// An unpinned sibling (no board coordinate at all).
	unpinned := filepath.Join(projectsRoot, "unpinned")
	if err := os.MkdirAll(filepath.Join(unpinned, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir unpinned: %v", err)
	}
	if err := rdconfig.SaveSyncConfig(unpinned, &rdconfig.SyncConfig{ProjectName: "unpinned"}); err != nil {
		t.Fatalf("SaveSyncConfig unpinned: %v", err)
	}
	// A foreign-owned sibling (pinned, but not to this key's board).
	fk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey foreign: %v", err)
	}
	foreign := filepath.Join(projectsRoot, "foreign")
	if err := os.MkdirAll(filepath.Join(foreign, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir foreign: %v", err)
	}
	if err := rdconfig.SaveSyncConfig(foreign, &rdconfig.SyncConfig{ProjectName: "foreign", Board: rdSync.BoardCoord(fk.PubKeyHex(), "foreign")}); err != nil {
		t.Fatalf("SaveSyncConfig foreign: %v", err)
	}

	gk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}

	err = runGrantAllBoards(projectsRoot, gk.PubKeyHex(), rdSync.RoleContributor, "", 0, "")
	if err == nil {
		t.Fatalf("expected 'no locally-pinned boards' error, got nil")
	}
	if !strings.Contains(err.Error(), "no locally-pinned boards") {
		t.Errorf("error is not the empty-discovery message: %v", err)
	}
	// Sanity: the owner genuinely owns nothing discoverable here.
	if boards, derr := discoverLocalOwnedBoards(projectsRoot, owner); derr != nil || len(boards) != 0 {
		t.Fatalf("discovery should be empty: boards=%v err=%v", boards, derr)
	}
}
