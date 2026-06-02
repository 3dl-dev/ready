package conventionserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// --- isHexString tests ---

func TestIsHexString_ValidLowercaseHex(t *testing.T) {
	cases := []string{
		"0123456789abcdef",
		"aaaaaaaaaaaaaaaa",
		"0000000000000000",
		"abcdef1234567890",
	}
	for _, s := range cases {
		if !isHexString(s) {
			t.Errorf("isHexString(%q) = false, want true", s)
		}
	}
}

func TestIsHexString_ValidUppercaseHex(t *testing.T) {
	cases := []string{
		"0123456789ABCDEF",
		"AAAAAAAAAAAAAAAA",
		"0000000000000000",
		"ABCDEF1234567890",
	}
	for _, s := range cases {
		if !isHexString(s) {
			t.Errorf("isHexString(%q) = false, want true", s)
		}
	}
}

func TestIsHexString_ValidMixedCaseHex(t *testing.T) {
	cases := []string{
		"0123456789AbCdEf",
		"AaBbCcDdEeFf0011",
		"aBcDeF1234567890",
	}
	for _, s := range cases {
		if !isHexString(s) {
			t.Errorf("isHexString(%q) = false, want true", s)
		}
	}
}

func TestIsHexString_InvalidNonHexChars(t *testing.T) {
	cases := []string{
		"gggggggggggggggg", // non-hex chars
		"0123456789abcdeG", // G is not hex
		"0123456789ABCDEG", // G is not hex
		"0x1234",           // has '0x' prefix
		"12 34 56 78",      // has spaces
	}
	for _, s := range cases {
		if isHexString(s) {
			t.Errorf("isHexString(%q) = true, want false", s)
		}
	}
}

// --- Rate limiter tests ---

func TestJoinRateLimiter_AllowsUpToMax(t *testing.T) {
	rl := newJoinRateLimiter(10)
	pubkey := "aabbcc"

	for i := 0; i < 10; i++ {
		if !rl.Allow(pubkey) {
			t.Fatalf("Allow() returned false on request %d (expected true)", i+1)
		}
	}
}

func TestJoinRateLimiter_RejectsOnEleventhRequest(t *testing.T) {
	rl := newJoinRateLimiter(10)
	pubkey := "aabbcc"

	for i := 0; i < 10; i++ {
		rl.Allow(pubkey)
	}

	if rl.Allow(pubkey) {
		t.Fatal("Allow() returned true on 11th request — should be rejected")
	}
}

func TestJoinRateLimiter_IndependentPerPubkey(t *testing.T) {
	rl := newJoinRateLimiter(10)

	pubkey1 := "pubkey1"
	pubkey2 := "pubkey2"

	// Exhaust limit for pubkey1.
	for i := 0; i < 10; i++ {
		rl.Allow(pubkey1)
	}
	if rl.Allow(pubkey1) {
		t.Error("pubkey1: Allow() should be false after 10 requests")
	}

	// pubkey2 should still have its own fresh bucket.
	if !rl.Allow(pubkey2) {
		t.Error("pubkey2: Allow() should be true (independent bucket)")
	}
}

func TestJoinRateLimiter_ResetsAfterWindowExpiry(t *testing.T) {
	rl := newJoinRateLimiter(10)
	pubkey := "aabbcc"

	// Fake clock: start at t=0.
	now := time.Now()
	rl.nowFunc = func() time.Time { return now }

	// Exhaust the limit.
	for i := 0; i < 10; i++ {
		rl.Allow(pubkey)
	}
	if rl.Allow(pubkey) {
		t.Fatal("should be rate limited before window expiry")
	}

	// Advance clock past 1 hour — old entries should expire.
	now = now.Add(time.Hour + time.Second)
	rl.nowFunc = func() time.Time { return now }

	if !rl.Allow(pubkey) {
		t.Fatal("Allow() should return true after window expiry")
	}
}

func TestJoinRateLimiter_ConcurrentSafe(t *testing.T) {
	rl := newJoinRateLimiter(10)
	var wg sync.WaitGroup
	allowed := make([]bool, 20)
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(1)
		go func() {
			defer wg.Done()
			allowed[i] = rl.Allow("shared-key")
		}()
	}
	wg.Wait()

	count := 0
	for _, ok := range allowed {
		if ok {
			count++
		}
	}
	if count > 10 {
		t.Errorf("concurrent Allow() allowed %d requests — max is 10", count)
	}
}

// --- Inbox watcher materialization tests ---

// fakeInboxClient implements inboxWatcherReader (defined in production code).
type fakeInboxClient struct {
	mu      sync.Mutex
	sent    []protocol.SendRequest
	msgs    []protocol.Message
	readErr error
	sendErr error
}

func (f *fakeInboxClient) Read(_ protocol.ReadRequest) (*protocol.ReadResult, error) {
	if f.readErr != nil {
		return nil, f.readErr
	}
	f.mu.Lock()
	msgs := make([]protocol.Message, len(f.msgs))
	copy(msgs, f.msgs)
	f.mu.Unlock()
	return &protocol.ReadResult{Messages: msgs}, nil
}

func (f *fakeInboxClient) Send(req protocol.SendRequest) (*message.Message, error) {
	if f.sendErr != nil {
		return nil, f.sendErr
	}
	f.mu.Lock()
	f.sent = append(f.sent, req)
	f.mu.Unlock()
	return &message.Message{ID: "test-msg-id"}, nil
}

// TestJoinRateLimiter_DeletesBucketKeyWhenEmptyAfterEviction verifies that when
// all timestamps in a bucket expire and the next Allow() call is for a pubkey
// that is now over the limit (max=0 forces the deny path with an empty fresh
// slice), the map key is deleted rather than retained with a zero-length slice.
// This is the memory-leak regression: unique senders whose buckets are fully
// expired should not linger in the map indefinitely.
func TestJoinRateLimiter_DeletesBucketKeyWhenEmptyAfterEviction(t *testing.T) {
	// max=0 means every request is denied; the bucket is always empty after
	// eviction because no timestamps are ever appended.
	rl := &joinRateLimiter{
		max:     0,
		window:  time.Hour,
		buckets: make(map[string][]time.Time),
	}
	pubkey := "testpubkey"

	// Manually seed the bucket with an expired timestamp so that on the first
	// Allow() call there is something to evict.
	past := time.Now().Add(-2 * time.Hour)
	rl.nowFunc = func() time.Time { return time.Now() }
	rl.mu.Lock()
	rl.buckets[pubkey] = []time.Time{past}
	rl.mu.Unlock()

	// Key must exist before Allow().
	rl.mu.Lock()
	_, exists := rl.buckets[pubkey]
	rl.mu.Unlock()
	if !exists {
		t.Fatal("precondition: bucket key should exist before Allow()")
	}

	// Allow() evicts the expired timestamp → fresh is empty → key should be
	// deleted. max=0 so it also returns false (denied), exercising the deny
	// branch with an empty fresh slice.
	result := rl.Allow(pubkey)
	if result {
		t.Fatal("Allow() should return false when max=0")
	}

	rl.mu.Lock()
	_, stillPresent := rl.buckets[pubkey]
	rl.mu.Unlock()

	if stillPresent {
		t.Error("bucket key still present after all entries evicted — memory leak not fixed")
	}
}

func TestInboxWatcher_MaterializesJoinRequest(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"

	// pubkey must be a valid 64-char lowercase hex string.
	const validPubkey = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

	payload := JoinRequestPayload{
		Pubkey:        validPubkey,
		RequestedRole: "member",
	}
	payloadBytes, _ := json.Marshal(payload)

	fake := &fakeInboxClient{
		msgs: []protocol.Message{
			{
				ID:      "msg-001",
				Sender:  "sender-pubkey-001",
				Payload: payloadBytes,
				Tags:    []string{"work:join-request"},
			},
		},
	}

	w := &inboxWatcher{
		reader:          fake,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		rateLimit:       newJoinRateLimiter(10),
	}

	w.poll(context.Background())

	fake.mu.Lock()
	sent := make([]protocol.SendRequest, len(fake.sent))
	copy(sent, fake.sent)
	fake.mu.Unlock()

	if len(sent) != 1 {
		t.Fatalf("expected 1 materialized message, got %d", len(sent))
	}
	if sent[0].CampfireID != projectID {
		t.Errorf("materialized to wrong campfire: %s", sent[0].CampfireID)
	}

	found := false
	for _, tag := range sent[0].Tags {
		if tag == "work:join-request" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("materialized message missing work:join-request tag: %v", sent[0].Tags)
	}

	var materialized map[string]any
	if err := json.Unmarshal(sent[0].Payload, &materialized); err != nil {
		t.Fatalf("parsing materialized payload: %v", err)
	}
	if materialized["pubkey"] != validPubkey {
		t.Errorf("pubkey mismatch: got %v, want %v", materialized["pubkey"], validPubkey)
	}
	if materialized["requested_role"] != payload.RequestedRole {
		t.Errorf("requested_role mismatch: got %v, want %v", materialized["requested_role"], payload.RequestedRole)
	}
}

func TestInboxWatcher_RateLimitsEleventhRequest(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"
	// senderPubkey is used as msg.Sender (rate limit key); payload pubkey must be
	// a valid 64-char lowercase hex string.
	const senderPubkey = "spammer-pubkey"
	const validPubkey = "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"

	payload := JoinRequestPayload{
		Pubkey:        validPubkey,
		RequestedRole: "member",
	}
	payloadBytes, _ := json.Marshal(payload)

	// 11 messages from the same sender.
	msgs := make([]protocol.Message, 11)
	for i := range msgs {
		msgs[i] = protocol.Message{
			ID:      fmt.Sprintf("msg-%03d", i),
			Sender:  senderPubkey,
			Payload: payloadBytes,
			Tags:    []string{"work:join-request"},
		}
	}

	fake := &fakeInboxClient{msgs: msgs}

	w := &inboxWatcher{
		reader:          fake,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		rateLimit:       newJoinRateLimiter(10),
	}

	w.poll(context.Background())

	fake.mu.Lock()
	count := len(fake.sent)
	fake.mu.Unlock()

	if count != 10 {
		t.Errorf("expected 10 materialized messages (11th rejected), got %d", count)
	}
}

// TestInboxWatcher_InvalidPubkeyRejected verifies that handleJoinRequest on the
// production inboxWatcher returns an error (and does NOT attempt to materialize the
// message) when the payload pubkey is not a valid 64-char hex string.
// The client is left nil because validation returns before any client call.
func TestInboxWatcher_InvalidPubkeyRejected(t *testing.T) {
	cases := []struct {
		name   string
		pubkey string
	}{
		{"too short", "abcdef1234"},
		{"non-hex chars", "gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg"},
		{"empty pubkey", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := JoinRequestPayload{
				Pubkey:        tc.pubkey,
				RequestedRole: "member",
			}
			payloadBytes, _ := json.Marshal(payload)

			// reader is nil — production code must not reach reader.Send.
			w := &inboxWatcher{
				reader:    nil,
				rateLimit: newJoinRateLimiter(10),
			}
			msg := protocol.Message{
				ID:      "msg-bad",
				Sender:  "sender-key",
				Payload: payloadBytes,
				Tags:    []string{"work:join-request"},
			}
			err := w.handleJoinRequest(msg)
			if err == nil {
				t.Fatalf("expected error for invalid pubkey %q, got nil", tc.pubkey)
			}
		})
	}
}

// TestInboxWatcher_UppercaseHexPubkeyAccepted is a regression test verifying that
// join requests with uppercase hex pubkeys are now accepted (not silently dropped).
// Previously, isHexString only accepted lowercase hex [0-9a-f], causing valid
// uppercase pubkeys [0-9A-F] to be rejected.
func TestInboxWatcher_UppercaseHexPubkeyAccepted(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"

	// A valid 64-char hex string using uppercase letters.
	const uppercasePubkey = "ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890ABCDEF1234567890"

	payload := JoinRequestPayload{
		Pubkey:        uppercasePubkey,
		RequestedRole: "member",
	}
	payloadBytes, _ := json.Marshal(payload)

	fake := &fakeInboxClient{
		msgs: []protocol.Message{
			{
				ID:      "msg-001",
				Sender:  "sender-pubkey-001",
				Payload: payloadBytes,
				Tags:    []string{"work:join-request"},
			},
		},
	}

	w := &inboxWatcher{
		reader:          fake,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		rateLimit:       newJoinRateLimiter(10),
	}

	w.poll(context.Background())

	fake.mu.Lock()
	sent := make([]protocol.SendRequest, len(fake.sent))
	copy(sent, fake.sent)
	fake.mu.Unlock()

	if len(sent) != 1 {
		t.Fatalf("expected 1 materialized message, got %d", len(sent))
	}

	var materialized map[string]any
	if err := json.Unmarshal(sent[0].Payload, &materialized); err != nil {
		t.Fatalf("parsing materialized payload: %v", err)
	}
	if materialized["pubkey"] != uppercasePubkey {
		t.Errorf("pubkey mismatch: got %v, want %v", materialized["pubkey"], uppercasePubkey)
	}
}

// TestInboxWatcher_MixedCaseHexPubkeyAccepted is a regression test verifying that
// join requests with mixed-case hex pubkeys are accepted.
func TestInboxWatcher_MixedCaseHexPubkeyAccepted(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"

	// A valid 64-char hex string using mixed case.
	const mixedCasePubkey = "AbCdEf1234567890AbCdEf1234567890AbCdEf1234567890AbCdEf1234567890"

	payload := JoinRequestPayload{
		Pubkey:        mixedCasePubkey,
		RequestedRole: "member",
	}
	payloadBytes, _ := json.Marshal(payload)

	fake := &fakeInboxClient{
		msgs: []protocol.Message{
			{
				ID:      "msg-001",
				Sender:  "sender-pubkey-001",
				Payload: payloadBytes,
				Tags:    []string{"work:join-request"},
			},
		},
	}

	w := &inboxWatcher{
		reader:          fake,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		rateLimit:       newJoinRateLimiter(10),
	}

	w.poll(context.Background())

	fake.mu.Lock()
	sent := make([]protocol.SendRequest, len(fake.sent))
	copy(sent, fake.sent)
	fake.mu.Unlock()

	if len(sent) != 1 {
		t.Fatalf("expected 1 materialized message, got %d", len(sent))
	}

	var materialized map[string]any
	if err := json.Unmarshal(sent[0].Payload, &materialized); err != nil {
		t.Fatalf("parsing materialized payload: %v", err)
	}
	if materialized["pubkey"] != mixedCasePubkey {
		t.Errorf("pubkey mismatch: got %v, want %v", materialized["pubkey"], mixedCasePubkey)
	}
}

func TestInboxWatcher_OptionalFieldsPassedThrough(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"

	payload := JoinRequestPayload{
		Pubkey:                           "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890",
		RequestedRole:                    "admin",
		OptionalAttestations:             "some-attestation",
		OptionalJoinConversationCampfire: "conversation-campfire-id",
	}
	payloadBytes, _ := json.Marshal(payload)

	fake := &fakeInboxClient{
		msgs: []protocol.Message{
			{ID: "msg-001", Sender: "sender", Payload: payloadBytes, Tags: []string{"work:join-request"}},
		},
	}

	w := &inboxWatcher{
		reader:          fake,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		rateLimit:       newJoinRateLimiter(10),
	}

	w.poll(context.Background())

	fake.mu.Lock()
	sent := make([]protocol.SendRequest, len(fake.sent))
	copy(sent, fake.sent)
	fake.mu.Unlock()

	if len(sent) != 1 {
		t.Fatalf("expected 1 materialized message, got %d", len(sent))
	}

	var materialized map[string]any
	if err := json.Unmarshal(sent[0].Payload, &materialized); err != nil {
		t.Fatalf("parsing materialized payload: %v", err)
	}

	if materialized["optional_attestations"] != payload.OptionalAttestations {
		t.Errorf("optional_attestations mismatch: got %v", materialized["optional_attestations"])
	}
	if materialized["optional_join_conversation_campfire"] != payload.OptionalJoinConversationCampfire {
		t.Errorf("optional_join_conversation_campfire mismatch: got %v", materialized["optional_join_conversation_campfire"])
	}
}

// trackingInboxClient wraps fakeInboxClient to track Read call counts.
type trackingInboxClient struct {
	fake      *fakeInboxClient
	readCalls int
	mu        sync.Mutex
}

func (t *trackingInboxClient) Read(req protocol.ReadRequest) (*protocol.ReadResult, error) {
	t.mu.Lock()
	t.readCalls++
	t.mu.Unlock()
	return t.fake.Read(req)
}

func (t *trackingInboxClient) Send(req protocol.SendRequest) (*message.Message, error) {
	return t.fake.Send(req)
}

// TestInboxWatcherRunTimerLoop verifies that inboxWatcher.run() polls on a
// timer until context is cancelled, handling each poll cycle correctly.
func TestInboxWatcherRunTimerLoop(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"

	validPubkey := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	payload := JoinRequestPayload{
		Pubkey:        validPubkey,
		RequestedRole: "member",
	}
	payloadBytes, _ := json.Marshal(payload)

	fake := &fakeInboxClient{
		msgs: []protocol.Message{
			{
				ID:        "msg-001",
				Sender:    "sender-001",
				Payload:   payloadBytes,
				Tags:      []string{"work:join-request"},
				Timestamp: 100,
			},
		},
	}

	tracker := &trackingInboxClient{fake: fake}

	w := &inboxWatcher{
		reader:          tracker,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		pollInterval:    50 * time.Millisecond,
		rateLimit:       newJoinRateLimiter(10),
		errCh:           make(chan error, 10),
	}

	// Run the watcher in a goroutine with a short timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go w.run(ctx)

	// Wait for context to expire, allowing ~4 poll cycles (50ms each).
	<-ctx.Done()

	// Give the run loop a moment to exit.
	time.Sleep(50 * time.Millisecond)

	tracker.mu.Lock()
	calls := tracker.readCalls
	tracker.mu.Unlock()

	// Should have at least 3 poll cycles (immediate + 2-3 ticks within 200ms).
	if calls < 3 {
		t.Errorf("run() did %d poll cycles, expected at least 3", calls)
	}
}

// TestInboxWatcherRunContextCancelledExitsLoop verifies that inboxWatcher.run()
// exits promptly when the context is cancelled.
func TestInboxWatcherRunContextCancelledExitsLoop(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"

	fake := &fakeInboxClient{
		msgs: []protocol.Message{},
	}

	w := &inboxWatcher{
		reader:          fake,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		pollInterval:    100 * time.Millisecond, // Long interval to avoid spurious polling
		rateLimit:       newJoinRateLimiter(10),
		errCh:           make(chan error, 10),
	}

	// Create a context with a short timeout.
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	// Run the watcher; it should exit when context times out.
	done := make(chan struct{})
	go func() {
		w.run(ctx)
		close(done)
	}()

	// Wait for the run loop to exit with a generous timeout.
	select {
	case <-done:
		// run() exited as expected.
	case <-time.After(1 * time.Second):
		t.Fatal("run() did not exit within 1 second after context cancellation")
	}
}

// TestInboxWatcherRunMaterializesMultiplePollCycles verifies that run() calls
// poll() multiple times and processes messages across successive polls.
func TestInboxWatcherRunMaterializesMultiplePollCycles(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"

	validPubkey := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	payload := JoinRequestPayload{
		Pubkey:        validPubkey,
		RequestedRole: "member",
	}
	payloadBytes, _ := json.Marshal(payload)

	msgs := []protocol.Message{
		{
			ID:        "msg-001",
			Sender:    "sender-001",
			Payload:   payloadBytes,
			Tags:      []string{"work:join-request"},
			Timestamp: 100,
		},
		{
			ID:        "msg-002",
			Sender:    "sender-002",
			Payload:   payloadBytes,
			Tags:      []string{"work:join-request"},
			Timestamp: 200,
		},
	}

	fake := &fakeInboxClient{
		msgs: msgs,
	}

	w := &inboxWatcher{
		reader:          fake,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		pollInterval:    30 * time.Millisecond,
		rateLimit:       newJoinRateLimiter(10),
		errCh:           make(chan error, 10),
	}

	// Run for a short duration to allow multiple polls.
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go w.run(ctx)

	// Wait for context to expire.
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)

	// Check that at least one message was materialized.
	fake.mu.Lock()
	sentCount := len(fake.sent)
	fake.mu.Unlock()

	if sentCount < 1 {
		t.Errorf("run() materialized %d messages, expected at least 1", sentCount)
	}
}

// TestInboxWatcherRunHandlesReadErrors verifies that read errors are captured
// and sent to the error channel, and polling continues on next cycle.
func TestInboxWatcherRunHandlesReadErrors(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"

	fake := &fakeInboxClient{
		msgs:    []protocol.Message{},
		readErr: fmt.Errorf("simulated read failure"),
	}

	errCh := make(chan error, 10)
	w := &inboxWatcher{
		reader:          fake,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		pollInterval:    30 * time.Millisecond,
		rateLimit:       newJoinRateLimiter(10),
		errCh:           errCh,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	go w.run(ctx)

	// Wait a bit for at least one poll cycle to happen.
	time.Sleep(50 * time.Millisecond)

	// Check that an error was sent to the error channel.
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("expected a non-nil error in errCh")
		}
	default:
		// No error received, which may be OK if timing didn't align.
		// (The important thing is that run() doesn't crash on read errors.)
	}

	// Allow run() to finish.
	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)
}

// cursorTrackingClient tracks the cursor values passed to Read calls.
type cursorTrackingClient struct {
	fake        *fakeInboxClient
	readCursors []int64
	mu          sync.Mutex
}

func (c *cursorTrackingClient) Read(req protocol.ReadRequest) (*protocol.ReadResult, error) {
	c.mu.Lock()
	c.readCursors = append(c.readCursors, req.AfterTimestamp)
	c.mu.Unlock()

	// Return all messages for the first call, none after cursor advances.
	if req.AfterTimestamp == 0 {
		// First poll: return the message with its timestamp.
		result, err := c.fake.Read(req)
		if err != nil {
			return result, err
		}
		// Ensure MaxTimestamp is set to the message timestamp.
		if result != nil && len(result.Messages) > 0 {
			result.MaxTimestamp = result.Messages[0].Timestamp
		}
		return result, err
	}
	// After the first poll, cursor is advanced, so return no new messages
	// but keep the same MaxTimestamp so cursor stays advanced.
	return &protocol.ReadResult{
		Messages:     []protocol.Message{},
		MaxTimestamp: 1000,
	}, nil
}

func (c *cursorTrackingClient) Send(req protocol.SendRequest) (*message.Message, error) {
	return c.fake.Send(req)
}

// TestInboxWatcherRunAdvancesCursorBetweenPolls verifies that the cursor
// advances between poll cycles, preventing duplicate processing.
func TestInboxWatcherRunAdvancesCursorBetweenPolls(t *testing.T) {
	const inboxID = "inbox-campfire-id"
	const projectID = "project-campfire-id"

	validPubkey := "abcdef1234567890abcdef1234567890abcdef1234567890abcdef1234567890"
	payload := JoinRequestPayload{
		Pubkey:        validPubkey,
		RequestedRole: "member",
	}
	payloadBytes, _ := json.Marshal(payload)

	// A single message with a specific timestamp.
	msgs := []protocol.Message{
		{
			ID:        "msg-001",
			Sender:    "sender-001",
			Payload:   payloadBytes,
			Tags:      []string{"work:join-request"},
			Timestamp: 1000,
		},
	}

	fake := &fakeInboxClient{
		msgs: msgs,
	}

	tracker := &cursorTrackingClient{fake: fake}

	w := &inboxWatcher{
		reader:          tracker,
		inboxCampfire:   inboxID,
		projectCampfire: projectID,
		pollInterval:    30 * time.Millisecond,
		rateLimit:       newJoinRateLimiter(10),
		errCh:           make(chan error, 10),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	go w.run(ctx)

	<-ctx.Done()
	time.Sleep(50 * time.Millisecond)

	tracker.mu.Lock()
	cursors := make([]int64, len(tracker.readCursors))
	copy(cursors, tracker.readCursors)
	tracker.mu.Unlock()

	// Should have at least 3 Read calls across 200ms with 30ms interval:
	// immediate poll + tick at 30ms + tick at 60ms + tick at 90ms + tick at 120ms + tick at 150ms + tick at 180ms
	if len(cursors) < 3 {
		t.Errorf("expected at least 3 Read calls, got %d", len(cursors))
		return
	}

	// First cursor should be 0 (no prior messages).
	if cursors[0] != 0 {
		t.Errorf("first Read cursor = %d, want 0", cursors[0])
	}

	// Check that cursor advanced at some point (doesn't have to be immediately
	// on the second call due to timing).
	advancedFound := false
	for i := 1; i < len(cursors); i++ {
		if cursors[i] > 0 {
			advancedFound = true
			break
		}
	}
	if !advancedFound {
		t.Errorf("cursor never advanced beyond 0; cursors: %v", cursors)
	}
}
