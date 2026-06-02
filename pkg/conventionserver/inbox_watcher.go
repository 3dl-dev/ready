package conventionserver

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// inboxWatcherReader is the interface the inbox watcher uses to read from
// the inbox campfire and send to the project campfire. *protocol.Client satisfies
// this interface; tests inject a fake.
type inboxWatcherReader interface {
	Read(req protocol.ReadRequest) (*protocol.ReadResult, error)
	Send(req protocol.SendRequest) (*message.Message, error)
}

// JoinRequestPayload is the payload of an incoming work:join-request message
// received on the inbox campfire.
type JoinRequestPayload struct {
	Pubkey                           string `json:"pubkey"`
	RequestedRole                    string `json:"requested_role"`
	OptionalAttestations             string `json:"optional_attestations,omitempty"`
	OptionalJoinConversationCampfire string `json:"optional_join_conversation_campfire,omitempty"`
}

// inboxWatcher watches the inbox campfire for work:join-request messages and
// materializes them as work:join-request items in the project campfire.
type inboxWatcher struct {
	reader          inboxWatcherReader
	inboxCampfire   string
	projectCampfire string
	pollInterval    time.Duration
	rateLimit       *joinRateLimiter

	// errCh receives non-fatal errors from poll and handleJoinRequest.
	// May be nil; sends are always non-blocking.
	errCh chan<- error

	// cursor is the last-seen message timestamp; only messages after this are
	// processed on subsequent polls.
	cursor int64
}

// run polls the inbox campfire on a timer until ctx is cancelled.
func (w *inboxWatcher) run(ctx context.Context) {
	ticker := time.NewTicker(w.pollInterval)
	defer ticker.Stop()

	// Poll once immediately on start, then on each tick.
	w.poll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			w.poll(ctx)
		}
	}
}

// poll reads new join-request messages from the inbox campfire and materializes
// work:join-request items in the project campfire.
// Note: protocol.Client.Read does not accept a context; cancellation relies on
// the caller's timer loop checking ctx.Done() between polls.
func (w *inboxWatcher) poll(_ context.Context) {
	result, err := w.reader.Read(protocol.ReadRequest{
		CampfireID:     w.inboxCampfire,
		Tags:           []string{"work:join-request"},
		AfterTimestamp: w.cursor,
	})
	if err != nil {
		w.sendErr(fmt.Errorf("inbox_watcher: reading inbox campfire: %w", err))
		return
	}

	for _, msg := range result.Messages {
		if err := w.handleJoinRequest(msg); err != nil {
			w.sendErr(err)
		}
	}

	// Advance cursor to skip processed messages on next poll.
	if result.MaxTimestamp > w.cursor {
		w.cursor = result.MaxTimestamp
	}
}

// sendErr sends err to errCh without blocking. If the channel is nil or full,
// the error is silently dropped.
func (w *inboxWatcher) sendErr(err error) {
	if w.errCh == nil {
		return
	}
	select {
	case w.errCh <- err:
	default:
	}
}

// handleJoinRequest processes a single join-request message: applies rate
// limiting, then materializes a work:join-request item in the project campfire.
func (w *inboxWatcher) handleJoinRequest(msg protocol.Message) error {
	// Apply rate limit: N requests per hour per source pubkey.
	if !w.rateLimit.Allow(msg.Sender) {
		return fmt.Errorf("inbox_watcher: rate limit exceeded for pubkey %s", shortKey(msg.Sender))
	}

	// Parse the payload to extract join-request fields.
	var payload JoinRequestPayload
	if err := json.Unmarshal(msg.Payload, &payload); err != nil {
		return fmt.Errorf("inbox_watcher: parsing join-request payload: %w", err)
	}

	// Validate pubkey format — must be a 64-char hex string.
	// Reject malformed pubkeys before materializing them into the project campfire.
	if len(payload.Pubkey) != 64 || !isHexString(payload.Pubkey) {
		return fmt.Errorf("inbox_watcher: join-request has invalid pubkey format %q (from sender %s)", payload.Pubkey, shortKey(msg.Sender))
	}

	// Materialize a work:join-request item in the project campfire.
	itemPayload := map[string]any{
		"pubkey":         payload.Pubkey,
		"requested_role": payload.RequestedRole,
	}
	if payload.OptionalAttestations != "" {
		itemPayload["optional_attestations"] = payload.OptionalAttestations
	}
	if payload.OptionalJoinConversationCampfire != "" {
		itemPayload["optional_join_conversation_campfire"] = payload.OptionalJoinConversationCampfire
	}

	payloadBytes, err := json.Marshal(itemPayload)
	if err != nil {
		return fmt.Errorf("inbox_watcher: encoding item payload: %w", err)
	}

	_, err = w.reader.Send(protocol.SendRequest{
		CampfireID: w.projectCampfire,
		Payload:    payloadBytes,
		Tags:       []string{"work:join-request"},
	})
	if err != nil {
		return fmt.Errorf("inbox_watcher: materializing join-request item: %w", err)
	}

	return nil
}

// shortKey returns the first 12 characters of a hex pubkey for log messages.
func shortKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12] + "..."
}

// joinRateLimiter enforces a per-pubkey sliding-window rate limit.
// The window is 1 hour; max is the maximum number of requests allowed per window.
type joinRateLimiter struct {
	mu      sync.Mutex
	max     int
	window  time.Duration
	buckets map[string][]time.Time
	nowFunc func() time.Time // injectable for tests
}

// newJoinRateLimiter creates a rate limiter allowing at most max requests per
// pubkey per hour.
func newJoinRateLimiter(max int) *joinRateLimiter {
	return &joinRateLimiter{
		max:     max,
		window:  time.Hour,
		buckets: make(map[string][]time.Time),
		nowFunc: time.Now,
	}
}

// Allow reports whether the given pubkey is allowed to make another request.
// It returns true and records the request if within the limit, or false if
// the limit has been exceeded.
func (r *joinRateLimiter) Allow(pubkey string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()

	now := r.nowFunc()
	cutoff := now.Add(-r.window)

	// Evict expired timestamps.
	existing := r.buckets[pubkey]
	fresh := existing[:0]
	for _, t := range existing {
		if t.After(cutoff) {
			fresh = append(fresh, t)
		}
	}

	if len(fresh) == 0 {
		delete(r.buckets, pubkey)
	} else {
		r.buckets[pubkey] = fresh
	}

	if len(fresh) >= r.max {
		return false
	}

	r.buckets[pubkey] = append(fresh, now)
	return true
}

// isHexString returns true if s consists entirely of hex characters (0-9, a-f, A-F).
func isHexString(s string) bool {
	for _, c := range s {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || (c >= 'A' && c <= 'F')) {
			return false
		}
	}
	return true
}
