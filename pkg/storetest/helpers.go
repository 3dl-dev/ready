// Package storetest provides a Harness for testing code that depends on
// the campfire store and work management convention. It wraps a real SQLite
// store with convention-aware helpers so tests can create items, send
// convention messages, and query derived state without mock infrastructure.
package storetest

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/views"
)

// DefaultCampfire is the campfire ID used by the Harness unless overridden.
const DefaultCampfire = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

// DefaultSender is the sender identity used by the Harness unless overridden.
const DefaultSender = "testkey"

// counter is a package-level atomic counter for generating unique IDs.
var counter atomic.Int64

// nextID returns a unique string suffix for use in message and item IDs.
func nextID() string {
	return fmt.Sprintf("%d", counter.Add(1))
}

// nextTimestamp returns an auto-incrementing nanosecond timestamp.
// Each call returns a value 1ms greater than the last, ensuring ordering.
var tsBase = time.Now().UnixNano()
var tsCounter atomic.Int64

func nextTimestamp() int64 {
	n := tsCounter.Add(1)
	return tsBase + n*int64(time.Millisecond)
}

// Harness wraps a real SQLite campfire store with convention-aware helpers.
type Harness struct {
	t          *testing.T
	Store      store.Store
	CampfireID string
	Sender     string
}

// New creates a Harness with a fresh temp SQLite store and registers a campfire
// membership. The store is automatically closed when the test ends.
func New(t *testing.T) *Harness {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir + "/store.db")
	if err != nil {
		t.Fatalf("storetest.New: open store: %v", err)
	}
	t.Cleanup(func() { s.Close() })

	h := &Harness{
		t:          t,
		Store:      s,
		CampfireID: DefaultCampfire,
		Sender:     DefaultSender,
	}
	h.addMembership()
	return h
}

// WithSender returns a copy of the Harness with a different sender identity.
// The copy shares the same underlying store and campfire ID.
func (h *Harness) WithSender(sender string) *Harness {
	return &Harness{
		t:          h.t,
		Store:      h.Store,
		CampfireID: h.CampfireID,
		Sender:     sender,
	}
}

// addMembership registers the campfire membership in the store.
func (h *Harness) addMembership() {
	h.t.Helper()
	if err := h.Store.AddMembership(store.Membership{
		CampfireID:   h.CampfireID,
		TransportDir: os.TempDir(),
		JoinProtocol: "invite",
		Role:         "full",
		JoinedAt:     time.Now().Unix(),
	}); err != nil {
		// Ignore duplicate errors — harmless if already registered.
		_ = err
	}
}

// addMessage writes a MessageRecord to the store and returns the message ID.
func (h *Harness) addMessage(tag string, payload interface{}, antecedents []string) string {
	h.t.Helper()
	msgID := "msg-" + nextID()
	raw, err := json.Marshal(payload)
	if err != nil {
		h.t.Fatalf("storetest: marshal payload for %s: %v", tag, err)
	}
	ts := nextTimestamp()
	_, err = h.Store.AddMessage(store.MessageRecord{
		ID:          msgID,
		CampfireID:  h.CampfireID,
		Sender:      h.Sender,
		Payload:     raw,
		Tags:        []string{tag},
		Antecedents: antecedents,
		Timestamp:   ts,
		Signature:   []byte("fakesig-" + msgID),
		ReceivedAt:  ts,
	})
	if err != nil {
		h.t.Fatalf("storetest: AddMessage(%s): %v", tag, err)
	}
	return msgID
}

// ---------------------------------------------------------------------------
// CreateOpt — functional options for Create.
// ---------------------------------------------------------------------------

type createConfig struct {
	context  string
	itemType string
	level    string
	project  string
	forParty string
	by       string
	parentID string
	eta      string
	due      string
	labels   []string
}

// CreateOpt is a functional option for the Create method.
type CreateOpt func(*createConfig)

// WithContext sets the context/description field on the created item.
func WithContext(ctx string) CreateOpt {
	return func(c *createConfig) { c.context = ctx }
}

// WithType sets the item type (task, decision, review, etc.).
func WithType(t string) CreateOpt {
	return func(c *createConfig) { c.itemType = t }
}

// WithLevel sets the item level (epic, task, subtask).
func WithLevel(l string) CreateOpt {
	return func(c *createConfig) { c.level = l }
}

// WithProject sets the project field.
func WithProject(p string) CreateOpt {
	return func(c *createConfig) { c.project = p }
}

// WithFor sets the for (beneficiary) field.
func WithFor(f string) CreateOpt {
	return func(c *createConfig) { c.forParty = f }
}

// WithBy sets the by (assignee) field at creation time.
func WithBy(b string) CreateOpt {
	return func(c *createConfig) { c.by = b }
}

// WithParentID sets the parent_id field for hierarchy.
func WithParentID(pid string) CreateOpt {
	return func(c *createConfig) { c.parentID = pid }
}

// WithETA sets the eta field.
func WithETA(eta string) CreateOpt {
	return func(c *createConfig) { c.eta = eta }
}

// WithDue sets the due field.
func WithDue(due string) CreateOpt {
	return func(c *createConfig) { c.due = due }
}

// WithLabels sets the labels field as a comma-separated scalar.
// Labels are joined with commas in the payload. Derive-time enforcement
// will drop any labels that are malformed or not in the registry.
func WithLabels(labels ...string) CreateOpt {
	return func(c *createConfig) { c.labels = labels }
}

// ---------------------------------------------------------------------------
// StatusOpt — functional options for UpdateStatus.
// ---------------------------------------------------------------------------

type statusConfig struct {
	reason      string
	waitingOn   string
	waitingType string
}

// StatusOpt is a functional option for the UpdateStatus method.
type StatusOpt func(*statusConfig)

// WithReason sets the reason field on the status change.
func WithReason(r string) StatusOpt {
	return func(c *statusConfig) { c.reason = r }
}

// WithWaitingOn sets the waiting_on field (used when status=waiting).
func WithWaitingOn(w string) StatusOpt {
	return func(c *statusConfig) { c.waitingOn = w }
}

// WithWaitingType sets the waiting_type field (used when status=waiting).
func WithWaitingType(wt string) StatusOpt {
	return func(c *statusConfig) { c.waitingType = wt }
}

// ---------------------------------------------------------------------------
// Convention message methods.
// ---------------------------------------------------------------------------

// Create sends a work:create message and returns the message ID.
// The item ID is derived from the provided id argument.
func (h *Harness) Create(id, title, priority string, opts ...CreateOpt) string {
	h.t.Helper()
	cfg := &createConfig{
		itemType: "task",
		forParty: "baron@3dl.dev",
	}
	for _, o := range opts {
		o(cfg)
	}
	payload := map[string]interface{}{
		"id":       id,
		"title":    title,
		"priority": priority,
		"type":     cfg.itemType,
		"for":      cfg.forParty,
	}
	if cfg.context != "" {
		payload["context"] = cfg.context
	}
	if cfg.level != "" {
		payload["level"] = cfg.level
	}
	if cfg.project != "" {
		payload["project"] = cfg.project
	}
	if cfg.by != "" {
		payload["by"] = cfg.by
	}
	if cfg.parentID != "" {
		payload["parent_id"] = cfg.parentID
	}
	if cfg.eta != "" {
		payload["eta"] = cfg.eta
	}
	if cfg.due != "" {
		payload["due"] = cfg.due
	}
	if len(cfg.labels) > 0 {
		payload["labels"] = strings.Join(cfg.labels, ",")
	}
	return h.addMessage("work:create", payload, nil)
}

// RawCreate injects a work:create message directly into the store, bypassing
// the convention executor. This is the bypass path — equivalent to the
// ready-1c2 pending.jsonl flush that writes raw via transport, skipping
// executor validation. Used to test that derive-time enforcement catches
// labels that the write-side gate never saw.
func (h *Harness) RawCreate(id, title, priority string, rawPayload map[string]interface{}) string {
	h.t.Helper()
	// Merge required fields with caller-provided raw payload (caller wins).
	merged := map[string]interface{}{
		"id":       id,
		"title":    title,
		"priority": priority,
		"type":     "task",
		"for":      "baron@3dl.dev",
	}
	for k, v := range rawPayload {
		merged[k] = v
	}
	return h.addMessage("work:create", merged, nil)
}

// Claim sends a work:claim message referencing the given create message ID.
// The claim sets by=Sender and transitions the item to active.
func (h *Harness) Claim(createMsgID string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"target": createMsgID,
	}
	return h.addMessage("work:claim", payload, []string{createMsgID})
}

// UpdateStatus sends a work:status message referencing the given create message ID.
func (h *Harness) UpdateStatus(createMsgID, status string, opts ...StatusOpt) string {
	h.t.Helper()
	cfg := &statusConfig{}
	for _, o := range opts {
		o(cfg)
	}
	payload := map[string]interface{}{
		"target": createMsgID,
		"to":     status,
	}
	if cfg.reason != "" {
		payload["reason"] = cfg.reason
	}
	if cfg.waitingOn != "" {
		payload["waiting_on"] = cfg.waitingOn
	}
	if cfg.waitingType != "" {
		payload["waiting_type"] = cfg.waitingType
	}
	return h.addMessage("work:status", payload, []string{createMsgID})
}

// Close sends a work:close message. resolution is one of: done, cancelled, failed.
func (h *Harness) Close(createMsgID, resolution, reason string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"target":     createMsgID,
		"resolution": resolution,
		"reason":     reason,
	}
	return h.addMessage("work:close", payload, []string{createMsgID})
}

// UpdateFields sends a work:update message with the provided field map.
// The fields map may contain any updateable field names (title, context,
// priority, eta, due, level, for, by, gate). Use "-" as a value to clear a field.
func (h *Harness) UpdateFields(createMsgID string, fields map[string]string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"target": createMsgID,
	}
	for k, v := range fields {
		payload[k] = v
	}
	return h.addMessage("work:update", payload, []string{createMsgID})
}

// Block sends a work:block message establishing that blockerMsg's item blocks
// blockedMsg's item.
func (h *Harness) Block(blockerID, blockedID, blockerMsg, blockedMsg string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"blocker_id":  blockerID,
		"blocked_id":  blockedID,
		"blocker_msg": blockerMsg,
		"blocked_msg": blockedMsg,
	}
	return h.addMessage("work:block", payload, nil)
}

// Unblock sends a work:unblock message targeting the given work:block message ID.
func (h *Harness) Unblock(blockMsgID string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"target": blockMsgID,
	}
	return h.addMessage("work:unblock", payload, []string{blockMsgID})
}

// Gate sends a work:gate message requesting human escalation.
// gateType is one of: budget, design, scope, review, human, stall.
func (h *Harness) Gate(createMsgID, gateType, description string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"target":      createMsgID,
		"gate_type":   gateType,
		"description": description,
	}
	return h.addMessage("work:gate", payload, []string{createMsgID})
}

// GateResolve sends a work:gate-resolve message. resolution is "approved" or "rejected".
func (h *Harness) GateResolve(gateMsgID, resolution string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"target":     gateMsgID,
		"resolution": resolution,
	}
	return h.addMessage("work:gate-resolve", payload, []string{gateMsgID})
}

// Delegate sends a work:delegate message assigning the item to toParty.
func (h *Harness) Delegate(createMsgID, toParty string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"target": createMsgID,
		"to":     toParty,
		"from":   h.Sender,
	}
	return h.addMessage("work:delegate", payload, []string{createMsgID})
}

// ---------------------------------------------------------------------------
// Query methods.
// ---------------------------------------------------------------------------

// RoleGrant sends a work:role-grant message granting role to pubkey.
// role is one of: "maintainer", "contributor", "revoked".
func (h *Harness) RoleGrant(pubkey, role string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"pubkey":     pubkey,
		"role":       role,
		"granted_at": "2026-01-01T00:00:00Z",
	}
	return h.addMessage("work:role-grant", payload, nil)
}

// AddLabel sends a work:label-add message for the given item ID and label.
// itemID is the work item's ID string (e.g. "ready-94a"), not a message ID.
func (h *Harness) AddLabel(itemID, label string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"id":    itemID,
		"label": label,
	}
	return h.addMessage("work:label-add", payload, nil)
}

// RemoveLabel sends a work:label-remove message for the given item ID and label.
// itemID is the work item's ID string (e.g. "ready-94a"), not a message ID.
func (h *Harness) RemoveLabel(itemID, label string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"id":    itemID,
		"label": label,
	}
	return h.addMessage("work:label-remove", payload, nil)
}

// RawAddLabel injects a work:label-add message directly into the store, bypassing
// the convention executor. Used to test derive-time enforcement for labels that
// the write-side gate never validated (bypass path).
// itemID is the work item's ID string.
func (h *Harness) RawAddLabel(itemID, label string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"id":    itemID,
		"label": label,
	}
	return h.addMessage("work:label-add", payload, nil)
}

// RawAddLabelByItemID injects a work:label-add message targeting an item by its
// item ID string. Used to test nonexistent item paths.
func (h *Harness) RawAddLabelByItemID(itemID, label string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"id":    itemID,
		"label": label,
	}
	return h.addMessage("work:label-add", payload, nil)
}

// RawRemoveLabelByItemID injects a work:label-remove message targeting an item by
// its item ID string. Used to test nonexistent item paths.
func (h *Harness) RawRemoveLabelByItemID(itemID, label string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"id":    itemID,
		"label": label,
	}
	return h.addMessage("work:label-remove", payload, nil)
}

// LabelDefine sends a work:label-define message.
// desc may be empty.
func (h *Harness) LabelDefine(label, desc string) string {
	h.t.Helper()
	payload := map[string]interface{}{
		"label": label,
	}
	if desc != "" {
		payload["description"] = desc
	}
	return h.addMessage("work:label-define", payload, nil)
}

// DeriveAll calls state.DeriveAllFromStore and returns the full DeriveResult.
func (h *Harness) DeriveAll() *state.DeriveResult {
	h.t.Helper()
	result, err := state.DeriveAllFromStore(h.Store, h.CampfireID)
	if err != nil {
		h.t.Fatalf("storetest: DeriveAllFromStore: %v", err)
	}
	return result
}

// Derive calls state.DeriveFromStore and returns the full item map.
func (h *Harness) Derive() map[string]*state.Item {
	h.t.Helper()
	items, err := state.DeriveFromStore(h.Store, h.CampfireID)
	if err != nil {
		h.t.Fatalf("storetest: DeriveFromStore: %v", err)
	}
	return items
}

// Item returns the derived state for the given item ID, or nil if not found.
func (h *Harness) Item(id string) *state.Item {
	return h.Derive()[id]
}

// MustItem returns the derived state for the given item ID, fataling if not found.
func (h *Harness) MustItem(id string) *state.Item {
	h.t.Helper()
	item := h.Item(id)
	if item == nil {
		h.t.Fatalf("storetest: item %q not found in derived state", id)
	}
	return item
}

// Ready returns all items passing the ReadyFilter.
func (h *Harness) Ready() []*state.Item {
	h.t.Helper()
	derived := h.Derive()
	f := views.ReadyFilter()
	var result []*state.Item
	for _, item := range derived {
		if f(item) {
			result = append(result, item)
		}
	}
	return result
}

// ViewItems returns all items passing the named view filter for the given identity.
// Returns nil if the view name is not recognized.
func (h *Harness) ViewItems(viewName, identity string) []*state.Item {
	h.t.Helper()
	f := views.Named(viewName, identity)
	if f == nil {
		return nil
	}
	derived := h.Derive()
	var result []*state.Item
	for _, item := range derived {
		if f(item) {
			result = append(result, item)
		}
	}
	return result
}
