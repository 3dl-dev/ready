package main

// Re-sealing an already-published PLAINTEXT card in place (ready-a43, epic ready-336).
//
// WHAT IS EASY TO FAKE HERE, and therefore what these tests refuse to accept as
// evidence:
//
//   - "the owner reads the item fine afterwards" — true of doing nothing at all.
//     The whole point is what a STRANGER reading the relay gets, so the assertions
//     are on the event a relay would serve at the coordinate, not on rd's own view.
//   - "a sealed card exists for that item" — true of a replacement that LOSES the
//     latest-wins tie-break and is therefore never served (ready-500 shipped exactly
//     that once). So the winner at the coordinate is recomputed by an oracle local to
//     this file, independently of the production helper, and the ordering is asserted
//     strictly.
//   - "the plaintext is gone" — must NOT be true of the local log. Re-sealing is an
//     addressable REPLACEMENT, not a delete; the append-only log has to keep both
//     copies byte-for-byte or the operation destroyed history instead of hiding it.
//
// The rotation guard (TestConfidentialRotateDoesNotTouchHistoryAtRest) is deliberately
// untouched by any of this: re-sealing is a separate verb with a separate entry point,
// and both tests run in this same package on every `go test ./cmd/rd/`.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/rdconfig"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// coordinateWinner is this file's INDEPENDENT oracle for "which kind-30302 event
// does a NIP-01 relay retain at (30302, author, itemID)". It re-derives the
// replaceable-event rule — greatest created_at, ties broken by lowest event id —
// rather than calling rdSync.WinningCardEvent, so a test asserting "the sealed card
// won" cannot be satisfied by a production helper that got the rule wrong.
//
// It keys on the event AUTHOR as well as the d tag, because that pair (plus kind) IS
// the addressable coordinate; two authors writing the same item id occupy two slots.
func coordinateWinner(t *testing.T, dir, author, itemID string) *nostr.Event {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	var winner *nostr.Event
	for _, e := range events {
		if e.Kind != 30302 || e.PubKey != author {
			continue
		}
		if d, _ := tagVal(e.Tags, "d"); d != itemID {
			continue
		}
		if winner == nil || e.CreatedAt > winner.CreatedAt ||
			(e.CreatedAt == winner.CreatedAt && e.ID < winner.ID) {
			winner = e
		}
	}
	if winner == nil {
		t.Fatalf("no kind-30302 event authored by %s for item %s", shortKey(author), itemID)
	}
	return winner
}

// setupMixedConfidentialProject reproduces the exact production shape this whole
// epic exists for: a board that was PUBLIC while items were written, then flipped to
// confidential. The pre-cutover cards are grandfathered plaintext — readable by the
// tool AND by anyone with a relay connection — while everything written afterwards
// is sealed. It mirrors TestConfidentialEnableMigration's setup (confidential_test.go).
func setupMixedConfidentialProject(t *testing.T) (dir, owner, boardD string) {
	t.Helper()
	dir = setupNostrCmdTest(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	owner = k.PubKeyHex()
	boardD = projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)
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
	return dir, owner, boardD
}

// enableConfidential flips the project to confidential and bootstraps the board CEK,
// exactly as `rd confidential enable` does (confidential_cmd.go).
func enableConfidential(t *testing.T, dir, owner, boardD string) {
	t.Helper()
	cfg, err := rdconfig.LoadSyncConfig(dir)
	if err != nil {
		t.Fatalf("LoadSyncConfig: %v", err)
	}
	cfg.Public = false
	if err := rdconfig.SaveSyncConfig(dir, cfg); err != nil {
		t.Fatalf("SaveSyncConfig: %v", err)
	}
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	if _, err := boardConfidentialEnvelope(dir, pub, owner, boardD); err != nil {
		t.Fatalf("bootstrap CEK: %v", err)
	}
}

// resealOne runs the entry point for one item, resolving the item through the REAL
// projection first (as any caller must), with no relay observation — the
// single-machine case.
func resealOne(t *testing.T, dir, owner, boardD, itemID string) (*resealOutcome, error) {
	t.Helper()
	return resealOneWith(t, dir, owner, boardD, itemID, resealOptions{})
}

// resealOneWith is resealOne with the caller's relay observation supplied.
func resealOneWith(t *testing.T, dir, owner, boardD, itemID string, opts resealOptions) (*resealOutcome, error) {
	t.Helper()
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	item := byID[itemID]
	if item == nil {
		t.Fatalf("item %s not in the projection", itemID)
	}
	return resealCard(dir, pub, owner, boardD, item, opts)
}

// TestResealSupersedesThePlaintextCardAtItsCoordinate is done-condition (a) and (b).
//
// (a) the published plaintext card is REPLACED, at the same addressable coordinate,
// by a sealed card the fold resolves as the winner — asserted on the event a relay
// would serve, on the strict created_at ordering that makes the replacement effective
// rather than a no-op, and on what a keyless reader can now see (nothing) versus
// what it could see before (everything).
//
// (b) the ORIGINAL survives in the local append-only log, byte-identical. This is the
// half that separates "hidden from strangers" from "destroyed", and it is what makes
// the operation defensible at all.
func TestResealSupersedesThePlaintextCardAtItsCoordinate(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}

	const (
		title  = "PLAINTEXT acquisition terms with Contoso"
		ctxTxt = "offer is 4.2M, walk-away 3.1M; do not forward"
	)
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: title, context: ctxTxt, itemType: "task", priority: "p1", labels: []string{"deal"},
	})
	if err != nil {
		t.Fatalf("create plaintext item: %v", err)
	}
	enableConfidential(t, dir, owner, boardD)

	// BEFORE: the card a relay serves at this coordinate is in the clear — the exact
	// defect. A reader holding NO CEK reads every word of it through the real fold,
	// because a pre-cutover plaintext card is grandfathered.
	original := coordinateWinner(t, dir, owner, id)
	if _, sealed := tagVal(original.Tags, "enc"); sealed {
		t.Fatal("setup is wrong: the pre-cutover card is already sealed, so there is nothing to re-seal")
	}
	if got, _ := tagVal(original.Tags, "title"); got != title {
		t.Fatalf("pre-cutover card title tag = %q, want the cleartext %q", got, title)
	}
	if !strings.Contains(original.Content, ctxTxt) {
		t.Fatalf("pre-cutover card content does not carry the cleartext description: %q", original.Content)
	}
	if it := projectWithEpochs(t, dir, k, owner, boardD)[id]; it == nil || it.Title != title {
		t.Fatalf("setup is wrong: a keyless reader should already read this card in the clear, got %+v", it)
	}

	before := logSnapshot(t, dir)
	_, beforeByID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project before: %v", err)
	}
	historyBefore := len(beforeByID[id].History)

	out, err := resealOne(t, dir, owner, boardD, id)
	if err != nil {
		t.Fatalf("resealCard: %v", err)
	}

	// --- (a) the coordinate now serves a SEALED card, and it genuinely wins. -------
	if out.OriginalEventID != original.ID {
		t.Fatalf("outcome names original %s, but the coordinate held %s", out.OriginalEventID, original.ID)
	}
	winner := coordinateWinner(t, dir, owner, id)
	if winner.ID != out.SealedEventID {
		t.Fatalf("the coordinate's winner is %s, but the re-seal published %s — the replacement LOST the latest-wins order and the relay still serves the plaintext",
			shortID(winner.ID), shortID(out.SealedEventID))
	}
	if winner.CreatedAt <= original.CreatedAt {
		t.Fatalf("sealed replacement created_at %d does not strictly exceed the original's %d — a tie is decided by lowest event id, so this is a coin-flip no-op (ready-500)",
			winner.CreatedAt, original.CreatedAt)
	}
	if winner.PubKey != original.PubKey {
		t.Fatalf("sealed card is authored by %s but the plaintext one by %s — kind-30302 is addressable on (kind, AUTHOR, d), so that is a different coordinate and evicts nothing",
			shortKey(winner.PubKey), shortKey(original.PubKey))
	}
	if v, ok := tagVal(winner.Tags, "enc"); !ok || v != "1" {
		t.Fatalf("winning card carries enc=%q (present=%v), want \"1\"", v, ok)
	}
	if _, ok := tagVal(winner.Tags, "title"); ok {
		t.Fatal("winning card still carries a CLEAR title tag — the title is exactly what must stop being world-readable")
	}
	if strings.Contains(winner.Content, ctxTxt) || strings.Contains(winner.Content, title) {
		t.Fatalf("winning card's content still contains the cleartext free text: %q", winner.Content)
	}
	// The observable form of the property: a reader holding no CEK can no longer read
	// this card, through the real fold gate. Before the re-seal (asserted above) it could.
	if it := projectWithEpochs(t, dir, k, owner, boardD)[id]; it == nil || it.Title != "[encrypted]" {
		t.Fatalf("a keyless reader still reads the re-sealed card: %+v", it)
	}

	// --- the owner loses nothing: same item, same fields, no fabricated history. ---
	_, afterByID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project after: %v", err)
	}
	it := afterByID[id]
	if it == nil {
		t.Fatal("the item vanished from the owner's projection after re-sealing")
	}
	if it.Title != title {
		t.Fatalf("owner reads title %q, want %q", it.Title, title)
	}
	if it.Context != ctxTxt {
		t.Fatalf("owner reads context %q, want %q", it.Context, ctxTxt)
	}
	if it.Status != beforeByID[id].Status || it.Priority != "p1" || it.Type != "task" {
		t.Fatalf("re-seal changed item state: status=%q (was %q) priority=%q type=%q", it.Status, beforeByID[id].Status, it.Priority, it.Type)
	}
	if len(it.Labels) != 1 || it.Labels[0] != "deal" {
		t.Fatalf("re-seal lost the labels: %v", it.Labels)
	}
	if len(it.History) != historyBefore {
		t.Fatalf("re-seal changed history from %d to %d entries — it must publish a card and nothing else", historyBefore, len(it.History))
	}
	if out.Epoch != 1 {
		t.Fatalf("sealed under epoch %d, want the board's current epoch 1", out.Epoch)
	}
	if out.RelayRejected {
		t.Fatal("the sealed card was reported oversized; this fixture is far below 64 KiB")
	}

	// --- (b) the append-only log kept the original, byte-identical. ---------------
	after := logSnapshot(t, dir)
	for id0, was := range before {
		now, ok := after[id0]
		if !ok {
			t.Fatalf("re-seal REMOVED event %s (kind %d) from the append-only log", id0, was.Kind)
		}
		if now.Content != was.Content || now.CreatedAt != was.CreatedAt || now.Sig != was.Sig {
			t.Fatalf("re-seal MUTATED pre-existing event %s (kind %d) — the local log must retain the original verbatim", id0, was.Kind)
		}
	}
	kept, ok := after[original.ID]
	if !ok {
		t.Fatal("the ORIGINAL plaintext card is gone from the local log — a re-seal is an addressable replacement, not a delete")
	}
	if got, _ := tagVal(kept.Tags, "title"); got != title {
		t.Fatalf("the retained original no longer carries its cleartext title tag (%q) — history was rewritten, not superseded", got)
	}
	// Exactly one event was added, and it is the sealed card.
	var added []*nostr.Event
	for id0, e := range after {
		if _, existed := before[id0]; !existed {
			added = append(added, e)
		}
	}
	if len(added) != 1 {
		t.Fatalf("re-seal added %d events, want exactly 1 (the sealed card)", len(added))
	}
	if added[0].Kind != 30302 || added[0].ID != out.SealedEventID {
		t.Fatalf("re-seal added kind %d id %s, want the kind-30302 sealed card %s", added[0].Kind, shortID(added[0].ID), shortID(out.SealedEventID))
	}
}

// TestResealPreservesEveryClearRoutingTag guards the half of the claim that is easy
// to assert loosely and hard to assert honestly: "the owner loses nothing".
//
// A re-seal rebuilds the card from the PROJECTED item (CardSpecFromItem), so any tag
// the original carried that does not survive the card -> item -> card round trip is
// silently dropped at the moment the plaintext copy stops being reachable. Checking a
// handful of named fields cannot catch that; it only catches the fields someone
// thought of. So this compares the FULL clear tag multiset of the original against the
// replacement's, and allows a difference ONLY where sealing is defined to make one
// (BuildCardEvent, pkg/sync/nostrwire.go): `title`, `waiting_on` and `l` are the free
// text that moves into the sealed blob, and `enc`/`cek_epoch` are the markers that
// arrive. A label may come back as a clear `l` HMAC TOKEN when the board has an LTK —
// so this also asserts no `l` tag ever carries the readable label value, which is the
// leak the tokenization exists to avoid.
func TestResealPreservesEveryClearRoutingTag(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t)
	parent, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "parent", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create parent: %v", err)
	}
	blocker, err := runCreateNostr(mustDir(t), nostrCreateSpec{title: "blocker", itemType: "task", priority: "p1"})
	if err != nil {
		t.Fatalf("create blocker: %v", err)
	}
	// A card carrying as much routing surface as the shape supports: parent, deps,
	// labels, eta, level, assignee, priority, type.
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "richly tagged", context: "body", itemType: "review", priority: "p0",
		labels: []string{"deal", "security"}, parentID: parent,
		level: "subtask", eta: "2026-09-01T00:00:00Z",
	})
	if err != nil {
		t.Fatalf("create rich item: %v", err)
	}
	if err := runDepAddNostr(id, blocker); err != nil {
		t.Fatalf("dep add: %v", err)
	}
	enableConfidential(t, dir, owner, boardD)

	original := coordinateWinner(t, dir, owner, id)
	out, err := resealOne(t, dir, owner, boardD, id)
	if err != nil {
		t.Fatalf("resealCard: %v", err)
	}
	sealed := coordinateWinner(t, dir, owner, id)
	if sealed.ID != out.SealedEventID {
		t.Fatalf("winner %s is not the sealed card %s", shortID(sealed.ID), shortID(out.SealedEventID))
	}

	count := func(e *nostr.Event) map[string]int {
		m := map[string]int{}
		for _, tg := range e.Tags {
			if len(tg) < 2 {
				continue
			}
			m[tg[0]+"="+tg[1]]++
		}
		return m
	}
	// The free text sealing is DEFINED to move out of the clear. Everything else must
	// survive byte-identically.
	sealedAway := func(k string) bool {
		return strings.HasPrefix(k, "title=") || strings.HasPrefix(k, "waiting_on=") || strings.HasPrefix(k, "l=")
	}
	was, now := count(original), count(sealed)
	for k, n := range was {
		if sealedAway(k) {
			if now[k] > 0 {
				t.Fatalf("the sealed card still carries the clear free-text tag %q — that value is exactly what must stop being world-readable", k)
			}
			continue
		}
		if now[k] != n {
			t.Fatalf("re-seal dropped or changed clear tag %q: %d on the original, %d on the replacement — the plaintext copy stops being served, so anything lost here is lost", k, n, now[k])
		}
	}
	for k, n := range now {
		if strings.HasPrefix(k, "enc=") || strings.HasPrefix(k, "cek_epoch=") {
			continue
		}
		// A clear `l` on a sealed card is only legal as an opaque token.
		if strings.HasPrefix(k, "l=") {
			for _, label := range []string{"deal", "security"} {
				if k == "l="+label {
					t.Fatalf("the sealed card leaks the readable label in a clear tag %q", k)
				}
			}
			continue
		}
		if was[k] != n {
			t.Fatalf("re-seal INVENTED clear tag %q (%d on the replacement, %d on the original)", k, n, was[k])
		}
	}
	// The owner still reads both labels back, so nothing was lost — only moved.
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project after: %v", err)
	}
	if got := byID[id]; got == nil || len(got.Labels) != 2 {
		t.Fatalf("the owner lost the labels the re-seal moved into the sealed blob: %+v", got)
	}
}

// TestResealOutranksTheCardTheRelayServes is the ready-500 trap in the form that can
// actually still fire, and the reason resealOptions exists.
//
// The ordering decision is made against the LOCAL log, but the operation is defined by
// what a RELAY serves. Those disagree whenever another machine wrote a newer card at
// this coordinate and this one has not synced: the local floor is satisfied, the
// publish succeeds, every local signal says sealed — and the relay keeps serving a
// card that outranks the replacement. Nothing in the local log can detect it, so the
// caller passes in what it observed.
//
// Flooring against the local original alone is UNREACHABLE code (a card's DriftScope
// is "item:"+d, so nostrNextCreatedAt has already floored above it): delete that floor
// and every other test here still passes. This one goes red, which is what makes the
// guard real rather than decorative.
func TestResealOutranksTheCardTheRelayServes(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t)
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "plaintext, and newer somewhere else", context: "written on another machine since", itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create plaintext item: %v", err)
	}
	enableConfidential(t, dir, owner, boardD)

	local := coordinateWinner(t, dir, owner, id)
	// What the relay serves: a copy stamped well AHEAD of anything in this log and of
	// this machine's clock. An hour is far outside any tie-break window.
	relayCreatedAt := time.Now().Unix() + 3600
	if relayCreatedAt <= local.CreatedAt {
		t.Fatalf("fixture is wrong: relay copy %d must be ahead of the local winner %d", relayCreatedAt, local.CreatedAt)
	}

	out, err := resealOneWith(t, dir, owner, boardD, id, resealOptions{RelayCardCreatedAt: relayCreatedAt})
	if err != nil {
		t.Fatalf("resealCard: %v", err)
	}
	if out.SealedCreatedAt <= relayCreatedAt {
		t.Fatalf("sealed replacement stamped %d, which does NOT outrank the card the relay serves at %d — the publish succeeds and the plaintext keeps being served (ready-500)",
			out.SealedCreatedAt, relayCreatedAt)
	}
	if !out.RelayFloorObserved || out.SupersededRelayCreatedAt != relayCreatedAt {
		t.Fatalf("outcome records observed=%v at %d, want true at %d — a sweep cannot tell a verified coordinate from an unverified one",
			out.RelayFloorObserved, out.SupersededRelayCreatedAt, relayCreatedAt)
	}
	// The local winner must still be the sealed card: outranking the relay copy must
	// not come at the cost of losing at home.
	if w := coordinateWinner(t, dir, owner, id); w.ID != out.SealedEventID {
		t.Fatalf("local coordinate winner is %s, want the sealed card %s", shortID(w.ID), shortID(out.SealedEventID))
	}
}

// TestResealWithoutARelayObservationSaysSo is the other half: not looking is allowed
// (a single-machine re-seal is the common case) but must never be indistinguishable
// from having looked. A board-wide pass that reported "sealed" for a coordinate it
// never read back off the relay is exactly the false-completion this epic cannot
// afford — ready-336's data-plane pass has to verify off the relay, not off the log.
func TestResealWithoutARelayObservationSaysSo(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t)
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "plaintext, single machine", context: "nothing else wrote this", itemType: "task", priority: "p2",
	})
	if err != nil {
		t.Fatalf("create plaintext item: %v", err)
	}
	enableConfidential(t, dir, owner, boardD)

	out, err := resealOne(t, dir, owner, boardD, id)
	if err != nil {
		t.Fatalf("resealCard: %v", err)
	}
	if out.RelayFloorObserved || out.SupersededRelayCreatedAt != 0 {
		t.Fatalf("outcome claims a relay observation (observed=%v at %d) the caller never made",
			out.RelayFloorObserved, out.SupersededRelayCreatedAt)
	}
	if w := coordinateWinner(t, dir, owner, id); w.ID != out.SealedEventID {
		t.Fatalf("local coordinate winner is %s, want the sealed card %s", shortID(w.ID), shortID(out.SealedEventID))
	}
}

// TestResealRefusesAnAlreadySealedCard keeps a board-wide pass CONVERGENT.
//
// Re-sealing mints a new event id every time it runs. If "already sealed" were
// treated as "seal it again", a sweep over a mixed board would publish a fresh signed
// event for every already-sealed coordinate on every run, forever, and could never
// report how much work was left. planEpochRepublish (confidential_republish.go) needed
// the same guard for the same reason.
func TestResealRefusesAnAlreadySealedCard(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t)
	enableConfidential(t, dir, owner, boardD)

	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "born sealed", context: "post-cutover", itemType: "task", priority: "p2",
	})
	if err != nil {
		t.Fatalf("create sealed item: %v", err)
	}
	before := logSnapshot(t, dir)

	_, err = resealOne(t, dir, owner, boardD, id)
	if !errors.Is(err, errCardAlreadySealed) {
		t.Fatalf("re-sealing an already-sealed card returned %v, want errCardAlreadySealed", err)
	}
	if after := logSnapshot(t, dir); len(after) != len(before) {
		t.Fatalf("a refused re-seal still wrote to the log: %d events -> %d", len(before), len(after))
	}
}

// TestResealRefusesACardSignedByAnotherKey is the trap that would make this whole
// feature a lie on a multi-writer board.
//
// A card's addressable coordinate is (kind, EVENT AUTHOR, d). A contributor's
// plaintext card sits at the CONTRIBUTOR's coordinate. If the owner published a sealed
// card for the same item id, it would land at the OWNER's coordinate: rd's own fold
// would show the sealed one (later created_at wins across authors, so every local
// signal reads "sealed"), while the relay would keep serving the contributor's
// plaintext card to anyone who asks — forever, and invisibly.
//
// So this asserts the refusal AND, to prove the premise rather than assume it, that
// the contributor's plaintext card is still the winner at the contributor's own
// coordinate afterwards.
func TestResealRefusesACardSignedByAnotherKey(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t)
	ownerHome, ownerCf := os.Getenv("RD_HOME"), os.Getenv("CF_HOME")

	// A contributor with its own identity and its own $RD_HOME, writing into the
	// same project — i.e. a second machine on this board.
	ck, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	contributor := ck.PubKeyHex()
	cHome := t.TempDir()
	if err := nostr.WriteKeyFileExclusive(filepath.Join(cHome, "nostr-identity.json"), ck, cHome); err != nil {
		t.Fatalf("persist contributor key: %v", err)
	}
	if _, err := publishRoleGrant(contributor, rdSync.RoleContributor, "contrib", 0, ""); err != nil {
		t.Fatalf("grant contributor: %v", err)
	}

	t.Setenv("RD_HOME", cHome)
	t.Setenv("CF_HOME", t.TempDir())
	const cTitle = "PLAINTEXT written by the contributor"
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: cTitle, context: "signed by a key that is not the owner", itemType: "task", priority: "p2",
	})
	if err != nil {
		t.Fatalf("contributor create: %v", err)
	}

	t.Setenv("RD_HOME", ownerHome)
	t.Setenv("CF_HOME", ownerCf)
	enableConfidential(t, dir, owner, boardD)

	// Premise: the plaintext card really is at the CONTRIBUTOR's coordinate.
	original := coordinateWinner(t, dir, contributor, id)
	if got, _ := tagVal(original.Tags, "title"); got != cTitle {
		t.Fatalf("contributor's card title tag = %q, want %q", got, cTitle)
	}

	before := logSnapshot(t, dir)
	out, err := resealOne(t, dir, owner, boardD, id)
	if !errors.Is(err, errCardForeignAuthor) {
		t.Fatalf("owner re-sealing a contributor-authored card returned (%v, %v), want errCardForeignAuthor — publishing here would create a SECOND coordinate and leave the contributor's plaintext card served",
			out, err)
	}
	if after := logSnapshot(t, dir); len(after) != len(before) {
		t.Fatalf("a refused re-seal still wrote to the log: %d events -> %d", len(before), len(after))
	}
	if still := coordinateWinner(t, dir, contributor, id); still.ID != original.ID {
		t.Fatalf("the contributor's coordinate changed from %s to %s despite the refusal", shortID(original.ID), shortID(still.ID))
	}
}

// TestResealRefusesAnUnreadableItem is the ready-76b interlock, restated for this
// entry point. An item whose free text this machine could not decrypt projects with
// the literal "[encrypted]" placeholder in Title/Context/Description; re-sealing it
// would seal that placeholder AS the item's content at a LATER created_at, so the
// placeholder would win latest-wins and the real content would be unrecoverable from
// the projection. That is not hypothetical — it destroyed four items on the ready
// board. A re-seal is precisely a read-then-republish of a projected item, so it is
// squarely in the class of writes that guard exists for.
//
// The item handed in is a REAL projected item with a REAL re-sealable plaintext card
// at the owner's coordinate, carrying exactly the field shape itemFromCard writes when
// decryption fails (pkg/sync/nostrproject.go). So dropping the guard does not fall
// through to some other refusal: it succeeds, and seals the placeholder.
func TestResealRefusesAnUnreadableItem(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t)
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "real content that must survive", context: "and its real description", itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	enableConfidential(t, dir, owner, boardD)
	pub, ok, err := nostrPublisher()
	if err != nil || !ok {
		t.Fatalf("publisher: %v", err)
	}
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	item := byID[id]
	if item == nil {
		t.Fatalf("item %s not in the projection", id)
	}
	// The exact shape the fold hands a caller that could not open the envelope.
	item.Redacted = true
	item.Title, item.Context, item.Description = "[encrypted]", "[encrypted]", "[encrypted]"

	before := logSnapshot(t, dir)
	if _, err := resealCard(dir, pub, owner, boardD, item, resealOptions{}); err == nil {
		t.Fatal("re-sealing an item rd could not decrypt SUCCEEDED — it would have sealed the [encrypted] placeholder as the card's content, at a created_at that wins latest-wins")
	} else if !strings.Contains(err.Error(), "refusing to modify") {
		t.Fatalf("wrong refusal for a redacted item: %v", err)
	}
	if after := logSnapshot(t, dir); len(after) != len(before) {
		t.Fatalf("a refused re-seal still wrote to the log: %d events -> %d", len(before), len(after))
	}
}

// TestResealRefusesOnAPublicBoard: on a public board there is no reader who is
// expected to hold a key, so sealing a card there would make it unreadable to the
// board's own audience while achieving nothing this feature is for. It must refuse
// before publishing rather than seal to whatever key happens to exist.
func TestResealRefusesOnAPublicBoard(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t) // stays PUBLIC
	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "public by design", itemType: "task", priority: "p3",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	before := logSnapshot(t, dir)
	if _, err := resealOne(t, dir, owner, boardD, id); err == nil {
		t.Fatal("re-sealing on a PUBLIC board succeeded")
	} else if !strings.Contains(err.Error(), "PUBLIC") {
		t.Fatalf("wrong refusal on a public board: %v", err)
	}
	if after := logSnapshot(t, dir); len(after) != len(before) {
		t.Fatalf("a refused re-seal still wrote to the log: %d events -> %d", len(before), len(after))
	}
}
