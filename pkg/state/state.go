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
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/3dl-dev/ready/pkg/declarations"
	"github.com/3dl-dev/ready/pkg/msgrec"
)

// labelAtomPattern is the per-atom validation pattern for label names.
// A valid atom is lowercase alphanumeric with hyphens, 1-32 characters,
// starting with a letter or digit (not a hyphen).
// This pattern is checked at DERIVE TIME (read-side). The write-side (executor)
// validates the composite comma-separated scalar pattern; registry membership
// is a read-side concern only because the executor cannot see campfire data.
var labelAtomPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

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
	// warnings holds advisory messages that could not be attached to a specific item
	// (e.g., label-add/remove targeting a nonexistent item ID — signals deleted item,
	// typo, or race in a distributed log).
	warnings []string
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

// Warnings returns advisory messages recorded during derivation that could not be
// attached to a specific item (e.g., label-add/remove targeting a nonexistent item ID).
func (r *DeriveResult) Warnings() []string {
	return r.warnings
}

// TerminalStatuses is the set of statuses where an item is no longer active.
var TerminalStatuses = map[string]bool{
	StatusDone:      true,
	StatusCancelled: true,
	StatusFailed:    true,
}

// Item is the derived state of a work item. All fields are derived from the
// project's signed event log; that log is the source of truth.
type Item struct {
	// ID is the work item ID (project-prefixed, e.g. "ready-a1b").
	ID string `json:"id"`
	// MsgID is the event ID of the work:create event — the antecedent target
	// that subsequent operations (claim, status, close) reference. On a
	// nostr-native project this is the nostr event id.
	MsgID string `json:"msg_id"`
	// CampfireID is set only for legacy campfire-era items. A nostr-native item
	// never carries one, so it is omitempty — the shipped nostr JSON surface
	// must not leak a "campfire_id" field (zero-campfire invariant, ready-327).
	CampfireID string `json:"campfire_id,omitempty"`

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

	// Labels is the set of registry-validated label atoms attached to this item.
	// Labels are validated at derive time: each atom must (a) match the atom pattern
	// ^[a-z0-9][a-z0-9-]{0,31}$ and (b) exist in the campfire label registry.
	// Labels that fail either check are dropped and recorded in LabelWarnings.
	// NOTE: write-side (convention executor) validates the composite pattern only;
	// registry membership is a read-side (derive-time) concern because the executor
	// cannot see campfire-specific data. Documents the write/read enforcement split.
	Labels []string `json:"labels,omitempty"`

	// LabelWarnings records labels that were dropped at derive time because they
	// failed pattern validation or were not present in the label registry.
	// Advisory only — the item still materializes with its other fields intact.
	LabelWarnings []string `json:"label_warnings,omitempty"`

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
	// Labels is the raw comma-separated label scalar from the convention arg.
	// Parsed and validated during handleWorkCreate.
	Labels string `json:"labels,omitempty"`
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

	// warnings accumulates advisory messages that cannot be attached to a specific item
	// (e.g., label-add/remove targeting a nonexistent item ID).
	warnings []string
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
func Derive(campfireID string, msgs []msgrec.MessageRecord) map[string]*Item {
	return DeriveAll(campfireID, msgs).items
}

// DeriveAll replays all work convention messages and returns a DeriveResult
// containing both the item map and the label registry.
func DeriveAll(campfireID string, msgs []msgrec.MessageRecord) *DeriveResult {
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
			handleWorkCreate(campfireID, m, rs, registry)
		case hasTag(m.Tags, "work:status"):
			handleWorkStatus(m, rs)
		case hasTag(m.Tags, "work:claim"):
			handleWorkClaim(m, rs)
		case hasTag(m.Tags, "work:delegate"):
			handleWorkDelegate(m, rs)
		case hasTag(m.Tags, "work:close"):
			handleWorkClose(m, rs)
		case hasTag(m.Tags, "work:update"):
			handleWorkUpdate(m, rs)
		case hasTag(m.Tags, "work:block"):
			handleWorkBlock(m, rs)
		case hasTag(m.Tags, "work:unblock"):
			handleWorkUnblock(m, rs)
		case hasTag(m.Tags, "work:gate"):
			handleWorkGate(m, rs)
		case hasTag(m.Tags, "work:gate-resolve"):
			handleWorkGateResolve(m, rs)
		case hasTag(m.Tags, "work:label-add"):
			handleWorkLabelAdd(m, rs, registry)
		case hasTag(m.Tags, "work:label-remove"):
			handleWorkLabelRemove(m, rs, registry)
		}
	}

	applyBlockStatus(rs)
	applyStrandedItemReclaim(rs.items, roleMap)

	return &DeriveResult{
		items:    rs.items,
		registry: registry,
		warnings: rs.warnings,
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
func buildLabelRegistry(msgs []msgrec.MessageRecord) map[string]LabelDef {
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
func buildRoleMap(msgs []msgrec.MessageRecord) map[string]roleInfo {
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
// registry is the label registry built in Pass 1; used for derive-time label enforcement.
func handleWorkCreate(campfireID string, m msgrec.MessageRecord, rs *replayState, registry map[string]LabelDef) {
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
	// Derive-time label enforcement (read-side trust gate).
	// Write-side (executor) validates the composite pattern only.
	// Registry membership is enforced HERE because the executor cannot see campfire data.
	// Non-registry and malformed labels are DROPPED and recorded as warnings.
	if p.Labels != "" {
		for _, atom := range strings.Split(p.Labels, ",") {
			atom = strings.TrimSpace(atom)
			if atom == "" {
				continue
			}
			if !labelAtomPattern.MatchString(atom) {
				item.LabelWarnings = append(item.LabelWarnings,
					fmt.Sprintf("label %q dropped: fails pattern validation", atom))
				continue
			}
			if _, inRegistry := registry[atom]; !inRegistry {
				item.LabelWarnings = append(item.LabelWarnings,
					fmt.Sprintf("label %q dropped: not in label registry", atom))
				continue
			}
			item.Labels = appendUnique(item.Labels, atom)
		}
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

// labelMutPayload mirrors the fields in a work:label-add or work:label-remove payload.
type labelMutPayload struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// handleWorkLabelAdd processes a work:label-add message, adding the label to the item
// identified by payload.ID (the item ID). Enforcement is identical to handleWorkCreate:
// pattern-invalid and unregistered labels are dropped with a warning.
func handleWorkLabelAdd(m msgrec.MessageRecord, rs *replayState, registry map[string]LabelDef) {
	var p labelMutPayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	if p.ID == "" || p.Label == "" {
		return
	}
	item, ok := rs.items[p.ID]
	if !ok {
		// Nonexistent item: record a warning for observability (signals deleted item,
		// typo, or race in a distributed log). No phantom item is created.
		rs.warnings = append(rs.warnings,
			fmt.Sprintf("label-add targeting nonexistent item %q ignored", p.ID))
		return
	}
	// Enforce the same pattern + registry gate as handleWorkCreate.
	if !labelAtomPattern.MatchString(p.Label) {
		item.LabelWarnings = append(item.LabelWarnings,
			fmt.Sprintf("label %q dropped: fails pattern validation", p.Label))
		return
	}
	if _, inRegistry := registry[p.Label]; !inRegistry {
		item.LabelWarnings = append(item.LabelWarnings,
			fmt.Sprintf("label %q dropped: not in label registry", p.Label))
		return
	}
	item.Labels = appendUnique(item.Labels, p.Label)
}

// handleWorkLabelRemove processes a work:label-remove message, removing the label
// from the item identified by payload.ID. Removing a label not present is idempotent.
func handleWorkLabelRemove(m msgrec.MessageRecord, rs *replayState, registry map[string]LabelDef) {
	var p labelMutPayload
	if err := json.Unmarshal(m.Payload, &p); err != nil {
		return
	}
	if p.ID == "" || p.Label == "" {
		return
	}
	item, ok := rs.items[p.ID]
	if !ok {
		// Nonexistent item: record a warning for observability (signals deleted item,
		// typo, or race in a distributed log). No phantom item is created.
		rs.warnings = append(rs.warnings,
			fmt.Sprintf("label-remove targeting nonexistent item %q ignored", p.ID))
		return
	}
	// Remove the label if present; no-op if absent.
	var filtered []string
	for _, l := range item.Labels {
		if l != p.Label {
			filtered = append(filtered, l)
		}
	}
	item.Labels = filtered
}

// handleWorkStatus processes a work:status message, updating item status and waiting fields.
func handleWorkStatus(m msgrec.MessageRecord, rs *replayState) {
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
func handleWorkClaim(m msgrec.MessageRecord, rs *replayState) {
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

// handleWorkDelegate processes a work:delegate message.
func handleWorkDelegate(m msgrec.MessageRecord, rs *replayState) {
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

// handleWorkClose processes a work:close message, transitioning to a
// terminal status and implicitly unblocking any items this item was blocking.
func handleWorkClose(m msgrec.MessageRecord, rs *replayState) {
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
func handleWorkUpdate(m msgrec.MessageRecord, rs *replayState) {
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
func handleWorkBlock(m msgrec.MessageRecord, rs *replayState) {
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
func handleWorkUnblock(m msgrec.MessageRecord, rs *replayState) {
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
func handleWorkGate(m msgrec.MessageRecord, rs *replayState) {
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

// handleWorkGateResolve processes a work:gate-resolve message.
// approved → transitions to active; rejected → item remains waiting.
func handleWorkGateResolve(m msgrec.MessageRecord, rs *replayState) {
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

// DeriveFromJSONL reads all MutationRecords from the given JSONL file path,
// converts them to msgrec.MessageRecord, and derives item state by replaying
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
	// Convert MutationRecords → msgrec.MessageRecord.
	msgs := make([]msgrec.MessageRecord, len(records))
	for i, r := range records {
		msgs[i] = msgrec.MessageRecord{
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
