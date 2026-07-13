package e2e_test

// gate_test.go — end-to-end tests for the gate/approve/reject escalation cycle.
//
// ready-6ef cutover: `rd gate`/`approve`/`reject` were re-headed onto the
// nostr-native secp256k1 signing path in S-write. The full-cycle tests below are
// MIGRATED to the nostr-native default path (mirroring init_test.go's
// _NostrNative_ThenCreateClaimClose): a single secp256k1 self-identity created by
// the default `rd init` gates and resolves the gate, and every transition is read
// back through the nostr projection. The default path provisions NO .cf and NO
// .campfire — asserted at the end of each test.
//
// TestE2E_Gate_BadInputs stays on the campfire harness (NewEnv) — it exercises the
// still-present campfire error paths and is deleted with the campfire code in I7.

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestE2E_Gate_ApproveFullCycle verifies the full gate escalation cycle with
// approval on the nostr-native default path:
//   - Owner (secp256k1 self) creates an item and gates it (rd gate <id> --gate-type design)
//   - rd gates lists the pending gate
//   - rd approve <id> resolves the gate
//   - Item transitions to active after approval; gate cleared
//   - NO .cf / .campfire is ever provisioned
func TestE2E_Gate_ApproveFullCycle(t *testing.T) {
	projectDir := t.TempDir()
	home := t.TempDir()
	rdHome := t.TempDir()
	cfHome := filepath.Join(t.TempDir(), "cf") // must NOT be created by the default path

	if _, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--name", "gate-approve"); code != 0 {
		t.Fatalf("rd init failed (exit %d): %s", code, stderr)
	}

	// Step 1: create an item.
	createOut, createStderr, createCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "create",
		"--title", "Gate approve test item",
		"--priority", "p1",
		"--type", "task",
		"--json",
	)
	if createCode != 0 {
		t.Fatalf("rd create failed (exit %d): %s", createCode, createStderr)
	}
	var item Item
	if err := json.Unmarshal([]byte(createOut), &item); err != nil {
		t.Fatalf("parse create JSON: %v\noutput: %s", err, createOut)
	}
	if item.ID == "" {
		t.Fatal("rd create returned empty ID")
	}

	// Step 2: gate the item.
	gateOut, gateStderr, gateCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "gate", item.ID,
		"--gate-type", "design",
		"--description", "Confirm API shape before implementing",
		"--json",
	)
	if gateCode != 0 {
		t.Fatalf("rd gate failed (exit %d): %s\nstdout: %s", gateCode, gateStderr, gateOut)
	}
	var gateResult struct {
		ID       string `json:"id"`
		MsgID    string `json:"msg_id"`
		GateType string `json:"gate_type"`
	}
	if err := json.Unmarshal([]byte(gateOut), &gateResult); err != nil {
		t.Fatalf("parse gate JSON: %v\noutput: %s", err, gateOut)
	}
	if gateResult.ID != item.ID {
		t.Errorf("gate result id=%q, want %q", gateResult.ID, item.ID)
	}
	if gateResult.GateType != "design" {
		t.Errorf("gate_type=%q, want design", gateResult.GateType)
	}
	if gateResult.MsgID == "" {
		t.Error("gate msg_id should be non-empty")
	}

	// Step 3: item is now waiting with waiting_type=gate.
	showOut, showStderr, showCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "show", item.ID, "--json")
	if showCode != 0 {
		t.Fatalf("rd show after gate (exit %d): %s", showCode, showStderr)
	}
	var gatedItem Item
	if err := json.Unmarshal([]byte(showOut), &gatedItem); err != nil {
		t.Fatalf("parse show JSON after gate: %v\noutput: %s", err, showOut)
	}
	if gatedItem.Status != "waiting" {
		t.Errorf("after gate: status=%q, want waiting", gatedItem.Status)
	}
	if gatedItem.WaitingType != "gate" {
		t.Errorf("after gate: waiting_type=%q, want gate", gatedItem.WaitingType)
	}
	if gatedItem.GateMsgID == "" {
		t.Error("after gate: gate_msg_id should be non-empty")
	}

	// Step 4: rd gates lists the pending gate.
	gatesOut, gatesStderr, gatesCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "gates", "--json")
	if gatesCode != 0 {
		t.Fatalf("rd gates failed (exit %d): %s", gatesCode, gatesStderr)
	}
	var gateItems []Item
	if err := json.Unmarshal([]byte(gatesOut), &gateItems); err != nil {
		t.Fatalf("parse gates JSON: %v\noutput: %s", err, gatesOut)
	}
	gateFound := false
	for _, gi := range gateItems {
		if gi.ID == item.ID {
			gateFound = true
			break
		}
	}
	if !gateFound {
		t.Errorf("item %s should appear in rd gates output after gating\ngates: %s", item.ID, gatesOut)
	}

	// Step 5: approve the gate.
	approveOut, approveStderr, approveCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "approve", item.ID,
		"--reason", "Approved, proceed with design approach",
		"--json",
	)
	if approveCode != 0 {
		t.Fatalf("rd approve failed (exit %d): %s\nstdout: %s", approveCode, approveStderr, approveOut)
	}
	var approveResult struct {
		ID         string `json:"id"`
		Resolution string `json:"resolution"`
	}
	if err := json.Unmarshal([]byte(approveOut), &approveResult); err != nil {
		t.Fatalf("parse approve JSON: %v\noutput: %s", err, approveOut)
	}
	if approveResult.ID != item.ID {
		t.Errorf("approve result id=%q, want %q", approveResult.ID, item.ID)
	}
	if approveResult.Resolution != "approved" {
		t.Errorf("approve resolution=%q, want approved", approveResult.Resolution)
	}

	// Step 6: item is now active after approval; gate cleared.
	showOut2, showStderr2, showCode2 := runIsolatedRd(projectDir, home, rdHome, cfHome, "show", item.ID, "--json")
	if showCode2 != 0 {
		t.Fatalf("rd show after approve (exit %d): %s", showCode2, showStderr2)
	}
	var approvedItem Item
	if err := json.Unmarshal([]byte(showOut2), &approvedItem); err != nil {
		t.Fatalf("parse show JSON after approve: %v\noutput: %s", err, showOut2)
	}
	if approvedItem.Status != "active" {
		t.Errorf("after approve: status=%q, want active", approvedItem.Status)
	}
	if approvedItem.WaitingType != "" {
		t.Errorf("after approve: waiting_type=%q, want empty", approvedItem.WaitingType)
	}
	if approvedItem.GateMsgID != "" {
		t.Errorf("after approve: gate_msg_id=%q, want empty (gate resolved)", approvedItem.GateMsgID)
	}

	// Step 7: rd gates should no longer list the item.
	gatesOut2, _, _ := runIsolatedRd(projectDir, home, rdHome, cfHome, "gates", "--json")
	if strings.Contains(gatesOut2, item.ID) {
		t.Errorf("item %s should not appear in rd gates after approval", item.ID)
	}

	assertNoDotCfCampfire(t, projectDir, cfHome)
}

// TestE2E_Gate_RejectFullCycle verifies the full gate escalation cycle with
// rejection on the nostr-native default path:
//   - Owner (secp256k1 self) creates an item, gates it (rd gate <id> --gate-type scope)
//   - rd reject <id> --reason ... records the rejection
//   - Item remains in waiting status after rejection (gate not cleared)
//   - NO .cf / .campfire is ever provisioned
func TestE2E_Gate_RejectFullCycle(t *testing.T) {
	projectDir := t.TempDir()
	home := t.TempDir()
	rdHome := t.TempDir()
	cfHome := filepath.Join(t.TempDir(), "cf")

	if _, stderr, code := runIsolatedRd(projectDir, home, rdHome, cfHome, "init", "--name", "gate-reject"); code != 0 {
		t.Fatalf("rd init failed (exit %d): %s", code, stderr)
	}

	// Step 1: create an item.
	createOut, createStderr, createCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "create",
		"--title", "Gate reject test item",
		"--priority", "p2",
		"--type", "task",
		"--json",
	)
	if createCode != 0 {
		t.Fatalf("rd create failed (exit %d): %s", createCode, createStderr)
	}
	var item Item
	if err := json.Unmarshal([]byte(createOut), &item); err != nil {
		t.Fatalf("parse create JSON: %v\noutput: %s", err, createOut)
	}
	if item.ID == "" {
		t.Fatal("rd create returned empty ID")
	}

	// Step 2: gate the item.
	_, gateStderr, gateCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "gate", item.ID,
		"--gate-type", "scope",
		"--description", "Scope too broad, needs design review",
	)
	if gateCode != 0 {
		t.Fatalf("rd gate failed (exit %d): %s", gateCode, gateStderr)
	}

	// Step 3: item is waiting with waiting_type=gate.
	showOut, showStderr, showCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "show", item.ID, "--json")
	if showCode != 0 {
		t.Fatalf("rd show after gate (exit %d): %s", showCode, showStderr)
	}
	var gatedItem Item
	if err := json.Unmarshal([]byte(showOut), &gatedItem); err != nil {
		t.Fatalf("parse show JSON after gate: %v\noutput: %s", err, showOut)
	}
	if gatedItem.Status != "waiting" {
		t.Errorf("after gate: status=%q, want waiting", gatedItem.Status)
	}
	if gatedItem.WaitingType != "gate" {
		t.Errorf("after gate: waiting_type=%q, want gate", gatedItem.WaitingType)
	}
	if gatedItem.GateMsgID == "" {
		t.Error("after gate: gate_msg_id should be non-empty")
	}

	// Step 4: rd gates lists the pending gate.
	gatesOut, gatesStderr, gatesCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "gates", "--json")
	if gatesCode != 0 {
		t.Fatalf("rd gates failed (exit %d): %s", gatesCode, gatesStderr)
	}
	var gateItems []Item
	if err := json.Unmarshal([]byte(gatesOut), &gateItems); err != nil {
		t.Fatalf("parse gates JSON: %v\noutput: %s", err, gatesOut)
	}
	gateFound := false
	for _, gi := range gateItems {
		if gi.ID == item.ID {
			gateFound = true
			break
		}
	}
	if !gateFound {
		t.Errorf("item %s should appear in rd gates output after gating\ngates: %s", item.ID, gatesOut)
	}

	// Step 5: reject the gate.
	rejectOut, rejectStderr, rejectCode := runIsolatedRd(projectDir, home, rdHome, cfHome, "reject", item.ID,
		"--reason", "Scope too broad, split into smaller pieces first",
		"--json",
	)
	if rejectCode != 0 {
		t.Fatalf("rd reject failed (exit %d): %s\nstdout: %s", rejectCode, rejectStderr, rejectOut)
	}
	var rejectResult struct {
		ID         string `json:"id"`
		Resolution string `json:"resolution"`
	}
	if err := json.Unmarshal([]byte(rejectOut), &rejectResult); err != nil {
		t.Fatalf("parse reject JSON: %v\noutput: %s", err, rejectOut)
	}
	if rejectResult.ID != item.ID {
		t.Errorf("reject result id=%q, want %q", rejectResult.ID, item.ID)
	}
	if rejectResult.Resolution != "rejected" {
		t.Errorf("reject resolution=%q, want rejected", rejectResult.Resolution)
	}

	// Step 6: item remains waiting after rejection; gate not cleared.
	showOut2, showStderr2, showCode2 := runIsolatedRd(projectDir, home, rdHome, cfHome, "show", item.ID, "--json")
	if showCode2 != 0 {
		t.Fatalf("rd show after reject (exit %d): %s", showCode2, showStderr2)
	}
	var rejectedItem Item
	if err := json.Unmarshal([]byte(showOut2), &rejectedItem); err != nil {
		t.Fatalf("parse show JSON after reject: %v\noutput: %s", err, showOut2)
	}
	if rejectedItem.Status != "waiting" {
		t.Errorf("after reject: status=%q, want waiting (rejection does not close the gate)", rejectedItem.Status)
	}
	if rejectedItem.GateMsgID == "" {
		t.Error("after reject: gate_msg_id should still be set (gate unresolved until approved)")
	}

	// Step 7: rd gates should still list the item (rejection keeps it waiting).
	gatesOut2, gatesStderr2, gatesCode2 := runIsolatedRd(projectDir, home, rdHome, cfHome, "gates", "--json")
	if gatesCode2 != 0 {
		t.Fatalf("rd gates after reject (exit %d): %s", gatesCode2, gatesStderr2)
	}
	var gateItems2 []Item
	if err := json.Unmarshal([]byte(gatesOut2), &gateItems2); err != nil {
		t.Fatalf("parse gates JSON after reject: %v\noutput: %s", err, gatesOut2)
	}
	stillFound := false
	for _, gi := range gateItems2 {
		if gi.ID == item.ID {
			stillFound = true
			break
		}
	}
	if !stillFound {
		t.Errorf("item %s should still appear in rd gates after rejection", item.ID)
	}

	assertNoDotCfCampfire(t, projectDir, cfHome)
}

// TestE2E_Gate_BadInputs verifies that gate/approve/reject fail cleanly on bad inputs:
//   - rd approve on an item with no pending gate → non-zero exit, clear error
//   - rd reject on an item with no pending gate → non-zero exit, clear error
//   - rd gate with no --gate-type → non-zero exit, clear error
//
// Uses NewEnv (campfire already configured via cf create) — no rd init needed.
func TestE2E_Gate_BadInputs(t *testing.T) {
	e := NewEnv(t)

	// Create an item (not gated) — NewEnv already has campfire configured.
	var item Item
	if err := e.RdJSON(&item, "create",
		"--title", "Non-gated item",
		"--priority", "p1",
		"--type", "task",
	); err != nil {
		t.Fatalf("rd create: %v", err)
	}

	// rd approve on non-gated item → error.
	approveStderr := e.RdMustFail("approve", item.ID)
	if !strings.Contains(approveStderr, "no pending gate") {
		t.Errorf("approve non-gated item: error should mention 'no pending gate', got: %q", approveStderr)
	}

	// rd reject on non-gated item → error.
	rejectStderr := e.RdMustFail("reject", item.ID)
	if !strings.Contains(rejectStderr, "no pending gate") {
		t.Errorf("reject non-gated item: error should mention 'no pending gate', got: %q", rejectStderr)
	}

	// rd gate without --gate-type → error.
	gateMissingTypeStderr := e.RdMustFail("gate", item.ID)
	if !strings.Contains(gateMissingTypeStderr, "gate-type") {
		t.Errorf("gate without --gate-type: error should mention 'gate-type', got: %q", gateMissingTypeStderr)
	}
}

// assertNoDotCfCampfire asserts the nostr-native default path provisioned neither a
// .cf identity home nor a .campfire pointer — the ready-6ef "no .cf on the default
// path" invariant, verified at the e2e layer.
func assertNoDotCfCampfire(t *testing.T, projectDir, cfHome string) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(cfHome, "identity.json")); err == nil {
		t.Errorf("default nostr path must NOT provision .cf/identity.json at %s", cfHome)
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".campfire")); err == nil {
		t.Errorf("default nostr path must NOT create .campfire/ in the project dir")
	}
	if _, err := os.Stat(filepath.Join(projectDir, ".cf")); err == nil {
		t.Errorf("default nostr path must NOT create .cf/ in the project dir")
	}
}
