// Projected sealed wire size for an already-published PLAINTEXT card (ready-c53,
// epic ready-336).
//
// WHY A PROJECTION IS NEEDED AT ALL. Sealing makes an event LARGER — the free text
// moves from clear tags and clear Content into a nonce + AEAD ciphertext + base64
// blob, and two marker tags arrive — while relays hard-reject above 64 KiB
// (maxEventWireSize). So a coordinate can be comfortably under the limit as
// plaintext and OVER it once sealed. Those are the dangerous ones: nothing about the
// card as it stands today says so.
//
// rd already refuses an oversized event loudly rather than stranding it silently
// (ready-c3e), which means a board-wide re-seal HALTS on such a coordinate instead of
// corrupting anything. That is the correct behaviour and exactly why the set has to
// be known BEFORE execution — otherwise the pass stops partway through a live board
// with no plan for the card it stopped on.
//
// WHY THIS COMPUTES RATHER THAN ESTIMATES. An estimate ("ciphertext is about 4/3 of
// plaintext") is the wrong tool for a decision whose failure mode is halting a
// data-plane operation across eight live projects. This seals the real payload with
// the real code path — the same cardPayload marshal, the same sealContent AEAD, the
// same tag transformation BuildCardEvent applies — under a THROWAWAY key, and
// measures the actual marshaled bytes. A throwaway key is exact for this purpose:
// ChaCha20-Poly1305 ciphertext length depends only on plaintext length, and a
// signature and event id are fixed-width hex regardless of which key produced them.
//
// MEASURE-ONLY. Nothing here signs with a real identity, publishes, or touches a
// relay or the local log. The events it builds exist only to be counted.
package sync

import (
	"encoding/json"
	"fmt"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// SealedSizeProjection is the before/after for one coordinate.
type SealedSizeProjection struct {
	// PlaintextBytes is the card's wire size as the relay serves it today.
	PlaintextBytes int
	// SealedBytes is the wire size the sealed replacement would have.
	SealedBytes int
	// OverLimit is true when SealedBytes exceeds what any relay in the fleet
	// accepts, i.e. the re-seal of this coordinate would be refused.
	OverLimit bool
	// Limit is the ceiling applied, recorded so a stored projection cannot be
	// misread later against a different assumed limit.
	Limit int
}

// ProjectSealedWireSize computes what a plaintext kind-30302 card would weigh once
// re-sealed, without sealing it for real.
//
// It models the tag transformation BuildCardEvent performs in confidential mode: the
// clear `title`, `waiting_on` and `l` tags leave (their values move into the sealed
// blob) and `enc` + `cek_epoch` arrive. Labels are counted in their LARGER form — an
// LTK-tokenized `l` tag per label, 64 hex characters each — because a board carrying
// a Label Token Key emits those and a board without one emits nothing, so the
// tokenized form is the ceiling. A projection that is wrong must be wrong in the
// direction that over-reports risk, never under.
//
// It returns an error for a non-card event or one already sealed: neither has a
// plaintext free-text set to project from, and quietly returning a number for them
// would put a meaningless row in a disposition list someone is going to act on.
func ProjectSealedWireSize(e *nostr.Event) (SealedSizeProjection, error) {
	var out SealedSizeProjection
	if e == nil {
		return out, fmt.Errorf("sync: sealed-size projection: nil event")
	}
	if e.Kind != KindCard {
		return out, fmt.Errorf("sync: sealed-size projection: event %s is kind %d, not a kind-%d card", e.ID, e.Kind, KindCard)
	}
	if tagValue(e, tagEnc) != "" {
		return out, fmt.Errorf("sync: sealed-size projection: card %s is already sealed, so there is no plaintext to project", tagValue(e, "d"))
	}
	plainBytes, err := marshaledEventSize(e)
	if err != nil {
		return out, err
	}

	var throwawayCEK, throwawayLTK [32]byte
	sealedContent, tags, err := sealPlaintextCardParts(e, throwawayCEK, &throwawayLTK, &Envelope{Epoch: cekEpochSizeCeiling})
	if err != nil {
		return out, fmt.Errorf("sync: sealed-size projection: %w", err)
	}

	k, err := nostr.GenerateKey()
	if err != nil {
		return out, fmt.Errorf("sync: sealed-size projection: throwaway key: %w", err)
	}
	sealed := &nostr.Event{Kind: e.Kind, CreatedAt: e.CreatedAt, Tags: tags, Content: sealedContent}
	if err := sealed.Sign(k); err != nil {
		return out, fmt.Errorf("sync: sealed-size projection: sign projection event: %w", err)
	}
	sealedBytes, err := marshaledEventSize(sealed)
	if err != nil {
		return out, err
	}
	return SealedSizeProjection{
		PlaintextBytes: plainBytes,
		SealedBytes:    sealedBytes,
		OverLimit:      sealedBytes > maxEventWireSize,
		Limit:          maxEventWireSize,
	}, nil
}

// cekEpochSizeCeiling is the epoch number the projection stamps into its throwaway
// ["cek_epoch","N"] tag. It is deliberately a wide value rather than the board's
// actual epoch: the tag is a decimal string, so a board that has rotated into
// double digits carries a byte more than one that has not, and a projection must
// not under-report because the board happens to be on epoch 1 today.
const cekEpochSizeCeiling = 999999

// sealPlaintextCardParts turns a PLAINTEXT card event into the content and tag set
// its sealed replacement carries: the free text (title, context, waiting_on, labels)
// moves into the sealed blob, every CLEAR routing tag survives untouched, labels
// come back as HMAC tokens, and the enc markers are appended.
//
// It is shared by the size projection and by the real sealer (SealPlaintextCard) on
// purpose. Those two must agree exactly — a projection that measured a different
// shape than the sealer produces would be a size guard measuring the wrong event,
// which is the failure mode ready-c3e exists to prevent.
func sealPlaintextCardParts(e *nostr.Event, cek [32]byte, ltk *[32]byte, env *Envelope) (string, [][]string, error) {
	var labels []string
	for _, tg := range e.Tags {
		if len(tg) > 1 && tg[0] == "l" {
			labels = append(labels, tg[1])
		}
	}
	payload, err := json.Marshal(cardPayload{
		Title:     tagValue(e, "title"),
		Context:   e.Content,
		WaitingOn: tagValue(e, "waiting_on"),
		Labels:    labels,
	})
	if err != nil {
		return "", nil, fmt.Errorf("marshal payload: %w", err)
	}
	sealedContent, err := sealContent(cek, payload)
	if err != nil {
		return "", nil, fmt.Errorf("seal payload: %w", err)
	}
	tags := make([][]string, 0, len(e.Tags)+2)
	for _, tg := range e.Tags {
		if len(tg) > 0 && (tg[0] == "title" || tg[0] == "waiting_on" || tg[0] == "l") {
			continue
		}
		tags = append(tags, tg)
	}
	// Matches BuildCardEvent exactly (pkg/sync/nostrwire.go): with an LTK the clear
	// l value is an owner-keyed HMAC token; WITHOUT one, NO clear l tag is emitted at
	// all, because a plaintext label tag on a confidential board leaks the label. The
	// label rides in the sealed blob either way. The size projection passes a
	// non-nil throwaway LTK deliberately, so it measures the LARGER form.
	if ltk != nil {
		for _, label := range labels {
			tags = append(tags, []string{"l", labelToken(*ltk, label)})
		}
	}
	tags = append(tags, encMarkerTags(env)...)
	return sealedContent, tags, nil
}

// SealPlaintextCard builds the sealed replacement for a PLAINTEXT card event, from
// that event's own bytes, signed by k and stamped at createdAt.
//
// WHY THIS EXISTS SEPARATELY FROM resealCard. The ordinary re-seal path rebuilds the
// card from the PROJECTED item, and must: a projected item has had the fold's
// overlays applied, and re-sealing an item this machine could not decrypt would seal
// the literal "[encrypted]" placeholder as the card's content and destroy the
// original in latest-wins (ready-76b).
//
// But a card the fold QUARANTINES never becomes an item at all — on a confidential
// board a plaintext card published after the cutover is dropped from the projection
// (ready-710, fail-closed), while a relay keeps serving its cleartext to anyone. The
// item-based path cannot reach those coordinates, so without this they stay readable
// forever.
//
// Sealing from the event's own bytes is SAFE HERE FOR THE REASON THE GENERAL CASE IS
// NOT: the source card is plaintext. There is no ciphertext to mis-decrypt and no
// placeholder to mistake for content — the bytes on the wire ARE the free text. The
// caller is still responsible for the two checks this cannot make: that the card is
// signed by the key doing the sealing (an owner must never launder another writer's
// quarantined cleartext into the projection), and that the replacement is stamped
// strictly above what the relay serves.
func SealPlaintextCard(k *nostr.Key, e *nostr.Event, env *Envelope, createdAt int64) (*nostr.Event, error) {
	if e == nil || k == nil {
		return nil, fmt.Errorf("sync: seal plaintext card: nil event or key")
	}
	if e.Kind != KindCard {
		return nil, fmt.Errorf("sync: seal plaintext card: event %s is kind %d, not a kind-%d card", e.ID, e.Kind, KindCard)
	}
	if tagValue(e, tagEnc) != "" {
		return nil, fmt.Errorf("sync: seal plaintext card: %s is already sealed", tagValue(e, "d"))
	}
	if env == nil {
		return nil, fmt.Errorf("sync: seal plaintext card: no sealing envelope for %s", tagValue(e, "d"))
	}
	content, tags, err := sealPlaintextCardParts(e, env.CEK, env.LTK, env)
	if err != nil {
		return nil, fmt.Errorf("sync: seal plaintext card %s: %w", tagValue(e, "d"), err)
	}
	sealed := &nostr.Event{Kind: e.Kind, CreatedAt: createdAt, Tags: tags, Content: content}
	if err := sealed.Sign(k); err != nil {
		return nil, fmt.Errorf("sync: seal plaintext card %s: sign: %w", tagValue(e, "d"), err)
	}
	return sealed, nil
}
