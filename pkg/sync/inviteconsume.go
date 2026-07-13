// Invite grant-presence gate (ready-a49, re-architected ready-ce0).
//
// The invite lifecycle is now SELF-MINT (design §2, generate-then-authorize): the
// owner mints only a one-use CLAIM-NONCE (rd invite), the joiner self-mints its own
// key and syncs read-only (rd join), and the owner binds that pubkey to the claim
// with an owner-signed kind-39301 grant carrying a "claim" tag (rd grant --claim).
// Single-use is REAL and owner-enforced: DeriveLevels/deriveGrants binds one
// claim-nonce to exactly one grantee (see rolegrant.go), so a leaked claim admitted
// to a SECOND self-minted key is rejected at derivation.
//
// The old kind-39303 relay "invite-consumed" marker (its builder and relay-precheck
// helpers) was deleted with ready-ce0: it was signed by the SAME minted key the token
// shipped, so it guarded nothing (pure theater), and its fail-closed relay write is
// what broke `rd join` on locked relays. Single-use no longer needs a relay write at
// all — it is a projection property of the owner's grant.
//
// InviteGrantValid remains a PURE scan helper (no I/O, no clock): it reports whether
// a self-minted key has actually received an owner-rooted, cap-valid contributor
// grant on the pinned board — the read-only join's post-admission liveness check.
package sync

import (
	"github.com/campfire-net/ready/pkg/nostr"
)

// InviteGrantValid reports whether events carry a cap-valid, owner-rooted grant
// admitting grantee to at least contributor on the board 30301:<boardAuthor>:<d>.
// It reuses DeriveLevels (the same derivation the trust gate uses), so a key whose
// grant never actually landed on the medium — or was forged, or bound to a
// different board — is refused fail-closed: an ungranted key is ABSENT from the
// derived level map. Used to report, after a read-only join, whether the owner has
// already granted the self-minted key (write access is live) or not yet.
func InviteGrantValid(events []*nostr.Event, boardAuthor, boardD, grantee string) bool {
	if boardAuthor == "" || boardD == "" || grantee == "" {
		return false
	}
	levels, _ := DeriveLevels(events, boardAuthor, boardD)
	lvl, ok := levels[grantee]
	// The board author is always present at level 2 via the bootstrap; a genuine
	// grantee is a DISTINCT self-minted key that must appear via an explicit grant. ok
	// is false for any key that never received an owner-rooted, cap-valid grant.
	return ok && lvl >= LevelContributor
}
