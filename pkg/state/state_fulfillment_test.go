package state_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	msgrec "github.com/campfire-net/ready/pkg/msgrec"
	"github.com/campfire-net/ready/pkg/state"
)

// TestDerive_FulfillmentWithValidServerBinding verifies that a work:close
// message is accepted when accompanied by a valid fulfillment from the bound server.
func TestDerive_FulfillmentWithValidServerBinding(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	serverPubKeyHex := hex.EncodeToString(pubKey)
	ts := now()
	bindingTime := ts + 1000           // Server binding posted 1us after create
	closeTime := ts + 2000             // Close issued 1us after binding
	fulfillmentTime := ts + 3000       // Fulfillment posted 1us after close
	boundServerPubkey := serverPubKeyHex

	msgs := []msgrec.MessageRecord{
		// Create the item
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Post the server-binding declaration (signed by campfire owner)
		makeMsg("msg-binding-1", []string{"convention:server-binding"}, map[string]interface{}{
			"convention":   "work",
			"operation":    "close",
			"server_pubkey": boundServerPubkey,
			"valid_from": ts/int64(time.Second), // Unix seconds
		}, nil, bindingTime),
		// Issue the close operation
		makeMsg("msg-close-1", []string{"work:close"}, map[string]interface{}{
			"target":     "msg-create-1",
			"resolution": "done",
			"reason":     "Completed",
		}, []string{"msg-create-1"}, closeTime),
		// Post fulfillment from the bound server
		makeMsg("msg-fulfillment-1", []string{"fulfills"}, map[string]interface{}{}, []string{"msg-close-1"}, fulfillmentTime),
	}
	// Set the fulfillment sender to the bound server pubkey
	msgs[3].Sender = boundServerPubkey

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Close should be accepted because there's a valid fulfillment from the bound server.
	if item.Status != state.StatusDone {
		t.Errorf("expected status done (operation accepted), got %q", item.Status)
	}
}

// TestDerive_FulfillmentRejectedWrongSender verifies that a work:close
// message is rejected when the fulfillment is from a sender that doesn't match
// the server-binding declaration.
func TestDerive_FulfillmentRejectedWrongSender(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	serverPubKeyHex := hex.EncodeToString(pubKey)
	wrongPubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	ts := now()
	bindingTime := ts + 1000
	closeTime := ts + 2000
	fulfillmentTime := ts + 3000
	boundServerPubkey := serverPubKeyHex
	wrongServerPubkey := hex.EncodeToString(wrongPubKey)

	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Server binding declares server-key-hex as authorized
		makeMsg("msg-binding-1", []string{"convention:server-binding"}, map[string]interface{}{
			"convention":   "work",
			"operation":    "close",
			"server_pubkey": boundServerPubkey,
			"valid_from": ts/int64(time.Second),
		}, nil, bindingTime),
		// Close issued
		makeMsg("msg-close-1", []string{"work:close"}, map[string]interface{}{
			"target":     "msg-create-1",
			"resolution": "done",
			"reason":     "Completed",
		}, []string{"msg-create-1"}, closeTime),
		// Fulfillment from wrong server
		makeMsg("msg-fulfillment-1", []string{"fulfills"}, map[string]interface{}{}, []string{"msg-close-1"}, fulfillmentTime),
	}
	msgs[3].Sender = wrongServerPubkey

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Close should be rejected because fulfillment is from wrong sender.
	if item.Status != state.StatusInbox {
		t.Errorf("expected status inbox (operation rejected), got %q", item.Status)
	}
}

// TestDerive_BypassModeNoServerBinding verifies that consequential operations
// are accepted when no server-binding is present (Wave 1 bypass mode).
func TestDerive_BypassModeNoServerBinding(t *testing.T) {
	ts := now()
	closeTime := ts + 1000

	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Close issued WITHOUT any server-binding declared
		makeMsg("msg-close-1", []string{"work:close"}, map[string]interface{}{
			"target":     "msg-create-1",
			"resolution": "done",
			"reason":     "Completed",
		}, []string{"msg-create-1"}, closeTime),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Close should be accepted (bypass mode) even without fulfillment.
	if item.Status != state.StatusDone {
		t.Errorf("expected status done (bypass mode accepted), got %q", item.Status)
	}
}

// TestDerive_PreBindingItemsAccepted verifies that operations issued before
// a server-binding is declared are implicitly authorized.
func TestDerive_PreBindingItemsAccepted(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	serverPubKeyHex := hex.EncodeToString(pubKey)
	// Use a fixed base time so second-level granularity is controlled.
	// T1 (close) is 10 seconds before T2 (binding valid_from).
	baseNs := now()
	createTime := baseNs
	closeTime := baseNs + 5*int64(time.Second)  // T1: close at base+5s (nanoseconds)
	bindingTime := baseNs + 15*int64(time.Second) // binding message posted at base+15s
	// valid_from is T2 = base+10s in Unix seconds — after closeTime but before bindingTime post
	bindingValidFrom := (baseNs + 10*int64(time.Second)) / int64(time.Second) // Unix seconds

	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, createTime),
		// Close issued at T1, before binding valid_from T2
		makeMsg("msg-close-1", []string{"work:close"}, map[string]interface{}{
			"target":     "msg-create-1",
			"resolution": "done",
			"reason":     "Completed",
		}, []string{"msg-create-1"}, closeTime),
		// Binding posted later with valid_from=T2 > T1
		makeMsg("msg-binding-1", []string{"convention:server-binding"}, map[string]interface{}{
			"convention":   "work",
			"operation":    "close",
			"server_pubkey": serverPubKeyHex,
			"valid_from": bindingValidFrom,
		}, nil, bindingTime),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Close should be accepted as pre-binding (issued before binding valid_from).
	if item.Status != state.StatusDone {
		t.Errorf("expected status done (pre-binding accepted), got %q", item.Status)
	}
}

// TestDerive_DelegateWithServerBinding verifies that work:delegate also respects
// the fulfillment gating rules (it's a consequential operation).
func TestDerive_DelegateWithServerBinding(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	serverPubKeyHex := hex.EncodeToString(pubKey)
	ts := now()
	bindingTime := ts + 1000
	delegateTime := ts + 2000
	fulfillmentTime := ts + 3000
	boundServerPubkey := serverPubKeyHex

	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-binding-1", []string{"convention:server-binding"}, map[string]interface{}{
			"convention":   "work",
			"operation":    "delegate",
			"server_pubkey": boundServerPubkey,
			"valid_from": ts/int64(time.Second),
		}, nil, bindingTime),
		// Delegate operation
		makeMsg("msg-delegate-1", []string{"work:delegate"}, map[string]interface{}{
			"target": "msg-create-1",
			"to":     "alice@3dl.dev",
			"reason": "Alice is better for this",
		}, []string{"msg-create-1"}, delegateTime),
		// Fulfillment from bound server
		makeMsg("msg-fulfillment-1", []string{"fulfills"}, map[string]interface{}{}, []string{"msg-delegate-1"}, fulfillmentTime),
	}
	msgs[3].Sender = boundServerPubkey

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Delegate should be accepted due to valid fulfillment.
	if item.By != "alice@3dl.dev" {
		t.Errorf("expected by=alice@3dl.dev (operation accepted), got %q", item.By)
	}
}

// TestDerive_GateResolveWithServerBinding verifies that work:gate-resolve
// respects fulfillment gating.
func TestDerive_GateResolveWithServerBinding(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	serverPubKeyHex := hex.EncodeToString(pubKey)
	ts := now()
	gateTime := ts + 1000
	bindingTime := ts + 2000
	resolveTime := ts + 3000
	fulfillmentTime := ts + 4000
	boundServerPubkey := serverPubKeyHex

	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-t01", "title": "Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Issue a gate
		makeMsg("msg-gate-1", []string{"work:gate"}, map[string]interface{}{
			"target":      "msg-create-1",
			"gate_type":   "review",
			"description": "Needs review",
		}, []string{"msg-create-1"}, gateTime),
		// Post server binding for gate-resolve
		makeMsg("msg-binding-1", []string{"convention:server-binding"}, map[string]interface{}{
			"convention":   "work",
			"operation":    "gate-resolve",
			"server_pubkey": boundServerPubkey,
			"valid_from": (ts+2000)/int64(time.Second),
		}, nil, bindingTime),
		// Resolve the gate
		makeMsg("msg-resolve-1", []string{"work:gate-resolve"}, map[string]interface{}{
			"target":     "msg-gate-1",
			"resolution": "approved",
		}, []string{"msg-gate-1"}, resolveTime),
		// Fulfillment from bound server
		makeMsg("msg-fulfillment-1", []string{"fulfills"}, map[string]interface{}{}, []string{"msg-resolve-1"}, fulfillmentTime),
	}
	msgs[4].Sender = boundServerPubkey

	items := state.Derive(testCampfire, msgs)
	item := items["ready-t01"]
	if item == nil {
		t.Fatal("item not found")
	}
	// Gate-resolve should be accepted, transitioning item to active.
	if item.Status != state.StatusActive {
		t.Errorf("expected status active (gate-resolve accepted), got %q", item.Status)
	}
}
