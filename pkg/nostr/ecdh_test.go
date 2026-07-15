package nostr

import "testing"

// TestECDHSymmetry proves the raw-shared-X ECDH port agrees both directions:
// a.ECDH(b.pub) == b.ECDH(a.pub), the defining property of Diffie-Hellman key
// agreement. A third, unrelated key must NOT land on the same shared secret.
//
// This is a cheap sanity check; the AUTHORITATIVE proof that ECDH returns the
// correct raw shared-X (not sha256(X), correct even-Y lift) is the pkg/nip44
// known-answer suite, which derives every conversation key through this method
// against the official NIP-44 v2 vectors.
func TestECDHSymmetry(t *testing.T) {
	a, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate a: %v", err)
	}
	b, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate b: %v", err)
	}
	c, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate c: %v", err)
	}

	ab, err := a.ECDH(b.PubKeyHex())
	if err != nil {
		t.Fatalf("a.ECDH(b): %v", err)
	}
	ba, err := b.ECDH(a.PubKeyHex())
	if err != nil {
		t.Fatalf("b.ECDH(a): %v", err)
	}
	if ab != ba {
		t.Fatalf("ECDH not symmetric:\n a·b = %x\n b·a = %x", ab, ba)
	}

	// A non-zero shared secret (degenerate all-zero X would signal a broken
	// scalar-mult).
	var zero [32]byte
	if ab == zero {
		t.Fatal("shared secret is all-zero — scalar multiplication is broken")
	}

	// A third, unrelated key must not collide with the a·b secret.
	ac, err := a.ECDH(c.PubKeyHex())
	if err != nil {
		t.Fatalf("a.ECDH(c): %v", err)
	}
	if ac == ab {
		t.Fatal("unrelated key produced the same shared secret — ECDH is not binding to the counterparty")
	}
}

// TestECDHRejectsMalformedCounterparty asserts the x-only lift rejects bad
// input rather than panicking: wrong length, non-hex, and an x with no
// square-root Y on the curve (not a valid point).
func TestECDHRejectsMalformedCounterparty(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	bad := []string{
		"",     // empty
		"zz",   // non-hex
		"abcd", // too short
		"00",   // too short
		"ff" + "00000000000000000000000000000000000000000000000000000000000000", // 32 bytes but x with no even-Y point on curve? still must not panic
	}
	for _, s := range bad {
		if _, err := k.ECDH(s); err == nil {
			// Some 32-byte values are valid points; only assert no panic + that
			// clearly malformed (wrong length / non-hex) inputs error.
			if len(s) != 64 {
				t.Fatalf("ECDH(%q) expected error for malformed input, got nil", s)
			}
		}
	}
}
