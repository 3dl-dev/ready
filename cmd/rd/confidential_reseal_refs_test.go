package main

// What happens to a reference to the ORIGINAL plaintext card once it is superseded
// (ready-c9d, epic ready-336).
//
// THE RISK: re-sealing mints a new event id per coordinate, and the old event stops
// being served by any relay (addressable replacement). Anything that stored or cited
// that id now points at nothing, and the failure is SILENT — nothing errors, the
// reference simply resolves to nothing.
//
// THE ENUMERATION, made against the live `ready` board's own 1,820 events rather than
// a fixture, is on the item. In summary, and this test pins the load-bearing half:
//
//   - dep edges ("i") and parent edges ("parent") .... ITEM ids, not event ids. SURVIVE.
//   - status events' card anchor ("a") ............... a COORDINATE, not an event id.
//     This is the anchor the fold actually resolves, and it is exactly why an
//     addressable replacement does not orphan an item's history. SURVIVES.
//   - Item.MsgID / Item.GateMsgID .................... DERIVED at fold time from the
//     winning card event (nostrproject.go), never stored. They take a new VALUE after
//     a re-seal and cannot dangle. SURVIVES by re-derivation.
//   - status events' concrete card pointer ("e") ..... 328 of the ready board's status
//     events carry one. Every single one BREAKS. Accepted, because neither reader
//     resolves it — BuildStatusEvent/writeevents.ts WRITE it as NIP-10 provenance and
//     nothing in pkg/sync, cmd/rd or web/board reads it back — and the superseded
//     event stays in the local append-only log, where the reference still resolves.
//   - docs/ops/board-inventory.{csv,json} event_id ... BREAKS. Accepted: it is a
//     declared snapshot with a committed re-runnable producer (ready-207).
//   - `rd relay audit` .............................. reports a superseded coordinate
//     as MISSING rather than replaced. Tracked as ready-fcd, not accepted here.
//
// There are NO carried-forward cases: nothing had to be taught to rewrite a stored id,
// because nothing stores one that is later resolved.

import (
	"testing"

	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// TestResealLeavesNoDanglingReferenceThatAnythingResolves proves the two claims the
// enumeration rests on, on an item carrying real history and a live gate:
//
//	(1) everything that RESOLVES survives — history stays bound to the item through
//	    the coordinate anchor, and the derived ids re-derive to the new card;
//	(2) the references that BREAK are inert — the fixture really does contain status
//	    events pointing at the superseded event id, and the item folds correctly anyway.
//
// (2) is what stops this from being a vacuous "it still works": a fixture with no
// stale "e" pointer in it would prove nothing about the 328 on the live board.
func TestResealLeavesNoDanglingReferenceThatAnythingResolves(t *testing.T) {
	dir, owner, boardD := setupMixedConfidentialProject(t)

	id, err := runCreateNostr(mustDir(t), nostrCreateSpec{
		title: "carries history and a gate", context: "body", itemType: "task", priority: "p1",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	// Real history: a claim, then a gate. Both publish status events carrying the
	// concrete "e" pointer at the card that is current AT THAT MOMENT.
	if err := runClaimNostr(id, "starting"); err != nil {
		t.Fatalf("claim: %v", err)
	}
	if err := runGateNostr(id, "design", "needs a ruling"); err != nil {
		t.Fatalf("gate: %v", err)
	}

	_, beforeByID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project before: %v", err)
	}
	before := beforeByID[id]
	if before == nil {
		t.Fatalf("item %s missing before the re-seal", id)
	}
	if before.GateMsgID == "" {
		t.Fatal("fixture is wrong: the item carries no gate, so the derived GateMsgID case is not exercised")
	}
	historyBefore := len(before.History)
	if historyBefore == 0 {
		t.Fatal("fixture is wrong: the item carries no history, so the anchor claim is not exercised")
	}
	originalCard := coordinateWinner(t, dir, owner, id)
	if before.MsgID != originalCard.ID {
		t.Fatalf("MsgID %s is not the winning card %s — the fixture's assumption about derivation is wrong", before.MsgID, originalCard.ID)
	}

	enableConfidential(t, dir, owner, boardD)
	out, err := resealOne(t, dir, owner, boardD, id)
	if err != nil {
		t.Fatalf("resealCard: %v", err)
	}

	// --- (2) first: the stale-pointer population is real in this fixture. ---------
	stale := statusEventsPointingAt(t, dir, originalCard.ID)
	if stale == 0 {
		t.Fatal("no status event in this fixture points at the superseded card event id — the accepted-breakage case is not being exercised, so passing proves nothing")
	}

	// --- (1) everything that resolves still resolves. -----------------------------
	_, afterByID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project after: %v", err)
	}
	after := afterByID[id]
	if after == nil {
		t.Fatalf("the item vanished after re-sealing, with %d status events pointing at its superseded card", stale)
	}
	if len(after.History) != historyBefore {
		t.Fatalf("history went from %d to %d entries — status events bind to the card by COORDINATE, so an addressable replacement must not orphan them", historyBefore, len(after.History))
	}
	if after.MsgID != out.SealedEventID {
		t.Fatalf("MsgID = %s, want the sealed card %s — it is derived from the winning card, so a stale value means the fold resolved a superseded event", after.MsgID, out.SealedEventID)
	}
	if after.MsgID == before.MsgID {
		t.Fatalf("MsgID did not move off the superseded card %s", before.MsgID)
	}
	if after.GateMsgID != after.MsgID {
		t.Fatalf("GateMsgID = %s but MsgID = %s — the gate id is derived from the winning card and must follow it", after.GateMsgID, after.MsgID)
	}
	if after.WaitingType != before.WaitingType || after.Gate != before.Gate {
		t.Fatalf("the live gate changed shape across a re-seal: waiting_type %q->%q gate %q->%q", before.WaitingType, after.WaitingType, before.Gate, after.Gate)
	}

	// --- the broken references still resolve in the LOCAL log, which is what makes
	// accepting them defensible rather than merely convenient. ---------------------
	if !logHasEvent(t, dir, originalCard.ID) {
		t.Fatalf("the superseded card %s is gone from the local append-only log — then the %d status events pointing at it reference nothing anywhere, and 'accepted' would be wrong", originalCard.ID, stale)
	}
}

// statusEventsPointingAt counts events in the local log whose concrete "e" pointer
// names eventID — the provenance reference a re-seal breaks.
func statusEventsPointingAt(t *testing.T, dir, eventID string) int {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	n := 0
	for _, e := range events {
		if e.Kind == 30302 {
			continue
		}
		for _, tg := range e.Tags {
			if len(tg) > 1 && tg[0] == "e" && tg[1] == eventID {
				n++
			}
		}
	}
	return n
}

// logHasEvent reports whether the local append-only log still holds eventID.
func logHasEvent(t *testing.T, dir, eventID string) bool {
	t.Helper()
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("read log: %v", err)
	}
	for _, e := range events {
		if e.ID == eventID {
			return true
		}
	}
	return false
}
