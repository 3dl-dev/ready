package main

import (
	"testing"

	"github.com/campfire-net/ready/pkg/state"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

func itemMap(ids ...string) map[string]*state.Item {
	m := make(map[string]*state.Item, len(ids))
	for _, id := range ids {
		m[id] = &state.Item{ID: id, Title: id, Status: state.StatusActive, Priority: "p1", Type: "task"}
	}
	return m
}

// TestParityUndercount_FailsWithoutSample proves the ready-187 undercount fix: when
// the nostr projection holds FEWER items than the campfire source, parity WITHOUT
// --sample must compare the FULL source (so the missing items are reported LOST and
// AllMatch is false). Only --sample narrows to the projected subset.
func TestParityUndercount_FailsWithoutSample(t *testing.T) {
	src := itemMap("ready-a", "ready-b", "ready-c")
	projected := itemMap("ready-a", "ready-b") // ready-c genuinely lost

	// Default (sample=false): compare the WHOLE source — undercount must fail.
	full := parityCompareSource(src, projected, false)
	if len(full) != len(src) {
		t.Fatalf("without --sample the comparison must cover the FULL source; got %d want %d", len(full), len(src))
	}
	rep := rdSync.CompareItemSets(full, projected)
	if rep.AllMatch() {
		t.Fatalf("undercount was silently masked: parity reported all-match while ready-c is missing")
	}
	lostFlagged := false
	for _, ip := range rep.Items {
		if ip.ItemID == "ready-c" && !ip.Match() {
			lostFlagged = true
		}
	}
	if !lostFlagged {
		t.Fatalf("lost item ready-c not flagged; diffs=%+v", rep.Items)
	}

	// With --sample the operator asserts an intentional subset: narrow to projected.
	narrowed := parityCompareSource(src, projected, true)
	if len(narrowed) != len(projected) {
		t.Fatalf("--sample must narrow to the projected ids; got %d want %d", len(narrowed), len(projected))
	}
	if !rdSync.CompareItemSets(narrowed, projected).AllMatch() {
		t.Fatalf("--sample sample run should match on the migrated subset")
	}
}

// TestParityCompareSource_NoNarrowWhenComplete proves --sample is a no-op when the
// projection is complete (never drops a genuinely-present item).
func TestParityCompareSource_NoNarrowWhenComplete(t *testing.T) {
	src := itemMap("ready-a", "ready-b")
	projected := itemMap("ready-a", "ready-b")
	if got := parityCompareSource(src, projected, true); len(got) != 2 {
		t.Fatalf("complete projection must compare all items even with --sample; got %d", len(got))
	}
}
