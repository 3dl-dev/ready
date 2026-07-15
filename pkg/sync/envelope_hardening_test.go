package sync

// Hardening regression tests (ready-47a) for three confirmed findings from the
// ready-216 adversarial security review:
//  1. no clear label tag on a confidential board when the LTK is absent;
//  2. the fold gate quarantines plaintext NIP-34 status events too;
//  3. DeriveBoardKeyring rejects a CEK-bearing grant with a malformed/invalid epoch.

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// Fix 1: a confidential card with NO LTK must not emit a plaintext l tag; the
// labels still ride in the sealed Content blob for member rendering.
func TestConfidentialCardNoClearLabelWithoutLTK(t *testing.T) {
	k := testKey(t)
	var cek [32]byte
	for i := range cek {
		cek[i] = byte(i + 2)
	}
	env := &Envelope{CEK: cek, Epoch: 1} // LTK deliberately nil
	spec := CardSpec{
		ItemID: "ready-h1", Title: "t", Context: "c", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: "ready",
		Labels: []string{"secret-codename", "urgent"}, Enc: env,
	}
	ce, err := BuildCardEvent(k, spec, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildCardEvent: %v", err)
	}
	for _, tg := range ce.Tags {
		if len(tg) >= 1 && tg[0] == "l" {
			t.Fatalf("confidential card (nil LTK) emitted a clear l tag %v — label value leaked", tg)
		}
	}
	// Labels still recoverable by a member from the sealed blob.
	pt, err := openContent(cek, ce.Content)
	if err != nil {
		t.Fatalf("openContent: %v", err)
	}
	var pl cardPayload
	if err := json.Unmarshal(pt, &pl); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(pl.Labels) != 2 || pl.Labels[0] != "secret-codename" {
		t.Fatalf("labels not preserved in sealed blob: %v", pl.Labels)
	}
}

// Fix 2: a POST-cutover plaintext status event on a confidential board is
// quarantined (its cleartext reason never folds into history), while a
// well-formed encrypted status event folds normally.
func TestFoldGateQuarantinesPlaintextStatusEvents(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("owner key: %v", err)
	}
	boardCoord := BoardCoord(owner.PubKeyHex(), "ready")
	const cutover int64 = 1_700_000_500
	be, _ := BuildBoardEvent(owner, BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{owner.PubKeyHex()}}, 1_699_000_000)
	opts := ProjectOptions{
		Trusted:         map[string]bool{owner.PubKeyHex(): true},
		PinnedBoard:     boardCoord,
		EncryptedBoards: stubEncBoards{cutover: map[string]int64{boardCoord: cutover}},
	}

	// Item A: card + a POST-cutover PLAINTEXT status event with a cleartext reason.
	envA := &Envelope{CEK: cekBytes(0x30), Epoch: 1}
	cardA, _ := BuildCardEvent(owner, CardSpec{ItemID: "ready-hsa", Title: "x", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready", Enc: envA}, cutover+50)
	plainStatus, _ := BuildStatusEventWithIssueRoot(owner, "ready-hsa", state.StatusDone, cardA.ID, "", boardCoord, "SECRET CLEARTEXT REASON", cutover+100, nil) // env=nil → plaintext
	gotA := ProjectItems([]*nostr.Event{be, cardA, plainStatus}, opts)["ready-hsa"]
	if gotA == nil {
		t.Fatal("item A missing")
	}
	for _, h := range gotA.History {
		if strings.Contains(h.Note, "SECRET CLEARTEXT REASON") {
			t.Fatal("plaintext status event's cleartext reason folded into history — not quarantined")
		}
	}
	if gotA.Status == state.StatusDone {
		t.Fatal("plaintext status transition folded on a confidential board — should be quarantined")
	}

	// Item B: card + a WELL-FORMED ENCRYPTED status event → folds (status → done).
	envB := &Envelope{CEK: cekBytes(0x60), Epoch: 1}
	cardB, _ := BuildCardEvent(owner, CardSpec{ItemID: "ready-hsb", Title: "y", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready", Enc: envB}, cutover+50)
	encStatus, _ := BuildStatusEventWithIssueRoot(owner, "ready-hsb", state.StatusDone, cardB.ID, "", boardCoord, "sealed reason", cutover+100, envB)
	gotB := ProjectItems([]*nostr.Event{be, cardB, encStatus}, opts)["ready-hsb"]
	if gotB == nil {
		t.Fatal("item B missing")
	}
	if gotB.Status != state.StatusDone {
		t.Fatalf("well-formed encrypted status event was wrongly quarantined: status=%q", gotB.Status)
	}
}

// Fix 3: a CEK-bearing grant with an invalid (0/unparseable) epoch is rejected —
// no CEK stored, no cutover set.
func TestKeydistRejectsMalformedCEKEpoch(t *testing.T) {
	owner := kdKey(t)
	m := kdKey(t)
	boardCoord := BoardCoord(owner.PubKeyHex(), "ready")
	cek, _ := MintKey()
	grant := kdGrant(t, owner, RoleGrantSpec{
		BoardD: "ready", BoardAuthor: owner.PubKeyHex(), Grantee: m.PubKeyHex(),
		Role: RoleContributor, WrappedCEK: kdWrap(t, owner, m.PubKeyHex(), cek), CEKEpoch: 0,
	}, 1_700_000_000)
	kr := DeriveBoardKeyring([]*nostr.Event{grant}, m, owner.PubKeyHex(), "ready")
	if _, ok := kr.CEK(boardCoord, 0); ok {
		t.Fatal("epoch-0 (invalid) CEK was accepted")
	}
	if _, ok := kr.Cutover(boardCoord); ok {
		t.Fatal("a malformed-epoch grant set the board cutover — board falsely confidential")
	}
}
