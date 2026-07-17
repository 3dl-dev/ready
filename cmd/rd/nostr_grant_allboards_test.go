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
