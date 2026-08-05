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
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
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

// --- ready-ed4 rework: the write path's OWN confidentiality, and the
// --- status-change half of the pending-note mint -----------------------------
//
// Everything above this line runs on a PLAINTEXT board (setupNostrNativeProject
// marks the board Public). That was a coverage hole with a receipt: DELETING the
// setCardEnvelope call in publishItemNoteNostr — so every `rd progress` note
// publishes IN CLEAR on a confidential board — left the entire suite green,
// because pkg/sync's note tests all construct the Envelope themselves and so
// cannot observe the production path failing to construct one. The tests below
// assert on the PUBLISHED events instead, which is the only place a missing
// envelope is visible.

// noteEvents returns every kind-1111 progress-note event in the slice.
func noteEvents(events []*nostr.Event) []*nostr.Event {
	var out []*nostr.Event
	for _, e := range events {
		if e != nil && e.Kind == rdSync.KindNote {
			out = append(out, e)
		}
	}
	return out
}

// assertSealedOnTheWire fails unless every event in evs carries a well-formed
// confidential envelope AND none of `secrets` appears anywhere in the marshaled
// event — tags included, because a relay INDEXES tags and a leak into one is as
// fatal as a leak into Content.
//
// The envelope check is spelled out here rather than delegated to pkg/sync:
// encWellFormed is unexported, and delegating would make the assertion a second
// call into the code under test. enc marker is the known version, cek_epoch
// parses, and Content base64-decodes to at least a 12-byte nonce plus a 16-byte
// Poly1305 tag — so an enc-shaped event carrying smuggled cleartext fails here.
func assertSealedOnTheWire(t *testing.T, evs []*nostr.Event, what string, secrets ...string) {
	t.Helper()
	for _, e := range evs {
		if got, _ := tagVal(e.Tags, "enc"); got != "1" {
			t.Errorf("%s (kind %d, id %s) carries enc=%q, want \"1\" — it was published UNSEALED on a confidential board", what, e.Kind, e.ID, got)
		}
		if got, ok := tagVal(e.Tags, "cek_epoch"); !ok {
			t.Errorf("%s (id %s) carries no cek_epoch tag — no reader can pick a key for it", what, e.ID)
		} else if _, err := strconv.Atoi(got); err != nil {
			t.Errorf("%s (id %s) has an unparseable cek_epoch %q", what, e.ID, got)
		}
		raw, err := base64.StdEncoding.DecodeString(e.Content)
		if err != nil {
			t.Errorf("%s (id %s) Content is not base64 — it is not a sealed payload: %.80q", what, e.ID, e.Content)
		} else if len(raw) < 12+16 {
			t.Errorf("%s (id %s) Content decodes to %d bytes, too short to be nonce+AEAD — it is not sealed", what, e.ID, len(raw))
		}
		blob, merr := json.Marshal(e)
		if merr != nil {
			t.Fatalf("marshal %s: %v", what, merr)
		}
		for _, s := range secrets {
			if strings.Contains(string(blob), s) {
				t.Errorf("%s (id %s) LEAKS %q on the wire: %s", what, e.ID, s, blob)
			}
		}
	}
}

// assertLogCarriesNoPlaintext scans EVERY event in the log for each secret. A
// note sealed in its own event but echoed in the clear by some other event the
// same mutation wrote is not confidential.
func assertLogCarriesNoPlaintext(t *testing.T, events []*nostr.Event, secrets ...string) {
	t.Helper()
	for _, e := range events {
		blob, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal kind-%d event: %v", e.Kind, err)
		}
		for _, s := range secrets {
			if strings.Contains(string(blob), s) {
				t.Fatalf("kind-%d event leaks confidential free text (%q) on the wire: %s", e.Kind, s, blob)
			}
		}
	}
}

// TestProgress_ConfidentialBoardSealsTheNoteOnTheWire is the mutation-proof for
// the ONE way a progress note leaks: the production write path failing to supply
// an envelope AT ALL.
//
// It runs the real `rd progress` cobra command against a CONFIDENTIAL board and
// then asserts on what was actually PUBLISHED — the kind-1111 event as it sits
// in the authoritative log, which is byte-for-byte what a relay receives. This
// test constructs no Envelope and stubs no key source, so a write path that
// forgets to seal is visible here and nowhere else in the suite.
//
// MUTATION RECEIPT: delete the setCardEnvelope call at the top of
// publishItemNoteNostr (cmd/rd/nostrnote.go) and this test fails on the
// enc-marker assertion AND on the plaintext-on-the-wire scan.
func TestProgress_ConfidentialBoardSealsTheNoteOnTheWire(t *testing.T) {
	dir, _ := setupConfidentialProject(t)

	id, err := runCreateNostr(dir, nostrCreateSpec{
		title: "rotate the signing key", itemType: "task", priority: "p1",
		context: "the base description of the item",
	})
	if err != nil {
		t.Fatalf("runCreateNostr: %v", err)
	}

	// Deliberately disjoint from the item's own title/context, so a whole-log scan
	// for these tokens can only ever hit the NOTE.
	const secretNote = "prod-west deploy credential rotated at 20:17; predecessor revoked"
	secretWords := []string{"prod-west", "credential", "predecessor", secretNote}

	rootCmd.SetArgs([]string{"progress", id, "--notes", secretNote})
	if err := rootCmd.Execute(); err != nil {
		t.Fatalf("rd progress on a confidential board: %v", err)
	}

	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	notes := noteEvents(events)
	if len(notes) != 1 {
		t.Fatalf("one `rd progress` produced %d kind-1111 events, want exactly 1 — every assertion below would be vacuous", len(notes))
	}
	assertSealedOnTheWire(t, notes, "the published progress note", secretWords...)
	assertLogCarriesNoPlaintext(t, events, secretWords...)

	// NOT VACUOUS: the owner still reads its own note back in plaintext, so the
	// scan above proves SEALING rather than the note having gone missing.
	_, byID, err := nostrProjectAllItems()
	if err != nil {
		t.Fatalf("project: %v", err)
	}
	item := byID[id]
	if item == nil {
		t.Fatalf("item %s missing from the owner's projection", id)
	}
	if len(item.Notes) != 1 || item.Notes[0].Text != secretNote {
		t.Fatalf("owner reads back %v, want exactly the one note %q — the seal assertions must not be passing because the note vanished", noteTextsOf(item.Notes), secretNote)
	}
	assertNoDotCf(t)
}

func noteTextsOf(notes []state.ProgressNote) []string {
	out := make([]string, len(notes))
	for i, n := range notes {
		out[i] = n.Text
	}
	return out
}

// projectRelayView projects what a machine that only ever talked to a RELAY
// sees: superseded 30302 cards dropped (an addressable relay keeps one per
// coordinate), everything else kept. Keys are derived from that same view, so a
// grant this reader could not fetch cannot silently rescue the projection.
func projectRelayView(t *testing.T, dir string, events []*nostr.Event) map[string]*state.Item {
	t.Helper()
	k, err := nostrKey()
	if err != nil {
		t.Fatalf("nostrKey: %v", err)
	}
	view := newestCardsOnly(events)
	keyring := boardReadKeyring(dir, k, view)
	return rdSync.ProjectItems(view, rdSync.ProjectOptions{
		Trusted:         nostrTrustSet(dir, k.PubKeyHex()),
		PinnedBoard:     nostrPinnedBoard(dir),
		Decryptor:       keyring,
		EncryptedBoards: keyring,
	})
}

// legacyCardBlob rebuilds the exact Content shape the PRE-ready-ed4 appender
// produced: the base description, then one "\n\n[<ts>] <text>" per note.
func legacyCardBlob(base string, notes []string) string {
	blob := base
	for i, n := range notes {
		blob += fmt.Sprintf("\n\n[2026-07-%02dT10:%02dZ] %s", 1+i%28, i%60, n)
	}
	return blob
}

// TestCardRepublishPaths_MintPendingNotesFromALegacyCard covers the OTHER TWO
// THIRDS of appendPendingNotes' claim.
//
// appendPendingNotes is called from three publish paths — PublishNote (the
// `rd progress` path), PublishStatusChange (claim/done/fail/cancel), and
// PublishCardEdit (label/dep/update). Only the first was tested. Removing the
// call from PublishStatusChange therefore failed NOTHING but a spec
// line-number anchor, even though the behavioural consequence is total: a
// status change compacts the legacy card WITHOUT first minting events for the
// trail it compacts out, so every reader that does not hold the local log —
// which is every other machine, and the browser board — loses the item's whole
// history the moment anyone runs `rd claim` on it.
//
// The proof runs the REAL cobra commands on a genuinely legacy-shaped card and
// reads back through a RELAY-ONLY view (superseded cards dropped), which is the
// only view in which that loss is visible at all.
//
// Every path also runs on a CONFIDENTIAL board, because appendPendingNotes is
// where a recovered note's envelope comes from (Enc: card.Enc) — the same
// missing-envelope leak as the live-note path, on the recovery half.
//
// MUTATION RECEIPT: delete the appendPendingNotes call from PublishStatusChange
// and the claim/* and done/* subtests fail; delete it from PublishCardEdit and
// the label-add/* subtests fail.
func TestCardRepublishPaths_MintPendingNotesFromALegacyCard(t *testing.T) {
	base := "the base description of a legacy item"
	legacyNotes := []string{
		"legacy note one: reproduced the OOM under load",
		"legacy note two: the allocation came from the negentropy buffer",
		"legacy note three: patched and re-measured, 12 MB steady",
	}
	// Confidential subtests scan the wire for these; they appear in the notes and
	// nowhere in any title.
	secretWords := []string{"negentropy buffer", "re-measured", "reproduced the OOM"}

	cases := []struct {
		name         string
		confidential bool
		args         func(id string) []string
	}{
		{"claim/plaintext board", false, func(id string) []string { return []string{"claim", id} }},
		{"claim/confidential board", true, func(id string) []string { return []string{"claim", id} }},
		{"done/plaintext board", false, func(id string) []string { return []string{"done", id, "--reason", "shipped it"} }},
		{"done/confidential board", true, func(id string) []string { return []string{"done", id, "--reason", "shipped it"} }},
		// The THIRD appendPendingNotes caller: PublishCardEdit, reached by every
		// card-only mutation (label, dep, update). `rd label add` republishes the
		// card without changing status, so it compacts the legacy trail out of the
		// card exactly as a status change does — and must mint the same events.
		{"label-add/plaintext board", false, func(id string) []string { return []string{"label", "add", id, "urgent"} }},
		{"label-add/confidential board", true, func(id string) []string { return []string{"label", "add", id, "urgent"} }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var dir string
			if tc.confidential {
				dir, _ = setupConfidentialProject(t)
			} else {
				dir, _ = setupNostrNativeProject(t)
			}

			id, err := runCreateNostr(dir, nostrCreateSpec{
				title: "legacy item", itemType: "task", priority: "p1", context: base,
			})
			if err != nil {
				t.Fatalf("runCreateNostr: %v", err)
			}

			// SEED THE LEGACY CARD through the production card-edit hook, so the card
			// is sealed (or not) exactly as this board seals things and the test never
			// constructs an Envelope of its own. Notes are cleared so the edit carries
			// no PendingNotes: this publishes ONE card whose Content is the old
			// appender's inline blob, which is precisely what vms-760's card is.
			item, err := nostrResolveItem(id)
			if err != nil {
				t.Fatalf("resolve: %v", err)
			}
			item.Context = legacyCardBlob(base, legacyNotes)
			item.Notes = nil
			if err := publishItemCardEditNostr(item); err != nil {
				t.Fatalf("seed legacy card: %v", err)
			}

			log := rdSync.NewNostrLog(rdSync.NostrLogPath(dir))
			seeded, err := log.ReadAll()
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			// SEED SANITY: the trail exists ONLY inside the card right now — the fold
			// recovers it, and not one note has an event of its own yet.
			if n := countKind(seeded, rdSync.KindNote); n != 0 {
				t.Fatalf("seed produced %d kind-1111 events, want 0 — the seed is not legacy-shaped", n)
			}
			seedItem := projectRelayView(t, dir, seeded)[id]
			if seedItem == nil {
				t.Fatalf("seeded item %s missing from the relay view", id)
			}
			if len(seedItem.Notes) != len(legacyNotes) {
				t.Fatalf("seeded card splits into %d notes, want %d — the seed is not legacy-shaped", len(seedItem.Notes), len(legacyNotes))
			}
			for i, n := range seedItem.Notes {
				if n.MsgID != "" {
					t.Fatalf("seeded note %d already carries MsgID %q — it would not be pending, so this subtest would prove nothing", i, n.MsgID)
				}
			}

			// THE STATUS CHANGE. This is the mutation that republishes — and therefore
			// COMPACTS — the card.
			rootCmd.SetArgs(tc.args(id))
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("rd %v: %v", tc.args(id), err)
			}

			after, err := log.ReadAll()
			if err != nil {
				t.Fatalf("ReadAll: %v", err)
			}
			minted := noteEvents(after)
			if len(minted) != len(legacyNotes) {
				t.Fatalf("the status change minted %d kind-1111 events, want %d (one per note it compacted out of the card) — the trail was dropped from every relay's copy", len(minted), len(legacyNotes))
			}

			// THE CARD WAS ACTUALLY COMPACTED. Without this the assertion above could
			// pass on a card that still carries the trail inline, which is the growth
			// this whole item exists to stop.
			relay := projectRelayView(t, dir, after)
			got := relay[id]
			if got == nil {
				t.Fatalf("relay-only reader does not see %s after the status change", id)
			}
			if got.Context != base {
				t.Errorf("card Context after the status change = %.80q, want just the base description — the card is still carrying the trail", got.Context)
			}

			// AND NOTHING WAS LOST, for a reader holding no local log.
			if len(got.Notes) != len(legacyNotes) {
				t.Fatalf("relay-only reader sees %d notes, want %d — compaction destroyed the trail for every machine but this one", len(got.Notes), len(legacyNotes))
			}
			for i, want := range legacyNotes {
				if got.Notes[i].Text != want {
					t.Errorf("note %d reads back as %q, want %q", i, got.Notes[i].Text, want)
				}
				if got.Notes[i].MsgID == "" {
					t.Errorf("note %d has no MsgID — it was never minted as its own event", i)
				}
			}

			if tc.confidential {
				assertSealedOnTheWire(t, minted, "a recovered legacy note", secretWords...)
				assertLogCarriesNoPlaintext(t, after, secretWords...)
			}
			assertNoDotCf(t)
		})
	}
}
