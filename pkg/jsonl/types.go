// Package jsonl provides an append-only JSONL mutation store for rd operations.
// Every rd command that sends a convention message appends a MutationRecord to
// <project-root>/.ready/mutations.jsonl.
//
// The record format mirrors msgrec.MessageRecord fields but is self-contained —
// no campfire dependency — making the file portable and human-readable.
package jsonl

import (
	"encoding/json"

	"github.com/campfire-net/ready/pkg/msgrec"
)

// WorkTagPrefix is the tag namespace for all work management convention operations.
// Every convention operation tag is formed as WorkTagPrefix + operation name
// (e.g. "work:create", "work:close", "work:gate-resolve").
const WorkTagPrefix = "work:"

// MutationRecord is a single operation appended to mutations.jsonl.
// Fields mirror msgrec.MessageRecord to allow direct conversion for migration.
type MutationRecord struct {
	// MsgID is the campfire message ID (from the sent message).
	MsgID string `json:"msg_id"`
	// CampfireID is the project campfire this message was sent to.
	CampfireID string `json:"campfire_id"`
	// Timestamp is nanoseconds since Unix epoch (matches msgrec.MessageRecord.Timestamp).
	Timestamp int64 `json:"timestamp"`
	// Operation is the work convention tag (e.g. "work:create", "work:close").
	Operation string `json:"operation"`
	// Payload is the raw JSON payload of the convention message.
	Payload json.RawMessage `json:"payload"`
	// Tags is the full tag list from the sent message.
	Tags []string `json:"tags"`
	// Sender is the identity public key hex of the sender.
	Sender string `json:"sender"`
	// Antecedents is the list of message IDs this message replies to.
	Antecedents []string `json:"antecedents,omitempty"`
}

// ToMessageRecord converts a MutationRecord back to a msgrec.MessageRecord.
// The Signature, Provenance, and ReceivedAt fields are not stored in the
// MutationRecord and will be zero-valued in the result.
func (r MutationRecord) ToMessageRecord() msgrec.MessageRecord {
	return msgrec.MessageRecord{
		ID:          r.MsgID,
		CampfireID:  r.CampfireID,
		Timestamp:   r.Timestamp,
		Payload:     []byte(r.Payload),
		Tags:        r.Tags,
		Sender:      r.Sender,
		Antecedents: r.Antecedents,
		ReceivedAt:  r.Timestamp, // best effort
	}
}
