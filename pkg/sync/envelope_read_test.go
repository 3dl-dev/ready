package sync

// Decrypting read-path tests (ready-ce2). A granted member's projection renders
// the exact plaintext title/description/close-reason/labels; a non-member (or a
// member lacking the card's epoch key) sees a placeholder for the free-text
// fields while every clear routing field still renders — no error, no panic.

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

// mapDecryptor is a test BoardDecryptor backed by an explicit (coord,epoch)→CEK
// map, standing in for keydist's grant-unwrap. A member holds the entries for the
// epochs it was granted; a non-member holds none.
type mapDecryptor struct {
	keys map[string][32]byte
}

func newMapDecryptor() *mapDecryptor { return &mapDecryptor{keys: map[string][32]byte{}} }

func (m *mapDecryptor) add(coord string, epoch int, cek [32]byte) {
	m.keys[coord+"|"+itoa(epoch)] = cek
}

func (m *mapDecryptor) CEK(coord string, epoch int) ([32]byte, bool) {
	cek, ok := m.keys[coord+"|"+itoa(epoch)]
	return cek, ok
}

func itoa(i int) string {
	// tiny local int→string to avoid importing strconv just for tests
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}

func cekBytes(seed byte) [32]byte {
	var c [32]byte
	for i := range c {
		c[i] = seed + byte(i)
	}
	return c
}

// buildConfidentialItem returns the full event set (board, encrypted card,
// encrypted status) for an item authored by owner under env, with the given
// known plaintext.
func buildConfidentialItem(t *testing.T, owner *nostr.Key, itemID, title, desc, waitingOn, reason string, env *Envelope) []*nostr.Event {
	t.Helper()
	board := BoardSpec{BoardD: "ready", Title: "ready", Maintainers: []string{owner.PubKeyHex()}}
	be, err := BuildBoardEvent(owner, board, 1_700_000_000)
	if err != nil {
		t.Fatalf("board event: %v", err)
	}
	spec := CardSpec{
		ItemID: itemID, Title: title, Status: state.StatusActive, Priority: "p1",
		Type: "task", Context: desc, BoardD: "ready", WaitingOn: waitingOn,
		Labels: []string{"security", "urgent"}, Deps: []string{"ready-dep"}, Enc: env,
	}
	ce, err := BuildCardEvent(owner, spec, 1_700_000_100)
	if err != nil {
		t.Fatalf("card event: %v", err)
	}
	boardCoord := BoardCoord(owner.PubKeyHex(), "ready")
	se, err := BuildStatusEventWithIssueRoot(owner, itemID, state.StatusActive, ce.ID, "", boardCoord, reason, 1_700_000_200, env)
	if err != nil {
		t.Fatalf("status event: %v", err)
	}
	return []*nostr.Event{be, ce, se}
}

func TestConfidentialReadPath(t *testing.T) {
	owner, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("owner key: %v", err)
	}
	boardCoord := BoardCoord(owner.PubKeyHex(), "ready")
	var ltk [32]byte
	for i := range ltk {
		ltk[i] = byte(0x11 + i)
	}
	cek1 := cekBytes(0x40)
	env := &Envelope{CEK: cek1, Epoch: 1, LTK: &ltk}

	title := "Rotate the leaked pager secret"
	desc := "the pager secret leaked in a screenshot; rotate and audit access"
	waitingOn := "ready-blocker"
	reason := "escalated to security on-call"
	events := buildConfidentialItem(t, owner, "ready-cread1", title, desc, waitingOn, reason, env)

	trusted := map[string]bool{owner.PubKeyHex(): true}
	baseOpts := func(dec BoardDecryptor) ProjectOptions {
		return ProjectOptions{Trusted: trusted, PinnedBoard: boardCoord, Decryptor: dec}
	}

	// GRANTED MEMBER A: decryptor holds the epoch-1 CEK for this board.
	memberA := newMapDecryptor()
	memberA.add(boardCoord, 1, cek1)
	asA := ProjectItems(events, baseOpts(memberA))
	itemA, ok := asA["ready-cread1"]
	if !ok {
		t.Fatal("member A: item missing from projection")
	}
	if itemA.Title != title {
		t.Fatalf("member A title = %q, want %q", itemA.Title, title)
	}
	if itemA.Description != desc {
		t.Fatalf("member A description = %q, want %q", itemA.Description, desc)
	}
	if itemA.WaitingOn != waitingOn {
		t.Fatalf("member A waiting_on = %q, want %q", itemA.WaitingOn, waitingOn)
	}
	if len(itemA.History) == 0 || itemA.History[len(itemA.History)-1].Note != reason {
		t.Fatalf("member A close reason not decrypted: history=%+v", itemA.History)
	}
	// Labels rendered as human plaintext from the sealed blob.
	if len(itemA.Labels) != 2 || itemA.Labels[0] != "security" || itemA.Labels[1] != "urgent" {
		t.Fatalf("member A labels = %v, want [security urgent]", itemA.Labels)
	}

	// NON-MEMBER B: no decryptor. Free text is placeholder; routing fields intact.
	asB := ProjectItems(events, baseOpts(nil))
	itemB, ok := asB["ready-cread1"]
	if !ok {
		t.Fatal("non-member B: item missing from projection")
	}
	if itemB.Title != placeholderText || itemB.Description != placeholderText {
		t.Fatalf("non-member B free text not placeholdered: title=%q desc=%q", itemB.Title, itemB.Description)
	}
	if itemB.WaitingOn != "" {
		t.Fatalf("non-member B leaked waiting_on: %q", itemB.WaitingOn)
	}
	if len(itemB.History) == 0 || itemB.History[len(itemB.History)-1].Note != placeholderText {
		t.Fatalf("non-member B close reason not placeholdered: history=%+v", itemB.History)
	}
	// Clear routing fields still render correctly for the non-member (Status,
	// Priority, Type come straight from clear tags — untouched by decryption).
	if itemB.Status != state.StatusActive || itemB.Priority != "p1" || itemB.Type != "task" {
		t.Fatalf("non-member B routing fields wrong: status=%q priority=%q type=%q", itemB.Status, itemB.Priority, itemB.Type)
	}
	// Non-member sees the opaque tokens for labels (present, not readable, count preserved).
	if len(itemB.Labels) != 2 {
		t.Fatalf("non-member B label count = %d, want 2 (opaque tokens)", len(itemB.Labels))
	}
	if itemB.Labels[0] == "security" {
		t.Fatalf("non-member B read a plaintext label: %v", itemB.Labels)
	}

	// EPOCH SCOPING: a card minted under epoch 2 is a placeholder for A, who holds
	// only the epoch-1 key — proving forward-secrecy epoch scoping at read time.
	cek2 := cekBytes(0x90)
	env2 := &Envelope{CEK: cek2, Epoch: 2, LTK: &ltk}
	events2 := buildConfidentialItem(t, owner, "ready-cread2", "epoch2 title", "epoch2 desc", "", "epoch2 reason", env2)
	asAEpoch := ProjectItems(events2, baseOpts(memberA)) // memberA holds epoch 1 only
	itemE, ok := asAEpoch["ready-cread2"]
	if !ok {
		t.Fatal("epoch-2 item missing from projection")
	}
	if itemE.Title != placeholderText {
		t.Fatalf("epoch-2 card should be placeholder for an epoch-1-only member, got title=%q", itemE.Title)
	}
}
