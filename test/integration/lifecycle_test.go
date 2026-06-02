//go:build integration

// Package integration tests the convention layer (pkg/state + pkg/views) via the Go API
// directly against a real campfire. It verifies that messages round-trip correctly through
// the campfire store and that state derivation produces correct results.
//
// Use this layer to test: message encoding, state derivation, view predicates, compaction.
// Use test/e2e/ to test: the rd CLI binary, flag parsing, JSON output, command behaviour.
//
// Run with:
//
//	go test -tags integration ./test/integration/
package integration

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	campfirepkg "github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"
	"github.com/campfire-net/ready/pkg/state"
	"github.com/campfire-net/ready/pkg/views"
)

// testHarness holds shared state for integration test steps.
type testHarness struct {
	t          *testing.T
	tmpDir     string
	id         *identity.Identity
	cf         *campfirepkg.Campfire
	campfireID string
	tr         *fs.Transport
	s          store.Store
}

// newHarness creates a fresh campfire environment in a temp directory.
func newHarness(t *testing.T) *testHarness {
	t.Helper()
	tmpDir := t.TempDir()

	// Generate test identity.
	id, err := identity.Generate()
	if err != nil {
		t.Fatalf("identity.Generate(): %v", err)
	}

	// Create campfire (open, no reception requirements, threshold=1).
	cf, err := campfirepkg.New("open", nil, 1)
	if err != nil {
		t.Fatalf("campfire.New(): %v", err)
	}

	// Add the test identity as a member.
	cf.AddMember(id.PublicKey)

	campfireID := cf.PublicKeyHex()

	// Initialize filesystem transport.
	tr := fs.New(tmpDir)
	if err := tr.Init(cf); err != nil {
		t.Fatalf("tr.Init(): %v", err)
	}

	// Write the member record for our identity.
	memberRecord := campfirepkg.MemberRecord{
		PublicKey: id.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfirepkg.RoleFull,
	}
	if err := tr.WriteMember(campfireID, memberRecord); err != nil {
		t.Fatalf("tr.WriteMember(): %v", err)
	}

	// Open SQLite store.
	dbPath := filepath.Join(tmpDir, "store.db")
	s, err := store.Open(dbPath)
	if err != nil {
		t.Fatalf("store.Open(): %v", err)
	}
	t.Cleanup(func() { s.Close() })

	// Add membership to the store.
	membership := store.Membership{
		CampfireID:   campfireID,
		TransportDir: tmpDir,
		JoinProtocol: "open",
		Role:         store.PeerRoleCreator,
		JoinedAt:     time.Now().UnixNano(),
		Threshold:    1,
	}
	if err := s.AddMembership(membership); err != nil {
		t.Fatalf("s.AddMembership(): %v", err)
	}

	return &testHarness{
		t:          t,
		tmpDir:     tmpDir,
		id:         id,
		cf:         cf,
		campfireID: campfireID,
		tr:         tr,
		s:          s,
	}
}

// sendMsg creates a real signed message and writes it to the transport + store.
func (h *testHarness) sendMsg(payload []byte, tags []string, antecedents []string) *message.Message {
	h.t.Helper()

	msg, err := message.NewMessage(h.id.PrivateKey, h.id.PublicKey, payload, tags, antecedents)
	if err != nil {
		h.t.Fatalf("message.NewMessage(tags=%v): %v", tags, err)
	}

	// Add provenance hop (campfire signs it).
	cfState, err := h.tr.ReadState(h.campfireID)
	if err != nil {
		h.t.Fatalf("tr.ReadState(): %v", err)
	}
	members, err := h.tr.ListMembers(h.campfireID)
	if err != nil {
		h.t.Fatalf("tr.ListMembers(): %v", err)
	}
	cf := cfState.ToCampfire(members)

	if err := msg.AddHop(
		cfState.PrivateKey, cfState.PublicKey,
		cf.MembershipHash(), len(members),
		cfState.JoinProtocol, cfState.ReceptionRequirements,
		campfirepkg.RoleFull,
	); err != nil {
		h.t.Fatalf("msg.AddHop(): %v", err)
	}

	// Write to transport.
	if err := h.tr.WriteMessage(h.campfireID, msg); err != nil {
		h.t.Fatalf("tr.WriteMessage(): %v", err)
	}

	// Store locally.
	record := store.MessageRecordFromMessage(h.campfireID, msg, store.NowNano())
	if _, err := h.s.AddMessage(record); err != nil {
		h.t.Fatalf("s.AddMessage(): %v", err)
	}

	return msg
}

// deriveState returns the current derived item state from the local store.
func (h *testHarness) deriveState() map[string]*state.Item {
	h.t.Helper()
	items, err := state.DeriveFromStore(h.s, h.campfireID)
	if err != nil {
		h.t.Fatalf("state.DeriveFromStore(): %v", err)
	}
	return items
}

// getItem fetches a single item by ID, fataling if not found.
func (h *testHarness) getItem(items map[string]*state.Item, id string) *state.Item {
	h.t.Helper()
	item, ok := items[id]
	if !ok {
		h.t.Fatalf("item %s not found in derived state (items: %v)", id, itemIDs(items))
	}
	return item
}

func itemIDs(items map[string]*state.Item) []string {
	ids := make([]string, 0, len(items))
	for id := range items {
		ids = append(ids, id)
	}
	return ids
}

// mustMarshal marshals v or fatals.
func mustMarshal(t *testing.T, v interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	return b
}

// verifyConformance asserts convention conformance on a sent message.
func verifyConformance(t *testing.T, msg *message.Message, expectedTags []string, expectedAntecedents []string) {
	t.Helper()

	// Signature must verify.
	if !msg.VerifySignature() {
		t.Errorf("message %s: signature verification failed", msg.ID)
	}

	// Required tags must be present.
	tagSet := make(map[string]bool, len(msg.Tags))
	for _, tag := range msg.Tags {
		tagSet[tag] = true
	}
	for _, expected := range expectedTags {
		if !tagSet[expected] {
			t.Errorf("message %s: missing expected tag %q (tags: %v)", msg.ID, expected, msg.Tags)
		}
	}

	// Required antecedents must be present.
	antSet := make(map[string]bool, len(msg.Antecedents))
	for _, ant := range msg.Antecedents {
		antSet[ant] = true
	}
	for _, expected := range expectedAntecedents {
		if !antSet[expected] {
			t.Errorf("message %s: missing expected antecedent %q (antecedents: %v)", msg.ID, expected, msg.Antecedents)
		}
	}

	// Payload must be non-empty.
	if len(msg.Payload) == 0 {
		t.Errorf("message %s: payload is empty", msg.ID)
	}
}

// TestLifecycle_CreateClaimUpdateDelegateClose exercises the primary item lifecycle.
func TestLifecycle_CreateClaimUpdateDelegateClose(t *testing.T) {
	h := newHarness(t)

	// --- Step 1: Create ---
	createPayload := mustMarshal(t, map[string]interface{}{
		"id":       "ready-test-001",
		"title":    "Implement lifecycle test",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p1",
		"project":  "ready",
	})
	createMsg := h.sendMsg(createPayload, []string{"work:create"}, nil)
	verifyConformance(t, createMsg, []string{"work:create"}, nil)

	items := h.deriveState()
	item := h.getItem(items, "ready-test-001")
	if item.Status != state.StatusInbox {
		t.Errorf("after create: status=%s, want inbox", item.Status)
	}
	if item.Title != "Implement lifecycle test" {
		t.Errorf("after create: title=%q, want 'Implement lifecycle test'", item.Title)
	}
	if item.MsgID != createMsg.ID {
		t.Errorf("after create: MsgID=%q, want %q", item.MsgID, createMsg.ID)
	}

	// --- Step 2: Claim ---
	claimPayload := mustMarshal(t, map[string]interface{}{
		"target": createMsg.ID,
		"reason": "Picking this up now",
	})
	claimMsg := h.sendMsg(claimPayload, []string{"work:claim"}, []string{createMsg.ID})
	verifyConformance(t, claimMsg, []string{"work:claim"}, []string{createMsg.ID})

	items = h.deriveState()
	item = h.getItem(items, "ready-test-001")
	if item.Status != state.StatusActive {
		t.Errorf("after claim: status=%s, want active", item.Status)
	}
	senderHex := fmt.Sprintf("%x", h.id.PublicKey)
	if item.By != senderHex {
		t.Errorf("after claim: by=%q, want %q", item.By, senderHex)
	}

	// --- Step 3: Update ---
	updatePayload := mustMarshal(t, map[string]interface{}{
		"target":  createMsg.ID,
		"context": "Updated context after investigation",
		"priority": "p0",
	})
	updateMsg := h.sendMsg(updatePayload, []string{"work:update"}, []string{createMsg.ID})
	verifyConformance(t, updateMsg, []string{"work:update"}, []string{createMsg.ID})

	items = h.deriveState()
	item = h.getItem(items, "ready-test-001")
	if item.Context != "Updated context after investigation" {
		t.Errorf("after update: context=%q, want 'Updated context after investigation'", item.Context)
	}
	if item.Priority != "p0" {
		t.Errorf("after update: priority=%q, want p0", item.Priority)
	}

	// --- Step 4: Delegate ---
	delegateTo := "alice@3dl.dev"
	delegatePayload := mustMarshal(t, map[string]interface{}{
		"target": createMsg.ID,
		"to":     delegateTo,
		"from":   senderHex,
		"reason": "Better suited",
	})
	delegateMsg := h.sendMsg(delegatePayload, []string{"work:delegate", "work:by:" + delegateTo}, []string{createMsg.ID})
	verifyConformance(t, delegateMsg, []string{"work:delegate", "work:by:" + delegateTo}, []string{createMsg.ID})

	items = h.deriveState()
	item = h.getItem(items, "ready-test-001")
	if item.By != delegateTo {
		t.Errorf("after delegate: by=%q, want %q", item.By, delegateTo)
	}

	// --- Step 5: Close ---
	closePayloadBytes := mustMarshal(t, map[string]interface{}{
		"target":     createMsg.ID,
		"resolution": "done",
		"reason":     "Lifecycle test complete",
	})
	closeMsg := h.sendMsg(closePayloadBytes, []string{"work:close", "work:resolution:done"}, []string{createMsg.ID})
	verifyConformance(t, closeMsg, []string{"work:close", "work:resolution:done"}, []string{createMsg.ID})

	items = h.deriveState()
	item = h.getItem(items, "ready-test-001")
	if item.Status != state.StatusDone {
		t.Errorf("after close: status=%s, want done", item.Status)
	}
	if !state.IsTerminal(item) {
		t.Errorf("after close: item should be terminal")
	}

	// --- Step 6: Views ---
	allItems := make([]*state.Item, 0, len(items))
	for _, i := range items {
		allItems = append(allItems, i)
	}

	readyItems := views.Apply(allItems, views.ReadyFilter())
	for _, ri := range readyItems {
		if ri.ID == "ready-test-001" {
			t.Errorf("closed item should not appear in ready view")
		}
	}

	workItems := views.Apply(allItems, views.WorkFilter())
	for _, wi := range workItems {
		if wi.ID == "ready-test-001" {
			t.Errorf("closed item should not appear in work view")
		}
	}
}

// TestLifecycle_BlockImplicitUnblock creates two items, blocks one on the other,
// then closes the blocker and verifies the implicit unblock.
func TestLifecycle_BlockImplicitUnblock(t *testing.T) {
	h := newHarness(t)

	// Create item 1 (the blocker).
	create1Payload := mustMarshal(t, map[string]interface{}{
		"id":       "ready-block-001",
		"title":    "Blocker item",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p1",
	})
	create1Msg := h.sendMsg(create1Payload, []string{"work:create"}, nil)
	verifyConformance(t, create1Msg, []string{"work:create"}, nil)

	// Create item 2 (the blocked item).
	create2Payload := mustMarshal(t, map[string]interface{}{
		"id":       "ready-block-002",
		"title":    "Blocked item",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p2",
	})
	create2Msg := h.sendMsg(create2Payload, []string{"work:create"}, nil)
	verifyConformance(t, create2Msg, []string{"work:create"}, nil)

	// Block item 2 on item 1.
	blockPayload := mustMarshal(t, map[string]interface{}{
		"blocker_id":  "ready-block-001",
		"blocked_id":  "ready-block-002",
		"blocker_msg": create1Msg.ID,
		"blocked_msg": create2Msg.ID,
	})
	blockMsg := h.sendMsg(blockPayload, []string{"work:block"}, []string{create1Msg.ID, create2Msg.ID})
	verifyConformance(t, blockMsg, []string{"work:block"}, []string{create1Msg.ID, create2Msg.ID})

	// Verify item 2 is blocked.
	items := h.deriveState()
	item2 := h.getItem(items, "ready-block-002")
	if item2.Status != state.StatusBlocked {
		t.Errorf("after block: item2 status=%s, want blocked", item2.Status)
	}
	item1 := h.getItem(items, "ready-block-001")
	found := false
	for _, b := range item1.Blocks {
		if b == "ready-block-002" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("item1.Blocks should contain ready-block-002, got %v", item1.Blocks)
	}

	// Close item 1 (the blocker) — implicit unblock.
	close1Payload := mustMarshal(t, map[string]interface{}{
		"target":     create1Msg.ID,
		"resolution": "done",
		"reason":     "Blocker resolved",
	})
	close1Msg := h.sendMsg(close1Payload, []string{"work:close", "work:resolution:done"}, []string{create1Msg.ID})
	verifyConformance(t, close1Msg, []string{"work:close"}, []string{create1Msg.ID})

	// Verify item 2 is no longer blocked.
	items = h.deriveState()
	item2 = h.getItem(items, "ready-block-002")
	if item2.Status == state.StatusBlocked {
		t.Errorf("after blocker close: item2 should not be blocked, got %s", item2.Status)
	}

	// Verify item 2 now appears in the pending view (inbox by default, not blocked).
	allItems := make([]*state.Item, 0, len(items))
	for _, i := range items {
		allItems = append(allItems, i)
	}
	pendingItems := views.Apply(allItems, views.PendingFilter())
	for _, pi := range pendingItems {
		if pi.ID == "ready-block-002" {
			t.Errorf("unblocked item should not be in pending view (pending = waiting/scheduled/blocked)")
		}
	}
}

// TestLifecycle_GateApprove exercises the gate lifecycle: gate → approve → active.
func TestLifecycle_GateApprove(t *testing.T) {
	h := newHarness(t)

	// Create an item.
	createPayload := mustMarshal(t, map[string]interface{}{
		"id":       "ready-gate-001",
		"title":    "Gated item",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p1",
	})
	createMsg := h.sendMsg(createPayload, []string{"work:create"}, nil)

	// Claim it (active).
	claimPayload := mustMarshal(t, map[string]interface{}{
		"target": createMsg.ID,
	})
	h.sendMsg(claimPayload, []string{"work:claim"}, []string{createMsg.ID})

	// Gate it — requesting design review.
	gatePayload := mustMarshal(t, map[string]interface{}{
		"target":      createMsg.ID,
		"gate_type":   "design",
		"description": "Confirm architecture approach",
	})
	gateMsg := h.sendMsg(gatePayload, []string{"work:gate", "work:gate-type:design"}, []string{createMsg.ID})
	verifyConformance(t, gateMsg, []string{"work:gate", "work:gate-type:design"}, []string{createMsg.ID})

	// Verify status=waiting, waiting_type=gate.
	items := h.deriveState()
	item := h.getItem(items, "ready-gate-001")
	if item.Status != state.StatusWaiting {
		t.Errorf("after gate: status=%s, want waiting", item.Status)
	}
	if item.WaitingType != "gate" {
		t.Errorf("after gate: waiting_type=%q, want gate", item.WaitingType)
	}
	if item.GateMsgID != gateMsg.ID {
		t.Errorf("after gate: GateMsgID=%q, want %q", item.GateMsgID, gateMsg.ID)
	}

	// Verify it appears in the gates view.
	allItems := make([]*state.Item, 0, len(items))
	for _, i := range items {
		allItems = append(allItems, i)
	}
	gateItems := views.Apply(allItems, views.GatesFilter())
	found := false
	for _, gi := range gateItems {
		if gi.ID == "ready-gate-001" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("gated item should appear in gates view")
	}

	// Approve the gate.
	approvePayload := mustMarshal(t, map[string]interface{}{
		"target":     gateMsg.ID,
		"resolution": "approved",
		"reason":     "Architecture looks good",
	})
	approveMsg := h.sendMsg(approvePayload, []string{"work:gate-resolve", "work:gate-resolve:approved"}, []string{gateMsg.ID})
	verifyConformance(t, approveMsg, []string{"work:gate-resolve"}, []string{gateMsg.ID})

	// Verify item transitions back to active after approval.
	items = h.deriveState()
	item = h.getItem(items, "ready-gate-001")
	if item.Status != state.StatusActive {
		t.Errorf("after gate approve: status=%s, want active", item.Status)
	}
	if item.GateMsgID != "" {
		t.Errorf("after gate approve: GateMsgID should be cleared, got %q", item.GateMsgID)
	}
	if item.WaitingType != "" {
		t.Errorf("after gate approve: WaitingType should be cleared, got %q", item.WaitingType)
	}
}

// TestLifecycle_Views verifies that view filters correctly categorize items.
func TestLifecycle_Views(t *testing.T) {
	h := newHarness(t)

	// Create an inbox item (should appear in ready view).
	inboxPayload := mustMarshal(t, map[string]interface{}{
		"id":       "ready-view-001",
		"title":    "Inbox item",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p0",
	})
	inboxMsg := h.sendMsg(inboxPayload, []string{"work:create"}, nil)

	// Create a second item and set it to waiting.
	waitingPayload := mustMarshal(t, map[string]interface{}{
		"id":       "ready-view-002",
		"title":    "Waiting item",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p2",
	})
	waitingMsg := h.sendMsg(waitingPayload, []string{"work:create"}, nil)

	statusWaitingPayload := mustMarshal(t, map[string]interface{}{
		"target":       waitingMsg.ID,
		"to":           "waiting",
		"waiting_on":   "vendor response",
		"waiting_type": "vendor",
	})
	h.sendMsg(statusWaitingPayload, []string{"work:status", "work:status:waiting"}, []string{waitingMsg.ID})

	// Create a third item and claim it (active).
	activePayload := mustMarshal(t, map[string]interface{}{
		"id":       "ready-view-003",
		"title":    "Active item",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p1",
	})
	activeMsg := h.sendMsg(activePayload, []string{"work:create"}, nil)

	claimPayload := mustMarshal(t, map[string]interface{}{
		"target": activeMsg.ID,
	})
	h.sendMsg(claimPayload, []string{"work:claim"}, []string{activeMsg.ID})

	// Create a fourth item and close it (done).
	donePayload := mustMarshal(t, map[string]interface{}{
		"id":       "ready-view-004",
		"title":    "Done item",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p3",
	})
	doneMsg := h.sendMsg(donePayload, []string{"work:create"}, nil)

	closePayload := mustMarshal(t, map[string]interface{}{
		"target":     doneMsg.ID,
		"resolution": "done",
	})
	_ = inboxMsg // used above to create the item
	h.sendMsg(closePayload, []string{"work:close", "work:resolution:done"}, []string{doneMsg.ID})

	items := h.deriveState()
	allItems := make([]*state.Item, 0, len(items))
	for _, i := range items {
		allItems = append(allItems, i)
	}

	// ReadyFilter: inbox item with p0 ETA should be ready; active item is not blocked.
	readyItems := views.Apply(allItems, views.ReadyFilter())
	readyIDs := make(map[string]bool)
	for _, ri := range readyItems {
		readyIDs[ri.ID] = true
	}
	if !readyIDs["ready-view-001"] {
		t.Errorf("inbox p0 item should be in ready view")
	}
	if readyIDs["ready-view-004"] {
		t.Errorf("done item should NOT be in ready view")
	}
	if readyIDs["ready-view-002"] {
		t.Errorf("waiting item should NOT be in ready view")
	}

	// WorkFilter: only active items.
	workItems := views.Apply(allItems, views.WorkFilter())
	workIDs := make(map[string]bool)
	for _, wi := range workItems {
		workIDs[wi.ID] = true
	}
	if !workIDs["ready-view-003"] {
		t.Errorf("claimed item should appear in work view")
	}
	if workIDs["ready-view-001"] {
		t.Errorf("inbox item should NOT be in work view")
	}
	if workIDs["ready-view-004"] {
		t.Errorf("done item should NOT be in work view")
	}

	// PendingFilter: waiting/scheduled/blocked.
	pendingItems := views.Apply(allItems, views.PendingFilter())
	pendingIDs := make(map[string]bool)
	for _, pi := range pendingItems {
		pendingIDs[pi.ID] = true
	}
	if !pendingIDs["ready-view-002"] {
		t.Errorf("waiting item should appear in pending view")
	}
	if pendingIDs["ready-view-001"] {
		t.Errorf("inbox item should NOT be in pending view")
	}
}

// TestConformance_AllMessages runs convention conformance checks on every message type.
func TestConformance_AllMessages(t *testing.T) {
	h := newHarness(t)

	// Create
	createPayload := mustMarshal(t, map[string]interface{}{
		"id":       "ready-conform-001",
		"title":    "Conformance test item",
		"type":     "task",
		"for":      "baron@3dl.dev",
		"priority": "p1",
	})
	createMsg := h.sendMsg(createPayload, []string{"work:create"}, nil)

	// Verify: signature, tags, payload, antecedents.
	if !createMsg.VerifySignature() {
		t.Errorf("work:create message signature invalid")
	}
	if len(createMsg.Tags) != 1 || createMsg.Tags[0] != "work:create" {
		t.Errorf("work:create: expected tags=[work:create], got %v", createMsg.Tags)
	}
	if len(createMsg.Antecedents) != 0 {
		t.Errorf("work:create: expected no antecedents, got %v", createMsg.Antecedents)
	}
	if len(createMsg.Payload) == 0 {
		t.Errorf("work:create: payload must not be empty")
	}

	// Claim
	claimPayload := mustMarshal(t, map[string]interface{}{"target": createMsg.ID})
	claimMsg := h.sendMsg(claimPayload, []string{"work:claim"}, []string{createMsg.ID})
	if !claimMsg.VerifySignature() {
		t.Errorf("work:claim message signature invalid")
	}
	if len(claimMsg.Antecedents) != 1 || claimMsg.Antecedents[0] != createMsg.ID {
		t.Errorf("work:claim: antecedent must be create msg ID, got %v", claimMsg.Antecedents)
	}

	// Update
	updatePayload := mustMarshal(t, map[string]interface{}{
		"target":   createMsg.ID,
		"priority": "p0",
	})
	updateMsg := h.sendMsg(updatePayload, []string{"work:update"}, []string{createMsg.ID})
	if !updateMsg.VerifySignature() {
		t.Errorf("work:update message signature invalid")
	}

	// Status transition
	statusPayload := mustMarshal(t, map[string]interface{}{
		"target":       createMsg.ID,
		"to":           "waiting",
		"waiting_on":   "review",
		"waiting_type": "person",
	})
	statusMsg := h.sendMsg(statusPayload, []string{"work:status", "work:status:waiting"}, []string{createMsg.ID})
	if !statusMsg.VerifySignature() {
		t.Errorf("work:status message signature invalid")
	}
	if len(statusMsg.Tags) < 2 {
		t.Errorf("work:status: expected ≥2 tags, got %v", statusMsg.Tags)
	}

	// Delegate
	delegatePayload := mustMarshal(t, map[string]interface{}{
		"target": createMsg.ID,
		"to":     "carol@3dl.dev",
	})
	delegateMsg := h.sendMsg(delegatePayload, []string{"work:delegate", "work:by:carol@3dl.dev"}, []string{createMsg.ID})
	if !delegateMsg.VerifySignature() {
		t.Errorf("work:delegate message signature invalid")
	}
	foundByTag := false
	for _, tag := range delegateMsg.Tags {
		if tag == "work:by:carol@3dl.dev" {
			foundByTag = true
			break
		}
	}
	if !foundByTag {
		t.Errorf("work:delegate: missing work:by:<identity> tag, got %v", delegateMsg.Tags)
	}

	// Gate
	gatePayload := mustMarshal(t, map[string]interface{}{
		"target":    createMsg.ID,
		"gate_type": "review",
	})
	gateMsg := h.sendMsg(gatePayload, []string{"work:gate", "work:gate-type:review"}, []string{createMsg.ID})
	if !gateMsg.VerifySignature() {
		t.Errorf("work:gate message signature invalid")
	}

	// Gate-resolve
	resolvePayload := mustMarshal(t, map[string]interface{}{
		"target":     gateMsg.ID,
		"resolution": "approved",
	})
	resolveMsg := h.sendMsg(resolvePayload, []string{"work:gate-resolve", "work:gate-resolve:approved"}, []string{gateMsg.ID})
	if !resolveMsg.VerifySignature() {
		t.Errorf("work:gate-resolve message signature invalid")
	}
	if len(resolveMsg.Antecedents) != 1 || resolveMsg.Antecedents[0] != gateMsg.ID {
		t.Errorf("work:gate-resolve: antecedent must be gate msg ID, got %v", resolveMsg.Antecedents)
	}

	// Close
	closePayload := mustMarshal(t, map[string]interface{}{
		"target":     createMsg.ID,
		"resolution": "done",
	})
	closeMsg := h.sendMsg(closePayload, []string{"work:close", "work:resolution:done"}, []string{createMsg.ID})
	if !closeMsg.VerifySignature() {
		t.Errorf("work:close message signature invalid")
	}
	if len(closeMsg.Tags) < 2 {
		t.Errorf("work:close: expected ≥2 tags (work:close + resolution), got %v", closeMsg.Tags)
	}

	// Final state assertions.
	items := h.deriveState()
	item := h.getItem(items, "ready-conform-001")
	if item.Status != state.StatusDone {
		t.Errorf("final status=%s, want done", item.Status)
	}
}

// TestLifecycle_FSTransportRoundTrip verifies messages are readable from the
// filesystem transport after being written (round-trip test).
func TestLifecycle_FSTransportRoundTrip(t *testing.T) {
	h := newHarness(t)

	payload := mustMarshal(t, map[string]interface{}{
		"id":    "ready-fs-001",
		"title": "FS round-trip test",
		"type":  "task",
		"for":   "baron@3dl.dev",
	})
	sentMsg := h.sendMsg(payload, []string{"work:create"}, nil)

	// Read back from filesystem transport.
	fsMessages, err := h.tr.ListMessages(h.campfireID)
	if err != nil {
		t.Fatalf("tr.ListMessages(): %v", err)
	}
	if len(fsMessages) == 0 {
		t.Fatalf("expected at least 1 message from transport, got 0")
	}

	found := false
	for _, fsMsg := range fsMessages {
		if fsMsg.ID == sentMsg.ID {
			found = true
			// Verify signature from the transported message.
			if !fsMsg.VerifySignature() {
				t.Errorf("transported message %s: signature invalid after round-trip", fsMsg.ID)
			}
			break
		}
	}
	if !found {
		t.Errorf("sent message %s not found in transport", sentMsg.ID)
	}

	// Verify state derivation from store also works.
	items := h.deriveState()
	item := h.getItem(items, "ready-fs-001")
	if item.Title != "FS round-trip test" {
		t.Errorf("round-trip: title=%q, want 'FS round-trip test'", item.Title)
	}
}

// TestLifecycle_CancelledResolution verifies cancelled and failed resolutions.
func TestLifecycle_CancelledResolution(t *testing.T) {
	h := newHarness(t)

	// Cancelled item.
	p1 := mustMarshal(t, map[string]interface{}{
		"id":    "ready-cancel-001",
		"title": "To be cancelled",
		"type":  "task",
		"for":   "baron@3dl.dev",
	})
	create1 := h.sendMsg(p1, []string{"work:create"}, nil)
	close1 := mustMarshal(t, map[string]interface{}{
		"target":     create1.ID,
		"resolution": "cancelled",
		"reason":     "No longer needed",
	})
	closeMsg1 := h.sendMsg(close1, []string{"work:close", "work:resolution:cancelled"}, []string{create1.ID})
	verifyConformance(t, closeMsg1, []string{"work:close", "work:resolution:cancelled"}, []string{create1.ID})

	// Failed item.
	p2 := mustMarshal(t, map[string]interface{}{
		"id":    "ready-failed-001",
		"title": "To be failed",
		"type":  "task",
		"for":   "baron@3dl.dev",
	})
	create2 := h.sendMsg(p2, []string{"work:create"}, nil)
	close2 := mustMarshal(t, map[string]interface{}{
		"target":     create2.ID,
		"resolution": "failed",
		"reason":     "Unrecoverable error",
	})
	closeMsg2 := h.sendMsg(close2, []string{"work:close", "work:resolution:failed"}, []string{create2.ID})
	verifyConformance(t, closeMsg2, []string{"work:close", "work:resolution:failed"}, []string{create2.ID})

	items := h.deriveState()
	cancel := h.getItem(items, "ready-cancel-001")
	if cancel.Status != state.StatusCancelled {
		t.Errorf("cancelled item: status=%s, want cancelled", cancel.Status)
	}
	failed := h.getItem(items, "ready-failed-001")
	if failed.Status != state.StatusFailed {
		t.Errorf("failed item: status=%s, want failed", failed.Status)
	}
	if !state.IsTerminal(cancel) {
		t.Errorf("cancelled item should be terminal")
	}
	if !state.IsTerminal(failed) {
		t.Errorf("failed item should be terminal")
	}
}

// Ensure NowNano is available from store.
var _ = store.NowNano

// Ensure filepath is used.
var _ = os.Stat
var _ = filepath.Join
