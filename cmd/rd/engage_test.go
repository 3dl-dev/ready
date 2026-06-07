package main

// engage_test.go — integration test for engageCmd.RunE label-warning path.
//
// Verifies the gap identified in the ready-ef7 veracity review: when engageCmd
// engages a playbook whose template item carries a label atom that is NOT in the
// target campfire's label registry, the command emits a warning line to stderr
// naming the absent label.
//
// This tests the UX warning path only (engage.go lines 107-115). The derive-time
// drop is proven separately in pkg/state tests (LabelWarnings).

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/store"
)

// testCampfireID is a 64-hex campfire ID used across engage tests.
const testEngageCampfireID = "aabbccdd00112233aabbccdd00112233aabbccdd00112233aabbccdd00112233"

// TestEngageCmd_LabelNotInRegistry_WarnsOnStderr verifies that engageCmd.RunE
// emits a warning to stderr when a playbook item's label is absent from the
// target campfire's label registry. The warning must name the absent label atom.
//
// Setup:
//  1. Fresh rdHome (temp dir) — requireClient() initialises identity + store there.
//  2. Campfire membership with filesystem transport (no network calls).
//  3. work:label-define message for "known-label" (populates registry).
//  4. work:playbook-create message: one item carrying "unknown-label" (not defined).
//  5. Project dir with .campfire/root pointing at the test campfire.
//
// Assertions:
//   - stderr contains `warning: label "unknown-label" on item …`
//   - RunE either succeeds or returns an error unrelated to the warning
//     (engage may fail to send if executor rejects; the warning must fire first)
func TestEngageCmd_LabelNotInRegistry_WarnsOnStderr(t *testing.T) {
	// ── 1. Isolated CF home ─────────────────────────────────────────────────
	cfHome := t.TempDir()

	// Override the global rdHome variable so CFHome() and openStore() use our
	// temp directory rather than the real ~/.cf.
	origRDHome := rdHome
	rdHome = cfHome
	t.Cleanup(func() { rdHome = origRDHome })

	// Reset the cached protocol client so requireClient() re-initialises from
	// our temp cfHome (creating identity.json + store.db there).
	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	// Initialise identity + store in cfHome.
	if _, err := requireClient(); err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	// ── 2. Campfire membership with filesystem transport ─────────────────────
	transportDir := t.TempDir()
	s, err := store.Open(store.StorePath(cfHome))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	if addErr := s.AddMembership(store.Membership{
		CampfireID:   testEngageCampfireID,
		TransportDir: transportDir,
		JoinProtocol: "invite-only",
		Role:         "full",
		JoinedAt:     time.Now().Unix(),
	}); addErr != nil {
		t.Fatalf("AddMembership: %v", addErr)
	}

	// ── 3. work:label-define for "known-label" ────────────────────────────────
	// Writing this message into the store populates dr.LabelRegistry() at engage
	// time, so "known-label" is registered but "unknown-label" is not.
	labelPayload, _ := json.Marshal(map[string]string{"label": "known-label"})
	msgID := "msg-labeldef-0001"
	now := time.Now().UnixNano()
	if _, addErr := s.AddMessage(store.MessageRecord{
		ID:          msgID,
		CampfireID:  testEngageCampfireID,
		Sender:      "testkey",
		Payload:     labelPayload,
		Tags:        []string{"work:label-define"},
		Antecedents: nil,
		Timestamp:   now,
		Signature:   []byte("fakesig-labeldef"),
		ReceivedAt:  now,
	}); addErr != nil {
		t.Fatalf("AddMessage(label-define): %v", addErr)
	}

	// ── 4. work:playbook-create: one item with "unknown-label" ────────────────
	itemsJSON := `[{"title":"Test task","type":"task","priority":"p2","labels":["unknown-label"]}]`
	pbPayload, _ := json.Marshal(map[string]interface{}{
		"id":    "test-pb-warn",
		"title": "Test Playbook Warning",
		"items": json.RawMessage(itemsJSON),
	})
	pbMsgID := "msg-pbcreate-0001"
	now2 := time.Now().UnixNano() + int64(time.Millisecond)
	if _, addErr := s.AddMessage(store.MessageRecord{
		ID:          pbMsgID,
		CampfireID:  testEngageCampfireID,
		Sender:      "testkey",
		Payload:     pbPayload,
		Tags:        []string{"work:playbook-create"},
		Antecedents: nil,
		Timestamp:   now2,
		Signature:   []byte("fakesig-pbcreate"),
		ReceivedAt:  now2,
	}); addErr != nil {
		t.Fatalf("AddMessage(playbook-create): %v", addErr)
	}
	s.Close()

	// ── 5. Project dir with .campfire/root ────────────────────────────────────
	projectDir := t.TempDir()
	campfireDir := filepath.Join(projectDir, ".campfire")
	if err := os.MkdirAll(campfireDir, 0755); err != nil {
		t.Fatalf("mkdir .campfire: %v", err)
	}
	if err := os.WriteFile(filepath.Join(campfireDir, "root"), []byte(testEngageCampfireID), 0600); err != nil {
		t.Fatalf("write .campfire/root: %v", err)
	}
	readyDir := filepath.Join(projectDir, ".ready")
	if err := os.MkdirAll(readyDir, 0700); err != nil {
		t.Fatalf("mkdir .ready: %v", err)
	}

	// Chdir to project dir so projectRoot() walks up and finds .campfire/root.
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd: %v", err)
	}
	if err := os.Chdir(projectDir); err != nil {
		t.Fatalf("Chdir: %v", err)
	}
	t.Cleanup(func() { os.Chdir(origWD) })

	// ── 6. Redirect os.Stderr to capture warning output ──────────────────────
	origStderr := os.Stderr
	r, w, pipeErr := os.Pipe()
	if pipeErr != nil {
		t.Fatalf("os.Pipe: %v", pipeErr)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	// ── 7. Run engageCmd.RunE ─────────────────────────────────────────────────
	// Reset cobra flag state so re-use across tests is safe.
	if err := engageCmd.Flags().Set("project", "testproject"); err != nil {
		t.Fatalf("set --project: %v", err)
	}
	if err := engageCmd.Flags().Set("for", "test@example.com"); err != nil {
		t.Fatalf("set --for: %v", err)
	}

	// RunE may return an error if the executor cannot send (e.g. transport
	// rejects the message). That is acceptable — the warning fires before any
	// send attempt, so we only need to inspect stderr.
	_ = engageCmd.RunE(engageCmd, []string{"test-pb-warn"})

	// Flush and read stderr.
	w.Close()
	var buf bytes.Buffer
	io.Copy(&buf, r)
	os.Stderr = origStderr

	stderrOutput := buf.String()

	// ── 8. Assert warning line is present and names the absent label ──────────
	if !strings.Contains(stderrOutput, "unknown-label") {
		t.Errorf("expected stderr to contain warning for absent label %q, got:\n%s",
			"unknown-label", stderrOutput)
	}
	if !strings.Contains(stderrOutput, "warning:") {
		t.Errorf("expected stderr to contain 'warning:', got:\n%s", stderrOutput)
	}
	// Full expected fragment: warning: label "unknown-label" on item … is not in the target campfire registry
	if !strings.Contains(stderrOutput, "not in the target campfire registry") {
		t.Errorf("expected stderr to mention registry, got:\n%s", stderrOutput)
	}
}
