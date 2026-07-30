package main

// `rd confidential rewrap` — ready-470.
//
// THE PROPERTY UNDER TEST IS NOT "the grants changed". A command that rotated to a
// new epoch, or that re-minted the CEK, or that re-sealed every card, would all
// leave a board whose grants carry hex wraps and would all be wrong: the first two
// orphan every existing card for anyone who does not also hold the old key, and the
// third rewrites signed history. So each test below asserts the thing the hex
// encoding is only half of — the key VALUE is byte-identical, the epoch is
// unchanged, the cards are untouched, the cutover stays where it was — alongside
// the encoding itself.
//
// The legacy artifact is built with pkg/nip44.Seal over the RAW 32 bytes, which is
// literally what WrapKey did before ready-c4b. Nothing here simulates the bug with
// a flag or a stub.

import (
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/pflag"

	"github.com/3dl-dev/ready/pkg/nip44"
	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// legacyGrantsAt places the epoch-1 grants this far in the past, so a replacement
// stamped "now" is unmistakable next to one stamped original+1.
const legacyGrantAge = 100000

// legacyRawWrap seals key to grantee the way pkg/sync/keydist.go's WrapKey did
// BEFORE ready-c4b: the 32 raw bytes, not their hex. This is the payload a browser
// cannot recover, because NIP-07's nip44.decrypt returns a string and 32 random
// bytes do not survive a UTF-8 decode.
func legacyRawWrap(t *testing.T, owner *nostr.Key, granteePub string, key [32]byte) string {
	t.Helper()
	w, err := nip44.Seal(owner, granteePub, key[:])
	if err != nil {
		t.Fatalf("legacy raw wrap: %v", err)
	}
	return w
}

// legacyBoard is a confidential board bootstrapped ENTIRELY out of pre-ready-c4b
// grants: the owner self-grant plus one member grant, both carrying raw-byte wraps
// of the same epoch-1 CEK and LTK.
type legacyBoard struct {
	dir     string
	owner   string
	boardD  string
	coord   string
	ownerK  *nostr.Key
	member  *nostr.Key
	cek     [32]byte
	ltk     [32]byte
	grantAt int64
}

func setupLegacyRawWrapBoard(t *testing.T) legacyBoard {
	t.Helper()
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	member, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	cek, err := rdSync.MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	ltk, err := rdSync.MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	at := time.Now().Unix() - legacyGrantAge

	lb := legacyBoard{
		dir: dir, owner: owner, boardD: boardD, coord: rdSync.BoardCoord(owner, boardD),
		ownerK: k, member: member, cek: cek, ltk: ltk, grantAt: at,
	}
	lb.appendLegacyGrant(t, owner, rdSync.RoleOwner, cek, ltk, 1, at)
	lb.appendLegacyGrant(t, member.PubKeyHex(), rdSync.RoleContributor, cek, ltk, 1, at)
	return lb
}

// appendLegacyGrant writes one owner-signed grant with raw-byte wraps straight
// into the local log, as a machine syncing from a relay would receive it.
func (lb legacyBoard) appendLegacyGrant(t *testing.T, grantee, role string, cek, ltk [32]byte, epoch int, at int64) *nostr.Event {
	t.Helper()
	spec := rdSync.RoleGrantSpec{
		BoardD: lb.boardD, BoardAuthor: lb.owner, Grantee: grantee, Role: role,
		Label:      "legacy key (epoch 1)",
		WrappedCEK: legacyRawWrap(t, lb.ownerK, grantee, cek), CEKEpoch: epoch,
		WrappedLTK: legacyRawWrap(t, lb.ownerK, grantee, ltk),
	}
	ev, err := rdSync.BuildRoleGrantEvent(lb.ownerK, spec, at)
	if err != nil {
		t.Fatalf("BuildRoleGrantEvent: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(lb.dir)).AppendUnique([]*nostr.Event{ev}); err != nil {
		t.Fatalf("append legacy grant: %v", err)
	}
	return ev
}

// runConfidentialArgv drives the REAL cobra path for `rd confidential ...`, so the
// flags, the plan and the output a human sees are what is under test — not a RunE
// reached around the parser. Flags are restored afterwards because cobra remembers
// what it parsed onto a package-level command.
func runConfidentialArgv(t *testing.T, argv ...string) (string, error) {
	t.Helper()
	var sink strings.Builder
	rootCmd.SetErr(&sink)
	rootCmd.SetOut(&sink)
	defaults := map[string]string{}
	confidentialRewrapCmd.LocalNonPersistentFlags().VisitAll(func(f *pflag.Flag) { defaults[f.Name] = f.DefValue })
	t.Cleanup(func() {
		rootCmd.SetErr(nil)
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
		for name, def := range defaults {
			_ = confidentialRewrapCmd.Flags().Set(name, def)
		}
	})
	var runErr error
	out := captureStdoutPipe(t, func() {
		rootCmd.SetArgs(append([]string{"confidential"}, argv...))
		runErr = rootCmd.Execute()
	})
	return out, runErr
}

// cekGrantsFor returns every owner-signed CEK-bearing grant in the log for
// (grantee, epoch), newest first is not assumed — the caller asserts over all.
func cekGrantsFor(t *testing.T, lb legacyBoard, grantee string, epoch int) []*nostr.Event {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(lb.dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var out []*nostr.Event
	for _, e := range events {
		if e.Kind != rdSync.KindRoleGrant || e.PubKey != lb.owner {
			continue
		}
		if tagVal1(e, "a") != lb.coord || tagVal1(e, "p") != grantee || tagVal1(e, "cek") == "" {
			continue
		}
		if tagVal1(e, "cek_epoch") != strconv.Itoa(epoch) {
			continue
		}
		out = append(out, e)
	}
	return out
}

// newestGrant returns the grant with the highest created_at for (grantee, epoch) —
// the one a NIP-01 relay keeps in that addressable slot, i.e. the only one a
// browser ever sees.
func newestGrant(t *testing.T, lb legacyBoard, grantee string, epoch int) *nostr.Event {
	t.Helper()
	var best *nostr.Event
	for _, e := range cekGrantsFor(t, lb, grantee, epoch) {
		if best == nil || e.CreatedAt > best.CreatedAt || (e.CreatedAt == best.CreatedAt && e.ID > best.ID) {
			best = e
		}
	}
	if best == nil {
		t.Fatalf("no owner-signed epoch-%d CEK grant for %s", epoch, shortKey(grantee))
	}
	return best
}

// TestConfidentialRewrapMakesLegacyWrapsBrowserReadable is the done condition of
// ready-470, asserted on the four things that must ALL hold at once: the wrap a
// relay now serves is hex (so a NIP-07 signer's string return survives), the key
// inside it is byte-identical to the one it replaced, the epoch did not move, and
// no card event changed.
func TestConfidentialRewrapMakesLegacyWrapsBrowserReadable(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)

	cardID, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "SECRET under the legacy epoch", context: "sealed with the raw-wrapped CEK",
		itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create card: %v", err)
	}

	// PRECONDITION: what the board page receives today is unreadable to it. The
	// owner's OWN wrap opens in Go and carries the raw payload — which is exactly
	// the state that renders [encrypted] in the browser.
	before := newestGrant(t, lb, lb.owner, 1)
	gotCEK, browserSafe, err := rdSync.UnwrapKeyPayload(lb.ownerK, lb.owner, tagVal1(before, "cek"))
	if err != nil {
		t.Fatalf("owner cannot open its own legacy wrap: %v", err)
	}
	if browserSafe {
		t.Fatal("fixture is not the legacy artifact: its wrap already carries the hex payload")
	}
	if gotCEK != lb.cek {
		t.Fatal("fixture wrap does not carry the CEK it was built from")
	}
	beforeIDs := logSnapshot(t, lb.dir)

	out, err := runConfidentialArgv(t, "rewrap", "--no-verify")
	if err != nil {
		t.Fatalf("rd confidential rewrap: %v\n%s", err, out)
	}
	if !strings.Contains(out, "2 grant(s) to re-wrap") {
		t.Fatalf("output does not name the two grants it re-wrapped:\n%s", out)
	}

	for _, grantee := range []string{lb.owner, lb.member.PubKeyHex()} {
		g := newestGrant(t, lb, grantee, 1)
		cek, safe, uerr := rdSync.UnwrapKeyPayload(lb.ownerK, grantee, tagVal1(g, "cek"))
		if uerr != nil {
			t.Fatalf("%s: re-wrapped CEK does not open: %v", shortKey(grantee), uerr)
		}
		if !safe {
			t.Fatalf("%s: the grant a relay now serves STILL carries a raw payload — the board page is no better off", shortKey(grantee))
		}
		if cek != lb.cek {
			t.Fatalf("%s: the re-wrap CHANGED the CEK value — every card sealed under it just became unreadable to this member", shortKey(grantee))
		}
		ltk, safeLTK, uerr := rdSync.UnwrapKeyPayload(lb.ownerK, grantee, tagVal1(g, "ltk"))
		if uerr != nil || !safeLTK || ltk != lb.ltk {
			t.Fatalf("%s: LTK re-wrap wrong (err=%v hex=%v same=%v)", shortKey(grantee), uerr, safeLTK, ltk == lb.ltk)
		}
	}

	// NO NEW EPOCH. A rotation would also produce hex wraps and would orphan the
	// card above for anyone not holding epoch 1.
	kr := keyringFor(t, lb.dir, lb.ownerK, lb.owner, lb.boardD)
	if epochs := kr.Epochs(lb.coord); len(epochs) != 1 || epochs[0] != 1 {
		t.Fatalf("owner now holds epochs %v, want exactly [1] — the re-wrap minted an epoch", epochs)
	}
	if got, ok := kr.CEK(lb.coord, 1); !ok || got != lb.cek {
		t.Fatal("the epoch-1 CEK the owner derives changed across the re-wrap")
	}

	// NOTHING BUT GRANTS WAS WRITTEN, and nothing already in the log was altered.
	after := logSnapshot(t, lb.dir)
	added := 0
	for id, e := range after {
		if _, existed := beforeIDs[id]; existed {
			continue
		}
		added++
		if e.Kind != rdSync.KindRoleGrant {
			t.Fatalf("re-wrap published a kind-%d event; it must only publish grants", e.Kind)
		}
	}
	if added != 2 {
		t.Fatalf("re-wrap added %d events, want exactly 2 (one per legacy grant)", added)
	}
	for id, e := range beforeIDs {
		if got := after[id]; got == nil || got.Content != e.Content || got.CreatedAt != e.CreatedAt {
			t.Fatalf("event %s was altered or dropped — existing history must be untouched", id[:12])
		}
	}

	// And the card still reads, through the real projection, for the owner.
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if it := byID[cardID]; it == nil || it.Title != "SECRET under the legacy epoch" {
		t.Fatalf("the card stopped reading after the re-wrap: %+v", it)
	}
}

// TestConfidentialRewrapKeepsTheCutoverWhereItWas pins the created_at rule.
//
// A board's confidential CUTOVER is the EARLIEST created_at of any owner-signed
// CEK-bearing grant, and a relay serves only the newest event per slot — so a
// re-wrap stamped "now" moves the cutover a relay-seeded reader derives forward by
// the whole age of the board, and every plaintext card written in between starts
// reading as grandfathered instead of quarantined. The replacement is therefore
// stamped one second after the grant it replaces, and this test fails if anyone
// ever "simplifies" that to time.Now().
func TestConfidentialRewrapKeepsTheCutoverWhereItWas(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	if _, err := runConfidentialArgv(t, "rewrap", "--no-verify"); err != nil {
		t.Fatalf("rewrap: %v", err)
	}

	g := newestGrant(t, lb, lb.owner, 1)
	if g.CreatedAt != lb.grantAt+1 {
		t.Fatalf("re-wrapped grant created_at = %d, want %d (the original + 1s)", g.CreatedAt, lb.grantAt+1)
	}

	// Derive the cutover the way a machine seeding ONLY from a relay would: the
	// newest event per slot, i.e. the re-wrapped grants alone.
	relayView := []*nostr.Event{newestGrant(t, lb, lb.owner, 1), newestGrant(t, lb, lb.member.PubKeyHex(), 1)}
	kr := rdSync.DeriveBoardKeyring(relayView, lb.ownerK, lb.owner, lb.boardD)
	cutover, ok := kr.Cutover(lb.coord)
	if !ok {
		t.Fatal("a relay-only view of the re-wrapped grants no longer marks the board confidential")
	}
	if cutover != lb.grantAt+1 {
		t.Fatalf("relay-derived cutover = %d, want %d — the grandfather boundary moved by %d seconds",
			cutover, lb.grantAt+1, cutover-lb.grantAt)
	}
}

// TestConfidentialRewrapIsIdempotent: the second run must publish nothing. A
// command that republished every run would churn the relay and, because each run
// bumps created_at, would walk the cutover forward one second at a time.
func TestConfidentialRewrapIsIdempotent(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	if _, err := runConfidentialArgv(t, "rewrap", "--no-verify"); err != nil {
		t.Fatalf("first rewrap: %v", err)
	}
	snapshot := logSnapshot(t, lb.dir)

	out, err := runConfidentialArgv(t, "rewrap", "--no-verify")
	if err != nil {
		t.Fatalf("second rewrap: %v", err)
	}
	if !strings.Contains(out, "nothing to re-wrap") {
		t.Fatalf("second run did not report itself a no-op:\n%s", out)
	}
	if got := logSnapshot(t, lb.dir); len(got) != len(snapshot) {
		t.Fatalf("second run published %d new event(s); a re-wrap of an already-hex board must publish nothing", len(got)-len(snapshot))
	}
}

// TestConfidentialRewrapDryRunPublishesNothing — the preview has to be free.
func TestConfidentialRewrapDryRunPublishesNothing(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	snapshot := logSnapshot(t, lb.dir)

	out, err := runConfidentialArgv(t, "rewrap", "--dry-run")
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !strings.Contains(out, "2 grant(s) to re-wrap") || !strings.Contains(out, "nothing published") {
		t.Fatalf("dry run output is not a preview:\n%s", out)
	}
	if got := logSnapshot(t, lb.dir); len(got) != len(snapshot) {
		t.Fatalf("--dry-run published %d event(s)", len(got)-len(snapshot))
	}
}

// TestConfidentialRewrapWithholdsRevokedKeys. A revoked member still holds its old
// raw wrap and its rd CLI still reads the epoch it was given — that is the accepted
// limit of prospective revocation. What must NOT happen is the owner handing that
// key a fresh, browser-readable copy while re-wrapping everyone else.
func TestConfidentialRewrapWithholdsRevokedKeys(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	revoke, err := rdSync.BuildRoleGrantEvent(lb.ownerK, rdSync.RoleGrantSpec{
		BoardD: lb.boardD, BoardAuthor: lb.owner, Grantee: lb.member.PubKeyHex(), Role: rdSync.RoleRevoked,
	}, lb.grantAt+10)
	if err != nil {
		t.Fatalf("build revoke: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(lb.dir)).AppendUnique([]*nostr.Event{revoke}); err != nil {
		t.Fatalf("append revoke: %v", err)
	}

	out, err := runConfidentialArgv(t, "rewrap", "--no-verify")
	if err != nil {
		t.Fatalf("rewrap: %v", err)
	}
	if !strings.Contains(out, "REVOKED — withheld") {
		t.Fatalf("output does not report the revoked key as withheld:\n%s", out)
	}
	if !strings.Contains(out, "1 grant(s) to re-wrap") {
		t.Fatalf("expected exactly the owner self-grant to be re-wrapped:\n%s", out)
	}
	g := newestGrant(t, lb, lb.member.PubKeyHex(), 1)
	if _, safe, uerr := rdSync.UnwrapKeyPayload(lb.ownerK, lb.member.PubKeyHex(), tagVal1(g, "cek")); uerr != nil || safe {
		t.Fatalf("the revoked member's newest epoch-1 grant became browser-readable (hex=%v err=%v)", safe, uerr)
	}
}

// TestConfidentialRewrapRefusesANonOwner. Only the owner's signature is honoured
// as a source of board keys, so only the owner can re-mint one.
func TestConfidentialRewrapRefusesANonOwner(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	stranger, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	pub.Key = stranger
	if _, err := planBoardRewrap(lb.dir, pub, lb.owner, lb.boardD); err == nil {
		t.Fatal("a non-owner planned a re-wrap of someone else's board keys")
	} else if !strings.Contains(err.Error(), "only the board OWNER") {
		t.Fatalf("wrong refusal: %v", err)
	}
}

// TestConfidentialRewrapRefusesTwoKeysForOneEpoch. Re-wrapping recovers each key
// from the very wrap it replaces, so a grant carrying a DIFFERENT key for an epoch
// the owner already holds is a fork: re-wrapping it would silently declare one of
// the two the winner for that member. Refuse instead.
func TestConfidentialRewrapRefusesTwoKeysForOneEpoch(t *testing.T) {
	lb := setupLegacyRawWrapBoard(t)
	other, err := rdSync.MintKey()
	if err != nil {
		t.Fatalf("MintKey: %v", err)
	}
	rogue, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	lb.appendLegacyGrant(t, rogue.PubKeyHex(), rdSync.RoleContributor, other, lb.ltk, 1, lb.grantAt)

	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	if _, err := planBoardRewrap(lb.dir, pub, lb.owner, lb.boardD); err == nil {
		t.Fatal("planned a re-wrap over two different CEKs for one epoch")
	} else if !strings.Contains(err.Error(), "NOT the one the owner holds") {
		t.Fatalf("wrong refusal: %v", err)
	}
}
