package sync

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// resealPlanFixture seeds one board carrying every case the plan has to classify, and
// returns the relay plus the keys involved.
//
// It is deliberately a MIXED board — the production shape this whole epic exists for:
// public while items were written, then flipped to confidential, so the pre-cutover
// cards are plaintext and readable by anyone while the board reports CONFIDENTIAL.
func resealPlanFixture(t *testing.T) (relay *storeRelay, owner, contributor, staleReader, revoked *nostr.Key, boardD, boardCoord string, plainCard, sealedCard, foreignCard *nostr.Event) {
	t.Helper()
	mk := func(what string) *nostr.Key {
		k, err := nostr.GenerateKey()
		if err != nil {
			t.Fatalf("gen %s key: %v", what, err)
		}
		return k
	}
	owner, contributor, staleReader, revoked = mk("owner"), mk("contributor"), mk("stale reader"), mk("revoked")
	boardD = "planboard"
	boardCoord = BoardCoord(owner.PubKeyHex(), boardD)
	base := time.Now().Unix() - 10000
	relay = newStoreRelay(t)

	be, err := BuildBoardEvent(owner, BoardSpec{BoardD: boardD, Title: boardD}, base)
	if err != nil {
		t.Fatalf("board event: %v", err)
	}
	relay.putRaw(be)

	// Owner grants at epoch 1 then 2: the board is confidential and the current
	// epoch — the one a re-seal would seal under — is 2.
	for _, ep := range []int{1, 2} {
		g, gerr := BuildRoleGrantEvent(owner, RoleGrantSpec{
			BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: owner.PubKeyHex(),
			Role: RoleOwner, WrappedCEK: "wrapped", CEKEpoch: ep,
		}, base+int64(ep))
		if gerr != nil {
			t.Fatalf("owner grant epoch %d: %v", ep, gerr)
		}
		relay.putRaw(g)
	}
	// A reader stuck on epoch 1: reads the plaintext tail today, loses it after.
	g1, err := BuildRoleGrantEvent(owner, RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: staleReader.PubKeyHex(),
		Role: RoleContributor, WrappedCEK: "wrapped", CEKEpoch: 1,
	}, base+3)
	if err != nil {
		t.Fatalf("stale reader grant: %v", err)
	}
	relay.putRaw(g1)
	// A REVOKED key also stuck on epoch 1: must NOT be counted as losing access it
	// no longer has. Its revoke lives in the bare slot, a different addressable
	// coordinate from the grant it supersedes (ready-889), so it needs putDup.
	g2, err := BuildRoleGrantEvent(owner, RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: revoked.PubKeyHex(),
		Role: RoleContributor, WrappedCEK: "wrapped", CEKEpoch: 1,
	}, base+4)
	if err != nil {
		t.Fatalf("revoked grant: %v", err)
	}
	relay.putDup(g2)
	rev, err := BuildRoleGrantEvent(owner, RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: revoked.PubKeyHex(), Role: RoleRevoked,
	}, base+5)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	relay.putRaw(rev)

	// Owner-authored plaintext card: the case that WOULD be re-sealed.
	plainCard, err = BuildCardEvent(owner, CardSpec{
		ItemID: "plan-plain", Title: "readable", Status: state.StatusActive, Priority: "p1",
		Type: "task", BoardD: boardD, Context: "body",
	}, base+10)
	if err != nil {
		t.Fatalf("plain card: %v", err)
	}
	relay.putRaw(plainCard)

	// Already sealed: nothing to do.
	var cek [32]byte
	sealedCard, err = BuildCardEvent(owner, CardSpec{
		ItemID: "plan-sealed", Title: "hidden", Status: state.StatusActive, Priority: "p2",
		Type: "task", BoardD: boardD, Enc: &Envelope{CEK: cek, Epoch: 2},
	}, base+11)
	if err != nil {
		t.Fatalf("sealed card: %v", err)
	}
	relay.putRaw(sealedCard)

	// A CONTRIBUTOR's plaintext card: a different addressable coordinate, which the
	// owner cannot replace.
	foreignCard, err = BuildCardEvent(contributor, CardSpec{
		ItemID: "plan-foreign", Title: "theirs", Status: state.StatusActive, Priority: "p2",
		Type: "task", BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Context: "body",
	}, base+12)
	if err != nil {
		t.Fatalf("foreign card: %v", err)
	}
	relay.putRaw(foreignCard)

	// Two status events citing the plaintext card's CONCRETE event id: the
	// references a re-seal breaks.
	for i, st := range []string{state.StatusActive, state.StatusDone} {
		se, serr := BuildStatusEvent(owner, "plan-plain", st, plainCard.ID, "reason", base+20+int64(i))
		if serr != nil {
			t.Fatalf("status event: %v", serr)
		}
		// A status event's "a" tag names the CARD coordinate, so give it the board
		// coordinate too — that is what the plan's #a walk fetches on.
		se.Tags = append(se.Tags, []string{"a", boardCoord})
		if err := se.Sign(owner); err != nil {
			t.Fatalf("re-sign status: %v", err)
		}
		relay.putRaw(se)
	}
	return
}

// TestBuildResealPlan_IsProvablyReadOnly is the done condition that cannot be met by
// inspection.
//
// A dry run is the gate before a data-plane operation across eight live projects, and
// this project has a documented case of a republish believed to be a no-op that was
// not (ready-500). So "we only call read functions" is not evidence. The fixture
// relay counts EVENT frames at arrival — before any accept/reject decision, so a
// REJECTED write still counts — and the plan is held to zero.
func TestBuildResealPlan_IsProvablyReadOnly(t *testing.T) {
	relay, owner, _, _, _, boardD, boardCoord, _, _, _ := resealPlanFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	plan, err := BuildResealPlan(ctx, relay.url, owner.PubKeyHex(), boardD, boardCoord)
	if err != nil {
		t.Fatalf("BuildResealPlan: %v", err)
	}
	if n := relay.writeCount(); n != 0 {
		t.Fatalf("the dry run sent %d EVENT frames to the relay — a plan that writes is not a plan", n)
	}
	if plan.Cards == 0 {
		t.Fatal("the plan saw no cards, so zero writes proves nothing about a run that did work")
	}
}

// TestBuildResealPlan_ClassifiesEveryCoordinateAndNamesTheCost checks the report
// answers what an operator cannot approve a board without.
func TestBuildResealPlan_ClassifiesEveryCoordinateAndNamesTheCost(t *testing.T) {
	relay, owner, _, staleReader, revoked, boardD, boardCoord, plainCard, sealedCard, foreignCard := resealPlanFixture(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	plan, err := BuildResealPlan(ctx, relay.url, owner.PubKeyHex(), boardD, boardCoord)
	if err != nil {
		t.Fatalf("BuildResealPlan: %v", err)
	}

	if !plan.Confidential || plan.CurrentEpoch != 2 {
		t.Fatalf("board reported confidential=%v epoch=%d, want true/2 — a re-seal seals under the current epoch, so getting it wrong mis-states who loses access", plan.Confidential, plan.CurrentEpoch)
	}
	if plan.Cards != 3 {
		t.Fatalf("plan covers %d coordinates, want 3", plan.Cards)
	}
	byItem := map[string]CoordPlan{}
	for _, c := range plan.Coords {
		byItem[c.ItemID] = c
	}

	// The one that would actually be re-sealed.
	got := byItem["plan-plain"]
	if !got.Reseal || got.SkipReason != "" {
		t.Fatalf("owner-authored plaintext card: reseal=%v skip=%q, want reseal with no skip reason", got.Reseal, got.SkipReason)
	}
	if got.EventID != plainCard.ID {
		t.Fatalf("plan names event %s, the relay serves %s", got.EventID, plainCard.ID)
	}
	if got.SealedBytes <= got.PlaintextBytes {
		t.Fatalf("projected sealed %d not larger than plaintext %d", got.SealedBytes, got.PlaintextBytes)
	}
	if got.BrokenRefs != 2 {
		t.Fatalf("plan reports %d references breaking for plan-plain, want the 2 status events citing its event id — an operator told 0 would approve blind", got.BrokenRefs)
	}

	if c := byItem["plan-sealed"]; c.Reseal || c.SkipReason != SkipAlreadySealed {
		t.Fatalf("already-sealed card: reseal=%v skip=%q, want skip %q (re-sealing it again mints a new id every run and never converges)", c.Reseal, c.SkipReason, SkipAlreadySealed)
	} else if c.EventID != sealedCard.ID {
		t.Fatalf("sealed row names %s, want %s", c.EventID, sealedCard.ID)
	}
	if c := byItem["plan-foreign"]; c.Reseal || c.SkipReason != SkipForeignAuthor {
		t.Fatalf("contributor-authored card: reseal=%v skip=%q, want skip %q — an owner-signed replacement lands at a DIFFERENT coordinate and evicts nothing", c.Reseal, c.SkipReason, SkipForeignAuthor)
	} else if c.Author != foreignCard.PubKey {
		t.Fatalf("foreign row names author %s, want %s", c.Author, foreignCard.PubKey)
	}

	if plan.WouldReseal != 1 {
		t.Fatalf("WouldReseal = %d, want 1", plan.WouldReseal)
	}
	if plan.Skipped[SkipAlreadySealed] != 1 || plan.Skipped[SkipForeignAuthor] != 1 {
		t.Fatalf("skip roll-up = %+v, want one already-sealed and one foreign-author", plan.Skipped)
	}
	if plan.BrokenRefs != 2 || plan.LargestSealedBytes != got.SealedBytes {
		t.Fatalf("roll-up: brokenRefs=%d largestSealed=%d, want 2 and %d", plan.BrokenRefs, plan.LargestSealedBytes, got.SealedBytes)
	}

	// THE IRREVERSIBLE HUMAN COST: the epoch-1 reader loses history it can read
	// today; the REVOKED epoch-1 key must not be counted, because it has already
	// lost access and reporting it inflates the cost of the decision.
	if len(plan.ReadersLosingHistory) != 1 || plan.ReadersLosingHistory[0] != staleReader.PubKeyHex() {
		t.Fatalf("readers losing history = %v, want exactly the epoch-1 reader %s", plan.ReadersLosingHistory, staleReader.PubKeyHex())
	}
	for _, pk := range plan.ReadersLosingHistory {
		if pk == revoked.PubKeyHex() {
			t.Fatal("a REVOKED key is listed as losing access it does not have")
		}
		if pk == owner.PubKeyHex() {
			t.Fatal("the owner is listed as losing access to its own board")
		}
	}
}

// TestBuildResealPlan_APublicBoardIsEntirelyOutOfScope: a board with no CEK-bearing
// grant was never confidential. Its plaintext is INTENDED, and sealing it would make
// it unreadable to its own audience while achieving nothing this epic is for. The
// whole board must classify as out of scope rather than as work to do.
func TestBuildResealPlan_APublicBoardIsEntirelyOutOfScope(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	boardD := "publicboard"
	boardCoord := BoardCoord(owner.PubKeyHex(), boardD)
	base := time.Now().Unix() - 5000
	relay := newStoreRelay(t)
	be, err := BuildBoardEvent(owner, BoardSpec{BoardD: boardD, Title: boardD}, base)
	if err != nil {
		t.Fatalf("board event: %v", err)
	}
	relay.putRaw(be)
	// A role grant with NO wrapped CEK — the shape a plaintext board's grants have.
	g, err := BuildRoleGrantEvent(owner, RoleGrantSpec{
		BoardD: boardD, BoardAuthor: owner.PubKeyHex(), Grantee: owner.PubKeyHex(), Role: RoleOwner,
	}, base+1)
	if err != nil {
		t.Fatalf("grant: %v", err)
	}
	relay.putRaw(g)
	for i := 0; i < 3; i++ {
		c, cerr := BuildCardEvent(owner, CardSpec{
			ItemID: "pub-" + strings.Repeat("x", i+1), Title: "public", Status: state.StatusActive,
			Priority: "p2", Type: "task", BoardD: boardD,
		}, base+10+int64(i))
		if cerr != nil {
			t.Fatalf("card: %v", cerr)
		}
		relay.putRaw(c)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	plan, err := BuildResealPlan(ctx, relay.url, owner.PubKeyHex(), boardD, boardCoord)
	if err != nil {
		t.Fatalf("BuildResealPlan: %v", err)
	}
	if plan.Confidential || plan.CurrentEpoch != 0 {
		t.Fatalf("a board whose only grant carries no cek reported confidential=%v epoch=%d", plan.Confidential, plan.CurrentEpoch)
	}
	if plan.WouldReseal != 0 || plan.Skipped[SkipBoardNotConfidential] != 3 {
		t.Fatalf("public board: wouldReseal=%d skipped=%+v, want 0 and 3 board-not-confidential", plan.WouldReseal, plan.Skipped)
	}
	if len(plan.ReadersLosingHistory) != 0 {
		t.Fatalf("public board reports readers losing history: %v", plan.ReadersLosingHistory)
	}
}
