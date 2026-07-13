// Mint-and-ship invite: one-use nonce marker + grant-presence check (ready-a49).
//
// `rd invite` mints a fresh secp256k1 key, publishes an owner-signed kind-39301
// CONTRIBUTOR grant for it, and ships {board, relays, secret, nonce, TTL} in an
// rd1_ token. `rd join rd1_...` redeems it exactly ONCE. Single-use is enforced
// RELAY/LOG-OBSERVABLY (design §3, ready-a49 constraint): the FIRST redeemer
// publishes a signed kind-39303 "invite-consumed" marker carrying the token's
// nonce; any later redemption reads the shared medium, sees the marker, and is
// refused. This is a published marker — not client-side memory — so it survives
// across machines and processes.
//
// These are PURE build/scan helpers (no I/O, no clock): the CLI (cmd/rd) drives
// the medium, and the two-actor deterministic test drives them against a shared
// local log with NO relay (ready-6d5 flaky family).
package sync

import (
	"github.com/campfire-net/ready/pkg/nostr"
)

// KindInviteConsumed is the addressable one-use "invite consumed" marker kind. It
// sits adjacent to KindRoleGrant (39301) and is deliberately outside the NIP-100
// board/status ranges so the item projection ignores it. The marker is signed by
// the MINTED contributor key (which the token carries), carries the token nonce in
// a "nonce" tag, and binds to the board via an "a" tag.
const KindInviteConsumed = 39303

// inviteNonceTag / inviteBoardTag name the marker's tags.
const (
	inviteNonceTag = "nonce"
)

// BuildInviteConsumedEvent constructs and signs a kind-39303 invite-consumed
// marker for nonce, bound to board coordinate boardCoord ("30301:<owner>:<d>"),
// signed by k (the minted contributor key the token carries). createdAt MUST be
// seconds (NIP-01) — the caller supplies it so id derivation is deterministic and
// testable.
func BuildInviteConsumedEvent(k *nostr.Key, nonce, boardCoord string, createdAt int64) (*nostr.Event, error) {
	e := &nostr.Event{
		Kind:      KindInviteConsumed,
		CreatedAt: createdAt,
		Tags: [][]string{
			{inviteNonceTag, nonce},
			{"a", boardCoord},
		},
	}
	if err := e.Sign(k); err != nil {
		return nil, err
	}
	return e, nil
}

// InviteNonceConsumed reports whether events contains a signature-valid kind-39303
// marker whose nonce tag equals nonce. It is the single-use gate: once ANY valid
// marker for the nonce exists on the shared medium, the token is spent. An empty
// nonce never matches (a token always carries a non-empty nonce), so a
// malformed/absent nonce fails closed to "not consumed" here and is rejected at
// the token-decode boundary instead.
func InviteNonceConsumed(events []*nostr.Event, nonce string) bool {
	if nonce == "" {
		return false
	}
	for _, e := range events {
		if e == nil || e.Kind != KindInviteConsumed {
			continue
		}
		if err := e.Verify(); err != nil {
			continue // a forged/tampered marker cannot consume the nonce
		}
		if tagValue(e, inviteNonceTag) == nonce {
			return true
		}
	}
	return false
}

// InviteGrantValid reports whether events carry a cap-valid, owner-rooted grant
// admitting grantee to at least contributor on the board 30301:<boardAuthor>:<d>.
// It reuses DeriveLevels (the same derivation the trust gate uses), so a token
// whose grant never actually landed on the medium — or was forged, or bound to a
// different board — is refused fail-closed: an ungranted minted key is ABSENT from
// the derived level map, so redemption is rejected before any identity is written.
func InviteGrantValid(events []*nostr.Event, boardAuthor, boardD, grantee string) bool {
	if boardAuthor == "" || boardD == "" || grantee == "" {
		return false
	}
	levels, _ := DeriveLevels(events, boardAuthor, boardD)
	lvl, ok := levels[grantee]
	// The board author is always present at level 2 via the bootstrap; a genuine
	// invitee is a DISTINCT minted key that must appear via an explicit grant. ok
	// is false for any key that never received an owner-rooted, cap-valid grant.
	return ok && lvl >= LevelContributor
}
