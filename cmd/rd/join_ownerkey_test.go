package main

// ready-b32 — the owner-key overwrite guard (edge #1, the moot-board near-miss).
//
// `rd join` self-mints a fresh identity and, with --force, used to clobber the
// machine key with NO warning and NO backup. Two invariants are proven here with
// REAL keys and a REAL local event log (no mocks of the code under test):
//
//   (a) join --force on a box whose key OWNS a board ERRORS, and the key on disk is
//       UNCHANGED (self-minting would orphan the board's only owner key). The
//       explicit board-naming escape hatch (--force-replace-owner-key <board>) is
//       what actually authorizes the replace.
//   (b) when a key IS legitimately replaced, a copy exists under the backups dir —
//       a key is never lost silently. A temp backupDir is passed so the real
//       ~/.config is never touched.

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

// writeOwnerBoardKey plants a real key at DefaultKeyPath(home) and pins a board
// whose owner IS that key, so keyOwnedBoard() sees the machine key as a board owner.
// Returns the key's pubkey and the board coordinate it owns.
func writeOwnerBoardKey(t *testing.T, home, dir string) (pub, board string) {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := nostr.SaveKeyFile(nostr.DefaultKeyPath(home), k, home); err != nil {
		t.Fatalf("SaveKeyFile: %v", err)
	}
	pub = k.PubKeyHex()
	board = rdSync.BoardCoord(pub, "ready")
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o755); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	cfg.Board = board
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	return pub, board
}

// writeOwnerBoardKeyViaLogScan plants a real key at DefaultKeyPath(home) and
// records its ownership ONLY via an authored-but-UNPINNED 30301 board event in
// dir's local log — NO board is pinned in .ready/config.json. This exercises the
// KindBoard branch of keyOwnsBoardInDir's log-scan fallback (nostr_invite.go
// ~570-578), distinct from writeOwnerBoardKey's pinned-board fast path. Returns
// the key's pubkey and the board coordinate the log-scan fallback must recover.
func writeOwnerBoardKeyViaLogScan(t *testing.T, home, dir string) (pub, board string) {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := nostr.SaveKeyFile(nostr.DefaultKeyPath(home), k, home); err != nil {
		t.Fatalf("SaveKeyFile: %v", err)
	}
	pub = k.PubKeyHex()
	const boardD = "ready"
	board = rdSync.BoardCoord(pub, boardD)
	be, err := rdSync.BuildBoardEvent(k, rdSync.BoardSpec{BoardD: boardD, Title: "ready"}, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o755); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{be}); err != nil {
		t.Fatalf("append board event: %v", err)
	}
	// Deliberately NO cfg.Board pin — ownership must be discovered purely by the
	// log-scan fallback, not the pinned-board fast path.
	return pub, board
}

// writeOwnerRoleGrantKeyViaLogScan plants a real key at DefaultKeyPath(home) and
// records its ownership ONLY via a 39301 role-grant event the key AUTHORED in
// dir's local log — no board pin, no 30301 board event either. This exercises the
// KindRoleGrant branch of keyOwnsBoardInDir's log-scan fallback (nostr_invite.go
// ~579-585): only an owner/maintainer signs grants, and the grant's "a" tag names
// the board it binds into. Returns the key's pubkey and that board coordinate.
func writeOwnerRoleGrantKeyViaLogScan(t *testing.T, home, dir string) (pub, board string) {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := nostr.SaveKeyFile(nostr.DefaultKeyPath(home), k, home); err != nil {
		t.Fatalf("SaveKeyFile: %v", err)
	}
	pub = k.PubKeyHex()
	const boardD = "ready"
	board = rdSync.BoardCoord(pub, boardD)
	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey grantee: %v", err)
	}
	grant, err := rdSync.BuildRoleGrantEvent(k, rdSync.RoleGrantSpec{
		BoardD:      boardD,
		BoardAuthor: pub,
		Grantee:     grantee.PubKeyHex(),
		Role:        rdSync.RoleMaintainer,
	}, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildRoleGrantEvent: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o755); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{grant}); err != nil {
		t.Fatalf("append role-grant event: %v", err)
	}
	// Deliberately NO cfg.Board pin and NO 30301 board event — ownership must be
	// discovered purely from the authored 39301 grant.
	return pub, board
}

// TestJoin_ForceRefusesOwnerKey_LogScanBoardEvent is ready-e50 coverage for a
// SECURITY path with zero prior coverage: keyOwnsBoardInDir's log-scan fallback
// for an authored-but-UNPINNED 30301 board event (nostr_invite.go ~570-578, the
// KindBoard case). If that fallback were removed (ownership collapsed to the
// pinned-board fast path only), keyOwnedBoard would report no owned board for this
// fixture, the guard would never fire, and redeemNostrClaimToken would proceed to
// self-mint — so this test would FAIL (err == nil) instead of asserting the stop.
func TestJoin_ForceRefusesOwnerKey_LogScanBoardEvent(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	dir := filepath.Join(base, "project")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	_, ownedBoard := writeOwnerBoardKeyViaLogScan(t, home, dir)

	// Sanity: the board is genuinely UNPINNED here — a passing test below can only
	// be explained by the log-scan fallback, not the pinned-board fast path.
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if cfg.Board != "" {
		t.Fatalf("test fixture pinned a board (%q); this test must exercise the UNPINNED log-scan path only", cfg.Board)
	}

	keyPath := nostr.DefaultKeyPath(home)
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key before: %v", err)
	}

	otherOwner, _ := nostr.GenerateKey()
	now := time.Now().Unix()
	token, err := buildNostrClaimToken(
		rdSync.BoardCoord(otherOwner.PubKeyHex(), "other"),
		[]string{"ws://127.0.0.1:1"}, "claim-e50a", now, now+7200, otherOwner.PubKeyHex())
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	backupDir := filepath.Join(base, "backups")
	medium := &logInviteMedium{log: rdSync.NewNostrLog(filepath.Join(base, "relay.jsonl"))}

	_, err = redeemNostrClaimToken(p, home, dir, backupDir, medium, true /*force*/, "")
	if err == nil {
		t.Fatalf("join --force on a box whose key authored an UNPINNED board event must ERROR (log-scan fallback), got nil")
	}
	if !strings.Contains(err.Error(), "OWNS board") || !strings.Contains(err.Error(), "rd follow") {
		t.Fatalf("error must explain the owner-key stop and point at `rd follow`: %v", err)
	}
	if !strings.Contains(err.Error(), ownedBoard) {
		t.Fatalf("error must name the owned board %s: %v", ownedBoard, err)
	}

	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("owner key on disk was modified by a refused join")
	}
	if entries, _ := os.ReadDir(backupDir); len(entries) != 0 {
		t.Fatalf("a refused join must not create backups; found %d", len(entries))
	}
}

// TestJoin_ForceRefusesOwnerKey_LogScanRoleGrantEvent is ready-e50 coverage for
// keyOwnsBoardInDir's OTHER log-scan branch: a 39301 role-grant the key authored
// (nostr_invite.go ~579-585, the KindRoleGrant case), with NO board pinned and NO
// 30301 board event in the log at all — ownership is recovered purely from the
// grant's "a" tag. If this branch were removed, keyOwnedBoard would report no
// owned board and the guard would not fire, so this test would FAIL.
func TestJoin_ForceRefusesOwnerKey_LogScanRoleGrantEvent(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	dir := filepath.Join(base, "project")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	_, ownedBoard := writeOwnerRoleGrantKeyViaLogScan(t, home, dir)

	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	if cfg.Board != "" {
		t.Fatalf("test fixture pinned a board (%q); this test must exercise the role-grant log-scan path only", cfg.Board)
	}

	keyPath := nostr.DefaultKeyPath(home)
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key before: %v", err)
	}

	otherOwner, _ := nostr.GenerateKey()
	now := time.Now().Unix()
	token, err := buildNostrClaimToken(
		rdSync.BoardCoord(otherOwner.PubKeyHex(), "other"),
		[]string{"ws://127.0.0.1:1"}, "claim-e50b", now, now+7200, otherOwner.PubKeyHex())
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	backupDir := filepath.Join(base, "backups")
	medium := &logInviteMedium{log: rdSync.NewNostrLog(filepath.Join(base, "relay.jsonl"))}

	_, err = redeemNostrClaimToken(p, home, dir, backupDir, medium, true /*force*/, "")
	if err == nil {
		t.Fatalf("join --force on a box whose key authored a role-grant must ERROR (log-scan fallback), got nil")
	}
	if !strings.Contains(err.Error(), "OWNS board") || !strings.Contains(err.Error(), "rd follow") {
		t.Fatalf("error must explain the owner-key stop and point at `rd follow`: %v", err)
	}
	if !strings.Contains(err.Error(), ownedBoard) {
		t.Fatalf("error must name the owned board %s: %v", ownedBoard, err)
	}

	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("owner key on disk was modified by a refused join")
	}
	if entries, _ := os.ReadDir(backupDir); len(entries) != 0 {
		t.Fatalf("a refused join must not create backups; found %d", len(entries))
	}
}

// TestJoin_ForceRefusesToOverwriteOwnerKey is done-condition (a): a plain --force
// join on an owner box ERRORS and leaves the on-disk key byte-identical.
func TestJoin_ForceRefusesToOverwriteOwnerKey(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	dir := filepath.Join(base, "project")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	ownerPub, ownedBoard := writeOwnerBoardKey(t, home, dir)

	keyPath := nostr.DefaultKeyPath(home)
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key before: %v", err)
	}

	// A DIFFERENT board's invite token — the joiner would abandon its own owner key.
	otherOwner, _ := nostr.GenerateKey()
	now := time.Now().Unix()
	token, err := buildNostrClaimToken(
		rdSync.BoardCoord(otherOwner.PubKeyHex(), "other"),
		[]string{"ws://127.0.0.1:1"}, "claim-b32", now, now+7200, otherOwner.PubKeyHex())
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	backupDir := filepath.Join(base, "backups")
	medium := &logInviteMedium{log: rdSync.NewNostrLog(filepath.Join(base, "relay.jsonl"))}

	// Plain --force MUST be refused for an owner key.
	_, err = redeemNostrClaimToken(p, home, dir, backupDir, medium, true /*force*/, "" /*no board-naming force*/)
	if err == nil {
		t.Fatalf("join --force on an owner box must ERROR, got nil")
	}
	if !strings.Contains(err.Error(), "OWNS board") || !strings.Contains(err.Error(), "rd follow") {
		t.Fatalf("error must explain the owner-key stop and point at `rd follow`: %v", err)
	}
	if !strings.Contains(err.Error(), ownedBoard) {
		t.Fatalf("error must name the owned board %s: %v", ownedBoard, err)
	}

	// Key on disk is UNCHANGED.
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("owner key on disk was modified by a refused join")
	}
	// And nothing was backed up (we never got to a legitimate replace).
	if entries, _ := os.ReadDir(backupDir); len(entries) != 0 {
		t.Fatalf("a refused join must not create backups; found %d", len(entries))
	}
	_ = ownerPub
}

// TestJoin_ForceRefusesOwnerKeyFromUnrelatedDir is the ready-23d done-condition
// (edge #1, machine-global): the identity key at DefaultKeyPath is MACHINE-GLOBAL, so
// the guard must fire based on that key's ownership across EVERY board the machine
// knows — not just the project dir the join is run from. With the owner key owning a
// board pinned in project A (a sibling under the projects root), `rd join --force` run
// from an UNRELATED sibling dir B still HARD-STOPS (key on disk unchanged) and points
// at `rd follow`; only the explicit --force-replace-owner-key naming A's board may
// proceed. Before the fix keyOwnedBoard() inspected only dir B, returned "", and the
// join silently orphaned the global owner key.
func TestJoin_ForceRefusesOwnerKeyFromUnrelatedDir(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	// Projects root with two SIBLING repos: A owns a board, B is unrelated.
	root := filepath.Join(base, "projects")
	dirA := filepath.Join(root, "project-a")
	dirB := filepath.Join(root, "project-b")
	if err := os.MkdirAll(dirB, 0o755); err != nil {
		t.Fatalf("mkdir dirB: %v", err)
	}
	// Plant the global identity key + pin an owned board in A ONLY.
	_, ownedBoard := writeOwnerBoardKey(t, home, dirA)

	keyPath := nostr.DefaultKeyPath(home)
	before, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key before: %v", err)
	}

	// A DIFFERENT board's invite token, redeemed from the UNRELATED dir B.
	otherOwner, _ := nostr.GenerateKey()
	now := time.Now().Unix()
	token, err := buildNostrClaimToken(
		rdSync.BoardCoord(otherOwner.PubKeyHex(), "other"),
		[]string{"ws://127.0.0.1:1"}, "claim-23d", now, now+7200, otherOwner.PubKeyHex())
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	backupDir := filepath.Join(base, "backups")
	medium := &logInviteMedium{log: rdSync.NewNostrLog(filepath.Join(base, "relay.jsonl"))}

	// Plain --force from dir B MUST still be refused — the guard is machine-global.
	_, err = redeemNostrClaimToken(p, home, dirB, backupDir, medium, true /*force*/, "")
	if err == nil {
		t.Fatalf("join --force from an unrelated dir must ERROR when the global key owns a board, got nil")
	}
	if !strings.Contains(err.Error(), "OWNS board") || !strings.Contains(err.Error(), "rd follow") {
		t.Fatalf("error must explain the owner-key stop and point at `rd follow`: %v", err)
	}
	if !strings.Contains(err.Error(), ownedBoard) {
		t.Fatalf("error must name the owned board %s pinned in a sibling repo: %v", ownedBoard, err)
	}

	// Key on disk is UNCHANGED, and nothing was backed up.
	after, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read key after: %v", err)
	}
	if string(before) != string(after) {
		t.Fatalf("global owner key on disk was modified by a refused cross-dir join")
	}
	if entries, _ := os.ReadDir(backupDir); len(entries) != 0 {
		t.Fatalf("a refused join must not create backups; found %d", len(entries))
	}

	// The explicit escape hatch naming A's board authorizes the replace (with backup).
	minted, err := redeemNostrClaimToken(p, home, dirB, backupDir, medium, true, ownedBoard)
	if err != nil {
		t.Fatalf("naming the owned board via --force-replace-owner-key must authorize the replace: %v", err)
	}
	if minted == "" {
		t.Fatalf("replace must self-mint a new key")
	}
	// The old owner key was backed up before replacement.
	if entries, _ := os.ReadDir(backupDir); len(entries) == 0 {
		t.Fatalf("owner key must be backed up before an authorized replace")
	}
}

// TestJoin_ReplacedKeyIsBackedUp is done-condition (b): a legitimately replaced key
// (here: a non-owner key overwritten with --force) is first copied under the backups
// dir, byte-identical to the original. Also exercises the board-naming escape hatch.
func TestJoin_ReplacedKeyIsBackedUp(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	dir := filepath.Join(base, "project")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	// A NON-owner key on disk (no board pinned in dir).
	existing, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	keyPath := nostr.DefaultKeyPath(home)
	if err := nostr.SaveKeyFile(keyPath, existing, home); err != nil {
		t.Fatalf("SaveKeyFile: %v", err)
	}
	original, err := os.ReadFile(keyPath)
	if err != nil {
		t.Fatalf("read original: %v", err)
	}
	existingPub := existing.PubKeyHex()

	owner, _ := nostr.GenerateKey()
	now := time.Now().Unix()
	token, err := buildNostrClaimToken(
		rdSync.BoardCoord(owner.PubKeyHex(), "ready"),
		[]string{"ws://127.0.0.1:1"}, "claim-b32b", now, now+7200, owner.PubKeyHex())
	if err != nil {
		t.Fatalf("buildNostrClaimToken: %v", err)
	}
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	backupDir := filepath.Join(base, "backups")
	medium := &logInviteMedium{log: rdSync.NewNostrLog(filepath.Join(base, "relay.jsonl"))}

	minted, err := redeemNostrClaimToken(p, home, dir, backupDir, medium, true /*force*/, "")
	if err != nil {
		t.Fatalf("force-replace of a non-owner key must succeed: %v", err)
	}
	if minted == existingPub {
		t.Fatalf("join must self-mint a NEW key, got the old pubkey")
	}

	// A backup of the replaced key exists, named <pubkey>-<n>, byte-identical.
	backupPath := filepath.Join(backupDir, existingPub+"-1")
	got, err := os.ReadFile(backupPath)
	if err != nil {
		t.Fatalf("expected backup at %s: %v", backupPath, err)
	}
	if string(got) != string(original) {
		t.Fatalf("backup content does not match the replaced key")
	}
	// The on-disk key is now the newly self-minted one (replace happened).
	newPub, err := nostr.StoredPubKeyHex(keyPath)
	if err != nil {
		t.Fatalf("read new key: %v", err)
	}
	if newPub != minted {
		t.Fatalf("on-disk key = %s, want the self-minted %s", newPub, minted)
	}
}

// TestJoin_OwnerKeyForceNamingBoardReplacesWithBackup proves the escape hatch: when
// the operator explicitly names the owned board via ownerKeyForce, the owner key IS
// replaced — but only after it is backed up.
func TestJoin_OwnerKeyForceNamingBoardReplacesWithBackup(t *testing.T) {
	base := t.TempDir()
	home := filepath.Join(base, "home")
	dir := filepath.Join(base, "project")
	if err := os.MkdirAll(home, 0o700); err != nil {
		t.Fatalf("mkdir home: %v", err)
	}
	ownerPub, ownedBoard := writeOwnerBoardKey(t, home, dir)

	owner, _ := nostr.GenerateKey()
	now := time.Now().Unix()
	token, _ := buildNostrClaimToken(
		rdSync.BoardCoord(owner.PubKeyHex(), "ready"),
		[]string{"ws://127.0.0.1:1"}, "claim-b32c", now, now+7200, owner.PubKeyHex())
	p, err := decodeNostrClaimToken(token)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	backupDir := filepath.Join(base, "backups")
	medium := &logInviteMedium{log: rdSync.NewNostrLog(filepath.Join(base, "relay.jsonl"))}

	// Wrong board name is still refused.
	if _, err := redeemNostrClaimToken(p, home, dir, backupDir, medium, true, "30301:deadbeef:wrong"); err == nil {
		t.Fatalf("a mismatched --force-replace-owner-key must still be refused")
	}

	// The exact owned coordinate authorizes the replace.
	minted, err := redeemNostrClaimToken(p, home, dir, backupDir, medium, true, ownedBoard)
	if err != nil {
		t.Fatalf("naming the owned board must authorize the replace: %v", err)
	}
	if minted == ownerPub {
		t.Fatalf("replace must self-mint a new key")
	}
	if _, err := os.ReadFile(filepath.Join(backupDir, ownerPub+"-1")); err != nil {
		t.Fatalf("owner key must be backed up before replace: %v", err)
	}
}
