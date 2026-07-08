package nostr

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	btcec "github.com/btcsuite/btcd/btcec/v2"
	"github.com/btcsuite/btcd/btcec/v2/schnorr"
)

// Key is a portfolio secp256k1 signing identity for nostr. The secret is held
// only in memory; callers persist it via SaveKeyFile. PubKeyHex returns the
// x-only (32-byte) public key nostr uses.
type Key struct {
	priv *btcec.PrivateKey
}

// GenerateKey creates a fresh random secp256k1 keypair using crypto/rand.
func GenerateKey() (*Key, error) {
	priv, err := btcec.NewPrivateKey()
	if err != nil {
		return nil, fmt.Errorf("nostr: generate key: %w", err)
	}
	return &Key{priv: priv}, nil
}

// KeyFromHex loads a key from a 32-byte lowercase-hex secret (the nostr "nsec"
// raw form). It rejects malformed or out-of-range scalars.
func KeyFromHex(secHex string) (*Key, error) {
	b, err := hex.DecodeString(secHex)
	if err != nil {
		return nil, fmt.Errorf("nostr: decode secret hex: %w", err)
	}
	if len(b) != 32 {
		return nil, fmt.Errorf("nostr: secret must be 32 bytes, got %d", len(b))
	}
	// PrivKeyFromBytes does not itself reject zero; guard explicitly so a
	// degenerate all-zero secret can never be used.
	allZero := true
	for _, x := range b {
		if x != 0 {
			allZero = false
			break
		}
	}
	if allZero {
		return nil, errors.New("nostr: secret key is zero")
	}
	priv, _ := btcec.PrivKeyFromBytes(b)
	return &Key{priv: priv}, nil
}

// SecretHex returns the 32-byte secret as lowercase hex. Handle with care — this
// is the private key. It exists so the portfolio key can be persisted and so
// tests can cross-check against external tooling (nak).
func (k *Key) SecretHex() string {
	b := k.priv.Serialize()
	return hex.EncodeToString(b)
}

// PubKeyHex returns the x-only 32-byte public key as lowercase hex, the form
// nostr events carry in their "pubkey" field.
func (k *Key) PubKeyHex() string {
	xonly := schnorr.SerializePubKey(k.priv.PubKey())
	return hex.EncodeToString(xonly)
}

// keyFile is the on-disk representation of a portfolio nostr key. It lives
// alongside the campfire identity (in CFHome()/.cf) and, like identity.json,
// carries the secret — so it must never be committed and must be written 0600.
type keyFile struct {
	Version   int    `json:"version"`
	SecretHex string `json:"secret_hex"`
	PubKeyHex string `json:"pubkey_hex"`
}

// SaveKeyFile writes the key to path as JSON with 0600 permissions, creating
// parent directories as needed. The pubkey is written for human/tooling
// convenience but is always re-derived from the secret on load.
func SaveKeyFile(path string, k *Key) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("nostr: mkdir for key file: %w", err)
	}
	kf := keyFile{Version: 1, SecretHex: k.SecretHex(), PubKeyHex: k.PubKeyHex()}
	data, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return fmt.Errorf("nostr: marshal key file: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("nostr: write key file: %w", err)
	}
	return nil
}

// LoadKeyFile reads and validates a key previously written by SaveKeyFile. The
// key is reconstructed from the secret; the on-disk pubkey is advisory only.
func LoadKeyFile(path string) (*Key, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("nostr: read key file: %w", err)
	}
	var kf keyFile
	if err := json.Unmarshal(data, &kf); err != nil {
		return nil, fmt.Errorf("nostr: parse key file: %w", err)
	}
	return KeyFromHex(kf.SecretHex)
}

// LoadOrCreatePortfolioKey loads the portfolio nostr key from path, or generates
// and persists a new one if the file does not exist. This mirrors the
// identity.json resolution pattern (pkg resolve / cmd/rd CFHome) WITHOUT touching
// the campfire ed25519 identity: the nostr secp256k1 key is a distinct file
// (default "nostr-identity.json") in the same .cf home, so existing identity
// resolution is unaffected. Real cross-machine provisioning of this key is a
// follow-up; for now first use generates a local key.
func LoadOrCreatePortfolioKey(path string) (*Key, error) {
	if _, err := os.Stat(path); err == nil {
		return LoadKeyFile(path)
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("nostr: stat key file: %w", err)
	}
	k, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := SaveKeyFile(path, k); err != nil {
		return nil, err
	}
	return k, nil
}

// DefaultKeyPath returns the conventional location of the portfolio nostr key
// given a campfire home directory (CFHome()). Kept separate from identity.json
// so it composes with existing resolution instead of overloading it.
func DefaultKeyPath(cfHome string) string {
	return filepath.Join(cfHome, "nostr-identity.json")
}
