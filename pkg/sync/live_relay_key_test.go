package sync

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// liveRelayKey returns the ALLOWLISTED portfolio signing key the locked strfry
// relays accept (ready-266). After the relay write-allowlist lockdown a relay
// REJECTS any event whose author pubkey is not admitted, so the live-relay
// publish proofs must sign with an admitted key rather than a throwaway one.
//
// Resolution order:
//  1. RD_NOSTR_TEST_SECRET_HEX — 32-byte hex secret of an admitted key (CI/other hosts).
//  2. RD_NOSTR_TEST_KEY_PATH   — path to a SaveKeyFile-format key file.
//  3. $HOME/.cf/nostr-identity.json — this machine's persistent portfolio key
//     (materialized by rd; the workshop VM's key is on the relay allowlist).
//
// If none resolve, the test is skipped: a write-allowlisted relay cannot be
// exercised for a publish proof without an admitted key.
func liveRelayKey(t *testing.T) *nostr.Key {
	t.Helper()
	if h := os.Getenv("RD_NOSTR_TEST_SECRET_HEX"); h != "" {
		k, err := nostr.KeyFromHex(h)
		if err != nil {
			t.Fatalf("RD_NOSTR_TEST_SECRET_HEX: %v", err)
		}
		return k
	}
	path := os.Getenv("RD_NOSTR_TEST_KEY_PATH")
	if path == "" {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, ".cf", "nostr-identity.json")
		}
	}
	if path != "" {
		if k, err := nostr.LoadKeyFile(path); err == nil {
			return k
		}
	}
	t.Skip("no allowlisted portfolio key available: set RD_NOSTR_TEST_SECRET_HEX or RD_NOSTR_TEST_KEY_PATH (the write-allowlisted relays reject non-admitted keys; ready-266)")
	return nil
}
