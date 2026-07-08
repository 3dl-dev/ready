// Package nostr implements the generic nostr NIP-01 event pipeline used by rd
// to publish work-management state to relays: canonical event construction,
// event-id derivation, BIP-340 schnorr signing over secp256k1, and independent
// verification.
//
// This package is deliberately generic. It does NOT map rd work items to events
// (that is a downstream concern) — it only provides the sign -> publish ->
// verify primitives and proves the loop end-to-end against a live relay.
//
// Canonical serialization follows NIP-01 EXACTLY: the event id is the sha256 of
// the UTF-8 JSON array
//
//	[0, <pubkey>, <created_at>, <kind>, <tags>, <content>]
//
// serialized with no insignificant whitespace and with the NIP-01 string
// escaping rules (see serializeString). This is NOT a campfire signing envelope
// and shares no code with campfire's ed25519 signing.
package nostr

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"

	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// Event is a nostr event as defined by NIP-01. Field JSON tags match the wire
// format relays expect. ID, PubKey and Sig are lowercase hex; Tags is a list of
// string arrays; Content is arbitrary UTF-8.
type Event struct {
	ID        string     `json:"id"`
	PubKey    string     `json:"pubkey"`
	CreatedAt int64      `json:"created_at"`
	Kind      int        `json:"kind"`
	Tags      [][]string `json:"tags"`
	Content   string     `json:"content"`
	Sig       string     `json:"sig"`
}

// canonicalForID returns the exact NIP-01 canonical byte serialization that the
// event id is computed over: the JSON array [0, pubkey, created_at, kind, tags,
// content]. The pubkey used here is the event's PubKey field (x-only, 32-byte
// lowercase hex). No insignificant whitespace is emitted.
func (e *Event) canonicalForID() []byte {
	buf := make([]byte, 0, 128+len(e.Content))
	buf = append(buf, '[')
	// 0
	buf = append(buf, '0', ',')
	// pubkey (string)
	buf = serializeString(buf, e.PubKey)
	buf = append(buf, ',')
	// created_at (number)
	buf = strconv.AppendInt(buf, e.CreatedAt, 10)
	buf = append(buf, ',')
	// kind (number)
	buf = strconv.AppendInt(buf, int64(e.Kind), 10)
	buf = append(buf, ',')
	// tags (array of arrays of strings)
	buf = append(buf, '[')
	for i, tag := range e.Tags {
		if i > 0 {
			buf = append(buf, ',')
		}
		buf = append(buf, '[')
		for j, el := range tag {
			if j > 0 {
				buf = append(buf, ',')
			}
			buf = serializeString(buf, el)
		}
		buf = append(buf, ']')
	}
	buf = append(buf, ']')
	buf = append(buf, ',')
	// content (string)
	buf = serializeString(buf, e.Content)
	buf = append(buf, ']')
	return buf
}

// serializeString appends the NIP-01 JSON-escaped form of s (including the
// surrounding double quotes) to dst. NIP-01 mandates a specific, minimal escape
// set: the backslash escapes for " \ \n \r \t \b \f, a \u00XX escape for any
// remaining control character below 0x20, and every other byte (including all
// multi-byte UTF-8 and characters like < > &) emitted verbatim. This differs
// from encoding/json.Marshal, which HTML-escapes < > & and  / , so we
// must not use the stdlib marshaller for the id preimage.
func serializeString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '"':
			dst = append(dst, '\\', '"')
		case '\\':
			dst = append(dst, '\\', '\\')
		case '\n':
			dst = append(dst, '\\', 'n')
		case '\r':
			dst = append(dst, '\\', 'r')
		case '\t':
			dst = append(dst, '\\', 't')
		case '\b':
			dst = append(dst, '\\', 'b')
		case '\f':
			dst = append(dst, '\\', 'f')
		default:
			if c < 0x20 {
				const hexdigits = "0123456789abcdef"
				dst = append(dst, '\\', 'u', '0', '0', hexdigits[c>>4], hexdigits[c&0xf])
			} else {
				dst = append(dst, c)
			}
		}
	}
	dst = append(dst, '"')
	return dst
}

// ComputeID derives the NIP-01 event id: the lowercase hex sha256 of the
// canonical serialization. It uses the event's current field values and does
// NOT mutate the event.
func (e *Event) ComputeID() string {
	sum := sha256.Sum256(e.canonicalForID())
	return hex.EncodeToString(sum[:])
}

// idBytes returns the raw 32-byte sha256 digest used both as the stored id and
// as the message that is schnorr-signed.
func (e *Event) idBytes() [32]byte {
	return sha256.Sum256(e.canonicalForID())
}

// Sign fills in the event's PubKey, CreatedAt (if unset), ID and Sig fields by
// signing with the given key. It computes the canonical id and produces a
// BIP-340 schnorr signature over the 32-byte id digest, exactly as NIP-01
// requires. CreatedAt must already be set by the caller for a fully
// deterministic event; if it is zero this returns an error rather than silently
// stamping a time, so that id derivation stays reproducible and testable.
func (e *Event) Sign(k *Key) error {
	if k == nil {
		return errors.New("nostr: nil signing key")
	}
	if e.CreatedAt == 0 {
		return errors.New("nostr: CreatedAt must be set before signing")
	}
	e.PubKey = k.PubKeyHex()

	id := e.idBytes()
	sig, err := schnorr.Sign(k.priv, id[:])
	if err != nil {
		return fmt.Errorf("nostr: schnorr sign: %w", err)
	}
	e.ID = hex.EncodeToString(id[:])
	e.Sig = hex.EncodeToString(sig.Serialize())
	return nil
}

// Verify independently re-derives the event id from the canonical serialization
// and verifies the BIP-340 schnorr signature against the event's x-only pubkey.
// It returns nil only when (a) the stored ID matches the recomputed canonical
// id AND (b) the signature verifies. Any tampering with pubkey, created_at,
// kind, tags, content, id, or sig causes a non-nil error. This is the
// tamper-rejection gate.
func (e *Event) Verify() error {
	// Re-derive the id from the canonical serialization. If the stored id was
	// tampered with (or any signed field changed), this mismatch rejects.
	want := e.idBytes()
	wantHex := hex.EncodeToString(want[:])
	if e.ID != wantHex {
		return fmt.Errorf("nostr: id mismatch: stored %s, computed %s", e.ID, wantHex)
	}

	pkBytes, err := hex.DecodeString(e.PubKey)
	if err != nil {
		return fmt.Errorf("nostr: decode pubkey: %w", err)
	}
	if len(pkBytes) != 32 {
		return fmt.Errorf("nostr: pubkey must be 32-byte x-only, got %d bytes", len(pkBytes))
	}
	pub, err := schnorr.ParsePubKey(pkBytes)
	if err != nil {
		return fmt.Errorf("nostr: parse pubkey: %w", err)
	}

	sigBytes, err := hex.DecodeString(e.Sig)
	if err != nil {
		return fmt.Errorf("nostr: decode sig: %w", err)
	}
	sig, err := schnorr.ParseSignature(sigBytes)
	if err != nil {
		return fmt.Errorf("nostr: parse sig: %w", err)
	}

	if !sig.Verify(want[:], pub) {
		return errors.New("nostr: schnorr signature verification failed")
	}
	return nil
}
