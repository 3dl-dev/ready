package conventionserver

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"testing"
	"time"

	trust "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	"github.com/campfire-net/campfire/cf-conventions/cf-convention"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
)

var (
	ownerKey = ed25519.PublicKey(bytes.Repeat([]byte{0x01}, 32))
	childKey = ed25519.PublicKey(bytes.Repeat([]byte{0x02}, 32))
)

// fakeGateReader serves canned delegation:grant and identity:revoked messages.
type fakeGateReader struct {
	grants  []protocol.Message
	revokes []protocol.Message
}

func (f *fakeGateReader) Read(req protocol.ReadRequest) (*protocol.ReadResult, error) {
	for _, tag := range req.Tags {
		switch tag {
		case delegationGrantTag:
			return &protocol.ReadResult{Messages: f.grants}, nil
		case identityRevokedTag:
			return &protocol.ReadResult{Messages: f.revokes}, nil
		}
	}
	return &protocol.ReadResult{}, nil
}

func grantMsg(t *testing.T, child, granter ed25519.PublicKey, opPattern string) protocol.Message {
	t.Helper()
	gp := trust.GrantPayload{
		ChildPubkey: child,
		Capabilities: []trust.Capability{{
			Convention: "work",
			OpPattern:  opPattern,
			Until:      time.Now().Add(time.Hour).UnixNano(),
			Nonce:      bytes.Repeat([]byte{0x09}, 16),
		}},
		Depth:         0,
		GranterPubKey: granter,
	}
	cbor, err := trust.MarshalGrantPayloadCBOR(gp)
	if err != nil {
		t.Fatalf("MarshalGrantPayloadCBOR: %v", err)
	}
	return protocol.Message{ID: "g1", Payload: cbor, Tags: []string{delegationGrantTag}}
}

func revokeMsg(child ed25519.PublicKey) protocol.Message {
	payload, _ := json.Marshal(map[string]string{"child_pubkey": hex.EncodeToString(child)})
	return protocol.Message{ID: "r1", Payload: payload, Tags: []string{identityRevokedTag}}
}

func evalReq(sender ed25519.PublicKey, op string) convention.EvaluateRequest {
	return convention.EvaluateRequest{
		Request: convention.GateOpRequest{
			Convention: "work",
			Operation:  op,
			CampfireID: "cafe",
			Sender:     sender,
		},
		CurrentTime: time.Now(),
	}
}

func TestGrantGate_OwnerAlwaysAllowed(t *testing.T) {
	g := newGrantGate(&fakeGateReader{}, "cafe", ownerKey)
	if g == nil {
		t.Fatal("newGrantGate returned nil for a valid root principal")
	}
	res := g.Evaluate(context.Background(), evalReq(ownerKey, "close"))
	if res.Decision != convention.GateAllow {
		t.Errorf("owner: got decision %d reason %q, want GateAllow", res.Decision, res.Reason)
	}
}

func TestGrantGate_InScopeGrantAllowed(t *testing.T) {
	reader := &fakeGateReader{grants: []protocol.Message{grantMsg(t, childKey, ownerKey, "create|claim|update")}}
	g := newGrantGate(reader, "cafe", ownerKey)
	res := g.Evaluate(context.Background(), evalReq(childKey, "create"))
	if res.Decision != convention.GateAllow {
		t.Errorf("in-scope grant: got decision %d reason %q, want GateAllow", res.Decision, res.Reason)
	}
}

func TestGrantGate_OutOfScopeDenied(t *testing.T) {
	// Member-scoped grant (no "close"); a close op must be denied.
	reader := &fakeGateReader{grants: []protocol.Message{grantMsg(t, childKey, ownerKey, "create|claim|update")}}
	g := newGrantGate(reader, "cafe", ownerKey)
	res := g.Evaluate(context.Background(), evalReq(childKey, "close"))
	if res.Decision == convention.GateAllow {
		t.Errorf("out-of-scope op: got GateAllow, want deny")
	}
}

func TestGrantGate_RevokedDenied(t *testing.T) {
	reader := &fakeGateReader{
		grants:  []protocol.Message{grantMsg(t, childKey, ownerKey, "*")},
		revokes: []protocol.Message{revokeMsg(childKey)},
	}
	g := newGrantGate(reader, "cafe", ownerKey)
	res := g.Evaluate(context.Background(), evalReq(childKey, "create"))
	if res.Decision == convention.GateAllow {
		t.Errorf("revoked grant-holder: got GateAllow, want deny")
	}
}

func TestGrantGate_NoGrantLegacyFallbackAllowed(t *testing.T) {
	// A sender with no delegation grant is a legacy work:role-grant member during
	// migration — allowed (pre-migration behavior preserved).
	g := newGrantGate(&fakeGateReader{}, "cafe", ownerKey)
	res := g.Evaluate(context.Background(), evalReq(childKey, "create"))
	if res.Decision != convention.GateAllow {
		t.Errorf("no-grant legacy member: got decision %d, want GateAllow", res.Decision)
	}
}

func TestNewGrantGate_NilOnInvalidRoot(t *testing.T) {
	if g := newGrantGate(&fakeGateReader{}, "cafe", ed25519.PublicKey{0x01}); g != nil {
		t.Error("expected nil gate for a malformed root principal")
	}
}
