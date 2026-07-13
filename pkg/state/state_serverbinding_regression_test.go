package state_test

// Regression tests for ready-3ef: serverBindingPayload type mismatch between
// conventionserver (writes int64 JSON numbers) and state (was reading string fields).
// JSON numbers do NOT unmarshal into Go string fields — they silently stay empty.
// parseTimestamp("") returns 0, causing every binding to be skipped as "not yet valid".
// Result: findActiveServerBinding always returned nil, breaking isOperationAuthorized
// for ALL consequential operations (close, delegate, gate-resolve) in campfires with
// a server-binding.

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"testing"
	"time"

	msgrec "github.com/campfire-net/ready/pkg/msgrec"
	"github.com/campfire-net/ready/pkg/state"
)

// TestRegression_ServerBindingNumericTimestamps verifies that a server-binding
// message with numeric (int64) valid_from/valid_until timestamps is correctly
// parsed and applied. This is the core regression test for ready-3ef.
//
// Before the fix, conventionserver wrote JSON numbers but state.serverBindingPayload
// declared string fields. The unmarshal silently produced empty strings, causing
// findActiveServerBinding to always return nil and all consequential operations to
// be rejected as unauthorized.
func TestRegression_ServerBindingNumericTimestamps(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	serverPubKeyHex := hex.EncodeToString(pubKey)

	ts := now()
	bindingTime := ts + 1000
	closeTime := ts + 2000
	fulfillmentTime := ts + 3000

	// valid_from is a Unix-seconds integer — exactly what conventionserver writes.
	// Before the fix, this would silently fail to unmarshal into a string field,
	// causing the binding to be skipped and the close to be rejected as unauthorized.
	validFromUnixSec := ts / int64(time.Second)

	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-reg-t01", "title": "Regression Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		// Server-binding message with NUMERIC valid_from (as conventionserver writes it).
		makeMsg("msg-binding-1", []string{"convention:server-binding"}, map[string]interface{}{
			"convention":    "work",
			"operation":     "close",
			"server_pubkey": serverPubKeyHex,
			"valid_from":    validFromUnixSec, // int64 — the canonical format from conventionserver
		}, nil, bindingTime),
		// Close operation requiring fulfillment
		makeMsg("msg-close-1", []string{"work:close"}, map[string]interface{}{
			"target":     "msg-create-1",
			"resolution": "done",
			"reason":     "Completed",
		}, []string{"msg-create-1"}, closeTime),
		// Fulfillment from the bound server
		makeMsg("msg-fulfillment-1", []string{"fulfills"}, map[string]interface{}{}, []string{"msg-close-1"}, fulfillmentTime),
	}
	// Set fulfillment sender to the bound server key.
	msgs[3].Sender = serverPubKeyHex

	items := state.Derive(testCampfire, msgs)
	item := items["ready-reg-t01"]
	if item == nil {
		t.Fatal("item not found in derived state")
	}

	// The binding should be found and the close authorized by the fulfillment.
	// Before the fix: status would be "inbox" (close rejected as unauthorized).
	// After the fix: status is "done" (binding found, fulfillment accepted).
	if item.Status != state.StatusDone {
		t.Errorf("regression ready-3ef: expected status done (server-binding with numeric timestamps authorizes close), got %q — binding was likely not parsed due to type mismatch", item.Status)
	}
}

// TestRegression_ServerBindingNumericTimestamps_WithExpiry verifies that a binding
// with a numeric valid_until is also correctly parsed, so expiry gating works.
func TestRegression_ServerBindingNumericTimestamps_WithExpiry(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	serverPubKeyHex := hex.EncodeToString(pubKey)

	// Set up time window: binding valid 10s window, operation inside window.
	baseNs := now()
	bindingValidFromSec := baseNs/int64(time.Second) - 5   // 5 seconds ago
	bindingValidUntilSec := baseNs/int64(time.Second) + 60  // expires in 60 seconds

	createTime := baseNs
	bindingTime := baseNs + 1*int64(time.Second)
	closeTime := baseNs + 2*int64(time.Second)
	fulfillmentTime := baseNs + 3*int64(time.Second)

	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-reg-t02", "title": "Expiry Regression Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, createTime),
		// Binding with NUMERIC valid_from and valid_until.
		makeMsg("msg-binding-1", []string{"convention:server-binding"}, map[string]interface{}{
			"convention":    "work",
			"operation":     "close",
			"server_pubkey": serverPubKeyHex,
			"valid_from":    bindingValidFromSec,   // int64
			"valid_until":   bindingValidUntilSec,  // int64
		}, nil, bindingTime),
		makeMsg("msg-close-1", []string{"work:close"}, map[string]interface{}{
			"target":     "msg-create-1",
			"resolution": "done",
			"reason":     "Completed within expiry window",
		}, []string{"msg-create-1"}, closeTime),
		makeMsg("msg-fulfillment-1", []string{"fulfills"}, map[string]interface{}{}, []string{"msg-close-1"}, fulfillmentTime),
	}
	msgs[3].Sender = serverPubKeyHex

	items := state.Derive(testCampfire, msgs)
	item := items["ready-reg-t02"]
	if item == nil {
		t.Fatal("item not found in derived state")
	}

	// Binding is active (op is within valid_from..valid_until window) and fulfillment matches.
	if item.Status != state.StatusDone {
		t.Errorf("regression ready-3ef: expected status done (binding with numeric valid_until active, fulfillment valid), got %q", item.Status)
	}
}

// TestRegression_ServerBindingRejectsWithoutFulfillment verifies that once a
// numeric-timestamp binding is properly parsed, operations WITHOUT fulfillment
// are still rejected (authorization gating is enforced, not bypassed).
func TestRegression_ServerBindingRejectsWithoutFulfillment(t *testing.T) {
	pubKey, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("failed to generate ed25519 key: %v", err)
	}
	serverPubKeyHex := hex.EncodeToString(pubKey)

	ts := now()
	bindingTime := ts + 1000
	closeTime := ts + 2000

	msgs := []msgrec.MessageRecord{
		makeMsg("msg-create-1", []string{"work:create"}, map[string]interface{}{
			"id": "ready-reg-t03", "title": "Rejection Regression Test", "type": "task",
			"for": "baron@3dl.dev", "priority": "p1",
		}, nil, ts),
		makeMsg("msg-binding-1", []string{"convention:server-binding"}, map[string]interface{}{
			"convention":    "work",
			"operation":     "close",
			"server_pubkey": serverPubKeyHex,
			"valid_from":    ts / int64(time.Second), // int64 numeric
		}, nil, bindingTime),
		// Close with no fulfillment — should be rejected.
		makeMsg("msg-close-1", []string{"work:close"}, map[string]interface{}{
			"target":     "msg-create-1",
			"resolution": "done",
			"reason":     "No fulfillment provided",
		}, []string{"msg-create-1"}, closeTime),
	}

	items := state.Derive(testCampfire, msgs)
	item := items["ready-reg-t03"]
	if item == nil {
		t.Fatal("item not found in derived state")
	}

	// Binding is found and active, but no fulfillment — close must be rejected.
	if item.Status != state.StatusInbox {
		t.Errorf("regression ready-3ef: expected status inbox (binding active, no fulfillment → rejected), got %q", item.Status)
	}
}
