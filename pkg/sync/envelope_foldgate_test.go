package sync

// Fail-closed fold gate tests (ready-710). On a confidential board the local
// projection quarantines any card lacking a well-formed enc envelope, grandfathering
// only genuine pre-cutover plaintext cards — strfry can't validate payload shape,
// so this local fold is the single enforcement point.

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// stubEncBoards is a test EncryptedBoardSet: a fixed coord→cutover map standing in
// for keydist's per-board CEK-epoch state.
type stubEncBoards struct {
	cutover map[string]int64
}

func (s stubEncBoards) Cutover(coord string) (int64, bool) {
	c, ok := s.cutover[coord]
	return c, ok
}

// signCard builds a plaintext-style card then mutates it into the exact shape the
// test needs (extra tags / Content) and RE-SIGNS it with owner, so the projection's
// signature + author checks pass. Used to craft malformed-confidential cards the
// normal builder would never emit.
func signCard(t *testing.T, owner *nostr.Key, itemID string, createdAt int64, extraTags [][]string, content string) *nostr.Event {
	t.Helper()
	spec := CardSpec{ItemID: itemID, Title: "plain", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready"}
	e, err := BuildCardEvent(owner, spec, createdAt)
	if err != nil {
		t.Fatalf("build base card: %v", err)
	}
	e.Tags = append(e.Tags, extraTags...)
	if content != "" {
		e.Content = content
	}
	if err := e.Sign(owner); err != nil {
		t.Fatalf("re-sign: %v", err)
	}
	return e
}

func TestFoldGateQuarantine(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("owner key: %v", err)
	}
	boardCoord := BoardCoord(owner.PubKeyHex(), "ready")
	const cutover int64 = 1_700_000_500
	preCut := cutover - 100  // genuine pre-cutover
	postCut := cutover + 100 // after the board went confidential

	be, err := BuildBoardEvent(owner, BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{owner.PubKeyHex()}}, 1_699_000_000)
	if err != nil {
		t.Fatalf("board: %v", err)
	}

	// 1. POST-cutover plaintext card → quarantined.
	postPlain := signCard(t, owner, "ready-postplain", postCut, nil, "")

	// 2. PRE-cutover plaintext card → grandfathered.
	prePlain := signCard(t, owner, "ready-preplain", preCut, nil, "")

	// 3. well-formed encrypted card (post-cutover) → materializes.
	env := &Envelope{CEK: cekBytes(0x20), Epoch: 1}
	goodEncSpec := CardSpec{ItemID: "ready-goodenc", Title: "secret", Status: state.StatusActive, Priority: "p1", Type: "task", BoardD: "ready", Context: "hidden desc", Enc: env}
	goodEnc, err := BuildCardEvent(owner, goodEncSpec, postCut)
	if err != nil {
		t.Fatalf("good enc card: %v", err)
	}

	// 4a. v-shaped card with SMUGGLED cleartext (enc marker present, Content is
	//     readable plaintext, not base64 ciphertext) → quarantined.
	smuggled := signCard(t, owner, "ready-smuggled", postCut,
		[][]string{{"enc", "1"}, {"cek_epoch", "1"}}, "SMUGGLED CLEARTEXT title=secret plan")

	// 4b. v-shaped card with UNKNOWN enc version → quarantined.
	badVersion := signCard(t, owner, "ready-badver", postCut,
		[][]string{{"enc", "9"}, {"cek_epoch", "1"}}, "cQ==cQ==cQ==cQ==cQ==cQ==cQ==cQ==cQ==cQ==")

	events := []*nostr.Event{be, postPlain, prePlain, goodEnc, smuggled, badVersion}
	opts := ProjectOptions{
		Trusted:         map[string]bool{owner.PubKeyHex(): true},
		PinnedBoard:     boardCoord,
		EncryptedBoards: stubEncBoards{cutover: map[string]int64{boardCoord: cutover}},
	}
	got := ProjectItems(events, opts)

	present := func(id string) bool { _, ok := got[id]; return ok }

	if present("ready-postplain") {
		t.Error("post-cutover PLAINTEXT card was NOT quarantined (confidentiality bypass)")
	}
	if !present("ready-preplain") {
		t.Error("pre-cutover plaintext card was NOT grandfathered")
	}
	if !present("ready-goodenc") {
		t.Error("well-formed encrypted card was wrongly quarantined")
	}
	if present("ready-smuggled") {
		t.Error("v-shaped card with smuggled cleartext was NOT quarantined")
	}
	if present("ready-badver") {
		t.Error("card with unknown enc version was NOT quarantined")
	}
}

// TestFoldGateInertWhenPlaintextBoard: a board NOT in the encrypted set keeps
// every plaintext card (the gate must not touch normal boards).
func TestFoldGateInertWhenPlaintextBoard(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("owner key: %v", err)
	}
	boardCoord := BoardCoord(owner.PubKeyHex(), "ready")
	be, _ := BuildBoardEvent(owner, BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{owner.PubKeyHex()}}, 1_699_000_000)
	plain := signCard(t, owner, "ready-plain", 1_700_000_000, nil, "")

	// EncryptedBoards names a DIFFERENT board, so this board is plaintext-legal.
	opts := ProjectOptions{
		Trusted:         map[string]bool{owner.PubKeyHex(): true},
		PinnedBoard:     boardCoord,
		EncryptedBoards: stubEncBoards{cutover: map[string]int64{"30301:other:board": 1}},
	}
	got := ProjectItems([]*nostr.Event{be, plain}, opts)
	if _, ok := got["ready-plain"]; !ok {
		t.Fatal("plaintext card on a non-confidential board was wrongly quarantined — gate not inert")
	}

	// And with a nil set the gate is fully inert.
	got2 := ProjectItems([]*nostr.Event{be, plain}, ProjectOptions{Trusted: map[string]bool{owner.PubKeyHex(): true}, PinnedBoard: boardCoord})
	if _, ok := got2["ready-plain"]; !ok {
		t.Fatal("nil EncryptedBoards must leave the gate inert")
	}
}
