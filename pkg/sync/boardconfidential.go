// The OWNER-SIGNED CUTOVER ASSERTION: `confidential_since` on a board's own
// kind-30301 definition (spec §11.13a, ready-475).
//
// WHY IT EXISTS. §11.13's cutover is DERIVED — a minimum over the owner CEK
// grants a reader happened to receive — and §11.13a's three witnesses exist
// because that minimum is only ever a LOWER BOUND on the truth. The witnesses
// are testimony, not fact, and TIME (a verified sealed event older than the
// derived instant) can fire on an event that is not evidence about this board's
// cutover at all. Measured on the live `ready` board, 2026-07-30: three kind-1630
// status events (e9d00f5f, 383d967f, effda256) carry enc=1 + cek_epoch=1 and this
// board's `a` tag, but were sealed under a TEST-LOCAL CEK that was never a
// ready-board CEK — the ready-enc-live-* fixtures envelope_live_relay_test.go
// wrote to the production board before the ready-fce guard existed. Kind 1630 is
// a REGULAR nostr event, so unlike an addressable card they can never be
// superseded: they are permanent. TIME therefore fires forever on a board that is
// demonstrably fine (of 36 board events strictly between those fixtures and the
// derived cutover, 36 are plaintext and 0 sealed — the board was PUBLIC through
// that window, so the derived instant is CORRECT), and both readers quarantine
// 167 of 536 cards from the board's own owner.
//
// WHAT IT IS. One additive clear tag on the board's OWN definition event, stating
// the instant its owner says the board went confidential. The 30301 coordinate is
// `30301:<owner>:<d>`, so an assertion is inseparable from the owner's signature
// over it: a relay can DROP the definition, replace it with an older one it has,
// or serve one signed by somebody else, and none of those three yields a wider
// reading than today (see below). The owner is already the authz root for board
// keys (§11.12: only the board author mints CEKs), so this adds no trust root.
//
// WHY IT IS AN EXTENSION AND NOT A WEAKENING — the three properties, each pinned
// by a test that goes red when its own guard is removed
// (keydist_confidentialsince_test.go,
// web/board/src/lib/confidentiality.test.ts, and the shared vectors):
//
//  1. NO ASSERTION -> today's behaviour, exactly. The witnesses are untouched for
//     every board that carries no assertion; nothing about §11.13a's derivation
//     changed.
//  2. A FOREIGN SIGNATURE IS IGNORED. Only a schnorr-verified kind-30301 whose
//     own coordinate IS this board's — which binds the author, since the
//     coordinate embeds the pubkey — can assert anything.
//  3. OMISSION CANNOT WIDEN. A relay that withholds the definition leaves a
//     reader on path 1, which is today's behaviour; it cannot manufacture an
//     assertion (no owner key) and cannot edit one (the tag is inside the signed
//     id).
//
// AND THE ASSERTION CANNOT GRANDFATHER MORE THAN THE GRANTS ALREADY DO: the
// effective cutover is min(asserted, derived). Taking the minimum is what makes
// this monotonically TIGHTENING in the cutover dimension — the assertion can only
// ever move the instant EARLIER, never later — so an owner who asserts an instant
// later than their own earliest served grant (or a relay replaying a stale
// definition that says so) still gets the derived instant. See
// AssertedConfidentialSince.
package sync

import (
	"fmt"
	"strconv"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// TagConfidentialSince is the clear tag name a board definition carries its
// owner's cutover assertion under. Its value is decimal unix SECONDS, the same
// unit as every created_at on the wire (NIP-01).
const TagConfidentialSince = "confidential_since"

// BoardConfidentialSince reads the assertion off ONE kind-30301 event, ok=false
// when the tag is absent, unparseable, or not a positive instant.
//
// A non-positive value is NOT an assertion: 0 is what strconv yields for garbage,
// and "the board went confidential at the unix epoch" is not a statement any
// board makes. Rejecting it here keeps a malformed tag on today's path (property
// 1) instead of pinning the cutover to 0, which would quarantine the board's
// whole plaintext history.
//
// The caller MUST have verified e and established that it is this board's own
// definition — see AssertedConfidentialSince, which is the only production
// reader.
func BoardConfidentialSince(e *nostr.Event) (int64, bool) {
	if e == nil {
		return 0, false
	}
	v, err := strconv.ParseInt(tagValue(e, TagConfidentialSince), 10, 64)
	if err != nil || v <= 0 {
		return 0, false
	}
	return v, true
}

// AssertedConfidentialSince returns the owner-signed cutover assertion for coord
// among events, ok=false when none of them carries one.
//
// THE AUTHOR CHECK IS THE COORDINATE CHECK, and that is the whole of property 2:
// a kind-30301's coordinate is `30301:<its own pubkey>:<its own d tag>`, so an
// event only matches coord when its author IS coord's owner. A definition signed
// by anybody else lands on a different coordinate and is invisible here, exactly
// as a card on a foreign board coordinate is invisible to the board pin (§3.5).
// Verify() then rejects a tampered or forged one, because the relay serving these
// events is untrusted — the same rule DeriveBoardKeyring's CHECK 1 and
// WinningBoardEvent apply.
//
// WHY THE MINIMUM AND NOT LATEST-WINS. A kind-30301 is addressable, so §4.5 would
// say "newest definition wins" — and a relay may serve an OLDER definition it
// still holds instead of the newest. Under latest-wins that replay decides which
// assertion a reader believes; under the minimum it cannot, because the only
// direction a replayed definition can move the effective cutover is EARLIER,
// which quarantines MORE. The cost is that an owner correcting a too-early
// assertion upward must expect the earlier one to keep applying wherever it is
// still served — the fail-closed direction, and the same shape §11.13's own
// minimum-over-grants already has.
func AssertedConfidentialSince(events []*nostr.Event, coord string) (int64, bool) {
	var best int64
	var found bool
	for _, e := range events {
		if e == nil || e.Kind != KindBoard {
			continue
		}
		// The coordinate embeds the author: this IS the owner check.
		if BoardCoord(e.PubKey, tagValue(e, "d")) != coord {
			continue
		}
		since, ok := BoardConfidentialSince(e)
		if !ok {
			continue
		}
		// Last, because it is the expensive one and every event above it is
		// eliminated by a tag comparison first.
		if e.Verify() != nil {
			continue
		}
		if !found || since < best {
			best, found = since, true
		}
	}
	return best, found
}

// BuildBoardEventWithConfidentialSince is BuildBoardEvent plus the owner's
// cutover assertion. A since of 0 (or below) emits NO tag and the result is
// byte-identical to BuildBoardEvent's, so the assertion is strictly additive:
// every board that has never asserted one keeps producing exactly the definition
// event it produced before (property 1, on the WRITE side).
//
// It appends to the built event and re-signs rather than reimplementing
// BuildBoardEvent's tag list, so the two can never drift about what a board
// definition contains. Sign() re-derives the id from the canonical
// serialization, so the re-signed event is a genuine single signature over the
// full tag set — not a signature patched onto a mutated event.
//
// THE ASSERTION IS CARRIED FORWARD BY EVERY OTHER BOARD MUTATION. `rd board
// archive` / `unarchive` republish the definition through this function with the
// value read off the CURRENT one (BoardSpecFromEvent's §16.3 read-modify-write
// rule, applied to a tag BoardSpec does not model), and the ITEM-write path does
// the same through buildBoardDefinition below, because a republish that silently
// dropped the tag would put the board back on the withheld path with no relay
// misbehaving.
func BuildBoardEventWithConfidentialSince(k *nostr.Key, spec BoardSpec, since, createdAt int64) (*nostr.Event, error) {
	e, err := BuildBoardEvent(k, spec, createdAt)
	if err != nil {
		return nil, err
	}
	if since <= 0 {
		return e, nil
	}
	e.Tags = append(e.Tags, []string{TagConfidentialSince, strconv.FormatInt(since, 10)})
	if err := e.Sign(k); err != nil {
		return nil, err
	}
	return e, nil
}

// buildBoardDefinition materializes THIS publisher's own kind-30301 definition
// for spec, carrying the board's current owner-signed cutover assertion forward.
// It is the ONLY way PublishItemWithReason may build a board event.
//
// WHY IT EXISTS — THE FAR MORE COMMON REPUBLISH PATH. `rd board archive` /
// `unarchive` / `confidential-since` all START from the board's CURRENT
// definition (BoardSpecFromEvent) and so carry the tag across by hand. The item
// write path does not: cmd/rd's boardSpecForProject BUILDS a BoardSpec from the
// project directory's name and the board author, from nothing, and
// PublishItemWithReason republishes the definition from it on EVERY owner-signed
// `rd create` / `rd nostr publish` / `rd nostr put` (cmd/rd/nostrwrite.go's
// boardArg — set whenever the signer IS the board author). A kind-30301 is
// addressable, so that republish REPLACES the asserted definition on every
// conformant relay. One ordinary item write by the owner would therefore have
// silently un-asserted the cutover and put the board back to withholding its
// whole plaintext history — no relay misbehaving, nobody having asked for it.
// Routing that build through here is what makes the assertion survive a path
// that never mentions it.
//
// THE LOCAL LOG IS THE SOURCE, and it is the right one for three reasons. It is
// where §16.1 says the durable truth lives; it is the SAME snapshot
// DeriveBoardKeyring reads, so this machine can never republish a definition
// disagreeing with what this machine itself reads; and because the log is
// APPEND-ONLY while AssertedConfidentialSince takes the minimum over every
// definition in it (not latest-wins), an assertion that has ever been seen here
// can never fall out of it again — including one that arrived by `rd nostr sync`
// rather than being published here.
//
// IT FAILS CLOSED. A log this publisher cannot read is a cutover it cannot
// establish, and publishing an unasserted definition on top of an asserted one is
// exactly the silent regression above — so the write is refused instead. That is
// no new failure mode: publishEvents appends every event to this same log a few
// lines later and fails the publish if it cannot.
func (p *Publisher) buildBoardDefinition(spec BoardSpec, createdAt int64) (*nostr.Event, error) {
	coord := BoardCoord(p.Key.PubKeyHex(), spec.BoardD)
	if p.Log == nil {
		return nil, fmt.Errorf("sync: refusing to republish %s's definition with no authoritative log to read its %s assertion from (a dropped assertion silently un-confidentials the board)", coord, TagConfidentialSince)
	}
	events, err := p.Log.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("sync: refusing to republish %s's definition: cannot read the authoritative log to carry its %s assertion forward: %w", coord, TagConfidentialSince, err)
	}
	since, _ := AssertedConfidentialSince(events, coord)
	return BuildBoardEventWithConfidentialSince(p.Key, spec, since, createdAt)
}
