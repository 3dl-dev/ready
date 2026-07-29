package main

// ready-f7b regression coverage: `rd board share` (and its shared body,
// runNostrGrantRevoke -> publishRoleGrant) must report the ACTUAL relay-
// delivery outcome of a role-grant instead of a blanket "granted ... they can
// read and write" — the same false-success shape ready-e0e already fixed for
// gates (runGateNostr's re-resolve-and-verify). There are exactly three
// outcomes a publish attempt can produce (pkg/sync's PublishResult, reduced
// per event):
//
//   - published and ACCEPTED by at least one relay — the confident case;
//     "granted/revoked ... they can/no longer read or write" is true.
//   - BUFFERED — reached no relay for a TRANSIENT reason (down, unreachable,
//     rate-limited...). This is NOT a failure: the signed event is already
//     durable in the local authoritative log, and nostr-pending.jsonl will
//     flush it automatically once a relay is reachable — the deliberate
//     offline-durability design this fix must NOT break. But it must be
//     reported AS buffered, not as plain success: nobody reading from a
//     relay (every OTHER machine/teammate) can see it yet.
//   - REJECTED — a relay PERMANENTLY refused the event (malformed/
//     disallowed); it is dead-lettered to nostr-rejected.jsonl and will NEVER
//     be retried. This is a genuine failure: publishRoleGrant now returns a
//     non-nil error so the command fails loudly (non-zero exit, no board URL
//     printed) instead of reporting success.
//
// Each test below drives a REAL publish attempt through the REAL cmd/rd
// commands against a REAL relay configuration — an in-process fake relay for
// the accepted/rejected states, and the unreachable-URL harness
// setupNostrCmdTest already uses (unreachableRelayURL, nostr_test.go) for the
// buffered state. Nothing here mocks PublishResult or fakes the
// classification; every outcome is produced by an actual network round-trip.

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/3dl-dev/ready/pkg/nostr"
	rdSync "github.com/3dl-dev/ready/pkg/sync"
	"github.com/gorilla/websocket"
)

// rejectingRoleGrantRelay is a fake in-process relay that PERMANENTLY rejects
// every event it receives with an "invalid: ..." OK message — the exact
// prefix classifyRelayResult (pkg/sync/relayclass.go) treats as permanent, not
// transient. A publish against it deterministically drives
// PublishResult.Rejected, the one outcome the unreachable-URL harness cannot
// produce (a failed dial is always transient/Buffered, never Permanent).
func rejectingRoleGrantRelay(t *testing.T) string {
	t.Helper()
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			_, data, err := conn.ReadMessage()
			if err != nil {
				return
			}
			var frame []json.RawMessage
			if json.Unmarshal(data, &frame) != nil || len(frame) < 2 {
				continue
			}
			var typ string
			_ = json.Unmarshal(frame[0], &typ)
			if typ != "EVENT" {
				continue
			}
			var ev struct {
				ID string `json:"id"`
			}
			_ = json.Unmarshal(frame[1], &ev)
			_ = conn.WriteJSON([]any{"OK", ev.ID, false, "invalid: test relay rejects every role-grant"})
		}
	}))
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// storedEvents is a test-only accessor onto storingRelay (defined in
// confidential_selfheal_test.go, same package) so the "accepted" case below
// can prove the grant actually reached the relay, not merely that the command
// returned no error.
func (r *storingRelay) storedEvents() []*nostr.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*nostr.Event(nil), r.events...)
}

// resetBoardShareFlags restores boardShareCmd's flags to their registered
// defaults before a test runs: the flag set lives on a package-level
// *cobra.Command shared by every test in this package, so an earlier test's
// Set() otherwise leaks in (board_test.go's own tests take the same
// precaution for --host).
func resetBoardShareFlags(t *testing.T) {
	t.Helper()
	for _, kv := range [][2]string{
		{"host", ""}, {"role", rdSync.RoleContributor}, {"label", ""},
	} {
		if err := boardShareCmd.Flags().Set(kv[0], kv[1]); err != nil {
			t.Fatalf("reset --%s: %v", kv[0], err)
		}
	}
}

// TestBoardShareCmd_RelayRejected_FailsLoudly is the GENUINE-FAILURE half of
// ready-f7b's done condition: when the role-grant is PERMANENTLY rejected by
// every relay it reaches, `rd board share <pubkey>` must fail loudly (non-nil
// error) and must NOT print the board URL — handing out a link after
// silently failing to grant anything would defeat the fix.
func TestBoardShareCmd_RelayRejected_FailsLoudly(t *testing.T) {
	dir, _ := setupNostrNativeProject(t)
	t.Setenv("RD_NOSTR_RELAY_URL", rejectingRoleGrantRelay(t))
	resetBoardShareFlags(t)

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	var runErr error
	out := captureStdoutPipe(t, func() {
		runErr = boardShareCmd.RunE(boardShareCmd, []string{grantee.PubKeyHex()})
	})
	if runErr == nil {
		t.Fatalf("rd board share against a permanently-rejecting relay returned nil error — a genuine failure must fail loudly, not report success")
	}
	msg := runErr.Error()
	if !strings.Contains(msg, "rejected") {
		t.Errorf("error %q does not mention the rejection", msg)
	}
	if strings.Contains(out, "https://") {
		t.Errorf("output printed a board URL despite the grant failing:\n%s", out)
	}

	// The event is durable in the local log AND dead-lettered — never silently
	// lost, never silently retried.
	rejectedPath := filepath.Join(dir, ".ready", rdSync.NostrRejectedFile)
	data, rerr := os.ReadFile(rejectedPath)
	if rerr != nil {
		t.Fatalf("reading dead-letter file %s: %v", rejectedPath, rerr)
	}
	if !strings.Contains(string(data), grantee.PubKeyHex()) {
		t.Errorf("dead-letter file %s does not contain the rejected grantee %s:\n%s", rejectedPath, grantee.PubKeyHex(), data)
	}
}

// TestBoardShareCmd_RelayUnreachable_ReportsBufferedHonestly is the BUFFERED
// half: setupNostrNativeProject (via setupNostrCmdTest) already points
// RD_NOSTR_RELAY_URL at unreachableRelayURL, so this test drives the exact
// harness ready-f7b's done condition names, with no override needed. Buffered
// is NOT a failure — the signed grant is durable locally and will flush
// automatically — so the command must still succeed and still print the board
// URL, but the message must say plainly that the grant reached no relay yet
// and must NOT claim the unqualified "they can read and write".
func TestBoardShareCmd_RelayUnreachable_ReportsBufferedHonestly(t *testing.T) {
	dir, owner := setupNostrNativeProject(t)
	resetBoardShareFlags(t)

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	var runErr error
	out := captureStdoutPipe(t, func() {
		runErr = boardShareCmd.RunE(boardShareCmd, []string{grantee.PubKeyHex()})
	})
	if runErr != nil {
		t.Fatalf("rd board share buffered by an unreachable relay should still succeed (durable locally, will flush): %v", runErr)
	}
	if !strings.Contains(out, "reached NO relay yet") || !strings.Contains(out, "buffered") {
		t.Errorf("output does not honestly report the buffered state:\n%s", out)
	}
	if strings.Contains(out, "they can read and write") {
		t.Errorf("output claims unqualified success (\"they can read and write\") for a grant that reached no relay:\n%s", out)
	}
	if !strings.Contains(out, "https://") {
		t.Errorf("output should still print the board URL on the buffered (non-failure) path:\n%s", out)
	}

	// The grant IS durable locally regardless of relay reachability — the
	// invariant that makes "buffered, not failed" the correct report.
	boardD := projectPrefix(dir)
	events, everr := rdSync.NewNostrLog(rdSync.NostrLogPath(dir)).ReadAll()
	if everr != nil {
		t.Fatalf("reading local log: %v", everr)
	}
	if !rdSync.InviteGrantValid(events, owner, boardD, grantee.PubKeyHex()) {
		t.Error("the buffered grant did not land durably in the local authoritative log")
	}
}

// TestBoardShareCmd_RelayAccepted_PlainSuccess is the CONFIDENT half: when a
// relay actually ACCEPTS the role-grant, the original "granted ... they can
// read and write" message is correct and must still be printed — this test
// guards against ready-f7b's fix over-correcting into ALWAYS hedging.
func TestBoardShareCmd_RelayAccepted_PlainSuccess(t *testing.T) {
	relay := newStoringRelay(t)
	t.Cleanup(relay.close)
	setupNostrNativeProject(t)
	t.Setenv("RD_NOSTR_RELAY_URL", relay.url())
	resetBoardShareFlags(t)

	grantee, err := nostr.GenerateKey()
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}

	var runErr error
	out := captureStdoutPipe(t, func() {
		runErr = boardShareCmd.RunE(boardShareCmd, []string{grantee.PubKeyHex()})
	})
	if runErr != nil {
		t.Fatalf("rd board share against an accepting relay: %v", runErr)
	}
	if !strings.Contains(out, "they can read and write") {
		t.Errorf("output does not report plain success on an accepted grant:\n%s", out)
	}
	if strings.Contains(out, "buffered") || strings.Contains(out, "reached NO relay yet") {
		t.Errorf("output hedges even though the relay accepted the grant:\n%s", out)
	}

	found := false
	for _, ev := range relay.storedEvents() {
		if ev.Kind == rdSync.KindRoleGrant {
			found = true
		}
	}
	if !found {
		t.Error("the role-grant event never actually reached the relay — the test fixture is not proving what it claims")
	}
}
