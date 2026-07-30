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
//     A relay reconciles at most a CAPPED number of records per query, so this
//     step repeats over `until`-scoped windows walking backwards in time until a
//     window comes back under the cap (ready-bec). A single unpaged exchange
//     silently truncated every board bigger than that cap, and never converged.
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
	"math"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// SyncResult reports the outcome + measured cost of a negentropy sync against one
// relay.
type SyncResult struct {
	Relay string
	// LocalBefore is how many filter-matching events the local log held pre-sync.
	LocalBefore int
	// Need / Have are the diff sizes negentropy computed, summed over the walk's
	// windows and de-duplicated (ready-bec). Need is every distinct id the relay
	// held that the local log lacked. Have is every distinct local id a window
	// could establish the relay lacks — an id below a CAPPED window's floor is
	// simply out of that window's view, not missing, so it is left to the window
	// that can actually see it rather than counted (and re-uploaded) here.
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
	// Pages is how many `until`-scoped windows the walk needed (ready-bec). It is
	// always >= 1. A value > 1 means the relay's per-query record CAP was reached
	// and the sync paged past it — the observable that distinguishes "the board is
	// this small" from "the relay stopped talking at its cap".
	Pages int
}

// SyncPageLimit is the number of records rd asks a relay to reconcile per
// negentropy window (ready-bec).
//
// A relay caps how many records it will serve for ONE query, and NIP-01/NIP-77
// give the client no way to read that cap back. Measured against
// wss://relay.3dl.network on 2026-07-30, with rd's board filter and an EMPTY
// local log:
//
//	NEG-OPEN {kinds,#a}              -> need=500   (the relay's cap, silently)
//	NEG-OPEN {kinds,#a,limit:500}    -> need=500
//	NEG-OPEN {kinds,#a,limit:5000}   -> no reply at all; the read deadline fires
//	the same filter walked with `until` -> 1802 events over 4 windows
//
// So an unpaged sync downloaded 500 of 1802 events, reported success, and NEVER
// converged: every later sync saw need=0 because the relay's capped window was
// already fully local. A fresh clone of the board projected 207 items instead of
// its real ~540 and was told nothing. This is the Go-side twin of the browser
// defect fixed in web/board/src/lib/relay.ts (ready-5c5).
//
// THE CAP IS PER-RELAY CONFIGURATION AND CANNOT BE READ BACK. Re-measured
// 2026-07-30 against the LAN strfry ws://192.168.2.40:7777 over a purpose-built
// 560-event board (TestLiveRelay_OneQueryIsBoundedAndTheWalkPagesPastIt):
//
//	NEG-OPEN {kinds,#a}              -> need=560   (no default cap at this size)
//	NEG-OPEN {kinds,#a,limit:100}    -> need=100   (clamps to the asked limit)
//	NEG-OPEN {kinds,#a,limit:500}    -> need=500   (the newest 500)
//	NEG-OPEN {kinds,#a,limit:5000}   -> need=560   (answered, NOT refused)
//
// Two relays, two different answers to the same query: 3dl volunteers 500 and
// stops, this one volunteers everything. Nothing in NIP-01 or NIP-77 tells a
// client which it is talking to, so the walk must not depend on the relay's own
// bound — it names its own.
//
// Naming the limit EXPLICITLY does two things. It makes the window size OURS, so
// the walk's termination test ("did this window come back under the limit?") is
// asked against a number we chose rather than one we cannot see. And where a
// relay's cap is BELOW what we ask for, the query is refused rather than answered
// short — pinned by TestNegentropySync_RelayCapBelowOurLimitFailsLoudly, which
// checks rd propagates that refusal instead of merging a partial board. (Not
// every relay refuses: the strfry above answers limit=5000 with what it has. That
// is why the walk's correctness rests on the `until` cursor, and the refusal is
// the safety net, not the mechanism.) 500 is strfry's default maxFilterLimit and
// is what MaxREQIDs already uses for the download side.
const SyncPageLimit = 500

// MaxSyncPages hard-stops the `until` walk. It is a backstop against a relay that
// answers every window identically, NOT a tuning knob: the walk normally ends on
// its own the first time a window comes back under SyncPageLimit. At 500 records
// per window this admits a 100k-event board.
const MaxSyncPages = 200

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
// (rdconfig.Config.TrustSet). Uploads are unaffected BY THE TRUST GATE —
// publishing local events (already admitted into the log by some other,
// already-gated path) is always permitted here on THAT axis; the trust gate
// only guards INBOUND admission.
//
// production is a SEPARATE axis, threaded to GuardedPublish for the upload step
// (ready-6d0 finding (3)): an event addressing the reserved production board
// coordinate is refused before any relay dial unless production is true. Pass
// true only from the sanctioned CLI's own sync command (cmd/rd/nostr.go); every
// other caller (tests) passes false explicitly.
//
// PAGING (ready-bec). One negentropy exchange does NOT reconcile "every matching
// event": it reconciles at most as many records as the relay's per-query CAP
// allows, and nothing in the protocol says which of those two happened. Measured
// on the live relay, a single unpaged exchange reconciled 500 of 1802 events,
// merged them, reported success — and every subsequent sync then computed need=0,
// because the capped window it could see was by then fully local. The board never
// converged and nothing said so. See SyncPageLimit for the measurements.
//
// So the exchange is repeated over `until`-scoped WINDOWS walking backwards in
// time, each asking for at most SyncPageLimit records, until a window comes back
// holding FEWER records than that — the only available evidence that the relay
// answered with everything it had rather than with a capped prefix. Both sides are
// scoped to the same window (matchesFilter honours `until`), so each exchange
// still costs only its window's diff and a converged re-sync still moves zero
// event bytes.
func NegentropySync(ctx context.Context, relayURL string, log *NostrLog, filter map[string]any, trusted map[string]bool, timeout time.Duration, production bool) (SyncResult, error) {
	res := SyncResult{Relay: relayURL}
	if timeout <= 0 {
		timeout = nostr.DefaultTimeout
	}

	events, err := log.ReadAll()
	if err != nil {
		return res, err
	}
	byID := make(map[string]*nostr.Event, len(events))
	var local []*nostr.Event
	for _, e := range events {
		if e == nil || !matchesFilter(e, filter) {
			continue
		}
		if _, seen := byID[e.ID]; seen {
			continue
		}
		byID[e.ID] = e
		local = append(local, e)
	}
	res.LocalBefore = len(local)

	needSeen := map[string]bool{}
	uploadSeen := map[string]bool{}
	var uploadIDs []string

	// until is the walk's cursor: the INCLUSIVE upper bound on created_at for the
	// window being reconciled. The first window is unbounded (the relay answers
	// with its newest cap-many records); every later one is bounded by the oldest
	// record of the window before it.
	var until int64
	bounded := false

	for res.Pages = 1; ; res.Pages++ {
		if res.Pages > MaxSyncPages {
			return res, fmt.Errorf("sync: %s: gave up after %d windows still hitting the relay's %d-record cap — the board is either larger than %d events or the relay is re-serving the same window",
				relayURL, MaxSyncPages, SyncPageLimit, MaxSyncPages*SyncPageLimit)
		}
		windowFilter := syncWindowFilter(filter, until, bounded)

		// Reduce the LOCAL set to the same window, or the diff is skewed: a local
		// event outside the relay's window would be reported as one the relay lacks
		// and re-uploaded on every sync forever.
		var localItems []nostr.NegItem
		localWindow := 0
		for _, e := range local {
			if bounded && e.CreatedAt > until {
				continue
			}
			it, err := nostr.NegItemFromEvent(e)
			if err != nil {
				return res, err
			}
			localItems = append(localItems, it)
			localWindow++
		}

		rctx, cancel := context.WithTimeout(ctx, timeout)
		neg, err := nostr.NegentropyReconcile(rctx, relayURL, windowFilter, localItems)
		cancel()
		if err != nil {
			return res, fmt.Errorf("sync: negentropy reconcile %s: %w", relayURL, err)
		}
		res.BytesSent += neg.BytesSent
		res.BytesReceived += neg.BytesReceived
		res.RoundTrips += neg.RoundTrips
		for _, id := range neg.Need {
			needSeen[id] = true
		}
		res.Need = len(needSeen)

		// How many records the relay actually reconciled in this window. Negentropy
		// never states it, but it is exactly recoverable: the relay's window is the
		// local window, minus what only we hold, plus what only it holds.
		relayWindow := localWindow - len(neg.Have) + len(neg.Need)

		// Download the ids the relay has and we lack; Verify; merge into the log.
		// oldest tracks the lower edge of the relay's window, which is the next
		// cursor: the min created_at over the records the relay reconciled here,
		// i.e. what it served for `need` plus the local records it also held.
		oldest := int64(math.MaxInt64)
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
			// The cursor is a claim about the RELAY's set, so it is read off everything
			// the relay served — including events the trust gate is about to drop. Using
			// only admitted events would let one untrusted event at the window's edge
			// stall the walk. A relay lying about created_at can only shorten its own
			// walk, which it can already do by serving nothing; admission is unaffected.
			for _, e := range fetched {
				if e != nil && e.CreatedAt < oldest {
					oldest = e.CreatedAt
				}
			}
			merge, wireBytes := admitDownloaded(fetched, trusted)
			res.EventBytesDownloaded += wireBytes
			added, err := log.AppendUnique(merge)
			if err != nil {
				return res, fmt.Errorf("sync: merge downloaded events: %w", err)
			}
			res.Downloaded += added
			// Newly-merged events are local from here on, so the next window does not
			// re-request them.
			for _, e := range merge {
				if _, seen := byID[e.ID]; seen {
					continue
				}
				byID[e.ID] = e
				local = append(local, e)
			}
		}

		// The rest of the relay's window is the local records it did NOT report as
		// missing. Those bound the cursor too, and they decide which local events
		// this window can speak for at all.
		relayHas := make(map[string]bool, len(neg.Have))
		for _, id := range neg.Have {
			relayHas[id] = true
		}
		for _, e := range local {
			if bounded && e.CreatedAt > until {
				continue
			}
			if relayHas[e.ID] {
				continue
			}
			if e.CreatedAt < oldest {
				oldest = e.CreatedAt
			}
		}

		capped := relayWindow >= SyncPageLimit

		// Queue the uploads this window can honestly speak for. In a CAPPED window
		// the relay only reconciled [oldest, until], so a local event older than
		// that is not "missing from the relay" — it is merely below the cap, and a
		// later window decides. Uploading it here is how a converged machine would
		// end up re-publishing its whole backlog on every sync.
		for _, id := range neg.Have {
			e := byID[id]
			if e == nil {
				continue
			}
			if capped && e.CreatedAt < oldest {
				continue
			}
			if uploadSeen[id] {
				continue
			}
			uploadSeen[id] = true
			uploadIDs = append(uploadIDs, id)
		}

		if !capped {
			break
		}
		if oldest == math.MaxInt64 {
			// A full window whose every record is unaccounted for — nothing to move
			// the cursor to. Stop rather than re-ask the same window forever.
			break
		}
		if bounded && oldest >= until {
			// More than a cap's worth of events share a single created_at second, so
			// `until` cannot move without skipping some of them. Say so: this is the
			// silent-truncation case, and staying quiet about it is the defect.
			return res, fmt.Errorf("sync: %s: the relay's %d-record window at until=%d is full and every record shares that timestamp — cannot page further without dropping events",
				relayURL, SyncPageLimit, until)
		}
		until = oldest
		bounded = true
	}

	res.Have = len(uploadIDs)

	// Upload the ids we have and the relay lacks (idempotent — relay dedupes).
	for _, id := range uploadIDs {
		e := byID[id]
		if e == nil {
			continue
		}
		pctx, pcancel := context.WithTimeout(ctx, timeout)
		accepted, _, perr := GuardedPublish(pctx, relayURL, e, production)
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

// syncWindowFilter copies filter and stamps it with this window's cursor. `limit`
// is always set: see SyncPageLimit for why asking explicitly is what turns a
// relay cap below it into a loud refusal instead of a short answer. Any `until` /
// `limit` already on the caller's filter is REPLACED — they are the walk's own
// controls, and no rd caller sets them.
func syncWindowFilter(filter map[string]any, until int64, bounded bool) map[string]any {
	out := make(map[string]any, len(filter)+2)
	for k, v := range filter {
		out[k] = v
	}
	out["limit"] = SyncPageLimit
	if bounded {
		out["until"] = until
	} else {
		delete(out, "until")
	}
	return out
}

// NegentropySyncMany runs NegentropySync against each relay in turn, accumulating
// per-relay results. Syncing against multiple relays makes any single relay a
// replaceable cache: an event downloaded from relay-a is uploaded to relay-b on
// the next pass, so the machine converges even if it can only reach one relay at
// a time. Unreachable relays are recorded as errors and skipped, never fatal.
// production is threaded straight through to NegentropySync/GuardedPublish per
// relay (ready-6d0 finding (3)); see NegentropySync's doc for the contract.
func NegentropySyncMany(ctx context.Context, relays []string, log *NostrLog, filter map[string]any, trusted map[string]bool, timeout time.Duration, production bool) ([]SyncResult, []string) {
	var results []SyncResult
	var errs []string
	for _, relay := range relays {
		r, err := NegentropySync(ctx, relay, log, filter, trusted, timeout, production)
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
