package nostr

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

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

// cfHomeDirName is the conventional campfire home directory name. It is
// git-ignored (see .gitignore's blanket "`.cf/`" entry), so any path with an
// ancestor directory literally named this is safe from accidental commit.
const cfHomeDirName = ".cf"

// requireUnderCFHome guards against SaveKeyFile being pointed at an arbitrary,
// potentially git-tracked location. It rejects path unless one of its resolved
// ancestor directories is literally named ".cf" — the same directory name the
// repo's .gitignore blanket-ignores. This does not resolve symlinks (a
// symlink-swap TOCTOU against this lexical check is a separate, still-open
// hardening concern, distinct from the create-or-load race ready-53f fixes
// below); it only checks the lexical path structure.
func requireUnderCFHome(path string) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("nostr: resolve key path: %w", err)
	}
	for dir := filepath.Dir(abs); ; {
		if filepath.Base(dir) == cfHomeDirName {
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return fmt.Errorf("nostr: refusing to write key file to %q: path has no %q ancestor directory, so it could end up in a git-tracked location; pass a path under the campfire home instead", path, cfHomeDirName)
}

// SaveKeyFile writes the key to path as JSON with 0600 permissions, creating
// parent directories as needed. The pubkey is written for human/tooling
// convenience but is always re-derived from the secret on load.
//
// path must resolve to a location under a directory named ".cf" (the
// campfire home, which is git-ignored) — see requireUnderCFHome. This
// guards against a caller accidentally persisting the secret to a
// git-tracked location.
func SaveKeyFile(path string, k *Key) error {
	if err := requireUnderCFHome(path); err != nil {
		return err
	}
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
//
// Concurrency: the naive "os.Stat then SaveKeyFile" sequence has a TOCTOU
// race — two concurrent first-time callers can both observe the file missing,
// both generate a *different* key, and the second SaveKeyFile silently
// overwrites the first, leaving the two callers holding mismatched identities.
// To close that race, the create path uses os.O_CREATE|os.O_EXCL: exactly one
// caller (per process, and across processes on POSIX filesystems since
// O_EXCL is atomic at the OS level) wins the exclusive create and writes its
// generated key; every other concurrent caller gets EEXIST and instead reads
// back whatever the winner wrote, so all callers converge on one identical
// key and an existing key is never overwritten.
func LoadOrCreatePortfolioKey(path string) (*Key, error) {
	if err := requireUnderCFHome(path); err != nil {
		return nil, err
	}

	// Fast path: the key already exists (the common case after first use).
	if k, err := loadKeyFileIfExists(path); err != nil {
		return nil, err
	} else if k != nil {
		return k, nil
	}

	k, err := GenerateKey()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("nostr: mkdir for key file: %w", err)
	}
	kf := keyFile{Version: 1, SecretHex: k.SecretHex(), PubKeyHex: k.PubKeyHex()}
	data, err := json.MarshalIndent(kf, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("nostr: marshal key file: %w", err)
	}

	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		if os.IsExist(err) {
			// Another concurrent caller won the race and created the file
			// first (or is in the middle of writing it). Converge on
			// whatever it wrote instead of overwriting it.
			return waitForKeyFile(path)
		}
		return nil, fmt.Errorf("nostr: create key file: %w", err)
	}
	defer f.Close()
	if _, err := f.Write(data); err != nil {
		return nil, fmt.Errorf("nostr: write key file: %w", err)
	}
	return k, nil
}

// loadKeyFileIfExists returns (nil, nil) when path does not exist, so callers
// can distinguish "not created yet" from a real read/parse error.
func loadKeyFileIfExists(path string) (*Key, error) {
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("nostr: stat key file: %w", err)
	}
	k, err := LoadKeyFile(path)
	if err != nil {
		return nil, err
	}
	return k, nil
}

// waitForKeyFile is used by the loser of the O_CREATE|O_EXCL race: the
// winner's file is guaranteed to exist but may not be fully written yet
// (create and write are two separate syscalls), so retry the load briefly
// instead of racing on a partial file.
func waitForKeyFile(path string) (*Key, error) {
	const (
		attempts = 100
		delay    = 2 * time.Millisecond
	)
	var lastErr error
	for i := 0; i < attempts; i++ {
		k, err := LoadKeyFile(path)
		if err == nil {
			return k, nil
		}
		lastErr = err
		time.Sleep(delay)
	}
	return nil, fmt.Errorf("nostr: timed out waiting for concurrently-created key file %q: %w", path, lastErr)
}

// DefaultKeyPath returns the conventional location of the portfolio nostr key
// given a campfire home directory (CFHome()). Kept separate from identity.json
// so it composes with existing resolution instead of overloading it.
func DefaultKeyPath(cfHome string) string {
	return filepath.Join(cfHome, "nostr-identity.json")
}
