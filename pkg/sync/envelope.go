// Confidential-board envelope primitives (epic ready-216, write item ready-e63).
//
// Implements the FROZEN wire contract in
// docs/design/confidential-boards-envelope.md: a confidential board encrypts
// ONLY free text into event.Content while every relay-indexed routing tag stays
// plaintext (the label value is replaced by an owner-keyed HMAC token). This file
// is the single crypto+encoding seam shared by the write path (seal), the read
// path (open), and label tokenization.
//
// Content wire format (spec §3):
//
//	event.Content = base64Std( nonce(12) ‖ ChaCha20-Poly1305(CEK, nonce, plaintext) )
//
// The AEAD here (ChaCha20-Poly1305 over the CONTENT body under the per-board CEK)
// is DISTINCT from the NIP-44 v2 envelope (pkg/nip44), which wraps the 32-byte
// CEK itself to a member. Do not conflate them.
package sync

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/3dl-dev/ready/pkg/nostr"
	"golang.org/x/crypto/chacha20poly1305"
)

// placeholderText is what the projection renders in place of confidential free
// text when the reader cannot decrypt it (no grant, an epoch minted before the
// grant, or an AEAD failure). It is a fixed, safe placeholder — the read path
// NEVER surfaces raw ciphertext/base64, never panics, never exits non-zero.
const placeholderText = "[encrypted]"

// BoardDecryptor supplies per-board content-encryption keys to the projection so
// a granted member's rd list/show renders confidential free text. Nil (the
// default), or a miss for a card's epoch, or an AEAD failure, makes the free-text
// fields render as placeholderText while every clear routing field still renders.
// keydist (ready-a8a) provides the production implementation by unwrapping the
// owner-signed role grant; the read path (ready-ce2) only CONSUMES it.
type BoardDecryptor interface {
	// CEK returns the content-encryption key for an event whose board coordinate
	// is boardCoord and whose cek_epoch marker is epoch, and ok=false if the
	// reader holds no such key.
	CEK(boardCoord string, epoch int) (cek [32]byte, ok bool)
}

// boardCoordOf returns the event's 30301 board-membership coordinate. A card
// carries it as its (only) "a" tag; a NIP-34 status event carries the board
// coordinate as one of several "a" tags (the first "a" is the 30302 card
// coordinate), so this scans for the "a" tag with the board-kind prefix — one
// lookup that works for both event shapes.
func boardCoordOf(e *nostr.Event) string {
	prefix := fmt.Sprintf("%d:", KindBoard)
	for _, tg := range e.Tags {
		if len(tg) >= 2 && tg[0] == "a" && strings.HasPrefix(tg[1], prefix) {
			return tg[1]
		}
	}
	return ""
}

// isConfidential reports whether e carries an enc envelope marker at all (any
// version). Used to decide placeholder-vs-plaintext on the read path and to
// version-dispatch on the fold gate.
func isConfidential(e *nostr.Event) bool {
	return tagValue(e, tagEnc) != ""
}

// cekFor resolves the CEK for a confidential event via the decryptor, returning
// ok=false unless the envelope is a KNOWN version, the cek_epoch parses, and the
// decryptor holds the key. An unknown enc version or malformed epoch yields
// ok=false (fail-closed to placeholder on read).
func cekFor(e *nostr.Event, dec BoardDecryptor) (cek [32]byte, ok bool) {
	if dec == nil || tagValue(e, tagEnc) != encVersion {
		return cek, false
	}
	epoch, err := strconv.Atoi(tagValue(e, tagCEKEpoch))
	if err != nil {
		return cek, false
	}
	return dec.CEK(boardCoordOf(e), epoch)
}

// decryptCardPayload returns the decrypted free-text blob for a confidential
// card, or ok=false when it cannot be decrypted (fail-closed to placeholder).
func decryptCardPayload(e *nostr.Event, dec BoardDecryptor) (cardPayload, bool) {
	var pl cardPayload
	cek, ok := cekFor(e, dec)
	if !ok {
		return pl, false
	}
	raw, err := openContent(cek, e.Content)
	if err != nil {
		return pl, false
	}
	if err := json.Unmarshal(raw, &pl); err != nil {
		return pl, false
	}
	return pl, true
}

// decryptStatusReason returns the decrypted close/change reason for a
// confidential status event, or ok=false when it cannot be decrypted.
func decryptStatusReason(e *nostr.Event, dec BoardDecryptor) (string, bool) {
	cek, ok := cekFor(e, dec)
	if !ok {
		return "", false
	}
	raw, err := openContent(cek, e.Content)
	if err != nil {
		return "", false
	}
	var pl statusPayload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return "", false
	}
	return pl.Reason, true
}

const (
	// encVersion is the current envelope-version discriminator carried in the
	// clear ["enc","1"] marker tag. A future format bumps this; readers and the
	// fold gate version-dispatch on it and never guess.
	encVersion = "1"
	// tagEnc / tagCEKEpoch are the two (and only) new always-clear marker tags a
	// confidential card/status event carries.
	tagEnc      = "enc"
	tagCEKEpoch = "cek_epoch"
)

// Envelope is the INJECTED per-board sealing material the write path needs. The
// write item (ready-e63) takes it as an injected parameter so it is testable
// before keydist (ready-a8a) exists; keydist later supplies it by unwrapping the
// owner-signed role grant. A nil *Envelope means plaintext mode — the exact
// pre-existing code path, zero structural drift.
type Envelope struct {
	// CEK is the per-board 32-byte current-epoch content-encryption key (random,
	// crypto/rand, NEVER content-derived).
	CEK [32]byte
	// Epoch is the integer id of the CEK epoch that sealed this Content; emitted
	// clear as ["cek_epoch","<Epoch>"].
	Epoch int
	// LTK, when non-nil, is the per-board Label Token Key (stable across CEK
	// epochs) that HMAC-tokenizes the clear l tag (ruling (b), ready-c83). Nil
	// leaves labels as plaintext l tags (free-text-only envelope) — used by the
	// write item's own tests before keydist distributes the LTK.
	LTK *[32]byte
}

// cardPayload is the plaintext JSON blob sealed into a confidential card's
// Content. Write and read MUST agree byte-for-byte, so both use this struct.
type cardPayload struct {
	Title     string   `json:"title"`
	Context   string   `json:"context,omitempty"`
	WaitingOn string   `json:"waiting_on,omitempty"`
	Labels    []string `json:"labels,omitempty"`
}

// statusPayload is the plaintext JSON blob sealed into a confidential status
// event's Content.
type statusPayload struct {
	Reason string `json:"reason"`
}

// sealContent encrypts plaintext under the per-board CEK and returns the
// canonical base64( nonce(12) ‖ ChaCha20-Poly1305 ) wire string (spec §3). A
// fresh 12-byte crypto/rand nonce is prepended per call.
func sealContent(cek [32]byte, plaintext []byte) (string, error) {
	aead, err := chacha20poly1305.New(cek[:])
	if err != nil {
		return "", fmt.Errorf("sync: envelope: init aead: %w", err)
	}
	nonce := make([]byte, chacha20poly1305.NonceSize) // 12
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("sync: envelope: read nonce: %w", err)
	}
	ct := aead.Seal(nil, nonce, plaintext, nil)
	out := make([]byte, 0, len(nonce)+len(ct))
	out = append(out, nonce...)
	out = append(out, ct...)
	return base64.StdEncoding.EncodeToString(out), nil
}

// openContent reverses sealContent: base64-decode, split nonce‖ciphertext,
// ChaCha20-Poly1305 Open under the CEK. A wrong CEK, truncated payload, or
// tampered ciphertext returns an error (never a panic) — the read path
// fail-closes to a placeholder on any error.
func openContent(cek [32]byte, payload string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(payload)
	if err != nil {
		return nil, fmt.Errorf("sync: envelope: base64 decode: %w", err)
	}
	if len(raw) < chacha20poly1305.NonceSize+chacha20poly1305.Overhead {
		return nil, errors.New("sync: envelope: ciphertext too short")
	}
	nonce := raw[:chacha20poly1305.NonceSize]
	ct := raw[chacha20poly1305.NonceSize:]
	aead, err := chacha20poly1305.New(cek[:])
	if err != nil {
		return nil, fmt.Errorf("sync: envelope: init aead: %w", err)
	}
	pt, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return nil, fmt.Errorf("sync: envelope: aead open: %w", err)
	}
	return pt, nil
}

// labelToken returns the confidential-board tag value for a label:
// lowercaseHex( HMAC-SHA256(LTK, label) ) (spec §7). Equality-preserving (same
// label + same LTK ⇒ same token) so the relay does exact-match #l filtering
// without seeing plaintext; a different board's LTK yields a different token.
func labelToken(ltk [32]byte, label string) string {
	m := hmac.New(sha256.New, ltk[:])
	m.Write([]byte(label))
	return hex.EncodeToString(m.Sum(nil))
}

// sealCardPayload marshals + seals the card free-text blob (title, context,
// waiting_on, and — for member-side rendering under tokenization — the plaintext
// labels) into the Content wire string.
func sealCardPayload(env *Envelope, spec CardSpec) (string, error) {
	pl := cardPayload{
		Title:     spec.Title,
		Context:   spec.Context,
		WaitingOn: spec.WaitingOn,
		Labels:    spec.Labels,
	}
	raw, err := json.Marshal(pl)
	if err != nil {
		return "", fmt.Errorf("sync: envelope: marshal card payload: %w", err)
	}
	return sealContent(env.CEK, raw)
}

// sealStatusPayload marshals + seals the status close/change reason.
func sealStatusPayload(env *Envelope, reason string) (string, error) {
	raw, err := json.Marshal(statusPayload{Reason: reason})
	if err != nil {
		return "", fmt.Errorf("sync: envelope: marshal status payload: %w", err)
	}
	return sealContent(env.CEK, raw)
}

// encMarkerTags returns the two always-clear marker tags for a confidential
// event sealed under env: ["enc","1"] and ["cek_epoch","<epoch>"].
func encMarkerTags(env *Envelope) [][]string {
	return [][]string{
		{tagEnc, encVersion},
		{tagCEKEpoch, fmt.Sprintf("%d", env.Epoch)},
	}
}
