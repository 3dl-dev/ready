package main

// ready-475: `rd board confidential-since` — the CLI half of §11.13a's
// owner-signed cutover assertion. The READER's half (what the assertion does to
// a fold, and the three security properties) is
// pkg/sync/keydist_confidentialsince_test.go and
// web/board/src/lib/confidentiality.test.ts; this file is about the WRITE path:
// what event the command publishes, what it refuses, and what it must not lose.
//
// Deterministic, like board_archive_test.go and for the same reason: boardTestEnv
// configures NO relay, so the relay-read loop is a no-op and every assertion here
// is against the LOCAL authoritative log (§16.1). The live-relay read-back is
// this item's audit trail, not a unit test.

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

func runConfidentialSince(t *testing.T, args ...string) error {
	t.Helper()
	var errBuf bytes.Buffer
	boardConfidentialSinceCmd.SetErr(&errBuf)
	t.Cleanup(func() { boardConfidentialSinceCmd.SetErr(nil) })
	return boardConfidentialSinceCmd.RunE(boardConfidentialSinceCmd, args)
}

// TestBoardConfidentialSinceCmd_AssertsAndRemoves is the round trip: the
// published definition carries the tag, the board's OTHER fields survive the
// republish (§16.3 read-modify-write), and `0` takes the assertion back off.
func TestBoardConfidentialSinceCmd_AssertsAndRemoves(t *testing.T) {
	owner, _, _, dir := boardTestEnv(t)
	self := owner.PubKeyHex()
	other := testCLIKey(t).PubKeyHex()

	const boardD = "since-target"
	seedBoard(t, dir, owner, boardD, "Since Target", []string{self, other}, 1700000000)
	coord := rdSync.BoardCoord(self, boardD)

	if err := runConfidentialSince(t, boardD, "1784206981"); err != nil {
		t.Fatalf("rd board confidential-since %s: %v", boardD, err)
	}
	win := winningBoardInLog(t, dir, coord)
	since, ok := rdSync.BoardConfidentialSince(win)
	if !ok || since != 1784206981 {
		t.Fatalf("published definition asserts (%d, %v), want (1784206981, true): tags=%v", since, ok, win.Tags)
	}
	// The assertion must be readable exactly as the READERS read it — through
	// the coordinate-bound, signature-verified path, not just as a tag.
	if got, found := rdSync.AssertedConfidentialSince([]*nostr.Event{win}, coord); !found || got != 1784206981 {
		t.Fatalf("AssertedConfidentialSince = (%d, %v), want (1784206981, true)", got, found)
	}
	assertBoardSpecUnchanged(t, win, "Since Target", []string{self, other})

	// --- removal is the exact inverse --------------------------------------
	if err := runConfidentialSince(t, boardD, "0"); err != nil {
		t.Fatalf("rd board confidential-since %s 0: %v", boardD, err)
	}
	win2 := winningBoardInLog(t, dir, coord)
	if _, ok := rdSync.BoardConfidentialSince(win2); ok {
		t.Fatalf("assertion survived removal: tags=%v", win2.Tags)
	}
	assertBoardSpecUnchanged(t, win2, "Since Target", []string{self, other})
	if win2.ID == win.ID || win2.CreatedAt <= win.CreatedAt {
		t.Fatalf("removal did not publish a later, distinct event (%s@%d vs %s@%d)", win2.ID, win2.CreatedAt, win.ID, win.CreatedAt)
	}
}

// TestBoardConfidentialSinceCmd_RefusesForeignOwnerAndBadInstant pins the two
// refusals, and that neither one publishes anything: only the OWNER may assert a
// board's cutover (a coordinate naming another pubkey is refused before any
// event is built), and a non-instant argument is rejected rather than coerced to
// 0 — which would silently REMOVE an existing assertion.
func TestBoardConfidentialSinceCmd_RefusesForeignOwnerAndBadInstant(t *testing.T) {
	owner, _, _, dir := boardTestEnv(t)
	self := owner.PubKeyHex()
	const boardD = "since-refuse"
	seedBoard(t, dir, owner, boardD, "Since Refuse", []string{self}, 1700000000)
	coord := rdSync.BoardCoord(self, boardD)
	before := winningBoardInLog(t, dir, coord)

	foreign := rdSync.BoardCoord(testCLIKey(t).PubKeyHex(), boardD)
	if err := runConfidentialSince(t, foreign, "1784206981"); err == nil {
		t.Fatal("asserting on a board owned by another pubkey was accepted")
	}
	for _, bad := range []string{"-1", "abc", "1784206981.5", ""} {
		if err := boardConfidentialSinceCmd.Args(boardConfidentialSinceCmd, []string{boardD, bad}); err != nil {
			continue // arity is checked before RunE; these all pass it
		}
		if err := boardConfidentialSinceCmd.RunE(boardConfidentialSinceCmd, []string{boardD, bad}); err == nil {
			t.Fatalf("instant %q was accepted", bad)
		}
	}

	after := winningBoardInLog(t, dir, coord)
	if after.ID != before.ID {
		t.Fatalf("a refused assertion still published an event: %s -> %s", before.ID, after.ID)
	}
}

// TestBoardArchiveCarriesTheAssertionForward is the regression this command's
// existence creates: `rd board archive` republishes the SAME definition event,
// and BoardSpec does not model `confidential_since`. A republish that dropped it
// would put the board back on the derived-cutover path — on a board whose own log
// contradicts that derivation, back to withholding its entire plaintext history —
// as a silent side effect of archiving.
func TestBoardArchiveCarriesTheAssertionForward(t *testing.T) {
	owner, _, _, dir := boardTestEnv(t)
	self := owner.PubKeyHex()
	const boardD = "since-archive"
	seedBoard(t, dir, owner, boardD, "Since Archive", []string{self}, 1700000000)
	coord := rdSync.BoardCoord(self, boardD)

	if err := runConfidentialSince(t, boardD, "1784206981"); err != nil {
		t.Fatalf("rd board confidential-since: %v", err)
	}

	var errBuf bytes.Buffer
	boardArchiveCmd.SetErr(&errBuf)
	t.Cleanup(func() { boardArchiveCmd.SetErr(nil) })
	if err := boardArchiveCmd.RunE(boardArchiveCmd, []string{boardD}); err != nil {
		t.Fatalf("rd board archive: %v (stderr=%s)", err, errBuf.String())
	}
	win := winningBoardInLog(t, dir, coord)
	if !rdSync.IsBoardArchived(win) {
		t.Fatal("archive did not mark the board archived — the fixture proves nothing")
	}
	if since, ok := rdSync.BoardConfidentialSince(win); !ok || since != 1784206981 {
		t.Fatalf("archiving DROPPED the cutover assertion: got (%d, %v), want (1784206981, true)", since, ok)
	}

	errBuf.Reset()
	boardUnarchiveCmd.SetErr(&errBuf)
	t.Cleanup(func() { boardUnarchiveCmd.SetErr(nil) })
	if err := boardUnarchiveCmd.RunE(boardUnarchiveCmd, []string{boardD}); err != nil {
		t.Fatalf("rd board unarchive: %v (stderr=%s)", err, errBuf.String())
	}
	if since, ok := rdSync.BoardConfidentialSince(winningBoardInLog(t, dir, coord)); !ok || since != 1784206981 {
		t.Fatalf("unarchiving DROPPED the cutover assertion: got (%d, %v), want (1784206981, true)", since, ok)
	}
}

// ---------------------------------------------------------------------------
// THE ITEM-WRITE PATHS (ready-475 REWORK).
//
// `rd board archive`/`unarchive` above are the RARE way a board's kind-30301
// definition gets rebuilt. The common way — several times a day on a live
// project, and never mentioning boards at all — is an ORDINARY ITEM WRITE by the
// board's owner: cmd/rd/nostrwrite.go sets boardArg whenever the signer IS the
// board author, and PublishItemWithReason then republishes the definition beside
// the card. A kind-30301 is addressable, so that republish REPLACES the asserted
// definition on every conformant relay. Dropping the tag there would silently
// un-assert the cutover and put the board back to withholding its whole plaintext
// history — 167 of 536 cards on the live `ready` board — with no relay
// misbehaving and nobody having asked for it.
//
// There are exactly three such paths, and each gets its own case below. They
// share one guard (Publisher.buildBoardDefinition, pkg/sync/boardconfidential.go)
// on purpose: that is what makes a FOURTH caller of PublishItem safe by
// construction rather than by remembering.
//
// EACH CASE ASSERTS THE PATH REALLY DID REPUBLISH, not just that the log still
// holds an asserted definition somewhere. Without that, deleting boardArg
// entirely would make all three pass while the product broke — and asserting on
// the LATEST-WINS winner is not enough either, because the item path's created_at
// comes from a different drift scope than the board path's and may tie or trail.
// What is checked is the LAST kind-30301 the path appended.
// ---------------------------------------------------------------------------

// csItemEnv is boardTestEnv with ONE difference that matters here: the project
// DIRECTORY is named after the board, so boardSpecForProject (which derives the
// board "d" from the directory basename) produces the same coordinate the project
// pins. boardTestEnv's dir is always "project" while its board is "board384-...",
// which is harmless for the commands it serves and fatal for these: the item
// write would publish a definition for a DIFFERENT coordinate than the one under
// assertion, and every case below would pass without proving anything.
func csItemEnv(t *testing.T) (owner *nostr.Key, boardD, coord, dir string) {
	t.Helper()
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	base := t.TempDir()
	rdHome := filepath.Join(base, "rdhome")
	if err := os.MkdirAll(rdHome, 0o700); err != nil {
		t.Fatalf("mkdir rdhome: %v", err)
	}
	t.Setenv("RD_HOME", rdHome)
	t.Setenv("RD_NOSTR_RELAY_URL", "")
	t.Setenv("RD_NOSTR", "")
	t.Setenv("RD_NOSTR_READ", "")

	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	if err := nostr.SaveKeyFile(nostr.DefaultKeyPath(rdHome), k, rdHome); err != nil {
		t.Fatalf("SaveKeyFile: %v", err)
	}

	// No hyphens: projectPrefix strips everything but alphanumerics, so a hyphen
	// in the directory name would make boardSpecForProject's "d" differ from the
	// pinned one (the check below catches it either way).
	boardD = fmt.Sprintf("sinceitem%d", time.Now().UnixNano())
	dir = filepath.Join(base, boardD)
	if err := os.MkdirAll(filepath.Join(dir, ".ready"), 0o755); err != nil {
		t.Fatalf("mkdir project: %v", err)
	}
	coord = rdSync.BoardCoord(k.PubKeyHex(), boardD)
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{Board: coord, ProjectName: boardD}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	if got := boardSpecForProject(dir, k.PubKeyHex()).BoardD; got != boardD {
		t.Fatalf("fixture is not wired: boardSpecForProject says %q, project pins %q", got, boardD)
	}
	return k, boardD, coord, dir
}

// boardEventsInLog returns every kind-30301 for coord in the log's APPEND order.
// Append order, not latest-wins, because the question is what a given write path
// PUBLISHED — which is what a relay stores for the addressable slot — and the
// winner by created_at can be an older event from a different drift scope.
func boardEventsInLog(t *testing.T, dir, coord string) []*nostr.Event {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var out []*nostr.Event
	for _, e := range events {
		if e == nil || e.Kind != rdSync.KindBoard {
			continue
		}
		if rdSync.BoardCoord(e.PubKey, rdSync.BoardSpecFromEvent(e).BoardD) == coord {
			out = append(out, e)
		}
	}
	return out
}

// csAssertedProject seeds a project whose board already carries the assertion,
// via the REAL commands: an item create publishes the board's first definition,
// then `rd board confidential-since` republishes it asserting csItemAssert. It
// returns the number of definitions in the log at that point, which is the
// baseline each case's "this path really did republish" check counts from.
func csAssertedProject(t *testing.T) (owner *nostr.Key, boardD, coord, dir string, defsBefore int) {
	t.Helper()
	owner, boardD, coord, dir = csItemEnv(t)
	self := owner.PubKeyHex()

	if err := publishItemFullCreateNostr(dir, self, &state.Item{
		ID: "since-seed", Title: "seed item", Type: "task", Priority: "p2",
		Status: state.StatusInbox, By: self, For: self,
	}); err != nil {
		t.Fatalf("seed create: %v", err)
	}
	if err := runConfidentialSince(t, boardD, "1784206981"); err != nil {
		t.Fatalf("rd board confidential-since: %v", err)
	}
	defs := boardEventsInLog(t, dir, coord)
	if len(defs) < 2 {
		t.Fatalf("fixture: want at least a create-time definition and an asserted one, got %d", len(defs))
	}
	if since, ok := rdSync.BoardConfidentialSince(defs[len(defs)-1]); !ok || since != csItemAssert {
		t.Fatalf("fixture: the seeded assertion is not in place: got (%d, %v)", since, ok)
	}
	return owner, boardD, coord, dir, len(defs)
}

// csItemAssert is the instant these cases assert — the live `ready` board's own,
// so a failure reads as the bug it stands for.
const csItemAssert = int64(1784206981)

// csAssertLastDefinitionCarriesIt is the shared check: the path under test
// appended at least one NEW kind-30301 (so the case cannot pass by the path not
// republishing at all), and the last one it appended still carries the assertion.
func csAssertLastDefinitionCarriesIt(t *testing.T, dir, coord, path string, defsBefore int) {
	t.Helper()
	defs := boardEventsInLog(t, dir, coord)
	if len(defs) <= defsBefore {
		t.Fatalf("%s republished NO board definition (%d before, %d after) — this case would pass vacuously; "+
			"if the path legitimately stopped republishing the board, delete the case rather than leaving it green", path, defsBefore, len(defs))
	}
	last := defs[len(defs)-1]
	since, ok := rdSync.BoardConfidentialSince(last)
	if !ok || since != csItemAssert {
		t.Fatalf("%s republished the board definition WITHOUT the cutover assertion: got (%d, %v), want (%d, true). "+
			"A kind-30301 is addressable, so this event REPLACES the asserted one on every relay and the board "+
			"silently goes back to withholding its plaintext history. tags=%v", path, since, ok, csItemAssert, last.Tags)
	}
	// The assertion must survive as the READERS read it — coordinate-bound and
	// signature-verified — not merely as a tag that happens to be present.
	if got, found := rdSync.AssertedConfidentialSince([]*nostr.Event{last}, coord); !found || got != csItemAssert {
		t.Fatalf("%s: AssertedConfidentialSince on the republished definition = (%d, %v), want (%d, true)", path, got, found, csItemAssert)
	}
}

// TestItemCreateCarriesTheAssertionForward is the path that made this a REWORK:
// `rd create` on the owner's own board. It is the most frequent 30301 republish
// there is, and it names no board and asks for no board change.
func TestItemCreateCarriesTheAssertionForward(t *testing.T) {
	owner, _, coord, dir, defsBefore := csAssertedProject(t)
	self := owner.PubKeyHex()

	if err := publishItemFullCreateNostr(dir, self, &state.Item{
		ID: "since-create", Title: "an ordinary item, created after the assertion",
		Type: "task", Priority: "p1", Status: state.StatusInbox, By: self, For: self,
	}); err != nil {
		t.Fatalf("publishItemFullCreateNostr: %v", err)
	}
	csAssertLastDefinitionCarriesIt(t, dir, coord, "rd create (publishItemFullCreateNostr)", defsBefore)
}

// TestNostrPublishCarriesTheAssertionForward is `rd nostr publish <item>`: the
// manual republish path, which goes through PublishItemWithReason with the same
// owner-signed boardArg.
func TestNostrPublishCarriesTheAssertionForward(t *testing.T) {
	_, _, coord, dir, defsBefore := csAssertedProject(t)

	var out bytes.Buffer
	nostrPublishCmd.SetOut(&out)
	nostrPublishCmd.SetErr(&out)
	t.Cleanup(func() { nostrPublishCmd.SetOut(nil); nostrPublishCmd.SetErr(nil) })
	if err := nostrPublishCmd.RunE(nostrPublishCmd, []string{"since-seed"}); err != nil {
		t.Fatalf("rd nostr publish: %v (out=%s)", err, out.String())
	}
	csAssertLastDefinitionCarriesIt(t, dir, coord, "rd nostr publish", defsBefore)
}

// TestNostrPutCarriesTheAssertionForward is `rd nostr put <item>`: the low-level
// create/update, the third and last caller that hands PublishItem a board spec.
func TestNostrPutCarriesTheAssertionForward(t *testing.T) {
	_, _, coord, dir, defsBefore := csAssertedProject(t)

	if err := nostrPutCmd.Flags().Set("title", "put after the assertion"); err != nil {
		t.Fatalf("set --title: %v", err)
	}
	t.Cleanup(func() { _ = nostrPutCmd.Flags().Set("title", "") })
	var out bytes.Buffer
	nostrPutCmd.SetOut(&out)
	nostrPutCmd.SetErr(&out)
	t.Cleanup(func() { nostrPutCmd.SetOut(nil); nostrPutCmd.SetErr(nil) })
	if err := nostrPutCmd.RunE(nostrPutCmd, []string{"since-put"}); err != nil {
		t.Fatalf("rd nostr put: %v (out=%s)", err, out.String())
	}
	csAssertLastDefinitionCarriesIt(t, dir, coord, "rd nostr put", defsBefore)
}
