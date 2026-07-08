package sync

import (
	"path/filepath"
	"testing"

	"github.com/campfire-net/ready/pkg/nostr"
	"github.com/campfire-net/ready/pkg/state"
)

func mustKey(t *testing.T) *nostr.Key {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("gen key: %v", err)
	}
	return k
}

func mustCard(t *testing.T, k *nostr.Key, id, status string, ts int64) *nostr.Event {
	t.Helper()
	e, err := BuildCardEvent(k, CardSpec{ItemID: id, Title: id, Status: status, Type: "task", BoardD: "ready"}, ts)
	if err != nil {
		t.Fatalf("build card: %v", err)
	}
	return e
}

func TestMatchesFilter(t *testing.T) {
	k := mustKey(t)
	card := mustCard(t, k, "ready-x1", state.StatusActive, 1000)

	// kinds + authors match.
	if !matchesFilter(card, map[string]any{"kinds": []int{KindCard}, "authors": []string{k.PubKeyHex()}}) {
		t.Error("expected card to match kinds+authors filter")
	}
	// wrong kind.
	if matchesFilter(card, map[string]any{"kinds": []int{KindBoard}}) {
		t.Error("card should not match board-kind filter")
	}
	// wrong author.
	if matchesFilter(card, map[string]any{"authors": []string{"deadbeef"}}) {
		t.Error("card should not match wrong-author filter")
	}
	// #d tag match / mismatch.
	if !matchesFilter(card, map[string]any{"#d": []string{"ready-x1"}}) {
		t.Error("card should match #d=ready-x1")
	}
	if matchesFilter(card, map[string]any{"#d": []string{"ready-x2"}}) {
		t.Error("card should not match #d=ready-x2")
	}
	// ids match via []any (JSON-decoded shape).
	if !matchesFilter(card, map[string]any{"ids": []any{card.ID}}) {
		t.Error("card should match its own id via []any")
	}
	// empty filter matches everything.
	if !matchesFilter(card, map[string]any{}) {
		t.Error("empty filter should match")
	}
}

func TestBoardSyncFilterOmitsCoordWhenEmpty(t *testing.T) {
	f := BoardSyncFilter("", []string{"aa"})
	if _, ok := f["#a"]; ok {
		t.Error("empty boardCoord must not add #a (would exclude status events)")
	}
	kinds, _ := f["kinds"].([]int)
	if len(kinds) == 0 {
		t.Fatal("expected kinds in filter")
	}
	// Must include both card and status kinds.
	var hasCard, hasStatus bool
	for _, k := range kinds {
		if k == KindCard {
			hasCard = true
		}
		if k == KindStatusResolved {
			hasStatus = true
		}
	}
	if !hasCard || !hasStatus {
		t.Errorf("filter must include card+status kinds, got %v", kinds)
	}
}

// TestMergeFromDegradeFloor proves the relay-free degrade floor: machine B merges
// machine A's committed JSONL log, gaining A's events, idempotently and with a
// verify gate that rejects forged lines.
func TestMergeFromDegradeFloor(t *testing.T) {
	kA := mustKey(t)
	kB := mustKey(t)
	dir := t.TempDir()

	logA := NewNostrLog(filepath.Join(dir, "a", "nostr-log.jsonl"))
	logB := NewNostrLog(filepath.Join(dir, "b", "nostr-log.jsonl"))

	// A authors two cards; B authors one.
	for _, e := range []*nostr.Event{
		mustCard(t, kA, "ready-a1", state.StatusActive, 1000),
		mustCard(t, kA, "ready-a2", state.StatusActive, 1001),
	} {
		if err := logA.Append(e); err != nil {
			t.Fatal(err)
		}
	}
	if err := logB.Append(mustCard(t, kB, "ready-b1", state.StatusActive, 2000)); err != nil {
		t.Fatal(err)
	}

	// B merges A's committed log (the git-JSONL fallback).
	added, err := logB.MergeFrom(logA.Path())
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	if added != 2 {
		t.Fatalf("expected 2 events merged, got %d", added)
	}
	// Idempotent: a second merge adds nothing.
	added2, err := logB.MergeFrom(logA.Path())
	if err != nil {
		t.Fatal(err)
	}
	if added2 != 0 {
		t.Fatalf("second merge should add 0, got %d", added2)
	}
	// B now replays to all three items with zero relay involvement.
	evs, _ := logB.ReadAll()
	items := ProjectItems(evs, ProjectOptions{})
	for _, id := range []string{"ready-a1", "ready-a2", "ready-b1"} {
		if items[id] == nil {
			t.Errorf("degrade-floor merge missing item %s", id)
		}
	}

	// Verify gate: a tampered event in the source log is NOT merged.
	tampered := mustCard(t, kA, "ready-a3", state.StatusActive, 1002)
	tampered.Content = "forged after signing"
	forgedLog := NewNostrLog(filepath.Join(dir, "forged", "nostr-log.jsonl"))
	if err := forgedLog.Append(tampered); err != nil {
		t.Fatal(err)
	}
	addedF, err := logB.MergeFrom(forgedLog.Path())
	if err != nil {
		t.Fatal(err)
	}
	if addedF != 0 {
		t.Errorf("tampered event must be rejected by the verify gate, merged %d", addedF)
	}
}

// TestPendingBufferMechanics checks the offline buffer read/rewrite: rewriting
// with a subset keeps only those events; rewriting empty removes the file.
func TestPendingBufferMechanics(t *testing.T) {
	k := mustKey(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "nostr-pending.jsonl")

	e1 := mustCard(t, k, "ready-p1", state.StatusInbox, 1)
	e2 := mustCard(t, k, "ready-p2", state.StatusInbox, 2)
	e3 := mustCard(t, k, "ready-p3", state.StatusInbox, 3)
	if err := appendPendingEvent(path, e1); err != nil {
		t.Fatal(err)
	}
	if err := appendPendingEvent(path, e2); err != nil {
		t.Fatal(err)
	}
	if err := appendPendingEvent(path, e3); err != nil {
		t.Fatal(err)
	}

	got, err := readPendingEvents(path)
	if err != nil || len(got) != 3 {
		t.Fatalf("read pending: n=%d err=%v", len(got), err)
	}

	// Keep only e2 (simulate e1,e3 flushed).
	if err := rewritePendingEvents(path, []*nostr.Event{e2}); err != nil {
		t.Fatal(err)
	}
	got, _ = readPendingEvents(path)
	if len(got) != 1 || got[0].ID != e2.ID {
		t.Fatalf("expected only e2 to remain, got %d", len(got))
	}

	// Empty rewrite removes the file.
	if err := rewritePendingEvents(path, nil); err != nil {
		t.Fatal(err)
	}
	got, err = readPendingEvents(path)
	if err != nil || len(got) != 0 {
		t.Fatalf("expected empty buffer, n=%d err=%v", len(got), err)
	}
}
