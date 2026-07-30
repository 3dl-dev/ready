package nostr

import (
	"os"
	"path/filepath"
	"testing"
)

// liveRelayKey returns this machine's persistent portfolio signing key for the
// live-relay proofs in this package that need a STABLE identity rather than a
// throwaway one (TestLiveRelay_PublishFetchVerifyTamper reads its own write
// back; TestLiveRelay_Negentropy reconciles against its own published set).
//
// THIS IS NO LONGER ABOUT rd's OWN RELAY WRITE-ALLOWLIST. That portfolio-wide
// strfry writePolicy (ready-266) was RETIRED and the shared LAN relays unfenced
// (ready-5fd, scripts/unlock-relays.sh; scripts/lock-relays.sh must NOT be
// re-applied). Measured 2026-07-30 and re-measured live by pkg/sync's
// TestLiveRelay_OpenRelayIngestionTrustGate: ws://192.168.2.40:7777 and
// ws://192.168.2.41:7777 accept a write from a freshly generated, never-granted
// key. What remains is that a configured relay may enforce a policy of ITS OWN
// that rd neither owns nor controls — wss://relay.3dl.network answers
// "restricted: pubkey is not admitted to this relay's tenant write-allowlist" —
// so a live proof pointed at such a relay still needs a key that relay's
// operator has admitted, and every proof that reads its own writes back wants a
// stable author either way.
//
// Resolution order:
//  1. RD_NOSTR_TEST_SECRET_HEX — 32-byte hex secret (CI/other hosts).
//  2. RD_NOSTR_TEST_KEY_PATH   — path to a SaveKeyFile-format key file.
//  3. the rd home's nostr-identity.json — this machine's persistent portfolio
//     key, materialized by rd. Resolved over the SAME candidate homes cmd/rd's
//     resolveRDHome uses ($RD_HOME, then $XDG_CONFIG_HOME/rd, then
//     ~/.config/rd), plus the legacy campfire ~/.cf home last.
//
// THE HOME LIST MATTERS. Until ready-5fd this helper looked ONLY at
// ~/.cf/nostr-identity.json — the pre-XDG campfire home. rd has stored its
// identity under ~/.config/rd since the nostr cutover, so on a machine that has
// a perfectly good portfolio key this helper found nothing and BOTH live-relay
// proofs in this package skipped silently, citing the retired ready-266 fence
// as the reason. That is the same masking the ready-5fd veracity finding named
// in pkg/sync — this package held a byte-for-byte copy of it, and fixing only
// the pkg/sync instance left these two proofs dark. Measured 2026-07-30 after
// the fix: both live tests in this package RUN and pass against
// ws://192.168.2.40:7777 with no env var set beyond RD_NOSTR_LIVE_RELAY=1 +
// RD_NOSTR_RELAY_URL.
//
// If none resolve, the caller is skipped for a precondition that is ACTUALLY
// missing: this machine has no rd identity materialized and none was supplied,
// so there is no stable author to publish under and read back.
func liveRelayKey(t *testing.T) *Key {
	t.Helper()
	if h := os.Getenv("RD_NOSTR_TEST_SECRET_HEX"); h != "" {
		k, err := KeyFromHex(h)
		if err != nil {
			t.Fatalf("RD_NOSTR_TEST_SECRET_HEX: %v", err)
		}
		return k
	}
	var candidates []string
	if path := os.Getenv("RD_NOSTR_TEST_KEY_PATH"); path != "" {
		candidates = append(candidates, path)
	} else {
		for _, home := range rdHomeCandidates() {
			candidates = append(candidates, DefaultKeyPath(home))
		}
	}
	for _, path := range candidates {
		if k, err := LoadKeyFile(path); err == nil {
			return k
		}
	}
	t.Skipf("no stable portfolio key available: RD_NOSTR_TEST_SECRET_HEX is unset and no readable rd nostr identity was found at any candidate path %v. These proofs publish and then read their own writes back, so they need a stable author; run `rd init` to materialize one, or set RD_NOSTR_TEST_SECRET_HEX / RD_NOSTR_TEST_KEY_PATH. NOT a relay-fence skip: rd's portfolio-wide relay write-allowlist (ready-266) was RETIRED in ready-5fd and the LAN strfry relays accept never-granted keys.", candidates)
	return nil
}

// rdHomeCandidates lists the directories that may hold this machine's rd nostr
// identity, in the same precedence cmd/rd's resolveRDHome applies (it is not
// importable here — cmd/rd imports this package). The legacy campfire ~/.cf
// home is kept LAST so a pre-cutover machine still resolves. Mirrors the
// identical helper in pkg/sync/live_relay_key_test.go; the two are separate
// packages' test binaries and neither can import the other's test-only code.
func rdHomeCandidates() []string {
	var homes []string
	if env := os.Getenv("RD_HOME"); env != "" {
		homes = append(homes, env)
	}
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		homes = append(homes, filepath.Join(xdg, "rd"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		homes = append(homes, filepath.Join(home, ".config", "rd"), filepath.Join(home, ".cf"))
	}
	return homes
}
