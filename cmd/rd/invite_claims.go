package main

// Local invite-claim bookkeeping (ready-ce0; mint verification ready-c40).
//
// consumedInvitesPath (the JOINER-side record) is HONEST LOCAL IDEMPOTENCY / UX
// only — it just lets `rd join` refuse an accidental second redemption of the same
// token on the SAME machine without --force. It is NOT a security boundary: a
// hostile joiner can trivially delete or never write it.
//
// unclaimedInvitesPath (the OWNER-side record) is DIFFERENT: since ready-c40 it IS
// part of the security boundary for `rd grant --claim`. Single-use reuse-by-a-
// different-pubkey is still owner-enforced at grant DERIVATION (one claim-nonce
// binds to exactly one self-minted pubkey, pkg/sync deriveGrants/ClaimGrantee) —
// that half was already correct. What was MISSING is that `rd grant --claim
// <nonce>` accepted ANY caller-supplied nonce string with no check that this
// owner ever minted it via `rd invite`/`rd board share`, and no check that a
// minted nonce's TTL had not already elapsed (the TTL was previously enforced
// ONLY on the join side: decodeNostrClaimToken / redeemNostrClaimToken). An owner
// induced to run `rd grant --claim <attacker-string> <attacker-pubkey>` would
// confer write access on a nonce nobody ever issued. publishRoleGrant
// (cmd/rd/nostr_grant.go) now requires a matching, unexpired record in THIS
// file before honoring --claim.
//
// Format: newline-delimited JSON (one localClaim per line), append-only. A missing
// file reads as empty. We never treat a corrupt line as fatal — bookkeeping must not
// wedge the join/invite path.

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// localClaim is one recorded invite claim-nonce. Pubkey is set on the JOIN side (the
// self-minted key the joiner will send to the owner); it is empty on the owner's
// unclaimed-invites record.
type localClaim struct {
	Claim     string `json:"claim"`
	Board     string `json:"board"`
	ExpiresAt int64  `json:"exp,omitempty"`
	Pubkey    string `json:"pubkey,omitempty"`
}

// consumedInvitesPath is the joiner-side record of claim-nonces this machine has
// already redeemed (self-minted a key for).
func consumedInvitesPath(rdHome string) string {
	return filepath.Join(rdHome, "consumed-invites")
}

// unclaimedInvitesPath is the owner-side record of claim-nonces minted by `rd invite`
// that have not yet been bound to a joiner pubkey via `rd grant --claim`.
func unclaimedInvitesPath(rdHome string) string {
	return filepath.Join(rdHome, "unclaimed-invites")
}

// readLocalClaims reads the newline-delimited JSON claim records at path. A missing
// file is an empty slice (not an error); malformed lines are skipped.
func readLocalClaims(path string) ([]localClaim, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []localClaim
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var c localClaim
		if err := json.Unmarshal([]byte(line), &c); err != nil {
			continue // skip a corrupt line — bookkeeping is best-effort
		}
		out = append(out, c)
	}
	return out, sc.Err()
}

// appendLocalClaim appends one claim record to path (0600, created if absent).
func appendLocalClaim(path string, c localClaim) error {
	if dir := filepath.Dir(path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	data, err := json.Marshal(c)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = f.Write(append(data, '\n'))
	return err
}

// localClaimPresent reports whether path already records the given claim-nonce.
func localClaimPresent(path, claim string) (bool, error) {
	claims, err := readLocalClaims(path)
	if err != nil {
		return false, err
	}
	for _, c := range claims {
		if c.Claim == claim {
			return true, nil
		}
	}
	return false, nil
}

// findLocalClaim returns the recorded claim entry for the given nonce at path, and
// whether one was found (ready-c40). `rd invite`/`rd board share` mint a
// crypto-random 128-bit nonce (randomNonce), so a collision between two distinct
// mints is not a real-world concern; the first match is authoritative.
func findLocalClaim(path, claim string) (localClaim, bool, error) {
	claims, err := readLocalClaims(path)
	if err != nil {
		return localClaim{}, false, err
	}
	for _, c := range claims {
		if c.Claim == claim {
			return c, true, nil
		}
	}
	return localClaim{}, false, nil
}
