// Owner-board discovery (ready-636, `rd follow`).
//
// `rd follow <owner>` joins ALL of an owner's boards in one act — no 64-hex
// coordinate to copy. The enumeration source is the owner's own signed kind-30301
// board events on the relays: every board an owner published is one 30301 event
// whose author is that owner and whose "d" tag is the board identifier. Given a
// relay snapshot, DiscoverOwnerBoards is the PURE function that turns "the owner's
// pubkey(s)" into "every board coordinate they own", with no I/O — the command
// layer fetches the snapshot and drives binding/backfill around it.
package sync

import (
	"sort"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// DiscoverOwnerBoards returns the sorted, de-duplicated board coordinates
// (30301:<author>:<d>) for every kind-30301 board event in events authored by a
// pubkey in ownerPubkeys. When boardD is non-empty only the board with that "d" is
// returned (single-board `rd follow --board`).
//
// Each candidate 30301 event is schnorr-VERIFIED before it mints a coordinate — a
// relay is untrusted, so a forged/tampered board event served by a hostile relay
// must not turn into a bound board. A board event with no "d" tag is skipped (it
// names no addressable board). ownerPubkeys is the set of keys the caller already
// resolved the follow target to (one npub, one token owner, or every key of an
// email's party), so a single call enumerates a multi-key owner's boards too.
func DiscoverOwnerBoards(events []*nostr.Event, ownerPubkeys []string, boardD string) []string {
	owners := make(map[string]bool, len(ownerPubkeys))
	for _, pk := range ownerPubkeys {
		if pk != "" {
			owners[pk] = true
		}
	}
	if len(owners) == 0 {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	for _, e := range events {
		if e == nil || e.Kind != KindBoard {
			continue
		}
		if !owners[e.PubKey] {
			continue
		}
		if e.Verify() != nil {
			continue
		}
		d := tagValue(e, "d")
		if d == "" {
			continue
		}
		if boardD != "" && d != boardD {
			continue
		}
		coord := BoardCoord(e.PubKey, d)
		if seen[coord] {
			continue
		}
		seen[coord] = true
		out = append(out, coord)
	}
	sort.Strings(out)
	return out
}
