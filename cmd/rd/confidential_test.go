package main

// CLI integration tests for confidential-by-default boards (ready-deb, epic
// ready-216). These exercise the REAL run* command bodies through the nostr-native
// write/read path: `rd init` (confidential) → `rd create` → `rd show`/`rd list`
// (owner decrypts) with at-rest opacity + owner-self-grant recoverability asserted
// against the on-disk event log.

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// setupConfidentialProject mirrors setupNostrNativeProject but marks the board
// Confidential (what `rd init` does by default).
func setupConfidentialProject(t *testing.T) (string, string) {
	t.Helper()
	dir := setupNostrCmdTest(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	board := rdSync.BoardSpec{BoardD: boardD, Title: "project", Maintainers: []string{owner}}
	be, err := rdSync.BuildBoardEvent(k, board, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{be}); err != nil {
		t.Fatalf("append board event: %v", err)
	}
	return dir, owner
}

func TestConfidentialCLIRoundTrip(t *testing.T) {
	dir, _ := setupConfidentialProject(t)

	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "SECRET rotate the leaked signing key", context: "the signing key leaked; rotate now",
		itemType: "task", priority: "p1", labels: []string{"urgent"},
	})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}

	// OWNER reads plaintext transparently (no manual key handling) — done-condition #3.
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	it := byID[id]
	if it == nil {
		t.Fatalf("item %s missing from projection", id)
	}
	if it.Title != "SECRET rotate the leaked signing key" {
		t.Fatalf("owner did not read plaintext title: %q", it.Title)
	}
	if it.Context != "the signing key leaked; rotate now" {
		t.Fatalf("owner did not read plaintext context: %q", it.Context)
	}
	if len(it.Labels) != 1 || it.Labels[0] != "urgent" {
		t.Fatalf("owner did not render the human label: %v", it.Labels)
	}

	// AT REST: inspect the on-disk log.
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var sawSealedCard, sawSelfGrantCEK, sawIssue bool
	for _, e := range events {
		switch e.Kind {
		case 30302: // card
			if strings.Contains(e.Content, "SECRET") || strings.Contains(e.Content, "leaked") {
				t.Fatalf("confidential card leaks plaintext in Content: %q", e.Content)
			}
			if v, ok := tagVal(e.Tags, "title"); ok {
				t.Fatalf("confidential card carries a clear title tag: %q", v)
			}
			if l, ok := tagVal(e.Tags, "l"); ok && l == "urgent" {
				t.Fatalf("confidential card leaks a plaintext label")
			}
			if v, _ := tagVal(e.Tags, "enc"); v == "1" {
				sawSealedCard = true
			}
		case 39301: // role grant — the owner self-grant must carry the CEK (recoverability)
			if _, ok := tagVal(e.Tags, "cek"); ok {
				sawSelfGrantCEK = true
			}
		case 1621: // NIP-34 issue event — must NOT exist on a confidential board
			sawIssue = true
		}
	}
	if !sawSealedCard {
		t.Fatal("no sealed (enc=1) card event on the confidential board")
	}
	if !sawSelfGrantCEK {
		t.Fatal("no owner self-grant carrying the CEK — key material is not recoverable from the log")
	}
	if sawIssue {
		t.Fatal("confidential board published a plaintext kind:1621 issue event (title/description leak)")
	}
}

// TestConfidentialEnableMigration proves `rd confidential enable` on an EXISTING
// plaintext board: the pre-cutover item stays readable (grandfathered) while a new
// item is sealed — and the cutover self-grant is stamped after the old card so the
// strict created_at<cutover grandfather does not drop a same-second card.
func TestConfidentialEnableMigration(t *testing.T) {
	dir := setupNostrCmdTest(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
	// Start PUBLIC.
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord, Public: true}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	board := rdSync.BoardSpec{BoardD: boardD, Title: "project", Maintainers: []string{owner}}
	be, _ := rdSync.BuildBoardEvent(k, board, time.Now().Unix())
	rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{be})

	oldID, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "OLD plaintext item", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create old: %v", err)
	}

	// Enable confidential mode (mirror `rd confidential enable`): mark + bootstrap.
	cfg, _ := rdconfig.LoadSyncConfig(dir)
	cfg.Public = false
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatalf("save confidential cfg: %v", err)
	}
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	if _, err := boardConfidentialEnvelope(dir, pub, owner, boardD); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	newID, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "NEW secret item", context: "sealed", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create new: %v", err)
	}

	// Owner reads BOTH: old grandfathered (plaintext), new sealed (decrypted).
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if it := byID[oldID]; it == nil || it.Title != "OLD plaintext item" {
		t.Fatalf("pre-cutover item not grandfathered/readable: %+v", it)
	}
	if it := byID[newID]; it == nil || it.Title != "NEW secret item" {
		t.Fatalf("post-enable item not sealed/readable: %+v", it)
	}

	// At rest: old card clear, new card sealed.
	events, _ := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	oldClear, newSealed := false, false
	for _, e := range events {
		if e.Kind != 30302 {
			continue
		}
		d, _ := tagVal(e.Tags, "d")
		_, hasTitle := tagVal(e.Tags, "title")
		_, sealed := tagVal(e.Tags, "enc")
		if d == oldID && hasTitle && !sealed {
			oldClear = true
		}
		if d == newID && !hasTitle && sealed {
			newSealed = true
		}
	}
	if !oldClear {
		t.Fatal("old card should remain clear plaintext at rest")
	}
	if !newSealed {
		t.Fatal("new card should be sealed at rest")
	}

	// §11.13a (ready-9a6): the board must not CONTRADICT ITS OWN cutover. `rd
	// confidential enable` stamps the CEK self-grant at max(log)+1 precisely so the
	// strict `created_at < cutover` grandfather clause keeps the same-second plaintext
	// card above — which puts the cutover one second in the FUTURE, and the card this
	// same test seals is stamped "now". A sealed card OLDER than the cutover is
	// §11.13a's TIME witness, so without sealedItemCreatedAt's floor the reader would
	// refuse the derived cutover on every subsequent read and drop the grandfathered
	// card the assertions above just proved readable — no relay misbehaviour anywhere.
	kr := rdSync.DeriveBoardKeyring(events, k, owner, boardD)
	cut, ok := kr.Cutover(coord)
	if !ok || cut == 0 {
		t.Fatalf("board contradicts its own cutover: Cutover = (%d, %v), want the derived instant", cut, ok)
	}
	for _, e := range events {
		if e.Kind != 30302 {
			continue
		}
		if _, sealed := tagVal(e.Tags, "enc"); !sealed {
			continue
		}
		if e.CreatedAt < cut {
			d, _ := tagVal(e.Tags, "d")
			t.Fatalf("sealed card %s stamped %d, BEFORE its own board's cutover %d — §11.13a TIME witness", d, e.CreatedAt, cut)
		}
	}
}

// ----------------------------------------------------------------------------
// §11.13a write-side floor, the two REPUBLISH sites (ready-9a6 round 2).
//
// TestConfidentialEnableMigration above covers only the CREATE site
// (nostrwrite.go's runCreateNostr). The floor is wired into two more:
// publishItemStatusChangeNostr (`rd claim` / `rd done` / …) and
// publishItemCardEditNostr (`rd label add` / `rd update` …). Those are NOT dead
// code for §11.13a: BuildStatusEventWithIssueRoot copies the card's enc /
// cek_epoch markers and the board `a` tag onto the kind-1630 STATUS event
// (pkg/sync/nostrwire.go), and grantsWithheld (pkg/sync/keydist.go) admits ANY
// verified confidential event on the coordinate as a TIME witness regardless of
// KIND. So a status close or a card edit racing `rd confidential enable` inside
// one wall-clock second manufactures the witness against rd's own board exactly
// as the create path did.
//
// WHY A REPUBLISH NEEDS A GAP TO BE ARMED, and why the decoy below exists. A
// CREATE stamps at `now` because the new item's drift scope is empty, while
// cutoverCreatedAt stamps the self-grant at max(WHOLE log)+1 — the create site is
// therefore armed by default. A REPUBLISH stamps at max(this item's scope)+1, so
// it collides with the cutover only when some OTHER event in the log is NEWER
// than this item's own last event when confidentiality is enabled. That is
// ordinary board activity: a second item touched more recently than the one being
// closed. armSameSecondCutoverRace builds exactly that state out of real command
// bodies, and asserts the arm is live before the operation under test runs — so a
// wall-clock tick that disarms the race fails LOUDLY here instead of passing
// vacuously.
// ----------------------------------------------------------------------------

// armSameSecondCutoverRace builds a board that has just been switched to
// confidential in the same wall-clock second as ordinary activity, and returns
// the id of an item whose next republish would — without the floor — be stamped
// strictly BEFORE the board's cutover. decoyID stays plaintext and untouched
// after the switch, so it is the grandfathered card a contradiction would drop.
func armSameSecondCutoverRace(t *testing.T) (dir, targetID, decoyID, coord string, cutover int64) {
	t.Helper()
	dir = setupNostrCmdTest(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	boardD := projectPrefix(dir)
	coord = rdSync.BoardCoord(owner, boardD)
	// Start PUBLIC, exactly as TestConfidentialEnableMigration does.
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord, Public: true}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	board := rdSync.BoardSpec{BoardD: boardD, Title: "project", Maintainers: []string{owner}}
	be, err := rdSync.BuildBoardEvent(k, board, time.Now().Unix())
	if err != nil {
		t.Fatalf("BuildBoardEvent: %v", err)
	}
	if _, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{be}); err != nil {
		t.Fatalf("append board event: %v", err)
	}

	targetID, err = runCreateNostr(mustDir(t), nostrCreateSpec{title: "TARGET plaintext item", itemType: "task", priority: "p2"})
	if err != nil {
		t.Fatalf("create target: %v", err)
	}
	decoyID, err = runCreateNostr(mustDir(t), nostrCreateSpec{title: "DECOY plaintext item", itemType: "task", priority: "p3"})
	if err != nil {
		t.Fatalf("create decoy: %v", err)
	}
	// Ordinary activity on the OTHER item, which the per-item drift clock stamps at
	// max(decoy scope)+1 each time. Four of them put the whole-log maximum four
	// seconds above the target's own last event, so the race stays armed even if the
	// test straddles a second boundary or three.
	for i := 0; i < 4; i++ {
		if err := runLabelAddNostr(decoyID, fmt.Sprintf("busy-%d", i)); err != nil {
			t.Fatalf("decoy label add %d: %v", i, err)
		}
	}

	// `rd confidential enable`: mark the config, then bootstrap the CEK self-grant
	// at cutoverCreatedAt = max(whole log)+1.
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	cfg.Public = false
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatalf("save confidential cfg: %v", err)
	}
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v (ok=%v)", err, ok)
	}
	if _, err := boardConfidentialEnvelope(dir, pub, owner, boardD); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	cutover, ok = rdSync.DeriveBoardKeyring(events, k, owner, boardD).Cutover(coord)
	if !ok || cutover == 0 {
		t.Fatalf("board already contradicts its own cutover before the operation under test: (%d, %v)", cutover, ok)
	}

	// THE ARM. This is what the unfloored per-item clock would stamp the next
	// republish of targetID with. If it is not strictly below the cutover there is
	// no TIME witness to manufacture and the test would pass for the wrong reason.
	unfloored := nostrNextCreatedAt(pub.Log, rdSync.ItemDriftScope(targetID))
	if unfloored >= cutover {
		t.Fatalf("race not armed: unfloored write clock %d is not below cutover %d — the assertion below could not fail", unfloored, cutover)
	}
	return dir, targetID, decoyID, coord, cutover
}

// sealedEventKindsOnBoard counts the confidential events the log carries on the
// board coordinate, keyed by KIND, using exactly grantsWithheld's admission test
// (pkg/sync/keydist.go over envelope.go's isConfidential / boardCoordOf): an
// `enc` marker plus SOME "a" tag equal to the board coordinate. Reading only the
// FIRST "a" tag would silently drop every kind-1630 status event, whose first
// "a" is the CARD coordinate (30302:…) and whose board coordinate is the second,
// purely additive one BuildStatusEventWithIssueRoot appends (ready-7ec).
func sealedEventKindsOnBoard(events []*nostr.Event, coord string) map[int][]*nostr.Event {
	out := map[int][]*nostr.Event{}
	for _, e := range events {
		if _, sealed := tagVal(e.Tags, "enc"); !sealed {
			continue
		}
		onBoard := false
		for _, tg := range e.Tags {
			if len(tg) >= 2 && tg[0] == "a" && tg[1] == coord {
				onBoard = true
				break
			}
		}
		if !onBoard {
			continue
		}
		out[e.Kind] = append(out[e.Kind], e)
	}
	return out
}

// assertCutoverUncontradicted is the §11.13a read-side consequence: NO verified
// sealed event of ANY KIND on the board may predate the board's cutover, the
// reader must still believe that cutover, and the plaintext card it grandfathers
// must still project. wantKinds are the sealed event kinds the operation under
// test must actually have produced — without them the sweep below is vacuous.
func assertCutoverUncontradicted(t *testing.T, dir, decoyID, coord string, cutover int64, wantKinds ...int) {
	t.Helper()
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	boardD := projectPrefix(dir)
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	byKind := sealedEventKindsOnBoard(events, coord)
	for _, want := range wantKinds {
		if len(byKind[want]) == 0 {
			t.Fatalf("no sealed kind-%d event on %s — the operation under test did not exercise the path this arm exists for", want, coord)
		}
	}
	// Kind-BLIND, matching grantsWithheld: the 30302 card and the 1630 status event
	// both carry enc/cek_epoch and the board coordinate, and EITHER one testifies.
	for kind, evs := range byKind {
		for _, e := range evs {
			if e.CreatedAt < cutover {
				d, _ := tagVal(e.Tags, "d")
				t.Errorf("sealed kind-%d event (d=%q) stamped %d, BEFORE its own board's cutover %d — §11.13a TIME witness manufactured by rd's own write path",
					kind, d, e.CreatedAt, cutover)
			}
		}
	}

	// End-to-end: the reader still believes the cutover, and the grandfathered
	// plaintext card the cutover exists to preserve still projects.
	got, ok := rdSync.DeriveBoardKeyring(events, k, owner, boardD).Cutover(coord)
	if !ok || got != cutover {
		t.Fatalf("reader refuses the board's own cutover after the write: Cutover = (%d, %v), want (%d, true)", got, ok, cutover)
	}
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	if it := byID[decoyID]; it == nil || it.Title != "DECOY plaintext item" {
		t.Fatalf("grandfathered pre-cutover card no longer readable: %+v", it)
	}
}

// TestConfidentialEnableStatusChangeSameSecond pins the floor at
// publishItemStatusChangeNostr (cmd/rd/nostr.go). `rd claim` republishes the card
// AND a kind-1630 status event under the same stamp; both carry the sealed
// board's enc marker, so either one predating the cutover poisons the board.
func TestConfidentialEnableStatusChangeSameSecond(t *testing.T) {
	dir, targetID, decoyID, coord, cutover := armSameSecondCutoverRace(t)

	if err := runClaimNostr(targetID, "picking this up in the same second as the switch"); err != nil {
		t.Fatalf("runClaimNostr: %v", err)
	}

	// Both the re-sealed 30302 card AND the kind-1630 status event must land at or
	// after the cutover — the status event is the one the create-path arm can never
	// reach, and it is admissible testimony because grantsWithheld is kind-blind.
	assertCutoverUncontradicted(t, dir, decoyID, coord, cutover, 30302, 1630)
}

// TestConfidentialEnableCardEditSameSecond pins the floor at
// publishItemCardEditNostr (cmd/rd/nostr.go). `rd label add` republishes the
// re-sealed card with NO status event, so this arm is red for that site alone.
func TestConfidentialEnableCardEditSameSecond(t *testing.T) {
	dir, targetID, decoyID, coord, cutover := armSameSecondCutoverRace(t)

	if err := runLabelAddNostr(targetID, "urgent"); err != nil {
		t.Fatalf("runLabelAddNostr: %v", err)
	}

	assertCutoverUncontradicted(t, dir, decoyID, coord, cutover, 30302)
}

// TestTwoIdentityConfidentialCLI is the two-identity CLI end-to-end (ready-deb):
// an OWNER grants a distinct MEMBER identity, the member reads plaintext through
// the real `rd` read path, a non-member sees the placeholder, and after `rd revoke`
// the member keeps its pre-revoke (epoch-1) reads but a post-revoke (epoch-2) card
// is unreadable to it (forward secrecy) — all by swapping the ambient $RD_HOME /
// $CF_HOME identity in-process, exactly as two machines would.
func TestTwoIdentityConfidentialCLI(t *testing.T) {
	dir, _ := setupConfidentialProject(t)
	ownerHome := os.Getenv("RD_HOME")
	ownerCf := os.Getenv("CF_HOME")

	// Owner authors an epoch-1 confidential item.
	id1, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "SECRET epoch-1", context: "member-readable", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("owner create: %v", err)
	}

	// Mint a DISTINCT member identity, persisted to its own home.
	mk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("member key: %v", err)
	}
	memberPub := mk.PubKeyHex()
	memberHome := t.TempDir()
	if err := nostr.WriteKeyFileExclusive(filepath.Join(memberHome, "nostr-identity.json"), mk, memberHome); err != nil {
		t.Fatalf("persist member key: %v", err)
	}
	memberCf := t.TempDir()

	// Owner grants the member read access (wraps the CEK into the signed grant).
	if _, err := publishRoleGrant(memberPub, rdSync.RoleContributor, "", 0, ""); err != nil {
		t.Fatalf("owner grant: %v", err)
	}

	readAs := func(home, cf string) map[string]*state.Item {
		t.Helper()
		t.Setenv("RD_HOME", home)
		t.Setenv("CF_HOME", cf)
		_, byID, err := nostrProjectAllItems()
		if err != nil {
			t.Fatalf("read as %s: %v", home, err)
		}
		return byID
	}

	// MEMBER reads the epoch-1 item as plaintext.
	if it := readAs(memberHome, memberCf)[id1]; it == nil || it.Title != "SECRET epoch-1" {
		t.Fatalf("granted member did not read plaintext: %+v", it)
	}

	// A NON-member (fresh third identity) sees the placeholder.
	if it := readAs(t.TempDir(), t.TempDir())[id1]; it == nil || it.Title != "[encrypted]" {
		t.Fatalf("non-member should see placeholder, got %+v", it)
	}

	// OWNER revokes the member (rotates to epoch 2) and authors an epoch-2 item.
	t.Setenv("RD_HOME", ownerHome)
	t.Setenv("CF_HOME", ownerCf)
	if err := runNostrGrantRevoke(dir, memberPub, rdSync.RoleRevoked, "", 0, ""); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	id2, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "SECRET epoch-2", context: "post-revoke", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("owner create epoch-2: %v", err)
	}

	// MEMBER keeps its epoch-1 read but the epoch-2 card is a placeholder (forward secrecy).
	byMember := readAs(memberHome, memberCf)
	if it := byMember[id1]; it == nil || it.Title != "SECRET epoch-1" {
		t.Fatalf("revoked member lost its historical epoch-1 read: %+v", it)
	}
	if it := byMember[id2]; it == nil || it.Title != "[encrypted]" {
		t.Fatalf("revoked member read a POST-revoke epoch-2 card — forward secrecy broken: %+v", it)
	}

	// OWNER still reads everything.
	byOwner := readAs(ownerHome, ownerCf)
	if byOwner[id1].Title != "SECRET epoch-1" || byOwner[id2].Title != "SECRET epoch-2" {
		t.Fatalf("owner lost a read: e1=%q e2=%q", byOwner[id1].Title, byOwner[id2].Title)
	}
}

// TestConfidentialBootstrapWrapsExistingMembers proves that flipping a board that
// ALREADY has a member to confidential wraps the CEK to that member at bootstrap —
// so making a live multi-writer board confidential does not lock existing members
// out of their own board.
func TestConfidentialBootstrapWrapsExistingMembers(t *testing.T) {
	dir := setupNostrCmdTest(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
	// Start PUBLIC (plaintext).
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord, Public: true}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	board := rdSync.BoardSpec{BoardD: boardD, Title: "project", Maintainers: []string{owner}}
	be, _ := rdSync.BuildBoardEvent(k, board, time.Now().Unix())
	rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{be})

	// Owner grants a member while the board is still public (grant carries no CEK).
	mk, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("member key: %v", err)
	}
	if _, err := publishRoleGrant(mk.PubKeyHex(), rdSync.RoleContributor, "", 0, ""); err != nil {
		t.Fatalf("grant member on public board: %v", err)
	}

	// Flip to confidential and bootstrap (as the owner's first confidential write would).
	cfg, _ := rdconfig.LoadSyncConfig(dir)
	cfg.Public = false
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatalf("flip confidential: %v", err)
	}
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	if _, err := boardConfidentialEnvelope(dir, pub, owner, boardD); err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	// The pre-existing member must now hold the CEK — via the bootstrap wrap, with
	// NO explicit post-flip grant.
	events, _ := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	kr := rdSync.DeriveBoardKeyring(events, mk, owner, boardD)
	if _, _, ok := kr.CurrentEpoch(coord); !ok {
		t.Fatal("existing member did NOT receive the CEK at bootstrap — flipping the board locked them out")
	}
}

func TestPublicBoardStaysPlaintext(t *testing.T) {
	// A board explicitly marked NOT confidential keeps writing plaintext cards.
	dir := setupNostrCmdTest(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	coord := rdSync.BoardCoord(owner, projectPrefix(dir))
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord, Public: true}); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	board := rdSync.BoardSpec{BoardD: projectPrefix(dir), Title: "project", Maintainers: []string{owner}}
	be, _ := rdSync.BuildBoardEvent(k, board, time.Now().Unix())
	rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).AppendUnique([]*nostr.Event{be})

	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "public title", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}
	events, _ := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	for _, e := range events {
		if e.Kind == 30302 {
			if v, ok := tagVal(e.Tags, "title"); !ok || v != "public title" {
				t.Fatalf("public board card should carry a clear title tag, got %q (present=%v)", v, ok)
			}
			if _, ok := tagVal(e.Tags, "enc"); ok {
				t.Fatal("public board card unexpectedly carries an enc marker")
			}
		}
		if e.Kind == 1621 {
			// A public board SHOULD still get its NIP-34 issue interop anchor.
			if s, _ := tagVal(e.Tags, "subject"); s == "public title" {
				return // found the plaintext issue anchor — correct for a public board
			}
		}
	}
	_ = id
}

// TestConfidentialRevokeUppercaseGrantee_WithholdsNewEpochCEK is ready-3e1's
// CONFIDENTIALITY case, and the one the earlier rounds of this item threw away as
// "not a real property". It is real, and this is the only test that covers it.
//
// The setup is `rd revoke <UPPERCASE-hex>` on a confidential board — the exact
// input the item was filed about, differing from the working case by letter case
// alone. Without publishRoleGrant's normalization (cmd/rd/nostr_grant.go) the
// sequence is:
//
//  1. the revocation is filed under p=<UPPERCASE>, so DeriveGrantHolders ends up
//     with level[UPPER]=revoked AND level[lower]=contributor — the member's real
//     key is still a live member;
//  2. runNostrGrantRevoke then rekeys (rekeyBoardOnRevoke → rotationMembership),
//     which classifies members by that level map. `exclude` is the same uppercase
//     string and matches no holder either, so the revoked key lands in `receive`;
//  3. the owner SIGNS the revoked key a fresh grant at cek_epoch=2 carrying the
//     wrapped new CEK;
//  4. the revoked key decrypts every card written after its own revocation, while
//     `rd revoke` prints "they can no longer read or write".
//
// TestConfidentialRotateWithholdsFromEVERYRevokedKey (confidential_rotate_test.go)
// does NOT catch this — it revokes with the canonical lowercase key, so it stays
// green in the pre-fix state — and the grant-tag tests assert only the p-tag
// string, never the CEK outcome. Asserted here in four independent ways: what the
// revoked identity can actually READ through the production read path, which
// epochs its derived keyring holds, structurally that no owner-signed grant
// addressed to that key in EITHER case carries an epoch above 1, and that the
// revocation landed on the member's real identity at all.
//
// TWO REVOCATION PATHS, because reverting ONE normalization is not the same
// experiment as reverting the other:
//
//	"rd revoke"        drives the production entry point runNostrGrantRevoke,
//	                   which normalizes its own copy of grantee BEFORE calling
//	                   publishRoleGrant. It therefore reproduces the true pre-fix
//	                   state (both grant-path lines reverted) and goes red there,
//	                   but stays green if only publishRoleGrant's line is reverted.
//	"publishRoleGrant" mirrors runNostrGrantRevoke's body (publish, then rekey)
//	                   while passing the uppercase string straight through, which
//	                   is exactly what the pre-fix entry point did. It ISOLATES
//	                   nostr_grant.go's line: it goes red on that single-line
//	                   revert alone, independent of authz_nostr.go.
func TestConfidentialRevokeUppercaseGrantee_WithholdsNewEpochCEK(t *testing.T) {
	revokes := []struct {
		name   string
		revoke func(t *testing.T, dir, owner, boardD, upper string)
	}{
		{
			name: "rd revoke <UPPERCASE> (production entry point)",
			revoke: func(t *testing.T, dir, _, _, upper string) {
				t.Helper()
				if err := runNostrGrantRevoke(dir, upper, rdSync.RoleRevoked, "", 0, ""); err != nil {
					t.Fatalf("revoke by UPPERCASE pubkey: %v", err)
				}
			},
		},
		{
			name: "publishRoleGrant(<UPPERCASE>) + rekey (isolates nostr_grant.go)",
			revoke: func(t *testing.T, dir, owner, boardD, upper string) {
				t.Helper()
				if _, err := publishRoleGrant(upper, rdSync.RoleRevoked, "", 0, ""); err != nil {
					t.Fatalf("publishRoleGrant(revoked, UPPERCASE): %v", err)
				}
				pub, ok, err := nostrPublisher()
				if err != nil || !ok {
					t.Fatalf("nostrPublisher: %v (ok=%v)", err, ok)
				}
				// The unnormalized string is handed on as `exclude` too — as
				// runNostrGrantRevoke did before ready-3e1.
				if err := rekeyBoardOnRevoke(dir, pub, owner, boardD, upper); err != nil {
					t.Fatalf("rekeyBoardOnRevoke: %v", err)
				}
			},
		},
	}

	for _, rc := range revokes {
		t.Run(rc.name, func(t *testing.T) {
			dir, owner := setupConfidentialProject(t)
			boardD := projectPrefix(dir)
			coord := rdSync.BoardCoord(owner, boardD)
			ownerHome, ownerCf := os.Getenv("RD_HOME"), os.Getenv("CF_HOME")

			m1, m1Home, m1Cf := mintIdentity(t)
			lower := m1.PubKeyHex()
			upper := strings.ToUpper(lower)

			// Grant with the CANONICAL key: the member is a genuine, live member
			// holding the epoch-1 CEK. Only the REVOKE is uppercase.
			if err := runNostrGrantRevoke(dir, lower, rdSync.RoleContributor, "agent-m1", 0, ""); err != nil {
				t.Fatalf("grant m1: %v", err)
			}
			idPre, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "PRE-REVOKE card", itemType: "task", priority: "p1"})
			if err != nil {
				t.Fatalf("create pre-revoke card: %v", err)
			}

			rc.revoke(t, dir, owner, boardD, upper)

			idPost, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "POST-REVOKE card", itemType: "task", priority: "p1"})
			if err != nil {
				t.Fatalf("create post-revoke card: %v", err)
			}

			// (1) READ as the revoked identity, through the production read path.
			t.Setenv("RD_HOME", m1Home)
			t.Setenv("CF_HOME", m1Cf)
			_, byM1, err := nostrProjectAllItems()
			if err != nil {
				t.Fatalf("read as the revoked member: %v", err)
			}
			if it := byM1[idPre]; it == nil || it.Title != "PRE-REVOKE card" {
				t.Fatalf("the revoked member lost its PRE-revocation read; revocation must not invalidate history: %+v", it)
			}
			if it := byM1[idPost]; it == nil || it.Title != "[encrypted]" {
				t.Fatalf("a key revoked by its UPPERCASE pubkey read a card written AFTER its revocation (%+v) — "+
					"the uppercase revocation was filed under a key that matches nobody, so the rekey handed the "+
					"new epoch CEK back to the revoked member: forward secrecy silently not delivered", it)
			}

			// (2) KEYRING: the revoked member must hold epoch 1 and nothing newer.
			if eps := keyringFor(t, dir, m1, owner, boardD).Epochs(coord); len(eps) != 1 || eps[0] != 1 {
				t.Fatalf("the revoked member's derived keyring holds epochs %v, want exactly [1] — an epoch above 1 "+
					"means the post-revocation CEK was wrapped to it", eps)
			}

			// (3) STRUCTURAL: no owner-signed grant addressed to that key in
			// EITHER case carries a CEK above epoch 1. Reading placeholders proves
			// it could not decrypt; this proves the key bytes were never wrapped to
			// it at all, so no future change to the decryptor can quietly undo the
			// property.
			t.Setenv("RD_HOME", ownerHome)
			t.Setenv("CF_HOME", ownerCf)
			events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
			if err != nil {
				t.Fatalf("read log: %v", err)
			}
			sawEpoch1Wrap := false
			for _, e := range events {
				if e.Kind != rdSync.KindRoleGrant || e.PubKey != owner {
					continue
				}
				p, _ := tagVal(e.Tags, "p")
				if !strings.EqualFold(p, lower) {
					continue
				}
				if cek, ok := tagVal(e.Tags, "cek"); !ok || cek == "" {
					continue
				}
				ep, _ := tagVal(e.Tags, "cek_epoch")
				if ep != "1" {
					t.Fatalf("an owner-signed grant addressed to the REVOKED key (p=%q) carries cek_epoch=%s — "+
						"the post-revocation CEK was wrapped to a revoked member", p, ep)
				}
				sawEpoch1Wrap = true
			}
			if !sawEpoch1Wrap {
				t.Fatal("expected the revoked key to still hold its original epoch-1 wrap (revocation must not erase history)")
			}

			// (4) And the revocation itself landed on the member's REAL identity:
			// without this, `rd sessions` and every authz gate still show it active.
			levels, _ := rdSync.DeriveLevels(events, owner, boardD)
			if lvl, ok := levels[lower]; !ok || lvl != rdSync.LevelRevoked {
				t.Fatalf("level for the member's real (lowercase) pubkey = (%d, present=%v), want revoked", levels[lower], ok)
			}
		})
	}
}
