package main

// Re-sealing an already-published PLAINTEXT card in place (ready-a43, epic
// ready-336).
//
// THE DEFECT THIS SERVES. A board that became confidential AFTER it had items
// carries a plaintext tail forever: `rd confidential enable` grandfathers every
// pre-cutover card so it stays readable, which also means it stays readable to
// ANYONE, on a public relay, permanently. Six of nine measured boards are mixed;
// 734 cards were world-readable on wss://relay.3dl.network while `rd confidential
// status` reported the board CONFIDENTIAL. Sealing future writes never closes that.
//
// THE MECHANISM IS ADDRESSABLE REPLACEMENT, NOT DELETION. kind-30302 is addressable
// (kind, author, d), so publishing a sealed card at the SAME coordinate with a later
// created_at makes the relay evict the plaintext copy by the ordinary NIP-01
// replaceable rule. That is deliberately not a NIP-09 delete: a delete request is
// advisory, a relay may keep serving the event, and nothing about it is verifiable
// afterwards. Nothing is destroyed either — the local append-only log keeps BOTH
// events; what changes is which one a stranger can fetch.
//
// THIS IS NOT ROTATION AND MUST NEVER BE REACHED THROUGH IT.
// TestConfidentialRotateDoesNotTouchHistoryAtRest holds a rotation to publishing
// ONLY kind-39301 grants and mutating no pre-existing event. That assertion is
// correct and is not weakened here: a rotation answers a LEAKED KEY and must not
// re-mint thousands of card events, while a re-seal answers PLAINTEXT ON A RELAY and
// must mint exactly one per coordinate. They are separate verbs with separate entry
// points, and re-sealing has its own guard test
// (confidential_reseal_test.go). Rotation calls nothing in this file.
//
// THE SILENT-NO-OP TRAPS THIS FILE EXISTS TO CLOSE — both have already burned this
// project once:
//
//  1. ORDERING (ready-500). Latest-wins is (created_at, then LOWEST event id). A
//     replacement stamped in the same wall-clock second as the original loses the
//     tie-break roughly half the time, and a replacement stamped behind it always
//     loses. Either way the publish "succeeds", the relay keeps serving the
//     plaintext, and every local signal says the card was sealed. resealCard
//     therefore stamps strictly after the event it supersedes and refuses to publish
//     if it cannot.
//
//  2. AUTHORSHIP. The addressable coordinate is (kind, EVENT AUTHOR, d) — the "a"
//     tag names the board, not the coordinate. A card authored by a contributor sits
//     at THAT contributor's coordinate, so an owner publishing a sealed card for the
//     same item id creates a SECOND coordinate instead of replacing the first. rd's
//     own fold would then show the sealed card (later created_at wins across
//     authors) while the relay happily keeps serving the contributor's plaintext one
//     to strangers — a re-seal that looks complete from inside the tool and changes
//     nothing outside it. resealCard refuses that case loudly rather than reporting
//     a success it cannot deliver.
//
// SIZE. Sealing makes an event LARGER and the fleet's relays hard-reject above
// 64 KiB. That guard already exists client-side (pkg/sync/nostrsize.go, ready-c3e):
// the local append still succeeds, the relay dial is skipped, and the event is
// dead-lettered with the byte count. resealCard routes its publish through the
// ordinary Publisher path so it inherits that behaviour, and surfaces it on the
// outcome rather than re-implementing the check.

import (
	"context"
	"errors"
	"fmt"

	"github.com/3dl-dev/ready/pkg/nostr"
	"github.com/3dl-dev/ready/pkg/state"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// errCardAlreadySealed reports that the coordinate's current card already carries a
// confidential envelope, so there is no plaintext copy for a relay to serve.
//
// It is a distinguishable error rather than a silent success because a re-seal MINTS
// a new event id every time it runs: a pass that treated "already sealed" as "seal it
// again" would never converge, would churn a new signed event per coordinate per run,
// and would make its own progress unmeasurable. Callers sweeping a board check it
// with errors.Is and skip.
var errCardAlreadySealed = errors.New("card is already sealed")

// errCardForeignAuthor reports that the plaintext card at this coordinate was signed
// by a different key, so this signer cannot replace it — see trap 2 in the file
// comment. Distinguishable so a sweep can collect these coordinates for a disposition
// (ready-c53) instead of halting or, worse, publishing a sealed sibling and counting
// it as sealed.
var errCardForeignAuthor = errors.New("plaintext card was authored by a different key")

// resealOutcome is what a re-seal did to one coordinate. Every field is recorded
// rather than derived later because the original event id is the only handle a
// caller has on the copy that was superseded — it stays in the local log forever,
// and any reference to it (ready-c9d) resolves against this record.
type resealOutcome struct {
	// ItemID is the coordinate's "d" tag.
	ItemID string
	// OriginalEventID / OriginalCreatedAt identify the plaintext card that was
	// superseded. It is NOT deleted: it remains in the local append-only log.
	OriginalEventID   string
	OriginalCreatedAt int64
	// SealedEventID / SealedCreatedAt / Epoch identify the replacement.
	// SealedCreatedAt is always strictly greater than OriginalCreatedAt.
	SealedEventID   string
	SealedCreatedAt int64
	Epoch           int
	// RelayRejected is true when the sealed card exceeded the fleet's 64 KiB event
	// limit and was dead-lettered instead of reaching a relay (pkg/sync/nostrsize.go).
	// The card IS durable locally and the fold resolves it as the winner, but the
	// RELAY still serves the plaintext original — so for the purpose this command
	// exists for, the coordinate is NOT sealed and the caller must act on it.
	RelayRejected bool
}

// resealCard seals the already-published plaintext card for item in place, by
// publishing a sealed card at the SAME addressable coordinate with a strictly later
// created_at. It publishes exactly ONE event (a kind-30302 card), mints no status
// event and therefore adds no history entry, and leaves every pre-existing event in
// the local append-only log untouched.
//
// item must come from the REAL projection (nostrProjectAllItems / ProjectItems), not
// be hand-assembled: the sealed replacement carries that item's fields, so anything
// the fold would have overlaid must already be resolved. refuseRedactedRepublish is
// the hard stop on the dangerous version of that mistake — re-sealing an item whose
// free text this machine could not decrypt would seal the literal "[encrypted]"
// placeholder AS the card's content and destroy the original in latest-wins, which is
// exactly how four items were lost before ready-76b.
//
// Errors (all before any event is signed) rather than half-acting: the board is
// public, the coordinate is already sealed (errCardAlreadySealed), the plaintext card
// belongs to another author (errCardForeignAuthor), the item is redacted, or no
// sealing key is available.
func resealCard(dir string, pub *rdSync.Publisher, boardAuthor, boardD string, item *state.Item) (*resealOutcome, error) {
	if item == nil {
		return nil, fmt.Errorf("reseal: no item")
	}
	coord := rdSync.BoardCoord(boardAuthor, boardD)
	if !boardIsConfidential(dir) {
		return nil, fmt.Errorf("refusing to re-seal %s: board %s is PUBLIC, so there is nothing to seal to and no reader is expected to hold a key; run `rd confidential enable` first", item.ID, coord)
	}
	// An item this machine could not decrypt must never be republished — see the doc
	// comment. Checked before anything else that could mint key material.
	if err := refuseRedactedRepublish(item); err != nil {
		return nil, err
	}

	// Resolve the sealing envelope FIRST: on a board marked confidential but not yet
	// bootstrapped this mints the CEK and publishes the owner self-grant, and that
	// self-grant establishes the cutover. Doing it before the created_at is computed
	// keeps the bootstrap out of the ordering decision below.
	env, err := boardConfidentialEnvelope(dir, pub, boardAuthor, boardD)
	if err != nil {
		return nil, err
	}
	if env == nil {
		return nil, fmt.Errorf("refusing to re-seal %s: board %s yielded no sealing key (unpinned board?), and publishing an unsealed replacement would leave the plaintext exactly where it is", item.ID, coord)
	}

	events, err := pub.Log.ReadAll()
	if err != nil {
		return nil, fmt.Errorf("reading log for %s: %w", item.ID, err)
	}
	original, ok := rdSync.WinningCardEvent(events, coord, item.ID)
	if !ok {
		return nil, fmt.Errorf("refusing to re-seal %s: the local log holds no kind-%d card for it on board %s, so there is no coordinate to replace", item.ID, rdSync.KindCard, coord)
	}
	if isSealedCard(original) {
		return nil, fmt.Errorf("%s (event %s at board %s): %w", item.ID, shortID(original.ID), coord, errCardAlreadySealed)
	}
	signer := pub.Key.PubKeyHex()
	if original.PubKey != signer {
		return nil, fmt.Errorf("refusing to re-seal %s: its plaintext card (event %s) is signed by %s, and kind-%d is addressable on (kind, AUTHOR, d) — a card signed by %s would occupy a DIFFERENT coordinate and would not evict that plaintext copy from any relay: %w",
			item.ID, shortID(original.ID), shortKey(original.PubKey), rdSync.KindCard, shortKey(signer), errCardForeignAuthor)
	}

	card := rdSync.CardSpecFromItem(item, boardD)
	card.BoardAuthor = boardAuthor
	// 'blocked' is DERIVED by the fold's dep pass and must never be written back
	// (ready-500). A re-seal republishes a projected item verbatim, so it is exactly
	// the shape of republish NonDerivedStatus exists for.
	card.Status = rdSync.NonDerivedStatus(item)
	card.Enc = env

	// STRICTLY AFTER the event being superseded. nostrNextCreatedAt already returns
	// max(now, newest-in-this-item's-chain + 1) and the original card is in that
	// chain, so this normally just takes it; the explicit floor is kept because the
	// invariant belongs to THIS operation, not to the drift scoping, and a tie here
	// is a silent no-op rather than an error (ready-500).
	createdAt := nostrNextCreatedAt(pub.Log, rdSync.ItemDriftScope(item.ID))
	if createdAt <= original.CreatedAt {
		createdAt = original.CreatedAt + 1
	}

	// Built here rather than via PublishCardEdit (identical bytes — that helper is
	// BuildCardEvent + the same publish) so the replacement's event id is known
	// exactly, instead of being scraped back out of the publish acks.
	sealed, err := rdSync.BuildCardEvent(pub.Key, card, createdAt)
	if err != nil {
		return nil, fmt.Errorf("building sealed replacement for %s: %w", item.ID, err)
	}
	res, err := pub.PublishEvents(context.Background(), []*nostr.Event{sealed})
	if err != nil {
		return nil, fmt.Errorf("publishing sealed replacement for %s: %w", item.ID, err)
	}
	return &resealOutcome{
		ItemID:            item.ID,
		OriginalEventID:   original.ID,
		OriginalCreatedAt: original.CreatedAt,
		SealedEventID:     sealed.ID,
		SealedCreatedAt:   sealed.CreatedAt,
		Epoch:             env.Epoch,
		RelayRejected:     res.Rejected,
	}, nil
}

// isSealedCard reports whether a card event carries a confidential envelope marker,
// i.e. whether its free text is ciphertext at rest. This is the same "sealed" test
// the epic's relay inventory uses (an ["enc","1"] tag), and deliberately NOT the
// fold's stricter encWellFormed: a card with a malformed envelope is not something
// to re-seal blindly, it is a separate defect, and quietly overwriting it here would
// bury it.
func isSealedCard(e *nostr.Event) bool { return tagVal1(e, "enc") != "" }

// shortID abbreviates an event id for operator-facing messages.
func shortID(id string) string {
	if len(id) <= 12 {
		return id
	}
	return id[:12]
}
