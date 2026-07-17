package identity

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
)

// mustKey generates a real signing key (NO signer mock — the done condition
// requires a genuine Schnorr signature we can verify).
func mustKey(t *testing.T) *nostr.Key {
	t.Helper()
	k, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return k
}

func contains(ss []string, want string) bool {
	for _, s := range ss {
		if s == want {
			return true
		}
	}
	return false
}

// TestAlias_ResolveParty is the load-bearing done-condition unit test: an alias
// signed by A binding {A, B, baron@3dl.dev} lets a resolver seeded with A resolve
// PartyForPubkey(B) and KeysForParty("baron@3dl.dev") -> {A, B}. The signature is
// verified for real (BuildAliasEvent signs; Resolve calls e.Verify()).
func TestAlias_ResolveParty(t *testing.T) {
	a := mustKey(t)
	b := mustKey(t)
	aPub, bPub := a.PubKeyHex(), b.PubKeyHex()
	const email = "baron@3dl.dev"

	ev, err := BuildAliasEvent(a, AliasSpec{
		Handle:  email,
		Pubkeys: []string{aPub, bPub},
		Emails:  []string{email},
		Label:   "Baron",
	}, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}

	// The event MUST be genuinely signed — verify the real signature, no mock.
	if err := ev.Verify(); err != nil {
		t.Fatalf("alias event signature does not verify: %v", err)
	}
	if ev.Kind != KindPersonAlias {
		t.Fatalf("kind = %d, want %d", ev.Kind, KindPersonAlias)
	}
	if ev.PubKey != aPub {
		t.Fatalf("signer = %s, want A %s", ev.PubKey, aPub)
	}

	// TRUST MODEL: seed the closure with the local machine key A.
	r := Resolve([]*nostr.Event{ev}, []string{aPub})

	// PartyForPubkey(B) returns the party (B was declared, not a trust root).
	party, ok := r.PartyForPubkey(bPub)
	if !ok {
		t.Fatalf("PartyForPubkey(B) not found; want the party bound by A's alias")
	}
	if !contains(party.Pubkeys, aPub) || !contains(party.Pubkeys, bPub) {
		t.Fatalf("party pubkeys = %v, want to include both A and B", party.Pubkeys)
	}
	if !contains(party.Emails, email) {
		t.Fatalf("party emails = %v, want to include %s", party.Emails, email)
	}

	// KeysForParty(email) returns {A, B}.
	keys, ok := r.KeysForParty(email)
	if !ok {
		t.Fatalf("KeysForParty(%s) not found", email)
	}
	want := []string{aPub, bPub}
	// KeysForParty returns sorted; sort want for comparison.
	if !(contains(keys, aPub) && contains(keys, bPub) && len(keys) == 2) {
		t.Fatalf("KeysForParty(%s) = %v, want %v", email, keys, want)
	}
}

// TestAlias_UntrustedSignerIgnored encodes the v1 trust model: an alias signed by
// a key OUTSIDE the caller's trust closure contributes nothing — accepting a third
// party's alias is out of scope.
func TestAlias_UntrustedSignerIgnored(t *testing.T) {
	stranger := mustKey(t)
	victim := mustKey(t)
	local := mustKey(t)

	ev, err := BuildAliasEvent(stranger, AliasSpec{
		Handle:  "evil@example.com",
		Pubkeys: []string{stranger.PubKeyHex(), victim.PubKeyHex()},
		Emails:  []string{"evil@example.com"},
	}, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}

	// The local operator's trust closure contains ONLY its own key — it never
	// vouched for `stranger`, so the alias is ignored.
	r := Resolve([]*nostr.Event{ev}, []string{local.PubKeyHex()})
	if _, ok := r.PartyForPubkey(victim.PubKeyHex()); ok {
		t.Fatalf("PartyForPubkey(victim) resolved from an UNTRUSTED alias — trust model breached")
	}
	if _, ok := r.KeysForParty("evil@example.com"); ok {
		t.Fatalf("KeysForParty resolved an untrusted alias's email — trust model breached")
	}
}

// TestAlias_TransitiveClosure verifies the closure walks one hop: A vouches for B
// (A trusted), then an alias SIGNED BY B binding C is honored because B became
// trusted through A's alias.
func TestAlias_TransitiveClosure(t *testing.T) {
	a := mustKey(t)
	b := mustKey(t)
	c := mustKey(t)

	evAB, err := BuildAliasEvent(a, AliasSpec{
		Handle:  "baron@3dl.dev",
		Pubkeys: []string{a.PubKeyHex(), b.PubKeyHex()},
		Emails:  []string{"baron@3dl.dev"},
	}, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildAliasEvent A: %v", err)
	}
	evBC, err := BuildAliasEvent(b, AliasSpec{
		Handle:  "baron@3dl.dev",
		Pubkeys: []string{b.PubKeyHex(), c.PubKeyHex()},
		Emails:  []string{"baron@3dl.dev"},
	}, 1_700_000_001)
	if err != nil {
		t.Fatalf("BuildAliasEvent B: %v", err)
	}

	r := Resolve([]*nostr.Event{evAB, evBC}, []string{a.PubKeyHex()})
	keys, ok := r.KeysForParty("baron@3dl.dev")
	if !ok {
		t.Fatalf("KeysForParty not found after transitive closure")
	}
	for _, want := range []string{a.PubKeyHex(), b.PubKeyHex(), c.PubKeyHex()} {
		if !contains(keys, want) {
			t.Fatalf("transitive closure keys = %v, missing %s", keys, want)
		}
	}
}

// TestAlias_TamperedSignatureDropped ensures a mutated alias event fails
// verification and is dropped by Resolve (defense: an attacker cannot forge party
// membership by editing an event's tags).
func TestAlias_TamperedSignatureDropped(t *testing.T) {
	a := mustKey(t)
	b := mustKey(t)
	ev, err := BuildAliasEvent(a, AliasSpec{
		Handle:  "baron@3dl.dev",
		Pubkeys: []string{a.PubKeyHex()},
		Emails:  []string{"baron@3dl.dev"},
	}, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}
	// Inject B into the party AFTER signing — id/sig no longer cover it.
	ev.Tags = append(ev.Tags, []string{"p", b.PubKeyHex()})

	r := Resolve([]*nostr.Event{ev}, []string{a.PubKeyHex()})
	if _, ok := r.PartyForPubkey(b.PubKeyHex()); ok {
		t.Fatalf("tampered alias admitted B into the party — signature check bypassed")
	}
}

// TestAlias_StoreRoundTrip is the integration leg of the done condition: the signed
// alias is persisted to the real NostrLog JSONL store, read back, and resolved —
// proving the event survives serialization and the resolver works off the store,
// not just an in-memory event.
func TestAlias_StoreRoundTrip(t *testing.T) {
	a := mustKey(t)
	b := mustKey(t)
	const email = "baron@3dl.dev"

	ev, err := BuildAliasEvent(a, AliasSpec{
		Handle:  email,
		Pubkeys: []string{a.PubKeyHex(), b.PubKeyHex()},
		Emails:  []string{email},
	}, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildAliasEvent: %v", err)
	}

	logPath := filepath.Join(t.TempDir(), "nostr-log.jsonl")
	log := rdSync.NewNostrLog(logPath)
	if err := log.Append(ev); err != nil {
		t.Fatalf("Append: %v", err)
	}
	events, err := log.ReadAll()
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}

	r := Resolve(events, []string{a.PubKeyHex()})
	keys, ok := r.KeysForParty(email)
	if !ok {
		t.Fatalf("KeysForParty(%s) not found after store round-trip", email)
	}
	want := []string{a.PubKeyHex(), b.PubKeyHex()}
	sortedWant := append([]string(nil), want...)
	// keys is sorted by the resolver.
	if !reflect.DeepEqual(keys, sortedStrings(sortedWant)) {
		t.Fatalf("KeysForParty(%s) = %v, want %v", email, keys, sortedStrings(sortedWant))
	}
}

// TestAlias_SupersedeEvictsDroppedKey is the load-bearing SECURITY done-condition
// (ready-998, found missing by ready-9f3): resolution is latest-wins per addressable
// (signer, d) slot, NOT a union across versions. A republished alias that OMITS a
// previously-bound key EVICTS it — so a compromised key removed from the party is no
// longer a valid confidential-write recipient. Signer A first binds {A, B}, then
// republishes (higher created_at) binding only {A}; B must be gone from BOTH
// KeysForParty and PartyForPubkey. Real signatures on both events, no mock.
func TestAlias_SupersedeEvictsDroppedKey(t *testing.T) {
	a := mustKey(t)
	b := mustKey(t)
	aPub, bPub := a.PubKeyHex(), b.PubKeyHex()
	const email = "baron@3dl.dev"

	evOld, err := BuildAliasEvent(a, AliasSpec{
		Handle:  email,
		Pubkeys: []string{aPub, bPub},
		Emails:  []string{email},
	}, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildAliasEvent old: %v", err)
	}
	// Republish the SAME (signer, d=email) slot with a higher created_at, dropping B.
	evNew, err := BuildAliasEvent(a, AliasSpec{
		Handle:  email,
		Pubkeys: []string{aPub},
		Emails:  []string{email},
	}, 1_700_000_100)
	if err != nil {
		t.Fatalf("BuildAliasEvent new: %v", err)
	}

	r := Resolve([]*nostr.Event{evOld, evNew}, []string{aPub})

	keys, ok := r.KeysForParty(email)
	if !ok {
		t.Fatalf("KeysForParty(%s) not found after supersede; the surviving alias still binds A", email)
	}
	if contains(keys, bPub) {
		t.Fatalf("KeysForParty(%s) = %v still contains dropped key B — supersede did NOT evict (union bug)", email, keys)
	}
	if !contains(keys, aPub) {
		t.Fatalf("KeysForParty(%s) = %v lost A; the newest alias still binds A", email, keys)
	}
	if _, ok := r.PartyForPubkey(bPub); ok {
		t.Fatalf("PartyForPubkey(B) still resolves after B was dropped — a removed key stays a confidential-write recipient forever")
	}
}

// TestAlias_StaleReplayIgnored encodes the replay half of the done condition: an
// OLDER (lower created_at) alias for the same (signer, d) slot is IGNORED even when
// ingested AFTER the newer one — order of arrival must not matter, only created_at.
// A stale, replayed alias cannot re-bind a key the current alias removed.
func TestAlias_StaleReplayIgnored(t *testing.T) {
	a := mustKey(t)
	b := mustKey(t)
	aPub, bPub := a.PubKeyHex(), b.PubKeyHex()
	const email = "baron@3dl.dev"

	// The CURRENT alias (higher created_at) binds only {A}.
	evNew, err := BuildAliasEvent(a, AliasSpec{
		Handle:  email,
		Pubkeys: []string{aPub},
		Emails:  []string{email},
	}, 1_700_000_100)
	if err != nil {
		t.Fatalf("BuildAliasEvent new: %v", err)
	}
	// A STALE alias (lower created_at) binding {A, B} — an attacker replays it late.
	evStale, err := BuildAliasEvent(a, AliasSpec{
		Handle:  email,
		Pubkeys: []string{aPub, bPub},
		Emails:  []string{email},
	}, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildAliasEvent stale: %v", err)
	}

	// Ingest order: newest first, stale replayed AFTER — the stale one must lose.
	r := Resolve([]*nostr.Event{evNew, evStale}, []string{aPub})

	if _, ok := r.PartyForPubkey(bPub); ok {
		t.Fatalf("a stale replayed alias re-bound removed key B — latest-wins breached by ingest order")
	}
	keys, _ := r.KeysForParty(email)
	if contains(keys, bPub) {
		t.Fatalf("KeysForParty(%s) = %v re-admitted B from a stale replay", email, keys)
	}
}

// TestAlias_SupersedeIsPerSlot ensures supersede is scoped to (signer, d): a newer
// alias in ONE slot does not evict keys bound by a DIFFERENT signer's alias for the
// same party (that is the transitive-closure union across signers, which is correct).
func TestAlias_SupersedeIsPerSlot(t *testing.T) {
	a := mustKey(t)
	b := mustKey(t)
	c := mustKey(t)
	const email = "baron@3dl.dev"

	// A binds {A, B}.
	evA, err := BuildAliasEvent(a, AliasSpec{
		Handle:  email,
		Pubkeys: []string{a.PubKeyHex(), b.PubKeyHex()},
		Emails:  []string{email},
	}, 1_700_000_000)
	if err != nil {
		t.Fatalf("BuildAliasEvent A: %v", err)
	}
	// B (trusted transitively) binds {B, C} in a DIFFERENT (signer=B) slot.
	evB, err := BuildAliasEvent(b, AliasSpec{
		Handle:  email,
		Pubkeys: []string{b.PubKeyHex(), c.PubKeyHex()},
		Emails:  []string{email},
	}, 1_700_000_050)
	if err != nil {
		t.Fatalf("BuildAliasEvent B: %v", err)
	}

	r := Resolve([]*nostr.Event{evA, evB}, []string{a.PubKeyHex()})
	keys, ok := r.KeysForParty(email)
	if !ok {
		t.Fatalf("KeysForParty(%s) not found", email)
	}
	// All three survive: different signers = different slots, unioned by the closure.
	for _, want := range []string{a.PubKeyHex(), b.PubKeyHex(), c.PubKeyHex()} {
		if !contains(keys, want) {
			t.Fatalf("per-slot supersede wrongly evicted a cross-signer key: keys=%v missing %s", keys, want)
		}
	}
}

func sortedStrings(in []string) []string {
	out := append([]string(nil), in...)
	// tiny insertion sort to avoid importing sort in the test twice
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
