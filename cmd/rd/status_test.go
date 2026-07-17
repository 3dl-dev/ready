package main

// Integration coverage for `rd status` (ready-e31). Each case CONSTRUCTS a real
// broken (or healthy) state on disk — a real .ready/ project with real signed
// events, NO mock of the state store or the projection — and asserts that
// `rd status` classifies it and prints the SINGLE correct next command.
//
// The states, per the item's done condition:
//   - no board linked here            -> `rd follow <owner>`
//   - this key OWNS a board, unlinked -> warn + `rd follow`
//   - confidential board, no read key -> `ask owner: rd grant --all-boards <pubkey>`
//   - alias-less follower, empty queue -> `rd identify` (bind key to party)
//   - unbootstrapped confidential (owner) -> all good, NEVER the no-read-key remedy
//   - healthy (owner / granted member)    -> all good + item count

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// statusEnv pins RD_HOME into a fresh sandbox and restores cwd on cleanup — the
// minimal isolation `rd status` needs (a fresh follower key is minted under
// RD_HOME on first nostrKey()). Returns the sandbox base.
func statusEnv(t *testing.T) string {
	t.Helper()
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	base := t.TempDir()
	if err := os.MkdirAll(filepath.Join(base, "rdhome"), 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", filepath.Join(base, "rdhome"))
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")
	return base
}

// appendEvents appends signed events to dir's authoritative log.
func appendEvents(t *testing.T, dir string, evs ...*nostr.Event) {
	t.Helper()
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique(evs); err != nil {
		t.Fatalf("append events: %v", err)
	}
}

// buildBoardProject creates a pinned board project at base/<boardD> authored by
// ownerKey, confidential unless public. It writes .ready/config.json (the pin)
// and appends the signed 30301 board event. Returns (dir, coord). It does NOT
// chdir — the caller chdirs after seeding so nostrKey mints the right identity.
func buildBoardProject(t *testing.T, base string, ownerKey *nostr.Key, boardD string, public bool) (string, string) {
	t.Helper()
	dir := filepath.Join(base, boardD)
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o700); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	coord := rdSync.BoardCoord(ownerKey.PubKeyHex(), boardD)
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: boardD, Board: coord, Public: public}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	be, err := rdSync.BuildBoardEvent(ownerKey, rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{ownerKey.PubKeyHex()}}, 1000)
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	appendEvents(t, dir, be)
	return dir, coord
}

// bootstrapConfidential mints a board CEK+LTK and appends the owner CEK
// self-grant (the cutover that makes the board actually confidential). Returns
// the CEK/LTK so a member grant can wrap them.
func bootstrapConfidential(t *testing.T, dir string, ownerKey *nostr.Key, boardD string) (cek, ltk [32]byte) {
	t.Helper()
	var err error
	if cek, err = rdSync.MintKey(); err != nil {
		t.Fatalf("MintKey cek: %v", err)
	}
	if ltk, err = rdSync.MintKey(); err != nil {
		t.Fatalf("MintKey ltk: %v", err)
	}
	appendEvents(t, dir, grantEvent(t, ownerKey, boardD, ownerKey.PubKeyHex(), rdSync.RoleOwner, cek, ltk, 2000))
	return cek, ltk
}

// grantMember appends an owner-signed contributor grant wrapping (cek,ltk) to
// grantee — the read key the follower is missing until the owner runs `rd grant`.
func grantMember(t *testing.T, dir string, ownerKey *nostr.Key, boardD, grantee string, cek, ltk [32]byte) {
	t.Helper()
	appendEvents(t, dir, grantEvent(t, ownerKey, boardD, grantee, rdSync.RoleContributor, cek, ltk, 2001))
}

func grantEvent(t *testing.T, ownerKey *nostr.Key, boardD, grantee, role string, cek, ltk [32]byte, at int64) *nostr.Event {
	t.Helper()
	wCEK, err := rdSync.WrapKey(ownerKey, grantee, cek)
	if err != nil {
		t.Fatalf("WrapKey cek: %v", err)
	}
	wLTK, err := rdSync.WrapKey(ownerKey, grantee, ltk)
	if err != nil {
		t.Fatalf("WrapKey ltk: %v", err)
	}
	ev, err := rdSync.BuildRoleGrantEvent(ownerKey, rdSync.RoleGrantSpec{
		BoardD: boardD, BoardAuthor: ownerKey.PubKeyHex(), Grantee: grantee, Role: role,
		WrappedCEK: wCEK, CEKEpoch: 1, WrappedLTK: wLTK,
	}, at)
	if err != nil {
		t.Fatalf("BuildRoleGrantEvent: %v", err)
	}
	return ev
}

// appendItem appends an owner-signed card+status pair for itemID, scoped --for
// forParty. env non-nil seals the free text (confidential board).
func appendItem(t *testing.T, dir string, ownerKey *nostr.Key, boardD, itemID, forParty string, env *rdSync.Envelope) {
	t.Helper()
	owner := ownerKey.PubKeyHex()
	coord := rdSync.BoardCoord(owner, boardD)
	card, err := rdSync.BuildCardEvent(ownerKey, rdSync.CardSpec{
		ItemID: itemID, Title: itemID, Status: state.StatusActive, Type: "task",
		BoardD: boardD, BoardAuthor: owner, For: forParty, Enc: env,
	}, 3000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	st, err := rdSync.BuildStatusEventWithIssueRoot(ownerKey, itemID, state.StatusActive, card.ID, "", coord, "", 3000, env)
	if err != nil {
		t.Fatalf("BuildStatusEvent: %v", err)
	}
	appendEvents(t, dir, card, st)
}

// TestStatus_RemediesPerState is the ready-e31 done condition: for every
// constructed state, `rd status` names the correct one-line next command (or
// "all good" when healthy) and never emits a raw hex identifier without --debug.
func TestStatus_RemediesPerState(t *testing.T) {
	cases := []struct {
		name       string
		setup      func(t *testing.T) // seeds state + chdirs into the project dir
		wantState  statusState
		wantSubstr []string
		notSubstr  []string
	}{
		{
			name: "no board linked here",
			setup: func(t *testing.T) {
				base := statusEnv(t)
				dir := filepath.Join(base, "empty")
				if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				chdir(t, dir)
			},
			wantState:  statusNoBoard,
			wantSubstr: []string{"rd follow"},
			notSubstr:  []string{"all good", "rd grant --all-boards"},
		},
		{
			name: "this key owns a board but the dir is unlinked",
			setup: func(t *testing.T) {
				base := statusEnv(t)
				me, err := nostrKey() // mint MY key first
				if err != nil {
					t.Fatalf("nostrKey: %v", err)
				}
				// A dir with .ready but NO pin, whose log carries a board I authored.
				dir := filepath.Join(base, "unlinked")
				if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o700); err != nil {
					t.Fatalf("mkdir: %v", err)
				}
				be, err := rdSync.BuildBoardEvent(me, rdSync.BoardSpec{BoardD: "mine", Title: "mine", Maintainers: []string{me.PubKeyHex()}}, 1000)
				if err != nil {
					t.Fatalf("BuildBoardEvent: %v", err)
				}
				appendEvents(t, dir, be)
				chdir(t, dir)
			},
			wantState:  statusOwnsUnlinked,
			wantSubstr: []string{"rd follow", "own"},
			notSubstr:  []string{"all good"},
		},
		{
			name: "confidential board, no read key (no grant)",
			setup: func(t *testing.T) {
				base := statusEnv(t)
				me, err := nostrKey()
				if err != nil {
					t.Fatalf("nostrKey: %v", err)
				}
				owner, err := nostr.GenerateKey()
				if err != nil {
					t.Fatalf("owner key: %v", err)
				}
				dir, _ := buildBoardProject(t, base, owner, "secret", false /* confidential */)
				cek, ltk := bootstrapConfidential(t, dir, owner, "secret")
				// Owner has an item, sealed — the follower holds NO grant, so cannot read.
				appendItem(t, dir, owner, "secret", "secret-1", owner.PubKeyHex(),
					&rdSync.Envelope{CEK: cek, Epoch: 1, LTK: &ltk})
				_ = me
				chdir(t, dir)
			},
			wantState:  statusNoReadKey,
			wantSubstr: []string{"rd grant --all-boards", "self-heal"},
			notSubstr:  []string{"all good"},
		},
		{
			name: "alias-less follower, empty personal queue",
			setup: func(t *testing.T) {
				base := statusEnv(t)
				me, err := nostrKey()
				if err != nil {
					t.Fatalf("nostrKey: %v", err)
				}
				owner, err := nostr.GenerateKey()
				if err != nil {
					t.Fatalf("owner key: %v", err)
				}
				dir, _ := buildBoardProject(t, base, owner, "team", true /* public */)
				// Items are scoped to an EMAIL the follower's key has no alias for, so
				// the personal queue is empty even though the board has work.
				appendItem(t, dir, owner, "team", "team-1", "someone@else.dev", nil)
				appendItem(t, dir, owner, "team", "team-2", "someone@else.dev", nil)
				_ = me
				chdir(t, dir)
			},
			wantState:  statusNoAlias,
			wantSubstr: []string{"rd identify"},
			notSubstr:  []string{"rd grant --all-boards"},
		},
		{
			name: "unbootstrapped confidential board (owner) is healthy, not no-read-key",
			setup: func(t *testing.T) {
				base := statusEnv(t)
				me, err := nostrKey()
				if err != nil {
					t.Fatalf("nostrKey: %v", err)
				}
				// I OWN this confidential board; it has no CEK yet (no first write).
				dir, _ := buildBoardProject(t, base, me, "fresh", false /* confidential */)
				chdir(t, dir)
			},
			wantState:  statusHealthy,
			wantSubstr: []string{"all good"},
			notSubstr:  []string{"rd grant --all-boards", "no read key"},
		},
		{
			name: "healthy owner with items",
			setup: func(t *testing.T) {
				base := statusEnv(t)
				me, err := nostrKey()
				if err != nil {
					t.Fatalf("nostrKey: %v", err)
				}
				dir, _ := buildBoardProject(t, base, me, "work", true /* public */)
				appendItem(t, dir, me, "work", "work-1", me.PubKeyHex(), nil)
				appendItem(t, dir, me, "work", "work-2", me.PubKeyHex(), nil)
				chdir(t, dir)
			},
			wantState:  statusHealthy,
			wantSubstr: []string{"all good", "2 items"},
		},
		{
			name: "healthy granted member on a confidential board reads the sealed items",
			setup: func(t *testing.T) {
				base := statusEnv(t)
				me, err := nostrKey()
				if err != nil {
					t.Fatalf("nostrKey: %v", err)
				}
				owner, err := nostr.GenerateKey()
				if err != nil {
					t.Fatalf("owner key: %v", err)
				}
				dir, _ := buildBoardProject(t, base, owner, "shared", false /* confidential */)
				cek, ltk := bootstrapConfidential(t, dir, owner, "shared")
				grantMember(t, dir, owner, "shared", me.PubKeyHex(), cek, ltk)
				env := &rdSync.Envelope{CEK: cek, Epoch: 1, LTK: &ltk}
				appendItem(t, dir, owner, "shared", "shared-1", me.PubKeyHex(), env)
				chdir(t, dir)
			},
			wantState:  statusHealthy,
			wantSubstr: []string{"all good", "1 item"},
			notSubstr:  []string{"rd grant --all-boards"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.setup(t)

			rep, err := computeStatus()
			if err != nil {
				t.Fatalf("computeStatus: %v", err)
			}
			if rep.State != tc.wantState {
				t.Errorf("state = %v, want %v", rep.State, tc.wantState)
			}

			out := captureStdoutPipe(t, func() { printStatusReport(rep, false) })

			// Bounded to ~12 plain lines.
			if n := strings.Count(strings.TrimSpace(out), "\n") + 1; n > 12 {
				t.Errorf("status printed %d lines, want <= 12:\n%s", n, out)
			}
			for _, s := range tc.wantSubstr {
				if !strings.Contains(out, s) {
					t.Errorf("output missing %q:\n%s", s, out)
				}
			}
			for _, s := range tc.notSubstr {
				if strings.Contains(out, s) {
					t.Errorf("output should NOT contain %q:\n%s", s, out)
				}
			}
			// No hex identifier without --debug, EXCEPT the pubkey the owner must copy
			// into the `rd grant --all-boards <pubkey>` remedy (load-bearing).
			if rep.State != statusNoReadKey && rep.State != statusOwnsUnlinked {
				if strings.Contains(out, "30301:") || strings.Contains(out, rep.Pubkey) {
					t.Errorf("output leaked a raw hex identifier without --debug:\n%s", out)
				}
			}
		})
	}
}

// TestStatus_DebugShowsHex proves --debug surfaces the pubkey + board coordinate
// that the default view withholds.
func TestStatus_DebugShowsHex(t *testing.T) {
	base := statusEnv(t)
	me, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	dir, coord := buildBoardProject(t, base, me, "dbg", true)
	chdir(t, dir)

	rep, err := computeStatus()
	if err != nil {
		t.Fatalf("computeStatus: %v", err)
	}
	plain := captureStdoutPipe(t, func() { printStatusReport(rep, false) })
	if strings.Contains(plain, me.PubKeyHex()) || strings.Contains(plain, coord) {
		t.Errorf("plain status leaked hex:\n%s", plain)
	}
	debug := captureStdoutPipe(t, func() { printStatusReport(rep, true) })
	if !strings.Contains(debug, me.PubKeyHex()) {
		t.Errorf("--debug status did not show the pubkey:\n%s", debug)
	}
	if !strings.Contains(debug, coord) {
		t.Errorf("--debug status did not show the board coordinate:\n%s", debug)
	}
}

// TestStatus_PartyAlias_MyCountFoldsSiblingPubkey is ready-54e's regression lock
// for duplication (3): computeStatus's party-membership fold must route through
// the SAME shared helper `rd ready`/`rd list` use (cmd/rd/party.go
// addPartyIdentities, ready-99d) rather than a private reimplementation. An item
// scoped to a SIBLING machine's pubkey — not this machine's own key — only counts
// toward MyCount once both keys are aliased into one party; this proves
// computeStatus performs that same fold, not a narrower local one.
func TestStatus_PartyAlias_MyCountFoldsSiblingPubkey(t *testing.T) {
	base := statusEnv(t)
	_, err := nostrKey() // mint THIS machine's key first
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("owner key: %v", err)
	}
	sibling, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("sibling key: %v", err)
	}
	dir, _ := buildBoardProject(t, base, owner, "team", true /* public */)
	appendSelfSignedAlias(t, dir, sibling.PubKeyHex(), []string{"baron@3dl.dev"})
	// Item is scoped to the SIBLING pubkey, never this machine's own key.
	appendItem(t, dir, owner, "team", "team-1", sibling.PubKeyHex(), nil)
	chdir(t, dir)

	rep, err := computeStatus()
	if err != nil {
		t.Fatalf("computeStatus: %v", err)
	}
	if rep.State != statusHealthy {
		t.Fatalf("state = %v, want statusHealthy", rep.State)
	}
	if rep.MyCount != 1 {
		t.Errorf("MyCount = %d, want 1 (sibling pubkey's item folded via shared party helper)", rep.MyCount)
	}
}

func chdir(t *testing.T, dir string) {
	t.Helper()
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir %s: %v", dir, err)
	}
}
