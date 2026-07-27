package sync

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

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

// reservedProductionBoardD is THIS repo's own live board D-tag. .ready/config.json
// pins this project's board coordinate to "30301:<owner>:ready" — the exact
// coordinate liveRelayKey's identity (the real portfolio key) maintains in
// production. A live-relay test that signs with liveRelayKey and addresses
// BoardD "ready" therefore publishes DIRECTLY onto the real "ready" board:
// `rd ready --view work` then shows the test's disposable fixture cards (and, for
// role-grant tests, actually grants a throwaway test key a role on the real
// board) as if they were genuine work (ready-fce).
const reservedProductionBoardD = "ready"

// requireIsolatedBoardD is the ready-fce guard: it FAILS the test immediately,
// before any publish call, if boardD is the reserved production board D-tag.
// Every live-relay test that constructs a BoardSpec/CardSpec must route its
// board D-tag through here (directly, or via liveTestBoardD) so a regression
// back to BoardD: "ready" breaks the test loudly instead of silently writing to
// the live board again.
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
