// ready-ed4's done condition, at the CLI layer: an item cannot become
// unpublishable through normal use.
//
// "Normal use" here is literally `rd progress --notes ...`, run through the REAL
// cobra command, past the point the OLD code would have refused — and the
// refusal point is not a number this file asserts, it is COMPUTED by replaying
// what the old code did (append the note to the card's context, rebuild the card)
// against the relay's own size expression. See oldCodeRefusalPoint.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// relayRejectsSize reproduces the relay's own oversize expression verbatim and
// independently of anything pkg/sync defines — a bare json.Marshal, a literal
// 64*1024, a strict `>` — so "too big" here is an EXTERNAL anchor, never a
// second call into the code under test. Mirrors nostrsize_test.go's
// relayBoundaryExceeds and nostr-relay's limits.go RejectOversizeEvent.
func relayRejectsSize(t *testing.T, e *nostr.Event) (int, bool) {
	t.Helper()
	b, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("json.Marshal (relay-expression anchor): %v", err)
	}
	return len(b), len(b) > 64*1024
}

// oldCodeRefusalPoint replays the PRE-ready-ed4 `rd progress` — "append
// \n\n[<ts>] <note> to the card's context and rebuild the whole card" — and
// returns how many notes of the given size that code could write before the card
// it produced crossed the relay's ceiling and the item became permanently
// unpublishable.
//
// Computing this rather than hardcoding it is the point: the test then drives the
// NEW code strictly PAST that number, so it can never silently degrade into
// "wrote a few notes, nothing broke". If the old appender's arithmetic changes,
// the bar moves with it.
func oldCodeRefusalPoint(t *testing.T, k *nostr.Key, itemID, boardCoord, note string) int {
	t.Helper()
	ctx := "base description"
	for i := 0; ; i++ {
		ctx += "\n\n[2026-07-30T20:00Z] " + note
		ev, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{
			ItemID: itemID, Title: "t", Status: "active", Context: ctx,
			BoardD: boardCoord,
		}, 1000)
		if err != nil {
			t.Fatalf("BuildCardEvent while measuring the old refusal point: %v", err)
		}
		if _, over := relayRejectsSize(t, ev); over {
			return i // i notes fit; note i+1 is the one that would be refused
		}
		if i > 10000 {
			t.Fatalf("old-code refusal point not reached after 10000 notes — note size %d is too small for this measurement", len(note))
		}
	}
}

// TestProgress_ItemStaysPublishablePastTheOldRefusalPoint is ready-ed4's done
// condition end to end:
//
//	write notes past the point the old code would refuse
//	  -> the item STILL accepts writes
//	  -> and still reads back COMPLETE on an independent reader
//
// The independent reader is a fresh projection built from the signed events
// ALONE — no CLI state, no cached item, nothing this process computed — which is
// what a second machine with a clean RD_HOME reconstructs from the log or a
// relay. Every note must be present, in order, with its text intact.
func TestProgress_ItemStaysPublishablePastTheOldRefusalPoint(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)

	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	boardD := projectPrefix(dir)

	id, err := runCreateNostr(dir, nostrCreateSpec{
		title: "Item that must never brick", itemType: "task", priority: "p0",
		context: "base description",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	// A realistically-sized orchestrator note. The item's own evidence says the
	// accelerant is "multi-thousand-character notes across four rework rounds", so
	// the unit under test is that note, not a one-liner.
	const noteBody = "ROUND 3 REWORK: re-measured the fold against the vector suite and the " +
		"browser port; the divergence was in tie-break order, not in the split itself. " +
		"Re-ran the whole suite green. "
	note := strings.Repeat(noteBody, 12) // ~2.7 KB per note

	refusalPoint := oldCodeRefusalPoint(t, k, id, boardD, note)
	if refusalPoint < 2 {
		t.Fatalf("test construction error: the old code could only write %d notes of %d bytes before refusing — pick a smaller note so the margin is meaningful", refusalPoint, len(note))
	}
	// Strictly PAST the old refusal point, with margin.
	total := refusalPoint + 5
	t.Logf("old in-card appender would refuse at note %d (each note %d bytes); writing %d", refusalPoint+1, len(note), total)

	want := make([]string, 0, total)
	for i := 0; i < total; i++ {
		body := fmt.Sprintf("note %d: %s", i, note)
		want = append(want, body)
		rootCmd.SetArgs([]string{"progress", id, "--notes", body})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("rd progress #%d failed — the item became unwritable, which is the exact defect ready-ed4 closes: %v", i, err)
		}
	}

	// (1) THE ITEM STILL ACCEPTS WRITES. A status change rebuilds and re-signs the
	// FULL current card — the operation that was frozen on vms-760 — so this is the
	// real test of "still publishable", not a repeat of the note path.
	rootCmd.SetArgs([]string{"claim", id})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rd claim after %d notes failed — the item is bricked: %v", total, err)
	}

	// (2) NO EVENT THE ITEM PRODUCED IS OVER THE RELAY'S CEILING. Asserted over
	// every signed event in the log, against the relay's own expression.
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	var biggestCard int
	for _, e := range events {
		n, over := relayRejectsSize(t, e)
		if over {
			t.Fatalf("kind-%d event for %s is %d bytes — OVER the relay's 64 KiB ceiling; growth is still unbounded", e.Kind, id, n)
		}
		if e.Kind == rdSync.KindCard && n > biggestCard {
			biggestCard = n
		}
	}
	// The card must not have grown with the trail at all: total note bytes are
	// well past 64 KiB by construction, so a card anywhere near that size would
	// mean notes are still riding inside it.
	if biggestCard > 8*1024 {
		t.Errorf("largest kind-30302 card is %d bytes after %d notes totalling ~%d bytes — the card is still absorbing the trail", biggestCard, total, total*len(note))
	}

	// (3) READS BACK COMPLETE ON AN INDEPENDENT READER. Project from the signed
	// events alone, with the read-trust gate on, exactly as a clean machine would.
	byID := rdSync.ProjectItems(events, rdSync.ProjectOptions{
		Trusted:     map[string]bool{owner: true},
		PinnedBoard: rdSync.BoardCoord(owner, boardD),
	})
	item := byID[id]
	if item == nil {
		t.Fatalf("independent reader does not see item %s at all", id)
	}
	if len(item.Notes) != total {
		t.Fatalf("independent reader sees %d notes, want %d — the trail did not survive the round trip", len(item.Notes), total)
	}
	for i, n := range item.Notes {
		if n.Text != want[i] {
			t.Fatalf("note %d read back as %.60q..., want %.60q... — trail order or content diverged", i, n.Text, want[i])
		}
		if n.MsgID == "" {
			t.Errorf("note %d has no MsgID — a note published as its own event must carry that event's id", i)
		}
	}
	// The rendered trail an agent actually reads must contain every note.
	trail := state.AssembleTrail(item)
	for i, w := range want {
		if !strings.Contains(trail, w) {
			t.Fatalf("assembled trail is missing note %d — `rd show` would not show it", i)
		}
	}
	assertNoDotCf(t)
}

// TestProgress_AlreadyOverLimitItemRecoversOnNextWrite is the cross-project half
// of the done condition: vms-760 and vms-01c are ALREADY over the ceiling, and a
// fix that cannot recover them leaves them bricked.
//
// It seeds an item whose card is genuinely OVER the relay's limit in exactly the
// shape the old code produced — a base description with the whole trail appended
// inline — and then asserts that ONE ordinary `rd progress` leaves the item with
// a small, publishable card AND every historical note preserved as its own event,
// readable by an independent projection.
func TestProgress_AlreadyOverLimitItemRecoversOnNextWrite(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	boardD := projectPrefix(dir)
	coord := rdSync.BoardCoord(owner, boardD)

	const itemID = "project-brick"
	// Build the bricked card the way the old appender did, until it is genuinely
	// over the relay's ceiling — not "large", OVER, by the relay's own expression.
	legacyNotes := []string{}
	ctx := "base description of the bricked item"
	for i := 0; ; i++ {
		body := fmt.Sprintf("legacy note %d: %s", i, strings.Repeat("recorded evidence. ", 60))
		legacyNotes = append(legacyNotes, body)
		ctx += fmt.Sprintf("\n\n[2026-07-%02dT10:%02dZ] %s", 1+i/24%28, i%60, body)
		ev, berr := rdSync.BuildCardEvent(k, rdSync.CardSpec{
			ItemID: itemID, Title: "Bricked", Status: "active", Context: ctx, BoardD: boardD,
		}, 1000)
		if berr != nil {
			t.Fatalf("BuildCardEvent: %v", berr)
		}
		if _, over := relayRejectsSize(t, ev); over {
			break
		}
		if i > 5000 {
			t.Fatalf("could not build an over-limit legacy card")
		}
	}
	// Seed the log with that over-limit card, plus a status event so the item is
	// a normal, claimable item.
	seed, err := rdSync.BuildCardEvent(k, rdSync.CardSpec{
		ItemID: itemID, Title: "Bricked", Status: "active", Context: ctx, BoardD: boardD,
	}, 1000)
	if err != nil {
		t.Fatalf("BuildCardEvent seed: %v", err)
	}
	if n, over := relayRejectsSize(t, seed); !over {
		t.Fatalf("test construction error: seeded card is %d bytes and is NOT over the relay ceiling", n)
	} else {
		t.Logf("seeded a genuinely bricked card: %d bytes (relay ceiling %d), %d inline notes", n, 64*1024, len(legacyNotes))
	}
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	if _, err := log.AppendUnique([]*nostr.Event{seed}); err != nil {
		t.Fatalf("seed append: %v", err)
	}

	// THE RECOVERY: one ordinary progress note. No migration command.
	rootCmd.SetArgs([]string{"progress", itemID, "--notes", "recovery note"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rd progress on an ALREADY-over-limit item failed — the item stays bricked: %v", err)
	}

	events, err := log.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	byID := rdSync.ProjectItems(events, rdSync.ProjectOptions{
		Trusted:     map[string]bool{owner: true},
		PinnedBoard: coord,
	})
	item := byID[itemID]
	if item == nil {
		t.Fatalf("item %s vanished from the projection after recovery", itemID)
	}

	// The item is writable again: the card the projection now republishes is small.
	rebuilt, err := rdSync.BuildCardEvent(k, rdSync.CardSpecFromItem(item, boardD), 2000)
	if err != nil {
		t.Fatalf("BuildCardEvent from the recovered item: %v", err)
	}
	n, over := relayRejectsSize(t, rebuilt)
	if over {
		t.Fatalf("the card rebuilt from the RECOVERED item is still %d bytes — over the ceiling; the item is still bricked", n)
	}
	t.Logf("card after recovery: %d bytes (was over %d)", n, 64*1024)

	// And nothing was lost: every legacy note is still in the trail, plus the new one.
	wantTotal := len(legacyNotes) + 1
	if len(item.Notes) != wantTotal {
		t.Fatalf("recovered trail has %d notes, want %d (%d legacy + 1 new) — compaction dropped history", len(item.Notes), wantTotal, len(legacyNotes))
	}
	for i, want := range legacyNotes {
		if item.Notes[i].Text != want {
			t.Fatalf("legacy note %d recovered as %.50q..., want %.50q...", i, item.Notes[i].Text, want)
		}
		if item.Notes[i].MsgID == "" {
			t.Errorf("legacy note %d has no MsgID after recovery — it was never minted as its own event, so a relay-only reader would lose it", i)
		}
	}
	if last := item.Notes[wantTotal-1]; last.Text != "recovery note" {
		t.Errorf("last note is %q, want the new %q", last.Text, "recovery note")
	}

	// A RELAY-ONLY READER SEES IT ALL. The compacted card replaced the old one at
	// the same addressable coordinate, so anything that survived only inside the
	// old card's content is gone from a relay's view. Drop every superseded card
	// (keep only the newest per coordinate, which is what a relay stores) and
	// re-project: the trail must still be complete.
	relayView := newestCardsOnly(events)
	byID2 := rdSync.ProjectItems(relayView, rdSync.ProjectOptions{
		Trusted:     map[string]bool{owner: true},
		PinnedBoard: coord,
	})
	relayItem := byID2[itemID]
	if relayItem == nil {
		t.Fatalf("relay-view reader does not see %s", itemID)
	}
	if len(relayItem.Notes) != wantTotal {
		t.Fatalf("relay-view reader sees %d notes, want %d — compaction destroyed the trail for every reader that does NOT have the local log", len(relayItem.Notes), wantTotal)
	}
	assertNoDotCf(t)
}

// newestCardsOnly models what an ADDRESSABLE-event relay actually stores: one
// kind-30302 per (pubkey, d) coordinate, the newest by (created_at, id). Every
// other kind is append-only and kept. This is how the test asks "what would a
// machine that only ever talked to a relay see?" without needing a relay.
func newestCardsOnly(events []*nostr.Event) []*nostr.Event {
	newest := map[string]*nostr.Event{}
	for _, e := range events {
		if e == nil || e.Kind != rdSync.KindCard {
			continue
		}
		var d string
		for _, tag := range e.Tags {
			if len(tag) >= 2 && tag[0] == "d" {
				d = tag[1]
				break
			}
		}
		key := e.PubKey + ":" + d
		cur, ok := newest[key]
		if !ok || e.CreatedAt > cur.CreatedAt || (e.CreatedAt == cur.CreatedAt && e.ID < cur.ID) {
			newest[key] = e
		}
	}
	out := make([]*nostr.Event, 0, len(events))
	for _, e := range events {
		if e == nil {
			continue
		}
		if e.Kind == rdSync.KindCard {
			var d string
			for _, tag := range e.Tags {
				if len(tag) >= 2 && tag[0] == "d" {
					d = tag[1]
					break
				}
			}
			if newest[e.PubKey+":"+d] != e {
				continue // superseded — a relay would not still hold it
			}
		}
		out = append(out, e)
	}
	return out
}

// TestProgress_DoesNotRepublishTheCard pins the mechanism, not just the outcome:
// an ordinary progress note on a healthy item publishes ONE kind-1111 event and
// republishes NO card. Without this, a future change could reintroduce a card
// rewrite per note and every size assertion above would still pass for a while —
// right up until the board hit the ceiling again.
func TestProgress_DoesNotRepublishTheCard(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	id, err := runCreateNostr(dir, nostrCreateSpec{title: "Healthy", itemType: "task", priority: "p2", context: "desc"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
	before, err := log.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	cardsBefore := countKind(before, rdSync.KindCard)

	rootCmd.SetArgs([]string{"progress", id, "--notes", "did a thing"})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rd progress: %v", err)
	}

	after, err := log.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := countKind(after, rdSync.KindCard); got != cardsBefore {
		t.Errorf("kind-30302 cards went %d -> %d across one `rd progress` — a note must NOT republish the card; that republish IS the unbounded-growth mechanism", cardsBefore, got)
	}
	if got := countKind(after, rdSync.KindNote) - countKind(before, rdSync.KindNote); got != 1 {
		t.Errorf("one `rd progress` produced %d kind-1111 note events, want exactly 1", got)
	}
}

func countKind(events []*nostr.Event, kind int) int {
	n := 0
	for _, e := range events {
		if e != nil && e.Kind == kind {
			n++
		}
	}
	return n
}

// TestProgress_OutputAndShowRenderTheTrail proves the READ surface an agent
// actually uses did not regress: `rd show` must still print every note, even
// though notes no longer live in the card's context field.
func TestProgress_OutputAndShowRenderTheTrail(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	id, err := runCreateNostr(dir, nostrCreateSpec{title: "Readable", itemType: "task", priority: "p2", context: "the description"})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	for _, n := range []string{"first thing", "second thing", "third thing"} {
		rootCmd.SetArgs([]string{"progress", id, "--notes", n})
		if err := rootCmd.Execute(); err != nil {
			t.Fatalf("rd progress %q: %v", n, err)
		}
	}
	item, err := nostrResolveItem(id)
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	trail := state.AssembleTrail(item)
	if !strings.HasPrefix(trail, "the description") {
		t.Errorf("assembled trail does not start with the item's description: %.80q", trail)
	}
	for _, n := range []string{"first thing", "second thing", "third thing"} {
		if !strings.Contains(trail, n) {
			t.Errorf("assembled trail is missing %q — `rd show` would not display it", n)
		}
	}
	// Order is chronological, not insertion-order-by-accident.
	if i, j, k := strings.Index(trail, "first"), strings.Index(trail, "second"), strings.Index(trail, "third"); !(i < j && j < k) {
		t.Errorf("trail order is wrong: first@%d second@%d third@%d", i, j, k)
	}
	// item.Context is the BASE description only — the invariant that keeps the
	// card from re-absorbing the trail on the next republish.
	if item.Context != "the description" {
		t.Errorf("item.Context = %q, want just the base description — anything more and the next card republish re-absorbs the trail", item.Context)
	}
	_ = dir
}

// TestSearch_MatchesProgressNoteText is a regression test for a reader that
// ready-ed4 nearly broke silently: `rd search` matched note text only because
// notes happened to live inside item.Context. Moving them into their own events
// would have made every progress note unsearchable — and a note is usually the
// most searchable text an item has ("what did we try for the OOM?") — while
// every existing search test kept passing, because they all search titles and
// descriptions.
func TestSearch_MatchesProgressNoteText(t *testing.T) {
	item := &state.Item{
		Title:   "Some item",
		Context: "the base description",
		Notes: []state.ProgressNote{
			{At: "2026-07-30T20:00Z", Text: "the OOM came from the negentropy buffer"},
		},
	}
	if !matchesSearch(item, "negentropy buffer") {
		t.Errorf("rd search does not match text that lives in a progress note — every note ever written just became unsearchable")
	}
	// Case-insensitive, like every other field.
	if !matchesSearch(item, "NEGENTROPY") {
		t.Errorf("progress-note search is not case-insensitive, unlike title/description search")
	}
	// It still matches the description, and still rejects a genuine miss.
	if !matchesSearch(item, "base description") {
		t.Errorf("rd search stopped matching the item's description")
	}
	if matchesSearch(item, "something entirely absent") {
		t.Errorf("rd search matched a term that appears nowhere")
	}
}
