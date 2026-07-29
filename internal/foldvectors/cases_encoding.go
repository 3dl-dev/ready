package foldvectors

// cases_encoding.go — ready-414: the vector file's cross-implementation
// encoding of item timestamps.

import (
	"fmt"

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
// large, non-tidy nanosecond magnitude that is well BELOW the float64-safe
// bound derived in spec §4.8 (sec <= 4,611,686,018) — bigWallClockSec is
// ~1.7 billion, so this value round-trips correctly under the old bare-number
// encoding too. Its job is narrower than proving the encoding necessary: it
// proves the NEW encoding is correct for a realistic, non-trivial magnitude,
// in the one place (the vector file) that changed. Two OTHER cases carry the
// necessity proof this one does not:
//   - item_timestamp_above_float64_safe_bound (below, same file): a
//     fold-checked vector at sec=4,611,686,019 — ABOVE the bound — whose
//     nanosecond value the live fold actually produces and which provably
//     does not survive a float64 round-trip.
//   - TestTimestampEncodingPreservesArbitraryNanoseconds in vectors_test.go:
//     a synthetic, genuinely non-round int64 nanosecond value the fold's
//     `sec * int64(time.Second)` formula (spec §4.6) never produces (it is
//     not a multiple of 1e9), included because the field's declared type
//     (arbitrary int64 nanoseconds) permits values the fold itself never
//     emits, and the encoding must be correct for those too.
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
			"t0-relative offset) to exercise the decimal-string encoding at realistic magnitude, well " +
			"below the float64-safe bound (spec §4.8). See this vector's Go doc comment " +
			"(cases_encoding.go) for what it deliberately does NOT prove, and " +
			"item_timestamp_above_float64_safe_bound (same file) plus " +
			"TestTimestampEncodingPreservesArbitraryNanoseconds for the actual old-encoding " +
			"counterexamples.",
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

// aboveFloatSafeBoundSec is the SMALLEST unix-second value whose nanosecond
// derivation (sec * int64(time.Second), spec §4.6) does NOT survive an
// IEEE-754 double round-trip: 4,611,686,019 has no factor of two, so spec
// §4.8's bound (sec <= 4,611,686,018 is guaranteed exact) applies exactly —
// one second past it, the odd-part-times-5^9 test fails and the nanosecond
// value is off by 512ns after a float64 round-trip. Verified directly (see
// the fixture check inside vItemTimestampAboveFloat64SafeBound, which fails
// loudly rather than silently proving nothing if this ever stops being true).
const aboveFloatSafeBoundSec = int64(4611686019)

// vItemTimestampAboveFloat64SafeBound is the REAL counterexample the false
// lemma in earlier drafts of spec §4.8 claimed could not exist: a fold-checked
// vector (built the same way as every other case here — a real signed card,
// replayed through the live fold, no bypass) whose created_at/updated_at the
// live fold ACTUALLY produces, and which is genuinely lossy under the old
// bare-number encoding. It differs from vItemTimestampEncoding (above) only in
// magnitude: that one picks a big-but-safe value; this one picks the smallest
// value that is provably unsafe.
func (b *builder) vItemTimestampAboveFloat64SafeBound() error {
	ts := nanos(aboveFloatSafeBoundSec)
	// Ground truth this vector depends on: if this ever became exact, the
	// vector would silently stop proving anything about the old encoding —
	// fail loudly instead of shipping a vacuous case.
	if int64(float64(ts)) == ts {
		return fmt.Errorf(
			"vItemTimestampAboveFloat64SafeBound: sec=%d no longer exceeds the float64-safe bound "+
				"(spec §4.8) — the fixture needs a new value above 4,611,686,018 with no factor of two",
			aboveFloatSafeBoundSec)
	}

	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v32", Title: "Timestamp above the float64-safe bound", Status: state.StatusInbox,
		Priority: "p2", Type: "task", Assignee: b.ownerPub,
	}, aboveFloatSafeBoundSec)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v32", MsgID: c.ID, Title: "Timestamp above the float64-safe bound",
		Type: "task", Priority: "p2", Status: state.StatusInbox, By: b.ownerPub,
		CreatedAt: ts, UpdatedAt: ts,
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "item_timestamp_above_float64_safe_bound",
		SpecClauses: []string{"4.8", "4.6", "5.1"},
		Note: fmt.Sprintf(
			"created_at/updated_at at sec=%d, ABOVE the float64-safe bound spec §4.8 derives "+
				"(sec <= 4,611,686,018 is guaranteed exact for a sec with no factor of two). This is a "+
				"GENUINE counterexample to the old bare-number encoding, produced by the live fold from "+
				"a real signed event, not a synthetic value the type merely permits: "+
				"int64(float64(%d)) == %d, a 512ns miss. A bare-number vector file would have handed an "+
				"independent client a value that fails JSON.parse-then-compare against this item's real "+
				"created_at.",
			aboveFloatSafeBoundSec, ts, int64(float64(ts))),
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: b.ownerPub,
		Events:   []*nostr.Event{c},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v32"}, "focus": {"ready-v32"}, "my-work": {"ready-v32"},
			}),
		},
	})
}

// itemContextTimestampBytePattern is a Context value crafted to contain the
// exact byte shape EncodeItem's two target fields have: a double-quoted key
// immediately followed by a colon and digits.
const itemContextTimestampBytePattern = `"created_at":123`

// vItemContextContainsTimestampBytePattern pins that EncodeItem only ever
// touches the item's OWN top-level created_at/updated_at fields, never a
// byte-identical pattern occurring inside another field's content (ready-414
// review finding: a regex applied to already-marshaled bytes would rewrite
// this pattern wherever it appeared, not only at the two real field
// positions).
//
// DISCLOSURE, verified directly (reverted EncodeItem to the prior regex
// implementation, ran this exact fixture, inspected the output): this
// specific literal does NOT, in fact, trigger a false match under that PRIOR
// implementation either. encoding/json unconditionally backslash-escapes any
// `"` inside string content, and a JSON value's closing delimiter is always
// followed by `,` or `}`, never `:` (only a KEY position is) — so the old
// regex's required unescaped quote+word+quote+colon shape could only ever
// occur at the two real field-key positions, for any string content this
// struct can carry today. This vector is kept anyway as a forward-looking
// structural regression pin, not as a proof the old code was broken: it locks
// today's decode-based implementation's correct behaviour against this
// content, so if a future change reintroduces a byte-level transform (or adds
// a field type — e.g. json.RawMessage or map[string]any — where the escaping
// guarantee above no longer holds), this vector's expectation is already on
// record to catch it.
func (b *builder) vItemContextContainsTimestampBytePattern() error {
	c, err := b.card(b.owner, rdsync.CardSpec{
		ItemID: "ready-v33", Title: "Context contains a timestamp-shaped byte pattern",
		Status: state.StatusInbox, Priority: "p2", Type: "task", Assignee: b.ownerPub,
		Context: itemContextTimestampBytePattern,
	}, t0)
	if err != nil {
		return err
	}
	items, err := itemsJSON(&state.Item{
		ID: "ready-v33", MsgID: c.ID, Title: "Context contains a timestamp-shaped byte pattern",
		Context: itemContextTimestampBytePattern, Description: itemContextTimestampBytePattern,
		Type: "task", Priority: "p2", Status: state.StatusInbox, By: b.ownerPub,
		CreatedAt: nanos(t0), UpdatedAt: nanos(t0),
	})
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "item_context_contains_timestamp_byte_pattern",
		SpecClauses: []string{"4.8", "5.1"},
		Note: "Context/Description literally contain the byte pattern \"created_at\":123 -- the exact " +
			"shape EncodeItem's target fields have. Pins that EncodeItem operates on the DECODED item " +
			"(a map[string]RawMessage keyed by field name), not a byte-pattern match over marshaled " +
			"JSON, so this content is never mistaken for the item's own created_at/updated_at and never " +
			"corrupts the surrounding JSON. See this vector's Go doc comment " +
			"(vItemContextContainsTimestampBytePattern, cases_encoding.go) for a disclosure: this " +
			"specific literal did not, in fact, defeat ready-414's PRIOR regex-based implementation " +
			"either, because encoding/json's escaping guarantees rule it out for this struct's current " +
			"field types — this vector is a forward-looking structural pin, not a proof the prior code " +
			"was broken.",
		Options:  Options{Trusted: trust(b.ownerPub)},
		Identity: b.ownerPub,
		Events:   []*nostr.Event{c},
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": {"ready-v33"}, "focus": {"ready-v33"}, "my-work": {"ready-v33"},
			}),
		},
	})
}

// rawCreatedCard builds and signs a 30302 card exactly like builder.card,
// then OVERWRITES its "created" tag value with rawCreated verbatim BEFORE
// signing -- spec.CreatedAt is only a placeholder to make BuildCardEvent
// emit a "created" tag in the first place (any nonzero value works; its wire
// value is replaced below). A relay accepts any validly signed tag content:
// only the fold's own parser decides whether a "created" tag is
// trustworthy. This is what lets vCreatedTagRejectsNonCanonicalShapes prove
// Go and TS AGREE on rejecting a non-canonical tag, not merely that each
// happens to reject the one shape its own author tried by hand.
func (b *builder) rawCreatedCard(k *nostr.Key, spec rdsync.CardSpec, createdAt int64, rawCreated string) (*nostr.Event, error) {
	if spec.CreatedAt == 0 {
		spec.CreatedAt = 1
	}
	ev, err := rdsync.BuildCardEvent(k, spec, createdAt)
	if err != nil {
		return nil, err
	}
	replaced := false
	for i, tag := range ev.Tags {
		if len(tag) >= 1 && tag[0] == "created" {
			ev.Tags[i] = []string{"created", rawCreated}
			replaced = true
		}
	}
	if !replaced {
		ev.Tags = append(ev.Tags, []string{"created", rawCreated})
	}
	if err := ev.Sign(k); err != nil {
		return nil, err
	}
	return ev, nil
}

// vCreatedTagRejectsNonCanonicalShapes pins the adversarial half of the
// carried-"created" tag mechanism (§5.1, ready-4ec rework 3): a validly
// RE-SIGNED card can carry ANY tag content an adversary chooses, and Go's
// strconv.ParseInt(raw, 10, 64) alone accepts a DIFFERENT input set than
// TS's BigInt(raw) alone -- a leading '+', leading zeros, surrounding
// whitespace, an empty string read as zero, and unbounded magnitude all
// diverge between the two parsers used bare. A forged non-canonical tag
// folded through both real projections previously landed on DIFFERENT
// CreatedAt values for the identical event -- a divergence that sat red on
// main for seven merges before this vector existed to catch it.
// parseCanonicalCreatedTag (Go, pkg/sync/nostrproject.go) and its TS mirror
// (web/board/src/lib/fold.ts) now both require EXACTLY the canonical
// decimal-integer shape strconv.FormatInt ever produces, so every one of
// these eight non-canonical shapes is REJECTED on both sides and each
// item's CreatedAt falls back to its own card's wire created_at, never the
// forged value. Each card's own created_at is deliberately far from what its
// forged tag claims, so a wrongly-ACCEPTED tag would produce an obviously
// wrong CreatedAt, never a coincidentally-matching one.
func (b *builder) vCreatedTagRejectsNonCanonicalShapes() error {
	type shape struct {
		id  string
		raw string
	}
	// claimedSec is a value each malformed tag's DIGITS encode that is
	// DELIBERATELY far from any card's own created_at below: if a parser ever
	// wrongly ACCEPTS one of these non-canonical shapes, the resulting
	// CreatedAt lands on (a variant of) claimedSec, not on the card's own
	// created_at -- so an accept/reject regression on EITHER side changes
	// this vector's expected item, not just its own internal consistency.
	// (Reusing each card's own created_at as the malformed digits would make
	// wrongly-accepting and correctly-rejecting numerically indistinguishable
	// for several of these shapes -- a vector that could never fail no
	// matter how the parser regressed.)
	const claimedSec = "1709000000"
	shapes := []shape{
		{"ready-v35a", "+" + claimedSec},              // leading plus
		{"ready-v35b", "000" + claimedSec},            // leading zeros
		{"ready-v35c", " " + claimedSec},               // leading whitespace
		{"ready-v35d", ""},                             // explicit empty tag value
		{"ready-v35e", "99999999999999999999999999"},  // beyond int64 magnitude
		{"ready-v35f", "-" + claimedSec},               // negative
		{"ready-v35g", "not-a-number"},                 // non-numeric
		{"ready-v35h", claimedSec + ".5"},               // fractional
	}
	var events []*nostr.Event
	var expects []*state.Item
	var readyIDs []string
	for i, s := range shapes {
		own := t0 + int64(100*(i+1))
		title := "Non-canonical created tag " + s.id
		e, err := b.rawCreatedCard(b.owner, rdsync.CardSpec{
			ItemID: s.id, Title: title, Status: state.StatusInbox,
			Priority: "p2", Type: "task",
		}, own, s.raw)
		if err != nil {
			return err
		}
		if err := e.Verify(); err != nil {
			return fmt.Errorf("vCreatedTagRejectsNonCanonicalShapes: %s must verify (validly "+
				"re-signed, not a forged-signature case): %w", s.id, err)
		}
		events = append(events, e)
		expects = append(expects, &state.Item{
			ID: s.id, MsgID: e.ID, Title: title,
			Type: "task", Priority: "p2", Status: state.StatusInbox,
			CreatedAt: nanos(own), UpdatedAt: nanos(own),
		})
		readyIDs = append(readyIDs, s.id)
	}
	items, err := itemsJSON(expects...)
	if err != nil {
		return err
	}
	return b.add(Vector{
		Name:        "created_tag_rejects_non_canonical_shapes",
		SpecClauses: []string{"5.1", "5.6"},
		Note: "Eight validly re-signed cards, each carrying one non-canonical \"created\" tag shape " +
			"(leading '+', leading zeros, leading whitespace, empty, beyond int64, negative, " +
			"non-numeric, fractional). parseCanonicalCreatedTag rejects all eight on both Go and TS, " +
			"so every item's CreatedAt falls back to its own card's wire created_at -- proving the two " +
			"languages' raw parsers (strconv.ParseInt vs BigInt), which accept DIFFERENT input sets on " +
			"their own, now agree once gated by the shared canonical-format pre-check.",
		Options: Options{Trusted: trust(b.ownerPub)},
		Events:  events,
		Expect: Expect{
			Items: items,
			Views: vw(map[string][]string{
				"ready": readyIDs, "focus": readyIDs,
			}),
		},
	})
}
