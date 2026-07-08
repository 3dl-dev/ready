package nostr

import (
	"os"
	"path/filepath"
	"testing"
)

func TestKeyFromHex_RejectsMalformed(t *testing.T) {
	cases := []string{
		"",               // empty
		"zz",             // not hex
		"00",             // wrong length
		strings32Zeros(), // zero scalar
	}
	for _, c := range cases {
		if _, err := KeyFromHex(c); err == nil {
			t.Errorf("KeyFromHex(%q) should have failed", c)
		}
	}
}

func strings32Zeros() string {
	b := make([]byte, 64)
	for i := range b {
		b[i] = '0'
	}
	return string(b)
}

func TestKeyRoundTrip_Hex(t *testing.T) {
	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	k2, err := KeyFromHex(k.SecretHex())
	if err != nil {
		t.Fatalf("from hex: %v", err)
	}
	if k.SecretHex() != k2.SecretHex() {
		t.Fatalf("secret round-trip mismatch")
	}
	if k.PubKeyHex() != k2.PubKeyHex() {
		t.Fatalf("pubkey round-trip mismatch")
	}
	if len(k.PubKeyHex()) != 64 {
		t.Fatalf("x-only pubkey must be 32-byte hex, got %q", k.PubKeyHex())
	}
}

func TestSaveLoadKeyFile_PermsAndRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nostr-identity.json")

	k, err := GenerateKey()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if err := SaveKeyFile(path, k); err != nil {
		t.Fatalf("save: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("key file perms = %o, want 0600 (secret must not be world-readable)", perm)
	}

	loaded, err := LoadKeyFile(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if loaded.SecretHex() != k.SecretHex() {
		t.Fatalf("loaded secret mismatch")
	}
}

func TestLoadOrCreatePortfolioKey_GeneratesThenLoads(t *testing.T) {
	dir := t.TempDir()
	path := DefaultKeyPath(dir)

	k1, err := LoadOrCreatePortfolioKey(path)
	if err != nil {
		t.Fatalf("first call (generate): %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("key file not persisted: %v", err)
	}

	k2, err := LoadOrCreatePortfolioKey(path)
	if err != nil {
		t.Fatalf("second call (load): %v", err)
	}
	if k1.SecretHex() != k2.SecretHex() {
		t.Fatalf("LoadOrCreatePortfolioKey regenerated instead of loading existing key")
	}
}
