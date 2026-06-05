// Package state derives work item state from a campfire message log.
// The campfire is the backend — state is derived by replaying convention
// messages (work:create, work:status, work:claim, work:close, etc.) in
// timestamp order.
package state

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/declarations"
)

// Status values as defined in the convention spec §4.4.
const (
	StatusInbox     = "inbox"
	StatusActive    = "active"
	StatusScheduled = "scheduled"
	StatusWaiting   = "waiting"
	StatusBlocked   = "blocked"
	StatusDone      = "done"
	StatusCancelled = "cancelled"
	StatusFailed    = "failed"
)

// LabelDef is a single label atom in the per-campfire registry.
// Seed atoms have DefinedBy == "seed" and DefinedAt == 0.
// User-defined atoms carry the defining operator's pubkey and message timestamp.
type LabelDef struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	DefinedBy   string `json:"defined_by"`  // "seed" or pubkey hex
	DefinedAt   int64  `json:"defined_at"`  // unix nanos; 0 for seed atoms
}

// DeriveResult holds the full derived state from a campfire message log:
// the work item map and the label registry.
type DeriveResult struct {
	items    map[string]*Item
	registry map[string]LabelDef
}

// Items returns the derived work item map (item ID → *Item).
func (r *DeriveResult) Items() map[string]*Item {
	return r.items
}

// LabelRegistry returns the derived label registry (label name → LabelDef).
// The registry includes seed atoms plus any user-defined atoms from work:label-define messages.
func (r *DeriveResult) LabelRegistry() map[string]LabelDef {
	return r.registry
}

// TerminalStatuses is the set of statuses where an item is no longer active.
var TerminalStatuses = map[string]bool{
	StatusDone:      true,
	StatusCancelled: true,
	StatusFailed:    true,
}

// Item is the derived state of a work item. All fields are derived from
// campfire messages; the campfire message log is the source of truth.
type Item struct {
	// ID is the work item ID (project-prefixed, e.g. "ready-a1b").
	ID string `json:"id"`
	// MsgID is the campfire message ID of the work:create message.
	MsgID string `json:"msg_id"`
	// CampfireID is the campfire this item lives in.
	CampfireID string `json:"campfire_id"`

	Title       string `json:"title"`
	Context     string `json:"context,omitempty"`
	Description string `json:"description,omitempty"` // alias for context, for bd compatibility
	Type        string `json:"type"`
	Level   string `json:"level,omitempty"`
	Project string `json:"project,omitempty"`
	For     string `json:"for"`
	By      string `json:"by,omitempty"`

	Priority string `json:"priority"`
	Status   string `json:"status"`
	ETA      string `json:"eta,omitempty"`
	Due      string `json:"due,omitempty"`

	ParentID  string   `json:"parent_id,omitempty"`
	BlockedBy []string `json:"blocked_by,omitempty"`
	Blocks    []string `json:"blocks,omitempty"`

	// Gate is set when the item requires human escalation. Values: budget, design, scope, review, human, stall.
	Gate string `json:"gate,omitempty"`

	// WaitingOn / WaitingType / WaitingSince are set when status=waiting.
	WaitingOn    string `json:"waiting_on,omitempty"`
	WaitingType  string `json:"waiting_type,omitempty"`
	WaitingSince string `json:"waiting_since,omitempty"`

	// GateMsgID is the campfire message ID of the most recent unfulfilled
	// work:gate message. Cleared when the gate is resolved.
	GateMsgID string `json:"gate_msg_id,omitempty"`

	// CreatedAt is the timestamp of the work:create message (unix nanos).
	CreatedAt int64 `json:"created_at"`
	// UpdatedAt is the timestamp of the most recent state-changing message.
	UpdatedAt int64 `json:"updated_at"`

	// History is the audit trail of status-changing events, in chronological order.
	// Populated from work:create, work:status, work:claim, work:close messages,
	// and from ImportHistory entries embedded in work:update messages.
	History []HistoryEntry `json:"history,omitempty"`

	// CrossCampfireWarnings lists advisory messages about cross-campfire dependencies
	// that could not be resolved (e.g., not a member of the target campfire, network
	// error). Cross-campfire deps are always NON-BLOCKING — the item stays actionable.
	CrossCampfireWarnings []string `json:"cross_campfire_warnings,omitempty"`
}

// HistoryEntry is a single audit trail entry for a work item.
type HistoryEntry struct {
	// Timestamp is ISO8601 UTC — either the original event time (for imported
	// history) or the campfire message time.
	Timestamp string `json:"timestamp"`
	// FromStatus is the status before this change.
	FromStatus string `json:"from_status"`
	// ToStatus is the status after this change.
	ToStatus string `json:"to_status"`
	// ChangedBy is the actor (email, pubkey hex, or "system").
	ChangedBy string `json:"changed_by"`
	// Note is an optional human-readable description of the change.
	Note string `json:"note,omitempty"`
}

// createPayload mirrors the fields in a work:create message payload.
type createPayload struct {
	ID       string `json:"id"`
	Title    string `json:"title"`
	Context  string `json:"context"`
	Type     string `json:"type"`
	Level    string `json:"level"`
	Project  string `json:"project"`
	For      string `json:"for"`
	By       string `json:"by"`
	Priority string `json:"priority"`
	ParentID string `json:"parent_id"`
	ETA      string `json:"eta"`
	Due      string `json:"due"`
	Gate     string `json:"gate"`
}

// statusPayload mirrors the fields in a work:status message payload.
type statusPayload struct {
	Target      string `json:"target"`
	To          string `json:"to"`
	Reason      string `json:"reason"`
	WaitingOn   string `json:"waiting_on"`
	WaitingType string `json:"waiting_type"`
}

// claimPayload mirrors the fields in a work:claim message payload.
type claimPayload struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// delegatePayload mirrors the fields in a work:delegate message payload.
type delegatePayload struct {
	Target string `json:"target"`
	To     string `json:"to"`
	From   string `json:"from"`
	Reason string `json:"reason"`
}

// closePayload mirrors the fields in a work:close message payload.
type closePayload struct {
	Target     string `json:"target"`
	Resolution string `json:"resolution"`
	Reason     string `json:"reason"`
}

// updatePayload mirrors the fields in a work:update message payload.
type updatePayload struct {
	Target   string `json:"target"`
	Title    string `json:"title,omitempty"`
	Context  string `json:"context,omitempty"`
	Priority string `json:"priority,omitempty"`
	ETA      string `json:"eta,omitempty"`
	Due      string `json:"due,omitempty"`
	Level    string `json:"level,omitempty"`
	For      string `json:"for,omitempty"`
	By       string `json:"by,omitempty"`
	Gate     string `json:"gate,omitempty"`
	// ImportHistory carries historical audit entries replayed during migration.
	// Each entry is appended to the item's History slice. Original actor and
	// timestamp are preserved in the entry; the campfire message timestamp
	// reflects import time (SendRequest has no Timestamp field in cf 0.14 —
	// see rd item rudi-trl).
	ImportHistory []HistoryEntry `json:"import_history,omitempty"`
}

// blockPayload mirrors the fields in a work:block message payload.
type blockPayload struct {
	BlockerID  string `json:"blocker_id"`
	BlockedID  string `json:"blocked_id"`
	BlockerMsg string `json:"blocker_msg"`
	BlockedMsg string `json:"blocked_msg"`
}

// unblockPayload mirrors the fields in a work:unblock message payload.
type unblockPayload struct {
	Target string `json:"target"`
	Reason string `json:"reason"`
}

// gatePayload mirrors the fields in a work:gate message payload.
type gatePayload struct {
	Target      string `json:"target"`
	GateType    string `json:"gate_type"`
	Description string `json:"description,omitempty"`
}

// gateResolvePayload mirrors the fields in a work:gate-resolve message payload.
type gateResolvePayload struct {
	Target     string `json:"target"`
	Resolution string `json:"resolution"`
	Reason     string `json:"reason,omitempty"`
}

// serverBindingPayload mirrors the fields in a convention:server-binding message payload.
// ValidFrom and ValidUntil are Unix timestamps in seconds (int64), matching the
// JSON numbers written by conventionserver/server.go.
type serverBindingPayload struct {
	Convention   string `json:"convention"`
	Operation    string `json:"operation"`
	ServerPubkey string `json:"server_pubkey"`
	ValidFrom    int64  `json:"valid_from"`
	ValidUntil   int64  `json:"valid_until,omitempty"`
}

// capabilityTokenPayload represents an offline capability token embedded in an operation.
type capabilityTokenPayload struct {
	Subject      string   `json:"subject"`
	Campfire     string   `json:"campfire"`
	Role         string   `json:"role"`
	Operations   []string `json:"operations"`
	IssuedAt     int64    `json:"issued_at"`
	ExpiresAt    int64    `json:"expires_at"`
	BindingMsgID string   `json:"binding_msg_id,omitempty"`
	Signature    string   `json:"signature"`
}

// hasTag reports whether tags contains the given tag.
func hasTag(tags []string, tag string) bool {
	for _, t := range tags {
		if t == tag {
			return true
		}
	}
	return false
}

// etaFromPriority derives the default ETA from priority if none was specified.
// P0=now, P1=+4h, P2=+24h, P3=+72h.
func etaFromPriority(priority string, now time.Time) string {
	switch priority {
	case "p0":
		return now.UTC().Format(time.RFC3339)
	case "p1":
		return now.Add(4 * time.Hour).UTC().Format(time.RFC3339)
	case "p2":
		return now.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	case "p3":
		return now.Add(72 * time.Hour).UTC().Format(time.RFC3339)
	default:
		return now.Add(24 * time.Hour).UTC().Format(time.RFC3339)
	}
}

// resolveItemID finds an item ID by looking up the target in msgIndex,
// falling back to antecedents if target is empty or not found.
func resolveItemID(msgIndex map[string]string, target string, antecedents []string) string {
	if target != "" {
		if id := msgIndex[target]; id != "" {
			return id
		}
	}
	for _, ant := range antecedents {
		if id := msgIndex[ant]; id != "" {
			return id
		}
	}
	return ""
}

// serverBinding represents an active server-binding declaration for a (convention, operation) pair.
type serverBinding struct {
	serverPubkey string
	validFrom    int64 // nanoseconds (unix timestamp converted)
	validUntil   int64 // nanoseconds, 0 means no expiration
	msgID        string
}

// roleInfo represents a member's role state from work:role-grant messages.
type roleInfo struct {
	role      string
	grantedAt int64 // nanoseconds
	expiresAt int64 // nanoseconds, 0 means no expiration
	msgID     string
}

// parseTimestamp converts an RFC3339 or unix timestamp string to int64 nanoseconds.
// Returns 0 if parsing fails.
func parseTimestamp(ts string) int64 {
	// Try RFC3339 first
	if t, err := time.Parse(time.RFC3339, ts); err == nil {
		return t.UnixNano()
	}
	// Try unix seconds
	if unixSec, err := strconv.ParseInt(ts, 10, 64); err == nil {
		return unixSec * 1e9
	}
	return 0
}

// parseTimestampValue converts a timestamp value (int, float64, or string) to int64 nanoseconds.
// This handles both old int64 nanosecond values and new RFC3339 string values.
// Returns 0 if the value is nil or parsing fails.
func parseTimestampValue(v interface{}) int64 {
	if v == nil {
		return 0
	}
	switch val := v.(type) {
	case float64:
		// JSON unmarshals numbers as float64; treat as nanoseconds
		return int64(val)
	case int64:
		return val
	case string:
		return parseTimestamp(val)
	default:
		return 0
	}
}

// findActiveServerBinding finds the most recent server-binding declaration
// that is valid at the given timestamp for the specified convention and operation.
func findActiveServerBinding(msgs []store.MessageRecord, convention, operation string, atTime int64) *serverBinding {
	var active *serverBinding
	for _, m := range msgs {
		if !hasTag(m.Tags, "convention:server-binding") {
			continue
		}
		var p serverBindingPayload
		if err := json.Unmarshal(m.Payload, &p); err != nil {
			continue
		}
		if p.Convention != convention || p.Operation != operation {
			continue
		}
		validFrom := p.ValidFrom * int64(time.Second)
		if validFrom == 0 || validFrom > atTime {
			// Binding not yet valid at this message's timestamp
			continue
		}
		validUntil := p.ValidUntil * int64(time.Second)
		if validUntil != 0 && validUntil < atTime {
			// Binding has expired
			continue
		}
		// Keep the most recent valid binding
		if active == nil || validFrom > active.validFrom {
			active = &serverBinding{
				serverPubkey: p.ServerPubkey,
				validFrom:    validFrom,
				validUntil:   validUntil,
				msgID:        m.ID,
			}
		}
	}
	return active
}

// findFirstServerBinding finds the earliest server-binding declaration for the
// specified convention and operation, without filtering by timestamp. Returns the
// binding with the smallest non-zero validFrom, or nil if none exists.
func findFirstServerBinding(msgs []store.MessageRecord, convention, operation string) *serverBinding {
	var first *serverBinding
	for _, m := range msgs {
		if !hasTag(m.Tags, "convention:server-binding") {
			continue
		}
		var p serverBindingPayload
		if err := json.Unmarshal(m.Payload, &p); err != nil {
			continue
		}
		if p.Convention != convention || p.Operation != operation {
			continue
		}
		validFrom := p.ValidFrom * int64(time.Second)
		if validFrom == 0 {
			// No valid_from — skip; can't determine pre-binding boundary.
			continue
		}
		if first == nil || validFrom < first.validFrom {
			validUntil := p.ValidUntil * int64(time.Second)
			first = &serverBinding{
				serverPubkey: p.ServerPubkey,
				validFrom:    validFrom,
				validUntil:   validUntil,
				msgID:        m.ID,
			}
		}
	}
	return first
}

// findFulfillmentForOperation finds a fulfillment message that matches the given
// target message, sent by the specified sender pubkey.
func findFulfillmentForOperation(msgs []store.MessageRecord, targetMsgID string, senderPubkey string) *store.MessageRecord {
	for i, m := range msgs {
		if hasTag(m.Tags, "fulfills") && m.Sender == senderPubkey {
			// Check if this fulfillment targets the operation
			// Fulfillment messages have the target in their Antecedents
			for _, ant := range m.Antecedents {
				if ant == targetMsgID {
					return &msgs[i]
				}
			}
		}
	}
	return nil
}

// isOperationAuthorized checks if a consequential operation is authorized under
// the server-binding gating rules.
//
// Rules:
// - If no server-binding exists (bypass mode): accept all (Wave 1 behavior)
// - If binding exists and operation has a valid fulfillment from the bound server: accept
// - If binding exists and operation has a valid capability token: accept
// - If binding exists and operation was issued before the binding's valid_from: accept (pre-binding)
// - Otherwise: reject (drop from derived state)
//
// roleMap is reserved for Wave 3+ role-based authorization (currently unused here).
func isOperationAuthorized(msgs []store.MessageRecord, op store.MessageRecord, convention, operation string, _ map[string]roleInfo) bool {
	// Find the earliest server-binding ever declared for this operation.
	firstBinding := findFirstServerBinding(msgs, convention, operation)

	// Bypass mode: no binding has ever been declared for this operation (Wave 1 behavior).
	if firstBinding == nil {
		return true
	}

	// 1. Pre-binding: operation was issued before the first binding became valid.
	// These operations were submitted when no authorization requirement existed yet.
	if op.Timestamp < firstBinding.validFrom {
		return true
	}

	// Find the active server-binding at the time of this operation.
	binding := findActiveServerBinding(msgs, convention, operation, op.Timestamp)
	if binding == nil {
		// A binding has been declared but is not active at this timestamp
		// (e.g. op falls in a gap or after expiry). Reject.
		return false
	}

	// 2. Check for a fulfillment message from the bound server.
	if findFulfillmentForOperation(msgs, op.ID, binding.serverPubkey) != nil {
		return true
	}

	// 3. Check for a valid capability token in the operation payload.
	if hasValidCapabilityToken(&op, binding.serverPubkey) {
		return true
	}

	// No authorization path succeeded.
	return false
}

// hasValidCapabilityToken checks if an operation message contains a valid
// capability token signed by the specified server pubkey.
//
// For now, this is a stub that returns false. Full implementation requires
// cryptographic signature verification (Ed25519), which is deferred to Wave 2+
// as part of the capability token verification framework.
func hasValidCapabilityToken(op *store.MessageRecord, serverPubkey string) bool {
	// Stub implementation. Full token verification requires:
	// 1. Extract token from operation payload
	// 2. Verify signature against serverPubkey
	// 3. Check expires_at > now
	// 4. Check operation in token's operations list
	// 5. Check subject matches sender
	//
	// For now, always return false to avoid false positives.
	return false
}

// replayState holds mutable intermediate state used during Pass 2 of Derive.
type replayState struct {
	items    map[string]*Item
	msgIndex map[string]string // campfire message ID → item ID

	// blockEdges tracks blocker→blocked relationships.
	// When a blocker item closes, its entries are removed.
	blockEdges    []blockEdge
	blockMsgIndex map[string]blockEdgeKey // work:block message ID → {blockerID, blockedID}

	// gateMsgIndex maps a gate message ID → item ID, used by work:gate-resolve.
	gateMsgIndex map[string]string
}

// blockEdge records a single blocker→blocked dependency.
type blockEdge struct {
	blockerID  string
	blockedID  string
	blockMsgID string // campfire message ID of the work:block message
}

// blockEdgeKey is used as the value type in replayState.blockMsgIndex.
type blockEdgeKey struct {
	blockerID string
	blockedID string
}

// Derive replays all work convention messages from the provided campfire message
// records and returns the derived item states keyed by item ID. Messages must
// be from a single campfire and are processed in timestamp order (the store
// returns them in ascending timestamp order by default).
//
// Three-pass approach:
// Pass 1: Build the role map from work:role-grant messages. Latest grant per pubkey wins.
//         Also build the label registry (seed + work:label-define messages).
// Pass 2: Replay operations, with fulfillment gating for consequential ops (close, delegate, gate-resolve).
// Pass 3: Stranded-item reclaim — flip active items owned by revoked members back to inbox.
//
// Deprecated: use DeriveAll to also get the label registry.
func Derive(campfireID string, msgs []store.MessageRecord) map[string]*Item {
	return DeriveAll(campfireID, msgs).items
}

// DeriveAll replays all work convention messages and returns a DeriveResult
// containing both the item map and the label registry.
func DeriveAll(campfireID string, msgs []store.MessageRecord) *DeriveResult {
	roleMap := buildRoleMap(msgs)
	registry := buildLabelRegistry(msgs)

	rs := &replayState{
		items:         make(map[string]*Item),
		msgIndex:      make(map[string]string),
		blockMsgIndex: make(map[string]blockEdgeKey),
		gateMsgIndex:  make(map[string]string),
	}

	for _, m := range msgs {
		switch {
		case hasTag(m.Tags, "work:create"):
			handleWorkCreate(campfireID, m, rs)
		case hasTag(m.Tags, "work:status"):
			handleWorkStatus(m, rs)
		case hasTag(m.Tags, "work:claim"):
			handleWorkClaim(m, rs)
		case hasTag(m.Tags, "work:delegate"):
			handleWorkDelegate(msgs, m, rs, roleMap)
		case hasTag(m.Tags, "work:close"):
			handleWorkClose(msgs, m, rs, roleMap)
		case hasTag(m.Tags, "work:update"):
			handleWorkUpdate(m, rs)
		case hasTag(m.Tags, "work:block"):
			handleWorkBlock(m, rs)
		case hasTag(m.Tags, "work:unblock"):
			handleWorkUnblock(m, rs)
		case hasTag(m.Tags, "work:gate"):
			handleWorkGate(m, rs)
		case hasTag(m.Tags, "work:gate-resolve"):
			handleWorkGateResolve(msgs, m, rs, roleMap)
		}
	}

	applyBlockStatus(rs)
	applyStrandedItemReclaim(rs.items, roleMap)

	return &DeriveResult{
		items:    rs.items,
		registry: registry,
	}
}

// labelDefinePayload mirrors the fields in a work:label-define message payload.
type labelDefinePayload struct {
	Label       string `json:"label"`
	Description string `json:"description"`
}

// buildLabelRegistry scans msgs for work:label-define messages and constructs
// the label registry map. Seed atoms (from declarations.LoadSeedLabels) are
// always included. A later label-define for an existing name overwrites the entry
// (demand-then-promote flow: retroactive legitimization is intentional).
// Registry membership is timestamp-independent — all messages are scanned
// in a single pass regardless of order.
func buildLabelRegistry(msgs []store.MessageRecord) map[string]LabelDef {
	registry := make(map[string]LabelDef)

	// Populate seed atoms first.
	seedLabels, _ := declarations.LoadSeedLabels()
	for _, sl := range seedLabels {
		registry[sl.Name] = LabelDef{
			Name:        sl.Name,
			Description: sl.Description,
			DefinedBy:   "seed",
			DefinedAt:   0,
		}
	}

	// Overlay user-defined atoms from work:label-define messages.
	// Later timestamps win for the same label name.
	for _, m := range msgs {
		if !hasTag(m.Tags, "work:label-define") {
			continue
		}
		var p labelDefinePayload
		if err := json.Unmarshal(m.Payload, &p); err != nil {
			continue
		}
		if p.Label == "" {
			continue
		}
		existing, ok := registry[p.Label]
		// Seed atoms and entries with a later timestamp win over earlier ones.
		if !ok || existing.DefinedAt == 0 || m.Timestamp >= existing.DefinedAt {
			registry[p.Label] = LabelDef{
				Name:        p.Label,
				Description: p.Description,
				DefinedBy:   m.Sender,
				DefinedAt:   m.Timestamp,
			}
		}
	}

	return registry
}

// buildRoleMap scans msgs for work:role-grant messages and returns a map of
// pubkey → roleInfo. The most recent grant per pubkey wins.
func buildRoleMap(msgs []store.MessageRecord) map[string]roleInfo {
	roleMap := make(map[string]roleInfo)
	for _, m := range msgs {
		if !hasTag(m.Tags, "work:role-grant") {
			continue
		}
		var p struct {
			Pubkey    string      `json:"pubkey"`
			Role      string      `json:"role"`
			GrantedAt interface{} `json:"granted_at,omitempty"`
			ExpiresAt interface{} `json:"expires_at,omitempty"`
		}
		if err := json.Unmarshal(m.Payload, &p); err != nil {
			continue
		}
		if p.Pubkey == "" {
			continue
		}
		grantedAt := m.Timestamp
		if p.GrantedAt != nil {
			if parsed := parseTimestampValue(p.GrantedAt); parsed != 0 {
				grantedAt = parsed
			}
		}
		expiresAt := int64(0)
		if p.ExpiresAt != nil {
			if parsed := parseTimestampValue(p.ExpiresAt); parsed != 0 {
				expiresAt = parsed
			}
		}
		existing, ok := roleMap[p.Pubkey]
		if !ok || grantedAt >= existing.grantedAt {
			roleMap[p.Pubkey] = roleInfo{
				role:      p.Role,
				grantedAt: grantedAt,
				expiresAt: expiresAt,
				msgID:     m.ID,
			}
		}
	}
	return roleMap
}

// handleWorkCreate processes a work:create message and adds the new item to rs.
func handleWorkCreate(campfireID string, m store.MessageRecord, rs *replayState) {
	var p createPayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	if p.ID == "" {
		return
	}
	now := time.Unix(0, m.Timestamp)
	eta := p.ETA
	if eta == "" {
		eta = etaFromPriority(p.Priority, now)
	}
	item := &Item{
		ID:          p.ID,
		MsgID:       m.ID,
		CampfireID:  campfireID,
		Title:       p.Title,
		Context:     p.Context,
		Description: p.Context, // alias for bd compatibility
		Type:        p.Type,
		Level:       p.Level,
		Project:     p.Project,
		For:         p.For,
		By:          p.By,
		Priority:    p.Priority,
		Status:      StatusInbox,
		ETA:         eta,
		Due:         p.Due,
		ParentID:    p.ParentID,
		Gate:        p.Gate,
		CreatedAt:   m.Timestamp,
		UpdatedAt:   m.Timestamp,
	}
	item.History = append(item.History, HistoryEntry{
		Timestamp:  time.Unix(0, m.Timestamp).UTC().Format(time.RFC3339),
		FromStatus: StatusInbox,
		ToStatus:   StatusInbox,
		ChangedBy:  m.Sender,
		Note:       "created",
	})
	rs.items[p.ID] = item
	rs.msgIndex[m.ID] = p.ID
}

// handleWorkStatus processes a work:status message, updating item status and waiting fields.
func handleWorkStatus(m store.MessageRecord, rs *replayState) {
	var p statusPayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	itemID := resolveItemID(rs.msgIndex, p.Target, m.Antecedents)
	item, ok := rs.items[itemID]
	if !ok {
		return
	}
	prevStatus := item.Status
	item.Status = p.To
	item.UpdatedAt = m.Timestamp
	item.History = append(item.History, HistoryEntry{
		Timestamp:  time.Unix(0, m.Timestamp).UTC().Format(time.RFC3339),
		FromStatus: prevStatus,
		ToStatus:   p.To,
		ChangedBy:  m.Sender,
		Note:       p.Reason,
	})
	if p.To == StatusWaiting {
		item.WaitingOn = p.WaitingOn
		item.WaitingType = p.WaitingType
		item.WaitingSince = time.Unix(0, m.Timestamp).UTC().Format(time.RFC3339)
	} else {
		item.WaitingOn = ""
		item.WaitingType = ""
		item.WaitingSince = ""
	}
}

// handleWorkClaim processes a work:claim message, setting by=sender and transitioning to active.
func handleWorkClaim(m store.MessageRecord, rs *replayState) {
	var p claimPayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	itemID := resolveItemID(rs.msgIndex, p.Target, m.Antecedents)
	item, ok := rs.items[itemID]
	if !ok {
		return
	}
	prevStatus := item.Status
	item.By = m.Sender
	item.Status = StatusActive
	item.UpdatedAt = m.Timestamp
	item.History = append(item.History, HistoryEntry{
		Timestamp:  time.Unix(0, m.Timestamp).UTC().Format(time.RFC3339),
		FromStatus: prevStatus,
		ToStatus:   StatusActive,
		ChangedBy:  m.Sender,
	})
}

// handleWorkDelegate processes a work:delegate message (fulfillment-gated).
func handleWorkDelegate(msgs []store.MessageRecord, m store.MessageRecord, rs *replayState, roleMap map[string]roleInfo) {
	if !isOperationAuthorized(msgs, m, "work", "delegate", roleMap) {
		return
	}
	var p delegatePayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	itemID := resolveItemID(rs.msgIndex, p.Target, m.Antecedents)
	item, ok := rs.items[itemID]
	if !ok {
		return
	}
	item.By = p.To
	item.UpdatedAt = m.Timestamp
}

// handleWorkClose processes a work:close message (fulfillment-gated), transitioning to a
// terminal status and implicitly unblocking any items this item was blocking.
func handleWorkClose(msgs []store.MessageRecord, m store.MessageRecord, rs *replayState, roleMap map[string]roleInfo) {
	if !isOperationAuthorized(msgs, m, "work", "close", roleMap) {
		return
	}
	var p closePayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	itemID := resolveItemID(rs.msgIndex, p.Target, m.Antecedents)
	item, ok := rs.items[itemID]
	if !ok {
		return
	}
	prevStatus := item.Status
	switch p.Resolution {
	case "done":
		item.Status = StatusDone
	case "cancelled":
		item.Status = StatusCancelled
	case "failed":
		item.Status = StatusFailed
	default:
		item.Status = StatusDone
	}
	item.UpdatedAt = m.Timestamp
	item.History = append(item.History, HistoryEntry{
		Timestamp:  time.Unix(0, m.Timestamp).UTC().Format(time.RFC3339),
		FromStatus: prevStatus,
		ToStatus:   item.Status,
		ChangedBy:  m.Sender,
		Note:       p.Reason,
	})
	// Implicit unblock: remove all block edges where this item is the blocker.
	// Also clean up blockMsgIndex so stale entries don't linger.
	var newEdges []blockEdge
	for _, edge := range rs.blockEdges {
		if edge.blockerID != item.ID {
			newEdges = append(newEdges, edge)
		} else {
			delete(rs.blockMsgIndex, edge.blockMsgID)
		}
	}
	rs.blockEdges = newEdges
}

// handleWorkUpdate processes a work:update message, applying field-level updates.
func handleWorkUpdate(m store.MessageRecord, rs *replayState) {
	var p updatePayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	itemID := resolveItemID(rs.msgIndex, p.Target, m.Antecedents)
	item, ok := rs.items[itemID]
	if !ok {
		return
	}
	// Apply field updates. The sentinel "-" clears a field.
	if p.Title != "" {
		item.Title = clearOrSet(p.Title)
	}
	if p.Context != "" {
		item.Context = clearOrSet(p.Context)
		item.Description = item.Context // keep alias in sync
	}
	if p.Priority != "" {
		item.Priority = clearOrSet(p.Priority)
	}
	if p.ETA != "" {
		item.ETA = clearOrSet(p.ETA)
	}
	if p.Due != "" {
		item.Due = clearOrSet(p.Due)
	}
	if p.Level != "" {
		item.Level = clearOrSet(p.Level)
	}
	if p.For != "" {
		item.For = clearOrSet(p.For)
	}
	if p.By != "" {
		item.By = clearOrSet(p.By)
	}
	if p.Gate != "" {
		item.Gate = clearOrSet(p.Gate)
	}
	// ImportHistory: append original history entries from migration replay.
	if len(p.ImportHistory) > 0 {
		item.History = append(item.History, p.ImportHistory...)
	}
	item.UpdatedAt = m.Timestamp
}

// handleWorkBlock processes a work:block message, recording the blocker→blocked edge.
// Cross-campfire references are recorded as warnings and do not create blocking edges.
func handleWorkBlock(m store.MessageRecord, rs *replayState) {
	var p blockPayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	if p.BlockerID == "" || p.BlockedID == "" {
		return
	}
	blockerIsCross := IsCrossCampfireRef(p.BlockerID)
	blockedIsCross := IsCrossCampfireRef(p.BlockedID)
	if blockerIsCross || blockedIsCross {
		// Attach warning to the local item involved.
		localItemID := p.BlockedID
		if blockedIsCross {
			localItemID = p.BlockerID
		}
		if localItem, ok := rs.items[localItemID]; ok {
			crossRef := p.BlockerID
			if blockedIsCross {
				crossRef = p.BlockedID
			}
			localItem.CrossCampfireWarnings = appendUnique(
				localItem.CrossCampfireWarnings,
				fmt.Sprintf("unresolved cross-campfire dep: %s (not a member — non-blocking)", crossRef),
			)
		}
		return
	}
	rs.blockEdges = append(rs.blockEdges, blockEdge{
		blockerID:  p.BlockerID,
		blockedID:  p.BlockedID,
		blockMsgID: m.ID,
	})
	rs.blockMsgIndex[m.ID] = blockEdgeKey{p.BlockerID, p.BlockedID}
}

// handleWorkUnblock processes a work:unblock message, removing the referenced block edge.
func handleWorkUnblock(m store.MessageRecord, rs *replayState) {
	var p unblockPayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	targetMsg := p.Target
	if targetMsg == "" {
		for _, ant := range m.Antecedents {
			targetMsg = ant
			break
		}
	}
	if edge, ok := rs.blockMsgIndex[targetMsg]; ok {
		var newEdges []blockEdge
		for _, e := range rs.blockEdges {
			if e.blockerID != edge.blockerID || e.blockedID != edge.blockedID {
				newEdges = append(newEdges, e)
			}
		}
		rs.blockEdges = newEdges
		delete(rs.blockMsgIndex, targetMsg)
	}
}

// handleWorkGate processes a work:gate message, transitioning the item to waiting/gate.
func handleWorkGate(m store.MessageRecord, rs *replayState) {
	// work:gate implicitly transitions item to waiting with waiting_type=gate.
	// The gate message is always sent as --future in a full implementation.
	// TODO: when campfire transport supports --future, this should be sent
	// with --future and resolved via cf await. For now, we send normally.
	var p gatePayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	itemID := resolveItemID(rs.msgIndex, p.Target, m.Antecedents)
	item, ok := rs.items[itemID]
	if !ok {
		return
	}
	item.Status = StatusWaiting
	item.WaitingType = "gate"
	item.WaitingOn = p.Description
	item.WaitingSince = time.Unix(0, m.Timestamp).UTC().Format(time.RFC3339)
	item.GateMsgID = m.ID
	item.UpdatedAt = m.Timestamp
	rs.gateMsgIndex[m.ID] = itemID
}

// handleWorkGateResolve processes a work:gate-resolve message (fulfillment-gated).
// approved → transitions to active; rejected → item remains waiting.
func handleWorkGateResolve(msgs []store.MessageRecord, m store.MessageRecord, rs *replayState, roleMap map[string]roleInfo) {
	if !isOperationAuthorized(msgs, m, "work", "gate-resolve", roleMap) {
		return
	}
	var p gateResolvePayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	// Resolve via target (gate msg ID) or antecedents.
	gateMsgID := p.Target
	if gateMsgID == "" {
		for _, ant := range m.Antecedents {
			if _, ok := rs.gateMsgIndex[ant]; ok {
				gateMsgID = ant
				break
			}
		}
	}
	itemID := rs.gateMsgIndex[gateMsgID]
	if itemID == "" {
		return
	}
	item, ok := rs.items[itemID]
	if !ok {
		return
	}
	if p.Resolution == "approved" {
		item.Status = StatusActive
		item.WaitingOn = ""
		item.WaitingType = ""
		item.WaitingSince = ""
		item.GateMsgID = ""
	}
	// rejected: item remains waiting; gate stays open.
	// The by party should revise approach and re-gate or resume.
	item.UpdatedAt = m.Timestamp
}

// applyBlockStatus derives the blocked status for items by inspecting remaining
// block edges. An item is blocked if at least one of its blocker items is
// non-terminal. Only non-terminal items can become blocked.
func applyBlockStatus(rs *replayState) {
	for _, edge := range rs.blockEdges {
		blocker, blockerOK := rs.items[edge.blockerID]
		blocked, blockedOK := rs.items[edge.blockedID]
		if !blockerOK || !blockedOK {
			continue
		}
		if TerminalStatuses[blocked.Status] {
			continue
		}
		if !TerminalStatuses[blocker.Status] {
			blocked.Status = StatusBlocked
		}
		blocked.BlockedBy = appendUnique(blocked.BlockedBy, edge.blockerID)
		blocker.Blocks = appendUnique(blocker.Blocks, edge.blockedID)
	}
}

// applyStrandedItemReclaim implements §4.5: for each pubkey with role=revoked,
// flip any active items claimed by that pubkey back to inbox so other members
// can pick them up.
func applyStrandedItemReclaim(items map[string]*Item, roleMap map[string]roleInfo) {
	for pubkey, ri := range roleMap {
		if ri.role != "revoked" {
			continue
		}
		for _, item := range items {
			if item.Status == StatusActive && item.By == pubkey {
				item.Status = StatusInbox
				item.By = ""
			}
		}
	}
}

// DeriveFromStore loads all messages from the given campfire and derives item state.
func DeriveFromStore(s store.Store, campfireID string) (map[string]*Item, error) {
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{})
	if err != nil {
		return nil, err
	}
	return Derive(campfireID, msgs), nil
}

// DeriveAllFromStore loads all messages from the given campfire and returns a
// DeriveResult containing both the item map and the label registry.
func DeriveAllFromStore(s store.Store, campfireID string) (*DeriveResult, error) {
	msgs, err := s.ListMessages(campfireID, 0, store.MessageFilter{})
	if err != nil {
		return nil, err
	}
	return DeriveAll(campfireID, msgs), nil
}

// DeriveFromJSONL reads all MutationRecords from the given JSONL file path,
// converts them to store.MessageRecord, and derives item state by replaying
// the mutation log. The campfireID is inferred from the first record's
// CampfireID field; callers may pass an empty string to use that default,
// or pass an explicit value to override (useful in tests).
//
// Returns an empty map (not an error) when the file does not exist —
// a missing mutations.jsonl is valid for a freshly initialised project.
func DeriveFromJSONL(path string) (map[string]*Item, error) {
	return DeriveFromJSONLWithCampfire(path, "")
}

// DeriveFromJSONLWithCampfire is like DeriveFromJSONL but accepts an explicit
// campfireID override. When campfireID is empty, it is inferred from the first
// record's CampfireID field (falling back to an empty string).
func DeriveFromJSONLWithCampfire(path, campfireID string) (map[string]*Item, error) {
	records, err := readMutations(path)
	if err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return make(map[string]*Item), nil
	}
	// Infer campfireID from first record when not provided.
	if campfireID == "" {
		campfireID = records[0].CampfireID
	}
	// Convert MutationRecords → store.MessageRecord.
	msgs := make([]store.MessageRecord, len(records))
	for i, r := range records {
		msgs[i] = store.MessageRecord{
			ID:          r.MsgID,
			CampfireID:  r.CampfireID,
			Timestamp:   r.Timestamp,
			Payload:     []byte(r.Payload),
			Tags:        r.Tags,
			Sender:      r.Sender,
			Antecedents: r.Antecedents,
			ReceivedAt:  r.Timestamp,
		}
	}
	return Derive(campfireID, msgs), nil
}

// mutationRecord is a minimal local type that mirrors jsonl.MutationRecord.
// We define it here to avoid an import cycle (jsonl imports nothing from state,
// but state must not import jsonl either). The JSON field names match exactly.
type mutationRecord struct {
	MsgID       string          `json:"msg_id"`
	CampfireID  string          `json:"campfire_id"`
	Timestamp   int64           `json:"timestamp"`
	Operation   string          `json:"operation"`
	Payload     json.RawMessage `json:"payload"`
	Tags        []string        `json:"tags"`
	Sender      string          `json:"sender"`
	Antecedents []string        `json:"antecedents,omitempty"`
}

// readMutations opens the JSONL file at path and returns all valid records
// sorted by Timestamp ascending. Returns nil, nil when the file does not exist.
func readMutations(path string) ([]mutationRecord, error) {
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("state: open %s: %w", path, err)
	}
	defer f.Close()

	var records []mutationRecord
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 1024*1024), 10*1024*1024)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var r mutationRecord
		if err := json.Unmarshal(line, &r); err != nil {
			continue // skip malformed lines — resilience
		}
		records = append(records, r)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("state: read %s: %w", path, err)
	}
	// Sort by timestamp ascending (matches store behaviour).
	sort.Slice(records, func(i, j int) bool {
		return records[i].Timestamp < records[j].Timestamp
	})
	return records, nil
}

// ClearSentinel is the value that clears a field in work:update.
const ClearSentinel = "-"

// clearOrSet returns "" if val is the clear sentinel, otherwise returns val.
func clearOrSet(val string) string {
	if val == ClearSentinel {
		return ""
	}
	return val
}

// appendUnique appends val to slice only if not already present.
func appendUnique(slice []string, val string) []string {
	for _, v := range slice {
		if v == val {
			return slice
		}
	}
	return append(slice, val)
}

// IsBlocked reports whether item is currently blocked.
func IsBlocked(item *Item) bool {
	return item.Status == StatusBlocked
}

// IsTerminal reports whether item is in a terminal state.
func IsTerminal(item *Item) bool {
	return TerminalStatuses[item.Status]
}

// IsCrossCampfireRef reports whether ref looks like a cross-campfire item
// reference (e.g. "acme.frontend.item-abc"). A cross-campfire ref contains at
// least one dot and the last dot-separated segment is the item ID (looks like
// a project-prefixed ID with a hyphen). The preceding segments are the campfire
// name path.
func IsCrossCampfireRef(ref string) bool {
	dot := strings.LastIndex(ref, ".")
	if dot < 0 {
		return false
	}
	itemPart := ref[dot+1:]
	// Item IDs are project-prefixed: at least one letter prefix, a hyphen, then alphanumeric.
	hyphen := strings.Index(itemPart, "-")
	return hyphen > 0 && hyphen < len(itemPart)-1
}

// ParseCrossCampfireRef parses a cross-campfire item reference. Returns nil if ref
// is not a cross-campfire reference.
func ParseCrossCampfireRef(ref string) *CrossCampfireRef {
	if !IsCrossCampfireRef(ref) {
		return nil
	}
	dot := strings.LastIndex(ref, ".")
	return &CrossCampfireRef{
		CampfireName: ref[:dot],
		ItemID:       ref[dot+1:],
		Raw:          ref,
	}
}

// CrossCampfireRef holds the parsed components of a cross-campfire item reference.
type CrossCampfireRef struct {
	// CampfireName is the dot-separated campfire path (e.g. "acme.frontend").
	CampfireName string
	// ItemID is the item ID within the target campfire (e.g. "item-abc").
	ItemID string
	// Raw is the original reference string.
	Raw string
}
