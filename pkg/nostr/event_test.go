package nostr

import (
	"strings"
	"testing"
)

// TestComputeID_KnownVector pins the NIP-01 event-id derivation against a known
// vector. The pubkey/created_at/kind/tags/content below are the canonical
// example carried in the reference nostr client (nak); the sha256 of the
// canonical [0,pubkey,created_at,kind,tags,content] array MUST equal this id.
// This is the ground truth that our canonical serialization must reproduce.
func TestComputeID_KnownVector(t *testing.T) {
	e := &Event{
		PubKey:    "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5",
		CreatedAt: 1698623783,
		Kind:      1,
		Tags:      [][]string{},
		Content:   "hello from the nostr army knife",
	}
	const want = "a889df6a387419ff204305f4c2d296ee328c3cd4f8b62f205648a541b4554dfb"
	if got := e.ComputeID(); got != want {
		t.Fatalf("event id mismatch:\n  got  %s\n  want %s", got, want)
	}
}

// TestSerialize_EscapingMatchesNIP01 verifies the exact NIP-01 string escaping:
// only " \ \n \r \t \b \f get backslash escapes, control bytes below 0x20 get
// \u00XX, and <, >, & are emitted VERBATIM (unlike encoding/json, which HTML-
// escapes them). A drift here silently changes every event id.
func TestSerialize_EscapingMatchesNIP01(t *testing.T) {
	e := &Event{
		PubKey:    "c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5",
		CreatedAt: 1,
		Kind:      1,
		Tags:      [][]string{},
		Content:   "a<b>c&d\"e\\f\ng\th",
	}
	got := string(e.canonicalForID())
	// Note content substring: quotes and backslash escaped, < > & verbatim.
	wantContent := `"a<b>c&d\"e\\f\ng\th"`
	if !strings.Contains(got, wantContent) {
		t.Fatalf("canonical content escaping wrong:\n  serialization: %s\n  want substring: %s", got, wantContent)
	}
	// Must NOT contain HTML-style \u00XX escapes for the three chars
	// encoding/json escapes (0x3c '<', 0x3e '>', 0x26 '&'); NIP-01 forbids that
	// and keeps the bytes verbatim.
	for _, bad := range []string{"\\u003c", "\\u003e", "\\u0026"} {
		if strings.Contains(got, bad) {
			t.Fatalf("serialization HTML-escaped a char (%s), NIP-01 forbids it: %s", bad, got)
		}
	}
	// And the raw 0x3c/0x3e/0x26 bytes MUST be present verbatim.
	for _, want := range []string{"\x3c", "\x3e", "\x26"} {
		if !strings.Contains(got, want) {
			t.Fatalf("serialization dropped verbatim char %q: %s", want, got)
		}
	}
	// Prefix must be the canonical array head.
	if !strings.HasPrefix(got, `[0,"c6047f9441ed7d6d3045406e95c07cd85c778e4b8cef3ca7abac09b95c709ee5",1,1,[],`) {
		t.Fatalf("canonical array head wrong: %s", got)
	}
}

// TestSignVerify_RoundTrip proves a freshly generated key can sign an event and
// that independent Verify (re-derive id + verify schnorr sig) accepts it. This
// is the ACCEPTANCE half of the acceptance/rejection pair.
func TestSignVerify_RoundTrip(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	e := &Event{
		CreatedAt: 1700000000,
		Kind:      1,
		Tags:      [][]string{{"t", "rd"}, {"client", "rd-nostr"}},
		Content:   "round-trip <>&\" line1\nline2\ttab",
	}
	if err := e.Sign(k); err != nil {
		t.Fatalf("sign: %v", err)
	}
	if e.PubKey != k.PubKeyHex() {
		t.Fatalf("Sign did not set pubkey: got %q want %q", e.PubKey, k.PubKeyHex())
	}
	if len(e.ID) != 64 {
		t.Fatalf("id not 32-byte hex: %q", e.ID)
	}
	if len(e.Sig) != 128 {
		t.Fatalf("sig not 64-byte hex: %q", e.Sig)
	}
	if err := e.Verify(); err != nil {
		t.Fatalf("Verify rejected a valid event: %v", err)
	}
}

// TestSignVerify_DeterministicVector locks the full sign path (id AND schnorr
// sig) to a byte-exact vector that was cross-checked against nak for the same
// secret/created_at/kind/tags/content. btcec's default schnorr signing is
// deterministic, so nak and this code agree byte-for-byte. If this vector ever
// changes, either the canonical serialization or the signing changed — both are
// consensus-breaking and must be caught.
func TestSignVerify_DeterministicVector(t *testing.T) {
	k, err := KeyFromHex("3cf18b1c855044728c4ade9d12a89c1cec9f1c3014d4060b18a8f59f3962d600")
	if err != nil {
		t.Fatalf("key from hex: %v", err)
	}
	e := &Event{CreatedAt: 1700000000, Kind: 1, Tags: [][]string{}, Content: "simple"}
	if err := e.Sign(k); err != nil {
		t.Fatalf("sign: %v", err)
	}
	const (
		wantPub = "edf5efbdaadd2bc2befdd977f5de050d36ded18b9973bbfd45d33dafeee52fea"
		wantID  = "c2101f08212b4b5713fd43a0b948232c5bf3f01fe017e7243a40eb9a8443fcc8"
		wantSig = "4fc7ca69db3348b9877c6539a5620866d6984334041637560791624ed4c65393" +
			"d63e3dfef3c6e148606824d79346c41c947b571782d7c9c4781b4bda69a8906d"
	)
	if e.PubKey != wantPub {
		t.Errorf("pubkey: got %s want %s", e.PubKey, wantPub)
	}
	if e.ID != wantID {
		t.Errorf("id: got %s want %s", e.ID, wantID)
	}
	if e.Sig != wantSig {
		t.Errorf("sig (must match nak byte-for-byte): got %s want %s", e.Sig, wantSig)
	}
	if err := e.Verify(); err != nil {
		t.Fatalf("Verify rejected the pinned vector: %v", err)
	}
}

// TestVerify_TamperRejection proves the REJECTION half: mutating any signed
// field of a validly-signed event makes Verify fail. Each case flips exactly one
// field and asserts a non-nil error. This is the tamper gate the relay ->
// verify loop relies on.
func TestVerify_TamperRejection(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	base := func() *Event {
		e := &Event{
			CreatedAt: 1700000000,
			Kind:      1,
			Tags:      [][]string{{"t", "rd"}},
			Content:   "authentic",
		}
		if err := e.Sign(k); err != nil {
			t.Fatalf("sign: %v", err)
		}
		return e
	}

	// Sanity: the untampered event verifies.
	if err := base().Verify(); err != nil {
		t.Fatalf("baseline event should verify: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Event)
	}{
		{"content", func(e *Event) { e.Content = "forged" }},
		{"created_at", func(e *Event) { e.CreatedAt++ }},
		{"kind", func(e *Event) { e.Kind = 2 }},
		{"tags", func(e *Event) { e.Tags = [][]string{{"t", "evil"}} }},
		{"add-tag", func(e *Event) { e.Tags = append(e.Tags, []string{"p", "x"}) }},
		{"id-only", func(e *Event) {
			// Flip one hex nibble of the id (id no longer matches recomputed).
			b := []byte(e.ID)
			if b[0] == '0' {
				b[0] = '1'
			} else {
				b[0] = '0'
			}
			e.ID = string(b)
		}},
		{"pubkey", func(e *Event) {
			other, _ := GenerateKey()
			e.PubKey = other.PubKeyHex() // id recomputes -> mismatch
		}},
		{"sig-bitflip", func(e *Event) {
			b := []byte(e.Sig)
			if b[10] == 'a' {
				b[10] = 'b'
			} else {
				b[10] = 'a'
			}
			e.Sig = string(b)
		}},
		{"sig-truncated", func(e *Event) { e.Sig = e.Sig[:len(e.Sig)-2] }},
		{"pubkey-not-hex", func(e *Event) { e.PubKey = "zz" + e.PubKey[2:] }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := base()
			tc.mutate(e)
			if err := e.Verify(); err == nil {
				t.Fatalf("Verify ACCEPTED a tampered event (%s) — tamper gate broken", tc.name)
			}
		})
	}
}

// TestVerify_ForgedSigForValidPubkey proves that a signature that is structurally
// valid but produced by a DIFFERENT key over the same message is rejected. This
// guards against a verify path that only checks structural well-formedness.
func TestVerify_ForgedSigForValidPubkey(t *testing.T) {
	victim, _ := GenerateKey()
	attacker, _ := GenerateKey()

	e := &Event{CreatedAt: 1700000000, Kind: 1, Tags: [][]string{}, Content: "as victim"}
	// Sign with the attacker's key, then claim the victim's pubkey.
	if err := e.Sign(attacker); err != nil {
		t.Fatalf("sign: %v", err)
	}
	e.PubKey = victim.PubKeyHex()
	// Recompute id so the id check passes and only the SIGNATURE check can fail.
	e.ID = e.ComputeID()

	if err := e.Verify(); err == nil {
		t.Fatal("Verify accepted a signature that does not match the claimed pubkey")
	}
}
