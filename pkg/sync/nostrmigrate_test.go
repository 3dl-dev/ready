package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/state"
)

// TestNonDerivedStatus_NeverReturnsDerivedBlocked is ready-500's guard-logic
// test: NonDerivedStatus is called explicitly by every REPUBLISH hook
// (cmd/rd/nostr.go's publishItemStatusChangeNostr / publishItemCardEditNostr,
// and the manual `rd nostr publish` command) right before it builds the
// outbound CardSpec, so a caller that republishes an item without itself
// deciding a new status can never leak the dep pass's derived "blocked"
// overlay back onto the wire.
func TestNonDerivedStatus_NeverReturnsDerivedBlocked(t *testing.T) {
	cases := []struct {
		name   string
		item   *state.Item
		want   string
		reason string
	}{
		{
			name:   "non-blocked status passes through unchanged",
			item:   &state.Item{ID: "x", Status: state.StatusActive},
			want:   state.StatusActive,
			reason: "every explicit write (claim/close/gate/approve/reject/update --status) already decided a real target status before reaching NonDerivedStatus; only blocked is ever derived",
		},
		{
			name: "derived-blocked with a valid authoritative history recovers the pre-block status",
			item: &state.Item{ID: "x", Status: state.StatusBlocked, History: []state.HistoryEntry{
				{FromStatus: "", ToStatus: state.StatusInbox},
				{FromStatus: state.StatusInbox, ToStatus: state.StatusActive},
			}},
			want:   state.StatusActive,
			reason: "delegate on a blocked item that was actively worked before the block must republish 'active', not 'blocked'",
		},
		{
			name:   "derived-blocked with EMPTY history falls back to the explicit inbox default, never blocked",
			item:   &state.Item{ID: "x", Status: state.StatusBlocked, History: nil},
			want:   state.StatusInbox,
			reason: "empty History is not hypothetical: a card-only item (no authoritative status event at all — stripped by a non-maintainer republish on a multi-agent board, or delivered by a partial relay reconcile) has nothing to fall back to except a NAMED default, never the derived value itself",
		},
		{
			name: "history where EVERY entry is itself blocked (fully burned-in) falls back to inbox, not the burned-in value",
			item: &state.Item{ID: "x", Status: state.StatusBlocked, History: []state.HistoryEntry{
				{FromStatus: state.StatusActive, ToStatus: state.StatusBlocked},
			}},
			want:   state.StatusInbox,
			reason: "a chain that is entirely blocked entries has no real recorded status to recover — trusting the last entry here would republish blocked and perpetuate the burn-in the fix exists to heal",
		},
		{
			name: "pre-burned-in item: last history entry is blocked but an earlier entry is real — walk PAST the burn-in, do not stop at the last entry",
			item: &state.Item{ID: "x", Status: state.StatusBlocked, History: []state.HistoryEntry{
				{FromStatus: "", ToStatus: state.StatusInbox},
				{FromStatus: state.StatusInbox, ToStatus: state.StatusBlocked}, // burned in by the pre-fix bug
			}},
			want:   state.StatusInbox,
			reason: "a naive 'use item.History[len-1].ToStatus' fallback (the prior fix's own logic) reads 'blocked' here and republishes it, PERPETUATING the burn-in instead of healing it; the guard must walk back past any number of blocked entries",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := NonDerivedStatus(c.item)
			if got == state.StatusBlocked {
				t.Fatalf("%s: NonDerivedStatus returned blocked (the derived value) — %s", c.name, c.reason)
			}
			if got != c.want {
				t.Fatalf("%s: NonDerivedStatus = %q; want %q — %s", c.name, got, c.want, c.reason)
			}
		})
	}
}

// TestCardSpecFromItem_CreatePathCarriesStatusVerbatim proves CardSpecFromItem
// itself is deliberately NOT where the ready-500 guard lives: it is also the
// mapper publishItemFullCreateNostr uses for brand-new items, and a freshly
// constructed *state.Item legitimately has no history to derive anything from
// yet. A caller that explicitly wants a new item to start out blocked (a test
// fixture standing in for "this epic isn't actionable yet", or a future
// template feature) must still be able to say so — CardSpecFromItem must carry
// Status verbatim, unguarded; only a REPUBLISH hook calls NonDerivedStatus
// explicitly before building its CardSpec.
func TestCardSpecFromItem_CreatePathCarriesStatusVerbatim(t *testing.T) {
	item := &state.Item{ID: "x", Status: state.StatusBlocked} // fresh item, empty History
	card := CardSpecFromItem(item, "boardD")
	if card.Status != state.StatusBlocked {
		t.Fatalf("card.Status = %q; want blocked carried through verbatim for a freshly-constructed item with no history — CardSpecFromItem must not itself guard status (that would silently downgrade a deliberately-blocked NEW item, e.g. a fixture or template)", card.Status)
	}
}

// TestCardSpecFromItem_NonStatusFieldsCarryThrough is an unrelated-field sanity
// check: every other field CardSpecFromItem maps passes through item's value
// verbatim regardless of status.
func TestCardSpecFromItem_NonStatusFieldsCarryThrough(t *testing.T) {
	item := &state.Item{
		ID: "x", Status: state.StatusBlocked, Title: "T", Priority: "p1", By: "agent-1",
		Type: "task", Context: "ctx", BlockedBy: []string{"blocker-1"},
	}
	card := CardSpecFromItem(item, "boardD")
	if card.Title != "T" || card.Priority != "p1" || card.Assignee != "agent-1" || card.Type != "task" || card.Context != "ctx" {
		t.Fatalf("non-status fields were altered: %+v", card)
	}
	if len(card.Deps) != 1 || card.Deps[0] != "blocker-1" {
		t.Fatalf("Deps not carried through: %v", card.Deps)
	}
}
