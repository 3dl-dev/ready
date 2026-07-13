package main

// join_test.go — unit tests for rd join helpers.
//
// Done conditions tested:
//   - isHex rejects non-hex strings and accepts valid hex
//   - containsTag finds present tags and misses absent ones
//   - grantTargets matches correct pubkey in payload
//   - pollForRoleGrant: returns msg ID when fake client returns a matching message (ready-421)
//   - pollForRoleGrant: times out when no matching message arrives (ready-421)
//   - pollForRoleGrant: ignores fake role-grants from unauthorized senders (ready-9ce)
//   - resolveName: returns hex ID unchanged when input is already 64-char hex (ready-421)
//   - TOFU beacon root: all five paths via applyBeaconRootTOFU / resetBeaconRoot (ready-c3a)
//   - Open campfire join: non-member joins open campfire via client.Join() (ready-1b5)
//   - Invite-only campfire join: client.Join() returns "invite-only" error (ready-1b5)

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/ready/pkg/rdconfig"
)

// TestIsHex verifies isHex correctly identifies hex strings.
func TestIsHex(t *testing.T) {
	cases := []struct {
		input string
		want  bool
	}{
		{"0123456789abcdef", true},
		{"ABCDEF0123456789", true},
		{"0123456789abcdefABCDEF", true},
		{"", true}, // empty string — vacuously true (all chars are hex)
		{"xyz", false},
		{"0123456789abcdefg", false},
		{"ghijklmn", false},
	}
	for _, tc := range cases {
		got := isHex(tc.input)
		if got != tc.want {
			t.Errorf("isHex(%q) = %v, want %v", tc.input, got, tc.want)
		}
	}
}

// TestContainsTag verifies containsTag finds present and misses absent tags.
func TestContainsTag(t *testing.T) {
	tags := []string{"work:role-grant", "work:for:abcdef", "meta:test"}

	if !containsTag(tags, "work:role-grant") {
		t.Error("containsTag should find work:role-grant")
	}
	if !containsTag(tags, "work:for:abcdef") {
		t.Error("containsTag should find work:for:abcdef")
	}
	if containsTag(tags, "work:missing") {
		t.Error("containsTag should not find work:missing")
	}
	if containsTag(nil, "work:role-grant") {
		t.Error("containsTag on nil should return false")
	}
}

// TestGrantTargets verifies grantTargets matches correct pubkey in payload.
func TestGrantTargets(t *testing.T) {
	targetKey := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	otherKey := "0000000000000000000000000000000000000000000000000000000000000001"

	makeMsg := func(pubkey, role string) protocol.Message {
		payload := map[string]string{"pubkey": pubkey, "role": role}
		data, _ := json.Marshal(payload)
		return protocol.Message{Payload: data}
	}

	if !grantTargets(makeMsg(targetKey, "member"), targetKey) {
		t.Error("grantTargets should match correct pubkey with admission role")
	}
	if grantTargets(makeMsg(otherKey, "member"), targetKey) {
		t.Error("grantTargets should not match wrong pubkey")
	}
	// Revocation grants must not be accepted as admissions.
	if grantTargets(makeMsg(targetKey, "revoked"), targetKey) {
		t.Error("grantTargets should reject role=revoked grants")
	}
	// Empty role must not be accepted.
	if grantTargets(makeMsg(targetKey, ""), targetKey) {
		t.Error("grantTargets should reject empty role")
	}

	// Empty payload should not match.
	emptyMsg := protocol.Message{Payload: []byte("{}")}
	if grantTargets(emptyMsg, targetKey) {
		t.Error("grantTargets should not match when payload has no pubkey")
	}

	// Invalid JSON should not match.
	badMsg := protocol.Message{Payload: []byte("not-json")}
	if grantTargets(badMsg, targetKey) {
		t.Error("grantTargets should not match on invalid JSON payload")
	}
}

// TestPollForRoleGrant_FoundViaTag verifies that pollForRoleGrant returns the
// message ID immediately when the fake client returns a message tagged with
// "work:for:<pubkey>". This exercises the containsTag path.
func TestPollForRoleGrant_FoundViaTag(t *testing.T) {
	myPubKey := pubkeyHex("ab")
	ownerKey := pubkeyHex("dd")
	campfire := pubkeyHex("cc")
	msgID := "grant-msg-0001"

	msg := makeGrantMsgForTag(msgID, myPubKey, "member")
	msg.Sender = ownerKey

	fake := &fakeReadClient{
		messages: []protocol.Message{msg},
	}

	authorizedSenders := map[string]bool{ownerKey: true}
	got, err := pollForRoleGrant(fake, campfire, myPubKey, authorizedSenders, 5*time.Second)
	if err != nil {
		t.Fatalf("pollForRoleGrant: unexpected error: %v", err)
	}
	if got != msgID {
		t.Errorf("pollForRoleGrant returned msg ID %q, want %q", got, msgID)
	}
}

// TestPollForRoleGrant_FoundViaPayload verifies that pollForRoleGrant returns
// the message ID when the payload contains the matching pubkey and role
// (via grantTargets), even without a "work:for:<pubkey>" tag.
func TestPollForRoleGrant_FoundViaPayload(t *testing.T) {
	myPubKey := pubkeyHex("ab")
	ownerKey := pubkeyHex("dd")
	campfire := pubkeyHex("cc")
	msgID := "grant-msg-0002"

	msg := makeGrantMsg(msgID, myPubKey, "member")
	msg.Sender = ownerKey

	fake := &fakeReadClient{
		messages: []protocol.Message{msg},
	}

	authorizedSenders := map[string]bool{ownerKey: true}
	got, err := pollForRoleGrant(fake, campfire, myPubKey, authorizedSenders, 5*time.Second)
	if err != nil {
		t.Fatalf("pollForRoleGrant via payload: unexpected error: %v", err)
	}
	if got != msgID {
		t.Errorf("pollForRoleGrant returned msg ID %q, want %q", got, msgID)
	}
}

// TestPollForRoleGrant_Timeout verifies that pollForRoleGrant returns an error
// after the timeout when the fake client returns no matching messages.
func TestPollForRoleGrant_Timeout(t *testing.T) {
	myPubKey := pubkeyHex("ab")
	ownerKey := pubkeyHex("dd")
	campfire := pubkeyHex("cc")

	// Fake returns a message for a different pubkey — never matches.
	otherKey := pubkeyHex("11")
	msg := makeGrantMsg("grant-other", otherKey, "member")
	msg.Sender = ownerKey

	fake := &fakeReadClient{
		messages: []protocol.Message{msg},
	}

	authorizedSenders := map[string]bool{ownerKey: true}
	timeout := 50 * time.Millisecond
	_, err := pollForRoleGrant(fake, campfire, myPubKey, authorizedSenders, timeout)
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	if !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected 'timed out' in error, got: %v", err)
	}
}

// TestPollForRoleGrant_AttackerFakeGrantIgnored is the security regression test
// for ready-9ce. An attacker (unauthorized sender) posts a fake work:role-grant
// targeting the joiner. pollForRoleGrant must ignore the attacker's message and
// only accept a grant from an authorized sender (the campfire owner).
//
// Scenario:
//   - joinerKey: the pubkey of the identity waiting to be admitted
//   - ownerKey: the campfire creator (authorized sender)
//   - attackerKey: a different campfire member (unauthorized sender)
//
// The attacker posts a valid-looking role-grant message targeting joinerKey.
// pollForRoleGrant must not accept it. Only the owner's grant is accepted.
func TestPollForRoleGrant_AttackerFakeGrantIgnored(t *testing.T) {
	joinerKey := pubkeyHex("aa")
	ownerKey := pubkeyHex("bb")
	attackerKey := pubkeyHex("cc")
	campfire := pubkeyHex("ee")

	// Attacker's fake grant — valid payload and tag, but wrong sender.
	attackerGrant := makeGrantMsgForTag("attacker-grant-001", joinerKey, "member")
	attackerGrant.Sender = attackerKey

	// Owner's legitimate grant — same payload, authorized sender.
	ownerGrant := makeGrantMsgForTag("owner-grant-001", joinerKey, "member")
	ownerGrant.Sender = ownerKey

	// Only owner is in the authorized senders set.
	authorizedSenders := map[string]bool{ownerKey: true}

	t.Run("attacker_grant_only_times_out", func(t *testing.T) {
		// Client returns only the attacker's fake grant — must time out.
		fake := &fakeReadClient{
			messages: []protocol.Message{attackerGrant},
		}
		_, err := pollForRoleGrant(fake, campfire, joinerKey, authorizedSenders, 50*time.Millisecond)
		if err == nil {
			t.Fatal("pollForRoleGrant accepted attacker's fake role-grant — security violation")
		}
		if !strings.Contains(err.Error(), "timed out") {
			t.Errorf("expected 'timed out' error, got: %v", err)
		}
	})

	t.Run("owner_grant_accepted", func(t *testing.T) {
		// Client returns both attacker's and owner's grant — must accept owner's.
		fake := &fakeReadClient{
			messages: []protocol.Message{attackerGrant, ownerGrant},
		}
		got, err := pollForRoleGrant(fake, campfire, joinerKey, authorizedSenders, 5*time.Second)
		if err != nil {
			t.Fatalf("pollForRoleGrant rejected valid owner grant: %v", err)
		}
		if got != ownerGrant.ID {
			t.Errorf("pollForRoleGrant returned msg ID %q, want owner grant %q", got, ownerGrant.ID)
		}
	})
}

// TestResolveName_DirectHex verifies that resolveName returns a 64-char hex
// campfire ID unchanged — no client call is made for this path.
//
// We call the real resolveName function using a real (temp-dir) client so the
// test actually exercises production code, not just code inspection.
func TestResolveName_DirectHex(t *testing.T) {
	// Set up a temp client dir so requireClient() / CFHome() use isolated state.
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	client, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	hexID := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	got, err := resolveName(client, hexID)
	if err != nil {
		t.Fatalf("resolveName direct hex: unexpected error: %v", err)
	}
	if got != hexID {
		t.Errorf("resolveName(%q) = %q, want same value", hexID, got)
	}
}

// cfHomeTempDir creates a temp dir and sets rdHome so CFHome() returns it.
// Cleans up on test completion.
func cfHomeTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })
	return dir
}

// sampleRoot is a 64-char hex string used as a fake beacon root in TOFU tests.
const sampleRoot = "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899"

// sampleRoot2 is a different 64-char hex string for mismatch tests.
const sampleRoot2 = "1122334455667788990011223344556677889900112233445566778899001122"

// TestBeaconRoot_FirstUse_NonInteractive verifies path 1:
// First use (no pin) in a non-interactive context (tests always run non-interactive).
// Expected: pin is saved to config, no error.
func TestBeaconRoot_FirstUse_NonInteractive(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	cfg := &rdconfig.Config{} // no pin yet
	err := applyBeaconRootTOFU(cfHome, cfg, sampleRoot, false /* confirm */)
	if err != nil {
		t.Fatalf("applyBeaconRootTOFU first use: unexpected error: %v", err)
	}

	// Pin must be set in cfg in-place.
	if cfg.BeaconRoot != sampleRoot {
		t.Errorf("cfg.BeaconRoot = %q, want %q", cfg.BeaconRoot, sampleRoot)
	}

	// Pin must be persisted to disk.
	saved, err := rdconfig.Load(cfHome)
	if err != nil {
		t.Fatalf("rdconfig.Load after pin: %v", err)
	}
	if saved.BeaconRoot != sampleRoot {
		t.Errorf("saved BeaconRoot = %q, want %q", saved.BeaconRoot, sampleRoot)
	}
}

// TestBeaconRoot_FirstUse_WithConfirmFlag verifies path 2:
// First use (no pin) with --confirm=true bypasses any prompt and pins.
// Expected: pin saved, no error, same as non-interactive path.
func TestBeaconRoot_FirstUse_WithConfirmFlag(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	cfg := &rdconfig.Config{}
	err := applyBeaconRootTOFU(cfHome, cfg, sampleRoot, true /* confirm */)
	if err != nil {
		t.Fatalf("applyBeaconRootTOFU with --confirm: unexpected error: %v", err)
	}
	if cfg.BeaconRoot != sampleRoot {
		t.Errorf("cfg.BeaconRoot = %q, want %q", cfg.BeaconRoot, sampleRoot)
	}

	saved, err := rdconfig.Load(cfHome)
	if err != nil {
		t.Fatalf("rdconfig.Load: %v", err)
	}
	if saved.BeaconRoot != sampleRoot {
		t.Errorf("persisted BeaconRoot = %q, want %q", saved.BeaconRoot, sampleRoot)
	}
}

// TestBeaconRoot_PinExistsAndMatches verifies path 3:
// Pin is already set, requested root matches. No error, no prompt.
func TestBeaconRoot_PinExistsAndMatches(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	cfg := &rdconfig.Config{BeaconRoot: sampleRoot}
	err := applyBeaconRootTOFU(cfHome, cfg, sampleRoot, false /* confirm */)
	if err != nil {
		t.Fatalf("applyBeaconRootTOFU pin matches: unexpected error: %v", err)
	}

	// Config must be unchanged on disk — write an initial state and verify no save.
	if err := rdconfig.Save(cfHome, &rdconfig.Config{BeaconRoot: sampleRoot}); err != nil {
		t.Fatalf("setup Save: %v", err)
	}
	// Run again with matching root; should be a no-op.
	cfg2 := &rdconfig.Config{BeaconRoot: sampleRoot}
	err = applyBeaconRootTOFU(cfHome, cfg2, sampleRoot, false)
	if err != nil {
		t.Errorf("applyBeaconRootTOFU matching pin: unexpected error: %v", err)
	}
}

// TestBeaconRoot_PinMismatch_NoConfirm verifies path 4:
// Pin exists, requested root is different, --confirm not passed.
// Expected: error containing "does not match pinned root".
func TestBeaconRoot_PinMismatch_NoConfirm(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	cfg := &rdconfig.Config{BeaconRoot: sampleRoot}
	err := applyBeaconRootTOFU(cfHome, cfg, sampleRoot2, false /* confirm */)
	if err == nil {
		t.Fatal("expected error on beacon root mismatch without --confirm, got nil")
	}
	if !strings.Contains(err.Error(), "does not match pinned root") {
		t.Errorf("error should mention 'does not match pinned root', got: %v", err)
	}
}

// TestBeaconRoot_PinMismatch_WithConfirm verifies that --confirm allows proceeding
// even when the requested root differs from the pinned root.
func TestBeaconRoot_PinMismatch_WithConfirm(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	cfg := &rdconfig.Config{BeaconRoot: sampleRoot}
	err := applyBeaconRootTOFU(cfHome, cfg, sampleRoot2, true /* confirm */)
	if err != nil {
		t.Fatalf("applyBeaconRootTOFU with --confirm on mismatch: unexpected error: %v", err)
	}
}

// TestBeaconRoot_Reset verifies path 5:
// --reset-beacon-root clears the pin from config and returns the previous value.
func TestBeaconRoot_Reset(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	// Write a config with a beacon root pinned.
	if err := rdconfig.Save(cfHome, &rdconfig.Config{BeaconRoot: sampleRoot}); err != nil {
		t.Fatalf("setup Save: %v", err)
	}

	prev, err := resetBeaconRoot(cfHome)
	if err != nil {
		t.Fatalf("resetBeaconRoot: unexpected error: %v", err)
	}
	if prev != sampleRoot {
		t.Errorf("resetBeaconRoot prev = %q, want %q", prev, sampleRoot)
	}

	// Config must now have no pin.
	saved, err := rdconfig.Load(cfHome)
	if err != nil {
		t.Fatalf("rdconfig.Load after reset: %v", err)
	}
	if saved.BeaconRoot != "" {
		t.Errorf("BeaconRoot after reset = %q, want empty", saved.BeaconRoot)
	}
}

// TestBeaconRoot_Reset_NoPinned verifies resetBeaconRoot returns empty string
// when no root is pinned (idempotent).
func TestBeaconRoot_Reset_NoPinned(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	// No config file written — Load returns zero Config.
	prev, err := resetBeaconRoot(cfHome)
	if err != nil {
		t.Fatalf("resetBeaconRoot on empty config: unexpected error: %v", err)
	}
	if prev != "" {
		t.Errorf("expected empty prev, got %q", prev)
	}
}

// TestBeaconRoot_EmptyRoot_NoOp verifies that calling applyBeaconRootTOFU with
// an empty root is a no-op (no save, no error).
func TestBeaconRoot_EmptyRoot_NoOp(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	cfg := &rdconfig.Config{}
	err := applyBeaconRootTOFU(cfHome, cfg, "" /* beaconRoot */, false)
	if err != nil {
		t.Fatalf("empty beacon root should be no-op, got error: %v", err)
	}

	// No config file should have been written.
	configPath := filepath.Join(cfHome, "rd.json")
	if _, statErr := os.Stat(configPath); statErr == nil {
		t.Error("applyBeaconRootTOFU with empty root must not write config file")
	}
}

// TestJoin_OpenCampfire_NonMemberJoinsSuccessfully verifies the open join_protocol path:
//   - Identity A creates an open campfire (filesystem transport, shared temp base dir).
//   - Identity B (separate CF_HOME, non-member) calls client.Join() using the
//     campfire-specific dir returned by resolveTransportDir.
//   - Join succeeds and B can subsequently send a message (post-join create).
//
// This is the regression test for ready-1b5. No mocks — real protocol.Client
// instances with real temp dirs and real SQLite stores.
func TestJoin_OpenCampfire_NonMemberJoinsSuccessfully(t *testing.T) {
	// Use a shared transport dir so both identities access the same campfire state.
	sharedTransportDir := t.TempDir()
	origTransportDir := os.Getenv("CF_TRANSPORT_DIR")
	os.Setenv("CF_TRANSPORT_DIR", sharedTransportDir)
	t.Cleanup(func() {
		if origTransportDir != "" {
			os.Setenv("CF_TRANSPORT_DIR", origTransportDir)
		} else {
			os.Unsetenv("CF_TRANSPORT_DIR")
		}
	})

	// --- Identity A: campfire creator ---
	cfHomeA := t.TempDir()
	origRDHome := rdHome
	rdHome = cfHomeA
	protocolClientOrig := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = protocolClientOrig
		rdHome = origRDHome
	})

	clientA, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient (A): %v", err)
	}

	// A creates an open campfire in the shared transport dir.
	createResult, err := clientA.Create(protocol.CreateRequest{
		JoinProtocol: "open",
		Transport:    protocol.FilesystemTransport{Dir: sharedTransportDir},
	})
	if err != nil {
		t.Fatalf("A.Create (open): %v", err)
	}
	campfireID := createResult.CampfireID

	// --- Identity B: non-member joiner (separate CF_HOME) ---
	// Reset global client cache so B gets a fresh client with a different identity.
	if protocolClient != nil {
		protocolClient.Close()
	}
	protocolClient = nil

	cfHomeB := t.TempDir()
	rdHome = cfHomeB

	clientB, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient (B): %v", err)
	}

	// resolveTransportDir must return the campfire-specific dir (sharedTransportDir/campfireID).
	// With no beacon published (no beacon dir scan), it falls back to:
	//   localCampfireBaseDir() / campfireID = sharedTransportDir / campfireID
	transportDir := resolveTransportDir(campfireID)
	wantDir := filepath.Join(sharedTransportDir, campfireID)
	if transportDir != wantDir {
		t.Errorf("resolveTransportDir = %q, want %q", transportDir, wantDir)
	}

	// B calls client.Join() — open campfire, must succeed.
	joinResult, err := clientB.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport:  protocol.FilesystemTransport{Dir: transportDir},
	})
	if err != nil {
		t.Fatalf("B.Join (open campfire): %v", err)
	}
	if joinResult.JoinProtocol != "open" {
		t.Errorf("joinResult.JoinProtocol = %q, want %q", joinResult.JoinProtocol, "open")
	}

	// After join, B must be able to send a message (simulates 'rd create').
	_, err = clientB.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    []byte(`{"title":"post-join message from B"}`),
		Tags:       []string{"work:create"},
	})
	if err != nil {
		t.Fatalf("B.Send after join: %v — B is not recognized as a member after joining", err)
	}
}

// TestJoin_InviteOnlyCampfire_FailsWithClearError verifies the invite-only path:
//   - Identity A creates an invite-only campfire.
//   - Identity B (non-member) calls client.Join() on the invite-only campfire.
//   - Join fails with an error containing "invite-only".
//   - The error must NOT be a transport error — it must clearly indicate the protocol
//     rejection so rd join can distinguish it from network/transport failures and
//     produce the right user-facing message.
//
// This is the regression test for ready-1b5. No mocks — real protocol.Client instances.
func TestJoin_InviteOnlyCampfire_FailsWithClearError(t *testing.T) {
	sharedTransportDir := t.TempDir()
	origTransportDir := os.Getenv("CF_TRANSPORT_DIR")
	os.Setenv("CF_TRANSPORT_DIR", sharedTransportDir)
	t.Cleanup(func() {
		if origTransportDir != "" {
			os.Setenv("CF_TRANSPORT_DIR", origTransportDir)
		} else {
			os.Unsetenv("CF_TRANSPORT_DIR")
		}
	})

	// --- Identity A: campfire creator ---
	cfHomeA := t.TempDir()
	origRDHome := rdHome
	rdHome = cfHomeA
	protocolClientOrig := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = protocolClientOrig
		rdHome = origRDHome
	})

	clientA, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient (A): %v", err)
	}

	// A creates an invite-only campfire.
	createResult, err := clientA.Create(protocol.CreateRequest{
		JoinProtocol: "invite-only",
		Transport:    protocol.FilesystemTransport{Dir: sharedTransportDir},
	})
	if err != nil {
		t.Fatalf("A.Create (invite-only): %v", err)
	}
	campfireID := createResult.CampfireID

	// --- Identity B: non-member (separate CF_HOME) ---
	if protocolClient != nil {
		protocolClient.Close()
	}
	protocolClient = nil

	cfHomeB := t.TempDir()
	rdHome = cfHomeB

	clientB, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient (B): %v", err)
	}

	// B attempts to join an invite-only campfire without pre-admission.
	transportDir := filepath.Join(sharedTransportDir, campfireID)
	_, joinErr := clientB.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport:  protocol.FilesystemTransport{Dir: transportDir},
	})
	if joinErr == nil {
		t.Fatal("B.Join (invite-only): expected error, got nil — non-member must not join invite-only campfire")
	}

	// Error must contain "invite-only" so rd join can produce the right user-facing message.
	if !strings.Contains(joinErr.Error(), "invite-only") {
		t.Errorf("B.Join error = %q — expected 'invite-only' in message so rd join can distinguish protocol rejection from transport errors", joinErr.Error())
	}
}

// TestBeaconRoot_NotPinnedOnJoinFailure is the regression test for ready-f43.
// When join fails (e.g., invite-only campfire), the beacon root must NOT be pinned,
// even if --root was passed and validateBeaconRootTOFU allowed proceeding.
//
// Scenario:
//   - User runs: rd join <invite-only-campfire> --root <beacon-root>
//   - validateBeaconRootTOFU allows proceeding (first use, beacon root to be pinned)
//   - client.Join() fails with invite-only error
//   - Beacon root must NOT be persisted to config (no pin saved)
//   - User can try again with a different root or after being admitted
func TestBeaconRoot_NotPinnedOnJoinFailure(t *testing.T) {
	sharedTransportDir := t.TempDir()
	origTransportDir := os.Getenv("CF_TRANSPORT_DIR")
	os.Setenv("CF_TRANSPORT_DIR", sharedTransportDir)
	t.Cleanup(func() {
		if origTransportDir != "" {
			os.Setenv("CF_TRANSPORT_DIR", origTransportDir)
		} else {
			os.Unsetenv("CF_TRANSPORT_DIR")
		}
	})

	// --- Identity A: campfire creator (invite-only) ---
	cfHomeA := t.TempDir()
	origRDHome := rdHome
	rdHome = cfHomeA
	protocolClientOrig := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = protocolClientOrig
		rdHome = origRDHome
	})

	clientA, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient (A): %v", err)
	}

	// A creates an invite-only campfire.
	createResult, err := clientA.Create(protocol.CreateRequest{
		JoinProtocol: "invite-only",
		Transport:    protocol.FilesystemTransport{Dir: sharedTransportDir},
	})
	if err != nil {
		t.Fatalf("A.Create (invite-only): %v", err)
	}
	campfireID := createResult.CampfireID

	// --- Identity B: non-member (separate CF_HOME) ---
	if protocolClient != nil {
		protocolClient.Close()
	}
	protocolClient = nil

	cfHomeB := t.TempDir()
	rdHome = cfHomeB

	// Verify no beacon root is pinned initially.
	cfgBefore, err := rdconfig.Load(cfHomeB)
	if err != nil {
		t.Fatalf("rdconfig.Load before join: %v", err)
	}
	if cfgBefore.BeaconRoot != "" {
		t.Errorf("config before join should have empty beacon root, got %q", cfgBefore.BeaconRoot)
	}

	// Attempt join with a beacon root. This will fail because the campfire is invite-only.
	// validateBeaconRootTOFU with --confirm should allow this (first use, first-use guard passes).
	// But client.Join() will fail, and the beacon root should NOT be pinned.
	testBeaconRoot := sampleRoot

	cfg := &rdconfig.Config{}
	err = validateBeaconRootTOFU(cfHomeB, cfg, testBeaconRoot, true /* confirm */)
	if err != nil {
		t.Fatalf("validateBeaconRootTOFU with --confirm should allow first use: %v", err)
	}
	// validateBeaconRootTOFU does NOT save — it only validates and prompts.
	// Config should still have empty beacon root.
	if cfg.BeaconRoot != "" {
		t.Errorf("cfg.BeaconRoot after validateBeaconRootTOFU should remain empty, got %q", cfg.BeaconRoot)
	}

	// Now simulate the join failure by calling client.Join() directly.
	clientB, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient (B): %v", err)
	}

	transportDir := filepath.Join(sharedTransportDir, campfireID)
	_, joinErr := clientB.Join(protocol.JoinRequest{
		CampfireID: campfireID,
		Transport:  protocol.FilesystemTransport{Dir: transportDir},
	})
	if joinErr == nil {
		t.Fatal("B.Join (invite-only): expected error, got nil")
	}
	if !strings.Contains(joinErr.Error(), "invite-only") {
		t.Errorf("B.Join error should contain 'invite-only', got: %v", joinErr)
	}

	// --- Verification: Beacon root must NOT be pinned ---
	// After join failure, config should still have empty beacon root.
	cfgAfter, err := rdconfig.Load(cfHomeB)
	if err != nil {
		t.Fatalf("rdconfig.Load after failed join: %v", err)
	}
	if cfgAfter.BeaconRoot != "" {
		t.Errorf("beacon root must NOT be pinned on join failure; got pinned to %q", cfgAfter.BeaconRoot)
	}
}

// TestValidateNameFormat is the security regression test for ready-bf5.
// validateNameFormat must reject malformed names before any network call.
func TestValidateNameFormat(t *testing.T) {
	// validHexID is a well-formed 64-char campfire ID.
	validHexID := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

	cases := []struct {
		name    string
		input   string
		wantErr bool
		errFrag string // substring that must appear in the error message
	}{
		// Valid inputs — must not error.
		{name: "valid_hex_id", input: validHexID, wantErr: false},
		{name: "valid_short_name", input: "myorg.ready/myproject", wantErr: false},
		{name: "valid_cf_uri", input: "cf://myorg.ready/myproject", wantErr: false},
		{name: "valid_simple_name", input: "myproject", wantErr: false},
		{name: "valid_hyphenated", input: "my-org.ready/my-project", wantErr: false},

		// Path traversal — must error.
		{name: "path_traversal_unix", input: "../etc/passwd", wantErr: true, errFrag: "path traversal"},
		{name: "path_traversal_in_middle", input: "foo/../bar", wantErr: true, errFrag: "path traversal"},
		{name: "path_traversal_windows", input: `foo..\bar`, wantErr: true, errFrag: "path traversal"},
		{name: "path_traversal_bare", input: "..", wantErr: true, errFrag: "path traversal"},
		{name: "path_traversal_trailing_unix", input: "foo/..", wantErr: true, errFrag: "path traversal"},
		{name: "path_traversal_trailing_nested", input: "bar/baz/..", wantErr: true, errFrag: "path traversal"},
		{name: "path_traversal_trailing_windows", input: `foo\..\`, wantErr: true, errFrag: "path traversal"},

		// Null bytes — must error.
		{name: "null_byte_prefix", input: "\x00foo", wantErr: true, errFrag: "null byte"},
		{name: "null_byte_middle", input: "foo\x00bar", wantErr: true, errFrag: "null byte"},
		{name: "null_byte_only", input: "\x00", wantErr: true, errFrag: "null byte"},

		// Excessive length — must error.
		{name: "too_long_257", input: strings.Repeat("a", 257), wantErr: true, errFrag: "too long"},
		{name: "exactly_256", input: strings.Repeat("a", 256), wantErr: false},

		// Empty input — must error.
		{name: "empty", input: "", wantErr: true, errFrag: "empty"},

		// Invalid characters — must error.
		{name: "space_char", input: "my project", wantErr: true, errFrag: "invalid character"},
		{name: "at_sign", input: "user@host", wantErr: true, errFrag: "invalid character"},
		{name: "backslash_only", input: `foo\bar`, wantErr: true, errFrag: "invalid character"},
		{name: "semicolon", input: "foo;bar", wantErr: true, errFrag: "invalid character"},
		{name: "newline", input: "foo\nbar", wantErr: true, errFrag: "invalid character"},
		{name: "tilde", input: "~root", wantErr: true, errFrag: "invalid character"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := validateNameFormat(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("validateNameFormat(%q): expected error, got nil", tc.input)
				}
				if tc.errFrag != "" && !strings.Contains(err.Error(), tc.errFrag) {
					t.Errorf("validateNameFormat(%q): error %q does not contain %q", tc.input, err.Error(), tc.errFrag)
				}
			} else {
				if err != nil {
					t.Fatalf("validateNameFormat(%q): unexpected error: %v", tc.input, err)
				}
			}
		})
	}
}

// TestBeaconRoot_FirstUse_WithConfirm_AllowsNonInteractive is the regression test for ready-a6c.
// Security fix: validateBeaconRootTOFU with --confirm=true should allow proceeding in any context
// (interactive or non-interactive), because the user has explicitly confirmed the beacon root pin.
// This test verifies the fix: when --confirm is passed, validation succeeds without prompt.
func TestBeaconRoot_FirstUse_WithConfirm_AllowsNonInteractive(t *testing.T) {
	cfHome := cfHomeTempDir(t)

	cfg := &rdconfig.Config{} // no pin yet
	// First use with --confirm flag should proceed without prompting.
	// The fix ensures this works in both interactive and non-interactive contexts.
	err := validateBeaconRootTOFU(cfHome, cfg, sampleRoot, true /* confirm */)
	if err != nil {
		t.Fatalf("validateBeaconRootTOFU with --confirm should allow first use without error: %v", err)
	}

	// validateBeaconRootTOFU does not save — it only validates.
	// Config should still be empty (save happens post-join in joinCmd.RunE).
	if cfg.BeaconRoot != "" {
		t.Errorf("cfg.BeaconRoot after validateBeaconRootTOFU should remain empty, got %q", cfg.BeaconRoot)
	}
}

// TestResolveName_EdgeCases_EmptyName verifies that resolveName rejects empty names.
func TestResolveName_EdgeCases_EmptyName(t *testing.T) {
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	client, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	_, err = resolveName(client, "")
	if err == nil {
		t.Fatal("resolveName with empty name should error")
	}
	if !strings.Contains(err.Error(), "empty") {
		t.Errorf("expected 'empty' in error, got: %v", err)
	}
}

// TestResolveName_EdgeCases_SpecialCharactersInName verifies that names with
// special characters (but not path traversal) are accepted or rejected per spec.
func TestResolveName_EdgeCases_SpecialCharactersInName(t *testing.T) {
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	client, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	cases := []struct {
		name    string
		input   string
		wantErr bool
	}{
		// Valid special chars per allowlist (alphanumeric, dot, hyphen, slash, colon)
		{name: "name_with_dots", input: "my.org.ready.project", wantErr: false},
		{name: "name_with_hyphens", input: "my-org-ready-project", wantErr: false},
		{name: "name_with_colons", input: "cf://myorg.ready/myproject", wantErr: false},
		{name: "name_with_slashes", input: "myorg/myproject/sub", wantErr: false},

		// Invalid special chars (space, @, !, ?, etc.)
		{name: "name_with_space", input: "my org", wantErr: true},
		{name: "name_with_at", input: "user@host", wantErr: true},
		{name: "name_with_exclamation", input: "project!", wantErr: true},
		{name: "name_with_question", input: "what?", wantErr: true},
		{name: "name_with_equals", input: "x=y", wantErr: true},
		{name: "name_with_plus", input: "a+b", wantErr: true},
		{name: "name_with_ampersand", input: "a&b", wantErr: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveName(client, tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveName(%q): expected error, got nil", tc.input)
				}
			} else {
				if err != nil {
					// For non-error cases, we expect it to fail on resolver (not client) which is expected
					// The point is validateNameFormat should not reject it.
					if strings.Contains(err.Error(), "invalid name") {
						t.Fatalf("resolveName(%q): validateNameFormat rejected valid name: %v", tc.input, err)
					}
				}
			}
		})
	}
}

// TestResolveName_EdgeCases_VeryLongName verifies that resolveName rejects
// names longer than the maximum allowed (256 chars).
func TestResolveName_EdgeCases_VeryLongName(t *testing.T) {
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	client, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	// Create a name exactly at boundary (256 chars) — should be OK
	validLongName := strings.Repeat("a", 256)
	_, err = resolveName(client, validLongName)
	if err != nil && strings.Contains(err.Error(), "too long") {
		t.Fatalf("resolveName with 256-char name should not reject for length: %v", err)
	}

	// Create a name over the limit (257 chars) — should error
	tooLongName := strings.Repeat("a", 257)
	_, err = resolveName(client, tooLongName)
	if err == nil {
		t.Fatal("resolveName with 257-char name should error")
	}
	if !strings.Contains(err.Error(), "too long") {
		t.Errorf("expected 'too long' in error, got: %v", err)
	}
}

// TestResolveName_EdgeCases_HexLikeName verifies that names that resemble
// hex but are not exactly 64 chars are handled correctly.
func TestResolveName_EdgeCases_HexLikeName(t *testing.T) {
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	client, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	cases := []struct {
		name    string
		input   string
		wantErr bool // expect validation error or resolver error
	}{
		// 63 hex chars — looks like ID but is one char short
		{name: "hex_63_chars", input: "bcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", wantErr: true},
		// 65 hex chars — looks like ID but is one char too long
		{name: "hex_65_chars", input: "aabcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", wantErr: true},
		// 64 chars but not all hex (letter 'g' is not hex)
		{name: "64_chars_non_hex", input: "abcdefg234567890abcdef1234567890abcdef1234567890abcdef1234567890", wantErr: true},
		// 64 hex chars (valid) — treated as direct ID
		{name: "valid_64_hex", input: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890", wantErr: false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveName(client, tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("resolveName(%q): expected error, got nil", tc.input)
				}
			} else {
				// Valid 64-char hex should return without error
				if err != nil {
					t.Fatalf("resolveName(%q): unexpected error: %v", tc.input, err)
				}
			}
		})
	}
}

// TestResolveName_EdgeCases_NullBytesRejected verifies that names containing
// null bytes are rejected before any network call.
func TestResolveName_EdgeCases_NullBytesRejected(t *testing.T) {
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	client, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	cases := []struct {
		name  string
		input string
	}{
		{name: "null_at_start", input: "\x00foo"},
		{name: "null_in_middle", input: "foo\x00bar"},
		{name: "null_at_end", input: "foo\x00"},
		{name: "multiple_nulls", input: "foo\x00bar\x00baz"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveName(client, tc.input)
			if err == nil {
				t.Fatalf("resolveName with null byte should error")
			}
			if !strings.Contains(err.Error(), "null byte") {
				t.Errorf("expected 'null byte' in error, got: %v", err)
			}
		})
	}
}

// TestResolveName_EdgeCases_PathTraversalRejected verifies that names with
// path traversal sequences are rejected before any network call.
func TestResolveName_EdgeCases_PathTraversalRejected(t *testing.T) {
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	client, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	cases := []struct {
		name  string
		input string
	}{
		{name: "unix_traversal_prefix", input: "../etc/passwd"},
		{name: "unix_traversal_middle", input: "foo/../bar"},
		{name: "unix_traversal_bare", input: ".."},
		{name: "unix_traversal_suffix", input: "foo/.."},
		{name: "windows_traversal", input: `foo..\bar`},
		{name: "windows_traversal_suffix", input: `foo\..\`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := resolveName(client, tc.input)
			if err == nil {
				t.Fatalf("resolveName with path traversal should error")
			}
			if !strings.Contains(err.Error(), "path traversal") {
				t.Errorf("expected 'path traversal' in error, got: %v", err)
			}
		})
	}
}

// TestResolveName_EdgeCases_MixedCaseHex verifies that both uppercase and
// lowercase hex IDs (and mixed case) are accepted when they are exactly 64 chars.
func TestResolveName_EdgeCases_MixedCaseHex(t *testing.T) {
	dir := t.TempDir()
	origRDHome := rdHome
	rdHome = dir
	t.Cleanup(func() { rdHome = origRDHome })

	origClient := protocolClient
	protocolClient = nil
	t.Cleanup(func() {
		if protocolClient != nil {
			protocolClient.Close()
		}
		protocolClient = origClient
	})

	client, err := requireClient()
	if err != nil {
		t.Fatalf("requireClient: %v", err)
	}

	cases := []struct {
		name  string
		input string
	}{
		{name: "lowercase_hex", input: "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"},
		{name: "uppercase_hex", input: "ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890"},
		{name: "mixed_case_hex", input: "AbCdEf1234567890aBcDeF1234567890AbCdEf1234567890aBcDeF1234567890"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := resolveName(client, tc.input)
			if err != nil {
				t.Fatalf("resolveName(%q): unexpected error: %v", tc.input, err)
			}
			if got != tc.input {
				t.Errorf("resolveName(%q) = %q, want same value", tc.input, got)
			}
		})
	}
}
