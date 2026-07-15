package nostr

// ECDH key agreement for the portfolio nostr identity.
//
// This is a byte-for-byte port of dontguess's pkg/identity/secp256k1.go ECDH +
// liftXOnlyToEvenY (github.com/3dl-dev/dontguess), which the confidential-boards
// epic (ready-216) vendors so rd's single secp256k1 key can BOTH Schnorr-sign
// (see key.go) AND do the raw-shared-X ECDH that NIP-44 v2 consumes (see
// pkg/nip44). The lift and the raw-X return are security-critical: see the
// per-function comments. Validated end-to-end by the pkg/nip44 known-answer
// vectors, which derive every conversation key THROUGH this method.

import (
	"encoding/hex"
	"fmt"

	btcec "github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// liftXOnlyToEvenY is the ONE place the BIP-340 x-only → even-Y (0x02) parity
// lift happens. A 32-byte x-only key omits the Y coordinate's sign; BIP-340
// defines its canonical point as the one with EVEN Y, and schnorr.ParsePubKey
// returns exactly that even-Y point. Every party must lift identically before
// ECDH — a divergent lift yields a different shared point and makes the
// counterparty's decryption fail SILENTLY. Confining the lift here guarantees
// they cannot diverge.
func liftXOnlyToEvenY(xOnlyHex string) (*btcec.PublicKey, error) {
	raw, err := hex.DecodeString(xOnlyHex)
	if err != nil {
		return nil, fmt.Errorf("nostr: decode counterparty x-only pubkey hex: %w", err)
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("nostr: counterparty x-only pubkey must be 32 bytes, got %d", len(raw))
	}
	// schnorr.ParsePubKey applies the BIP-340 lift_x: it rejects x ≥ field prime
	// and x values with no square-root Y (not on curve), and returns the even-Y
	// point. This is the single authoritative lift.
	pub, err := schnorr.ParsePubKey(raw)
	if err != nil {
		return nil, fmt.Errorf("nostr: lift x-only pubkey to even-Y point: %w", err)
	}
	return pub, nil
}

// ECDH performs secp256k1 key agreement against the counterparty's x-only key
// and returns the raw 32-byte big-endian X coordinate of the shared point.
//
// It multiplies our private scalar by the counterparty's even-Y point and
// returns the affine X of the product. This is the RAW shared X that NIP-44 v2
// consumes — NOT sha256(X). btcec.GenerateSharedSecret is deliberately avoided
// because it hashes the X coordinate (NIP-04/ECIES style), which is the wrong
// value for NIP-44 and would break decryption silently.
//
// The private scalar is read in place (k.priv.Key) and never returned or
// exported — this method leaks no key material.
func (k *Key) ECDH(counterpartyXOnlyHex string) ([32]byte, error) {
	var shared [32]byte
	pub, err := liftXOnlyToEvenY(counterpartyXOnlyHex)
	if err != nil {
		return shared, err
	}
	var point, product btcec.JacobianPoint
	pub.AsJacobian(&point)
	// product = privScalar · counterpartyPoint. Both operands are valid (a
	// non-zero scalar mod N and a point of prime order N), so the product is
	// never the point at infinity.
	btcec.ScalarMultNonConst(&k.priv.Key, &point, &product)
	product.ToAffine()
	shared = *product.X.Bytes()
	return shared, nil
}
