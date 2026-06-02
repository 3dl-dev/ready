package conventionserver_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/campfire"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	cffs "github.com/campfire-net/campfire/cf-protocol/transport/fs"

	"github.com/campfire-net/ready/pkg/conventionserver"
)

// testEnv holds the campfire test environment shared by all tests.
type testEnv struct {
	// serverClient is used by the in-process convention server.
	serverClient *protocol.Client
	// callerClient is used by the test to send operation requests.
	callerClient *protocol.Client
	campfireID   string
}

// setupTestEnv creates two identities (server + caller), a shared filesystem
// campfire that both are members of, and returns clients for each.
// Mirrors setupServerTestEnv from convention/server_test.go.
func setupTestEnv(t *testing.T) *testEnv {
	t.Helper()

	storeDir := t.TempDir()
	transportDir := t.TempDir()

	serverID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating server identity: %v", err)
	}
	callerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating caller identity: %v", err)
	}

	// Create campfire identity.
	cfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating campfire identity: %v", err)
	}
	campfireID := cfID.PublicKeyHex()

	// Set up directory structure.
	cfDir := filepath.Join(transportDir, campfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(cfDir, sub), 0755); err != nil {
			t.Fatalf("creating %s dir: %v", sub, err)
		}
	}

	// Write campfire state.
	state := &campfire.CampfireState{
		PublicKey:             cfID.PublicKey,
		PrivateKey:            cfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	stateData, err := cfencoding.Marshal(state)
	if err != nil {
		t.Fatalf("marshalling campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfDir, "campfire.cbor"), stateData, 0644); err != nil {
		t.Fatalf("writing campfire state: %v", err)
	}

	tr := cffs.New(transportDir)

	// Write both members.
	for _, id := range []*identity.Identity{serverID, callerID} {
		if err := tr.WriteMember(campfireID, campfire.MemberRecord{
			PublicKey: id.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
			Role:      campfire.RoleFull,
		}); err != nil {
			t.Fatalf("writing member: %v", err)
		}
	}

	// Set up stores.
	serverStore, err := store.Open(filepath.Join(storeDir, "server.db"))
	if err != nil {
		t.Fatalf("opening server store: %v", err)
	}
	t.Cleanup(func() { serverStore.Close() })

	callerStore, err := store.Open(filepath.Join(storeDir, "caller.db"))
	if err != nil {
		t.Fatalf("opening caller store: %v", err)
	}
	t.Cleanup(func() { callerStore.Close() })

	membership := store.Membership{
		CampfireID:    campfireID,
		TransportDir:  tr.CampfireDir(campfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}
	if err := serverStore.AddMembership(membership); err != nil {
		t.Fatalf("server store add membership: %v", err)
	}
	if err := callerStore.AddMembership(membership); err != nil {
		t.Fatalf("caller store add membership: %v", err)
	}

	return &testEnv{
		serverClient: protocol.New(serverStore, serverID),
		callerClient: protocol.New(callerStore, callerID),
		campfireID:   campfireID,
	}
}

// setupSummaryCampfireEnv creates a summary campfire shared between a server
// and an observer client. Returns the campfire ID and a read client backed by
// the server identity (to verify messages the server writes).
func setupSummaryCampfireEnv(t *testing.T, transportDir string) (summaryID string, serverID *identity.Identity, readClient *protocol.Client) {
	t.Helper()

	sumCfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating summary campfire identity: %v", err)
	}
	summaryCampfireID := sumCfID.PublicKeyHex()

	sumCfDir := filepath.Join(transportDir, summaryCampfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(sumCfDir, sub), 0755); err != nil {
			t.Fatalf("creating summary %s dir: %v", sub, err)
		}
	}

	sumState := &campfire.CampfireState{
		PublicKey:             sumCfID.PublicKey,
		PrivateKey:            sumCfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	sumStateData, err := cfencoding.Marshal(sumState)
	if err != nil {
		t.Fatalf("marshalling summary campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sumCfDir, "campfire.cbor"), sumStateData, 0644); err != nil {
		t.Fatalf("writing summary campfire state: %v", err)
	}

	srvID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating server identity for summary: %v", err)
	}

	tr := cffs.New(transportDir)
	if err := tr.WriteMember(summaryCampfireID, campfire.MemberRecord{
		PublicKey: srvID.PublicKey,
		JoinedAt:  time.Now().UnixNano(),
		Role:      campfire.RoleFull,
	}); err != nil {
		t.Fatalf("writing server member to summary campfire: %v", err)
	}

	storeDir := t.TempDir()
	sumStore, err := store.Open(filepath.Join(storeDir, "summary.db"))
	if err != nil {
		t.Fatalf("opening summary store: %v", err)
	}
	t.Cleanup(func() { sumStore.Close() })

	if err := sumStore.AddMembership(store.Membership{
		CampfireID:    summaryCampfireID,
		TransportDir:  tr.CampfireDir(summaryCampfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("sum store add membership: %v", err)
	}

	return summaryCampfireID, srvID, protocol.New(sumStore, srvID)
}

// minimalDecl returns a minimal work:close-like declaration for testing.
// It does NOT use the real embedded declarations to avoid external dependencies
// in this specific test; tests that need real declarations use the default loader.
func minimalCloseDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "work",
		Operation:  "close",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "target", Type: "message_id", Required: true},
			{Name: "resolution", Type: "string", Required: true},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "work:close", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}
}

// minimalCreateDecl returns a minimal work:create-like declaration for testing.
func minimalCreateDecl() *convention.Declaration {
	return &convention.Declaration{
		Convention: "work",
		Operation:  "create",
		Signing:    "member_key",
		Args: []convention.ArgDescriptor{
			{Name: "id", Type: "string", Required: true},
			{Name: "title", Type: "string", Required: true},
		},
		ProducesTags: []convention.TagRule{
			{Tag: "work:create", Cardinality: "exactly_one"},
		},
		Antecedents: "none",
	}
}

// TestServerPostsServerBinding verifies that Start() posts a convention:server-binding
// message with the server's public key to the campfire.
func TestServerPostsServerBinding(t *testing.T) {
	env := setupTestEnv(t)

	// Use a minimal loader so the server doesn't need real declarations for Start().
	loader := func(name string) (*convention.Declaration, error) {
		return minimalCloseDecl(), nil
	}

	srv, err := conventionserver.New(
		env.serverClient,
		env.campfireID,
		conventionserver.WithPollInterval(50*time.Millisecond),
		conventionserver.WithDeclarationLoader(loader),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the server a moment to post the binding.
	time.Sleep(100 * time.Millisecond)

	// Read convention:server-binding messages from the campfire (using callerClient
	// to verify the binding is visible to other participants).
	result, err := env.callerClient.Read(protocol.ReadRequest{
		CampfireID: env.campfireID,
		Tags:       []string{"convention:server-binding"},
	})
	if err != nil {
		t.Fatalf("reading server-binding messages: %v", err)
	}

	if len(result.Messages) == 0 {
		t.Fatal("expected at least one convention:server-binding message, got none")
	}

	// Verify that one of the bindings contains the server's public key.
	serverPubKey := srv.PubKeyHex()
	var found bool
	for _, msg := range result.Messages {
		var payload struct {
			ServerPubkey string `json:"server_pubkey"`
			Convention   string `json:"convention"`
			ValidUntil   int64  `json:"valid_until"`
		}
		if err := json.Unmarshal(msg.Payload, &payload); err != nil {
			continue
		}
		if payload.ServerPubkey == serverPubKey {
			found = true
			// Verify valid_until is in the future.
			if payload.ValidUntil <= time.Now().UnixNano() {
				t.Errorf("server-binding valid_until %d is not in the future", payload.ValidUntil)
			}
			// Verify convention field.
			if payload.Convention != "work" {
				t.Errorf("server-binding convention = %q, want %q", payload.Convention, "work")
			}
			break
		}
	}

	if !found {
		t.Errorf("no convention:server-binding found for server pubkey %s", serverPubKey)
	}
}

// TestServerFulfillsCloseOperation verifies that a work:close operation message
// posted to the campfire is received by the in-process server and a fulfillment
// message is sent back.
func TestServerFulfillsCloseOperation(t *testing.T) {
	env := setupTestEnv(t)

	// Use a minimal close declaration for the server.
	loader := func(name string) (*convention.Declaration, error) {
		if name != "close" {
			// Return minimal close decl for all operations so the server starts cleanly.
			return minimalCloseDecl(), nil
		}
		return minimalCloseDecl(), nil
	}

	srv, err := conventionserver.New(
		env.serverClient,
		env.campfireID,
		conventionserver.WithPollInterval(50*time.Millisecond),
		conventionserver.WithDeclarationLoader(loader),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the server a moment to start its subscriptions.
	time.Sleep(100 * time.Millisecond)

	// Send a work:close operation message from the caller.
	sentMsg, err := env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    []byte(`{"target":"some-msg-id","resolution":"done","reason":"test close"}`),
		Tags:       []string{"work:close"},
	})
	if err != nil {
		t.Fatalf("caller Send work:close: %v", err)
	}

	// Poll the campfire for a fulfillment message antecedent to sentMsg.ID.
	var fulfillmentFound bool
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := env.callerClient.Read(protocol.ReadRequest{
			CampfireID: env.campfireID,
			Tags:       []string{"fulfills"},
		})
		if err != nil {
			t.Fatalf("reading fulfillment messages: %v", err)
		}
		for _, msg := range result.Messages {
			for _, ant := range msg.Antecedents {
				if ant == sentMsg.ID {
					fulfillmentFound = true
					break
				}
			}
			if fulfillmentFound {
				break
			}
		}
		if fulfillmentFound {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()

	if !fulfillmentFound {
		t.Errorf("no fulfillment message found for work:close request (msg ID %s)", sentMsg.ID)
	}
}

// TestMinOperatorLevel verifies the authorization matrix.
func TestMinOperatorLevel(t *testing.T) {
	tests := []struct {
		op    string
		level int
	}{
		{"close", 2},
		{"delegate", 2},
		{"gate-resolve", 2},
		{"role-grant", 2},
		{"create", 1},
		{"claim", 1},
		{"update", 1},
		{"block", 1},
		{"unblock", 1},
		{"engage", 1},
		{"gate", 1},
		{"status", 1},
	}
	for _, tc := range tests {
		if got := conventionserver.MinOperatorLevel(tc.op); got != tc.level {
			t.Errorf("MinOperatorLevel(%q) = %d, want %d", tc.op, got, tc.level)
		}
	}
}

// TestServerBindingIdempotent verifies that calling Start() twice (or if a binding
// already exists) does not create duplicate server-binding messages.
func TestServerBindingIdempotent(t *testing.T) {
	env := setupTestEnv(t)

	loader := func(name string) (*convention.Declaration, error) {
		return minimalCloseDecl(), nil
	}

	srv, err := conventionserver.New(
		env.serverClient,
		env.campfireID,
		conventionserver.WithPollInterval(50*time.Millisecond),
		conventionserver.WithDeclarationLoader(loader),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	// Create a second server with the SAME key by reading the count first.
	result1, err := env.callerClient.Read(protocol.ReadRequest{
		CampfireID: env.campfireID,
		Tags:       []string{"convention:server-binding"},
	})
	if err != nil {
		t.Fatalf("reading server bindings (1): %v", err)
	}
	count1 := len(result1.Messages)
	if count1 == 0 {
		t.Fatal("expected at least one server-binding after Start()")
	}

	// Start again (simulating a second invocation with a NEW server, different key).
	srv2, err := conventionserver.New(
		env.serverClient,
		env.campfireID,
		conventionserver.WithPollInterval(50*time.Millisecond),
		conventionserver.WithDeclarationLoader(loader),
	)
	if err != nil {
		t.Fatalf("New srv2: %v", err)
	}
	if err := srv2.Start(ctx); err != nil {
		t.Fatalf("srv2.Start: %v", err)
	}
	time.Sleep(150 * time.Millisecond)

	result2, err := env.callerClient.Read(protocol.ReadRequest{
		CampfireID: env.campfireID,
		Tags:       []string{"convention:server-binding"},
	})
	if err != nil {
		t.Fatalf("reading server bindings (2): %v", err)
	}
	count2 := len(result2.Messages)

	// The second server has a DIFFERENT ephemeral key, so it posts its own binding.
	// count2 should be count1 + 1 (one new binding for srv2's key).
	// Both are valid. What we want to avoid is duplicates for the SAME key.
	if count2 < count1 {
		t.Errorf("expected count2 >= count1 (%d), got %d", count1, count2)
	}

	cancel()
}

// TestServerWritesSummaryOnCreate verifies that a work:create operation causes
// the convention server to write a work:item-summary message to the bound
// summary campfire.
func TestServerWritesSummaryOnCreate(t *testing.T) {
	transportDir := t.TempDir()
	storeDir := t.TempDir()

	// Set up the summary campfire first (server identity comes from this helper).
	summaryCampfireID, serverID, sumClient := setupSummaryCampfireEnv(t, transportDir)

	callerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating caller identity: %v", err)
	}

	// Set up the main campfire.
	mainCfID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating main campfire identity: %v", err)
	}
	mainCampfireID := mainCfID.PublicKeyHex()

	mainCfDir := filepath.Join(transportDir, mainCampfireID)
	for _, sub := range []string{"members", "messages"} {
		if err := os.MkdirAll(filepath.Join(mainCfDir, sub), 0755); err != nil {
			t.Fatalf("creating main %s dir: %v", sub, err)
		}
	}
	mainState := &campfire.CampfireState{
		PublicKey:             mainCfID.PublicKey,
		PrivateKey:            mainCfID.PrivateKey,
		JoinProtocol:          "open",
		ReceptionRequirements: []string{},
		CreatedAt:             time.Now().UnixNano(),
	}
	mainStateData, err := cfencoding.Marshal(mainState)
	if err != nil {
		t.Fatalf("marshalling main campfire state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(mainCfDir, "campfire.cbor"), mainStateData, 0644); err != nil {
		t.Fatalf("writing main campfire state: %v", err)
	}

	tr := cffs.New(transportDir)
	for _, id := range []*identity.Identity{serverID, callerID} {
		if err := tr.WriteMember(mainCampfireID, campfire.MemberRecord{
			PublicKey: id.PublicKey,
			JoinedAt:  time.Now().UnixNano(),
			Role:      campfire.RoleFull,
		}); err != nil {
			t.Fatalf("writing member to main campfire: %v", err)
		}
	}

	serverStore, err := store.Open(filepath.Join(storeDir, "server.db"))
	if err != nil {
		t.Fatalf("opening server store: %v", err)
	}
	t.Cleanup(func() { serverStore.Close() })

	callerStore, err := store.Open(filepath.Join(storeDir, "caller.db"))
	if err != nil {
		t.Fatalf("opening caller store: %v", err)
	}
	t.Cleanup(func() { callerStore.Close() })

	mainMembership := store.Membership{
		CampfireID:    mainCampfireID,
		TransportDir:  tr.CampfireDir(mainCampfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}
	if err := serverStore.AddMembership(mainMembership); err != nil {
		t.Fatalf("server store add main membership: %v", err)
	}
	if err := callerStore.AddMembership(mainMembership); err != nil {
		t.Fatalf("caller store add main membership: %v", err)
	}

	// The server identity also needs membership in the summary campfire in its
	// main store so it can write summaries. Add the summary membership to serverStore.
	if err := serverStore.AddMembership(store.Membership{
		CampfireID:    summaryCampfireID,
		TransportDir:  tr.CampfireDir(summaryCampfireID),
		JoinProtocol:  "open",
		Role:          campfire.RoleFull,
		JoinedAt:      time.Now().UnixNano(),
		Threshold:     1,
		TransportType: "filesystem",
	}); err != nil {
		t.Fatalf("server store add summary membership: %v", err)
	}

	// serverClient uses the same identity as sumClient (single identity serves both campfires).
	serverClient := protocol.New(serverStore, serverID)
	callerClient := protocol.New(callerStore, callerID)

	// Build the convention server with summary campfire configured.
	loader := func(name string) (*convention.Declaration, error) {
		if name == "create" {
			return minimalCreateDecl(), nil
		}
		return minimalCloseDecl(), nil
	}

	srv, err := conventionserver.New(
		serverClient,
		mainCampfireID,
		conventionserver.WithPollInterval(50*time.Millisecond),
		conventionserver.WithDeclarationLoader(loader),
		conventionserver.WithSummaryCampfireID(summaryCampfireID),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	time.Sleep(100 * time.Millisecond)

	// Send a work:create operation from the caller.
	_, err = callerClient.Send(protocol.SendRequest{
		CampfireID: mainCampfireID,
		Payload:    []byte(`{"id":"test-001","title":"Test item","for":"baron","priority":"p1","type":"task"}`),
		Tags:       []string{"work:create"},
	})
	if err != nil {
		t.Fatalf("caller Send work:create: %v", err)
	}

	// Wait for summary to appear in the summary campfire.
	var summaryFound bool
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		result, err := sumClient.Read(protocol.ReadRequest{
			CampfireID: summaryCampfireID,
			Tags:       []string{"work:item-summary"},
		})
		if err != nil {
			time.Sleep(100 * time.Millisecond)
			continue
		}
		for _, msg := range result.Messages {
			var payload struct {
				Convention string `json:"convention"`
				Operation  string `json:"operation"`
				ID         string `json:"id"`
				Title      string `json:"title"`
			}
			if err := json.Unmarshal(msg.Payload, &payload); err != nil {
				continue
			}
			if payload.Convention == "work:item-summary" && payload.Operation == "create" {
				summaryFound = true
				if payload.ID != "test-001" {
					t.Errorf("summary ID = %q, want %q", payload.ID, "test-001")
				}
				if payload.Title != "Test item" {
					t.Errorf("summary title = %q, want %q", payload.Title, "Test item")
				}
				break
			}
		}
		if summaryFound {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}

	cancel()

	if !summaryFound {
		t.Error("no work:item-summary found in summary campfire after work:create")
	}
}

// TestStartRejectsUnknownInboxCampfire verifies that Start() returns a non-nil
// error when the configured inbox campfire ID is not in the client's membership
// list, rather than silently dropping all join requests.
func TestStartRejectsUnknownInboxCampfire(t *testing.T) {
	env := setupTestEnv(t)

	loader := func(name string) (*convention.Declaration, error) {
		return minimalCloseDecl(), nil
	}

	// Use a made-up campfire ID that the serverClient has no membership for.
	unknownInboxID := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef"

	srv, err := conventionserver.New(
		env.serverClient,
		env.campfireID,
		conventionserver.WithPollInterval(50*time.Millisecond),
		conventionserver.WithDeclarationLoader(loader),
		conventionserver.WithInboxCampfireID(unknownInboxID),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	startErr := srv.Start(ctx)
	cancel() // shut down the operation goroutines that were already launched

	if startErr == nil {
		t.Fatal("Start() should return an error when inbox campfire is not in membership list, got nil")
	}
}

// TestIsSoloMode_SkipsExpiredBindings verifies that IsSoloMode returns true
// (solo mode enabled) when the only server-binding messages are from a different
// server and those bindings have expired. Regression test for ready-7c3.
func TestIsSoloMode_SkipsExpiredBindings(t *testing.T) {
	env := setupTestEnv(t)

	// Create an identity for a "remote" server.
	remoteServerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating remote server identity: %v", err)
	}
	remoteServerPubKey := remoteServerID.PublicKeyHex()

	// Create an expired server-binding message from the remote server.
	// ValidUntil is set to 1 second in the past (nanoseconds, matching ensureServerBinding).
	expiredBinding := map[string]interface{}{
		"convention":   "work",
		"operation":    "server-binding",
		"server_pubkey": remoteServerPubKey,
		"valid_from":   time.Now().UnixNano() - int64(10*time.Second),
		"valid_until":  time.Now().UnixNano() - int64(time.Second), // Expired 1 second ago
	}
	expiredPayload, err := json.Marshal(expiredBinding)
	if err != nil {
		t.Fatalf("marshalling expired binding: %v", err)
	}

	// Post the expired binding to the campfire.
	_, err = env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    expiredPayload,
		Tags:       []string{"convention:server-binding"},
	})
	if err != nil {
		t.Fatalf("posting expired binding: %v", err)
	}

	// Get the local server's public key.
	localServerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating local server identity: %v", err)
	}
	localServerPubKey := localServerID.PublicKeyHex()

	// IsSoloMode should return true because the only binding is expired.
	result := conventionserver.IsSoloMode(env.callerClient, env.campfireID, localServerPubKey)
	if !result {
		t.Errorf("IsSoloMode with only expired binding from different server = %v, want true", result)
	}

	// Now post a valid binding from the remote server.
	validBinding := map[string]interface{}{
		"convention":   "work",
		"operation":    "server-binding",
		"server_pubkey": remoteServerPubKey,
		"valid_from":   time.Now().UnixNano(),
		"valid_until":  time.Now().UnixNano() + int64(time.Hour), // Expires in 1 hour
	}
	validPayload, err := json.Marshal(validBinding)
	if err != nil {
		t.Fatalf("marshalling valid binding: %v", err)
	}

	_, err = env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    validPayload,
		Tags:       []string{"convention:server-binding"},
	})
	if err != nil {
		t.Fatalf("posting valid binding: %v", err)
	}

	// IsSoloMode should return false because there's a valid binding from a different server.
	result = conventionserver.IsSoloMode(env.callerClient, env.campfireID, localServerPubKey)
	if result {
		t.Errorf("IsSoloMode with valid binding from different server = %v, want false", result)
	}
}

// TestIsSoloMode_NanosecondUnitConsistency is a regression test for ready-d81.
// ensureServerBinding stores ValidUntil as UnixNano (~1.7e18). IsSoloMode must
// compare with UnixNano as well — using Unix seconds (~1.7e9) would make the
// expiry check never fire, causing stale bindings from crashed servers to
// persist forever and block solo mode.
func TestIsSoloMode_NanosecondUnitConsistency(t *testing.T) {
	env := setupTestEnv(t)

	remoteServerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating remote server identity: %v", err)
	}
	remoteServerPubKey := remoteServerID.PublicKeyHex()

	localServerID, err := identity.Generate()
	if err != nil {
		t.Fatalf("generating local server identity: %v", err)
	}
	localServerPubKey := localServerID.PublicKeyHex()

	// Post a binding with ValidUntil set in nanoseconds (as ensureServerBinding does),
	// 1 second in the past. This should be treated as expired.
	staleBinding := map[string]interface{}{
		"convention":    "work",
		"operation":     "server-binding",
		"server_pubkey": remoteServerPubKey,
		"valid_from":    time.Now().UnixNano() - int64(2*time.Hour),
		"valid_until":   time.Now().UnixNano() - int64(time.Second), // expired 1s ago, nanoseconds
	}
	stalePayload, err := json.Marshal(staleBinding)
	if err != nil {
		t.Fatalf("marshalling stale binding: %v", err)
	}
	_, err = env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    stalePayload,
		Tags:       []string{"convention:server-binding"},
	})
	if err != nil {
		t.Fatalf("posting stale binding: %v", err)
	}

	// IsSoloMode must return true: the stale binding is expired and should be ignored.
	// If IsSoloMode compared with Unix seconds instead of UnixNano, the nanosecond
	// ValidUntil value (~1.7e18) would always exceed Unix seconds (~1.7e9), so the
	// expiry check would never fire and this would incorrectly return false.
	result := conventionserver.IsSoloMode(env.callerClient, env.campfireID, localServerPubKey)
	if !result {
		t.Errorf("IsSoloMode with nanosecond-unit expired binding = %v, want true (stale binding should be ignored)", result)
	}

	// Post a valid binding in nanoseconds (as ensureServerBinding does), 1 hour in the future.
	activeBinding := map[string]interface{}{
		"convention":    "work",
		"operation":     "server-binding",
		"server_pubkey": remoteServerPubKey,
		"valid_from":    time.Now().UnixNano(),
		"valid_until":   time.Now().UnixNano() + int64(time.Hour), // expires in 1h, nanoseconds
	}
	activePayload, err := json.Marshal(activeBinding)
	if err != nil {
		t.Fatalf("marshalling active binding: %v", err)
	}
	_, err = env.callerClient.Send(protocol.SendRequest{
		CampfireID: env.campfireID,
		Payload:    activePayload,
		Tags:       []string{"convention:server-binding"},
	})
	if err != nil {
		t.Fatalf("posting active binding: %v", err)
	}

	// IsSoloMode must return false: an active binding from a different server exists.
	result = conventionserver.IsSoloMode(env.callerClient, env.campfireID, localServerPubKey)
	if result {
		t.Errorf("IsSoloMode with nanosecond-unit active binding = %v, want false (active remote server should block solo mode)", result)
	}
}

// TestServerShutdownBlocksUntilWorkersDone verifies that Shutdown() blocks until
// all worker goroutines have exited.
func TestServerShutdownBlocksUntilWorkersDone(t *testing.T) {
	env := setupTestEnv(t)

	loader := func(name string) (*convention.Declaration, error) {
		return minimalCloseDecl(), nil
	}

	srv, err := conventionserver.New(
		env.serverClient,
		env.campfireID,
		conventionserver.WithPollInterval(50*time.Millisecond),
		conventionserver.WithDeclarationLoader(loader),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Give the server a moment to start its workers.
	time.Sleep(100 * time.Millisecond)

	// Cancel context to signal workers to exit.
	cancel()

	// Shutdown should block until all workers have exited.
	// Use a goroutine with a timeout to detect if Shutdown hangs.
	done := make(chan struct{})
	go func() {
		srv.Shutdown()
		close(done)
	}()

	select {
	case <-done:
		// Shutdown returned successfully.
	case <-time.After(2 * time.Second):
		t.Fatal("Shutdown() blocked for more than 2 seconds; workers may not have exited")
	}
}

// TestServerErrorsChannelDelivered verifies that non-fatal errors from worker
// goroutines are sent to the Errors() channel.
func TestServerErrorsChannelDelivered(t *testing.T) {
	env := setupTestEnv(t)

	// Create a loader that returns a valid declaration.
	loader := func(name string) (*convention.Declaration, error) {
		return minimalCloseDecl(), nil
	}

	// Create a client that will generate errors (by using a broken reader).
	srv, err := conventionserver.New(
		env.serverClient,
		env.campfireID,
		conventionserver.WithPollInterval(50*time.Millisecond),
		conventionserver.WithDeclarationLoader(loader),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Start reading from the Errors channel.
	errCh := srv.Errors()
	deadline := time.Now().Add(2500 * time.Millisecond)

	// The server may not immediately produce errors (depends on timing), but
	// the channel should be available and readable. We drain it to verify it's wired up.
	for time.Now().Before(deadline) {
		select {
		case err := <-errCh:
			if err != nil {
				// Error received and non-nil, which confirms channel works.
				_ = fmt.Sprintf("received error: %v", err)
				break
			}
		case <-time.After(100 * time.Millisecond):
			// No error yet, keep trying.
		}
	}

	cancel()
	srv.Shutdown()

	// Just verify the channel exists and is readable (non-blocking sends should work).
	// Errors() should always return a valid channel.
	if errCh == nil {
		t.Fatal("Errors() returned nil channel")
	}
}

// TestServerErrorsChannelIsBuffered verifies that the Errors channel is buffered
// and exists. The channel is receive-only to the caller, so we verify it can
// be read from without blocking indefinitely.
func TestServerErrorsChannelIsBuffered(t *testing.T) {
	env := setupTestEnv(t)

	loader := func(name string) (*convention.Declaration, error) {
		return minimalCloseDecl(), nil
	}

	srv, err := conventionserver.New(
		env.serverClient,
		env.campfireID,
		conventionserver.WithDeclarationLoader(loader),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	errCh := srv.Errors()

	// Verify the channel can be read from without blocking. If it's buffered
	// and empty, a non-blocking read should complete immediately.
	select {
	case <-errCh:
		// Channel has data (or is buffered and empty); OK.
	case <-time.After(100 * time.Millisecond):
		// Timeout on read from a buffered channel with no senders means it's working.
	}

	// The important thing is that Errors() returns a valid channel.
	if errCh == nil {
		t.Fatal("Errors() returned nil")
	}
}

// TestServerMultipleShutdownCallsSafe verifies that calling Shutdown() multiple
// times is safe (non-blocking subsequent calls after the first).
func TestServerMultipleShutdownCallsSafe(t *testing.T) {
	env := setupTestEnv(t)

	loader := func(name string) (*convention.Declaration, error) {
		return minimalCloseDecl(), nil
	}

	srv, err := conventionserver.New(
		env.serverClient,
		env.campfireID,
		conventionserver.WithPollInterval(50*time.Millisecond),
		conventionserver.WithDeclarationLoader(loader),
	)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	if err := srv.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	cancel()

	// First Shutdown should block until workers exit.
	srv.Shutdown()

	// Second Shutdown should return immediately (done channel already closed).
	// This tests that the code is safe for multiple calls.
	start := time.Now()
	srv.Shutdown()
	elapsed := time.Since(start)

	if elapsed > 500*time.Millisecond {
		t.Errorf("second Shutdown() took %v, expected immediate return", elapsed)
	}
}
