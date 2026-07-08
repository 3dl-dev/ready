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

// ReconcileItem queries the read relays for the item's 30302 card + NIP-34 status
// events and merges any new TRUSTED-AUTHOR signed events into the local log. Every
// relay may be offline — that is fine, reconcile just returns what it could
// (possibly nothing) and the caller reads from the local log regardless.
//
// The relay is an UNTRUSTED cache, so authorization is done client-side, not by
// asking the relay to pre-filter by author (a permissive or hostile relay ignores
// such a filter and can serve events from any key). Only events that (a) pass
// schnorr Verify AND (b) are authored by a pubkey in the `trusted` allowlist are
// merged — an untrusted-author event is dropped and never reaches the local log
// (the log is the source of truth and must not be poisoned; ready-d53). A nil
// `trusted` set disables the gate (tests / legacy paths); production callers pass
// at least the self pubkey (rdconfig.Config.TrustSet).
func ReconcileItem(ctx context.Context, relays []string, log *NostrLog, itemID string, trusted map[string]bool, timeout time.Duration) (ReconcileResult, error) {
	filter := map[string]any{
		"kinds": []int{KindCard, KindStatusOpen, KindStatusResolved, KindStatusClosed, KindStatusDraft},
		"#d":    []string{itemID},
	}
	return reconcile(ctx, relays, log, filter, itemID, trusted, timeout)
}

// ReconcileAll queries the read relays for EVERY card + status event (no item-id
// filter) and merges any new TRUSTED-AUTHOR signed events into the local log. This
// is the multi-item counterpart to ReconcileItem, needed by the attention engine
// (`rd ready`/`rd nostr ready`): readiness depends on the WHOLE dep/gate graph,
// not a single item. Same cache-fill + trust semantics as ReconcileItem: the local
// log stays authoritative, relays are best-effort/untrusted, every relay may be
// offline, and only trusted-author events are merged (ready-d53).
func ReconcileAll(ctx context.Context, relays []string, log *NostrLog, trusted map[string]bool, timeout time.Duration) (ReconcileResult, error) {
	filter := map[string]any{
		"kinds": []int{KindCard, KindStatusOpen, KindStatusResolved, KindStatusClosed, KindStatusDraft},
	}
	return reconcile(ctx, relays, log, filter, "", trusted, timeout)
}

// reconcile is the shared fetch+verify+authorize+merge core for
// ReconcileItem/ReconcileAll. wantItemID, when non-empty, is a defensive
// post-filter (relays may honor tag filters loosely) restricting merged events to
// a single item; empty means accept any rd item event. trusted is the web-of-trust
// allowlist (ready-d53): a non-nil set drops events from unlisted authors before
// they can be merged; a nil set disables the gate.
func reconcile(ctx context.Context, relays []string, log *NostrLog, filter map[string]any, wantItemID string, trusted map[string]bool, timeout time.Duration) (ReconcileResult, error) {
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
			// Read-side trust gate, step 1: never merge an event that does not verify.
			if err := e.Verify(); err != nil {
				continue
			}
			// Step 2 — web-of-trust authorization (ready-d53): Verify proves the
			// event is internally consistent, NOT that its author may write. Drop
			// any event whose author is not in the trust allowlist so a permissive
			// relay cannot poison the local authoritative log with events signed by
			// a foreign key. A nil `trusted` set disables the gate.
			if trusted != nil && !trusted[e.PubKey] {
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
