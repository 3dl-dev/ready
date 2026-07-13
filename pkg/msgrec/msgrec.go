// Package msgrec defines the native, campfire-free message record type that the
// work-item derivation engine replays.
//
// Historically the derivation engine consumed campfire's
// cf-protocol/store.MessageRecord directly, which coupled the read/replay path
// to the campfire SDK. As part of the nostr-native cutover (ready-cb6) the
// engine replays this self-contained record instead. It mirrors the subset of
// fields the convention replay actually needs — no campfire dependency, no
// SQLite backend, no encryption/provenance machinery.
//
// Records reach the engine from two sources:
//   - the local append-only mutation log (.ready/mutations.jsonl), and
//   - the nostr projection / inbound sync.
//
// Both convert into MessageRecord at their boundary. The engine itself is
// storage-agnostic.
package msgrec

// MessageRecord is a single replayable convention message.
//
// The field set is deliberately the subset the derivation engine and the
// mutation log need. Timestamp and ReceivedAt are nanoseconds since the Unix
// epoch; convention replay orders strictly by Timestamp.
type MessageRecord struct {
	// ID is the message identifier (nostr event id, or a synthetic id for
	// locally-buffered mutations).
	ID string `json:"id"`
	// CampfireID is the project/board identifier the message belongs to.
	// (Retained under the historical name; it is the board scope key.)
	CampfireID string `json:"campfire_id"`
	// Sender is the signer's public key hex (secp256k1 in the nostr-native path).
	Sender string `json:"sender"`
	// Payload is the raw JSON payload of the convention message.
	Payload []byte `json:"payload"`
	// Tags is the full tag list from the message (carries the work: operation tag).
	Tags []string `json:"tags"`
	// Antecedents is the list of message IDs this message causally follows.
	Antecedents []string `json:"antecedents"`
	// Timestamp is nanoseconds since the Unix epoch; replay orders by this.
	Timestamp int64 `json:"timestamp"`
	// Signature is the raw message signature, when present. The derivation
	// engine does not verify it (signature verification happens at ingest);
	// it is retained for lossless conversion from upstream records.
	Signature []byte `json:"signature,omitempty"`
	// ReceivedAt is nanoseconds since the Unix epoch at local receipt; used as
	// a stable tiebreaker where present.
	ReceivedAt int64 `json:"received_at"`
}
