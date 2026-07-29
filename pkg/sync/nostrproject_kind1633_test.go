// Regression proof for ready-816: isStatusKind (pkg/sync/nostrwire.go) was a RANGE
// test (1630 <= kind <= 1633), not an allowlist. rd itself only ever WRITES 1630
// (open/active/etc.), 1631 (resolved), 1632 (closed) — KindStatusDraft (1633) is
// reserved and no rd writer emits it (nostrwire.go:53-54). Because the range test
// admitted it anyway, a kind-1633 event signed by an ALREADY-TRUSTED key (e.g. a
// different tool that operator also runs, or a future NIP claiming 1633) folded as
// an ordinary authoritative status transition and silently mutated rd item state —
// a foreign kind mutating state that no rd writer produces. This is a trust-boundary
// bug: the read-side gate checks WHO signed, never WHICH KIND, so kind is a second,
// independent gate that must also be narrow.
//
// This test asserts the SECURE behavior (kind 1633 is inert) and must advance past
// the write to a LATER fold: it does not merely check `ProjectItems` accepts/drops
// the event, it re-projects the FULL event set (card + foreign draft) and asserts
// the resulting item's status/history are as if the draft had never been published,
// exactly like a second machine replaying the same log would see.
package sync

import (
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
)

func TestKind1633DraftDoesNotMutateStatus(t *testing.T) {
	owner := testKey(t)
	const itemID = "ready-816a"

	card := mustCard(t, owner, itemID, state.StatusInbox, 1000)

	// A raw kind-1633 (KindStatusDraft) event, signed by the item's OWNER — an
	// already-trusted key — carrying a "status" tag claiming a transition to
	// "active". rd never builds this kind (there is no BuildStatusEvent path that
	// emits KindStatusDraft); this simulates a FOREIGN client (or a future NIP)
	// publishing one under the same trusted signer.
	draft := &nostr.Event{
		Kind:      KindStatusDraft,
		CreatedAt: 2000,
		Tags: [][]string{
			{"a", CardCoord(owner.PubKeyHex(), itemID)},
			{"d", itemID},
			{"status", state.StatusActive},
		},
		Content: "draft transition from a foreign client",
	}
	if err := draft.Sign(owner); err != nil {
		t.Fatalf("sign draft: %v", err)
	}

	trust := map[string]bool{owner.PubKeyHex(): true}
	events := []*nostr.Event{card, draft}

	items := ProjectItems(events, ProjectOptions{Trusted: trust})
	it := items[itemID]
	if it == nil {
		t.Fatal("item missing from projection")
	}
	if it.Status != state.StatusInbox {
		t.Fatalf("kind-1633 draft mutated status: got %q, want %q (unchanged from card)", it.Status, state.StatusInbox)
	}
	if len(it.History) != 0 {
		t.Fatalf("kind-1633 draft contributed a history entry: %+v", it.History)
	}

	// Advance past the write to a LATER fold, per ready-816's regression
	// requirement: re-project the SAME event set a second time (simulating a
	// second machine, or a relay-reconcile replay) and confirm convergence — the
	// draft must stay inert on every re-fold, not just the first.
	itemsAgain := ProjectItems(events, ProjectOptions{Trusted: trust})
	itAgain := itemsAgain[itemID]
	if itAgain == nil {
		t.Fatal("item missing from second projection")
	}
	if itAgain.Status != state.StatusInbox {
		t.Fatalf("kind-1633 draft mutated status on re-fold: got %q, want %q", itAgain.Status, state.StatusInbox)
	}
}
