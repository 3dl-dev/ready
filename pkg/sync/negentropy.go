// Negentropy-backed machine<->relay sync (ready-797).
//
// This is the two-machine convergence path. Each machine keeps its own local
// authoritative signed-event log (pkg/sync NostrLog). To converge with another
// machine it reconciles its log against a shared strfry relay using NIP-77
// negentropy (pkg/nostr): the protocol computes exactly which event ids the relay
// has that the machine lacks (download) and which the machine has that the relay
// lacks (upload), transferring only that DIFFERENCE — never the whole set.
//
// Convergence flow for one relay:
//  1. Read the local log; select events matching the sync filter.
//  2. Negentropy-reconcile that id-set against the relay -> {need, have}.
//  3. Download `need` (REQ by ids), Verify, AppendUnique into the local log.
//  4. Upload `have` (EVENT) to the relay (idempotent — relay dedupes by id).
//
// After both machines run this against the same relay, both logs contain the
// union of events and replay (ProjectItems) to identical item state. The relay is
// a replaceable cache: the durability guarantee is the local log, and the sync
// cost is bounded by the diff (measured in SyncResult), which is what removes
// campfire's fs-sync re-sync pathology.
package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// SyncResult reports the outcome + measured cost of a negentropy sync against one
// relay.
type SyncResult struct {
	Relay string
	// LocalBefore is how many filter-matching events the local log held pre-sync.
	LocalBefore int
	// Need / Have are the diff sizes negentropy computed.
	Need int
	Have int
	// Downloaded is how many NEW events were merged into the local log (<= Need;
	// duplicates already present are not recounted).
	Downloaded int
	// Uploaded is how many local events the relay accepted (<= Have).
	Uploaded int
	// BytesSent / BytesReceived / RoundTrips measure the negentropy wire cost
	// (the reconciliation messages only — the small diff, not the whole set).
	BytesSent     int
	BytesReceived int
	RoundTrips    int
	// EventBytesDownloaded / EventBytesUploaded measure the actual event transfer.
	EventBytesDownloaded int
	EventBytesUploaded   int
}

// NegentropySync reconciles the local log against one relay via NIP-77 and
// performs the resulting download+upload so the two converge. filter is a NIP-01
// filter object identifying the event set to sync (e.g. a board's kinds+authors,
// or a single item's #d). The same filter is applied to the local log so the diff
// is computed over the same universe on both sides. A relay being unreachable is
// returned as an error; callers may ignore it and rely on the local log (and the
// git-JSONL degrade floor).
//
// TRUST GATE (ready-b57): the relay is an UNTRUSTED cache. A downloaded event is
// admitted into the local AUTHORITATIVE log ONLY when it (a) passes schnorr Verify
// AND (b) is authored by a pubkey in the `trusted` web-of-trust allowlist — the
// SAME two-step gate reconcile() applies (ready-d53). Verify proves the event is
// internally consistent, NOT that its author may write; the negentropy authors
// filter is advisory only (a permissive/hostile relay can ignore it and serve
// events from any key), so the author check MUST run client-side here too, or a
// validly-signed foreign event would enter the log via this path and bypass the
// 'single admission choke point' invariant. A nil `trusted` set disables the gate
// (tests / legacy paths); production callers pass at least the self pubkey
// (rdconfig.Config.TrustSet). Uploads are unaffected — publishing local events is
// always permitted; the gate only guards INBOUND admission.
func NegentropySync(ctx context.Context, relayURL string, log *NostrLog, filter map[string]any, trusted map[string]bool, timeout time.Duration) (SyncResult, error) {
	res := SyncResult{Relay: relayURL}
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}

	events, err := log.ReadAll()
	if err != nil {
		return res, err
	}
	byID := make(map[string]*nostr.Event, len(events))
	var localItems []nostr.NegItem
	for _, e := range events {
		if e == nil || !matchesFilter(e, filter) {
			continue
		}
		if _, seen := byID[e.ID]; seen {
			continue
		}
		byID[e.ID] = e
		it, err := nostr.NegItemFromEvent(e)
		if err != nil {
			return res, err
		}
		localItems = append(localItems, it)
	}
	res.LocalBefore = len(localItems)

	// 2. Negentropy reconcile against the relay.
	rctx, cancel := context.WithTimeout(ctx, timeout)
	neg, err := nostr.NegentropyReconcile(rctx, relayURL, filter, localItems)
	cancel()
	if err != nil {
		return res, fmt.Errorf("sync: negentropy reconcile %s: %w", relayURL, err)
	}
	res.Need = len(neg.Need)
	res.Have = len(neg.Have)
	res.BytesSent = neg.BytesSent
	res.BytesReceived = neg.BytesReceived
	res.RoundTrips = neg.RoundTrips

	// 3. Download the ids the relay has and we lack; Verify; merge into the log.
	if len(neg.Need) > 0 {
		fctx, fcancel := context.WithTimeout(ctx, timeout)
		// Chunk the id set: a single REQ for the whole `need` (which on a fresh
		// machine is the relay's entire board — ~9k ids) overflows strfry's per-REQ
		// filter limit and returns NO frames at all, so the read blocks until the
		// deadline and fails as a bare "i/o timeout" (ready-8de). FetchByIDs pages the
		// download into MaxREQIDs-sized REQs sharing this one deadline.
		fetched, err := nostr.FetchByIDs(fctx, relayURL, neg.Need)
		fcancel()
		if err != nil {
			return res, fmt.Errorf("sync: download need from %s: %w", relayURL, err)
		}
		merge, wireBytes := admitDownloaded(fetched, trusted)
		res.EventBytesDownloaded += wireBytes
		added, err := log.AppendUnique(merge)
		if err != nil {
			return res, fmt.Errorf("sync: merge downloaded events: %w", err)
		}
		res.Downloaded = added
	}

	// 4. Upload the ids we have and the relay lacks (idempotent — relay dedupes).
	for _, id := range neg.Have {
		e := byID[id]
		if e == nil {
			continue
		}
		pctx, pcancel := context.WithTimeout(ctx, timeout)
		accepted, _, perr := nostr.Publish(pctx, relayURL, e)
		pcancel()
		if perr != nil {
			continue
		}
		if accepted {
			res.Uploaded++
			res.EventBytesUploaded += eventWireSize(e)
		}
	}

	return res, nil
}

// NegentropySyncMany runs NegentropySync against each relay in turn, accumulating
// per-relay results. Syncing against multiple relays makes any single relay a
// replaceable cache: an event downloaded from relay-a is uploaded to relay-b on
// the next pass, so the machine converges even if it can only reach one relay at
// a time. Unreachable relays are recorded as errors and skipped, never fatal.
func NegentropySyncMany(ctx context.Context, relays []string, log *NostrLog, filter map[string]any, trusted map[string]bool, timeout time.Duration) ([]SyncResult, []string) {
	var results []SyncResult
	var errs []string
	for _, relay := range relays {
		r, err := NegentropySync(ctx, relay, log, filter, trusted, timeout)
		if err != nil {
			errs = append(errs, fmt.Sprintf("%s: %v", relay, err))
			continue
		}
		results = append(results, r)
	}
	return results, errs
}

// BoardSyncFilter builds the negentropy filter that selects an entire board's rd
// events (card + status kinds) authored by the given pubkeys. This is the natural
// unit of machine<->machine sync: everything on one project board.
func BoardSyncFilter(boardCoord string, authors []string) map[string]any {
	f := map[string]any{
		// KindRoleGrant (39301) is included so owner-signed role-grants PROPAGATE across
		// machines (GAP-1, ready-7c1): a second machine cannot derive read-trust for a
		// granted contributor unless the grant reaches its local log. Grants carry the
		// board "a" coordinate, so the #a board scope below matches them; in the
		// author-scoped (unpinned) path the owner's grants match the authors filter.
		"kinds": []int{KindBoard, KindCard, KindStatusOpen, KindStatusResolved, KindStatusClosed, KindStatusDraft, KindRoleGrant},
	}
	if boardCoord != "" {
		f["#a"] = []string{boardCoord}
	}
	if len(authors) > 0 {
		f["authors"] = authors
	}
	return f
}

// admitDownloaded is the download-side admission gate (ready-b57): the exact
// two-step check reconcile() applies, factored out so it is unit-testable without a
// live relay. Given the events a relay served for a negentropy `need` set, it returns
// the subset admissible into the local AUTHORITATIVE log plus their summed wire size.
//
// An event is admitted ONLY when it (a) passes schnorr Verify AND (b) is authored by
// a pubkey in the `trusted` allowlist. Verify proves internal consistency, NOT
// authorization — the negentropy authors filter is advisory (a hostile relay can
// serve any key), so the author check MUST run client-side or a validly-signed
// foreign event would enter the log via the download and bypass the single admission
// choke point. A nil `trusted` set disables the gate (tests / legacy paths).
func admitDownloaded(fetched []*nostr.Event, trusted map[string]bool) ([]*nostr.Event, int) {
	var merge []*nostr.Event
	wireBytes := 0
	for _, e := range fetched {
		if e == nil {
			continue
		}
		if err := e.Verify(); err != nil {
			continue // never merge an event that does not verify
		}
		if trusted != nil && !trusted[e.PubKey] {
			continue // untrusted author — drop before AppendUnique
		}
		wireBytes += eventWireSize(e)
		merge = append(merge, e)
	}
	return merge, wireBytes
}

// eventWireSize approximates the on-wire byte size of an event (its JSON form).
func eventWireSize(e *nostr.Event) int {
	// id(64) + pubkey(64) + sig(128) hex + content + tags, plus JSON overhead.
	size := 64 + 64 + 128 + len(e.Content) + 80
	for _, tag := range e.Tags {
		for _, v := range tag {
			size += len(v) + 4
		}
	}
	return size
}
