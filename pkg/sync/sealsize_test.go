package sync

import (
	"strings"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// TestProjectSealedWireSize_MatchesARealSeal is the assertion the whole disposition
// list rests on: the projection must equal what re-sealing ACTUALLY produces.
//
// A projection that is merely "close" is useless here. The number decides whether a
// coordinate goes on a halt-the-pass list, and the failure it exists to prevent is a
// board-wide operation stopping partway through on a card nobody knew about. So this
// seals the same card for real, with a real envelope through BuildCardEvent, and
// requires the byte counts to MATCH — not to be within a tolerance.
func TestProjectSealedWireSize_MatchesARealSeal(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	now := time.Now().Unix()
	spec := CardSpec{
		ItemID: "size-1", Title: "a readable title of some length", Status: state.StatusActive,
		Priority: "p1", Type: "task", BoardD: "sizeboard", Context: strings.Repeat("body text. ", 200),
		Labels: []string{"alpha", "beta"}, WaitingOn: "someone else",
		Level: "subtask", ParentID: "size-parent", ETA: "2026-09-01T00:00:00Z",
	}
	plain, err := BuildCardEvent(owner, spec, now)
	if err != nil {
		t.Fatalf("plaintext card: %v", err)
	}

	got, err := ProjectSealedWireSize(plain)
	if err != nil {
		t.Fatalf("ProjectSealedWireSize: %v", err)
	}

	// The real thing, sealed through the production builder with a real envelope
	// and an LTK (the larger label form the projection assumes).
	var cek, ltk [32]byte
	cek[0], ltk[0] = 7, 9
	sealedSpec := spec
	sealedSpec.Enc = &Envelope{CEK: cek, Epoch: cekEpochSizeCeiling, LTK: &ltk}
	real, err := BuildCardEvent(owner, sealedSpec, now)
	if err != nil {
		t.Fatalf("sealed card: %v", err)
	}
	wantSealed, err := marshaledEventSize(real)
	if err != nil {
		t.Fatalf("marshaledEventSize: %v", err)
	}
	if got.SealedBytes != wantSealed {
		t.Fatalf("projected sealed size %d, a real seal of the same card is %d — the projection does not model what re-sealing produces",
			got.SealedBytes, wantSealed)
	}
	wantPlain, err := marshaledEventSize(plain)
	if err != nil {
		t.Fatalf("marshaledEventSize: %v", err)
	}
	if got.PlaintextBytes != wantPlain {
		t.Fatalf("projected plaintext size %d, want %d", got.PlaintextBytes, wantPlain)
	}
	if got.SealedBytes <= got.PlaintextBytes {
		t.Fatalf("sealed %d is not larger than plaintext %d — sealing always grows an event, so a projection that says otherwise is wrong",
			got.SealedBytes, got.PlaintextBytes)
	}
	if got.Limit != maxEventWireSize {
		t.Fatalf("projection recorded limit %d, want %d", got.Limit, maxEventWireSize)
	}
}

// TestProjectSealedWireSize_CatchesTheCardThatOnlyBreAKSOnceSealed is the population
// this item exists to find: a card comfortably UNDER the relay's ceiling today whose
// sealed form is over it. Nothing about the card as served says so, and the re-seal
// pass would halt on it.
func TestProjectSealedWireSize_CatchesTheCardThatOnlyBreaksOnceSealed(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	// Binary-search a context length whose plaintext card fits and whose sealed
	// form does not, rather than hard-coding a byte count that would rot the first
	// time a tag is added to the card shape.
	build := func(n int) *nostr.Event {
		e, err := BuildCardEvent(owner, CardSpec{
			ItemID: "big-1", Title: "big", Status: state.StatusActive, Priority: "p2",
			Type: "task", BoardD: "sizeboard", Context: strings.Repeat("x", n),
		}, time.Now().Unix())
		if err != nil {
			t.Fatalf("build card: %v", err)
		}
		return e
	}
	lo, hi := 1, maxEventWireSize
	for lo < hi {
		mid := (lo + hi) / 2
		n, err := marshaledEventSize(build(mid))
		if err != nil {
			t.Fatalf("marshaledEventSize: %v", err)
		}
		if n > maxEventWireSize {
			hi = mid
		} else {
			lo = mid + 1
		}
	}
	// The largest context that still fits as PLAINTEXT.
	edge := build(lo - 1)
	plainBytes, err := marshaledEventSize(edge)
	if err != nil {
		t.Fatalf("marshaledEventSize: %v", err)
	}
	if plainBytes > maxEventWireSize {
		t.Fatalf("fixture is wrong: the plaintext edge card is already %d bytes, over the %d limit", plainBytes, maxEventWireSize)
	}

	got, err := ProjectSealedWireSize(edge)
	if err != nil {
		t.Fatalf("ProjectSealedWireSize: %v", err)
	}
	if !got.OverLimit {
		t.Fatalf("a card at the plaintext ceiling (%d bytes) projects to %d sealed and was NOT flagged over the %d limit — this is exactly the coordinate that halts the pass",
			plainBytes, got.SealedBytes, got.Limit)
	}
	// And the ordinary small card must NOT be flagged, or the list is noise.
	small, err := ProjectSealedWireSize(build(100))
	if err != nil {
		t.Fatalf("ProjectSealedWireSize small: %v", err)
	}
	if small.OverLimit {
		t.Fatalf("a 100-byte-context card was flagged over the limit (%d sealed) — a disposition list nobody can trust is worse than none", small.SealedBytes)
	}
}

// TestProjectSealedWireSize_RefusesWhatItCannotProject: an already-sealed card and a
// non-card event have no plaintext free-text set. Returning a number for them would
// put a meaningless row into a list a human is going to act on.
func TestProjectSealedWireSize_RefusesWhatItCannotProject(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	var cek [32]byte
	sealed, err := BuildCardEvent(owner, CardSpec{
		ItemID: "s-1", Title: "t", Status: state.StatusActive, Priority: "p2", Type: "task",
		BoardD: "sizeboard", Enc: &Envelope{CEK: cek, Epoch: 1},
	}, time.Now().Unix())
	if err != nil {
		t.Fatalf("sealed card: %v", err)
	}
	if _, err := ProjectSealedWireSize(sealed); err == nil {
		t.Fatal("projected a sealed card as if it were plaintext")
	}
	board, err := BuildBoardEvent(owner, BoardSpec{BoardD: "sizeboard", Title: "sizeboard"}, time.Now().Unix())
	if err != nil {
		t.Fatalf("board event: %v", err)
	}
	if _, err := ProjectSealedWireSize(board); err == nil {
		t.Fatal("projected a kind-30301 board definition as if it were a card")
	}
	if _, err := ProjectSealedWireSize(nil); err == nil {
		t.Fatal("projected a nil event")
	}
}

// TestProjectSealedWireSize_LabelsCountedAtTheirCeiling pins the deliberate
// conservatism: a board with an LTK emits a 64-hex-character `l` token per label and a
// board without one emits no `l` tag at all, so the tokenized form is the larger.
// Under-reporting here is what puts a halting coordinate on nobody's list.
func TestProjectSealedWireSize_LabelsCountedAtTheirCeiling(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	now := time.Now().Unix()
	mk := func(labels []string) *nostr.Event {
		e, err := BuildCardEvent(owner, CardSpec{
			ItemID: "lbl-1", Title: "t", Status: state.StatusActive, Priority: "p2",
			Type: "task", BoardD: "sizeboard", Labels: labels,
		}, now)
		if err != nil {
			t.Fatalf("build: %v", err)
		}
		return e
	}
	none, err := ProjectSealedWireSize(mk(nil))
	if err != nil {
		t.Fatalf("project none: %v", err)
	}
	three, err := ProjectSealedWireSize(mk([]string{"a", "b", "c"}))
	if err != nil {
		t.Fatalf("project three: %v", err)
	}
	// Three tokenized labels: 3 tags of ["l","<64 hex>"] plus the labels inside the
	// sealed blob. The exact figure is not the point; that it GROWS, materially, is.
	if three.SealedBytes-none.SealedBytes < 3*64 {
		t.Fatalf("three labels added only %d bytes over none (%d -> %d); tokenized labels are 64 hex characters each, so the projection is not counting them at their ceiling",
			three.SealedBytes-none.SealedBytes, none.SealedBytes, three.SealedBytes)
	}
	// A board with no LTK emits NO clear l tag, so the real sealed card is smaller
	// than this projection — over-reporting, which is the safe direction.
	var cek [32]byte
	noLTK, err := BuildCardEvent(owner, CardSpec{
		ItemID: "lbl-1", Title: "t", Status: state.StatusActive, Priority: "p2",
		Type: "task", BoardD: "sizeboard", Labels: []string{"a", "b", "c"},
		Enc: &Envelope{CEK: cek, Epoch: cekEpochSizeCeiling},
	}, now)
	if err != nil {
		t.Fatalf("no-LTK sealed card: %v", err)
	}
	n, err := marshaledEventSize(noLTK)
	if err != nil {
		t.Fatalf("marshaledEventSize: %v", err)
	}
	if three.SealedBytes < n {
		t.Fatalf("projection %d UNDER-reports the no-LTK real seal %d — the projection must never be smaller than what a board actually publishes", three.SealedBytes, n)
	}
}
