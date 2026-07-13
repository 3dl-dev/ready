package main

// join_test.go — unit tests for the surviving rd join helpers.
//
// CUTOVER (ready-cb6 I7): campfire open-join (join-by-name/ID, role-grant
// polling, TOFU beacon-root pinning via the campfire client, and the campfire
// transport-dir resolver) was retired with the campfire backend. The only join
// path is the nostr rd1_ invite token (covered by nostr_invite_test.go). What
// remains here is the shared isHex helper.
//
// HIGH-4 (ready-cc5): the --reset-beacon-root flag, resetBeaconRoot, and the
// BeaconRoot/PinBeaconRoot config plumbing were removed entirely. They were the
// last code that touched CFH()ome/.cf on the join path — resetBeaconRoot called
// CFHome() and os.OpenFile(O_CREATE) on <CFHome>/rd.json.lock even when nothing was
// pinned, writing ~/.cf and violating the "no .cf on any path" invariant. The
// no-.cf join test below is the regression guard.

import (
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// TestIsHex verifies isHex correctly identifies hex strings.
func TestIsHex(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"0123456789abcdef", true},
		{"ABCDEF0123456789", true},
		{"0123456789abcdefABCDEF", true},
		{"", true}, // empty string — vacuously true (all chars are hex)
		{"xyz", false},
		{"0123456789abcdefg", false},
		{"ghijklmn", false},
	}
	for _, tc := range cases {
		got := isHex(tc.input)
		if got != tc.want {
			t.Errorf("isHex(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// TestJoin_NoDotCfNoLock is the HIGH-4 regression guard: a full invite redemption
// (the join core) must create NO campfire state (.cf/ or identity.json) and NO
// rd.json.lock ANYWHERE. Before the fix, `rd join`'s --reset-beacon-root path wrote
// <CFHome>/rd.json.lock via os.OpenFile(O_CREATE) even with nothing pinned; that
// flag and its plumbing are now gone. This walks the whole temp base after a
// successful join and fails on any .cf artifact or *.lock file.
func TestJoin_NoDotCfNoLock(t *testing.T) {
	base := t.TempDir()
	sharedLog := rdSync.NewNostrLog(filepath.Join(base, "relay-log.jsonl"))
	medium := &logInviteMedium{log: sharedLog}

	ownerKey, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("owner GenerateKey: %v", err)
	}
	owner := ownerKey.PubKeyHex()
	const boardD = "ready"
	board := rdSync.BoardCoord(owner, boardD)

	ownerPub := &rdSync.Publisher{Key: ownerKey, Log: sharedLog}
	boardSpec := rdSync.BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{owner}}
	now := time.Now().Unix()
	if _, err := ownerPub.PublishItem(nil, &boardSpec, rdSync.CardSpec{
		ItemID: "ready-001", Title: "first", Status: "active",
		Priority: "p1", Type: "task", BoardD: boardD, BoardAuthor: owner,
	}, now); err != nil {
		t.Fatalf("owner PublishItem: %v", err)
	}

	minted, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("minted GenerateKey: %v", err)
	}
	token, grant, err := buildNostrInviteToken(ownerKey, board, minted, []string{"ws://127.0.0.1:1"}, "nonce-nocf", now, now+7200, now+2)
	if err != nil {
		t.Fatalf("buildNostrInviteToken: %v", err)
	}
	if err := medium.Publish([]*nostr.Event{grant}); err != nil {
		t.Fatalf("publishing grant: %v", err)
	}

	p, err := decodeNostrInviteToken(token)
	if err != nil {
		t.Fatalf("decode token: %v", err)
	}
	joinHome := filepath.Join(base, "joiner-home")
	joinDir := filepath.Join(base, "joiner-project")
	if err := redeemNostrInviteToken(p, joinHome, joinDir, medium, false); err != nil {
		t.Fatalf("redeem (join): %v", err)
	}

	// Whole-tree walk: no .cf, no identity.json, no *.lock created by the join.
	_ = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() && d.Name() == ".cf" {
			t.Fatalf("join created a .cf directory at %s — the nostr-native join must never write .cf", path)
		}
		if !d.IsDir() {
			if d.Name() == "identity.json" {
				t.Fatalf("join created a campfire identity at %s", path)
			}
			if strings.HasSuffix(d.Name(), ".lock") {
				t.Fatalf("join created a lock file at %s — the removed beacon-root plumbing wrote <CFHome>/rd.json.lock", path)
			}
		}
		return nil
	})
}
