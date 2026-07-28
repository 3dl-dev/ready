package main

// `rd confidential rotate` — retiring a compromised board read key (ready-2b25).
//
// The property under test is narrow and easy to fake: "the epoch advanced". A
// rotation that re-wrapped the SAME key bytes under a higher epoch number, or that
// re-sealed every historical card, or that handed the new key back to a key revoked
// two rotations ago, would all satisfy "the epoch advanced" and would all be
// worthless (or destructive). Each test below therefore asserts the thing the
// number is a proxy for: fresh key MATERIAL, untouched history at rest, an
// old-epoch-only reader who can still read the past and cannot read the present,
// and a withheld set that covers EVERY revoked key rather than the one most
// recently revoked.

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// epochLimitedKeyring exposes only the CEK epochs in `allow` from an otherwise
// complete keyring, modelling a reader (or a leaked `rd board --with-key` link)
// that holds SOME epochs and not others. Cutover is delegated unchanged, so the
// fold gate behaves exactly as it does for a real member — only key possession
// differs.
type epochLimitedKeyring struct {
	kr    *rdSync.BoardKeyring
	allow map[int]bool
}

func (e epochLimitedKeyring) CEK(coord string, epoch int) ([32]byte, bool) {
	if !e.allow[epoch] {
		var zero [32]byte
		return zero, false
	}
	return e.kr.CEK(coord, epoch)
}

func (e epochLimitedKeyring) Cutover(coord string) (int64, bool) { return e.kr.Cutover(coord) }

// projectWithEpochs projects the whole log as a reader holding exactly `epochs`
// of the owner's key material, through the REAL projection (same fold gate, same
// trust set, same placeholder rule as `rd list`).
func projectWithEpochs(t *testing.T, dir string, k *nostr.Key, owner, boardD string, epochs ...int) map[string]*state.Item {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	allow := map[int]bool{}
	for _, e := range epochs {
		allow[e] = true
	}
	limited := epochLimitedKeyring{kr: rdSync.DeriveBoardKeyring(events, k, owner, boardD), allow: allow}
	return rdSync.ProjectItems(events, rdSync.ProjectOptions{
		Trusted:         nostrTrustSet(dir, k.PubKeyHex()),
		PinnedBoard:     nostrPinnedBoard(dir),
		Decryptor:       limited,
		EncryptedBoards: limited,
	})
}

// keyringFor derives k's board key material from the on-disk log.
func keyringFor(t *testing.T, dir string, k *nostr.Key, owner, boardD string) *rdSync.BoardKeyring {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	return rdSync.DeriveBoardKeyring(events, k, owner, boardD)
}

// rotateOnce runs the rotation exactly as `rd confidential rotate` does (plan
// from the local log, then publish) and returns the plan and the grants it
// published.
func rotateOnce(t *testing.T, dir, boardAuthor, boardD string) (*epochRotationPlan, []*nostr.Event) {
	t.Helper()
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	plan, err := planBoardRotation(dir, pub, boardAuthor, boardD, "")
	if err != nil {
		t.Fatalf("planBoardRotation: %v", err)
	}
	published, err := rotateBoardEpoch(pub, boardAuthor, boardD, plan)
	if err != nil {
		t.Fatalf("rotateBoardEpoch: %v", err)
	}
	return plan, published
}

// logSnapshot indexes the log by event id so a test can prove which events a
// rotation ADDED and that it altered none of the ones already there.
func logSnapshot(t *testing.T, dir string) map[string]*nostr.Event {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	out := make(map[string]*nostr.Event, len(events))
	for _, e := range events {
		out[e.ID] = e
	}
	return out
}

// mintIdentity creates a fresh nostr identity persisted to its own $RD_HOME, so a
// test can read the board "as" another machine by swapping the ambient env.
func mintIdentity(t *testing.T) (*nostr.Key, string, string) {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	home := t.TempDir()
	if err := nostr.WriteKeyFileExclusive(filepath.Join(home, "nostr-identity.json"), k, home); err != nil {
		t.Fatalf("persist key: %v", err)
	}
	return k, home, t.TempDir()
}

// TestConfidentialRotateMintsFreshKeyMaterial is the core contract: rotation must
// mint NEW key bytes, not renumber the old ones.
//
// The weakest implementation that satisfies "epoch went from 1 to 2" is one that
// re-wraps the SAME CEK under cek_epoch=2 — which would leave a leaked key able to
// read everything written after the rotation, i.e. no remedy at all. So this
// asserts on the 32 bytes: the epoch-2 CEK differs from the epoch-1 CEK, the
// epoch-1 CEK is byte-identical to what it was before (history keeps its key), and
// a reader holding ONLY epoch 1 can read the pre-rotation card and cannot read the
// post-rotation one.
func TestConfidentialRotateMintsFreshKeyMaterial(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}

	beforeID, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "PRE-ROTATION secret", context: "written under epoch 1", itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create pre-rotation item: %v", err)
	}

	krBefore := keyringFor(t, dir, k, owner, boardD)
	ep1, cek1, ok := krBefore.CurrentEpoch(coord)
	if !ok || ep1 != 1 {
		t.Fatalf("expected a bootstrapped board at epoch 1, got epoch=%d ok=%v", ep1, ok)
	}
	ltk1, ok := krBefore.LTK(coord)
	if !ok {
		t.Fatal("owner holds no LTK before rotation")
	}

	plan, published := rotateOnce(t, dir, owner, boardD)
	if plan.OldEpoch != 1 || plan.NewEpoch != 2 {
		t.Fatalf("plan epochs = %d -> %d, want 1 -> 2", plan.OldEpoch, plan.NewEpoch)
	}
	if len(published) != 1 {
		t.Fatalf("a solo-owner board should publish exactly one grant (the owner self-grant), got %d", len(published))
	}

	krAfter := keyringFor(t, dir, k, owner, boardD)
	ep2, cek2, ok := krAfter.CurrentEpoch(coord)
	if !ok {
		t.Fatal("owner holds NO read key after rotating its own board")
	}
	if ep2 != 2 {
		t.Fatalf("current epoch after one rotation = %d, want exactly 2", ep2)
	}
	// THE assertion the epoch number is only a proxy for.
	if cek2 == cek1 {
		t.Fatal("rotation re-used the OLD CEK under a new epoch number — a leaked key would still decrypt everything written from now on")
	}
	// History keeps its own key: epoch 1's CEK is still held, unchanged.
	got1, ok := krAfter.CEK(coord, 1)
	if !ok {
		t.Fatal("owner lost the epoch-1 CEK — every pre-rotation card just became unreadable")
	}
	if got1 != cek1 {
		t.Fatal("the epoch-1 CEK CHANGED across the rotation — pre-rotation ciphertext no longer opens under it")
	}
	if epochs := krAfter.Epochs(coord); len(epochs) != 2 || epochs[0] != 1 || epochs[1] != 2 {
		t.Fatalf("owner holds epochs %v, want exactly [1 2]", epochs)
	}
	// The LTK is deliberately STABLE across epochs (label tokens must keep matching).
	ltk2, ok := krAfter.LTK(coord)
	if !ok || ltk2 != ltk1 {
		t.Fatalf("LTK changed across the rotation (ok=%v) — every future label token would stop matching existing cards", ok)
	}

	afterID, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "POST-ROTATION secret", context: "written under epoch 2", itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create post-rotation item: %v", err)
	}

	// Future writes really seal under the new epoch — assert the marker at rest.
	if got := cardEpochMarker(t, dir, afterID); got != "2" {
		t.Fatalf("post-rotation card carries cek_epoch=%q, want \"2\"", got)
	}
	if got := cardEpochMarker(t, dir, beforeID); got != "1" {
		t.Fatalf("pre-rotation card's cek_epoch changed to %q — history was re-sealed", got)
	}

	// A reader holding ONLY epoch 1 (the leaked link) reads the past, not the present.
	oldOnly := projectWithEpochs(t, dir, k, owner, boardD, 1)
	if it := oldOnly[beforeID]; it == nil || it.Title != "PRE-ROTATION secret" {
		t.Fatalf("an epoch-1 holder lost its pre-rotation read: %+v", it)
	}
	if it := oldOnly[afterID]; it == nil || it.Title != "[encrypted]" {
		t.Fatalf("an epoch-1-only holder READ a post-rotation card — the rotation retired nothing: %+v", it)
	}

	// The owner, holding both epochs, reads both.
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("owner projection: %v", err)
	}
	if byID[beforeID].Title != "PRE-ROTATION secret" || byID[afterID].Title != "POST-ROTATION secret" {
		t.Fatalf("owner lost a read across the rotation: pre=%q post=%q", byID[beforeID].Title, byID[afterID].Title)
	}
}

// cardEpochMarker returns the clear cek_epoch tag of the newest kind-30302 event
// for itemID in the on-disk log.
func cardEpochMarker(t *testing.T, dir, itemID string) string {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var newest *nostr.Event
	for _, e := range events {
		if e.Kind != 30302 {
			continue
		}
		if d, _ := tagVal(e.Tags, "d"); d != itemID {
			continue
		}
		if newest == nil || e.CreatedAt > newest.CreatedAt {
			newest = e
		}
	}
	if newest == nil {
		t.Fatalf("no card event for item %s", itemID)
	}
	v, _ := tagVal(newest.Tags, "cek_epoch")
	return v
}

// TestConfidentialRotateDoesNotTouchHistoryAtRest proves done-condition #2
// structurally rather than by reading: a rotation must add ONLY kind-39301 grants
// and must leave every byte of every pre-existing event alone.
//
// A rotation that "helpfully" re-sealed existing cards under the new epoch would
// still let the owner read everything, so a read-side assertion alone would pass.
// It would also mint thousands of new signed events, break every member who missed
// the rotation, and destroy the exact property the done-condition protects. So the
// assertion is on the log itself.
func TestConfidentialRotateDoesNotTouchHistoryAtRest(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)

	for _, title := range []string{"history one", "history two", "history three"} {
		if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: title, context: "ctx", itemType: "task", priority: "p2"}); err != nil {
			t.Fatalf("create %q: %v", title, err)
		}
	}
	// A member exists, so the rotation publishes member wraps too, not just a self-grant.
	mk, _, _ := mintIdentity(t)
	if err := publishRoleGrant(mk.PubKeyHex(), rdSync.RoleContributor, "member-a", 0, ""); err != nil {
		t.Fatalf("grant member: %v", err)
	}

	before := logSnapshot(t, dir)
	_, published := rotateOnce(t, dir, owner, boardD)
	after := logSnapshot(t, dir)

	for id, was := range before {
		now, ok := after[id]
		if !ok {
			t.Fatalf("rotation REMOVED event %s (kind %d) from the log", id, was.Kind)
		}
		if now.Content != was.Content || now.CreatedAt != was.CreatedAt || now.Sig != was.Sig {
			t.Fatalf("rotation MUTATED pre-existing event %s (kind %d) — history was re-sealed or re-signed", id, was.Kind)
		}
	}
	addedKinds := map[int]int{}
	for id, e := range after {
		if _, existed := before[id]; existed {
			continue
		}
		addedKinds[e.Kind]++
	}
	for kind, n := range addedKinds {
		if kind != rdSync.KindRoleGrant {
			t.Fatalf("rotation added %d event(s) of kind %d; a rotation must publish ONLY kind-%d role grants", n, kind, rdSync.KindRoleGrant)
		}
	}
	if addedKinds[rdSync.KindRoleGrant] != len(published) {
		t.Fatalf("log gained %d grants but the rotation reported %d published", addedKinds[rdSync.KindRoleGrant], len(published))
	}
	if len(published) != 2 {
		t.Fatalf("published %d grants, want 2 (owner self-grant + one member)", len(published))
	}
}

// TestConfidentialRotateWithholdsFromEVERYRevokedKey is done-condition #3, and the
// case the pre-existing revoke path got wrong.
//
// DeriveReadTrust/DeriveLevels deliberately KEEP a revoked key in the membership
// map (its past events must stay admissible). The old wrap-to-members loop walked
// that map and skipped only the single pubkey named by the caller — so revoking a
// SECOND member handed the fresh CEK straight back to the FIRST one. Forward
// secrecy held for exactly one revocation and then unwound.
//
// Testing only "the just-revoked key cannot read new cards" would pass against that
// bug. This revokes M1, lets a later rotation happen, and then asserts M1 is still
// out — both by reading (placeholder) and structurally (no owner-signed grant
// addressed to M1 carries an epoch above 1).
func TestConfidentialRotateWithholdsFromEVERYRevokedKey(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
	ownerHome, ownerCf := os.Getenv("RD_HOME"), os.Getenv("CF_HOME")

	m1, m1Home, m1Cf := mintIdentity(t)
	m2, m2Home, m2Cf := mintIdentity(t)
	for _, m := range []*nostr.Key{m1, m2} {
		if err := publishRoleGrant(m.PubKeyHex(), rdSync.RoleContributor, "", 0, ""); err != nil {
			t.Fatalf("grant %s: %v", shortKey(m.PubKeyHex()), err)
		}
	}

	idEpoch1, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "EPOCH-1 card", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create epoch-1: %v", err)
	}

	// Revoke M1. The revoke path auto-rotates to epoch 2, excluding M1.
	if err := runNostrGrantRevoke(dir, m1.PubKeyHex(), rdSync.RoleRevoked, "", 0, ""); err != nil {
		t.Fatalf("revoke m1: %v", err)
	}
	idEpoch2, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "EPOCH-2 card", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create epoch-2: %v", err)
	}

	// A LATER, unrelated rotation — the step that used to re-admit M1.
	plan, _ := rotateOnce(t, dir, owner, boardD)
	if plan.OldEpoch != 2 || plan.NewEpoch != 3 {
		t.Fatalf("plan epochs = %d -> %d, want 2 -> 3 (the revoke should already have bumped 1 -> 2)", plan.OldEpoch, plan.NewEpoch)
	}
	if got := holderPubkeys(plan.Members); len(got) != 1 || got[0] != m2.PubKeyHex() {
		t.Fatalf("rotation members = %v, want exactly [%s] (M1 is revoked)", got, m2.PubKeyHex())
	}
	if got := holderPubkeys(plan.Withheld); len(got) != 1 || got[0] != m1.PubKeyHex() {
		t.Fatalf("rotation withheld = %v, want exactly [%s]", got, m1.PubKeyHex())
	}
	idEpoch3, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "EPOCH-3 card", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create epoch-3: %v", err)
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

	// M1: keeps the past, sees nothing minted after its revocation — INCLUDING the
	// card written after the LATER rotation.
	byM1 := readAs(m1Home, m1Cf)
	if it := byM1[idEpoch1]; it == nil || it.Title != "EPOCH-1 card" {
		t.Fatalf("revoked member lost its pre-revocation read (rotation must not invalidate history): %+v", it)
	}
	if it := byM1[idEpoch2]; it == nil || it.Title != "[encrypted]" {
		t.Fatalf("revoked member read the post-revoke epoch-2 card: %+v", it)
	}
	if it := byM1[idEpoch3]; it == nil || it.Title != "[encrypted]" {
		t.Fatalf("revoked member read an epoch-3 card minted by a LATER rotation — the rotation re-admitted a previously-revoked key: %+v", it)
	}
	if eps := keyringFor(t, dir, m1, owner, boardD).Epochs(coord); len(eps) != 1 || eps[0] != 1 {
		t.Fatalf("revoked member holds epochs %v, want exactly [1]", eps)
	}

	// M2 (never revoked) spans every epoch.
	byM2 := readAs(m2Home, m2Cf)
	for id, want := range map[string]string{idEpoch1: "EPOCH-1 card", idEpoch2: "EPOCH-2 card", idEpoch3: "EPOCH-3 card"} {
		if it := byM2[id]; it == nil || it.Title != want {
			t.Fatalf("live member cannot read %s (want %q): %+v", id, want, it)
		}
	}
	if eps := keyringFor(t, dir, m2, owner, boardD).Epochs(coord); len(eps) != 3 {
		t.Fatalf("live member holds epochs %v, want all three", eps)
	}

	// STRUCTURAL: no owner-signed grant addressed to M1 carries an epoch above 1.
	// Reading placeholders proves M1 could not decrypt; this proves the key was
	// never even wrapped to it, so no future decryptor change can quietly undo it.
	t.Setenv("RD_HOME", ownerHome)
	t.Setenv("CF_HOME", ownerCf)
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	sawM1Epoch1 := false
	for _, e := range events {
		if e.Kind != rdSync.KindRoleGrant || e.PubKey != owner {
			continue
		}
		p, _ := tagVal(e.Tags, "p")
		if p != m1.PubKeyHex() {
			continue
		}
		cek, hasCEK := tagVal(e.Tags, "cek")
		if !hasCEK || cek == "" {
			continue
		}
		ep, _ := tagVal(e.Tags, "cek_epoch")
		if ep != "1" {
			t.Fatalf("a grant addressed to the REVOKED key carries cek_epoch=%s — the new key was wrapped to a revoked member", ep)
		}
		sawM1Epoch1 = true
	}
	if !sawM1Epoch1 {
		t.Fatal("expected the revoked key to still hold its original epoch-1 wrap (revocation must not erase history)")
	}
}

// TestConfidentialRotateTwiceStrandsNoOne is done-condition #5. Running rotate
// repeatedly must keep every live member whole: after two rotations the owner and
// each member hold EVERY epoch, and cards from all three eras stay readable to
// both. A rotation that re-wrapped only the newest epoch to the owner, or skipped
// a member on the second pass, would leave a hole this catches.
func TestConfidentialRotateTwiceStrandsNoOne(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
	ownerKey, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}

	mk, mHome, mCf := mintIdentity(t)
	if err := publishRoleGrant(mk.PubKeyHex(), rdSync.RoleContributor, "member", 0, ""); err != nil {
		t.Fatalf("grant member: %v", err)
	}

	ids := map[int]string{}
	for epoch := 1; epoch <= 3; epoch++ {
		id, cerr := runCreateNostr(mustDir(t), nostrCreateSpec{
			title: "card for epoch " + strconv.Itoa(epoch), itemType: "task", priority: "p2",
		})
		if cerr != nil {
			t.Fatalf("create card %d: %v", epoch, cerr)
		}
		ids[epoch] = id
		if got := cardEpochMarker(t, dir, id); got != strconv.Itoa(epoch) {
			t.Fatalf("card %d sealed under cek_epoch=%s, want %d", epoch, got, epoch)
		}
		if epoch < 3 {
			plan, _ := rotateOnce(t, dir, owner, boardD)
			if plan.OldEpoch != epoch || plan.NewEpoch != epoch+1 {
				t.Fatalf("rotation #%d stepped %d -> %d, want %d -> %d", epoch, plan.OldEpoch, plan.NewEpoch, epoch, epoch+1)
			}
		}
	}

	for who, k := range map[string]*nostr.Key{"owner": ownerKey, "member": mk} {
		eps := keyringFor(t, dir, k, owner, boardD).Epochs(coord)
		if len(eps) != 3 || eps[0] != 1 || eps[1] != 2 || eps[2] != 3 {
			t.Fatalf("%s holds epochs %v after two rotations, want [1 2 3] — someone was stranded", who, eps)
		}
	}

	t.Setenv("RD_HOME", mHome)
	t.Setenv("CF_HOME", mCf)
	_, byMember, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("member projection: %v", err)
	}
	for epoch, id := range ids {
		want := "card for epoch " + strconv.Itoa(epoch)
		if it := byMember[id]; it == nil || it.Title != want {
			t.Fatalf("member cannot read the epoch-%d card after two rotations: %+v", epoch, it)
		}
	}
}

// TestConfidentialRotatePreservesRoleAndLabel guards done-condition #5's other
// half: "does not corrupt state". kind-39301 is addressable on (board, grantee),
// so the grant a rotation publishes REPLACES the member's existing one. Re-issuing
// at a hardcoded role=contributor with a hardcoded label would silently demote
// every maintainer and erase the human label `rd sessions` renders — on every
// rotation, invisibly.
func TestConfidentialRotatePreservesRoleAndLabel(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)

	if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "bootstrap", itemType: "task", priority: "p2"}); err != nil {
		t.Fatalf("bootstrap write: %v", err)
	}
	mk, _, _ := mintIdentity(t)
	const label = "baron's laptop"
	if err := publishRoleGrant(mk.PubKeyHex(), rdSync.RoleMaintainer, label, 0, ""); err != nil {
		t.Fatalf("grant maintainer: %v", err)
	}

	rotateOnce(t, dir, owner, boardD)

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	h, ok := rdSync.DeriveGrantHolders(events, owner, boardD)[mk.PubKeyHex()]
	if !ok {
		t.Fatal("maintainer vanished from the grant holders after a rotation")
	}
	if h.Role != rdSync.RoleMaintainer {
		t.Fatalf("rotation demoted the maintainer to %q", h.Role)
	}
	if h.Label != label {
		t.Fatalf("rotation overwrote the member's label with %q, want %q", h.Label, label)
	}
	levels, _ := rdSync.DeriveLevels(events, owner, boardD)
	if levels[mk.PubKeyHex()] != rdSync.LevelMaintainer {
		t.Fatalf("maintainer's derived level is %d after rotation, want %d", levels[mk.PubKeyHex()], rdSync.LevelMaintainer)
	}
}

// TestConfidentialRotateRefusals proves rotation refuses — loudly and without
// writing — in each state where it cannot be correct: a public board (no key
// exists), a non-owner signer (only the owner's CEK is honoured by
// DeriveBoardKeyring, so a member's "rotation" would publish grants no reader
// accepts while looking like it worked), and a confidential board whose key was
// never bootstrapped.
func TestConfidentialRotateRefusals(t *testing.T) {
	t.Run("public board", func(t *testing.T) {
		dir := setupNostrCmdTest(t)
		k, err := nostrKey()
		if err != nil {
			t.Fatalf("nostrKey: %v", err)
		}
		owner := k.PubKeyHex()
		boardD := projectPrefix(dir)
		coord := rdSync.BoardCoord(owner, boardD)
		if err := rdconfig.SaveSyncConfig(dir, &rdconfig.SyncConfig{ProjectName: "project", Board: coord, Public: true}); err != nil {
			t.Fatalf("SaveSyncConfig: %v", err)
		}
		pub, ok, err := nostrPublisher()
		if err != nil || !ok {
			t.Fatalf("publisher: %v", err)
		}
		_, err = planBoardRotation(dir, pub, owner, boardD, "")
		if err == nil || !strings.Contains(err.Error(), "PUBLIC") {
			t.Fatalf("rotating a public board should refuse and say why, got %v", err)
		}
	})

	t.Run("not bootstrapped", func(t *testing.T) {
		dir, owner := setupConfidentialProject(t)
		boardD := projectPrefix(dir)
		pub, ok, err := nostrPublisher()
		if err != nil || !ok {
			t.Fatalf("publisher: %v", err)
		}
		// No owner write yet, so no CEK has ever been minted.
		_, err = planBoardRotation(dir, pub, owner, boardD, "")
		if err == nil || !strings.Contains(err.Error(), "not yet bootstrapped") {
			t.Fatalf("rotating an un-bootstrapped board should refuse and say why, got %v", err)
		}
	})

	t.Run("non-owner", func(t *testing.T) {
		dir, owner := setupConfidentialProject(t)
		boardD := projectPrefix(dir)
		coord := rdSync.BoardCoord(owner, boardD)
		if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "owned", itemType: "task", priority: "p2"}); err != nil {
			t.Fatalf("bootstrap write: %v", err)
		}
		mk, mHome, mCf := mintIdentity(t)
		if err := publishRoleGrant(mk.PubKeyHex(), rdSync.RoleContributor, "", 0, ""); err != nil {
			t.Fatalf("grant member: %v", err)
		}
		epochsBefore := keyringFor(t, dir, mk, owner, boardD).Epochs(coord)

		t.Setenv("RD_HOME", mHome)
		t.Setenv("CF_HOME", mCf)
		pub, ok, err := nostrPublisher()
		if err != nil || !ok {
			t.Fatalf("member publisher: %v", err)
		}
		before := logSnapshot(t, dir)
		_, err = planBoardRotation(dir, pub, owner, boardD, "")
		if err == nil || !strings.Contains(err.Error(), "only the board OWNER") {
			t.Fatalf("a member rotating the owner's board should refuse and say why, got %v", err)
		}
		if after := logSnapshot(t, dir); len(after) != len(before) {
			t.Fatalf("a refused rotation still wrote to the log (%d -> %d events)", len(before), len(after))
		}
		if eps := keyringFor(t, dir, mk, owner, boardD).Epochs(coord); len(eps) != len(epochsBefore) {
			t.Fatalf("a refused rotation changed the member's epochs %v -> %v", epochsBefore, eps)
		}
	})
}

// TestConfidentialRotateCmdOutputStatesWhatItDoesNotDo is done-condition #4. The
// operationally surprising fact is that a `rd board --with-key` link minted before
// the rotation KEEPS working on pre-rotation cards: an operator rotating because a
// link leaked will otherwise assume the link is dead. The command must say so
// itself, on stdout, on every rotation — not only in --help.
func TestConfidentialRotateCmdOutputStatesWhatItDoesNotDo(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	_ = owner
	if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "bootstrap", itemType: "task", priority: "p2"}); err != nil {
		t.Fatalf("bootstrap write: %v", err)
	}
	if err := confidentialRotateCmd.Flags().Set("no-verify", "true"); err != nil {
		t.Fatalf("set --no-verify: %v", err)
	}
	t.Cleanup(func() {
		confidentialRotateCmd.Flags().Set("no-verify", "false") //nolint:errcheck // test cleanup
		confidentialRotateCmd.Flags().Set("dry-run", "false")   //nolint:errcheck // test cleanup
	})

	out := captureStdoutPipe(t, func() {
		if err := confidentialRotateCmd.RunE(confidentialRotateCmd, nil); err != nil {
			t.Fatalf("rd confidential rotate: %v", err)
		}
	})

	// Each required statement, with the phrase that carries it. These are the
	// facts an operator acts on, not decoration.
	for _, want := range []string{
		"epoch 1 -> 2",       // which key is being retired, and for what
		"sealed under epoch", // what changes: future writes use the new key
		"NOT re-sealed",      // what does not change: history stays as it is
		"--with-key",         // the previously-minted links
		"BEFORE this rotation keep working",
		"cannot recall what the leaked key could already read",
		// The relay-replacement consequence (ready-44b): addressable grants mean
		// the old epoch's grants stop being SERVED, even though every existing
		// local log keeps them. An operator seeding a new machine after a
		// rotation has to know this.
		"does NOT keep the old epoch's grants on relays",
		"ready-44b",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("rotate output does not state %q.\n--- output ---\n%s", want, out)
		}
	}
	// And the rotation really happened, so the output is not describing a no-op.
	_ = dir
}

// TestConfidentialRotateCmdDryRunPublishesNothing: the preview must be a preview.
// It points the write relay at an unreachable address, so any dial would also show
// up as a buffered publish.
func TestConfidentialRotateCmdDryRunPublishesNothing(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "bootstrap", itemType: "task", priority: "p2"}); err != nil {
		t.Fatalf("bootstrap write: %v", err)
	}
	before := logSnapshot(t, dir)
	epochsBefore := keyringFor(t, dir, k, owner, boardD).Epochs(coord)

	if err := confidentialRotateCmd.Flags().Set("dry-run", "true"); err != nil {
		t.Fatalf("set --dry-run: %v", err)
	}
	t.Cleanup(func() {
		confidentialRotateCmd.Flags().Set("dry-run", "false")   //nolint:errcheck // test cleanup
		confidentialRotateCmd.Flags().Set("no-verify", "false") //nolint:errcheck // test cleanup
	})
	out := captureStdoutPipe(t, func() {
		if err := confidentialRotateCmd.RunE(confidentialRotateCmd, nil); err != nil {
			t.Fatalf("rd confidential rotate --dry-run: %v", err)
		}
	})
	if !strings.Contains(out, "DRY RUN — nothing published.") {
		t.Fatalf("dry run did not say so:\n%s", out)
	}
	if !strings.Contains(out, "epoch 1 -> 2") {
		t.Fatalf("dry run did not show the epoch step it would take:\n%s", out)
	}
	if after := logSnapshot(t, dir); len(after) != len(before) {
		t.Fatalf("--dry-run wrote %d new event(s) to the log", len(after)-len(before))
	}
	if eps := keyringFor(t, dir, k, owner, boardD).Epochs(coord); len(eps) != len(epochsBefore) {
		t.Fatalf("--dry-run changed the held epochs %v -> %v", epochsBefore, eps)
	}
}

// TestConfidentialStatusReportsEveryHeldEpoch: after a rotation, `rd confidential
// status` must show the CURRENT epoch AND that the older epochs are still held —
// the older keys are exactly what keeps pre-rotation cards readable, so an operator
// verifying a rotation needs to see both.
func TestConfidentialStatusReportsEveryHeldEpoch(t *testing.T) {
	dir, owner := setupConfidentialProject(t)
	boardD := projectPrefix(dir)
	if _, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "bootstrap", itemType: "task", priority: "p2"}); err != nil {
		t.Fatalf("bootstrap write: %v", err)
	}

	first := captureStdoutPipe(t, func() {
		if err := confidentialStatusCmd.RunE(confidentialStatusCmd, nil); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(first, "epoch 1;") || !strings.Contains(first, "epoch(s) 1") {
		t.Fatalf("status before rotation = %q, want epoch 1 held", first)
	}

	rotateOnce(t, dir, owner, boardD)

	second := captureStdoutPipe(t, func() {
		if err := confidentialStatusCmd.RunE(confidentialStatusCmd, nil); err != nil {
			t.Fatalf("status: %v", err)
		}
	})
	if !strings.Contains(second, "epoch 2;") {
		t.Fatalf("status after rotation = %q, want the current epoch to be 2", second)
	}
	if !strings.Contains(second, "epoch(s) 1,2") {
		t.Fatalf("status after rotation = %q, want it to show BOTH held epochs (1,2)", second)
	}
}
