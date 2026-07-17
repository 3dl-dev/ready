package main

import (
	"github.com/3dl-dev/ready/pkg/identity"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// nostrPartyIdentitySet expands an identity token (a pubkey hex OR an email
// handle) into the full set of identities — every pubkey and every email — that
// belong to the SAME party (ready-99d, edge #6). It builds the local party
// resolver from the authoritative nostr log, seeded with THIS machine key as the
// sole trust root, so only alias events the local operator has vouched for are
// honored (pkg/identity trust model, v1 single operator).
//
// The returned set ALWAYS contains token itself, so on a box with no person-alias
// declared — or a token that resolves to no party — the result is exactly
// {token} and identity scoping collapses to the pre-alias behaviour (match the
// raw pubkey only). This keeps `rd ready`/`rd list` identity filtering unchanged
// on machines that never ran `rd identify`.
func nostrPartyIdentitySet(token string) map[string]bool {
	set := map[string]bool{token: true}
	dir, ok := readyProjectDir()
	if !ok {
		return set
	}
	k, err := nostrKey()
	if err != nil {
		return set
	}
	events, err := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if err != nil {
		return set
	}
	r := identity.Resolve(events, []string{k.PubKeyHex()})
	addPartyIdentities(set, r, token)
	return set
}

// addPartyIdentities folds every pubkey and email of token's party into set.
// token is resolved first as a pubkey, then (on a miss) as an email handle. A
// token in no trusted party contributes nothing (set is left as-is).
func addPartyIdentities(set map[string]bool, r *identity.Resolver, token string) {
	if p, ok := r.PartyForPubkey(token); ok {
		for _, pk := range p.Pubkeys {
			set[pk] = true
		}
		for _, em := range p.Emails {
			set[em] = true
		}
		return
	}
	if keys, ok := r.KeysForParty(token); ok {
		for _, pk := range keys {
			set[pk] = true
		}
		// Fold in the party's emails via any resolved member key (KeysForParty
		// returns only pubkeys; PartyForPubkey on a member key carries the emails).
		if len(keys) > 0 {
			if p, ok := r.PartyForPubkey(keys[0]); ok {
				for _, em := range p.Emails {
					set[em] = true
				}
			}
		}
	}
}
