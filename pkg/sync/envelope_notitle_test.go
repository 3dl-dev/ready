package sync

// Regression test (ready-02e round 2). decryptCardPayload previously returned
// ok=true, Title=="" for a sealed payload of the shape {"context":"..."} — a
// JSON object that opens (AEAD succeeds) but carries no "title" key at all.
// json.Unmarshal into the typed cardPayload struct cannot distinguish a MISSING
// key from a present-but-empty one (both leave Title==""), so the read path
// (itemFromCard, pkg/sync/nostrproject.go) took the SUCCESS branch: it set
// item.Redacted=false and item.Title="" instead of failing closed to the
// placeholder. That is the exact silent-blank envelope.ts's decryptCardPayload
// was fixed to reject in the same round; this test proves the Go side now
// agrees with it (see envelope.go's decryptCardPayload / cardPayloadWellFormed
// doc comments).

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestDecryptCardPayloadFailsClosedOnMissingTitle is the unit-level proof: a
// sealed blob that opens to a JSON object with no "title" key must NOT
// decrypt successfully.
func TestDecryptCardPayloadFailsClosedOnMissingTitle(t *testing.T) {
	k := testKey(t)
	cek := cekBytes(0x11)
	env := &Envelope{CEK: cek, Epoch: 1}
	spec := CardSpec{
		ItemID: "ready-nt1", Title: "placeholder title", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: "ready", Enc: env,
	}
	ce, err := BuildCardEvent(k, spec, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}

	// Reseal Content with a payload that opens fine but carries NO "title" key
	// at all — the exact shape the adversary reproduced. Re-sign afterward: the
	// original signature covers the original Content, and decryptCardPayload
	// does not itself re-verify the event (the caller already did), but
	// resigning keeps ce well-formed for callers (like the end-to-end test
	// below) that DO verify.
	sealed, err := sealContent(cek, []byte(`{"context":"a body with no title field"}`))
	if err != nil {
		t.Fatalf("sealContent: %v", err)
	}
	ce.Content = sealed
	if err := ce.Sign(k); err != nil {
		t.Fatalf("re-sign: %v", err)
	}

	dec := newMapDecryptor()
	dec.add(boardCoordOf(ce), 1, cek)

	if pl, ok := decryptCardPayload(ce, dec); ok {
		t.Fatalf("CRITICAL: decryptCardPayload returned ok=true for a title-less payload: title=%q context=%q", pl.Title, pl.Context)
	}
}

// TestDecryptCardPayloadFailsClosedOnNonObject is the non-object sibling: a
// sealed blob that opens to a JSON array (or any non-object) must also fail
// closed, matching decodeCardPayload's TS-side shape gate.
func TestDecryptCardPayloadFailsClosedOnNonObject(t *testing.T) {
	k := testKey(t)
	cek := cekBytes(0x12)
	env := &Envelope{CEK: cek, Epoch: 1}
	spec := CardSpec{
		ItemID: "ready-nt2", Title: "placeholder title", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: "ready", Enc: env,
	}
	ce, err := BuildCardEvent(k, spec, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	sealed, err := sealContent(cek, []byte(`["not","an","object"]`))
	if err != nil {
		t.Fatalf("sealContent: %v", err)
	}
	ce.Content = sealed
	if err := ce.Sign(k); err != nil {
		t.Fatalf("re-sign: %v", err)
	}

	dec := newMapDecryptor()
	dec.add(boardCoordOf(ce), 1, cek)

	if pl, ok := decryptCardPayload(ce, dec); ok {
		t.Fatalf("CRITICAL: decryptCardPayload returned ok=true for a non-object payload: title=%q", pl.Title)
	}
}

// TestProjectItemsRedactsMissingTitlePayload is the end-to-end proof through
// itemFromCard/ProjectItems: a granted reader of a title-less confidential
// card sees the [encrypted] placeholder AND Redacted=true — not a blank title
// with Redacted=false, which would look like a legitimately empty-titled card
// to every downstream consumer (rd list/show, board render).
func TestProjectItemsRedactsMissingTitlePayload(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("owner key: %v", err)
	}
	boardCoord := BoardCoord(owner.PubKeyHex(), "ready")
	be, _ := BuildBoardEvent(owner, BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{owner.PubKeyHex()}}, 1_699_000_000)

	cek := cekBytes(0x13)
	env := &Envelope{CEK: cek, Epoch: 1}
	card, err := BuildCardEvent(owner, CardSpec{
		ItemID: "ready-nt3", Title: "placeholder title", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: "ready", Enc: env,
	}, 1_700_000_100)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	sealed, err := sealContent(cek, []byte(`{"context":"a body with no title field"}`))
	if err != nil {
		t.Fatalf("sealContent: %v", err)
	}
	card.Content = sealed
	if err := card.Sign(owner); err != nil {
		t.Fatalf("re-sign: %v", err)
	}

	dec := newMapDecryptor()
	dec.add(boardCoord, 1, cek)

	opts := ProjectOptions{
		Trusted:         map[string]bool{owner.PubKeyHex(): true},
		PinnedBoard:     boardCoord,
		EncryptedBoards: stubEncBoards{cutover: map[string]int64{boardCoord: 1_700_000_000}},
		Decryptor:       dec,
	}
	got := ProjectItems([]*nostr.Event{be, card}, opts)["ready-nt3"]
	if got == nil {
		t.Fatal("item missing")
	}
	if !got.Redacted {
		t.Fatalf("CRITICAL: title-less confidential payload projected as NOT redacted (Title=%q) — indistinguishable from a real empty-title card", got.Title)
	}
	if got.Title != placeholderText {
		t.Fatalf("Title = %q, want placeholder %q", got.Title, placeholderText)
	}
}
