// Nostr inbound reconcile (ready-a13).
//
// This is the nostr replacement for the campfire pull path. It queries read
// relays for an item's card + status events and CACHE-FILLS them into the local
// authoritative log (dedup by event id). The local log is the authority; the
// relay is a replaceable cache. rd read-back always prefers the local log —
// reconcile only fills gaps (e.g. after a wiped cache, or events authored on
// another machine).
package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/campfire-net/ready/pkg/nostr"
)

// ReconcileResult summarises a reconcile pass.
type ReconcileResult struct {
	// Fetched is the total events served by relays (pre-dedup).
	Fetched int
	// Added is the number of new events merged into the local log.
	Added int
	// RelayErrors holds per-relay errors (a relay being offline is non-fatal:
	// the local log remains authoritative).
	RelayErrors []string
}

// ReconcileItem queries the read relays for the item authored by authorPubkey
// (its 30302 card + NIP-34 status events) and merges any new signed events into
// the local log. Every relay may be offline — that is fine, reconcile just
// returns what it could (possibly nothing) and the caller reads from the local
// log regardless. Only events that pass Verify are merged.
func ReconcileItem(ctx context.Context, relays []string, log *NostrLog, itemID, authorPubkey string, timeout time.Duration) (ReconcileResult, error) {
	filter := map[string]any{
		"kinds": []int{KindCard, KindStatusOpen, KindStatusResolved, KindStatusClosed, KindStatusDraft},
		"#d":    []string{itemID},
	}
	if authorPubkey != "" {
		filter["authors"] = []string{authorPubkey}
	}
	return reconcile(ctx, relays, log, filter, itemID, timeout)
}

// ReconcileAll queries the read relays for EVERY card + status event authored
// by authorPubkey (no item-id filter) and merges any new signed events into the
// local log. This is the multi-item counterpart to ReconcileItem, needed by the
// attention engine (`rd ready`/`rd nostr ready`): readiness depends on the
// WHOLE dep/gate graph, not a single item. Same cache-fill semantics: the local
// log stays authoritative, relays are best-effort, every relay may be offline.
func ReconcileAll(ctx context.Context, relays []string, log *NostrLog, authorPubkey string, timeout time.Duration) (ReconcileResult, error) {
	filter := map[string]any{
		"kinds": []int{KindCard, KindStatusOpen, KindStatusResolved, KindStatusClosed, KindStatusDraft},
	}
	if authorPubkey != "" {
		filter["authors"] = []string{authorPubkey}
	}
	return reconcile(ctx, relays, log, filter, "", timeout)
}

// reconcile is the shared fetch+verify+merge core for ReconcileItem/ReconcileAll.
// wantItemID, when non-empty, is a defensive post-filter (relays may honor tag
// filters loosely) restricting merged events to a single item; empty means
// accept any rd item event.
func reconcile(ctx context.Context, relays []string, log *NostrLog, filter map[string]any, wantItemID string, timeout time.Duration) (ReconcileResult, error) {
	var res ReconcileResult
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}

	var fetched []*nostr.Event
	for _, relay := range relays {
		rctx, cancel := context.WithTimeout(ctx, timeout)
		evs, err := nostr.FetchMany(rctx, relay, filter)
		cancel()
		if err != nil {
			res.RelayErrors = append(res.RelayErrors, fmt.Sprintf("%s: %v", relay, err))
			continue
		}
		for _, e := range evs {
			if e == nil {
				continue
			}
			// Read-side trust gate: never merge an event that does not verify.
			if err := e.Verify(); err != nil {
				continue
			}
			id := itemIDForEvent(e)
			if id == "" {
				continue
			}
			if wantItemID != "" && id != wantItemID {
				continue
			}
			fetched = append(fetched, e)
			res.Fetched++
		}
	}

	added, err := log.AppendUnique(fetched)
	res.Added = added
	if err != nil {
		return res, fmt.Errorf("sync: reconcile merge: %w", err)
	}
	return res, nil
}
