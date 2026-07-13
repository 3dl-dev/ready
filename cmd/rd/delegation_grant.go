package main

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"

	trust "github.com/campfire-net/campfire/cf-conventions/cf-authority/trust"
	"github.com/campfire-net/campfire/cf-protocol/message"
	"github.com/campfire-net/campfire/cf-protocol/protocol"
	"github.com/campfire-net/campfire/cf-protocol/store"
)

// campfireAdmitter is the subset of protocol.Client used by postDelegationGrant
// (and the vestigial campfire admit helpers' tests). Defined here so tests can
// inject a fake. Send + PublicKeyHex support the cf-authority delegation:grant
// dual-write (ready-02b); GetMembership + Admit remain for the campfire-backed
// membership path that the campfire SDK (deleted in a later item) still serves.
type campfireAdmitter interface {
	GetMembership(campfireID string) (*store.Membership, error)
	Admit(req protocol.AdmitRequest) error
	Send(req protocol.SendRequest) (*message.Message, error)
	PublicKeyHex() string
}

// delegationGrantTag is the message tag carrying a cf-authority delegation grant
// (a CBOR-encoded trust.GrantPayload). The in-process convention server's gate
// evaluator (ready-197) reads these to authorize operations.
const delegationGrantTag = "delegation:grant"

// delegationGrantTTL is the lifetime of an admit-issued capability grant.
// Members are persistent in ready, but cf-authority capabilities are mandatorily
// time-boxed (Capability.Until), so admit issues a long-lived grant. Renewal and
// the eventual cutover from the legacy work:role-grant are tracked as follow-ups;
// today this grant is dual-written alongside work:role-grant (ready-02b).
const delegationGrantTTL = 365 * 24 * time.Hour

// workOpPatternForRole maps a ready role to the cf-authority OpPattern (a glob,
// pipe-alternation supported) over the "work" convention. Mirrors the level split
// the in-process server's authorizationMatrix encodes: maintainers get every
// operation; everyone else gets the contributor-level operations.
func workOpPatternForRole(role string) string {
	switch role {
	case "maintainer":
		return "*"
	default:
		// member / agent / org-observer: contributor-level operations only.
		return "create|claim|update|block|unblock|engage|gate|status"
	}
}

// buildDelegationGrant constructs the CBOR-encoded cf-authority trust.GrantPayload
// for an owner-issued (depth-0, no parent) grant from granterHex to childHex over
// the "work" convention, scoped by role. It is pure (no I/O) so it is unit-testable.
//
// Depth 0 / ParentGrantID nil is the owner-root shape: the granter IS the trust
// anchor, so DefaultGateEvaluator accepts the single-hop chain when the granter
// equals the campfire's RootPrincipal (true when the admitter is the creator).
func buildDelegationGrant(childHex, granterHex, role string, ttl time.Duration) ([]byte, error) {
	childKey, err := hex.DecodeString(childHex)
	if err != nil || len(childKey) != 32 {
		return nil, fmt.Errorf("child pubkey must be 32-byte hex, got %q", childHex)
	}
	granterKey, err := hex.DecodeString(granterHex)
	if err != nil || len(granterKey) != 32 {
		return nil, fmt.Errorf("granter pubkey must be 32-byte hex, got %q", granterHex)
	}
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		return nil, fmt.Errorf("generating grant nonce: %w", err)
	}
	gp := trust.GrantPayload{
		ParentGrantID: nil, // owner-root: the granter is the trust anchor
		ChildPubkey:   childKey,
		Capabilities: []trust.Capability{{
			Convention: "work",
			OpPattern:  workOpPatternForRole(role),
			Until:      time.Now().Add(ttl).UnixNano(),
			Nonce:      nonce,
		}},
		Depth:         0,
		GranterPubKey: granterKey,
	}
	return trust.MarshalGrantPayloadCBOR(gp)
}

// postDelegationGrant builds and posts a cf-authority delegation:grant message
// from the admitting (owner) identity to the admitted child key, returning the
// posted message ID. It runs alongside the legacy work:role-grant during the
// authority-model migration (dual-write): the gate evaluator (ready-197) reads
// this grant while legacy members remain recognized via work:role-grant.
func postDelegationGrant(client campfireAdmitter, campfireID, childHex, role string) (string, error) {
	cbor, err := buildDelegationGrant(childHex, client.PublicKeyHex(), role, delegationGrantTTL)
	if err != nil {
		return "", err
	}
	msg, err := client.Send(protocol.SendRequest{
		CampfireID: campfireID,
		Payload:    cbor,
		Tags:       []string{delegationGrantTag},
	})
	if err != nil {
		return "", fmt.Errorf("posting delegation grant: %w", err)
	}
	if msg == nil {
		return "", nil
	}
	return msg.ID, nil
}
