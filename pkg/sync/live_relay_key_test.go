package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/3dl-dev/ready/pkg/nostr"
)

// liveRelayKey returns this machine's persistent portfolio signing key for the
// live-relay proofs that need a STABLE identity rather than a throwaway one.
//
// THIS IS NO LONGER ABOUT rd's OWN RELAY WRITE-ALLOWLIST. That portfolio-wide
// strfry writePolicy (ready-266) was RETIRED and the shared LAN relays unfenced
// (ready-5fd, scripts/unlock-relays.sh; scripts/lock-relays.sh must NOT be
// re-applied). Measured 2026-07-30 and re-measured live by
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
// a perfectly good portfolio key this helper found nothing and EVERY live-relay
// proof in the package skipped silently. That is the same masking that let a
// factually false relay-write-allowlist assertion sit green in
// `go test ./...` for weeks (the ready-5fd veracity finding). Measured
// 2026-07-30 after the fix: all 14 live tests in this package RUN and pass
// against ws://192.168.2.40:7777 with no env var set beyond
// RD_NOSTR_LIVE_RELAY=1 + RD_NOSTR_RELAY_URL.
//
// If none resolve, the caller is skipped: without a stable, tenant-admitted
// identity a publish proof against a tenant-restricted relay cannot run. This
// skip hides NOTHING about the retired fence — the proof that the relays are
// open (TestLiveRelay_OpenRelayIngestionTrustGate) generates its own keys on
// purpose and never calls this helper.
func liveRelayKey(t *testing.T) *nostr.Key {
	t.Helper()
	if h := os.Getenv("RD_NOSTR_TEST_SECRET_HEX"); h != "" {
		k, err := nostr.KeyFromHex(h)
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
			candidates = append(candidates, nostr.DefaultKeyPath(home))
		}
	}
	for _, path := range candidates {
		if k, err := nostr.LoadKeyFile(path); err == nil {
			return k
		}
	}
	t.Skipf("no stable portfolio key available (tried %v): set RD_NOSTR_TEST_SECRET_HEX or RD_NOSTR_TEST_KEY_PATH. rd's own relay write-allowlist was RETIRED in ready-5fd and the LAN strfry relays are open, but a tenant-restricted relay such as wss://relay.3dl.network admits only keys its own operator has allowed, and these proofs read their own writes back so they need a stable author. The open-relay proof TestLiveRelay_OpenRelayIngestionTrustGate needs no key and does not skip.", candidates)
	return nil
}

// rdHomeCandidates lists the directories that may hold this machine's rd nostr
// identity, in the same precedence cmd/rd's resolveRDHome applies (it is not
// importable from pkg/sync — cmd/rd imports this package). The legacy campfire
// ~/.cf home is kept LAST so a pre-cutover machine still resolves.
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

// reservedProductionBoardD is defined in nostroutbound.go (production code,
// not test-only) — the REAL guard now lives on Publisher's write path
// (Publisher.Production / guardReservedBoard), which fires regardless of
// whether a test remembers to call requireIsolatedBoardD below. This helper
// remains as an early, in-test convention check (a live-relay test SHOULD fail
// fast with a clear message before even attempting to build a BoardSpec/CardSpec
// with the reserved D-tag) — belt to the write-path guard's suspenders, not the
// guard itself (ready-fce rework: the original write-path gap this closes).
//
// requireIsolatedBoardD FAILS the test immediately, before any publish call, if
// boardD is the reserved production board D-tag. Every live-relay test that
// constructs a BoardSpec/CardSpec is encouraged to route its board D-tag through
// here (directly, or via liveTestBoardD) for a fast, readable failure — but the
// actual safety property (no write ever reaches the log or a relay) is enforced
// unconditionally by Publisher.guardReservedBoard, not by this function being
// called.
func requireIsolatedBoardD(t *testing.T, boardD string) {
	t.Helper()
	if boardD == reservedProductionBoardD {
		t.Fatalf("refusing to publish to reserved production board D-tag %q — this is THIS repo's own live board (see .ready/config.json's pinned \"board\" coordinate); use liveTestBoardD(t) for an isolated per-run board instead (ready-fce guard)", boardD)
	}
}

// liveTestBoardD returns an isolated, per-run, disposable board D-tag for
// live-relay tests — never the reserved production "ready" board. See
// requireIsolatedBoardD.
func liveTestBoardD(t *testing.T) string {
	t.Helper()
	d := fmt.Sprintf("ready-livetest-%d", time.Now().UnixNano())
	requireIsolatedBoardD(t, d)
	return d
}
