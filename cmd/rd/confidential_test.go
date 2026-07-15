package main

// CLI integration tests for confidential-by-default boards (ready-deb, epic
// ready-216). These exercise the REAL run* command bodies through the nostr-native
// write/read path: `rd init` (confidential) → `rd create` → `rd show`/`rd list`
// (owner decrypts) with at-rest opacity + owner-self-grant recoverability asserted
// against the on-disk event log.

import (
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
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord, Confidential: true}); err != nil {
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
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord, Confidential: false}); err != nil {
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
	cfg.Confidential = true
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
	if err := publishRoleGrant(memberPub, rdSync.RoleContributor, "", 0, ""); err != nil {
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

func TestPublicBoardStaysPlaintext(t *testing.T) {
	// A board explicitly marked NOT confidential keeps writing plaintext cards.
	dir := setupNostrCmdTest(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner := k.PubKeyHex()
	coord := rdSync.BoardCoord(owner, projectPrefix(dir))
	if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord, Confidential: false}); err != nil {
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
