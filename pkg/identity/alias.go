// Person-alias events + local party resolution (ready-034, sharp edge #1 / #6).
//
// A PARTY is a human operator: a set of pubkeys (one per machine/agent key) that
// are "the same person", plus that person's stable email handle(s). rd needs to
// answer "which party owns this pubkey?" and "which keys does baron@3dl.dev sign
// with?" WITHOUT a central directory — so a party is DECLARED by a signed nostr
// event and RESOLVED as a pure function of the local log, exactly like operator
// levels (pkg/sync/rolegrant.go) are.
//
// WIRE MAPPING — kind 39302 "rd person-alias" (addressable / parameterized-
// replaceable, sits one slot above the 39301 role-grant, deliberately away from
// NIP-39's kind 0/30300 identity conventions to avoid collision):
//
//	kind    39302
//	d       "<party-handle>"       -> the party's stable handle (its first email);
//	                                  one addressable slot per (signer, handle) =>
//	                                  latest-wins, a re-published alias supersedes
//	p       <pubkeyHex>            -> one per key the signer asserts is this party
//	                                  (repeatable; the signer's own key SHOULD be
//	                                  among them but is added implicitly regardless)
//	email   <email>               -> one per email handle of this party (repeatable)
//	content optional human label   -> party display name
//
// TRUST MODEL (v1, single operator): an alias event is honored ONLY if its signer
// is ALREADY in the caller's own party — i.e. resolution seeds a trusted set with
// the local machine key(s) (trustRoots) and walks the TRANSITIVE CLOSURE: a trusted
// signer's asserted keys become trusted, which can in turn admit an alias THEY
// signed. An alias signed by a key outside that closure is IGNORED. Accepting a
// THIRD PARTY's alias (a key the operator has never vouched for) is explicitly OUT
// OF SCOPE for v1 — that needs a vouching/attestation layer (TODO: cross-party
// trust, edge #6 follow-up).
package identity

import (
	"fmt"
	"sort"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// KindPersonAlias is the addressable "rd person-alias" event kind. It is purely
// additive: with zero 39302 events present, PartyForPubkey/KeysForParty simply
// return nothing, so nothing depends on aliases existing.
const KindPersonAlias = 39302

// AliasSpec is the caller's view of a person-alias to build a 39302 event. The
// signing key asserts that Pubkeys (plus the signer's own key) and Emails all
// belong to ONE party whose stable handle is Handle.
type AliasSpec struct {
	// Handle is the party's stable handle carried in the "d" tag (its primary
	// email). It gives the addressable latest-wins slot per (signer, handle).
	Handle string
	// Pubkeys is the set of key pubkey-hexes the signer asserts belong to this
	// party. The signer's own pubkey is included implicitly by the resolver even if
	// omitted here.
	Pubkeys []string
	// Emails is the set of email handles of this party (Handle should be among them).
	Emails []string
	// Label is an optional human display name carried in content.
	Label string
}

// BuildAliasEvent constructs and signs a kind-39302 person-alias. createdAt MUST be
// seconds (NIP-01) — the caller supplies it so id derivation is deterministic and
// testable. The event is signed by k; resolution honors it only if k is in the
// reader's trust closure (see package doc).
func BuildAliasEvent(k *nostr.Key, spec AliasSpec, createdAt int64) (*nostr.Event, error) {
	if spec.Handle == "" {
		return nil, fmt.Errorf("identity: person-alias: empty handle (need at least one email)")
	}
	for _, pk := range spec.Pubkeys {
		if len(pk) != 64 || !isHex(pk) {
			return nil, fmt.Errorf("identity: person-alias: pubkey %q is not 64-char hex", pk)
		}
	}
	tags := [][]string{
		{"d", spec.Handle},
	}
	for _, pk := range spec.Pubkeys {
		tags = append(tags, []string{"p", pk})
	}
	for _, em := range spec.Emails {
		tags = append(tags, []string{"email", em})
	}
	e := &nostr.Event{
		Kind:      KindPersonAlias,
		CreatedAt: createdAt,
		Tags:      tags,
		Content:   spec.Label,
	}
	if err := e.Sign(k); err != nil {
		return nil, fmt.Errorf("identity: sign person-alias event: %w", err)
	}
	return e, nil
}

// Party is a resolved human operator: the union of every pubkey and every email
// that a trust-closure of alias events binds together. Slices are sorted for a
// stable, comparable result.
type Party struct {
	Pubkeys []string
	Emails  []string
}

// alias is the parsed view of a verified 39302 event. d/createdAt/id drive the
// latest-wins-per-(signer,d) supersede (Resolve), exactly like roleGrant does for
// its (board,grantee) slot.
type alias struct {
	signer    string
	d         string // the "d" tag = the addressable slot handle (its primary email)
	pubkeys   []string
	emails    []string
	createdAt int64  // NIP-01 seconds — newer supersedes older for the same (signer,d)
	id        string // event id — deterministic tiebreak on a created_at tie
}

// parseAlias extracts an alias from a kind-39302 event. ok=false when it is not a
// well-formed person-alias. It does NOT verify the signature — Resolve does that
// before calling. The signer's own key is folded into pubkeys so an alias that
// omits its own p tag still binds the signer to the party it declares.
func parseAlias(e *nostr.Event) (alias, bool) {
	if e == nil || e.Kind != KindPersonAlias {
		return alias{}, false
	}
	if e.PubKey == "" {
		return alias{}, false
	}
	pubkeys := tagValues(e, "p")
	// The signer is always part of the party it declares.
	pubkeys = append(pubkeys, e.PubKey)
	emails := tagValues(e, "email")
	return alias{
		signer:    e.PubKey,
		d:         tagValue(e, "d"),
		pubkeys:   dedup(pubkeys),
		emails:    dedup(emails),
		createdAt: e.CreatedAt,
		id:        e.ID,
	}, true
}

// newerAlias reports whether alias a should SUPERSEDE alias b for the same (signer,
// d) slot, under the deterministic latest-wins order shared with role-grants
// (rolegrant.go newerGrant / nostrproject.go newerThan): newer created_at wins; on a
// created_at TIE the LOWEST id wins.
func newerAlias(a, b alias) bool {
	if a.createdAt != b.createdAt {
		return a.createdAt > b.createdAt
	}
	return a.id < b.id
}

// Resolver answers party membership queries over a fixed set of alias events. Build
// one with Resolve; it is read-only and safe to reuse.
type Resolver struct {
	parties     []Party
	pubkeyIndex map[string]int // pubkeyHex -> index into parties
	emailIndex  map[string]int // email     -> index into parties
}

// Resolve derives the party graph from events, honoring only alias events inside
// the transitive trust closure of trustRoots (the local machine key(s)). It:
//
//  1. verifies each 39302 event's signature (a forged/tampered alias is dropped);
//  2. seeds the trusted key set with trustRoots;
//  3. repeatedly admits any alias whose SIGNER is trusted, adding that alias's
//     asserted keys to the trusted set, until a fixpoint (transitive closure);
//  4. unions the keys of every admitted alias and attaches its emails, yielding
//     one Party per connected component.
//
// An alias signed by a key never reachable from trustRoots contributes nothing.
func Resolve(events []*nostr.Event, trustRoots []string) *Resolver {
	// Parse + verify once.
	var aliases []alias
	for _, e := range events {
		if e == nil || e.Kind != KindPersonAlias {
			continue
		}
		if err := e.Verify(); err != nil {
			continue
		}
		a, ok := parseAlias(e)
		if !ok {
			continue
		}
		aliases = append(aliases, a)
	}

	// SUPERSEDE — latest-wins per addressable (signer, d) slot, exactly like
	// rolegrant.go keeps the newest grant per (board, grantee). Before this, Resolve
	// UNIONED the keys/emails of every 39302 event a signer ever published, so a
	// republished alias that DROPPED a key never evicted it and party membership only
	// grew — a compromised key stayed a valid confidential-write recipient forever,
	// and a replayed stale alias could re-bind a removed key. Keeping only the newest
	// event per (signer, d) makes revocation/supersede real: dropping a key removes
	// it, and an older alias for the same slot loses regardless of ingest order.
	type slotKey struct{ signer, d string }
	newest := make(map[slotKey]alias, len(aliases))
	for _, a := range aliases {
		k := slotKey{a.signer, a.d}
		if cur, ok := newest[k]; !ok || newerAlias(a, cur) {
			newest[k] = a
		}
	}
	aliases = aliases[:0]
	for _, a := range newest {
		aliases = append(aliases, a)
	}
	// Deterministic iteration for the closure below (map order is randomized). The
	// fixpoint's admitted set is order-independent, but a stable order keeps the whole
	// resolution reproducible run-to-run.
	sort.Slice(aliases, func(i, j int) bool {
		if aliases[i].signer != aliases[j].signer {
			return aliases[i].signer < aliases[j].signer
		}
		return aliases[i].d < aliases[j].d
	})

	trusted := map[string]bool{}
	for _, r := range trustRoots {
		trusted[r] = true
	}

	// Transitive-closure fixpoint: admit aliases whose signer is trusted; a newly
	// admitted alias's keys become trusted and can admit further aliases.
	admitted := make([]bool, len(aliases))
	uf := newUnionFind()
	for r := range trusted {
		uf.add(r)
	}
	for changed := true; changed; {
		changed = false
		for i, a := range aliases {
			if admitted[i] || !trusted[a.signer] {
				continue
			}
			admitted[i] = true
			changed = true
			// Union all of this alias's keys together (and with the signer).
			for _, pk := range a.pubkeys {
				uf.add(pk)
				uf.union(a.signer, pk)
				if !trusted[pk] {
					trusted[pk] = true
				}
			}
		}
	}

	r := &Resolver{pubkeyIndex: map[string]int{}, emailIndex: map[string]int{}}

	// Group pubkeys by union-find root -> Party.
	rootToIdx := map[string]int{}
	partyOf := func(root string) int {
		if idx, ok := rootToIdx[root]; ok {
			return idx
		}
		idx := len(r.parties)
		r.parties = append(r.parties, Party{})
		rootToIdx[root] = idx
		return idx
	}
	for _, pk := range uf.members() {
		idx := partyOf(uf.find(pk))
		r.parties[idx].Pubkeys = append(r.parties[idx].Pubkeys, pk)
		r.pubkeyIndex[pk] = idx
	}
	// Attach emails from each admitted alias to its signer's party.
	for i, a := range aliases {
		if !admitted[i] {
			continue
		}
		idx := partyOf(uf.find(a.signer))
		for _, em := range a.emails {
			r.parties[idx].Emails = append(r.parties[idx].Emails, em)
			r.emailIndex[em] = idx
		}
	}

	// Sort + dedup each party's slices for a stable, comparable result.
	for i := range r.parties {
		r.parties[i].Pubkeys = dedup(r.parties[i].Pubkeys)
		r.parties[i].Emails = dedup(r.parties[i].Emails)
		sort.Strings(r.parties[i].Pubkeys)
		sort.Strings(r.parties[i].Emails)
	}
	return r
}

// PartyForPubkey returns the party a pubkey belongs to, ok=false if no trusted
// alias binds it (including a pubkey that is a bare trust root with no alias — a
// trust root alone is not a "party" until an alias declares one).
func (r *Resolver) PartyForPubkey(pk string) (Party, bool) {
	if r == nil {
		return Party{}, false
	}
	idx, ok := r.pubkeyIndex[pk]
	if !ok {
		return Party{}, false
	}
	// A single bare pubkey with no email and no co-member is not a meaningful party.
	p := r.parties[idx]
	if len(p.Emails) == 0 && len(p.Pubkeys) <= 1 {
		return Party{}, false
	}
	return p, true
}

// KeysForParty returns the sorted pubkeys of the party whose handle is email,
// ok=false if no trusted alias declares that email.
func (r *Resolver) KeysForParty(email string) ([]string, bool) {
	if r == nil {
		return nil, false
	}
	idx, ok := r.emailIndex[email]
	if !ok {
		return nil, false
	}
	keys := r.parties[idx].Pubkeys
	if len(keys) == 0 {
		return nil, false
	}
	return keys, true
}

// --- small helpers (identity-local; the sync package's tag helpers are unexported) ---

// tagValue returns the first value of the first tag named name, or "" if absent.
func tagValue(e *nostr.Event, name string) string {
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			return t[1]
		}
	}
	return ""
}

func tagValues(e *nostr.Event, name string) []string {
	var out []string
	for _, t := range e.Tags {
		if len(t) >= 2 && t[0] == name {
			out = append(out, t[1])
		}
	}
	return out
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func isHex(s string) bool {
	for _, c := range s {
		switch {
		case c >= '0' && c <= '9', c >= 'a' && c <= 'f', c >= 'A' && c <= 'F':
		default:
			return false
		}
	}
	return len(s) > 0
}

// unionFind is a tiny string union-find for grouping party keys.
type unionFind struct {
	parent map[string]string
}

func newUnionFind() *unionFind { return &unionFind{parent: map[string]string{}} }

func (u *unionFind) add(x string) {
	if _, ok := u.parent[x]; !ok {
		u.parent[x] = x
	}
}

func (u *unionFind) find(x string) string {
	u.add(x)
	for u.parent[x] != x {
		u.parent[x] = u.parent[u.parent[x]] // path halving
		x = u.parent[x]
	}
	return x
}

func (u *unionFind) union(a, b string) {
	ra, rb := u.find(a), u.find(b)
	if ra != rb {
		u.parent[ra] = rb
	}
}

func (u *unionFind) members() []string {
	out := make([]string, 0, len(u.parent))
	for k := range u.parent {
		out = append(out, k)
	}
	sort.Strings(out) // deterministic iteration
	return out
}
