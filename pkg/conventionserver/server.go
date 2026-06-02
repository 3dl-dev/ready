// Package conventionserver provides an in-process convention server for the
// solo rd workflow. It boots alongside rd commands and fulfills work convention
// operations (close, delegate, gate-resolve, role-grant, create, claim, update,
// block, unblock, engage, gate, status) so that the local user's operations are
// self-authorized without needing a remote server.
//
// Design:
//   - One goroutine per operation, each backed by a convention.Server instance.
//   - Ephemeral ed25519 signing key per rd invocation (not persisted).
//   - Solo mode: all operations from any sender are fulfilled (local user IS the maintainer).
//   - Posts a convention:server-binding message on Start() if one is not already present.
//   - TTL for server-binding: 1 hour (covers long-running rd commands).
//   - On work:create, work:close, work:claim: writes a work:item-summary to the
//     bound summary campfire (if configured via WithSummaryCampfireID).
package conventionserver

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

// authorizationMatrix maps operation name to its minimum operator level.
// Level 2 = maintainer. Level 1 = member.
var authorizationMatrix = map[string]int{
	// Level 2 — maintainer operations
	"close":        2,
	"delegate":     2,
	"gate-resolve": 2,
	"role-grant":   2,
	// Level 1 — member operations
	"create":  1,
	"claim":   1,
	"update":  1,
	"block":   1,
	"unblock": 1,
	"engage":  1,
	"gate":    1,
	"status":  1,
}

// allOperations lists all operations the in-process server handles.
var allOperations = []string{
	"close", "delegate", "gate-resolve", "role-grant",
	"create", "claim", "update", "block", "unblock", "engage", "gate", "status",
}

// summaryOperations are the operations that trigger a work:item-summary write
// to the bound summary campfire.
var summaryOperations = map[string]bool{
	"create": true,
	"close":  true,
	"claim":  true,
}

// Server is the in-process convention server for solo rd workflows.
type Server struct {
	client     *protocol.Client
	campfireID string

	// summaryCampfireID, when non-empty, is the shadow summary campfire that
	// receives work:item-summary projections on every consequential operation.
	summaryCampfireID string

	// inboxCampfireID, when non-empty, is the maintainer inbox campfire that
	// receives incoming join-request messages. The server watches this campfire
	// and materializes work:join-request items in the project campfire.
	inboxCampfireID string

	// joinRateLimit is the max join requests per hour per source pubkey.
	// Defaults to 10.
	joinRateLimit int

	// ephemeral signing key — generated fresh per rd invocation, not persisted.
	pubKey  ed25519.PublicKey
	privKey ed25519.PrivateKey

	// declarationLoader loads a parsed convention declaration by operation name.
	// Defaults to the embedded declarations package; injectable for tests.
	declarationLoader func(name string) (*convention.Declaration, error)

	// pollInterval controls how often each Server instance polls for messages.
	pollInterval time.Duration

	// errCh receives non-fatal errors from serveOperation goroutines and the
	// inbox watcher. Buffered; sends are non-blocking. Callers opt in via Errors().
	errCh chan error

	// done is closed after all worker goroutines have exited (after Shutdown returns).
	done chan struct{}

	// gate is the cf-authority grant gate (ready-197), built in Start from the
	// campfire creator as root principal. Nil means serve un-gated (legacy).
	gate convention.GateEvaluator

	// wg tracks all running worker goroutines.
	wg sync.WaitGroup
}

// ServerOption configures a Server.
type ServerOption func(*Server)

// WithPollInterval sets the polling interval for all operation servers.
func WithPollInterval(d time.Duration) ServerOption {
	return func(s *Server) { s.pollInterval = d }
}

// WithDeclarationLoader injects a custom declaration loader (for testing).
func WithDeclarationLoader(fn func(name string) (*convention.Declaration, error)) ServerOption {
	return func(s *Server) { s.declarationLoader = fn }
}

// WithSummaryCampfireID configures the shadow summary campfire. When set, the
// server writes a work:item-summary message to this campfire on every
// work:create, work:close, and work:claim operation.
func WithSummaryCampfireID(id string) ServerOption {
	return func(s *Server) { s.summaryCampfireID = id }
}

// WithInboxCampfireID configures the maintainer inbox campfire. When set, the
// server watches this campfire for incoming work:join-request messages and
// materializes them as work:join-request items in the project campfire.
func WithInboxCampfireID(id string) ServerOption {
	return func(s *Server) { s.inboxCampfireID = id }
}

// WithJoinRateLimit sets the maximum number of join requests per hour per
// source pubkey. Defaults to 10.
func WithJoinRateLimit(n int) ServerOption {
	return func(s *Server) { s.joinRateLimit = n }
}

// New creates an in-process convention Server for the given campfire.
// It generates an ephemeral ed25519 key pair for signing server-binding messages.
// The key is tied to the process lifetime — each rd invocation gets a fresh key.
func New(client *protocol.Client, campfireID string, opts ...ServerOption) (*Server, error) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("generating ephemeral key: %w", err)
	}

	s := &Server{
		client:        client,
		campfireID:    campfireID,
		pubKey:        pub,
		privKey:       priv,
		pollInterval:  200 * time.Millisecond,
		joinRateLimit: 10,
		errCh:         make(chan error, 64),
		done:          make(chan struct{}),
	}
	for _, opt := range opts {
		opt(s)
	}
	return s, nil
}

// PubKeyHex returns the hex-encoded ephemeral public key.
func (s *Server) PubKeyHex() string {
	return hex.EncodeToString(s.pubKey)
}

// Start launches one goroutine per operation to serve incoming messages.
// It also ensures a convention:server-binding message exists for this server.
// If an inbox campfire is configured, validates that the inbox campfire ID is
// in the client's membership list and returns an error if not found; then
// starts the inbox watcher goroutine.
// The goroutines run until ctx is cancelled.
// Call Shutdown() to block until all goroutines have exited.
func (s *Server) Start(ctx context.Context) error {
	// Ensure server-binding exists before starting workers.
	if err := s.ensureServerBinding(ctx); err != nil {
		// Non-fatal: log and continue. The server still works; discovery fails gracefully.
		_ = err
	}

	// Build the cf-authority grant gate (ready-197). Best-effort: if the campfire
	// creator can't be resolved, leave the gate nil and serve un-gated (legacy).
	s.gate = s.buildGate()

	for _, op := range allOperations {
		op := op
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			if err := s.serveOperation(ctx, op); err != nil && err != ctx.Err() {
				s.sendErr(err)
			}
		}()
	}

	// Validate and start inbox watcher if an inbox campfire is configured.
	if s.inboxCampfireID != "" {
		m, err := s.client.GetMembership(s.inboxCampfireID)
		if err != nil {
			return fmt.Errorf("conventionserver: checking inbox campfire membership: %w", err)
		}
		if m == nil {
			return fmt.Errorf("conventionserver: inbox campfire %s not in client membership list", s.inboxCampfireID)
		}

		w := &inboxWatcher{
			reader:          s.client,
			inboxCampfire:   s.inboxCampfireID,
			projectCampfire: s.campfireID,
			pollInterval:    30 * time.Second,
			rateLimit:       newJoinRateLimiter(s.joinRateLimit),
			errCh:           s.errCh,
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			w.run(ctx)
		}()
	}

	// Close done channel after all goroutines exit.
	go func() {
		s.wg.Wait()
		close(s.done)
	}()

	return nil
}

// Shutdown blocks until all worker goroutines started by Start() have exited.
// Callers should cancel the context passed to Start() before calling Shutdown.
func (s *Server) Shutdown() {
	<-s.done
}

// Errors returns a receive-only channel that delivers non-fatal errors from
// serveOperation goroutines and the inbox watcher. The channel is buffered
// (64 slots); sends are non-blocking — errors are dropped if the caller is not
// draining. Callers may ignore this channel entirely; errors are never required
// to be handled.
func (s *Server) Errors() <-chan error {
	return s.errCh
}

// sendErr sends err to errCh without blocking. If the channel is full, the error
// is silently dropped.
func (s *Server) sendErr(err error) {
	select {
	case s.errCh <- err:
	default:
	}
}

// serveOperation starts a convention.Server for the named operation and serves
// until ctx is cancelled. It is safe to call from a goroutine.
func (s *Server) serveOperation(ctx context.Context, opName string) error {
	decl, err := s.loadDeclaration(opName)
	if err != nil {
		// Declaration not found (e.g. role-grant not yet defined) — skip silently.
		return nil
	}

	srv := convention.NewServer(s.client, decl).
		WithPollInterval(s.pollInterval)
	if s.gate != nil {
		srv = srv.WithGateEvaluator(s.gate)
	}

	srv.RegisterHandler(opName, s.makeHandler(opName))
	return srv.Serve(ctx, s.campfireID)
}

// buildGate constructs the cf-authority grant gate from the campfire creator as
// root principal. Returns nil (serve un-gated) if membership or the creator key
// is unavailable, so the server degrades gracefully to legacy behavior.
func (s *Server) buildGate() convention.GateEvaluator {
	m, err := s.client.GetMembership(s.campfireID)
	if err != nil || m == nil || m.CreatorPubkey == "" {
		return nil
	}
	root, err := hex.DecodeString(m.CreatorPubkey)
	if err != nil || len(root) != ed25519.PublicKeySize {
		return nil
	}
	g := newGrantGate(s.client, s.campfireID, ed25519.PublicKey(root))
	if g == nil {
		return nil
	}
	return g
}

// makeHandler returns a HandlerFunc that fulfills incoming operation requests.
// In solo mode, all requests are fulfilled regardless of sender (the local user
// is the only participant).
//
// For work:create, work:close, and work:claim operations: after fulfillment, a
// work:item-summary is written to the bound summary campfire (if configured).
func (s *Server) makeHandler(opName string) convention.HandlerFunc {
	return func(ctx context.Context, req *convention.Request) (*convention.Response, error) {
		// In solo mode: fulfill all operations from any sender.
		resp := &convention.Response{
			Payload: map[string]any{
				"ok":        true,
				"operation": opName,
				"fulfilled": true,
			},
			Tags: []string{"work:fulfilled", "work:" + opName + ":ack"},
		}

		// Write summary to shadow campfire for consequential operations.
		if summaryOperations[opName] && s.summaryCampfireID != "" {
			go s.writeSummary(opName, req)
		}

		return resp, nil
	}
}

// itemSummaryPayload is the JSON payload of a work:item-summary message.
// Contains only the fields safe for org-observer visibility — no body or context.
type itemSummaryPayload struct {
	Convention string `json:"convention"`
	Operation  string `json:"operation"`
	// Fields extracted from the original request payload (best-effort).
	ID        string `json:"id,omitempty"`
	Title     string `json:"title,omitempty"`
	Status    string `json:"status,omitempty"`
	Priority  string `json:"priority,omitempty"`
	Assignee  string `json:"assignee,omitempty"`
	ETA       string `json:"eta,omitempty"`
	UpdatedAt int64  `json:"updated_at"`
}

// writeSummary writes a work:item-summary message to the bound summary campfire.
// Called in a goroutine — errors are silently dropped (non-fatal).
func (s *Server) writeSummary(opName string, req *convention.Request) {
	// Extract fields from parsed request args (best-effort; missing fields are OK).
	extract := func(key string) string {
		if req.Args == nil {
			return ""
		}
		if v, ok := req.Args[key]; ok {
			if str, ok := v.(string); ok {
				return str
			}
		}
		return ""
	}

	summary := itemSummaryPayload{
		Convention: "work:item-summary",
		Operation:  opName,
		ID:         extract("id"),
		Title:      extract("title"),
		Status:     extract("status"),
		Priority:   extract("priority"),
		Assignee:   extract("for"),
		ETA:        extract("eta"),
		UpdatedAt:  time.Now().UnixNano(),
	}

	payload, err := json.Marshal(summary)
	if err != nil {
		return
	}

	_, _ = s.client.Send(protocol.SendRequest{
		CampfireID: s.summaryCampfireID,
		Payload:    payload,
		Tags:       []string{"work:item-summary", "work:" + opName},
	})
}

// ensureServerBinding checks whether a convention:server-binding message already
// exists in the campfire naming this server's public key. If not, it posts one.
//
// The server-binding message format:
//
//	{
//	  "convention":   "work",
//	  "operation":    "*",
//	  "server_pubkey": "<hex pubkey>",
//	  "valid_from":   <unix_ns>,
//	  "valid_until":  <unix_ns>   // now + 1h
//	}
//
// Tagged: convention:server-binding
func (s *Server) ensureServerBinding(ctx context.Context) error {
	// Check if a binding for our pubkey already exists.
	existing, err := s.client.Read(protocol.ReadRequest{
		CampfireID: s.campfireID,
		Tags:       []string{"convention:server-binding"},
	})
	if err != nil {
		// Read failure is non-fatal — proceed to post a new binding.
		existing = nil
	}

	myPubKeyHex := s.PubKeyHex()
	if existing != nil {
		for _, msg := range existing.Messages {
			var b serverBindingPayload
			if err := json.Unmarshal(msg.Payload, &b); err != nil {
				continue
			}
			if b.ServerPubkey == myPubKeyHex {
				// Binding already exists for our key — nothing to do.
				return nil
			}
		}
	}

	// Post a new server-binding.
	now := time.Now()
	binding := serverBindingPayload{
		Convention:   "work",
		Operation:    "*",
		ServerPubkey: myPubKeyHex,
		ValidFrom:    now.UnixNano(),
		ValidUntil:   now.Add(time.Hour).UnixNano(),
	}
	payload, err := json.Marshal(binding)
	if err != nil {
		return fmt.Errorf("marshalling server-binding: %w", err)
	}

	_, err = s.client.Send(protocol.SendRequest{
		CampfireID: s.campfireID,
		Payload:    payload,
		Tags:       []string{"convention:server-binding"},
	})
	return err
}

// serverBindingPayload is the JSON payload of a convention:server-binding message.
type serverBindingPayload struct {
	Convention   string `json:"convention"`
	Operation    string `json:"operation"`
	ServerPubkey string `json:"server_pubkey"`
	ValidFrom    int64  `json:"valid_from"`
	ValidUntil   int64  `json:"valid_until"`
}

// loadDeclaration loads a convention declaration by operation name.
// Uses the injectable loader if set, otherwise falls back to the embedded
// declarations package.
func (s *Server) loadDeclaration(name string) (*convention.Declaration, error) {
	if s.declarationLoader != nil {
		return s.declarationLoader(name)
	}
	return loadEmbeddedDeclaration(name)
}

// loadEmbeddedDeclaration loads a declaration from the embedded declarations package.
// This is the default loader used in production.
func loadEmbeddedDeclaration(name string) (*convention.Declaration, error) {
	data, err := loadDeclData(name)
	if err != nil {
		return nil, err
	}
	decl, _, err := convention.Parse([]string{"convention:operation"}, data, "", "", convention.DefaultDeniedTagPrefixes)
	if err != nil {
		return nil, fmt.Errorf("parsing declaration %q: %w", name, err)
	}
	return decl, nil
}

// IsSoloMode reports whether the campfire is in solo mode — i.e., no remote
// convention server is present other than the local in-process server.
// In solo mode, the local user IS the convention server.
//
// Solo mode is detected by checking if there are no convention:server-binding
// messages in the campfire, or if the only binding is our own.
// Expired bindings (ValidUntil != 0 && ValidUntil < now) are skipped.
func IsSoloMode(client *protocol.Client, campfireID, selfPubKeyHex string) bool {
	result, err := client.Read(protocol.ReadRequest{
		CampfireID: campfireID,
		Tags:       []string{"convention:server-binding"},
	})
	if err != nil || result == nil || len(result.Messages) == 0 {
		// No bindings = solo mode.
		return true
	}
	now := time.Now().UnixNano()
	for _, msg := range result.Messages {
		var b serverBindingPayload
		if err := json.Unmarshal(msg.Payload, &b); err != nil {
			continue
		}
		// Skip expired bindings.
		if b.ValidUntil != 0 && b.ValidUntil < now {
			continue
		}
		if b.ServerPubkey != selfPubKeyHex {
			// A binding for a different server exists — not solo.
			return false
		}
	}
	return true
}

// MinOperatorLevel returns the minimum operator level required for the given
// operation name. Returns 0 if the operation is not in the authorization matrix.
func MinOperatorLevel(operation string) int {
	return authorizationMatrix[operation]
}
