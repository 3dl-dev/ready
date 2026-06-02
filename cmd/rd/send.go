package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	campfirepkg "github.com/campfire-net/campfire/cf-protocol/campfire"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	cfencoding "github.com/campfire-net/campfire/cf-protocol/encoding"
	"github.com/campfire-net/campfire/pkg/identity"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/pkg/naming"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
	"github.com/campfire-net/campfire/cf-protocol/transport/fs"

	"github.com/campfire-net/ready/pkg/jsonl"
	"github.com/campfire-net/ready/pkg/rdconfig"
	rdSync "github.com/campfire-net/ready/pkg/sync"
)

// sendToProjectCampfire sends a convention message in two phases:
//  1. Append to local JSONL (always, if project root exists).
//  2. Post to campfire (optional — skipped if no campfire configured,
//     buffered to .ready/pending.jsonl if the send fails).
//
// Returns a synthetic message (with a generated ID) when operating in
// JSONL-only mode (no campfire configured). The campfireID return value is
// empty in that case.
func sendToProjectCampfire(agentID *identity.Identity, s store.Store, payload string, tags []string, antecedents []string) (*message.Message, string, error) {
	campfireID, _, hasCampfire := projectRoot()

	// Phase 1 — build the message object (we always need it for JSONL).
	signer, err := message.NewEd25519Signer(agentID.PrivateKey, agentID.PublicKey)
	if err != nil {
		return nil, "", fmt.Errorf("creating signer: %w", err)
	}
	msg, err := message.NewMessage(signer, []byte(payload), tags, antecedents)
	if err != nil {
		return nil, "", fmt.Errorf("creating message: %w", err)
	}

	// Phase 1 — write to local JSONL.
	if jpPath := jsonlPath(); jpPath != "" {
		w := jsonl.NewWriter(jpPath)
		rec := jsonl.MutationRecord{
			MsgID:       msg.ID,
			CampfireID:  campfireID, // may be empty in JSONL-only mode
			Timestamp:   time.Now().UnixNano(),
			Operation:   extractOperationFromTags(tags),
			Payload:     json.RawMessage(payload),
			Tags:        tags,
			Sender:      agentID.PublicKeyHex(),
			Antecedents: antecedents,
		}
		if err := w.Append(rec); err != nil {
			// JSONL write failed — this is fatal (disk not writable).
			return nil, "", fmt.Errorf("writing to local JSONL: %w", err)
		}
	}

	// Phase 2 — post to campfire (optional).
	if !hasCampfire {
		// No campfire configured — JSONL-only mode, done.
		return msg, "", nil
	}

	client, err := requireClient()
	if err != nil {
		// Client init failed — buffer for later sync and return success.
		fmt.Fprintf(os.Stderr, "warning: campfire client init failed (buffered to pending.jsonl): %v\n", err)
		if bufErr := bufferToPending(msg, campfireID, agentID.PublicKeyHex(), payload, tags, antecedents); bufErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", bufErr)
		}
		return msg, campfireID, nil
	}

	// Try to send via client.Send().
	_, sendErr := client.Send(protocol.SendRequest{
		CampfireID:  campfireID,
		Payload:     msg.Payload,
		Tags:        tags,
		Antecedents: antecedents,
	})
	if sendErr != nil {
		// Transport send failed — JSONL write already succeeded, buffer for sync.
		fmt.Fprintf(os.Stderr, "warning: campfire send failed (buffered to pending.jsonl): %v\n", sendErr)
		if bufErr := bufferToPending(msg, campfireID, agentID.PublicKeyHex(), payload, tags, antecedents); bufErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", bufErr)
		}
		// Return success — mutation is durable in JSONL.
		return msg, campfireID, nil
	}

	// Campfire send succeeded — update sync cursor and attempt to flush any
	// buffered pending mutations. Both are fire-and-forget: failures are logged
	// as warnings but never returned to the caller.
	if projectDir, ok := readyProjectDir(); ok {
		if markErr := rdSync.MarkSynced(projectDir, msg.ID, time.Now().UnixNano()); markErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not update sync cursor: %v\n", markErr)
		}
		// Flush pending — attempt to drain .ready/pending.jsonl using the same
		// campfire send path. Build a flusher that re-uses the already-open store.
		m, memberErr := s.GetMembership(campfireID)
		if memberErr == nil && m != nil {
			flushFn := buildFlusher(agentID, s, m, campfireID, projectDir)
			if n, flushErr := rdSync.FlushPending(projectDir, flushFn); flushErr != nil {
				fmt.Fprintf(os.Stderr, "warning: partial pending flush (%d sent): %v\n", n, flushErr)
			}
		}
	}

	return msg, campfireID, nil
}

// executeConventionOp executes a convention operation against the project campfire.
// It looks up the project campfire via projectRoot() and delegates to
// executeConventionOpToCampfire, which satisfies the D6 constraint (JSONL and
// campfire message IDs agree). When no campfire is configured (JSONL-only mode)
// it generates a local message ID, writes it to JSONL, and returns without sending.
func executeConventionOp(agentID *identity.Identity, s store.Store, exec *convention.Executor, decl *convention.Declaration, argsMap map[string]any) (*message.Message, string, error) {
	campfireID, _, hasCampfire := projectRoot()

	// JSONL-only mode (no campfire configured) — generate a local message ID and write.
	if !hasCampfire {
		payloadJSON, err := json.Marshal(argsMap)
		if err != nil {
			return nil, "", fmt.Errorf("encoding args: %w", err)
		}
		primaryTag := jsonl.WorkTagPrefix + decl.Operation
		signer, err := message.NewEd25519Signer(agentID.PrivateKey, agentID.PublicKey)
		if err != nil {
			return nil, "", fmt.Errorf("creating signer: %w", err)
		}
		msg, err := message.NewMessage(signer, payloadJSON, []string{primaryTag}, nil)
		if err != nil {
			return nil, "", fmt.Errorf("creating message: %w", err)
		}
		if jpPath := jsonlPath(); jpPath != "" {
			w := jsonl.NewWriter(jpPath)
			rec := jsonl.MutationRecord{
				MsgID:      msg.ID,
				CampfireID: "",
				Timestamp:  time.Now().UnixNano(),
				Operation:  primaryTag,
				Payload:    json.RawMessage(payloadJSON),
				Tags:       []string{primaryTag},
				Sender:     agentID.PublicKeyHex(),
			}
			if err := w.Append(rec); err != nil {
				return nil, "", fmt.Errorf("writing to local JSONL: %w", err)
			}
		}
		return msg, "", nil
	}

	return executeConventionOpToCampfire(agentID, s, exec, decl, campfireID, argsMap)
}

// executeConventionOpToCampfire executes a convention operation against an explicit
// campfireID satisfying the D6 constraint: the message ID recorded in mutations.jsonl
// must match the campfire-assigned message ID returned by exec.Execute.
//
// Order of operations:
//  1. Call exec.Execute() — gets the campfire-assigned message ID (result.MessageID).
//  2. Write mutations.jsonl using result.MessageID (D6: IDs agree).
//  3. Update sync cursor with result.MessageID.
//
// On executor failure, fall back to a locally-generated ID for the JSONL record and
// buffer the mutation to pending.jsonl for later retry. The flush path (buildFlusher)
// preserves the original ID when replaying pending records.
//
// Used by depRemoveCmd which must send the unblock message to the campfire
// containing the original block message rather than the project campfire.
func executeConventionOpToCampfire(agentID *identity.Identity, s store.Store, exec *convention.Executor, decl *convention.Declaration, campfireID string, argsMap map[string]any) (*message.Message, string, error) {
	payloadJSON, err := json.Marshal(argsMap)
	if err != nil {
		return nil, "", fmt.Errorf("encoding args: %w", err)
	}
	primaryTag := jsonl.WorkTagPrefix + decl.Operation

	// Phase 1 — execute via convention executor to get the campfire-assigned message ID.
	ctx := context.Background()
	result, execErr := exec.Execute(ctx, decl, campfireID, argsMap)
	if execErr != nil {
		// Executor failed — fall back to locally-generated ID for JSONL and pending buffer.
		signer, signerErr := message.NewEd25519Signer(agentID.PrivateKey, agentID.PublicKey)
		if signerErr != nil {
			return nil, "", fmt.Errorf("creating signer: %w", signerErr)
		}
		msg, msgErr := message.NewMessage(signer, payloadJSON, []string{primaryTag}, nil)
		if msgErr != nil {
			return nil, "", fmt.Errorf("creating fallback message: %w", msgErr)
		}
		if jpPath := jsonlPath(); jpPath != "" {
			w := jsonl.NewWriter(jpPath)
			rec := jsonl.MutationRecord{
				MsgID:      msg.ID,
				CampfireID: campfireID,
				Timestamp:  time.Now().UnixNano(),
				Operation:  primaryTag,
				Payload:    json.RawMessage(payloadJSON),
				Tags:       []string{primaryTag},
				Sender:     agentID.PublicKeyHex(),
			}
			if err := w.Append(rec); err != nil {
				return nil, "", fmt.Errorf("writing to local JSONL: %w", err)
			}
		}
		fmt.Fprintf(os.Stderr, "warning: campfire send failed (buffered to pending.jsonl): %v\n", execErr)
		if bufErr := bufferToPending(msg, campfireID, agentID.PublicKeyHex(), string(payloadJSON), []string{primaryTag}, nil); bufErr != nil {
			fmt.Fprintf(os.Stderr, "warning: %v\n", bufErr)
		}
		return msg, campfireID, nil
	}

	// Phase 2 — executor succeeded: write JSONL with campfire-assigned message ID (D6).
	ts := time.Now().UnixNano()
	if jpPath := jsonlPath(); jpPath != "" {
		w := jsonl.NewWriter(jpPath)
		rec := jsonl.MutationRecord{
			MsgID:      result.MessageID,
			CampfireID: campfireID,
			Timestamp:  ts,
			Operation:  primaryTag,
			Payload:    json.RawMessage(payloadJSON),
			Tags:       []string{primaryTag},
			Sender:     agentID.PublicKeyHex(),
		}
		if err := w.Append(rec); err != nil {
			// JSONL write failed after campfire succeeded — log but don't fail.
			// The campfire message is durable; local record will sync on next run.
			fmt.Fprintf(os.Stderr, "warning: could not write to local JSONL (campfire send succeeded): %v\n", err)
		}
	}

	// Update sync cursor with campfire-assigned ID (D6) and flush pending.
	if projectDir, ok := readyProjectDir(); ok {
		if markErr := rdSync.MarkSynced(projectDir, result.MessageID, ts); markErr != nil {
			fmt.Fprintf(os.Stderr, "warning: could not update sync cursor: %v\n", markErr)
		}
		m, memberErr := s.GetMembership(campfireID)
		if memberErr == nil && m != nil {
			flushFn := buildFlusher(agentID, s, m, campfireID, projectDir)
			if n, flushErr := rdSync.FlushPending(projectDir, flushFn); flushErr != nil {
				fmt.Fprintf(os.Stderr, "warning: partial pending flush (%d sent): %v\n", n, flushErr)
			}
		}
	}

	// Return a message whose ID is the campfire-assigned ID so callers can reference it.
	msg := &message.Message{
		ID:      result.MessageID,
		Sender:  agentID.PublicKey,
		Payload: payloadJSON,
		Tags:    []string{primaryTag},
	}
	return msg, campfireID, nil
}

// buildFlusher returns a Flusher that sends a single pending MutationRecord to campfire.
// It reconstructs the message from the stored record fields and writes it via the
// filesystem transport directly. The original message ID and timestamp from the pending
// record are preserved so that the campfire message ID matches the ID already written
// to mutations.jsonl. This cannot use client.Send() because client.Send() generates a
// new message ID — preserving the original ID is a permanent constraint (D6).
func buildFlusher(agentID *identity.Identity, s store.Store, m *store.Membership, campfireID, projectDir string) rdSync.Flusher {
	// Copy key bytes at closure-creation time so this flusher has an isolated signing context.
	privKeyCopy := make(ed25519.PrivateKey, len(agentID.PrivateKey))
	copy(privKeyCopy, agentID.PrivateKey)
	pubKeyCopy := make(ed25519.PublicKey, len(agentID.PublicKey))
	copy(pubKeyCopy, agentID.PublicKey)

	return func(rec jsonl.MutationRecord) error {
		// Reconstruct the message preserving the original message ID and timestamp.
		// We build the Message struct directly and re-sign using the same signing
		// logic as message.NewMessage, but with rec.MsgID and rec.Timestamp instead
		// of freshly generated values. This ensures the campfire message ID matches
		// the ID already recorded in mutations.jsonl.
		tags := rec.Tags
		if tags == nil {
			tags = []string{}
		}
		antecedents := rec.Antecedents
		if antecedents == nil {
			antecedents = []string{}
		}
		msg := &message.Message{
			ID:          rec.MsgID,
			Sender:      pubKeyCopy,
			Payload:     []byte(rec.Payload),
			Tags:        tags,
			Antecedents: antecedents,
			Timestamp:   rec.Timestamp,
			Provenance:  []message.ProvenanceHop{},
		}
		signInput := message.MessageSignInput{
			ID:          msg.ID,
			Payload:     msg.Payload,
			Tags:        msg.Tags,
			Antecedents: msg.Antecedents,
			Timestamp:   msg.Timestamp,
		}
		signBytes, err := cfencoding.Marshal(signInput)
		if err != nil {
			return fmt.Errorf("encoding pending message sign input: %w", err)
		}
		msg.Signature = ed25519.Sign(privKeyCopy, signBytes)
		// Use a synthetic identity built from the copied keys so that sendPrebuiltMessage's
		// member verification uses the original public key, not any post-construction mutation
		// of the caller's agentID. This completes the signing isolation guarantee.
		isolatedID := &identity.Identity{
			PrivateKey: privKeyCopy,
			PublicKey:  pubKeyCopy,
		}
		return sendPrebuiltMessage(isolatedID, s, m, campfireID, msg)
	}
}

// sendPrebuiltMessage sends a pre-built message (with existing ID/signature) via the
// filesystem transport and stores it locally. Used exclusively by buildFlusher to flush
// pending mutations while preserving their original message IDs.
func sendPrebuiltMessage(agentID *identity.Identity, s store.Store, m *store.Membership, campfireID string, msg *message.Message) error {
	baseDir := fs.DefaultBaseDir()
	if m.TransportDir != "" {
		baseDir = filepath.Dir(m.TransportDir)
	}
	tr := fs.New(baseDir)

	members, err := tr.ListMembers(campfireID)
	if err != nil {
		return fmt.Errorf("listing members: %w", err)
	}
	isMember := false
	for _, mem := range members {
		if fmt.Sprintf("%x", mem.PublicKey) == agentID.PublicKeyHex() {
			isMember = true
			break
		}
	}
	if !isMember {
		return fmt.Errorf("not recognized as a member in the transport directory")
	}

	cfState, err := tr.ReadState(campfireID)
	if err != nil {
		return fmt.Errorf("reading campfire state: %w", err)
	}
	cf := cfState.ToCampfire(members)
	if err := msg.AddHop(
		cfState.PrivateKey, cfState.PublicKey,
		cf.MembershipHash(), len(members),
		cfState.JoinProtocol, cfState.ReceptionRequirements,
		campfirepkg.RoleFull,
	); err != nil {
		return fmt.Errorf("adding provenance hop: %w", err)
	}

	if err := tr.WriteMessage(campfireID, msg); err != nil {
		return fmt.Errorf("writing message: %w", err)
	}

	// Store locally.
	if _, err := s.AddMessage(store.MessageRecordFromMessage(campfireID, msg, store.NowNano())); err != nil {
		// Non-fatal: transport write succeeded.
		fmt.Fprintf(os.Stderr, "warning: failed to cache message locally: %v\n", err)
	}

	return nil
}

// bufferToPending appends a mutation to .ready/pending.jsonl for later sync.
// Returns an error if no project root is found or the write fails.
// Callers log the error as a warning — the primary mutation already succeeded in JSONL.
func bufferToPending(msg *message.Message, campfireID, senderHex, payload string, tags, antecedents []string) error {
	pp := pendingPath()
	if pp == "" {
		return fmt.Errorf("no project root found — cannot buffer mutation to pending.jsonl")
	}
	w := jsonl.NewWriter(pp)
	rec := jsonl.MutationRecord{
		MsgID:       msg.ID,
		CampfireID:  campfireID,
		Timestamp:   time.Now().UnixNano(),
		Operation:   extractOperationFromTags(tags),
		Payload:     json.RawMessage(payload),
		Tags:        tags,
		Sender:      senderHex,
		Antecedents: antecedents,
	}
	if err := w.Append(rec); err != nil {
		return fmt.Errorf("could not buffer mutation to pending.jsonl: %w", err)
	}
	return nil
}

// extractOperationFromTags returns the first "work:" tag from the tags list.
func extractOperationFromTags(tags []string) string {
	for _, t := range tags {
		if strings.HasPrefix(t, jsonl.WorkTagPrefix) && len(t) > len(jsonl.WorkTagPrefix) {
			return t
		}
	}
	return ""
}

// projectRoot walks up from cwd looking for a .campfire/root file.
// Returns (campfireID, projectDir, true) if found.
func projectRoot() (campfireID string, projectDir string, ok bool) {
	dir, err := os.Getwd()
	if err != nil {
		return "", "", false
	}
	for {
		// Priority 1: .ready/config.json with project_name resolved via naming.
		cfg, cfgErr := rdconfig.LoadSyncConfig(dir)
		if cfgErr == nil && cfg != nil && cfg.ProjectName != "" {
			aliases := naming.NewAliasStore(CFHome())
			if resolvedID, aliasErr := aliases.Get(cfg.ProjectName); aliasErr == nil && len(resolvedID) == 64 {
				// Rewrite .campfire/root to keep it in sync when ProjectName takes priority.
				rootFile := filepath.Join(dir, ".campfire", "root")
				if existing, readErr := os.ReadFile(rootFile); readErr == nil && strings.TrimSpace(string(existing)) != resolvedID {
					_ = os.WriteFile(rootFile, []byte(resolvedID), 0600)
				}
				return resolvedID, dir, true
			}
		}

		// Priority 2: .campfire/root legacy path.
		rootFile := filepath.Join(dir, ".campfire", "root")
		data, err := os.ReadFile(rootFile)
		if err == nil {
			id := strings.TrimSpace(string(data))
			if len(id) == 64 {
				return id, dir, true
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", "", false
}

// formatCampfireIDForDisplay converts a hex campfire ID to a project name for display.
// If no project name is available or --debug is set, returns the hex ID.
// Returns the hex ID unchanged if it's not 64 characters (fallback).
func formatCampfireIDForDisplay(hexID string) string {
	if debugOutput || len(hexID) != 64 {
		return hexID
	}

	// Try to resolve the hex ID to a project name via the naming store.
	dir, err := os.Getwd()
	if err != nil {
		return hexID
	}

	// Walk up the directory tree looking for a .ready/config.json.
	for {
		cfg, cfgErr := rdconfig.LoadSyncConfig(dir)
		if cfgErr == nil && cfg != nil && cfg.ProjectName != "" {
			// Try to resolve this project name and check if it matches the given hex ID.
			aliases := naming.NewAliasStore(CFHome())
			if resolvedID, aliasErr := aliases.Get(cfg.ProjectName); aliasErr == nil && resolvedID == hexID {
				return cfg.ProjectName
			}
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}

	return hexID
}

// minInt returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
