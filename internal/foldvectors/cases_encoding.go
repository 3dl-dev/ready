package foldvectors

// cases_encoding.go — ready-414: the vector file's cross-implementation
// encoding of item timestamps.

import (
	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdsync "github.com/3dl-dev/ready/pkg/sync"
)

// bigWallClockSec is a large, deliberately non-tidy unix-second timestamp: not
// an offset from t0, not a round number, not near a power of two. It stands in
// for "a realistic wall-clock value," as opposed to the rest of this file's
// small t0-relative offsets, so this vector's created_at/updated_at look like
// production data rather than fixture arithmetic.
const bigWallClockSec = int64(1785193627) // 2026-07-26T09:07:07Z-ish, arbitrary

// vItemTimestampEncoding pins §4.8: expect.items[].created_at/updated_at are
// decimal STRINGS, not bare JSON numbers, and exercises that encoding at a
// large, non-tidy nanosecond magnitude.
//
// IMPORTANT — what this vector does NOT prove: this specific value is not a
// counterexample to the OLD bare-number encoding. Every value the live fold
// can produce for these two fields is `sec * int64(time.Second)` (§4.6), which
// is always an exact multiple of 2^9 and therefore always exactly
// representable as an IEEE-754 double, at any magnitude an int64 can hold — so
// this vector would ALSO round-trip correctly under the old encoding. Its job
// is narrower: prove the NEW encoding is correct for a realistic, non-trivial
// magnitude, in the one place (the vector file) that changed. The concrete
// counterexample — a genuinely non-round int64 nanosecond value the old
// encoding cannot survive, which the type permits even though this fold never
// emits one — is TestTimestampEncodingPreservesArbitraryNanoseconds in
// vectors_test.go, not a fold-checked vector (it cannot be one: every vector's
// expect.items is checked against the live fold, and the live fold's formula
// cannot produce that value — see ready-414's disclosure for the full
// argument).
func (b *builder) vItemTimestampEncoding() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v31", Title: "Large wall-clock timestamp", Status: state.StatusInbox,
		Priority: "p2", Type: "task", Assignee: b.ownerPub,
	}, bigWallClockSec)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v31", MsgID: c.ID, Title: "Large wall-clock timestamp",
		Type: "task", Priority: "p2", Status: state.StatusInbox, By: b.ownerPub,
		CreatedAt: nanos(bigWallClockSec), UpdatedAt: nanos(bigWallClockSec),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "item_timestamp_large_nontidy_value_encoded_as_string",
		SpecClauses: []string{"4.8", "4.6", "5.1"},
		Note: "created_at/updated_at at a large, non-tidy wall-clock second value (not a small " +
			"t0-relative offset) to exercise the decimal-string encoding at realistic magnitude. " +
			"See this vector's Go doc comment (cases_encoding.go) for what it deliberately does NOT " +
			"prove, and TestTimestampEncodingPreservesArbitraryNanoseconds for the actual old-encoding " +
			"counterexample.",
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: b.ownerPub,
		Events:   []*nostr.Event{c},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v31"}, "focus": {"ready-v31"}, "my-work": {"ready-v31"},
			}),
		},
	})
}
