package conventionserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/json"
	"strings"
	"time"

	trust "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

const (
	// delegationGrantTag carries a CBOR trust.GrantPayload (posted by rd admit).
	delegationGrantTag = "delegation:grant"
	// identityRevokedTag carries {"child_pubkey": hex} revoking a grant-holder.
	identityRevokedTag = "identity:revoked"
)

// gateReader is the slice of protocol.Client the grant gate needs: reading the
// delegation grants and revocations from the campfire store.
type gateReader interface {
	Read(req protocol.ReadRequest) (*protocol.ReadResult, error)
}

// grantGate is rd's store-aware convention.GateEvaluator (ready-197). The
// convention server passes only the sender/op in its EvaluateRequest, so this
// gate assembles the rest from the store — the sender's delegation grant, the
// campfire's root principal (creator), and the revocation view — then delegates
// to the cf-authority DefaultGateEvaluator (via the convention adapter) for the
// actual scope/expiry/revocation/root-anchor checks.
//
// Migration semantics (dual-read with the legacy work:role-grant): the owner is
// always allowed (anchor-self); a sender that HAS a delegation grant is enforced
// against it (revoked or out-of-scope → denied); a sender with NO delegation
// grant is a legacy member and is allowed (pre-migration behavior preserved).
// The post-migration cutover — deny non-granted senders — is a follow-up once
// every member carries a delegation grant.
type grantGate struct {
	reader        gateReader
	campfireID    string
	rootPrincipal ed25519.PublicKey
	inner         convention.GateEvaluator
}

// newGrantGate builds the gate for a campfire whose creator (root principal) is
// rootPrincipal. Returns nil if rootPrincipal is not a valid key, so callers can
// skip wiring the gate and fall back to the un-gated handler.
func newGrantGate(reader gateReader, campfireID string, rootPrincipal ed25519.PublicKey) *grantGate {
	if reader == nil || len(rootPrincipal) != ed25519.PublicKeySize {
		return nil
	}
	return &grantGate{
		reader:        reader,
		campfireID:    campfireID,
		rootPrincipal: rootPrincipal,
		inner:         trust.NewConventionAdapter(),
	}
}

// Evaluate implements convention.GateEvaluator.
func (g *grantGate) Evaluate(ctx context.Context, req convention.EvaluateRequest) convention.EvaluateResult {
	sender := req.Request.Sender

	// Owner anchor-self: the campfire creator is always allowed.
	if len(sender) == ed25519.PublicKeySize && bytes.Equal(sender, g.rootPrincipal) {
		return convention.EvaluateResult{Decision: convention.GateAllow}
	}

	grant, gp, ok := g.grantFor(sender)
	if !ok {
		// Legacy member (no delegation grant) — preserve pre-migration behavior.
		return convention.EvaluateResult{Decision: convention.GateAllow}
	}

	// Op-scope: the Server passes no compiled gate predicate, so the inner
	// evaluator checks grant validity/root/expiry/revocation but not whether this
	// operation is within the grant's OpPattern. Enforce that here.
	if !grantCoversOp(gp, req.Request.Convention, req.Request.Operation) {
		return convention.EvaluateResult{Decision: convention.GateDeny, Reason: convention.DenyReason("scope_mismatch")}
	}

	req.ChainMessages = []convention.GateChainMessage{grant}
	req.RootPrincipal = g.rootPrincipal
	req.RevocationView = g.revocationView()
	return g.inner.Evaluate(ctx, req)
}

// grantFor returns the most recent delegation:grant whose ChildPubkey == sender,
// along with its decoded payload.
func (g *grantGate) grantFor(sender ed25519.PublicKey) (convention.GateChainMessage, trust.GrantPayload, bool) {
	if len(sender) != ed25519.PublicKeySize {
		return convention.GateChainMessage{}, trust.GrantPayload{}, false
	}
	res, err := g.reader.Read(protocol.ReadRequest{CampfireID: g.campfireID, Tags: []string{delegationGrantTag}})
	if err != nil || res == nil {
		return convention.GateChainMessage{}, trust.GrantPayload{}, false
	}
	var found convention.GateChainMessage
	var foundGP trust.GrantPayload
	ok := false
	for _, msg := range res.Messages {
		gp, err := trust.UnmarshalGrantPayloadCBOR(msg.Payload)
		if err != nil {
			continue
		}
		if bytes.Equal(gp.ChildPubkey, sender) {
			// Keep scanning; the last matching message is the most recent grant.
			found = convention.GateChainMessage{ID: msg.ID, Payload: msg.Payload}
			foundGP = gp
			ok = true
		}
	}
	return found, foundGP, ok
}

// grantCoversOp reports whether any of the grant's capabilities for the given
// convention has an OpPattern matching op.
func grantCoversOp(gp trust.GrantPayload, conv, op string) bool {
	for _, c := range gp.Capabilities {
		if c.Convention == conv && opPatternMatches(c.OpPattern, op) {
			return true
		}
	}
	return false
}

// opPatternMatches matches a cf-authority OpPattern against an operation. "*"
// matches any operation; otherwise the pattern is pipe-alternation of exact ops.
func opPatternMatches(pattern, op string) bool {
	if pattern == "*" {
		return true
	}
	for _, alt := range strings.Split(pattern, "|") {
		if alt == "*" || alt == op {
			return true
		}
	}
	return false
}

// revocationView reads identity:revoked messages into the evaluator's view. The
// evaluator matches a revocation when entry.CampfireID == the revoked child key
// hex (trust/evaluator.go isRevokedInView), so child_pubkey goes in CampfireID.
func (g *grantGate) revocationView() []convention.GateRevocationViewEntry {
	res, err := g.reader.Read(protocol.ReadRequest{CampfireID: g.campfireID, Tags: []string{identityRevokedTag}})
	if err != nil || res == nil {
		return nil
	}
	var view []convention.GateRevocationViewEntry
	for _, msg := range res.Messages {
		var p struct {
			ChildPubkey string `json:"child_pubkey"`
		}
		if err := json.Unmarshal(msg.Payload, &p); err != nil || p.ChildPubkey == "" {
			continue
		}
		view = append(view, convention.GateRevocationViewEntry{
			CampfireID:          p.ChildPubkey,
			LatestObservedMsgID: msg.ID,
			ObservedAt:          time.Now(),
		})
	}
	return view
}
