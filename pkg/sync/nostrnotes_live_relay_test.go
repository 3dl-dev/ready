package sync

// ready-ed4's done condition against a REAL relay: "confirm the item still
// accepts writes and still reads back complete on an independent reader."
//
// Every other note test in this repo asserts against an in-process log or a
// simulated relay view (newestCardsOnly). Both model the two properties this fix
// depends on — that a relay keeps exactly ONE kind-30302 per (pubkey, d) and
// therefore physically discards the legacy card the moment the compacted one
// lands, and that it keeps every kind-1111 — but a model is an assumption. This
// file makes strfry itself supply them: the recovery is published to a live
// relay, and the trail is read back by a process whose ONLY source is that
// relay.
//
// Gated behind RD_NOSTR_LIVE_RELAY=1 with a stable portfolio key (liveRelayKey),
// like the other 13 live proofs in this package.

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestLiveRelay_ConfidentialTrailRecoversOnARelayOnlyReader is the ground-source
// proof for BOTH halves of ready-ed4, with no local log to fall back on:
//
//	(1) RECOVERY — an item whose card carries its whole trail inline (the shape
//	    that bricked vms-760) is compacted by one ordinary write, and a reader
//	    that has ONLY the relay still sees every historical note. This is the
//	    claim that has no fallback: the compacted card REPLACES the legacy one at
//	    the same addressable coordinate, so strfry deletes the only copy of the
//	    inline trail. If the note events were not published first, the history is
//	    gone from the network, not merely from a projection.
//
//	(2) CONFIDENTIALITY — every note event the relay serves is sealed, and the
//	    note text appears nowhere in the bytes strfry handed back.
//
// The board is CONFIDENTIAL throughout, because that is the case where both
// halves can fail silently at once: an unsealed note is readable by the relay
// operator, and a note the fold gate quarantines vanishes from the trail.
func TestLiveRelay_ConfidentialTrailRecoversOnARelayOnlyReader(t *testing.T) {
	if os.Getenv("RD_NOSTR_LIVE_RELAY") != "1" {
		t.Skip("set RD_NOSTR_LIVE_RELAY=1 (with a reachable strfry relay) to run the live note-trail recovery proof")
	}
	relay := liveRelayURL(t)
	t.Logf("live relay: %s", relay)

	k := liveRelayKey(t)
	boardD := liveTestBoardD(t)
	boardCoord := BoardCoord(k.PubKeyHex(), boardD)
	trust := map[string]bool{k.PubKeyHex(): true}
	itemID := fmt.Sprintf("ready-ed4-live-%d", time.Now().UnixNano())

	// A real per-board CEK: 32 random bytes, exactly as boardConfidentialEnvelope
	// mints one. Epoch 1.
	var cek [32]byte
	if _, err := rand.Read(cek[:]); err != nil {
		t.Fatalf("mint CEK: %v", err)
	}
	env := &Envelope{CEK: cek, Epoch: 1}
	dec := testNoteDecryptor{cek: cek, epoch: 1}
	// cutover 1 → every event in this test is post-cutover, so NOTHING is
	// grandfathered and the fail-closed fold gate is fully armed.
	ebs := fixedEncryptedBoards{coord: boardCoord, cutover: 1}
	opts := ProjectOptions{
		Trusted: trust, PinnedBoard: boardCoord,
		Decryptor: dec, EncryptedBoards: ebs,
	}

	// THE LEGACY CARD. Content is the base description with the whole trail
	// appended inline — byte-for-byte the shape the pre-ready-ed4 `rd progress`
	// appender produced — then sealed, because this board is confidential.
	const base = "base description of the item that bricked"
	legacy := make([]string, 12)
	inline := base
	for i := range legacy {
		legacy[i] = fmt.Sprintf("legacy note %d: %s", i, strings.Repeat("recorded evidence that must survive. ", 6))
		inline += fmt.Sprintf("\n\n[2026-07-%02dT10:%02dZ] %s", 1+i, i, legacy[i])
	}
	// Words that exist ONLY in note text, so a wire scan for them can only hit a
	// note (or a card that is still carrying one).
	secretWords := []string{"recorded evidence that must survive", "legacy note 0", "the credential rotated"}

	dir := t.TempDir()
	writerLog := NewNostrLog(filepath.Join(dir, ".ready", NostrLogFile))
	pub := &Publisher{
		Key: k, Log: writerLog, WriteRelays: []string{relay},
		PendingPath: filepath.Join(dir, ".ready", NostrPendingFile),
	}
	board := BoardSpec{BoardD: boardD, Title: boardD, Maintainers: []string{k.PubKeyHex()}}
	now := time.Now().Unix()

	mustAccept := func(label string, res PublishResult, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: publish: %v", label, err)
		}
		if len(res.Events) == 0 {
			t.Fatalf("%s: published no events", label)
		}
		for _, ev := range res.Events {
			if !ev.AnyRelay {
				t.Fatalf("%s: kind-%d event %s was NOT accepted by %s (acks=%+v)", label, ev.Kind, ev.EventID, relay, ev.Acks)
			}
		}
	}

	seedRes, seedErr := pub.PublishItem(context.Background(), &board, CardSpec{
		ItemID: itemID, Title: "bricked item", Status: state.StatusActive,
		Priority: "p1", Type: "task", Assignee: k.PubKeyHex(),
		Context: inline, BoardD: boardD, Enc: env,
	}, now)
	mustAccept("seed legacy card", seedRes, seedErr)

	// ---- READER A: nothing but the relay -------------------------------------
	// It must already see the trail (the fold splits the legacy card), and every
	// recovered note must carry NO MsgID — the marker that says "this note has no
	// event of its own yet", which is what makes the next write mint one.
	eventsA := reconcileFresh(t, relay, boardCoord, trust)
	itemA := ProjectItems(eventsA, opts)[itemID]
	if itemA == nil {
		t.Fatalf("relay-only reader A does not see %s at all", itemID)
	}
	if itemA.Context != base {
		t.Fatalf("reader A Context = %.80q, want the base description alone — SplitCardTrail did not run on the DECRYPTED card", itemA.Context)
	}
	if len(itemA.Notes) != len(legacy) {
		t.Fatalf("reader A recovered %d notes from the legacy card, want %d", len(itemA.Notes), len(legacy))
	}
	for i, n := range itemA.Notes {
		if n.MsgID != "" {
			t.Fatalf("recovered note %d already carries MsgID %q — it would not be pending and the recovery below would prove nothing", i, n.MsgID)
		}
	}
	// The legacy card the relay is holding is sealed: the trail is inline in its
	// Content, so if the envelope were missing, the relay would be holding the
	// entire trail in the clear.
	assertRelayServedNoPlaintext(t, eventsA, secretWords)

	// ---- THE RECOVERY: one ordinary write ------------------------------------
	// CardSpecFromItem carries the compacted Context plus the pending notes, which
	// is exactly what `rd progress` hands PublishNote in production.
	card := CardSpecFromItem(itemA, boardD)
	card.Enc = env
	if len(card.PendingNotes) != len(legacy) {
		t.Fatalf("CardSpecFromItem carried %d pending notes, want %d", len(card.PendingNotes), len(legacy))
	}
	const liveNote = "the credential rotated and the recovery landed"
	recRes, recErr := pub.PublishNote(context.Background(), card, state.ProgressNote{
		At: "2026-07-31T09:00Z", Text: liveNote,
	}, now+60)
	mustAccept("recovery write", recRes, recErr)

	// ---- READER B: a SECOND process whose only source is the relay ------------
	// strfry has by now discarded the legacy card (one 30302 per coordinate), so
	// anything that survived only inside it is gone from the network.
	eventsB := reconcileFresh(t, relay, boardCoord, trust)
	itemB := ProjectItems(eventsB, opts)[itemID]
	if itemB == nil {
		t.Fatalf("relay-only reader B does not see %s after the recovery", itemID)
	}

	// (a) THE CARD THE RELAY NOW HOLDS IS SMALL. Measured on the event strfry
	// actually served, against the relay's own oversize expression.
	var served *nostr.Event
	for _, e := range eventsB {
		if e != nil && e.Kind == KindCard && itemIDForEvent(e) == itemID {
			served = e
		}
	}
	if served == nil {
		t.Fatalf("the relay served no card for %s", itemID)
	}
	blob, err := json.Marshal(served)
	if err != nil {
		t.Fatalf("marshal served card: %v", err)
	}
	if len(blob) > 8*1024 {
		t.Errorf("the card the relay holds after recovery is %d bytes — it is still absorbing the trail", len(blob))
	}
	t.Logf("card on the relay after recovery: %d bytes (legacy inline content was %d bytes of context)", len(blob), len(inline))

	want := append(append([]string{}, legacy...), liveNote)

	// (b) CONFIDENTIALITY, ON THE BYTES STRFRY HANDED BACK. Asserted BEFORE the
	// trail assertion on purpose: an unsealed note is quarantined by the
	// fail-closed fold gate and so ALSO shows up as a missing note, and
	// "13 notes became 1" is a much worse diagnosis than "this note is in the
	// clear on the relay".
	notes := 0
	for _, e := range eventsB {
		if e == nil || e.Kind != KindNote {
			continue
		}
		notes++
		if !encWellFormed(e) {
			t.Errorf("kind-1111 note %s served by the relay has no well-formed envelope — it was published UNSEALED on a confidential board (enc=%q, content=%.40q)", e.ID, tagValue(e, tagEnc), e.Content)
		}
	}
	if notes != len(want) {
		t.Fatalf("the relay served %d kind-1111 events, want %d — the recovery did not publish one event per note it compacted out of the card", notes, len(want))
	}
	assertRelayServedNoPlaintext(t, eventsB, secretWords)

	// (c) NOTHING WAS LOST. Every legacy note, in order, plus the new one — for a
	// reader that never had the writer's local log.
	if len(itemB.Notes) != len(want) {
		t.Fatalf("relay-only reader B sees %d notes, want %d — the compacted card destroyed history on the network", len(itemB.Notes), len(want))
	}
	for i, w := range want {
		if itemB.Notes[i].Text != w {
			t.Fatalf("note %d reads back as %.60q, want %.60q", i, itemB.Notes[i].Text, w)
		}
		if itemB.Notes[i].MsgID == "" {
			t.Errorf("note %d has no MsgID — it is not backed by an event the relay served", i)
		}
	}
	if itemB.Context != base {
		t.Errorf("reader B Context = %.80q, want the base description alone", itemB.Context)
	}

	t.Logf("PROVEN on %s: a %d-note inline trail compacted to a %d-byte card, every note preserved and sealed, read back by a process holding only the relay", relay, len(legacy), len(blob))
}

// reconcileFresh reconciles boardCoord from relay into a BRAND-NEW empty log and
// returns everything it fetched. Every read in this file goes through here, so
// no assertion can accidentally consult the writer's local log.
//
// It uses ReconcileBoard, whose filter is scoped by the "#a" board coordinate —
// never an authors filter, which silently under-returns on some relays.
func reconcileFresh(t *testing.T, relay, boardCoord string, trust map[string]bool) []*nostr.Event {
	t.Helper()
	log := NewNostrLog(filepath.Join(t.TempDir(), ".ready", NostrLogFile))
	res, err := ReconcileBoard(context.Background(), []string{relay}, log, boardCoord, trust, 20*time.Second)
	if err != nil {
		t.Fatalf("ReconcileBoard(%s): %v", boardCoord, err)
	}
	if len(res.RelayErrors) > 0 {
		t.Fatalf("ReconcileBoard(%s) relay errors: %v", boardCoord, res.RelayErrors)
	}
	events, err := log.ReadAll()
	if err != nil {
		t.Fatalf("read fresh log: %v", err)
	}
	t.Logf("relay-only reader: fetched=%d added=%d events=%d", res.Fetched, res.Added, len(events))
	return events
}

// assertRelayServedNoPlaintext fails if any of `secrets` appears in the wire
// bytes of anything the relay served — Content or tags, any kind.
func assertRelayServedNoPlaintext(t *testing.T, events []*nostr.Event, secrets []string) {
	t.Helper()
	for _, e := range events {
		if e == nil {
			continue
		}
		blob, err := json.Marshal(e)
		if err != nil {
			t.Fatalf("marshal kind-%d event: %v", e.Kind, err)
		}
		for _, s := range secrets {
			if strings.Contains(string(blob), s) {
				t.Fatalf("the relay is holding confidential free text (%q) IN THE CLEAR on a kind-%d event: %s", s, e.Kind, blob)
			}
		}
	}
}
