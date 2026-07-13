package main

// Local invite-claim bookkeeping (ready-ce0).
//
// These records are HONEST LOCAL IDEMPOTENCY / UX only — NEVER a security boundary.
// The real single-use guarantee is owner-enforced at grant DERIVATION: one
// claim-nonce binds to exactly one self-minted pubkey (pkg/sync deriveGrants). These
// files just let `rd join` refuse an accidental second redemption of the same token
// on the SAME machine without --force, and let the owner see which claim-nonces are
// still outstanding.
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
